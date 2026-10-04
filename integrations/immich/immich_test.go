package immich

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

const (
	key     = "fake-immich-key"
	fixture = "../../testdata/integrations/immich"
)

// fake is an Immich serving the synthetic fixtures. The test decides which
// routes the key may read (403), which don't exist (404), and what the
// key's permissions are.
type fake struct {
	mu        sync.Mutex
	forbidden map[string]bool
	missing   map[string]bool
	apiKey    string // body of /api/api-keys/me; "" serves the fixture
	paths     []string
}

var files = map[string]string{
	"/api/server/version":      "server_version.json",
	"/api/server-info/version": "server_info_version.json",
	"/api/server/about":        "server_about.json",
	"/api/server/statistics":   "server_statistics.json",
	"/api/users/me":            "users_me.json",
	"/api/assets/statistics":   "assets_statistics.json",
	"/api/api-keys/me":         "api_keys_me.json",
}

// public routes answer without a key.
var public = map[string]bool{"/api/server/version": true, "/api/server-info/version": true}

func (f *fake) serve(t *testing.T, base string) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		path := strings.TrimPrefix(r.URL.Path, base)
		f.paths = append(f.paths, path)
		w.Header().Set("Content-Type", "application/json")
		name, ok := files[path]
		switch {
		case !ok || f.missing[path] || (path == "/api/server-info/version" && !f.missing["/api/server/version"]):
			http.NotFound(w, r)
			return
		case !public[path] && r.Header.Get("x-api-key") != key:
			w.WriteHeader(http.StatusUnauthorized)
			name = "unauthorized_401.json"
		case f.forbidden[path]:
			w.WriteHeader(http.StatusForbidden)
			name = "forbidden_403.json"
		case path == "/api/api-keys/me" && f.apiKey != "":
			w.Write([]byte(f.apiKey))
			return
		}
		b, err := os.ReadFile(filepath.Join(fixture, name))
		if err != nil {
			t.Error(err)
		}
		w.Write(b)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (f *fake) set(fn func(f *fake)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *fake) requested() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.paths
	f.paths = nil
	return out
}

func newFake() *fake { return &fake{forbidden: map[string]bool{}, missing: map[string]bool{}} }

func TestRegistered(t *testing.T) {
	_, m, ok := integration.Default.Get(ID)
	if !ok || m.Name != "Immich" || m.Engine != integration.EngineGo || m.SecretFields()[0] != "api_key" || m.Poll.Interval != interval {
		t.Fatalf("manifest = %+v", m)
	}
}

func TestValidate(t *testing.T) {
	// The Homepage importer's shape (docs/plan/07-v0.1.md), secret resolved.
	imported := map[string]any{"url": "https://photos.lab.example", "tls": map[string]any{"mode": "verify"}}
	if errs := integrationtest.Validate(Integration{}, imported, map[string]string{"api_key": key}); len(errs) != 0 {
		t.Fatalf("importer shape: %v", errs)
	}
	for name, c := range map[string]struct {
		config  map[string]any
		secrets map[string]string
		path    string
	}{
		"no key":      {map[string]any{"url": "https://photos.lab.example"}, nil, "/api_key"},
		"no url":      {map[string]any{}, map[string]string{"api_key": key}, "/url"},
		"unix url":    {map[string]any{"url": "unix:///run/immich.sock"}, map[string]string{"api_key": key}, "/url"},
		"unknown key": {map[string]any{"url": "https://photos.lab.example", "token": "x"}, map[string]string{"api_key": key}, "/token"},
	} {
		errs := integrationtest.Validate(Integration{}, c.config, c.secrets)
		if len(errs) == 0 || errs[0].Path != c.path {
			t.Errorf("%s: %v, want an error at %s", name, errs, c.path)
		}
	}
}

func TestBaseURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://photos.lab.example":          "https://photos.lab.example",
		"https://photos.lab.example/":         "https://photos.lab.example",
		"https://photos.lab.example/api":      "https://photos.lab.example",
		"https://photos.lab.example/api/":     "https://photos.lab.example",
		"http://10.0.11.120:2283/?x=1#frag":   "http://10.0.11.120:2283",
		"https://lab.example/immich/api?x=yz": "https://lab.example/immich",
	} {
		u, err := baseURL(in)
		if err != nil || u.String() != want {
			t.Errorf("baseURL(%q) = %v, %v; want %s", in, u, err, want)
		}
	}
}

func TestAdminKey(t *testing.T) {
	f := newFake()
	srv := f.serve(t, "")
	h := integrationtest.New(t)
	inst := h.Connect(Integration{}, map[string]any{"url": srv.URL + "/api"}, map[string]string{"api_key": key})

	res := inst.Test(t.Context())
	if !res.OK || res.Version != "2.1.0" || len(res.Warnings) != 0 {
		t.Fatalf("test = %+v", res)
	}
	// The fixture key is read-only: four grants, none that write.
	if len(res.Grants) != 4 || integration.WriteGrantWarning(res.Grants) != nil {
		t.Fatalf("grants = %+v", res.Grants)
	}
	if _, ok := h.Resource("immich:i_test/app"); ok {
		t.Fatal("Test must not emit facts")
	}
	f.requested()

	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(f.requested(), " "); got != "/api/server/version /api/server/about /api/server/statistics" {
		t.Fatalf("requests = %s", got)
	}
	r, ok := h.Resource("immich:i_test/app")
	if !ok || r.Kind != "app" || r.Name != "Test" || r.State != "running" || r.Health != integration.HealthOK || r.Source != "integration:i_test" ||
		r.Attrs["type"] != "immich" || r.Attrs["version"] != "2.1.0" || r.Attrs["users"] != 2.0 || r.Attrs["stats_scope"] != "server" ||
		r.Attrs["licensed"] != false || r.Attrs["url"] != srv.URL+"/api" {
		t.Fatalf("app = %+v", r)
	}
	if links := r.Attrs["links"].([]any); links[0].(map[string]any)["url"] != srv.URL+"/api" {
		t.Fatalf("links = %v", links)
	}
	if h.Graph().Relations["integration:i_test monitors immich:i_test/app"] == nil {
		t.Fatal("the instance should monitor its app")
	}
	for metric, want := range map[string]struct {
		value float64
		unit  string
	}{"photos": {44040, "count"}, "videos": {2401, "count"}, "storage.gib": {272.3, "GiB"}} {
		se := h.Series("immich:i_test/app", metric)
		if se == nil || se["value"] != want.value || se["unit"] != want.unit {
			t.Errorf("%s = %v, want %v", metric, se, want)
		}
	}

	// Nobody watching: health and the key are still checked, statistics
	// aren't read, and the last ones stay.
	h.Advance(idle)
	if err := inst.Sync(t.Context(), integration.Interest{}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(f.requested(), " "); got != "/api/server/version /api/server/about" {
		t.Fatalf("unwatched requests = %s", got)
	}
	if r, _ := h.Resource("immich:i_test/app"); r.Attrs["users"] != 2.0 {
		t.Fatalf("unwatched app = %+v", r)
	}
	if se := h.Series("immich:i_test/app", "photos"); se["value"] != 44040.0 {
		t.Fatalf("unwatched photos = %v", se)
	}
}

func TestNonAdminKey(t *testing.T) {
	f := newFake()
	f.forbidden["/api/server/statistics"] = true
	srv := f.serve(t, "")
	h := integrationtest.New(t)
	inst := h.Connect(Integration{}, map[string]any{"url": srv.URL}, map[string]string{"api_key": key})

	res := inst.Test(t.Context())
	if !res.OK || len(res.Warnings) != 1 || res.Warnings[0].Code != "statistics_not_admin" ||
		!strings.Contains(res.Warnings[0].Message, "this key's user isn't an admin") {
		t.Fatalf("test = %+v", res)
	}

	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	r, _ := h.Resource("immich:i_test/app")
	if r.Health != integration.HealthOK || r.Attrs["stats_scope"] != "user" {
		t.Fatalf("app = %+v", r)
	}
	if _, has := r.Attrs["users"]; has {
		t.Fatal("the user count is the server's; a non-admin key can't know it")
	}
	// The key's own library: its asset statistics and its quota usage.
	for metric, want := range map[string]float64{"photos": 12000, "videos": 500, "storage.gib": 50} {
		if se := h.Series("immich:i_test/app", metric); se["value"] != want {
			t.Errorf("%s = %v, want %v", metric, se, want)
		}
	}

	// Without user.read and asset.statistics either: unknown, never 0.
	f.set(func(f *fake) { f.forbidden["/api/users/me"], f.forbidden["/api/assets/statistics"] = true, true })
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	for _, metric := range []string{"photos", "videos", "storage.gib"} {
		if se := h.Series("immich:i_test/app", metric); se["value"] != nil {
			t.Errorf("%s = %v, want unknown", metric, se)
		}
	}
	// And without server.about: the app is still there.
	f.set(func(f *fake) { f.forbidden["/api/server/about"] = true })
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	if r, _ := h.Resource("immich:i_test/app"); r.Health != integration.HealthOK {
		t.Fatalf("without about: %+v", r)
	}
}

func TestWarningWording(t *testing.T) {
	yes, no := true, false
	for admin, want := range map[*bool]string{
		&yes: "This admin's API key lacks the server.statistics permission",
		&no:  "this key's user isn't an admin",
		nil:  "need an admin's API key with the server.statistics permission",
	} {
		if w := statsWarning(admin, "Until then."); !strings.Contains(w.Message, want) || !strings.HasSuffix(w.Message, " Until then.") || w.Severity != integration.WarnInfo {
			t.Errorf("admin %v: %+v", admin, w)
		}
	}
}

func TestGrants(t *testing.T) {
	f := newFake()
	f.apiKey = `{"id":"00000000-0000-4000-8000-0000000000a2","name":"Homepage","permissions":["all"]}`
	srv := f.serve(t, "")
	res := integrationtest.New(t).Connect(Integration{}, map[string]any{"url": srv.URL}, map[string]string{"api_key": key}).Test(t.Context())
	if len(res.Grants) != 1 || res.Grants[0].Access != integration.AccessAdmin || integration.WriteGrantWarning(res.Grants) == nil {
		t.Fatalf("an all-permissions key = %+v", res.Grants)
	}
	g := grants(apiKey{Permissions: []string{"asset.read", "asset.upload", "asset.edit.get", "server.versionCheck", "maintenance", "tag.asset"}})
	want := []string{"read", "write", "read", "read", "write", "write"}
	for i, w := range want {
		if g[i].Access != w {
			t.Errorf("%s = %s, want %s", g[i].Name, g[i].Access, w)
		}
	}
	// Older Immich has no /api/api-keys/me: no grants, still OK.
	f.set(func(f *fake) { f.missing["/api/api-keys/me"] = true })
	res = integrationtest.New(t).Connect(Integration{}, map[string]any{"url": srv.URL}, map[string]string{"api_key": key}).Test(t.Context())
	if !res.OK || res.Grants != nil {
		t.Fatalf("no read-back = %+v", res)
	}
}

func TestFailures(t *testing.T) {
	f := newFake()
	srv := f.serve(t, "/photos")
	url := srv.URL + "/photos"

	// A wrong key: an auth Repair, and the message doesn't quote the key.
	h := integrationtest.New(t)
	res := h.Connect(Integration{}, map[string]any{"url": url}, map[string]string{"api_key": "wrong-key"}).Test(t.Context())
	if res.OK || res.ErrorCode != integration.CodeAuth || res.Version != "2.1.0" || strings.Contains(res.ErrorMessage, "wrong-key") {
		t.Fatalf("wrong key: %+v", res)
	}
	inst := h.Connect(Integration{}, map[string]any{"url": url}, map[string]string{"api_key": "wrong-key"})
	if code, class, _ := integration.Classify(inst.Sync(t.Context(), integration.Interest{})); code != integration.CodeAuth || class != integration.NeedsRepair {
		t.Fatalf("wrong key, unwatched sync: %s %d", code, class)
	}

	// An Immich older than 1.107 answers only at /api/server-info.
	f.set(func(f *fake) { f.missing["/api/server/version"] = true })
	h = integrationtest.New(t)
	inst = h.Connect(Integration{}, map[string]any{"url": url}, map[string]string{"api_key": key})
	code, class, msg := integration.Classify(inst.Sync(t.Context(), integrationtest.Watched))
	if code != integration.CodeUnsupportedVersion || class != integration.NeedsRepair ||
		msg != "Immich 1.105.1 is older than this integration supports (1.107 or later)." {
		t.Fatalf("old immich: %s %d %q", code, class, msg)
	}
	if res := inst.Test(t.Context()); res.OK || res.ErrorCode != integration.CodeUnsupportedVersion || res.Version != "1.105.1" {
		t.Fatalf("old immich test: %+v", res)
	}

	// Neither path: not an Immich API this integration knows.
	f.set(func(f *fake) { f.missing["/api/server-info/version"] = true })
	code, _, msg = integration.Classify(inst.Sync(t.Context(), integrationtest.Watched))
	if code != integration.CodeAPIChanged || msg != "Immich's API changed: GET /photos/api/server/version answered 404 Not Found" {
		t.Fatalf("no version endpoint: %s %q", code, msg)
	}

	// The statistics endpoint gone on a current server is a changed API too.
	f.set(func(f *fake) { f.missing = map[string]bool{"/api/server/statistics": true} })
	code, _, msg = integration.Classify(inst.Sync(t.Context(), integrationtest.Watched))
	if code != integration.CodeAPIChanged || msg != "Immich's API changed: GET /photos/api/server/statistics answered 404 Not Found (detected version 2.1.0)" {
		t.Fatalf("statistics 404: %s %q", code, msg)
	}
}
