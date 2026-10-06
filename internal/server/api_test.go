package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"
	"time"

	"dash.cha.sh/internal/demo"
	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/topics"
	"dash.cha.sh/internal/vault"
)

// signIn completes first-run setup and returns a client holding the owner's
// session.
func signIn(t *testing.T, e *env) *http.Client {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	resp := e.request(t, c, "POST", "/api/setup", map[string]string{"token": e.token, "username": "owner", "password": "correct horse battery"}, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("setup: %d", resp.StatusCode)
	}
	return c
}

func decode(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	b, _ := io.ReadAll(resp.Body)
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%d %s: %v", resp.StatusCode, b, err)
	}
	return m
}

func TestBoardsAPI(t *testing.T) {
	e := newEnv(t)
	anon := &http.Client{}
	for _, r := range []struct{ method, path string }{
		{"GET", "/api/boards"}, {"GET", "/api/boards/x/layout"}, {"PUT", "/api/boards/x/layout"},
	} {
		if resp := e.request(t, anon, r.method, r.path, map[string]any{}, nil); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s %s without a session: %d", r.method, r.path, resp.StatusCode)
		}
	}

	c := signIn(t, e)
	resp := e.request(t, c, "GET", "/api/boards", nil, nil)
	if resp.StatusCode != 200 || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("boards: %d %v", resp.StatusCode, resp.Header)
	}
	boards := decode(t, resp)["boards"].([]any)
	if len(boards) != 1 || boards[0].(map[string]any)["name"] != "Home" {
		t.Fatalf("boards = %v", boards)
	}
	path := "/api/boards/" + boards[0].(map[string]any)["id"].(string) + "/layout"

	if m := decode(t, e.request(t, c, "GET", path, nil, nil)); m["revision"] != 0.0 || m["layout"] != nil {
		t.Fatalf("empty layout = %v", m)
	}
	save := map[string]any{"revision": 0, "format_version": 1, "layout": map[string]any{"version": 1, "columns": 12, "sections": []any{}}}
	// A cross-site save is refused before it reaches the handler.
	if resp := e.request(t, c, "PUT", path, save, map[string]string{"Origin": "https://evil.example", "Sec-Fetch-Site": "cross-site"}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-site save: %d", resp.StatusCode)
	}
	if m := decode(t, e.request(t, c, "PUT", path, save, nil)); m["revision"] != 1.0 {
		t.Fatalf("save = %v", m)
	}
	resp = e.request(t, c, "PUT", path, save, nil)
	if m := decode(t, resp); resp.StatusCode != http.StatusConflict || m["revision"] != 1.0 {
		t.Fatalf("stale save: %d %v", resp.StatusCode, m)
	}
	if resp := e.request(t, c, "GET", "/api/boards/b_missing/layout", nil, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing board: %d", resp.StatusCode)
	}
}

func TestDemoEndpointsNeedDemoMode(t *testing.T) {
	e := newEnv(t)
	c := signIn(t, e)
	for _, r := range []struct{ method, path string }{
		{"GET", "/api/demo"},
	} {
		resp := e.request(t, c, r.method, r.path, nil, nil)
		if resp.StatusCode != http.StatusNotFound || decode(t, resp)["error"].(map[string]any)["code"] != "not_found" {
			t.Fatalf("%s %s outside demo mode: %d", r.method, r.path, resp.StatusCode)
		}
	}
}

func TestDemoEndpoints(t *testing.T) {
	wall := time.Now()
	drv, err := demo.New(topics.New(graph.New(), wall), demo.Options{Seed: 5, At: demo.DefaultAt, Clock: func() time.Time { return wall }})
	if err != nil {
		t.Fatal(err)
	}
	e := newEnvWith(t, func(d *Deps) { d.Demo = drv })
	anon := &http.Client{}
	if resp := e.request(t, anon, "GET", "/api/demo", nil, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("demo info without a session: %d", resp.StatusCode)
	}

	c := signIn(t, e)
	m := decode(t, e.request(t, c, "GET", "/api/demo", nil, nil))
	if m["enabled"] != true || m["now"] != "2026-10-03T14:05:00Z" || m["seed"] != 5.0 {
		t.Fatalf("demo info = %v", m)
	}
	// Fixes run on the engine; the demo's own fix endpoint is gone.
	if resp := e.request(t, c, "POST", "/api/demo/incidents/plex-token-revoked/fix", nil, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("the removed fix endpoint: %d", resp.StatusCode)
	}
}

func TestImportRoutes(t *testing.T) {
	body := map[string]string{"services_yaml": "- Links:\n  - Example:\n      href: https://example.com\n"}

	// Without a vault the import routes aren't mounted.
	e := newEnv(t)
	c := signIn(t, e)
	if resp := e.request(t, c, "POST", "/api/import/homepage?dry_run=1", body, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("import without a vault: %d", resp.StatusCode)
	}

	e = newEnvWith(t, func(d *Deps) {
		v, err := vault.New(d.Store, make([]byte, 32))
		if err != nil {
			t.Fatal(err)
		}
		d.Vault = v
	})
	anon := &http.Client{}
	for _, r := range []struct{ method, path string }{
		{"POST", "/api/import/homepage?dry_run=1"}, {"POST", "/api/import/homepage"}, {"GET", "/api/import/homepage"},
	} {
		if resp := e.request(t, anon, r.method, r.path, body, nil); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s %s without a session: %d", r.method, r.path, resp.StatusCode)
		}
	}
	c = signIn(t, e)
	if resp := e.request(t, c, "POST", "/api/import/homepage", body, map[string]string{"Origin": "https://evil.example", "Sec-Fetch-Site": "cross-site"}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-site import: %d", resp.StatusCode)
	}
	m := decode(t, e.request(t, c, "POST", "/api/import/homepage?dry_run=1", body, nil))
	if _, ok := m["plan"].(map[string]any); !ok {
		t.Fatalf("dry run = %v", m)
	}
	m = decode(t, e.request(t, c, "POST", "/api/import/homepage", body, nil))
	if id, _ := m["board_id"].(string); !strings.HasPrefix(id, "b_") {
		t.Fatalf("apply = %v", m)
	}
	boards := decode(t, e.request(t, c, "GET", "/api/boards", nil, nil))["boards"].([]any)
	if len(boards) != 1 || boards[0].(map[string]any)["name"] != "Homepage" {
		t.Fatalf("boards after import = %v", boards)
	}
}
