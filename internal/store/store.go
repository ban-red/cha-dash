// Package store owns the SQLite database: connections, pragmas and schema
// migrations. Domain packages (auth, vault, …) write their own queries against
// the tables defined in migrations/.
//
// SQLite allows one writer at a time, so the store keeps a single-connection
// writer pool (writes queue in Go instead of failing with SQLITE_BUSY) and a
// small query-only read pool. WAL mode lets readers run while a write is in
// progress. driver_ncruces.go is the only file that knows which SQLite driver
// that is.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"

	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrations embed.FS

// historyMigrations are history.db's schema (internal/history, ADR-0031).
//
//go:embed history/*.sql
var historyMigrations embed.FS

const readPoolSize = 4

// readerIdle closes a reader that has sat idle this long. An ncruces
// connection keeps its memory high-water mark (about 6 MiB after a 1 MiB
// layout) until it closes, and reopening one costs about 0.3 ms.
const readerIdle = time.Minute

// Store is an open hub database.
type Store struct {
	w   *sql.DB // exactly one connection
	r   *sql.DB // query_only connections
	mig fs.FS   // its migrations
}

// Open opens (creating if needed) the database at path. Call Migrate before use.
func Open(ctx context.Context, path string) (*Store, error) {
	mig, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return nil, err
	}
	return open(ctx, path, mig, nil)
}

// OpenHistory opens (creating if needed) history.db, the metrics history
// (internal/history). It's a database of its own, so history writes never
// queue behind the hub's, and deleting it loses nothing else. A new file
// gets incremental auto-vacuum, so retention gives space back without a full
// VACUUM. Call Migrate before use.
func OpenHistory(ctx context.Context, path string) (*Store, error) {
	mig, err := fs.Sub(historyMigrations, "history")
	if err != nil {
		return nil, err
	}
	// This only takes effect before the first table exists.
	return open(ctx, path, mig, []string{"PRAGMA auto_vacuum = INCREMENTAL"})
}

func open(ctx context.Context, path string, mig fs.FS, fresh []string) (*Store, error) {
	if strings.ContainsRune(path, '?') {
		return nil, fmt.Errorf("store: database path %q must not contain '?'", path)
	}
	// Create the file ourselves so it is private: it holds password hashes and
	// wrapped secrets. The driver gives -wal the same mode (see dsn); -shm
	// relies on the process umask.
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	f.Close()

	w, err := openDB(path, false)
	if err != nil {
		return nil, fmt.Errorf("store: open writer: %w", err)
	}
	w.SetMaxOpenConns(1)
	w.SetMaxIdleConns(1)
	w.SetConnMaxLifetime(0)
	w.SetConnMaxIdleTime(0)

	for _, q := range fresh {
		if _, err := w.ExecContext(ctx, q); err != nil {
			w.Close()
			return nil, fmt.Errorf("store: %w", err)
		}
	}
	var mode string
	if err := w.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
		w.Close()
		return nil, fmt.Errorf("store: %w", err)
	}
	if !strings.EqualFold(mode, "wal") {
		w.Close()
		return nil, fmt.Errorf("store: journal_mode is %q, want wal", mode)
	}

	r, err := openDB(path, true)
	if err != nil {
		w.Close()
		return nil, fmt.Errorf("store: open readers: %w", err)
	}
	r.SetMaxOpenConns(readPoolSize)
	r.SetMaxIdleConns(readPoolSize)
	r.SetConnMaxIdleTime(readerIdle)
	if err := r.PingContext(ctx); err != nil {
		w.Close()
		r.Close()
		return nil, fmt.Errorf("store: %w", err)
	}
	return &Store{w: w, r: r, mig: mig}, nil
}

// Writer returns the single-connection write pool. Never issue a query on it
// while holding one of its transactions: with one connection that deadlocks.
func (s *Store) Writer() *sql.DB { return s.w }

// Reader returns the query-only read pool.
func (s *Store) Reader() *sql.DB { return s.r }

// WriteTx runs fn in an immediate transaction on the writer, committing if fn
// returns nil.
func (s *Store) WriteTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	return tx.Commit()
}

// Ping checks both pools, for /healthz.
func (s *Store) Ping(ctx context.Context) error {
	return errors.Join(s.w.PingContext(ctx), s.r.PingContext(ctx))
}

// Close closes both pools and checkpoints the WAL.
func (s *Store) Close() error {
	return errors.Join(s.r.Close(), s.w.Close())
}

// Migration describes one applied migration.
type Migration struct {
	Version  int64
	Name     string
	Duration time.Duration
}

func (s *Store) provider() (*goose.Provider, error) {
	// Provider (not the global goose API) keeps migrations free of package state.
	// Don't call Provider.Close: it would close our writer pool.
	return goose.NewProvider(goose.DialectSQLite3, s.w, s.mig, goose.WithDisableGlobalRegistry(true))
}

// Migrate applies all pending migrations and reports what it applied.
func (s *Store) Migrate(ctx context.Context) ([]Migration, error) {
	p, err := s.provider()
	if err != nil {
		return nil, fmt.Errorf("store: migrations: %w", err)
	}
	results, err := p.Up(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: migrate: %w", err)
	}
	applied := make([]Migration, 0, len(results))
	for _, r := range results {
		applied = append(applied, Migration{Version: r.Source.Version, Name: r.Source.Path, Duration: r.Duration})
	}
	return applied, nil
}

// MigrateTo migrates up or down to exactly version. It's for tests that
// check a migration against data written at an older version.
func (s *Store) MigrateTo(ctx context.Context, version int64) error {
	p, err := s.provider()
	if err != nil {
		return fmt.Errorf("store: migrations: %w", err)
	}
	cur, err := p.GetDBVersion(ctx)
	if err != nil {
		return fmt.Errorf("store: migrate: %w", err)
	}
	switch {
	case version > cur:
		_, err = p.UpTo(ctx, version)
	case version < cur:
		_, err = p.DownTo(ctx, version)
	}
	if err != nil {
		return fmt.Errorf("store: migrate to %d: %w", version, err)
	}
	return nil
}

// SchemaVersion returns the highest applied migration version.
func (s *Store) SchemaVersion(ctx context.Context) (int64, error) {
	p, err := s.provider()
	if err != nil {
		return 0, err
	}
	return p.GetDBVersion(ctx)
}

// NowMillis is the timestamp format used in every table.
func NowMillis() int64 { return time.Now().UnixMilli() }
