package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func openTemp(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "chadash.db")
	s, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if _, err := s.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	return s, path
}

func tables(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), `SELECT name FROM sqlite_schema WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		names = append(names, n)
	}
	return names
}

func TestMigrateUp(t *testing.T) {
	s, path := openTemp(t)
	ctx := t.Context()

	want := []string{"audit", "boards", "goose_db_version", "integration_instances", "layouts", "sessions", "settings", "users", "vault_secrets"}
	if got := tables(t, s.Reader()); !slices.Equal(got, want) {
		t.Fatalf("tables = %v, want %v", got, want)
	}
	v, err := s.SchemaVersion(ctx)
	if err != nil || v != 1 {
		t.Fatalf("SchemaVersion = %d, %v", v, err)
	}

	// Migrating again is a no-op.
	applied, err := s.Migrate(ctx)
	if err != nil || len(applied) != 0 {
		t.Fatalf("second Migrate applied %v, %v", applied, err)
	}

	var mode string
	if err := s.Reader().QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal_mode = %q, %v", mode, err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("db file mode = %o, want 600", perm)
	}
}

func TestMigrateDownThenUp(t *testing.T) {
	s, _ := openTemp(t)
	ctx := t.Context()
	p, err := s.provider()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.DownTo(ctx, 0); err != nil {
		t.Fatalf("down: %v", err)
	}
	if got := tables(t, s.Reader()); !slices.Equal(got, []string{"goose_db_version"}) {
		t.Fatalf("after down, tables = %v", got)
	}
	if _, err := s.Migrate(ctx); err != nil {
		t.Fatalf("up again: %v", err)
	}
}

func TestReaderIsQueryOnly(t *testing.T) {
	s, _ := openTemp(t)
	_, err := s.Reader().ExecContext(t.Context(), `INSERT INTO settings (key, value, updated_at) VALUES ('k', '1', 0)`)
	if err == nil {
		t.Fatal("write through the read pool should fail")
	}
}

func TestSchemaConstraints(t *testing.T) {
	s, _ := openTemp(t)
	ctx := t.Context()
	w := s.Writer()

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := w.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	mustFail := func(why, q string, args ...any) {
		t.Helper()
		if _, err := w.ExecContext(ctx, q, args...); err == nil {
			t.Fatalf("%s: expected an error from %s", why, q)
		}
	}

	mustExec(`INSERT INTO users (id, username, role, created_at, updated_at) VALUES ('u_1', 'Owner', 'owner', 0, 0)`)
	mustFail("single owner", `INSERT INTO users (id, username, role, created_at, updated_at) VALUES ('u_2', 'other', 'owner', 0, 0)`)
	mustFail("username is case-insensitive unique", `INSERT INTO users (id, username, role, created_at, updated_at) VALUES ('u_3', 'owner', 'viewer', 0, 0)`)
	mustFail("role check", `INSERT INTO users (id, username, role, created_at, updated_at) VALUES ('u_4', 'x', 'root', 0, 0)`)
	mustFail("STRICT types", `INSERT INTO settings (key, value, updated_at) VALUES ('k', '1', 'yesterday')`)
	mustFail("foreign keys", `INSERT INTO sessions (id, token_hash, user_id, method, created_at, expires_at, last_seen_at) VALUES ('s_1', x'00', 'u_missing', 'password', 0, 0, 0)`)

	mustExec(`INSERT INTO audit (at, actor, action, result) VALUES (0, '{}', 'test', 'ok')`)
	mustFail("audit append-only", `UPDATE audit SET result = 'tampered'`)
	mustExec(`DELETE FROM audit`) // retention may delete

	mustExec(`INSERT INTO boards (id, slug, title, created_at, updated_at) VALUES ('b_1', 'home', 'Home', 0, 0)`)
	mustExec(`INSERT INTO layouts (board_id, revision, format_version, document, created_at, created_by) VALUES ('b_1', 1, 1, '{"sections":[]}', 0, 'u_1')`)
	mustFail("layout revisions are unique per board", `INSERT INTO layouts (board_id, revision, format_version, document, created_at) VALUES ('b_1', 1, 1, '{}', 0)`)
}

func TestWriteTxRollsBack(t *testing.T) {
	s, _ := openTemp(t)
	ctx := t.Context()
	boom := context.Canceled
	err := s.WriteTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO settings (key, value, updated_at) VALUES ('k', '1', 0)`); err != nil {
			return err
		}
		return boom
	})
	if err == nil {
		t.Fatal("expected the fn error")
	}
	var n int
	if err := s.Reader().QueryRowContext(ctx, `SELECT count(*) FROM settings`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("rows after rollback = %d, %v", n, err)
	}
}

func TestOpenRejectsQueryInPath(t *testing.T) {
	if _, err := Open(t.Context(), filepath.Join(t.TempDir(), "x?mode=memory")); err == nil {
		t.Fatal("expected error")
	}
}
