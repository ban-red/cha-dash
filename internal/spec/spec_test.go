package spec

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/integration/integrationtest"
)

const base = `apiVersion: chadash/v1alpha1
kind: Integration
meta: { id: widgets, name: Widgets, icon: widgets, version: 0.1.0, tier: bronze, capabilities: [primitives.stat] }
config:
  token: { type: secret, title: Token, required: true }
  site: { type: string, title: Site }
source:
  headers: { Authorization: "Bearer {{ secret.token }}" }
requests:
  info:
    path: /api/info?verbose=1
    query: { site: "{{ config.site }}", key: "{{ secret.token }}" }
    extract: { version: info.version, ok: info.healthy }
  stats:
    path: /api/{{ config.site }}/stats
    extract: { count: data.count, items: data.items.#, missing: data.nope }
  extra:
    path: /api/extra
    optional: true
    extract: { n: n }
version: info.version
resources:
  - id: app
    kind: app
    name: '"Widgets " + instance.name'
    health: 'info.ok ? "ok" : "down"'
    reason: 'info.ok ? "" : "unhealthy"'
    attrs: { items: stats.items, missing: stats.missing, extra: extra.n }
metrics:
  - { resource: app, key: widgets.count, value: stats.count, unit: count, max: 100 }
  - { resource: app, key: widgets.missing, value: stats.missing, unit: count, max: 100 }
repairs:
  - { when: 'info.version == "9.9"', code: unsupported_version, message: '"version " + info.version + " is too new"' }
`

func mustParse(t *testing.T, src string) *Spec {
	t.Helper()
	s, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestParse(t *testing.T) {
	s := mustParse(t, base)
	m := s.Manifest()
	if m.ID != "widgets" || m.Engine != integration.EngineSpec || m.Poll.Interval != DefaultEvery || !m.IsSecret("token") || m.IsSecret("site") {
		t.Fatalf("manifest = %+v", m)
	}
	b, _ := json.Marshal(m.ConfigSchema)
	if !strings.HasPrefix(string(b), `{"type":"object","properties":{"url":`) || !strings.Contains(string(b), `"required":["url","token"]`) {
		t.Fatalf("schema = %s", b)
	}

	bad := map[string]struct{ from, to string }{
		"api version":       {"chadash/v1alpha1", "chadash/v1"},
		"kind":              {"kind: Integration", "kind: Widget"},
		"unknown key":       {"\nversion: info.version", "\nversion: info.version\nfrobnicate: true"},
		"secret in path":    {"/api/{{ config.site }}/stats", "/api/{{ secret.token }}/stats"},
		"config as secret":  {`key: "{{ secret.token }}"`, `key: "{{ secret.site }}"`},
		"secret as config":  {`site: "{{ config.site }}"`, `site: "{{ config.token }}"`},
		"unknown template":  {`site: "{{ config.site }}"`, `site: "{{ env.HOME }}"`},
		"malformed":         {`site: "{{ config.site }}"`, `site: "{{ config.site"`},
		"absolute path":     {"path: /api/extra", "path: https://elsewhere.example/api"},
		"protocol relative": {"path: /api/extra", "path: //elsewhere.example/api"},
		"dot dot":           {"path: /api/extra", "path: /api/../../admin"},
		"bad expr":          {"\nversion: info.version", "\nversion: info.version +"},
		"unknown variable":  {"\nversion: info.version", "\nversion: nope.version"},
		"fast":              {"source:", "every: 1s\nsource:"},
		"slow timeout":      {`source:`, "source:\n  timeout: 5m"},
		"metric resource":   {"resource: app, key: widgets.count", "resource: web, key: widgets.count"},
		"duplicate metric":  {"key: widgets.missing", "key: widgets.count"},
		"reserved name":     {"  stats:\n", "  config:\n"},
		"no extract":        {"    extract: { n: n }", ""},
		"secret default":    {"title: Token, required: true", "title: Token, default: x"},
		"field type":        {"site: { type: string", "site: { type: date"},
		"tier":              {"tier: bronze", "tier: platinum"},
		"health missing":    {`kind: app`, `kind: ""`},
		"body on get":       {"optional: true", "optional: true\n    body: '{}'"},
	}
	for name, c := range bad {
		if !strings.Contains(base, c.from) {
			t.Fatalf("%s: the base spec has no %q", name, c.from)
		}
		if _, err := Parse([]byte(strings.Replace(base, c.from, c.to, 1))); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

// widgets is a fake upstream for the base spec.
type widgets struct {
	mu      sync.Mutex
	version string
	healthy bool
	extra   bool
	stats   string // raw body for /stats
	queries []string
	auth    []string
}

func (w *widgets) serve(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		w.mu.Lock()
		defer w.mu.Unlock()
		w.auth = append(w.auth, r.Header.Get("Authorization"))
		switch r.URL.Path {
		case "/base/api/info":
			w.queries = append(w.queries, r.URL.RawQuery)
			json.NewEncoder(rw).Encode(map[string]any{"info": map[string]any{"version": w.version, "healthy": w.healthy}})
		case "/base/api/lab/stats":
			io.WriteString(rw, w.stats)
		case "/base/api/extra":
			if !w.extra {
				rw.WriteHeader(500)
				return
			}
			io.WriteString(rw, `{"n": 7}`)
		default:
			http.NotFound(rw, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestEngine(t *testing.T) {
	up := &widgets{version: "1.4", healthy: true, stats: `{"data":{"count":12,"items":[1,2,3]}}`}
	srv := up.serve(t)
	s := mustParse(t, base)
	h := integrationtest.New(t)
	inst := h.Connect(s, map[string]any{"url": srv.URL + "/base", "site": "lab"}, map[string]string{"token": "t0ken-value"})

	if res := inst.Test(t.Context()); !res.OK || res.Version != "1.4" {
		t.Fatalf("test = %+v", res)
	}
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	r, ok := h.Resource("widgets:i_test/app")
	if !ok || r.Name != "Widgets Test" || r.Health != "ok" || r.Attrs["items"] != 3.0 {
		t.Fatalf("resource = %+v", r)
	}
	if _, has := r.Attrs["missing"]; has {
		t.Fatal("a null attr is left out")
	}
	if _, has := r.Attrs["extra"]; has {
		t.Fatal("an optional request that failed contributes nothing")
	}
	if se := h.Series("widgets:i_test/app", "widgets.count"); se["value"] != 12.0 {
		t.Fatalf("count = %v", se)
	}
	if se := h.Series("widgets:i_test/app", "widgets.missing"); se["value"] != nil {
		t.Fatalf("a missing value is unknown: %v", se)
	}
	// Templates: the header and the query carry the secret, the path the
	// config; the path's own query survives.
	up.mu.Lock()
	if up.auth[0] != "Bearer t0ken-value" || up.queries[0] != "key=t0ken-value&site=lab&verbose=1" {
		t.Fatalf("auth %q, query %q", up.auth[0], up.queries[0])
	}
	up.healthy, up.extra = false, true
	up.mu.Unlock()
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	r, _ = h.Resource("widgets:i_test/app")
	if r.Health != "down" || r.Attrs["health.reason"] != "unhealthy" || r.Attrs["extra"] != 7.0 {
		t.Fatalf("unhealthy = %+v", r)
	}

	// A repair condition.
	up.mu.Lock()
	up.version = "9.9"
	up.mu.Unlock()
	err := inst.Sync(t.Context(), integrationtest.Watched)
	if code, class, msg := integration.Classify(err); code != integration.CodeUnsupportedVersion || class != integration.NeedsRepair || msg != "version 9.9 is too new" {
		t.Fatalf("repair: %s %d %q", code, class, msg)
	}
	up.mu.Lock()
	up.version = "1.4"
	up.stats = `<html>not json</html>`
	up.mu.Unlock()
	if err := inst.Sync(t.Context(), integrationtest.Watched); err == nil || !strings.Contains(err.Error(), "didn't answer JSON") {
		t.Fatalf("non-JSON: %v", err)
	}
	up.mu.Lock()
	up.stats = `{"data":{"pad":"` + strings.Repeat("x", MaxResponseSize) + `"}}`
	up.mu.Unlock()
	if err := inst.Sync(t.Context(), integrationtest.Watched); err == nil || !strings.Contains(err.Error(), "more than") {
		t.Fatalf("oversized: %v", err)
	}
}

// An old release often lacks an endpoint: when a repair already holds on
// what was read before the 404, it wins over "the API changed".
func TestRepairBeforeMissingEndpoint(t *testing.T) {
	up := &widgets{version: "9.9", healthy: true}
	srv := up.serve(t)
	h := integrationtest.New(t)
	gone := map[string]any{"url": srv.URL + "/base", "site": "gone"} // /base/api/gone/stats is a 404
	inst := h.Connect(mustParse(t, base), gone, map[string]string{"token": "t"})
	code, _, msg := integration.Classify(inst.Sync(t.Context(), integrationtest.Watched))
	if code != integration.CodeUnsupportedVersion || msg != "version 9.9 is too new" {
		t.Fatalf("repair before a 404: %s %q", code, msg)
	}
	res := inst.Test(t.Context())
	if res.OK || res.ErrorCode != integration.CodeUnsupportedVersion || res.Version != "9.9" {
		t.Fatalf("test = %+v", res)
	}

	// No repair holds: the 404 is the API changing, naming the version.
	up.mu.Lock()
	up.version = "1.4"
	up.mu.Unlock()
	code, _, msg = integration.Classify(inst.Sync(t.Context(), integrationtest.Watched))
	if code != integration.CodeAPIChanged || !strings.Contains(msg, "(detected version 1.4)") {
		t.Fatalf("404 without a repair: %s %q", code, msg)
	}

	// A condition that needs a request the 404 cut short can't be evaluated:
	// it's skipped, not a spec failure.
	partial := strings.Replace(base, `'info.version == "9.9"'`, `'stats.count > 5'`, 1)
	inst = integrationtest.New(t).Connect(mustParse(t, partial), gone, map[string]string{"token": "t"})
	if code, _, msg := integration.Classify(inst.Sync(t.Context(), integrationtest.Watched)); code != integration.CodeAPIChanged {
		t.Fatalf("unevaluable condition: %s %q", code, msg)
	}
}

func TestBadHealthValue(t *testing.T) {
	up := &widgets{version: "1.4", healthy: true, stats: `{"data":{"count":1}}`}
	srv := up.serve(t)
	s := mustParse(t, strings.Replace(base, `'info.ok ? "ok" : "down"'`, `'"great"'`, 1))
	h := integrationtest.New(t)
	inst := h.Connect(s, map[string]any{"url": srv.URL + "/base", "site": "lab"}, map[string]string{"token": "t"})
	if err := inst.Sync(t.Context(), integrationtest.Watched); err == nil || !strings.Contains(err.Error(), `"great" isn't ok`) {
		t.Fatalf("bad health: %v", err)
	}
}

func TestRegisterFS(t *testing.T) {
	reg := integration.NewRegistry()
	if err := RegisterFS(fstest.MapFS{"widgets.yaml": {Data: []byte(base)}}, reg); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := reg.Get("widgets"); !ok {
		t.Fatal("not registered")
	}
	if err := RegisterFS(fstest.MapFS{"gadgets.yaml": {Data: []byte(base)}}, integration.NewRegistry()); err == nil {
		t.Fatal("meta.id must match the file name")
	}
	if err := RegisterFS(fstest.MapFS{"widgets.yaml": {Data: []byte("apiVersion: nope")}}, integration.NewRegistry()); err == nil {
		t.Fatal("a bad spec must fail registration")
	}
}
