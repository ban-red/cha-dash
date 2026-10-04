package action

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"dash.cha.sh/internal/auth"
)

func TestHTTP(t *testing.T) {
	var calls []string
	unavail := Def{ID: "proxmox.guest.snapshot.create", Targets: []string{"vm"}, Tier: T1,
		Preconditions: func(context.Context, Request) error {
			return Unavailable("Add a control token to let Cha Dash snapshot guests on io.", "i_x")
		},
		Execute: func(context.Context, Request, *TaskRun) error { return nil }}
	withParams := Def{ID: "users.create", Tier: T1, Params: fakeSchema{}, Resolve: func(_ context.Context, id string) (Target, error) {
		return Target{ID: id}, nil
	}, Execute: func(context.Context, Request, *TaskRun) error { return nil }}
	e := newEnv(t, rolePolicy{}, reboot(&calls), unavail, withParams)

	mux := http.NewServeMux()
	// The session check: a test header names the user, as auth.Require
	// would from the cookie.
	users := map[string]auth.Identity{
		"sam": {User: auth.User{ID: owner.ID, Username: "sam", Role: "owner"}, Session: auth.Session{ID: owner.Session}},
		"kim": {User: auth.User{ID: other.ID, Username: "kim", Role: "operator"}, Session: auth.Session{ID: other.Session}},
		"lee": {User: auth.User{ID: "u_lee", Username: "lee", Role: "viewer"}, Session: auth.Session{ID: "s_3"}},
	}
	session := func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, ok := users[r.Header.Get("X-Test-User")]
			if !ok {
				http.Error(w, "sign in", http.StatusUnauthorized)
				return
			}
			h.ServeHTTP(w, r.WithContext(withIdentity(r.Context(), id)))
		})
	}
	e.e.Register(mux, session, slog.New(slog.DiscardHandler))

	do := func(user, method, path string, body any, hdr ...string) (int, map[string]any) {
		t.Helper()
		var rd *bytes.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		} else {
			rd = bytes.NewReader(nil)
		}
		req := httptest.NewRequest(method, path, rd)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Test-User", user)
		for i := 0; i+1 < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		var m map[string]any
		json.Unmarshal(rec.Body.Bytes(), &m)
		return rec.Code, m
	}
	errCode := func(m map[string]any) string {
		e, _ := m["error"].(map[string]any)
		c, _ := e["code"].(string)
		return c
	}

	code, plan := do("sam", "POST", "/api/actions/plan", map[string]any{"action": "proxmox.guest.reboot", "target": "proxmox:i_x/qemu/201"})
	if code != 200 {
		t.Fatalf("plan: %d %v", code, plan)
	}
	for _, k := range []string{"id", "hash", "action", "label", "target", "params", "tier", "confirm", "step_up", "steps", "impact", "undo", "expires_at"} {
		if _, ok := plan[k]; !ok {
			t.Errorf("plan lacks %s: %v", k, plan)
		}
	}
	if plan["tier"] != "T2" || plan["confirm"] != "hold" || plan["step_up"] != "none" || plan["undo"] != nil {
		t.Fatalf("plan = %v", plan)
	}

	for _, c := range []struct {
		user   string
		body   any
		status int
		code   string
		extra  string
	}{
		{"lee", map[string]any{"action": "proxmox.guest.reboot", "target": "proxmox:i_x/qemu/201"}, 403, "forbidden", ""},
		{"sam", map[string]any{"action": "nope.nope", "target": "x"}, 404, "not_found", ""},
		{"sam", map[string]any{"action": "proxmox.guest.snapshot.create", "target": "proxmox:i_x/qemu/201"}, 409, "unavailable", "recipe"},
		{"sam", map[string]any{"action": "users.create", "target": "users", "params": map[string]any{"username": ""}}, 422, "invalid", "fields"},
		{"sam", map[string]any{"action": "users.create", "target": "users", "extra": 1}, 400, "bad_json", ""},
	} {
		code, m := do(c.user, "POST", "/api/actions/plan", c.body)
		if code != c.status || errCode(m) != c.code {
			t.Errorf("%v: %d %v, want %d %s", c.body, code, m, c.status, c.code)
		}
		if msg, _ := m["error"].(map[string]any)["message"].(string); msg == "" {
			t.Errorf("%v: no message", c.body)
		}
		if c.extra != "" && m[c.extra] == nil {
			t.Errorf("%v: no %s beside the error: %v", c.body, c.extra, m)
		}
	}
	if code, _ := do("nobody", "POST", "/api/actions/plan", map[string]any{}); code != http.StatusUnauthorized {
		t.Fatalf("no session: %d", code)
	}

	// Approve: someone else's plan, a wrong hash, then the real thing.
	approve := "/api/plans/" + plan["id"].(string) + "/approve"
	if code, m := do("kim", "POST", approve, map[string]any{"hash": plan["hash"]}); code != 403 || errCode(m) != "forbidden" {
		t.Fatalf("another user's approval: %d %v", code, m)
	}
	if code, m := do("sam", "POST", approve, map[string]any{"hash": "sha256:00"}); code != 409 || errCode(m) != "plan_changed" {
		t.Fatalf("wrong hash: %d %v", code, m)
	}
	code, m := do("sam", "POST", approve, map[string]any{"hash": plan["hash"]}, "Idempotency-Key", "k1")
	task, _ := m["task"].(map[string]any)
	if code != 202 || task == nil || task["plan"] != plan["id"] || task["state"] != "queued" {
		t.Fatalf("approve: %d %v", code, m)
	}
	for _, k := range []string{"id", "plan", "action", "label", "target", "actor", "state", "started_at", "finished_at", "error", "undo", "audit"} {
		if _, ok := task[k]; !ok {
			t.Errorf("task lacks %s: %v", k, task)
		}
	}
	if code, again := do("sam", "POST", approve, map[string]any{"hash": plan["hash"]}, "Idempotency-Key", "k1"); code != 202 || again["task"].(map[string]any)["id"] != task["id"] {
		t.Fatalf("idempotent retry: %d %v", code, again)
	}
	if code, _ := do("sam", "POST", approve, map[string]any{"hash": plan["hash"]}, "Idempotency-Key", strings.Repeat("k", 129)); code != 422 {
		t.Fatalf("long key: %d", code)
	}
	e.wait(task["id"].(string))
	if code, got := do("sam", "GET", "/api/tasks/"+task["id"].(string), nil); code != 200 || got["state"] != "succeeded" || got["audit"] == nil {
		t.Fatalf("task: %d %v", code, got)
	}
	if code, got := do("kim", "GET", "/api/tasks/"+task["id"].(string), nil); code != 404 || errCode(got) != "not_found" {
		t.Fatalf("someone else's task: %d %v", code, got)
	}

	// The audit log: Admin and Owner only (here, the policy's owner).
	if code, got := do("sam", "GET", "/api/audit?result=refused", nil); code != 200 || len(got["events"].([]any)) == 0 {
		t.Fatalf("audit: %d %v", code, got)
	}
	if code, got := do("sam", "GET", "/api/audit/verify", nil); code != 200 || got["ok"] != true {
		t.Fatalf("verify: %d %v", code, got)
	}
	for _, path := range []string{"/api/audit", "/api/audit/export", "/api/audit/verify"} {
		if code, got := do("kim", "GET", path, nil); code != 403 || errCode(got) != "forbidden" {
			t.Errorf("%s as an operator: %d %v", path, code, got)
		}
	}
}
