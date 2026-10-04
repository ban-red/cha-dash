package main

import (
	"bytes"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"dash.cha.sh/internal/audit"
	"dash.cha.sh/internal/config"
	"dash.cha.sh/internal/store"
)

func TestAuditVerifyCommand(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{"CHADASH_DATA_DIR": dir}
	if code, _, stderr := runCmd(t, env, "audit", "verify"); code != 1 || !strings.Contains(stderr, "there's no hub database") {
		t.Fatalf("no database: %d %s", code, stderr)
	}
	if code, _, stderr := runCmd(t, env, "audit"); code != 2 || !strings.Contains(stderr, "Usage: chadash audit verify") {
		t.Fatalf("no subcommand: %d %s", code, stderr)
	}

	st, err := store.Open(t.Context(), filepath.Join(dir, "chadash.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	l := audit.New(st)
	for _, a := range []string{"a.b", "a.c", "a.d"} {
		if _, err := l.Append(t.Context(), audit.Entry{Actor: audit.Actor{Kind: "user", Name: "sam"}, Action: a, Result: audit.OK}); err != nil {
			t.Fatal(err)
		}
	}
	if code, stdout, stderr := runCmd(t, env, "audit", "verify"); code != 0 || stdout != "The audit log holds. Events checked: 3.\n" {
		t.Fatalf("intact: %d %q %s", code, stdout, stderr)
	}

	// Someone with the file drops the trigger and edits an event.
	for _, q := range []string{`DROP TRIGGER audit_events_no_update`, `UPDATE audit_events SET event = replace(event, '"a.c"', '"a.x"') WHERE seq = 2`} {
		if _, err := st.Writer().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	st.Close()
	code, stdout, _ := runCmd(t, env, "audit", "verify")
	if code != 1 || !strings.Contains(stdout, "broken at event 2 (a_") || !strings.Contains(stdout, "hash doesn't match") || !strings.Contains(stdout, "Events before it that check out: 1.") {
		t.Fatalf("tampered: %d %q", code, stdout)
	}
}

func TestNewAppWiresTheEngine(t *testing.T) {
	cfg := testConfig(t, t.TempDir())
	cfg.Demo, cfg.DemoAt, cfg.DemoSeed = true, config.DefaultDemoAt, 1
	var logs bytes.Buffer
	a, err := newApp(t.Context(), cfg, slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()
	if ids := a.actions.Registry().IDs(); !slices.Contains(ids, "pve.guest.restart") || !slices.Contains(ids, "integration.reconnect") {
		t.Fatalf("demo actions = %v", ids)
	}
}
