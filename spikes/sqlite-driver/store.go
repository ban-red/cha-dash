package sqlitedriver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/pressly/goose/v3"
)

// ReadPoolSize matches internal/store.
const ReadPoolSize = 4

// Store mirrors internal/store.Store: a single-connection writer pool and a
// query_only read pool, opened through one Variant.
type Store struct {
	W, R *sql.DB
}

// Open mirrors store.Open: create the file 0600, open the writer, check WAL,
// open the readers, ping.
func Open(ctx context.Context, v Variant, path string, key []byte) (*Store, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	f.Close()

	w, err := v.Open(path, false, key)
	if err != nil {
		return nil, fmt.Errorf("open writer: %w", err)
	}
	w.SetMaxOpenConns(1)
	w.SetMaxIdleConns(1)
	w.SetConnMaxLifetime(0)
	w.SetConnMaxIdleTime(0)

	var mode string
	if err := w.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
		w.Close()
		return nil, err
	}
	if !strings.EqualFold(mode, "wal") {
		w.Close()
		return nil, fmt.Errorf("journal_mode is %q, want wal", mode)
	}

	r, err := v.Open(path, true, key)
	if err != nil {
		w.Close()
		return nil, fmt.Errorf("open readers: %w", err)
	}
	r.SetMaxOpenConns(ReadPoolSize)
	r.SetMaxIdleConns(ReadPoolSize)
	if err := r.PingContext(ctx); err != nil {
		w.Close()
		r.Close()
		return nil, err
	}
	return &Store{W: w, R: r}, nil
}

// WriteTx mirrors store.WriteTx.
func (s *Store) WriteTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	return tx.Commit()
}

// Close closes both pools.
func (s *Store) Close() error { return errors.Join(s.R.Close(), s.W.Close()) }

// Migrate applies the hub's own migrations with goose, as store.Migrate does.
func (s *Store) Migrate(ctx context.Context) (int, error) {
	dir, err := MigrationsDir()
	if err != nil {
		return 0, err
	}
	p, err := goose.NewProvider(goose.DialectSQLite3, s.W, os.DirFS(dir), goose.WithDisableGlobalRegistry(true))
	if err != nil {
		return 0, err
	}
	res, err := p.Up(ctx)
	return len(res), err
}

// MigrationsDir finds the hub's migrations, internal/store/migrations, by
// walking up from the working directory, so the spike always runs the
// schema the hub ships rather than a copy. CHADASH_SPIKE_MIGRATIONS
// overrides it.
func MigrationsDir() (string, error) {
	if d := os.Getenv("CHADASH_SPIKE_MIGRATIONS"); d != "" {
		return d, nil
	}
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		cand := filepath.Join(dir, "internal", "store", "migrations")
		if fi, err := os.Stat(cand); err == nil && fi.IsDir() {
			return cand, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("internal/store/migrations not found above the working directory: %w", fs.ErrNotExist)
		}
		dir = parent
	}
}
