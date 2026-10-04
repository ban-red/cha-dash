package traefik

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/integration/integrationtest"
)

// fixtures is the spike 4.3 capture of the dogfood home's Traefik (v3.6.7).
const fixtures = "../../testdata/integrations/traefik"

// testPassword is the fake API's password; the capture had basic auth.
const testPassword = "not-a-real-password"

var files = map[string]string{
	"/api/version": "version.json", "/api/overview": "overview.json", "/api/entrypoints": "entrypoints.json",
	"/api/http/routers": "http-routers.json", "/api/http/services": "http-services.json", "/api/http/middlewares": "http-middlewares.json",
}

// fakeTraefik serves the capture, with basic auth when auth is on, and
// paginates the lists like Traefik (X-Next-Page, 1 on the last page).
type fakeTraefik struct {
	t        *testing.T
	mu       sync.Mutex
	auth     bool
	pageSize int // caps per_page, to exercise pagination
	data     map[string][]any
	single   map[string]any
	paths    []string
	unauth   int // requests without credentials
}

func newFake(t *testing.T, auth bool) *fakeTraefik {
	f := &fakeTraefik{t: t, auth: auth, data: map[string][]any{}, single: map[string]any{}}
	for path, name := range files {
		b, err := os.ReadFile(filepath.Join(fixtures, name))
		if err != nil {
			t.Fatal(err)
		}
		var v any
		if err := json.Unmarshal(b, &v); err != nil {
			t.Fatal(err)
		}
		if l, ok := v.([]any); ok {
			f.data[path] = l
		} else {
			f.single[path] = v
		}
	}
	return f
}

func (f *fakeTraefik) serve() string {
	srv := httptest.NewServer(f)
	f.t.Cleanup(srv.Close)
	return srv.URL
}

func (f *fakeTraefik) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paths = append(f.paths, r.URL.RequestURI())
	if r.Method != http.MethodGet {
		f.t.Errorf("the integration sent %s %s", r.Method, r.URL.Path)
	}
	user, pass, ok := r.BasicAuth()
	if !ok {
		f.unauth++
	}
	if f.auth && (!ok || user != "admin" || pass != testPassword) {
		w.Header().Set("WWW-Authenticate", `Basic realm="traefik"`)
		http.Error(w, "401 Unauthorized", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	if v, ok := f.single[r.URL.Path]; ok {
		_ = json.NewEncoder(w).Encode(v)
		return
	}
	l, ok := f.data[r.URL.Path]
	if !ok {
		http.NotFound(w, r)
		return
	}
	size, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	if size <= 0 {
		size = 100
	}
	if f.pageSize > 0 {
		size = min(size, f.pageSize)
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	page = max(page, 1)
	lo, hi := min((page-1)*size, len(l)), min(page*size, len(l))
	next := 1
	if hi < len(l) {
		next = page + 1
	}
	w.Header().Set("X-Next-Page", strconv.Itoa(next))
	_ = json.NewEncoder(w).Encode(l[lo:hi])
}

// find returns the fixture entry with a name, for editing under the lock.
func (f *fakeTraefik) find(path, name string) map[string]any {
	for _, x := range f.data[path] {
		if m := x.(map[string]any); m["name"] == name {
			return m
		}
	}
	f.t.Fatalf("no %s in %s", name, path)
	return nil
}

func importerConfig(url string) (map[string]any, map[string]string) {
	// The Homepage importer's shape (expected-plan.json), password vaulted.
	return map[string]any{"url": url, "username": "admin", "tls": map[string]any{"mode": "verify"}},
		map[string]string{"password": testPassword}
}

func syncOK(t *testing.T, inst integration.Instance) {
	t.Helper()
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
}

func resource(t *testing.T, h *integrationtest.Harness, id string) graph.Resource {
	t.Helper()
	r, ok := h.Resource(id)
	if !ok {
		t.Fatalf("no resource %s", id)
	}
	return r
}

func strs(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

func link(r graph.Resource) string {
	ls, _ := r.Attrs["links"].([]any)
	if len(ls) == 0 {
		return ""
	}
	return ls[0].(map[string]any)["url"].(string)
}

func TestRegistered(t *testing.T) {
	_, m, ok := integration.Default.Get(ID)
	if !ok || m.Name != "Traefik" || !slices.Equal(m.SecretFields(), []string{"password"}) || m.Poll.Interval != time.Minute {
		t.Fatalf("manifest = %+v", m)
	}
}

func TestValidate(t *testing.T) {
	cfg, secrets := importerConfig("http://traefik.lab.example")
	if errs := integrationtest.Validate(Integration{}, cfg, secrets); len(errs) != 0 {
		t.Fatalf("importer shape: %v", errs)
	}
	if errs := integrationtest.Validate(Integration{}, map[string]any{"url": "http://traefik:8080"}, nil); len(errs) != 0 {
		t.Fatalf("no credentials: %v", errs)
	}
	for _, c := range []struct {
		cfg     map[string]any
		secrets map[string]string
		path    string
	}{
		{map[string]any{"url": "http://t", "username": "admin"}, nil, "/password"},
		{map[string]any{"url": "http://t"}, map[string]string{"password": "x"}, "/username"},
		{map[string]any{"url": "ftp://t"}, nil, "/url"},
		{map[string]any{}, nil, "/url"},
	} {
		errs := integrationtest.Validate(Integration{}, c.cfg, c.secrets)
		if len(errs) == 0 || errs[0].Path != c.path {
			t.Errorf("%v: %v, want an error at %s", c.cfg, errs, c.path)
		}
	}
}

func TestSync(t *testing.T) {
	f := newFake(t, true)
	url := f.serve()
	h := integrationtest.New(t)
	cfg, secrets := importerConfig(url)
	inst := h.Connect(Integration{}, cfg, secrets)
	syncOK(t, inst)

	app := resource(t, h, "traefik:i_test/app")
	if app.Kind != "app" || app.Name != "Test" || app.State != "running" || app.Health != integration.HealthOK ||
		app.Attrs["type"] != "traefik" || app.Attrs["version"] != "3.6.7" || app.Attrs["routers"] != 61.0 ||
		app.Attrs["services"] != 56.0 || app.Attrs["middlewares"] != 7.0 || app.Attrs["tcp_routers"] != 3.0 ||
		app.Attrs["poll_url"] != url || !slices.Equal(strs(app.Attrs["providers"]), []string{"Docker", "File"}) {
		t.Fatalf("app = %+v", app)
	}
	// The open link is the dashboard's routed URL, not the API's.
	if link(app) != "https://traefik.lab.example" || !slices.Equal(strs(app.Attrs["caps"]), []string{"proxy.routes"}) {
		t.Fatalf("app links = %v", app.Attrs)
	}
	g := h.Graph()
	routes, services := 0, 0
	for _, r := range g.Resources {
		switch r.Kind {
		case "route":
			routes++
		case "service":
			services++
		}
	}
	if routes != 61 || services != 56 {
		t.Fatalf("%d routes, %d services", routes, services)
	}

	// A path route behind Authelia through the nginx-proxy hop: the open URL
	// is the routed one, the poll URL the backend's, past the SSO.
	sonarr := resource(t, h, "traefik:i_test/route/sonarr@file")
	want := map[string]any{
		"rule": "Host(`jupiter.lab.example`) && PathPrefix(`/sonarr`)", "entrypoint": "websecure", "tls": true,
		"upstream": "http://10.0.11.120:7780/sonarr", "upstream_addr": "10.0.11.120:7780", "upstream_path": "/sonarr",
		"poll_url": "http://10.0.11.120:7780/sonarr", "url": "https://jupiter.lab.example/sonarr", "auth": "forward",
		"service": "sonarr@file", "router": "sonarr@file", "provider": "file", "priority": 999.0, "path": "/sonarr",
	}
	for k, v := range want {
		if sonarr.Attrs[k] != v {
			t.Errorf("sonarr %s = %v, want %v", k, sonarr.Attrs[k], v)
		}
	}
	if sonarr.Kind != "route" || sonarr.Name != "sonarr" || sonarr.State != "enabled" || sonarr.Health != integration.HealthOK ||
		link(sonarr) != "https://jupiter.lab.example/sonarr" ||
		!slices.Equal(strs(sonarr.Attrs["middlewares"]), []string{"redirect-to-https@file", "authelia@docker"}) ||
		!slices.Equal(strs(sonarr.Attrs["hosts"]), []string{"jupiter.lab.example"}) {
		t.Fatalf("sonarr = %+v", sonarr)
	}
	for id, c := range map[string]struct{ upstream, poll, url string }{
		"bazarr@file":              {"http://10.0.11.120:6767", "http://10.0.11.120:6767/bazarr", "https://jupiter.lab.example/bazarr"},
		"linkding@docker":          {"http://10.106.0.5:9090", "http://10.106.0.5:9090", "https://links.lab.example"},
		"authelia@docker":          {"http://10.106.0.4:9091", "http://10.106.0.4:9091", "https://authelia.lab.example"},
		"homeassistant-local@file": {"http://10.0.11.20:8123", "http://10.0.11.20:8123", "https://host1.local:80"}, // TLS on :80
		"jupiter@file":             {"http://10.0.11.120:80", "http://10.0.11.120:80", "https://jupiter.lab.example"},
		"go-link@file":             {"https://go.lab3.example", "https://go.lab3.example", "https://go.lab.example"},
		"traefik@file":             {"api@internal", "", "https://traefik.lab.example"},
		"plex-rewrite@file":        {"noop@internal", "", "https://app.plex.tv:80"},
		"api@file":                 {"api@internal", "", "http://traefik.home1.lan/api"},
	} {
		r := resource(t, h, "traefik:i_test/route/"+id)
		poll, _ := r.Attrs["poll_url"].(string)
		if r.Attrs["upstream"] != c.upstream || poll != c.poll || r.Attrs["url"] != c.url {
			t.Errorf("%s: upstream %v, poll %v, url %v", id, r.Attrs["upstream"], r.Attrs["poll_url"], r.Attrs["url"])
		}
	}
	if api := resource(t, h, "traefik:i_test/route/api@file"); api.Attrs["auth"] != "basic" || len(api.Attrs["entrypoints"].([]any)) != 4 {
		t.Fatalf("api@file = %+v", api.Attrs)
	}
	if coder := resource(t, h, "traefik:i_test/route/coder@file"); coder.Attrs["host_regexp"] != true || !slices.Equal(strs(coder.Attrs["hosts"]), []string{"code.lab.example"}) {
		t.Fatalf("coder = %+v", coder.Attrs)
	}

	// Services: servers from Traefik, and serverStatus left out without a
	// health check (it says UP for everything).
	svc := resource(t, h, "traefik:i_test/service/sonarr@file")
	if svc.Kind != "service" || svc.Name != "sonarr" || svc.Health != integration.HealthOK || svc.Attrs["upstream"] != "http://10.0.11.120:7780/sonarr" ||
		svc.Attrs["type"] != "loadbalancer" || svc.Attrs["server_status"] != nil || !slices.Equal(strs(svc.Attrs["used_by"]), []string{"sonarr@file"}) {
		t.Fatalf("service = %+v", svc)
	}
	if in := resource(t, h, "traefik:i_test/service/api@internal"); in.Attrs["type"] != "internal" || in.Attrs["upstream"] != "api@internal" {
		t.Fatalf("api@internal = %+v", in.Attrs)
	}
	for _, rel := range []string{
		"integration:i_test monitors traefik:i_test/app",
		"traefik:i_test/route/sonarr@file part_of traefik:i_test/app",
		"traefik:i_test/route/sonarr@file routes_to traefik:i_test/service/sonarr@file",
		"traefik:i_test/route/traefik@file routes_to traefik:i_test/service/api@internal",
		"traefik:i_test/service/sonarr@file part_of traefik:i_test/app",
	} {
		if g.Relations[rel] == nil {
			t.Errorf("missing relation %s", rel)
		}
	}

	// Middleware configs never reach the graph: no hashes, no forwardAuth
	// address, no header values, no TLS options.
	all, _ := json.Marshal(g)
	for _, s := range []string{"$2a$", "basicAuth", "users", "authelia:9091", "Remote-User", "X-Forwarded-Proto", "certResolver", "cloudflare", testPassword} {
		if strings.Contains(string(all), s) {
			t.Errorf("%q was published", s)
		}
	}

	// Hints: credentials over plain http, and dashboards without auth (the
	// API itself asked for credentials).
	ph, ok := h.Incident(hintPlainHTTP)
	if !ok || ph.Severity != "attention" || ph.Category != integration.CategorySecurity || ph.Target != "traefik:i_test/app" || ph.Summary != "Test sends its credentials over plain http:// to 127.0.0.1. Use https:// if it supports it." {
		t.Fatalf("plain http hint = %+v", ph)
	}
	dh, ok := h.Incident(hintAPI)
	if !ok || dh.Severity != "info" || dh.Category != integration.CategorySecurity || !strings.Contains(dh.Summary, "on traefik.home1.lan, traefik.lab.example.") {
		t.Fatalf("dashboard hint = %+v", dh)
	}
	inbox, _ := json.Marshal([]any{ph, dh})
	if strings.Contains(string(inbox), testPassword) {
		t.Fatal("the password reached the inbox")
	}

	// The unauthenticated probe runs every 10 minutes, not every sync.
	if _, n := f.requests(); n != 1 {
		t.Fatalf("%d unauthenticated requests", n)
	}
	syncOK(t, inst)
	if _, n := f.requests(); n != 1 {
		t.Fatalf("%d unauthenticated requests after a second sync", n)
	}
	// The API opens up: the next probe sees it.
	f.update(func() { f.auth = false })
	h.Advance(10 * time.Minute)
	syncOK(t, inst)
	if dh, _ := h.Incident(hintAPI); dh.Severity != "attention" || dh.Category != integration.CategorySecurity || !strings.Contains(dh.Summary, "answers without credentials") {
		t.Fatalf("open API hint = %+v", dh)
	}
}

func TestOpenAPI(t *testing.T) {
	f := newFake(t, false)
	url := f.serve()
	h := integrationtest.New(t)
	inst := h.Connect(Integration{}, map[string]any{"url": url + "/dashboard/"}, nil)
	syncOK(t, inst)
	if _, ok := h.Incident(hintPlainHTTP); ok {
		t.Fatal("no credentials, no plain-http hint")
	}
	if dh, _ := h.Incident(hintAPI); dh.Severity != "attention" {
		t.Fatalf("open API hint = %+v", dh)
	}
	if paths, _ := f.requests(); slices.ContainsFunc(paths, func(p string) bool { return strings.Contains(p, "/dashboard/api") }) {
		t.Fatalf("a /dashboard/ URL means the API next to it: %v", paths)
	}
	res := h.Connect(Integration{}, map[string]any{"url": url}, nil).Test(t.Context())
	if !res.OK || len(res.Warnings) != 1 || res.Warnings[0].Code != "api_no_auth" {
		t.Fatalf("test = %+v", res)
	}
}

func TestTestAndAuth(t *testing.T) {
	f := newFake(t, true)
	url := f.serve()
	h := integrationtest.New(t)
	cfg, secrets := importerConfig(url)
	res := h.Connect(Integration{}, cfg, secrets).Test(t.Context())
	if !res.OK || res.Version != "3.6.7" || len(res.Grants) != 1 || res.Grants[0].CanWrite() || len(res.Warnings) != 0 {
		t.Fatalf("test = %+v", res)
	}
	res = h.Connect(Integration{}, cfg, map[string]string{"password": "wrong"}).Test(t.Context())
	if res.OK || res.ErrorCode != integration.CodeAuth {
		t.Fatalf("wrong password = %+v", res)
	}
	err := h.Connect(Integration{}, cfg, map[string]string{"password": "wrong"}).Sync(t.Context(), integrationtest.Watched)
	var ae *integration.AuthError
	if !errors.As(err, &ae) || ae.Field != "password" || integration.FieldOf(err) != "password" {
		t.Fatalf("sync with a wrong password: %v", err)
	}
	err = h.Connect(Integration{}, map[string]any{"url": url}, nil).Sync(t.Context(), integrationtest.Watched)
	if !errors.As(err, &ae) || ae.Field != "username" {
		t.Fatalf("sync without credentials: %v", err)
	}
	if _, ok := h.Resource("traefik:i_test/app"); ok {
		t.Fatal("failed syncs and Test publish nothing")
	}
}

func TestAPINotFound(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	h := integrationtest.New(t)
	err := h.Connect(Integration{}, map[string]any{"url": srv.URL}, nil).Sync(t.Context(), integrationtest.Watched)
	var re *integration.RepairError
	if !errors.As(err, &re) || re.Code != integration.CodeAPIChanged || re.Field != "url" {
		t.Fatalf("404: %v", err)
	}
}

func TestPagination(t *testing.T) {
	f := newFake(t, false)
	f.pageSize = 25
	h := integrationtest.New(t)
	syncOK(t, h.Connect(Integration{}, map[string]any{"url": f.serve()}, nil))
	n := 0
	for _, r := range h.Graph().Resources {
		if r.Kind == "route" {
			n++
		}
	}
	pages := 0
	paths, _ := f.requests()
	for _, p := range paths {
		if strings.HasPrefix(p, "/api/http/routers?") {
			pages++
		}
	}
	if n != 61 || pages != 3 {
		t.Fatalf("%d routes over %d pages", n, pages)
	}
}

func TestHealth(t *testing.T) {
	f := newFake(t, false)
	f.update(func() {
		s := f.find("/api/http/services", "sonarr@file")
		s["loadBalancer"].(map[string]any)["healthCheck"] = map[string]any{"path": "/ping", "interval": "30s"}
		s["serverStatus"] = map[string]any{"http://10.0.11.120:7780/sonarr": "DOWN"}
		r := f.find("/api/http/routers", "radarr@file")
		r["status"], r["error"] = "disabled", []any{`middleware "gone@file" does not exist`}
	})
	h := integrationtest.New(t)
	syncOK(t, h.Connect(Integration{}, map[string]any{"url": f.serve()}, nil))
	if s := resource(t, h, "traefik:i_test/service/sonarr@file"); s.Health != integration.HealthDown || s.Attrs["health_check"] != true || s.Attrs["server_status"] == nil {
		t.Fatalf("health-checked service = %+v", s)
	}
	if r := resource(t, h, "traefik:i_test/route/sonarr@file"); r.Health != integration.HealthAttention || r.Attrs["health.reason"] != "backend: every server fails Traefik's health check" {
		t.Fatalf("route to it = %+v", r)
	}
	if r := resource(t, h, "traefik:i_test/route/radarr@file"); r.State != "disabled" || r.Health != integration.HealthDown || !strings.Contains(r.Attrs["health.reason"].(string), "does not exist") {
		t.Fatalf("disabled router = %+v", r)
	}
}

func (f *fakeTraefik) update(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn()
}

// requests returns the paths requested so far and the number sent without
// credentials.
func (f *fakeTraefik) requests() ([]string, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.paths), f.unauth
}

func TestPollURL(t *testing.T) {
	rule := parseRule("Host(`jupiter.lab.example`) && PathPrefix(`/sonarr`)")
	strip := middleware{Name: "strip@file", Type: "stripprefix"}
	strip.StripPrefix = &struct {
		Prefixes []string `json:"prefixes"`
	}{Prefixes: []string{"/sonarr", "/radarr"}}
	for _, c := range []struct {
		s    server
		mws  []middleware
		want string
	}{
		{server{URL: "http://10.0.11.120:8989"}, nil, "http://10.0.11.120:8989/sonarr"},
		{server{URL: "http://10.0.11.120:8989"}, []middleware{strip}, "http://10.0.11.120:8989"},
		{server{URL: "http://10.0.11.120:7780/sonarr"}, nil, "http://10.0.11.120:7780/sonarr"},
		{server{URL: "http://10.0.11.120:7780/base", PreservePath: true}, nil, "http://10.0.11.120:7780/base/sonarr"},
		{server{URL: "http://10.0.11.120:7780/base/", PreservePath: true}, []middleware{strip}, "http://10.0.11.120:7780/base"},
	} {
		if got := pollURL(c.s, rule, c.mws); got != c.want {
			t.Errorf("pollURL(%+v) = %s, want %s", c.s, got, c.want)
		}
	}
	if p := parseRule(`Host("a.lab.example") && Path("/x")`); !slices.Equal(p.hosts, []string{"a.lab.example"}) || p.path != "/x" {
		t.Fatalf("double quotes = %+v", p)
	}
	if p := parseRule("HostSNI(`*`)"); len(p.hosts) != 0 {
		t.Fatalf("HostSNI = %+v", p)
	}
}
