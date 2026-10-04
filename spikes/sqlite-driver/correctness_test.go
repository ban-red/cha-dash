package sqlitedriver

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Every connection, writer and each of the four readers, must carry the
// hub's pragmas.
func TestPragmas(t *testing.T) {
	forEach(t, func(t *testing.T, v Variant) {
		st, _ := openMigrated(t, v)
		ctx := t.Context()
		check := func(t *testing.T, c *sql.Conn, queryOnly int) {
			t.Helper()
			want := map[string]string{
				"journal_mode": "wal", "busy_timeout": "5000", "foreign_keys": "1",
				"synchronous": "1", "query_only": fmt.Sprint(queryOnly),
			}
			for p, w := range want {
				var got string
				if err := c.QueryRowContext(ctx, "PRAGMA "+p).Scan(&got); err != nil {
					t.Fatalf("PRAGMA %s: %v", p, err)
				}
				if !strings.EqualFold(got, w) {
					t.Errorf("PRAGMA %s = %s, want %s", p, got, w)
				}
			}
		}
		w, err := st.W.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		check(t, w, 0)
		w.Close()

		// Hold all four readers at once so each is a distinct connection.
		var conns []*sql.Conn
		for range ReadPoolSize {
			c, err := st.R.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			conns = append(conns, c)
			check(t, c, 1)
		}
		for _, c := range conns {
			c.Close()
		}
	})
}

func TestStrictTables(t *testing.T) {
	forEach(t, func(t *testing.T, v Variant) {
		st, _ := openMigrated(t, v)
		ctx := t.Context()
		cases := []struct {
			name string
			sql  string
			args []any
			ok   bool
		}{
			{"text in INTEGER", `INSERT INTO settings VALUES ('a', '{}', ?)`, []any{"yesterday"}, false},
			{"numeric text in INTEGER is coerced", `INSERT INTO settings VALUES ('b', '{}', ?)`, []any{"123"}, true},
			{"[]byte in TEXT", `INSERT INTO settings VALUES ('c', ?, 1)`, []any{[]byte(`{}`)}, false},
			{"time.Time in INTEGER", `INSERT INTO settings VALUES ('d', '{}', ?)`, []any{time.Now()}, false},
			{"bool in INTEGER", `INSERT INTO integration_instances (id, integration, name, enabled, created_at, updated_at) VALUES ('i', 'x', 'y', ?, 1, 1)`, []any{true}, true},
			{"valid user row", `INSERT INTO users VALUES ('u', 'n', NULL, 'owner', 1, 1, NULL)`, nil, true},
		}
		for _, c := range cases {
			_, err := st.W.ExecContext(ctx, c.sql, c.args...)
			if (err == nil) != c.ok {
				t.Errorf("%s: err = %v, want ok=%v", c.name, err, c.ok)
			} else if err != nil {
				t.Logf("%s: %v", c.name, err)
			}
		}
		// A TEXT token hash in the BLOB column must be rejected.
		_, err := st.W.ExecContext(ctx, `INSERT INTO sessions (id, token_hash, user_id, method, created_at, expires_at, last_seen_at)
			VALUES ('s', ?, 'u', 'password', 1, 2, 1)`, "not-bytes")
		if err == nil {
			t.Error("string in BLOB column: want error")
		} else {
			t.Logf("string in BLOB: %v", err)
		}
	})
}

func TestForeignKeys(t *testing.T) {
	forEach(t, func(t *testing.T, v Variant) {
		st, fx, _ := seeded(t, v)
		ctx := t.Context()
		_, err := st.W.ExecContext(ctx, `INSERT INTO sessions (id, token_hash, user_id, method, created_at, expires_at, last_seen_at)
			VALUES ('s_x', x'00', 'u_nobody', 'password', 1, 2, 1)`)
		if err == nil || !strings.Contains(err.Error(), "FOREIGN KEY") {
			t.Errorf("orphan session: err = %v, want FOREIGN KEY constraint", err)
		}
		if _, err := SaveLayout(ctx, st, fx.BoardID, 0, `{}`, "u_0001"); err != nil {
			t.Fatal(err)
		}
		if _, err := st.W.ExecContext(ctx, `DELETE FROM users WHERE id = 'u_0001'`); err != nil {
			t.Fatal(err)
		}
		var sessions int
		var createdBy sql.NullString
		st.R.QueryRowContext(ctx, `SELECT count(*) FROM sessions WHERE user_id = 'u_0001'`).Scan(&sessions)
		st.R.QueryRowContext(ctx, `SELECT created_by FROM layouts WHERE board_id = ?`, fx.BoardID).Scan(&createdBy)
		if sessions != 0 || createdBy.Valid {
			t.Errorf("after deleting a user: %d sessions left (want 0, CASCADE), created_by = %v (want NULL, SET NULL)", sessions, createdBy)
		}
	})
}

func TestSchemaRules(t *testing.T) {
	forEach(t, func(t *testing.T, v Variant) {
		st, _, _ := seeded(t, v)
		ctx := t.Context()
		expectErr := func(what, contains, q string) {
			t.Helper()
			_, err := st.W.ExecContext(ctx, q)
			if err == nil || !strings.Contains(err.Error(), contains) {
				t.Errorf("%s: err = %v, want %q", what, err, contains)
			}
		}
		expectErr("second owner", "UNIQUE", `INSERT INTO users (id, username, role, created_at, updated_at) VALUES ('u_2', 'other', 'owner', 1, 1)`)
		expectErr("username differs only in case", "UNIQUE", `INSERT INTO users (id, username, role, created_at, updated_at) VALUES ('u_3', 'USER1', 'viewer', 1, 1)`)
		expectErr("bad role", "CHECK", `INSERT INTO users (id, username, role, created_at, updated_at) VALUES ('u_4', 'x', 'root', 1, 1)`)
		if err := AppendAudit(ctx, st, 1); err != nil {
			t.Fatal(err)
		}
		expectErr("audit update", "audit is append-only", `UPDATE audit SET result = 'forged'`)
	})
}

func TestReadersAreQueryOnly(t *testing.T) {
	forEach(t, func(t *testing.T, v Variant) {
		st, _ := openMigrated(t, v)
		_, err := st.R.ExecContext(t.Context(), `INSERT INTO settings VALUES ('k', '{}', 1)`)
		if err == nil {
			t.Fatal("write through the read pool succeeded")
		}
		t.Logf("write on reader: %v", err)
	})
}

// _txlock=immediate: the writer's BEGIN takes the write lock at once, so a
// second writer (another process, say) is refused before either has written.
// busy_timeout: that refusal waits for the timeout first.
func TestTxLockAndBusyTimeout(t *testing.T) {
	forEach(t, func(t *testing.T, v Variant) {
		st, path := openMigrated(t, v)
		ctx := t.Context()
		tx, err := st.W.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()

		other, err := v.Open(path, false, testKEK)
		if err != nil {
			t.Fatal(err)
		}
		defer other.Close()
		c, err := other.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		if _, err := c.ExecContext(ctx, "PRAGMA busy_timeout=300"); err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		_, err = c.ExecContext(ctx, "BEGIN IMMEDIATE")
		waited := time.Since(start)
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), "locked") && !strings.Contains(strings.ToLower(err.Error()), "busy") {
			t.Fatalf("second writer: err = %v, want SQLITE_BUSY", err)
		}
		if waited < 250*time.Millisecond || waited > 2*time.Second {
			t.Errorf("second writer gave up after %v, want ~300ms (busy_timeout)", waited)
		}
		t.Logf("second writer: %v after %v", err, waited.Round(time.Millisecond))

		// A deferred read on the second pool is not blocked: WAL.
		var n int
		if err := c.QueryRowContext(ctx, "SELECT count(*) FROM users").Scan(&n); err != nil {
			t.Errorf("read while the writer holds the lock: %v", err)
		}
	})
}

// WAL: a reader sees the last committed snapshot while a write is open, and
// the new row once it commits.
func TestWALSnapshot(t *testing.T) {
	forEach(t, func(t *testing.T, v Variant) {
		st, _, _ := seeded(t, v)
		ctx := t.Context()
		count := func() int {
			var n int
			if err := st.R.QueryRowContext(ctx, `SELECT count(*) FROM audit`).Scan(&n); err != nil {
				t.Fatal(err)
			}
			return n
		}
		tx, err := st.W.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO audit (at, actor, action, result) VALUES (1, '{}', 'x', 'ok')`); err != nil {
			t.Fatal(err)
		}
		readCtx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		var n int
		if err := st.R.QueryRowContext(readCtx, `SELECT count(*) FROM audit`).Scan(&n); err != nil || n != 0 {
			t.Fatalf("read during write: n=%d err=%v, want 0 and no wait", n, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		if n := count(); n != 1 {
			t.Fatalf("after commit: %d rows, want 1", n)
		}
	})
}

func TestMigrationsIdempotent(t *testing.T) {
	forEach(t, func(t *testing.T, v Variant) {
		st, _ := openMigrated(t, v)
		n, err := st.Migrate(t.Context())
		if err != nil || n != 0 {
			t.Fatalf("second Migrate: applied %d, %v", n, err)
		}
		var version int
		if err := st.R.QueryRowContext(t.Context(), `SELECT max(version_id) FROM goose_db_version`).Scan(&version); err != nil || version != 1 {
			t.Fatalf("version = %d, %v", version, err)
		}
	})
}

// The hub creates the database 0600 and relies on SQLite giving -wal and
// -shm the same mode. SQLite's own unix VFS (modernc) does; the Go VFS in
// ncruces creates them 0666 minus the umask. Under a 077 umask, which the
// hub would set at startup if it moves to ncruces, every variant gets 0600.
func TestSidecarFileModes(t *testing.T) {
	modes := func(t *testing.T, v Variant) []os.FileMode {
		_, _, path := seeded(t, v)
		var m []os.FileMode
		for _, suffix := range []string{"", "-wal", "-shm"} {
			fi, err := os.Stat(path + suffix)
			if err != nil {
				t.Fatal(err)
			}
			m = append(m, fi.Mode().Perm())
		}
		return m
	}
	all600 := func(m []os.FileMode) bool {
		return !slices.ContainsFunc(m, func(p os.FileMode) bool { return p != 0o600 })
	}
	forEach(t, func(t *testing.T, v Variant) {
		old := syscall.Umask(0o022) // Docker's default
		def := modes(t, v)
		syscall.Umask(0o077)
		strict := modes(t, v)
		syscall.Umask(old)

		t.Logf("db, -wal, -shm modes: umask 022 → %o, umask 077 → %o", def, strict)
		if v.Name == "modernc" && !all600(def) {
			t.Errorf("modernc under umask 022: %o, want all 600", def)
		}
		if !all600(strict) {
			t.Errorf("under umask 077: %o, want all 600", strict)
		}
	})
}

// Scanning into any is where drivers differ most. The hub scans into typed
// variables, but sqlc or ad-hoc code might not.
func TestScanTypes(t *testing.T) {
	got := map[string][]string{}
	forEach(t, func(t *testing.T, v Variant) {
		st, _ := openMigrated(t, v)
		var i, txt, blob, null, real, ts any
		err := st.R.QueryRowContext(t.Context(), `SELECT 1, 'x', x'00', NULL, 1.5, CURRENT_TIMESTAMP`).Scan(&i, &txt, &blob, &null, &real, &ts)
		if err != nil {
			t.Fatal(err)
		}
		for _, x := range []any{i, txt, blob, null, real, ts} {
			got[v.Name] = append(got[v.Name], fmt.Sprintf("%T", x))
		}
		t.Logf("%v", got[v.Name])
	})
	if !slices.Equal(got["modernc"], got["ncruces"]) {
		t.Logf("scan-into-any types differ: modernc %v, ncruces %v", got["modernc"], got["ncruces"])
	}
}

// Large TEXT and BLOB values survive a round trip byte for byte.
func TestRoundTrip(t *testing.T) {
	forEach(t, func(t *testing.T, v Variant) {
		st, fx, _ := seeded(t, v)
		doc := LayoutDoc(1<<20, 1)
		if _, err := SaveLayout(t.Context(), st, fx.BoardID, 0, doc, fx.OwnerID); err != nil {
			t.Fatal(err)
		}
		var back string
		if err := st.R.QueryRowContext(t.Context(), `SELECT document FROM layouts`).Scan(&back); err != nil || back != doc {
			t.Fatalf("1 MiB layout round trip: equal=%v err=%v", back == doc, err)
		}
		if err := LookupSession(t.Context(), st, fx.TokenHashes[7]); err != nil {
			t.Fatalf("session lookup by BLOB hash: %v", err)
		}
	})
}

func TestVersions(t *testing.T) {
	opts := map[string][]string{}
	forEach(t, func(t *testing.T, v Variant) {
		st, _ := openMigrated(t, v)
		var ver string
		st.R.QueryRowContext(t.Context(), `SELECT sqlite_version()`).Scan(&ver)
		rows, err := st.R.QueryContext(t.Context(), `PRAGMA compile_options`)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var o string
			rows.Scan(&o)
			opts[v.Name] = append(opts[v.Name], o)
		}
		rows.Close()
		t.Logf("SQLite %s, %d compile options", ver, len(opts[v.Name]))
	})
	a, b := opts["modernc"], opts["ncruces"]
	var onlyA, onlyB []string
	for _, o := range a {
		if !slices.Contains(b, o) {
			onlyA = append(onlyA, o)
		}
	}
	for _, o := range b {
		if !slices.Contains(a, o) {
			onlyB = append(onlyB, o)
		}
	}
	t.Logf("compile options only in modernc: %v", onlyA)
	t.Logf("compile options only in ncruces: %v", onlyB)
}
