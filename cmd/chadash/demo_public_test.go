package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"dash.cha.sh/internal/auth"
	"dash.cha.sh/internal/authz"
	"dash.cha.sh/internal/config"
	"dash.cha.sh/internal/runtime"
	"dash.cha.sh/internal/topics"
)

// publicDemo starts the hub as the public demo and returns it with a
// client whose cookie jar holds a visitor's session.
func publicDemo(t *testing.T) (*app, *httptest.Server, *http.Client) {
	t.Helper()
	cfg := testConfig(t, t.TempDir())
	cfg.Demo, cfg.DemoPublic, cfg.DemoAt, cfg.DemoSeed = true, true, config.DefaultDemoAt, 1
	a, err := newApp(t.Context(), cfg, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.close() })
	srv := httptest.NewServer(a.handler)
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	return a, srv, c
}

func call(t *testing.T, c *http.Client, method, url, body string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return resp.StatusCode, m
}

func errCode(m map[string]any) string {
	e, _ := m["error"].(map[string]any)
	s, _ := e["code"].(string)
	return s
}

func TestPublicDemoSignsVisitorsIn(t *testing.T) {
	a, srv, c := publicDemo(t)
	status, m := call(t, c, "GET", srv.URL+"/api/setup", "")
	if status != http.StatusOK || m["required"] != false || m["public_demo"] != true {
		t.Fatalf("setup = %d %v", status, m)
	}
	if status, _ := call(t, c, "GET", srv.URL+"/api/auth/session", ""); status != http.StatusUnauthorized {
		t.Fatalf("before the visit: %d", status)
	}
	status, m = call(t, c, "POST", srv.URL+"/api/auth/demo", "")
	user, _ := m["user"].(map[string]any)
	if status != http.StatusOK || user["username"] != auth.DemoUsername || user["role"] != auth.RoleOperator {
		t.Fatalf("visit = %d %v", status, m)
	}
	status, m = call(t, c, "GET", srv.URL+"/api/auth/session", "")
	sess, _ := m["session"].(map[string]any)
	if status != http.StatusOK || sess["method"] != auth.MethodDemo {
		t.Fatalf("session = %d %v", status, m)
	}

	// A visitor with a session gets it back; new ones are limited per address.
	for range 10 {
		if status, _ := call(t, c, "POST", srv.URL+"/api/auth/demo", ""); status != http.StatusOK {
			t.Fatalf("a signed-in visitor's revisit = %d", status)
		}
	}
	fresh := &http.Client{}
	limited := false
	for range 10 {
		status, m := call(t, fresh, "POST", srv.URL+"/api/auth/demo", "")
		if status == http.StatusTooManyRequests {
			if errCode(m) != "rate_limited" {
				t.Fatalf("429 = %v", m)
			}
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("new demo sessions from one address are never limited")
	}

	// The Demo Home's actions run, against the simulator: a T2 fix, as an
	// operator, with no step-up.
	var in struct {
		Suggested struct{ ID, Target string } `json:"suggested"`
	}
	if err := json.Unmarshal(a.topics.Inbox.Item(topics.Incidents, "host-memory-pressure"), &in); err != nil || in.Suggested.ID == "" {
		t.Fatalf("incident: %v %+v", err, in)
	}
	plan, _ := json.Marshal(map[string]string{"action": in.Suggested.ID, "target": in.Suggested.Target})
	status, m = call(t, c, "POST", srv.URL+"/api/actions/plan", string(plan))
	if status != http.StatusOK || m["tier"] != "T2" {
		t.Fatalf("plan = %d %v", status, m)
	}
	approve, _ := json.Marshal(map[string]any{"hash": m["hash"]})
	if status, m = call(t, c, "POST", srv.URL+"/api/plans/"+m["id"].(string)+"/approve", string(approve)); status != http.StatusAccepted {
		t.Fatalf("approve = %d %v", status, m)
	}
}

// The matrix: every row of the route table, as a visitor, with a path for
// each pattern and its method (or every unsafe method for the catch-alls).
// A route the public demo refuses answers 403 demo_read_only, or 404 where
// demo mode doesn't mount it; any other never says demo_read_only.
func TestPublicDemoRouteMatrix(t *testing.T) {
	_, srv, c := publicDemo(t)
	if status, _ := call(t, c, "POST", srv.URL+"/api/auth/demo", ""); status != http.StatusOK {
		t.Fatal("no visit")
	}
	param := regexp.MustCompile(`\{[^}]+\}`)
	for _, r := range authz.Routes {
		method, path, ok := strings.Cut(r.Pattern, " ")
		methods := []string{method}
		if !ok {
			path, methods = r.Pattern, []string{"GET", "POST", "PUT", "DELETE"}
		}
		if path == "/ws" || path == "/node" || path == "/api/auth/logout" {
			continue // an upgrade; signing the visitor out is checked below
		}
		url := srv.URL + param.ReplaceAllString(path, "x1")
		for _, m := range methods {
			body := ""
			if m != "GET" && m != "DELETE" {
				body = "{}"
			}
			status, doc := call(t, c, m, url, body)
			refused := status == http.StatusForbidden && errCode(doc) == "demo_read_only"
			switch {
			case authz.DemoRefuses(r, m) && !refused && status != http.StatusNotFound:
				t.Errorf("%s %s: %d %v, want 403 demo_read_only", m, r.Pattern, status, doc)
			case authz.DemoRefuses(r, m) && status/100 == 2:
				t.Errorf("%s %s answered %d", m, r.Pattern, status)
			case !authz.DemoRefuses(r, m) && refused:
				t.Errorf("%s %s: refused, but the public demo serves it", m, r.Pattern)
			}
		}
	}

	// Signing out works, and the next visit signs in again.
	if status, _ := call(t, c, "POST", srv.URL+"/api/auth/logout", ""); status != http.StatusNoContent {
		t.Fatalf("sign out = %d", status)
	}
	if status, _ := call(t, c, "GET", srv.URL+"/api/auth/session", ""); status != http.StatusUnauthorized {
		t.Fatalf("after signing out: %d", status)
	}
}

// What the plan names, by route: nothing a visitor does reaches past the
// simulator. Each answers 403 demo_read_only with the sentence.
func TestPublicDemoRefusesWithASentence(t *testing.T) {
	_, srv, c := publicDemo(t)
	call(t, c, "POST", srv.URL+"/api/auth/demo", "")
	for _, x := range []struct{ method, path, body string }{
		{"POST", "/api/integrations/instances", `{"integration":"checks","name":"Web","config":{}}`},
		{"PUT", "/api/integrations/instances/i_1", `{}`},
		{"POST", "/api/import/homepage?dry_run=1", `{}`},
		{"POST", "/api/specs/installed", `{"yaml":""}`},
		{"POST", "/api/nodes/tokens", `{}`},
		{"POST", "/api/tokens", `{"name":"x","scopes":["graph.read"]}`},
		{"POST", "/api/users", `{"username":"x","password":"xxxxxxxx","role":"admin"}`},
		{"PUT", "/api/settings/security", `{"posture":"strict"}`},
		{"PUT", "/api/settings/dozzle", `{"url":"https://logs.home.example"}`},
		{"PUT", "/api/stacks/local/web/sync", `{}`},
		{"POST", "/api/notify/channels", `{}`},
		{"POST", "/api/setup", `{"password":"xxxxxxxx"}`},
		{"POST", "/api/auth/login", `{"username":"visitor","password":"xxxxxxxx"}`},
	} {
		status, m := call(t, c, x.method, srv.URL+x.path, x.body)
		e, _ := m["error"].(map[string]any)
		if status != http.StatusForbidden || e["code"] != "demo_read_only" || e["id"] != "demo.read_only" || !strings.Contains(e["message"].(string), "public demo") {
			t.Errorf("%s %s = %d %v", x.method, x.path, status, m)
		}
	}
}

// The public demo won't start on a data directory that holds integrations:
// they'd run on a public host.
func TestPublicDemoNeedsAnEmptyDataDir(t *testing.T) {
	dir := t.TempDir()
	a, err := newApp(t.Context(), testConfig(t, dir), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.integrations.Create(t.Context(), runtime.CreateRequest{Integration: "checks", Name: "Checks",
		Config: map[string]any{"targets": []any{map[string]any{"name": "Web", "kind": "http", "url": "https://web.home.example"}}}}); err != nil {
		t.Fatal(err)
	}
	a.close()
	cfg := testConfig(t, dir)
	cfg.Demo, cfg.DemoPublic, cfg.DemoAt, cfg.DemoSeed = true, true, config.DefaultDemoAt, 1
	if _, err := newApp(t.Context(), cfg, slog.New(slog.DiscardHandler)); err == nil || !strings.Contains(err.Error(), "empty data directory") {
		t.Fatalf("err = %v", err)
	}
}
