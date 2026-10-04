package specs

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/integration/integrationtest"
)

// app is a fake upstream for one spec. It serves testdata/<id>/ files by
// route (the path under the base, or what route says), answers 401 to
// requests auth refuses, and 404 to routes it doesn't have. A route's value
// is a file name, or inline JSON when it starts with "{".
type app struct {
	t     *testing.T
	dir   string
	base  string
	auth  func(*http.Request) bool
	route func(*http.Request) string
	// unauthorized is the 401's body file, if the upstream sends one.
	unauthorized string

	mu     sync.Mutex
	routes map[string]string
	seen   []*http.Request
}

func (a *app) start() *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		a.seen = append(a.seen, r.Clone(r.Context()))
		routes := a.routes
		a.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if !a.auth(r) {
			w.WriteHeader(http.StatusUnauthorized)
			if a.unauthorized != "" {
				w.Write(a.read(a.unauthorized))
			}
			return
		}
		key := strings.TrimPrefix(r.URL.Path, a.base)
		if a.route != nil {
			key = a.route(r)
		}
		body, ok := routes[key]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(a.read(body))
	}))
	a.t.Cleanup(srv.Close)
	return srv
}

func (a *app) read(body string) []byte {
	if strings.HasPrefix(body, "{") {
		return []byte(body)
	}
	b, err := os.ReadFile(filepath.Join(a.dir, body))
	if err != nil {
		a.t.Error(err)
	}
	return b
}

// set changes one route's answer; "" removes it (a 404).
func (a *app) set(route, body string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	routes := make(map[string]string, len(a.routes))
	for k, v := range a.routes {
		routes[k] = v
	}
	if body == "" {
		delete(routes, route)
	} else {
		routes[route] = body
	}
	a.routes = routes
}

// requests returns the requests seen so far.
func (a *app) requests() []*http.Request {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]*http.Request(nil), a.seen...)
}

// bundled returns a registered spec, checking it's one with secret field.
func bundled(t *testing.T, id, name, secret string) integration.Integration {
	t.Helper()
	integ, m, ok := integration.Default.Get(id)
	if !ok || m.Engine != integration.EngineSpec || m.Name != name || !m.IsSecret(secret) || len(m.SecretFields()) != 1 {
		t.Fatalf("%s isn't registered as a spec with secret %s: %+v", id, secret, m)
	}
	return integ
}

// importerShape checks the config the Homepage importer writes validates.
func importerShape(t *testing.T, integ integration.Integration, url, secret string) {
	t.Helper()
	cfg := map[string]any{"url": url, "tls": map[string]any{"mode": "verify"}}
	if errs := integrationtest.Validate(integ, cfg, map[string]string{secret: "fake"}); len(errs) != 0 {
		t.Fatalf("the importer's config doesn't validate: %v", errs)
	}
	if errs := integrationtest.Validate(integ, map[string]any{"url": url}, nil); len(errs) == 0 || errs[0].Path != "/"+secret {
		t.Fatalf("a missing %s: %v", secret, errs)
	}
}

// checkApp checks what every spec's app resource has in common.
func checkApp(t *testing.T, h *integrationtest.Harness, id, url string) map[string]any {
	t.Helper()
	rid := id + ":i_test/app"
	r, ok := h.Resource(rid)
	if !ok || r.Kind != "app" || r.Name != "Test" || r.State != "running" || r.Source != "integration:i_test" || r.Attrs["type"] != id {
		t.Fatalf("app = %+v", r)
	}
	if links, _ := r.Attrs["links"].([]any); len(links) != 1 || links[0].(map[string]any)["url"] != url {
		t.Fatalf("links = %v", r.Attrs["links"])
	}
	if h.Graph().Relations["integration:i_test monitors "+rid] == nil {
		t.Fatal("the instance should monitor its app")
	}
	return r.Attrs
}

// checkSeries checks series values (nil: unknown) and units.
func checkSeries(t *testing.T, h *integrationtest.Harness, id string, want map[string]any, unit map[string]string) {
	t.Helper()
	for metric, v := range want {
		se := h.Series(id+":i_test/app", metric)
		if se == nil || se["value"] != v {
			t.Errorf("%s = %v, want %v", metric, se, v)
			continue
		}
		u := unit[metric]
		if u == "" {
			u = "count"
		}
		if se["unit"] != u {
			t.Errorf("%s unit = %v, want %s", metric, se["unit"], u)
		}
	}
}

// classify runs a sync and classifies its error.
func classify(t *testing.T, inst integration.Instance) (string, string) {
	t.Helper()
	code, _, msg := integration.Classify(inst.Sync(t.Context(), integrationtest.Watched))
	return code, msg
}
