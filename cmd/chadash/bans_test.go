package main

import (
	"path/filepath"
	"strings"
	"testing"

	"dash.cha.sh/internal/store"
)

func TestBansCommand(t *testing.T) {
	dir := seedDataDir(t)
	env := map[string]string{"CHADASH_DATA_DIR": dir}
	if code, stdout, stderr := runCmd(t, env, "bans"); code != 0 || !strings.Contains(stdout, "No addresses are banned.") {
		t.Fatalf("empty list: %d %q %q", code, stdout, stderr)
	}

	st, err := store.Open(t.Context(), filepath.Join(dir, "chadash.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Writer().Exec(`INSERT INTO ip_bans (ip, banned_at, failures) VALUES ('192.0.2.7', 1, 5)`); err != nil {
		t.Fatal(err)
	}
	st.Close()

	if code, stdout, _ := runCmd(t, env, "bans"); code != 0 || !strings.Contains(stdout, "192.0.2.7\tbanned 1970-01-01 00:00 UTC after 5 wrong passwords") {
		t.Fatalf("list: %d %q", code, stdout)
	}
	if code, _, stderr := runCmd(t, env, "bans", "clear", "nope"); code != 2 || !strings.Contains(stderr, "isn't an IP address") {
		t.Fatalf("bad address: %d %q", code, stderr)
	}
	if code, _, stderr := runCmd(t, env, "bans", "clear", "203.0.113.9"); code != 1 || !strings.Contains(stderr, "isn't banned") {
		t.Fatalf("not banned: %d %q", code, stderr)
	}
	if code, _, _ := runCmd(t, env, "bans", "clear"); code != 2 {
		t.Fatalf("clear without an address: %d", code)
	}
	if code, stdout, stderr := runCmd(t, env, "bans", "clear", "192.0.2.7"); code != 0 || !strings.Contains(stdout, "Cleared the ban on 192.0.2.7") {
		t.Fatalf("clear: %d %q %q", code, stdout, stderr)
	}
	if code, stdout, _ := runCmd(t, env, "bans"); code != 0 || !strings.Contains(stdout, "No addresses are banned.") {
		t.Fatalf("after clear: %d %q", code, stdout)
	}
}
