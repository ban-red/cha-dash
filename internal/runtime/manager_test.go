package runtime

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/store"
	"dash.cha.sh/internal/topics"
	"dash.cha.sh/internal/vault"
)

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// echo is a fake integration for an upstream that wants X-Api-Key: it
// reads /version and /data, logs its key (to prove the log scrubs it) and
// quotes the upstream's error body, key included, when it fails.
type echo struct{}

func (echo) Manifest() integration.Manifest {
	return integration.Manifest{
		ID: "echo", Name: "Echo", Icon: "echo", Version: "0.1.0", Tier: integration.Bronze,
		Capabilities: []integration.Capability{"primitives.stat"},
		ConfigSchema: integration.Object(integration.URLProp(), integration.TLSProp(),
			integration.Prop("api_key", integration.Secret("API key")),
			integration.Prop("password", integration.Secret("Password")),
			integration.Prop("label", integration.String("Label")),
		).Require("url", "api_key"),
		Poll: integration.Poll{Interval: 5 * time.Second, Idle: 5 * time.Second},
	}
}

func (echo) Connect(_ context.Context, cfg integration.Config, d integration.Deps) (integration.Instance, error) {
	d.Log.Info("connecting with key " + cfg.String("api_key"))
	return &echoInst{url: cfg.String("url"), key: cfg.String("api_key"), d: d}, nil
}

type echoInst struct {
	url, key string
	d        integration.Deps
}

func (e *echoInst) get(ctx context.Context, path string) (string, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", e.url+path, nil)
	req.Header.Set("X-Api-Key", e.key)
	resp, err := e.d.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == 500 {
		return "", fmt.Errorf("upstream said: %s", b)
	}
	if err := integration.CheckResponse(resp); err != nil {
		return "", err
	}
	return string(b), nil
}

func (e *echoInst) Test(ctx context.Context) integration.TestResult {
	v, err := e.get(ctx, "/version")
	if err != nil {
		return integration.Failed(err)
	}
	return integration.TestResult{OK: true, Version: v, Grants: []integration.Grant{{Name: "read"}, {Name: "write", Write: true}}}
}

func (e *echoInst) Sync(ctx context.Context, _ integration.Interest) error {
	e.d.Log.Debug("syncing", "key", e.key)
	data, err := e.get(ctx, "/data")
	if err != nil {
		return err
	}
	e.d.Emit.SetVersion("1.0")
	return e.d.Emit.Replace("", integration.Facts{Resources: []integration.Resource{
		{ID: "echo:" + e.d.Emit.Self()[len("integration:"):] + "/app", Kind: "app", Name: data, Health: integration.Health{Status: "ok"}},
	}})
}

func (e *echoInst) Close() error { return nil }

// upstream is an httptest server that wants X-Api-Key == key.
type upstream struct {
	*httptest.Server
	mu  sync.Mutex
	key string
	err string // when set, /data answers 500 with this body
}

func newUpstream(t *testing.T, key string, useTLS bool) *upstream {
	u := &upstream{key: key}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		key, fail := u.key, u.err
		u.mu.Unlock()
		if r.Header.Get("X-Api-Key") != key {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/version":
			io.WriteString(w, "4.2.0")
		case "/data":
			if fail != "" {
				w.WriteHeader(500)
				io.WriteString(w, fail)
				return
			}
			io.WriteString(w, "Echo app")
		}
	})
	if useTLS {
		u.Server = httptest.NewUnstartedServer(h)
		u.Config.ErrorLog = quiet
		u.StartTLS()
	} else {
		u.Server = httptest.NewServer(h)
	}
	t.Cleanup(u.Close)
	return u
}

type env struct {
	m    *Manager
	st   *store.Store
	v    *vault.Vault
	set  *topics.Set
	logs *lockedBuffer
	srv  *httptest.Server
}

func newEnv(t *testing.T, extra ...integration.Integration) *env {
	t.Helper()
	st, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "chadash.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	rand.Read(key)
	v, err := vault.New(st, key)
	if err != nil {
		t.Fatal(err)
	}
	reg := integration.NewRegistry()
	for _, i := range append([]integration.Integration{echo{}}, extra...) {
		if err := reg.Register(i); err != nil {
			t.Fatal(err)
		}
	}
	logs := &lockedBuffer{}
	set := topics.New(graph.New(), time.Now())
	m, err := New(Options{Store: st, Vault: v, Registry: reg, Topics: set,
		Log: slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	mux := http.NewServeMux()
	m.Register(mux, func(h http.Handler) http.Handler { return h })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &env{m: m, st: st, v: v, set: set, logs: logs, srv: srv}
}

// call makes a JSON request and returns the status and the raw body.
func (e *env) call(t *testing.T, method, path string, body any) (int, string) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func decodeAs[T any](t *testing.T, s string) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("%s: %v", s, err)
	}
	return v
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (e *env) status(id string) string {
	v, err := e.m.Get(id)
	if err != nil {
		return "missing"
	}
	return v.Status
}

func storedConfig(t *testing.T, st *store.Store, id string) string {
	t.Helper()
	var cfg string
	if err := st.Reader().QueryRow(`SELECT config FROM integration_instances WHERE id = ?`, id).Scan(&cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

const secret = "s3cret/api+key=0001" // base64-ish: query escaping changes it

func TestInstancesCRUD(t *testing.T) {
	e := newEnv(t)
	up := newUpstream(t, secret, false)

	code, body := e.call(t, "GET", "/api/integrations/catalog", nil)
	cat := decodeAs[[]map[string]any](t, body)
	if code != 200 || len(cat) != 1 || cat[0]["id"] != "echo" || cat[0]["tier"] != "bronze" ||
		!strings.Contains(body, `"api_key":{"type":"string","title":"API key","minLength":1,"maxLength":8192,"writeOnly":true}`) {
		t.Fatalf("catalog: %d %s", code, body)
	}

	code, body = e.call(t, "POST", "/api/integrations/instances", map[string]any{
		"integration": "echo", "name": "Echo", "config": map[string]any{"url": up.URL, "label": ""}, "secrets": map[string]any{"api_key": secret},
	})
	if code != 201 {
		t.Fatalf("create: %d %s", code, body)
	}
	id := decodeAs[map[string]string](t, body)["id"]
	if !strings.HasPrefix(id, "i_") {
		t.Fatalf("id = %q", id)
	}
	// Stored as a reference; the value is in the vault.
	cfg := storedConfig(t, e.st, id)
	if strings.Contains(cfg, secret) || !strings.Contains(cfg, `"api_key":"${secret:instance/`+id+`/api_key}"`) {
		t.Fatalf("stored config = %s", cfg)
	}
	names, _ := e.v.List(t.Context())
	if len(names) != 1 {
		t.Fatalf("vault = %v", names)
	}

	waitFor(t, "the instance to sync", func() bool { return e.status(id) == StatusOK })
	code, body = e.call(t, "GET", "/api/integrations/instances", nil)
	list := decodeAs[[]InstanceView](t, body)
	if code != 200 || len(list) != 1 || strings.Contains(body, secret) {
		t.Fatalf("list: %d %s", code, body)
	}
	got := list[0]
	if got.ID != id || got.Integration != "echo" || got.Name != "Echo" || !got.Enabled || got.Version != "1.0" ||
		got.LastOK == nil || got.LastError != nil || len(got.Warnings) != 0 {
		t.Fatalf("instance = %+v", got)
	}
	if got.Config["url"] != up.URL || got.Config["api_key"] != nil || got.Config["label"] != nil {
		t.Fatalf("config = %v", got.Config)
	}
	if !got.Secrets["api_key"].Set || got.Secrets["password"].Set || len(got.Secrets) != 2 {
		t.Fatalf("secrets = %v", got.Secrets)
	}
	if !strings.Contains(body, `"secrets":{"api_key":{"set":true},"password":{"set":false}}`) {
		t.Fatalf("secrets on the wire: %s", body)
	}
	if _, ok := e.set.Graph.Get(graph.RID("echo:" + id + "/app")); !ok {
		t.Fatal("the instance should publish into the graph")
	}
	if r, ok := e.set.Graph.Get(graph.RID("integration:" + id)); !ok || r.Name != "Echo" || r.Attrs["type"] != "echo" {
		t.Fatalf("instance resource = %+v", r)
	}

	// Rotate the key: a new vault entry, the old one deleted, config kept.
	up.mu.Lock()
	up.key = "rotated-key-0002"
	up.mu.Unlock()
	code, body = e.call(t, "PUT", "/api/integrations/instances/"+id, map[string]any{"secrets": map[string]any{"api_key": "rotated-key-0002"}})
	if code != 200 || strings.Contains(body, "rotated-key-0002") {
		t.Fatalf("rotate: %d %s", code, body)
	}
	names2, _ := e.v.List(t.Context())
	if val, _ := e.v.Get(t.Context(), "instance/"+id+"/api_key"); len(names2) != 1 || names2[0] != names[0] || string(val) != "rotated-key-0002" {
		t.Fatalf("vault after rotation = %v (was %v), value %q", names2, names, val)
	}
	if v := decodeAs[InstanceView](t, body); v.Config["url"] != up.URL || !v.Secrets["api_key"].Set {
		t.Fatalf("after rotation = %+v", v)
	}
	waitFor(t, "a sync with the new key", func() bool { return e.status(id) == StatusOK })

	// Edit the config only: the secret stays.
	code, body = e.call(t, "PUT", "/api/integrations/instances/"+id, map[string]any{"name": "Echo 2", "config": map[string]any{"url": up.URL, "label": "x"}})
	if v := decodeAs[InstanceView](t, body); code != 200 || v.Name != "Echo 2" || v.Config["label"] != "x" || !v.Secrets["api_key"].Set {
		t.Fatalf("edit: %d %s", code, body)
	}
	// Set and clear an optional secret.
	_, body = e.call(t, "PUT", "/api/integrations/instances/"+id, map[string]any{"secrets": map[string]any{"password": "p4ssword-0003"}})
	if !decodeAs[InstanceView](t, body).Secrets["password"].Set {
		t.Fatal("password should be set")
	}
	_, body = e.call(t, "PUT", "/api/integrations/instances/"+id, map[string]any{"secrets": map[string]any{"password": nil}})
	if decodeAs[InstanceView](t, body).Secrets["password"].Set {
		t.Fatal("password should be cleared")
	}
	if names, _ := e.v.List(t.Context()); len(names) != 1 {
		t.Fatalf("the cleared password should leave the vault: %v", names)
	}

	// Disable: stops, and its facts leave the graph.
	_, body = e.call(t, "PUT", "/api/integrations/instances/"+id, map[string]any{"enabled": false})
	if v := decodeAs[InstanceView](t, body); v.Status != StatusDisabled || v.Enabled {
		t.Fatalf("disabled = %+v", v)
	}
	if _, ok := e.set.Graph.Get(graph.RID("integration:" + id)); ok {
		t.Fatal("a disabled instance publishes nothing")
	}
	e.call(t, "PUT", "/api/integrations/instances/"+id, map[string]any{"enabled": true})
	waitFor(t, "re-enabled", func() bool { return e.status(id) == StatusOK })

	// Single GET, then delete: row, facts and secrets all go.
	if code, body := e.call(t, "GET", "/api/integrations/instances/"+id, nil); code != 200 || decodeAs[InstanceView](t, body).ID != id {
		t.Fatalf("get: %d %s", code, body)
	}
	if code, _ := e.call(t, "DELETE", "/api/integrations/instances/"+id, nil); code != 204 {
		t.Fatalf("delete: %d", code)
	}
	if _, body := e.call(t, "GET", "/api/integrations/instances", nil); body != "[]\n" {
		t.Fatalf("after delete: %s", body)
	}
	if names, _ := e.v.List(t.Context()); len(names) != 0 {
		t.Fatalf("secrets left behind: %v", names)
	}
	if _, doc := e.set.Graph.Snapshot(nil); len(doc.Resources) != 0 {
		t.Fatalf("facts left behind: %v", doc.Resources)
	}
	for _, r := range []struct{ method, path string }{
		{"GET", "/api/integrations/instances/" + id}, {"PUT", "/api/integrations/instances/" + id},
		{"DELETE", "/api/integrations/instances/" + id}, {"POST", "/api/integrations/instances/" + id + "/test"},
		{"GET", "/api/integrations/instances/" + id + "/diagnostics"},
	} {
		if code, body := e.call(t, r.method, r.path, map[string]any{}); code != 404 || !strings.Contains(body, `"not_found"`) {
			t.Errorf("%s %s: %d %s", r.method, r.path, code, body)
		}
	}
}

func TestInstancesValidation(t *testing.T) {
	e := newEnv(t)
	ok := func() map[string]any {
		return map[string]any{"integration": "echo", "name": "Echo",
			"config": map[string]any{"url": "https://echo.lab.example"}, "secrets": map[string]any{"api_key": secret}}
	}
	cases := []struct {
		name   string
		mutate func(map[string]any)
		status int
		code   string
		path   string
	}{
		{"missing url", func(b map[string]any) { delete(b["config"].(map[string]any), "url") }, 422, "invalid", "/url"},
		{"bad url", func(b map[string]any) { b["config"].(map[string]any)["url"] = "echo.lab.example" }, 422, "invalid", "/url"},
		{"credentials in url", func(b map[string]any) { b["config"].(map[string]any)["url"] = "https://u:p@echo.lab.example" }, 422, "invalid", "/url"},
		{"missing secret", func(b map[string]any) { delete(b, "secrets") }, 422, "invalid", "/api_key"},
		{"secret in config", func(b map[string]any) { b["config"].(map[string]any)["api_key"] = secret }, 422, "invalid", "/api_key"},
		{"unknown secret", func(b map[string]any) { b["secrets"].(map[string]any)["label"] = "x" }, 422, "invalid", "/label"},
		{"secret not a string", func(b map[string]any) { b["secrets"].(map[string]any)["api_key"] = 42 }, 422, "invalid", "/api_key"},
		{"unknown field", func(b map[string]any) { b["config"].(map[string]any)["colour"] = "red" }, 422, "invalid", "/colour"},
		{"secret reference", func(b map[string]any) { b["config"].(map[string]any)["label"] = "${secret:other}" }, 422, "invalid", "/label"},
		{"pin without pin", func(b map[string]any) { b["config"].(map[string]any)["tls"] = map[string]any{"mode": "pin"} }, 422, "invalid", "/tls"},
		{"bad tls mode", func(b map[string]any) { b["config"].(map[string]any)["tls"] = map[string]any{"mode": "maybe"} }, 422, "invalid", "/tls/mode"},
		{"empty name", func(b map[string]any) { b["name"] = "  " }, 422, "invalid", "/name"},
		{"unknown integration", func(b map[string]any) { b["integration"] = "proxmox" }, 422, "unknown_integration", ""},
	}
	for _, c := range cases {
		b := ok()
		c.mutate(b)
		code, body := e.call(t, "POST", "/api/integrations/instances", b)
		if code != c.status || !strings.Contains(body, `"code":"`+c.code+`"`) {
			t.Errorf("%s: %d %s", c.name, code, body)
			continue
		}
		if c.path != "" {
			fields := decodeAs[invalidBody](t, body).Fields
			if len(fields) == 0 || fields[0].Path != c.path {
				t.Errorf("%s: fields %v, want %s", c.name, fields, c.path)
			}
		}
		if strings.Contains(body, secret) {
			t.Errorf("%s: the response echoes the secret: %s", c.name, body)
		}
	}
	if code, body := e.call(t, "POST", "/api/integrations/instances", ok()); code != 201 {
		t.Fatalf("valid: %d %s", code, body)
	}
	if code, body := e.call(t, "POST", "/api/integrations/instances", ok()); code != 409 || !strings.Contains(body, `"conflict"`) {
		t.Fatalf("duplicate name: %d %s", code, body)
	}
	if names, _ := e.v.List(t.Context()); len(names) != 1 {
		t.Fatalf("refused creates must not leave secrets: %v", names)
	}
	// A refused update (the name is taken) puts the previous secret back.
	b := ok()
	b["name"] = "Other"
	_, body := e.call(t, "POST", "/api/integrations/instances", b)
	other := decodeAs[map[string]string](t, body)["id"]
	code, body := e.call(t, "PUT", "/api/integrations/instances/"+other, map[string]any{"name": "Echo", "secrets": map[string]any{"api_key": "new-key-0005"}})
	if code != 409 {
		t.Fatalf("rename onto a taken name: %d %s", code, body)
	}
	if v, _ := e.v.Get(t.Context(), "instance/"+other+"/api_key"); string(v) != secret {
		t.Fatalf("the refused update changed the secret to %q", v)
	}

	// Transport-level refusals.
	req, _ := http.NewRequest("POST", e.srv.URL+"/api/integrations/instances", strings.NewReader("integration=echo"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if resp, _ := http.DefaultClient.Do(req); resp.StatusCode != 415 {
		t.Fatalf("form body: %d", resp.StatusCode)
	}
	req, _ = http.NewRequest("POST", e.srv.URL+"/api/integrations/instances", strings.NewReader("{"))
	req.Header.Set("Content-Type", "application/json")
	if resp, _ := http.DefaultClient.Do(req); resp.StatusCode != 400 {
		t.Fatalf("bad JSON: %d", resp.StatusCode)
	}
}

// TestSecretsNeverLeak plants the secret in every place an integration could
// echo it (its logs, its errors, the upstream's error body) and checks that
// no response, log line or diagnostics bundle carries it.
func TestSecretsNeverLeak(t *testing.T) {
	e := newEnv(t)
	up := newUpstream(t, secret, false)
	up.err = "invalid key " + secret + " (in the query: apikey=" + url.QueryEscape(secret) + ")"

	var bodies []string
	call := func(method, path string, body any) string {
		_, b := e.call(t, method, path, body)
		bodies = append(bodies, b)
		return b
	}
	cfg := map[string]any{"url": up.URL}
	id := decodeAs[map[string]string](t, call("POST", "/api/integrations/instances", map[string]any{
		"integration": "echo", "name": "Echo", "config": cfg, "secrets": map[string]any{"api_key": secret},
	}))["id"]
	waitFor(t, "a failed sync", func() bool { v, _ := e.m.Get(id); return v.LastError != nil })
	v := decodeAs[InstanceView](t, call("GET", "/api/integrations/instances/"+id, nil))
	if !strings.Contains(*v.LastError, "invalid key [redacted]") || *v.LastErrorCode != integration.CodeUpstream || v.LastErrorAt == nil {
		t.Fatalf("last_error = %v", *v.LastError)
	}
	call("GET", "/api/integrations/instances", nil)
	call("GET", "/api/integrations/instances/"+id+"/diagnostics", nil)
	call("POST", "/api/integrations/instances/"+id+"/test", nil)
	call("POST", "/api/integrations/test", map[string]any{"integration": "echo", "config": cfg, "secrets": map[string]any{"api_key": secret}})
	// Wrong key over the test endpoint: the failure message mustn't quote it.
	call("POST", "/api/integrations/test", map[string]any{"integration": "echo", "config": cfg, "secrets": map[string]any{"api_key": "wrong-key-0004"}})
	call("PUT", "/api/integrations/instances/"+id, map[string]any{"secrets": map[string]any{"api_key": secret + "-v2"}})

	for _, b := range bodies {
		if strings.Contains(b, secret) || strings.Contains(b, "wrong-key-0004") {
			t.Errorf("a response carries a secret: %s", b)
		}
	}
	logs := e.logs.String()
	if !strings.Contains(logs, "connecting with key [redacted]") {
		t.Fatalf("the integration's log line should be there, scrubbed:\n%s", logs)
	}
	if strings.Contains(logs, secret) || strings.Contains(logs, "wrong-key-0004") || strings.Contains(logs, url.QueryEscape(secret)) {
		t.Fatalf("the logs carry a secret:\n%s", logs)
	}
}

func TestTestEndpoints(t *testing.T) {
	e := newEnv(t)
	up := newUpstream(t, secret, false)
	cfg := map[string]any{"url": up.URL}

	code, body := e.call(t, "POST", "/api/integrations/test", map[string]any{"integration": "echo", "config": cfg, "secrets": map[string]any{"api_key": secret}})
	res := decodeAs[map[string]any](t, body)
	if code != 200 || res["ok"] != true || res["version"] != "4.2.0" || res["error"] != nil {
		t.Fatalf("test: %d %s", code, body)
	}
	if _, ok := res["latency_ms"].(float64); !ok {
		t.Fatalf("latency_ms: %s", body)
	}
	if !strings.Contains(body, `"grants":[{"name":"read","access":"read","write":false},{"name":"write","access":"write","write":true}]`) ||
		!strings.Contains(body, `"code":"write_grant"`) || !strings.Contains(body, `"code":"plain_http"`) {
		t.Fatalf("grants and warnings: %s", body)
	}
	if n, _ := e.v.List(t.Context()); len(n) != 0 {
		t.Fatal("an unsaved test stores nothing")
	}

	code, body = e.call(t, "POST", "/api/integrations/test", map[string]any{"integration": "echo", "config": cfg, "secrets": map[string]any{"api_key": "nope-nope"}})
	res = decodeAs[map[string]any](t, body)
	if code != 200 || res["ok"] != false || res["error"].(map[string]any)["code"] != integration.CodeAuth {
		t.Fatalf("wrong key: %d %s", code, body)
	}
	code, body = e.call(t, "POST", "/api/integrations/test", map[string]any{"integration": "echo", "config": map[string]any{}})
	if code != 422 {
		t.Fatalf("invalid test config: %d %s", code, body)
	}

	id, err := e.m.Create(t.Context(), CreateRequest{Integration: "echo", Name: "Echo", Config: cfg, Secrets: map[string]any{"api_key": secret}})
	if err != nil {
		t.Fatal(err)
	}
	code, body = e.call(t, "POST", "/api/integrations/instances/"+id+"/test", nil)
	if res := decodeAs[map[string]any](t, body); code != 200 || res["ok"] != true || res["version"] != "4.2.0" {
		t.Fatalf("saved test: %d %s", code, body)
	}

	// Unsaved edits of a saved instance: the secret not retyped comes from
	// the vault, and nothing is saved.
	other := newUpstream(t, secret, false)
	code, body = e.call(t, "POST", "/api/integrations/test", map[string]any{"instance_id": id, "config": map[string]any{"url": other.URL, "label": "edited"}})
	if res := decodeAs[map[string]any](t, body); code != 200 || res["ok"] != true {
		t.Fatalf("edit test: %d %s", code, body)
	}
	if v, _ := e.m.Get(id); v.Config["url"] != up.URL || v.Config["label"] != nil {
		t.Fatalf("an edit test must not save: %v", v.Config)
	}
	code, body = e.call(t, "POST", "/api/integrations/test", map[string]any{"instance_id": id, "secrets": map[string]any{"api_key": "retyped-wrong"}})
	if res := decodeAs[map[string]any](t, body); code != 200 || res["ok"] != false || res["error"].(map[string]any)["code"] != integration.CodeAuth {
		t.Fatalf("edit test with a retyped key: %d %s", code, body)
	}
	if code, _ := e.call(t, "POST", "/api/integrations/test", map[string]any{"instance_id": "i_nope"}); code != 404 {
		t.Fatalf("unknown instance: %d", code)
	}
	if code, _ := e.call(t, "POST", "/api/integrations/test", map[string]any{"instance_id": id, "integration": "checks"}); code != 422 {
		t.Fatalf("mismatched integration: %d", code)
	}
}

func TestTOFUPinnedOnFirstSuccess(t *testing.T) {
	e := newEnv(t)
	up := newUpstream(t, secret, true)
	leaf := integration.Fingerprint(up.Certificate())
	cfg := map[string]any{"url": up.URL, "tls": map[string]any{"mode": "tofu"}}

	// Unsaved: the certificate is reported, nothing is pinned anywhere.
	_, body := e.call(t, "POST", "/api/integrations/test", map[string]any{"integration": "echo", "config": cfg, "secrets": map[string]any{"api_key": secret}})
	if res := decodeAs[map[string]any](t, body); res["ok"] != true || res["certificate"].(map[string]any)["sha256"] != leaf {
		t.Fatalf("unsaved tofu test: %s", body)
	}
	// Verify mode against a self-signed server fails with the fingerprint
	// to offer.
	_, body = e.call(t, "POST", "/api/integrations/test", map[string]any{"integration": "echo",
		"config": map[string]any{"url": up.URL}, "secrets": map[string]any{"api_key": secret}})
	if res := decodeAs[map[string]any](t, body); res["ok"] != false || res["error"].(map[string]any)["code"] != integration.CodeTLSUntrusted ||
		res["certificate"].(map[string]any)["sha256"] != leaf {
		t.Fatalf("verify against self-signed: %s", body)
	}

	// Saved: the first successful handshake records the pin in the config.
	id, err := e.m.Create(t.Context(), CreateRequest{Integration: "echo", Name: "Echo", Config: cfg, Secrets: map[string]any{"api_key": secret}})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the pin to be recorded", func() bool {
		v, _ := e.m.Get(id)
		tls, _ := v.Config["tls"].(map[string]any)
		return tls["pin"] == leaf
	})
	if !strings.Contains(storedConfig(t, e.st, id), leaf) {
		t.Fatal("the pin should be in the stored config")
	}
	waitFor(t, "ok", func() bool { return e.status(id) == StatusOK })

	// A different certificate under the same pin is refused: simulate it by
	// pinning something else.
	wrong := strings.Repeat("ab", 32)
	if err := e.m.Update(t.Context(), id, UpdateRequest{Config: map[string]any{"url": up.URL, "tls": map[string]any{"mode": "tofu", "pin": wrong}}}); err != nil {
		t.Fatal(err)
	}
	res, err := e.m.TestSaved(t.Context(), id)
	if err != nil || res.OK || res.ErrorCode != integration.CodeTLSPinMismatch {
		t.Fatalf("pin mismatch: %+v %v", res, err)
	}
	waitFor(t, "the instance to need repair", func() bool { return e.status(id) == StatusRepair })
	in := incident(t, e.set, id+".repair")
	if in.Scenario != "tls-pin-mismatch" || in.Status != "open" || in.Phase != PhaseOpen || in.Target != "integration:"+id ||
		in.Facts["instance"] != id || in.Facts["field"] != "tls" {
		t.Fatalf("repair = %+v", in)
	}
	if v, _ := e.m.Get(id); v.Config["tls"].(map[string]any)["pin"] != wrong {
		t.Fatal("a mismatch must not re-pin")
	}
	d, _ := e.m.Diagnostics(id)
	if d.TLS.Mode != "tofu" || d.TLS.Pin != wrong || len(d.Attempts) == 0 {
		t.Fatalf("diagnostics = %+v", d)
	}
}

// TestReload is the importer's path: rows and vault entries written
// directly, then Reload.
func TestReload(t *testing.T) {
	e := newEnv(t)
	up := newUpstream(t, secret, false)
	if err := e.v.Put(t.Context(), "homepage/echo/key", []byte(secret)); err != nil {
		t.Fatal(err)
	}
	cfg, _ := json.Marshal(map[string]any{"url": up.URL, "api_key": integration.SecretRef("homepage/echo/key")})
	now := store.NowMillis()
	for _, r := range [][]any{
		{"i_imported", "echo", "Imported", string(cfg), 1, now, now},
		{"i_later", "proxmox", "pluto", `{"url":"https://pluto.lab.example:8006","token_secret":"${secret:homepage/pve/secret}"}`, 1, now, now},
		{"i_nokey", "echo", "No key", `{"url":"https://echo.lab.example"}`, 1, now, now}, // the importer couldn't find its key
	} {
		if _, err := e.st.Writer().Exec(`INSERT INTO integration_instances (id, integration, name, config, enabled, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, r...); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.m.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the imported instance to run", func() bool { return e.status("i_imported") == StatusOK })
	v, _ := e.m.Get("i_imported")
	if !v.Secrets["api_key"].Set || v.Config["api_key"] != nil {
		t.Fatalf("imported = %+v", v)
	}
	// A row that doesn't validate doesn't start, and says why.
	nokey, _ := e.m.Get("i_nokey")
	if nokey.Status != StatusError || nokey.LastError == nil || *nokey.LastErrorCode != integration.CodeInvalidConfig ||
		!strings.Contains(*nokey.LastError, "/api_key: is required") {
		t.Fatalf("invalid imported row = %+v", nokey)
	}
	// An integration this build doesn't have yet is listed, as an error.
	later, _ := e.m.Get("i_later")
	if later.Status != StatusError || later.LastError == nil || !later.Secrets["token_secret"].Set || later.Config["token_secret"] != nil {
		t.Fatalf("unknown integration = %+v", later)
	}

	// Deleting the instance deletes the vault entry nobody else uses.
	if err := e.m.Delete(t.Context(), "i_imported"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.v.Get(t.Context(), "homepage/echo/key"); err == nil {
		t.Fatal("the imported secret should be gone")
	}
	// A row removed behind the manager's back stops on Reload.
	e.st.Writer().Exec(`DELETE FROM integration_instances WHERE id = 'i_later'`)
	if err := e.m.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Get("i_later"); err == nil {
		t.Fatal("i_later should be gone")
	}
}
