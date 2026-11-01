package store

import (
	"testing"
)

// TestV04Tables: the v0.4 migrations (00023–00027) take a row each, read
// it back as written, and refuse what their CHECKs rule out.
func TestV04Tables(t *testing.T) {
	s, _ := openTemp(t)
	ctx := t.Context()
	w, r := s.Writer(), s.Reader()

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

	// 00023: incident_events.
	mustExec(`INSERT INTO incident_events (id, incident_id, at, kind, data) VALUES ('iev_1', 'pve-a.down', 1000, 'opened', '{"severity":"down"}')`)
	mustExec(`INSERT INTO incident_events (id, incident_id, at, kind) VALUES ('iev_2', 'pve-a.down', 2000, 'resolved')`)
	var kind, data string
	var at int64
	if err := r.QueryRowContext(ctx, `SELECT kind, at, data FROM incident_events WHERE incident_id = 'pve-a.down' ORDER BY at DESC LIMIT 1`).Scan(&kind, &at, &data); err != nil {
		t.Fatal(err)
	}
	if kind != "resolved" || at != 2000 || data != "{}" {
		t.Fatalf("incident event = %s %d %s", kind, at, data)
	}
	mustFail("incident event kind", `INSERT INTO incident_events (id, incident_id, at, kind) VALUES ('iev_3', 'x', 0, 'escalated')`)
	mustFail("incident event time is an integer", `INSERT INTO incident_events (id, incident_id, at, kind) VALUES ('iev_4', 'x', 'today', 'opened')`)

	// 00024: maintenance_windows, rollouts, backup_objectives.
	mustExec(`INSERT INTO maintenance_windows (id, name, hosts, starts_at, ends_at, weekly, created_by, created_at)
		VALUES ('mw_1', 'Sunday night', '["proxmox:i_1/node/pve-a"]', 1000, 7201000, 1, 'u_1', 0)`)
	var name, hosts string
	var weekly int
	if err := r.QueryRowContext(ctx, `SELECT name, hosts, weekly FROM maintenance_windows WHERE id = 'mw_1'`).Scan(&name, &hosts, &weekly); err != nil {
		t.Fatal(err)
	}
	if name != "Sunday night" || hosts != `["proxmox:i_1/node/pve-a"]` || weekly != 1 {
		t.Fatalf("window = %s %s %d", name, hosts, weekly)
	}
	mustFail("weekly is 0 or 1", `INSERT INTO maintenance_windows (id, name, starts_at, ends_at, weekly, created_at) VALUES ('mw_2', 'x', 0, 1, 2, 0)`)
	mustFail("a window ends after it starts", `INSERT INTO maintenance_windows (id, name, starts_at, ends_at, created_at) VALUES ('mw_3', 'x', 5, 5, 0)`)
	mustFail("a weekly window is at most a week", `INSERT INTO maintenance_windows (id, name, starts_at, ends_at, weekly, created_at) VALUES ('mw_4', 'x', 0, 604800001, 1, 0)`)
	mustExec(`INSERT INTO maintenance_windows (id, name, starts_at, ends_at, created_at) VALUES ('mw_5', 'Once, for longer', 0, 604800001, 0)`)

	mustExec(`INSERT INTO rollouts (id, created_by, plan_hash, state, window_id, scheduled_for, steps, created_at)
		VALUES ('rol_1', 'u_1', 'sha256:abc', 'scheduled', 'mw_1', 0, '[{"target":"docker:i_2/container/sonarr","state":"pending"}]', 500)`)
	var state, window, steps string
	var started, finished int64
	if err := r.QueryRowContext(ctx, `SELECT state, window_id, steps, started_at, finished_at FROM rollouts WHERE id = 'rol_1'`).Scan(&state, &window, &steps, &started, &finished); err != nil {
		t.Fatal(err)
	}
	if state != "scheduled" || window != "mw_1" || steps != `[{"target":"docker:i_2/container/sonarr","state":"pending"}]` || started != 0 || finished != 0 {
		t.Fatalf("rollout = %s %s %s %d %d", state, window, steps, started, finished)
	}
	mustFail("rollout state", `INSERT INTO rollouts (id, plan_hash, state, created_at) VALUES ('rol_2', 'h', 'paused', 0)`)
	mustFail("a rollout's window exists", `INSERT INTO rollouts (id, plan_hash, state, window_id, created_at) VALUES ('rol_3', 'h', 'scheduled', 'mw_missing', 0)`)
	// Deleting a window leaves its rollouts, unscheduled from it.
	mustExec(`DELETE FROM maintenance_windows WHERE id = 'mw_1'`)
	var gone *string
	if err := r.QueryRowContext(ctx, `SELECT window_id FROM rollouts WHERE id = 'rol_1'`).Scan(&gone); err != nil || gone != nil {
		t.Fatalf("window_id after deleting the window = %v, %v", gone, err)
	}

	mustExec(`INSERT INTO backup_objectives (resource, max_age_ms, set_by, set_at) VALUES ('proxmox:i_1/guest/101', 93600000, 'u_1', 0)`)
	var maxAge int64
	if err := r.QueryRowContext(ctx, `SELECT max_age_ms FROM backup_objectives WHERE resource = 'proxmox:i_1/guest/101'`).Scan(&maxAge); err != nil || maxAge != 93600000 {
		t.Fatalf("objective = %d, %v", maxAge, err)
	}
	mustFail("one objective per resource", `INSERT INTO backup_objectives (resource, max_age_ms, set_at) VALUES ('proxmox:i_1/guest/101', 1, 0)`)
	mustFail("an objective is positive", `INSERT INTO backup_objectives (resource, max_age_ms, set_at) VALUES ('x', 0, 0)`)

	// 00025: api_tokens.kind.
	mustExec(`INSERT INTO api_tokens (id, user_id, name, token_hash, created_at) VALUES ('tok_a', 'u_1', 'script', x'01', 0)`)
	mustExec(`INSERT INTO api_tokens (id, user_id, name, token_hash, kind, created_at) VALUES ('tok_b', 'u_1', 'agent', x'02', 'agent', 0)`)
	for id, want := range map[string]string{"tok_a": "api", "tok_b": "agent"} {
		var got string
		if err := r.QueryRowContext(ctx, `SELECT kind FROM api_tokens WHERE id = ?`, id).Scan(&got); err != nil || got != want {
			t.Fatalf("%s kind = %q, %v; want %q", id, got, err, want)
		}
	}
	mustFail("token kind", `INSERT INTO api_tokens (id, user_id, name, token_hash, kind, created_at) VALUES ('tok_c', 'u_1', 'x', x'03', 'robot', 0)`)

	// 00027: sessions.step_up_method.
	mustExec(`INSERT INTO sessions (id, token_hash, user_id, method, created_at, expires_at, last_seen_at) VALUES ('s_1', x'01', 'u_1', 'password', 0, 1, 0)`)
	mustExec(`UPDATE sessions SET step_up_until = 1, step_up_method = 'passkey' WHERE id = 's_1'`)
	mustFail("step-up method", `UPDATE sessions SET step_up_method = 'sms' WHERE id = 's_1'`)
}
