// Package store owns the SQLite database: connections, pragmas and schema
// migrations. Domain packages (auth, vault, …) write their own queries against
// the tables defined in migrations/.
//
// SQLite allows one writer at a time, so the store keeps a single-connection
// writer pool (writes queue in Go instead of failing with SQLITE_BUSY) and a
// small query-only read pool. WAL mode lets readers run while a write is in
// progress.
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

const readPoolSize = 4

// Store is an open hub database.
type Store struct {
	w *sql.DB // exactly one connection
	r *sql.DB // query_only connections
}

// Open opens (creating if needed) the database at path. Call Migrate before use.
func Open(ctx context.Context, path string) (*Store, error) {
	if strings.ContainsRune(path, '?') {
		return nil, fmt.Errorf("store: database path %q must not contain '?'", path)
	}
	// Create the file ourselves so it is private: it holds password hashes and
	// wrapped secrets. SQLite gives the -wal and -shm files the same mode.
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	f.Close()

	w, err := sql.Open(driverName, dsn(path, false))
	if err != nil {
		return nil, fmt.Errorf("store: open writer: %w", err)
	}
	w.SetMaxOpenConns(1)
	w.SetMaxIdleConns(1)
	w.SetConnMaxLifetime(0)
	w.SetConnMaxIdleTime(0)

	var mode string
	if err := w.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
		w.Close()
		return nil, fmt.Errorf("store: %w", err)
	}
	if !strings.EqualFold(mode, "wal") {
		w.Close()
		return nil, fmt.Errorf("store: journal_mode is %q, want wal", mode)
	}

	r, err := sql.Open(driverName, dsn(path, true))
	if err != nil {
		w.Close()
		return nil, fmt.Errorf("store: open readers: %w", err)
	}
	r.SetMaxOpenConns(readPoolSize)
	r.SetMaxIdleConns(readPoolSize)
	if err := r.PingContext(ctx); err != nil {
		w.Close()
		r.Close()
		return nil, fmt.Errorf("store: %w", err)
	}
	return &Store{w: w, r: r}, nil
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
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return nil, err
	}
	// Provider (not the global goose API) keeps migrations free of package state.
	// Don't call Provider.Close: it would close our writer pool.
	return goose.NewProvider(goose.DialectSQLite3, s.w, sub, goose.WithDisableGlobalRegistry(true))
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
