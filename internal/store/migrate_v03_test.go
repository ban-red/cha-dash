package store

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"slices"
	"testing"
)

// v02Version is the last migration v0.2 shipped.
const v02Version = 16

// v02Database makes a database at v0.2's schema with a few rows in the
// tables v0.3's migrations touch or point at, and returns its path. Each
// test opens its own copy (VACUUM INTO), so they can't disturb each other.
func v02Database(t *testing.T) string {
	t.Helper()
	ctx := t.Context()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "v02.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.MigrateTo(ctx, v02Version); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO users (id, username, role, created_at, updated_at) VALUES ('u_owner', 'owner', 'owner', 1, 1), ('u_admin', 'sam', 'admin', 2, 2)`,
		`INSERT INTO nodes (id, name, public_key, state, enrolled_via, approved_by, approved_at, created_at, updated_at)
			VALUES ('n_local', 'local', 'ed25519:AAAA', 'approved', 'local', 'local-pairing', 3, 3, 3)`,
		`INSERT INTO stack_drafts (node, stack, content, revision, updated_at, updated_by) VALUES
			('n_local', 'media', 'services:
  web:
    image: nginx:1.27
', 3, 1700000000000, 'sam'),
			('n_local', 'vpn', 'services: {}
', 1, 1700000000001, '')`,
		`INSERT INTO stack_secret_grants (node, stack, name, granted_at) VALUES ('n_local', 'media', 'db_password', 4)`,
		`INSERT INTO settings (key, value, updated_at) VALUES ('security', '{"step_up":"fresh"}', 5)`,
	} {
		if _, err := s.Writer().ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	return dbPath(t, s)
}

func dbPath(t *testing.T, s *Store) string {
	t.Helper()
	var main string
	var seq int
	var name string
	if err := s.Reader().QueryRowContext(t.Context(), `PRAGMA database_list`).Scan(&seq, &name, &main); err != nil {
		t.Fatal(err)
	}
	return main
}

// copyOf opens a fresh copy of the database at src.
func copyOf(t *testing.T, src string) *Store {
	t.Helper()
	ctx := t.Context()
	from, err := Open(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "copy.db")
	if _, err := from.Writer().ExecContext(ctx, `VACUUM INTO ?`, dst); err != nil {
		t.Fatal(err)
	}
	from.Close()
	s, err := Open(ctx, dst)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

type draftRow struct {
	node, stack, content, by string
	revision, at             int64
}

func draftRows(t *testing.T, db *sql.DB) []draftRow {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), `SELECT node, stack, content, revision, updated_at, updated_by FROM stack_drafts ORDER BY node, stack`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []draftRow
	for rows.Next() {
		var r draftRow
		if err := rows.Scan(&r.node, &r.stack, &r.content, &r.revision, &r.at, &r.by); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func columns(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), `SELECT name FROM pragma_table_info(?) ORDER BY cid`, table)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		out = append(out, n)
	}
	return out
}

func count(t *testing.T, db *sql.DB, q string) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(t.Context(), q).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

// Each v0.3 migration applies to a v0.2 database, keeps its rows, and rolls
// back to exactly v0.2's tables and columns.
func TestV03MigrationsOnAV02Database(t *testing.T) {
	src := v02Database(t)

	base := copyOf(t, src)
	wantTables := tables(t, base.Reader())
	wantDraftCols := columns(t, base.Reader(), "stack_drafts")
	wantDrafts := draftRows(t, base.Reader())
	if len(wantDrafts) != 2 {
		t.Fatalf("fixture has %d drafts", len(wantDrafts))
	}

	for _, m := range []struct {
		version int64
		tables  []string
		check   func(t *testing.T, s *Store)
	}{
		{17, []string{"stack_hooks", "stack_syncs"}, checkStackSync},
		{18, []string{"forward_auth_links", "oidc_identities", "oidc_logins"}, checkOIDC},
		{19, []string{"node_join_tokens"}, checkJoinTokens},
	} {
		t.Run(fmt.Sprintf("%05d", m.version), func(t *testing.T) {
			s := copyOf(t, src)
			ctx := t.Context()
			if err := s.MigrateTo(ctx, m.version); err != nil {
				t.Fatalf("up: %v", err)
			}
			got := tables(t, s.Reader())
			for _, name := range m.tables {
				if !slices.Contains(got, name) {
					t.Errorf("after %d, no table %s", m.version, name)
				}
			}
			if d := draftRows(t, s.Reader()); !slices.Equal(d, wantDrafts) {
				t.Errorf("drafts after %d = %v, want %v", m.version, d, wantDrafts)
			}
			if n := count(t, s.Reader(), `SELECT count(*) FROM users`) + count(t, s.Reader(), `SELECT count(*) FROM nodes`) +
				count(t, s.Reader(), `SELECT count(*) FROM stack_secret_grants`) + count(t, s.Reader(), `SELECT count(*) FROM settings`); n != 5 {
				t.Errorf("v0.2 rows after %d: %d, want 5", m.version, n)
			}
			m.check(t, s)

			if err := s.MigrateTo(ctx, v02Version); err != nil {
				t.Fatalf("down: %v", err)
			}
			if got := tables(t, s.Reader()); !slices.Equal(got, wantTables) {
				t.Errorf("after rolling back %d, tables = %v, want %v", m.version, got, wantTables)
			}
			if got := columns(t, s.Reader(), "stack_drafts"); !slices.Equal(got, wantDraftCols) {
				t.Errorf("after rolling back %d, stack_drafts columns = %v, want %v", m.version, got, wantDraftCols)
			}
			// The check may have added drafts of its own; v0.2's are unchanged.
			d := draftRows(t, s.Reader())
			for _, want := range wantDrafts {
				if !slices.Contains(d, want) {
					t.Errorf("after rolling back %d, draft %s/%s changed or went: %v", m.version, want.node, want.stack, d)
				}
			}
			if err := s.MigrateTo(ctx, m.version); err != nil {
				t.Fatalf("up again: %v", err)
			}
		})
	}
}

// v0.2's drafts were all written by the editor, so they read back as 'web'.
func checkStackSync(t *testing.T, s *Store) {
	t.Helper()
	ctx := t.Context()
	r, w := s.Reader(), s.Writer()
	if n := count(t, r, `SELECT count(*) FROM stack_drafts WHERE source = 'web'`); n != 2 {
		t.Errorf("drafts with source web = %d, want 2", n)
	}
	mustExec := func(q string) {
		t.Helper()
		if _, err := w.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	mustFail := func(why, q string) {
		t.Helper()
		if _, err := w.ExecContext(ctx, q); err == nil {
			t.Fatalf("%s: expected an error from %s", why, q)
		}
	}
	mustExec(`INSERT INTO stack_drafts (node, stack, content, revision, updated_at) VALUES ('n_local', 'new', 'x', 1, 0)`)
	if n := count(t, r, `SELECT count(*) FROM stack_drafts WHERE stack = 'new' AND source = 'web'`); n != 1 {
		t.Error("a draft written without source isn't 'web'")
	}
	mustExec(`INSERT INTO stack_drafts (node, stack, content, revision, updated_at, source) VALUES ('n_local', 'synced', 'x', 1, 0, 'git:0123abc')`)

	const sync = `INSERT INTO stack_syncs (id, node, stack, url, branch, path, auth_kind, mode, poll_seconds, secrets_allow, owner_id, state, state_reason,
		last_seen_sha, last_deployed_sha, rejected_sha, last_error, created_at, updated_at) VALUES `
	mustExec(sync + `('syn_1', 'n_local', 'media', 'https://git.lab.example/ops/stacks', 'main', 'media/compose.yaml', 'https', 'ask', 300, '[]', 'u_admin', 'active', '', '', '', '', '', 0, 0)`)
	mustExec(sync + `('syn_2', 'n_local', 'vpn', 'https://git.lab.example/ops/stacks', 'main', 'vpn/compose.yaml', 'none', 'deploy', 300, '["vpn_key"]', 'u_admin', 'held', 'rolled back', '', '', '', '', 0, 0)`)
	mustFail("one sync per stack", sync+`('syn_3', 'n_local', 'media', 'https://git.lab.example/other', 'main', 'x', 'none', 'ask', 300, '[]', NULL, 'active', '', '', '', '', '', 0, 0)`)
	mustFail("auth_kind check", sync+`('syn_4', 'n_local', 'a', 'u', 'main', 'x', 'token', 'ask', 300, '[]', NULL, 'active', '', '', '', '', '', 0, 0)`)
	mustFail("mode check", sync+`('syn_5', 'n_local', 'b', 'u', 'main', 'x', 'none', 'auto', 300, '[]', NULL, 'active', '', '', '', '', '', 0, 0)`)
	mustFail("state check", sync+`('syn_6', 'n_local', 'c', 'u', 'main', 'x', 'none', 'ask', 300, '[]', NULL, 'stopped', '', '', '', '', '', 0, 0)`)
	mustFail("owner must exist", sync+`('syn_7', 'n_local', 'd', 'u', 'main', 'x', 'none', 'ask', 300, '[]', 'u_missing', 'active', '', '', '', '', '', 0, 0)`)
	mustFail("STRICT types", sync+`('syn_8', 'n_local', 'e', 'u', 'main', 'x', 'none', 'ask', 'often', '[]', NULL, 'active', '', '', '', '', '', 0, 0)`)

	const hook = `INSERT INTO stack_hooks (id, url, branch, sender, created_by, created_at, last_used_at) VALUES `
	mustExec(hook + `('whk_1', 'https://git.lab.example/ops/stacks', 'main', 'gitea', 'u_admin', 0, 0)`)
	mustFail("one hook per remote and branch", hook+`('whk_2', 'https://git.lab.example/ops/stacks', 'main', 'github', NULL, 0, 0)`)
	mustExec(hook + `('whk_3', 'https://git.lab.example/ops/stacks', 'dev', 'chadash', NULL, 0, 0)`)
	mustFail("sender check", hook+`('whk_4', 'https://git.lab.example/x', 'main', 'bitbucket', NULL, 0, 0)`)

	if n := count(t, r, `SELECT count(*) FROM sqlite_schema WHERE type = 'index' AND name = 'stack_syncs_remote' AND tbl_name = 'stack_syncs'`); n != 1 {
		t.Error("no stack_syncs_remote index")
	}

	// Deleting the owner leaves the sync, with no owner (paused by the syncer).
	mustExec(`DELETE FROM users WHERE id = 'u_admin'`)
	if n := count(t, r, `SELECT count(*) FROM stack_syncs WHERE owner_id IS NULL`); n != 2 {
		t.Errorf("syncs without an owner = %d, want 2", n)
	}
	if n := count(t, r, `SELECT count(*) FROM stack_hooks WHERE id = 'whk_1' AND created_by IS NULL`); n != 1 {
		t.Error("a hook's creator wasn't cleared")
	}
}

func checkOIDC(t *testing.T, s *Store) {
	t.Helper()
	ctx := t.Context()
	w := s.Writer()
	mustExec := func(q string) {
		t.Helper()
		if _, err := w.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	mustFail := func(why, q string) {
		t.Helper()
		if _, err := w.ExecContext(ctx, q); err == nil {
			t.Fatalf("%s: expected an error from %s", why, q)
		}
	}
	mustExec(`INSERT INTO oidc_identities (issuer, subject, user_id, email, created_at, last_used_at) VALUES ('https://id.lab.example', 'abc', 'u_admin', 'sam@lab.example', 0, 0)`)
	mustExec(`INSERT INTO oidc_identities (issuer, subject, user_id, email, created_at, last_used_at) VALUES ('https://id.lab.example', 'def', 'u_owner', '', 0, 0)`)
	mustFail("one link per identity", `INSERT INTO oidc_identities (issuer, subject, user_id, email, created_at, last_used_at) VALUES ('https://id.lab.example', 'abc', 'u_owner', '', 0, 0)`)
	mustFail("identity needs a user", `INSERT INTO oidc_identities (issuer, subject, user_id, email, created_at, last_used_at) VALUES ('https://id.lab.example', 'ghi', 'u_missing', '', 0, 0)`)
	mustExec(`INSERT INTO oidc_logins (state, nonce, verifier, return_to, link_user, expires_at) VALUES ('st_1', 'n', 'v', '/', NULL, 600000)`)
	mustExec(`INSERT INTO oidc_logins (state, nonce, verifier, return_to, link_user, expires_at) VALUES ('st_2', 'n', 'v', '/settings', 'u_admin', 600000)`)
	mustFail("a link login needs its user", `INSERT INTO oidc_logins (state, nonce, verifier, return_to, link_user, expires_at) VALUES ('st_3', 'n', 'v', '/', 'u_missing', 0)`)
	mustExec(`INSERT INTO forward_auth_links (username, user_id, created_at) VALUES ('sam', 'u_admin', 0)`)
	mustFail("one link per proxy username", `INSERT INTO forward_auth_links (username, user_id, created_at) VALUES ('sam', 'u_owner', 0)`)

	mustExec(`DELETE FROM users WHERE id = 'u_admin'`)
	r := s.Reader()
	if n := count(t, r, `SELECT count(*) FROM oidc_identities`); n != 1 {
		t.Errorf("identities after deleting a user = %d, want 1", n)
	}
	if n := count(t, r, `SELECT count(*) FROM oidc_logins`); n != 1 {
		t.Errorf("logins after deleting a user = %d, want 1", n)
	}
	if n := count(t, r, `SELECT count(*) FROM forward_auth_links`); n != 0 {
		t.Errorf("forward-auth links after deleting a user = %d, want 0", n)
	}
}

func checkJoinTokens(t *testing.T, s *Store) {
	t.Helper()
	ctx := t.Context()
	w := s.Writer()
	mustExec := func(q string) {
		t.Helper()
		if _, err := w.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	mustFail := func(why, q string) {
		t.Helper()
		if _, err := w.ExecContext(ctx, q); err == nil {
			t.Fatalf("%s: expected an error from %s", why, q)
		}
	}
	const tok = `INSERT INTO node_join_tokens (id, hash, name, add_docker, created_by, created_at, expires_at) VALUES `
	mustExec(tok + `('njt_1', 'aa', 'alpha', 1, 'u_admin', 0, 900000)`)
	mustExec(tok + `('njt_2', 'bb', '', 0, NULL, 0, 900000)`)
	mustFail("hash is unique", tok+`('njt_3', 'aa', '', 0, NULL, 0, 900000)`)
	mustFail("add_docker is 0 or 1", tok+`('njt_4', 'cc', '', 2, NULL, 0, 900000)`)
	mustFail("creator must exist", tok+`('njt_5', 'dd', '', 0, 'u_missing', 0, 900000)`)

	r := s.Reader()
	if n := count(t, r, `SELECT count(*) FROM node_join_tokens WHERE used_at = 0 AND revoked_at = 0 AND node_id IS NULL`); n != 2 {
		t.Errorf("fresh tokens = %d, want 2 unspent", n)
	}

	// Spending: the conditional update the handshake runs.
	mustExec(`INSERT INTO nodes (id, name, public_key, state, enrolled_via, created_at, updated_at) VALUES ('n_alpha', 'alpha', 'ed25519:BBBB', 'pending', 'token', 10, 10)`)
	spend := func() int64 {
		res, err := w.ExecContext(ctx, `UPDATE node_join_tokens SET used_at = 10, node_id = 'n_alpha' WHERE id = 'njt_1' AND used_at = 0 AND expires_at > 10`)
		if err != nil {
			t.Fatal(err)
		}
		n, _ := res.RowsAffected()
		return n
	}
	if spend() != 1 {
		t.Fatal("the token didn't spend")
	}
	if spend() != 0 {
		t.Fatal("the token spent twice")
	}
	mustFail("node must exist", `UPDATE node_join_tokens SET node_id = 'n_missing' WHERE id = 'njt_2'`)

	mustExec(`DELETE FROM nodes WHERE id = 'n_alpha'`)
	mustExec(`DELETE FROM users WHERE id = 'u_admin'`)
	if n := count(t, r, `SELECT count(*) FROM node_join_tokens WHERE id = 'njt_1' AND node_id IS NULL AND created_by IS NULL AND used_at = 10`); n != 1 {
		t.Error("deleting the node or the creator should clear the reference and keep the token")
	}
}
