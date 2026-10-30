//go:build unix

package main

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestDatabaseFilesArePrivate(t *testing.T) {
	// Start from a common default umask, then do what main does. The umask
	// is per process, so put the runner's back afterwards.
	orig := syscall.Umask(0o022)
	t.Cleanup(func() { syscall.Umask(orig) })
	if old := privateUmask(); old != 0o022 {
		t.Fatalf("privateUmask returned %o, want the previous mask 022", old)
	}

	dataDir := t.TempDir()
	a, err := newApp(t.Context(), testConfig(t, dataDir), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()
	if _, err := a.store.Writer().ExecContext(t.Context(), `INSERT INTO settings (key, value, updated_at) VALUES ('k', '1', 0)`); err != nil {
		t.Fatal(err)
	}

	// -wal and -shm exist only while the database is open.
	db := filepath.Join(dataDir, "chadash.db")
	hist := filepath.Join(dataDir, "history.db")
	for _, path := range []string{db, db + "-wal", db + "-shm", hist, hist + "-wal", hist + "-shm"} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != 0o600 {
			t.Errorf("%s mode = %o, want 600", filepath.Base(path), got)
		}
	}
}
