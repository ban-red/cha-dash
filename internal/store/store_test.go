package store

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
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

	want := []string{"audit", "boards", "goose_db_version", "integration_instances", "layouts", "passkey_users", "passkeys", "sessions", "settings", "users", "vault_secrets"}
	if got := tables(t, s.Reader()); !slices.Equal(got, want) {
		t.Fatalf("tables = %v, want %v", got, want)
	}
	v, err := s.SchemaVersion(ctx)
	if err != nil || v != 2 {
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

func TestDSN(t *testing.T) {
	path := "/data dir/50%#1+2/chadash.db"
	for _, readOnly := range []bool{false, true} {
		d := dsn(path, readOnly)
		// ncruces reads _pragma and _txlock only from file: URIs.
		rest, ok := strings.CutPrefix(d, "file:")
		if !ok {
			t.Fatalf("dsn %q lacks the file: prefix", d)
		}
		// SQLite reads modeof itself and doesn't decode "+" as a space.
		if _, query, _ := strings.Cut(rest, "?"); strings.ContainsAny(query, "+ ") || strings.Contains(rest, " ") {
			t.Fatalf("dsn %q has a space, or a '+' in its query", d)
		}
		u, err := url.Parse(rest)
		if err != nil {
			t.Fatal(err)
		}
		if u.Path != path {
			t.Fatalf("path %q, want %q", u.Path, path)
		}
		q := u.Query()
		// ncruces runs pragmas in order; busy_timeout must come first.
		want := []string{"busy_timeout(5000)", "journal_mode(WAL)", "foreign_keys(1)", "synchronous(NORMAL)"}
		txlock := "immediate"
		if readOnly {
			want, txlock = append(want, "query_only(1)"), ""
		}
		if got := q["_pragma"]; !slices.Equal(got, want) {
			t.Errorf("readOnly=%v: pragmas %v, want %v", readOnly, got, want)
		}
		if got := q.Get("_txlock"); got != txlock {
			t.Errorf("readOnly=%v: _txlock %q, want %q", readOnly, got, txlock)
		}
		if got := q.Get("modeof"); got != path {
			t.Errorf("modeof %q, want the database path", got)
		}
	}
}

func TestOpenEscapesThePath(t *testing.T) {
	// URI metacharacters in the path must reach the file system as they
	// are, not turn into a query or a different file name.
	dir := filepath.Join(t.TempDir(), "a dir#with%25odd+plus")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	s, err := Open(t.Context(), filepath.Join(dir, "chadash.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if !slices.Contains(names, "chadash.db") || !slices.Contains(names, "chadash.db-wal") {
		t.Fatalf("files in the data dir: %v", names)
	}
}

func TestWALFileIsPrivate(t *testing.T) {
	// The -wal file holds row data. The driver creates it 0666 minus the
	// umask, so modeof must give it the database's 0600 whatever the umask.
	// (-shm relies on the umask; cmd/chadash tests that.)
	s, path := openTemp(t)
	if _, err := s.Writer().ExecContext(t.Context(), `INSERT INTO settings (key, value, updated_at) VALUES ('k', '1', 0)`); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path + "-wal")
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("-wal mode = %o, want 600", perm)
	}
}

func TestDoubleQuotedStringsAreErrors(t *testing.T) {
	// SQLite is built with DQS=0: "k" is an identifier, never a string. If
	// this starts passing, the driver changed, and SQL that relies on
	// single quotes may need another look.
	s, _ := openTemp(t)
	var n int
	err := s.Reader().QueryRowContext(t.Context(), `SELECT count(*) FROM settings WHERE key = "k"`).Scan(&n)
	if err == nil || !strings.Contains(err.Error(), "no such column") {
		t.Fatalf("double-quoted literal: %v", err)
	}
}
