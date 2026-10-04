package onboarding

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/runtime"
	"dash.cha.sh/internal/store"
)

// fakeIntegrations answers Docker tests by endpoint and lists instances.
type fakeIntegrations struct {
	mu      sync.Mutex
	tested  []string
	answers map[string]integration.TestResult
	insts   []runtime.InstanceView
}

func (f *fakeIntegrations) Test(_ context.Context, req runtime.TestRequest) (integration.TestResult, error) {
	ep, _ := req.Config["endpoint"].(string)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tested = append(f.tested, req.Integration+" "+ep)
	if strings.HasPrefix(ep, "tcp://bad") {
		return integration.TestResult{}, &runtime.ValidationError{Fields: []integration.FieldError{{Path: "/endpoint", Message: "needs a host"}}}
	}
	if r, ok := f.answers[ep]; ok {
		return r, nil
	}
	return integration.TestResult{ErrorCode: integration.CodeUnreachable, ErrorMessage: "dial tcp: lookup: no such host"}, nil
}

func (f *fakeIntegrations) List() []runtime.InstanceView {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]runtime.InstanceView(nil), f.insts...)
}

func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "chadash.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestState(t *testing.T) {
	st := newStore(t)
	f := &fakeIntegrations{}
	s, err := New(Options{Store: st, Integrations: f})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	got, err := s.State(ctx)
	if err != nil || got.Status != StatusNew || !got.Show || got.Sidecar != SidecarEndpoint || got.UpdatedAt != nil {
		t.Fatalf("a new hub: %+v %v", got, err)
	}

	// A hub that already reads something (an upgrade, an import) doesn't
	// open the wizard on its own; neither does demo mode.
	f.insts = []runtime.InstanceView{{ID: "i_1", Integration: "checks", Name: "Checks"}}
	if got, _ := s.State(ctx); got.Show {
		t.Fatal("a hub with instances shouldn't open the wizard")
	}
	f.insts = nil
	demo, _ := New(Options{Store: st, Integrations: f, Demo: true})
	if got, _ := demo.State(ctx); got.Show || got.Status != StatusNew {
		t.Fatalf("demo mode: %+v", got)
	}

	// Started: it shows, at its step, even with instances, until skipped or done.
	if got, err = s.Set(ctx, Update{Status: StatusStarted, Step: "discover"}); err != nil || !got.Show || got.Step != "discover" || got.UpdatedAt == nil {
		t.Fatalf("started: %+v %v", got, err)
	}
	f.insts = []runtime.InstanceView{{ID: "i_1", Integration: "docker", Name: "Docker"}}
	again, _ := New(Options{Store: st, Integrations: f})
	if got, _ := again.State(ctx); got.Status != StatusStarted || got.Step != "discover" || !got.Show {
		t.Fatalf("resumed: %+v", got)
	}
	for _, status := range []string{StatusSkipped, StatusDone} {
		if got, err := s.Set(ctx, Update{Status: status}); err != nil || got.Show || got.Status != status {
			t.Fatalf("%s: %+v %v", status, got, err)
		}
	}
	for _, u := range []Update{{Status: StatusNew}, {Status: "finished"}, {Status: StatusStarted, Step: "Discover!"}, {Status: StatusStarted, Step: strings.Repeat("a", 40)}} {
		if _, err := s.Set(ctx, u); !errors.Is(err, ErrInvalid) {
			t.Errorf("%+v: %v", u, err)
		}
	}

	// A damaged row starts over rather than breaking the page.
	if _, err := st.Writer().ExecContext(ctx, `UPDATE settings SET value = '{"status":"weird"}' WHERE key = ?`, SettingsKey); err != nil {
		t.Fatal(err)
	}
	if got, err := s.State(ctx); err != nil || got.Status != StatusNew {
		t.Fatalf("damaged: %+v %v", got, err)
	}
}

func dockerOK(version string, warnings ...integration.Warning) integration.TestResult {
	return integration.TestResult{OK: true, Version: version, Grants: []integration.Grant{{Name: "containers"}, {Name: "events"}}, Warnings: warnings}
}

func TestDiscoverDocker(t *testing.T) {
	f := &fakeIntegrations{answers: map[string]integration.TestResult{
		SidecarEndpoint:               dockerOK("28.5.1"),
		"tcp://dockerproxy:2375":      dockerOK("27.3.1", integration.Warning{Code: "docker_broad_proxy", Severity: integration.WarnWarning, Message: "broad"}),
		"unix:///var/run/docker.sock": {ErrorCode: integration.CodeInvalidConfig, ErrorMessage: "no socket mounted at /var/run/docker.sock"},
	}}
	f.insts = []runtime.InstanceView{{ID: "i_d", Integration: "docker", Name: "Docker", Config: map[string]any{"endpoint": "tcp://dockerproxy:2375/"}}}
	s, _ := New(Options{Store: newStore(t), Integrations: f, Registry: integration.NewRegistry()})
	res, err := s.Discover(t.Context(), DiscoverRequest{Sidecar: true, Docker: []string{" tcp://dockerproxy:2375 ", "unix:///var/run/docker.sock", "tcp://nowhere:2375", "", "tcp://dockerproxy:2375"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Found) != 2 || len(res.Checked) != 4 {
		t.Fatalf("result: %+v", res)
	}
	side, proxy := res.Found[0], res.Found[1]
	if side.Source != "sidecar" || side.Integration != "docker" || !side.Ready || side.Version != "28.5.1" ||
		side.Config["endpoint"] != SidecarEndpoint || side.Added != "" {
		t.Fatalf("sidecar: %+v", side)
	}
	// Already read by an instance, and the suggested name stays clear of it.
	if proxy.Added != "i_d" || proxy.Name != "Docker on dockerproxy" || len(proxy.Warnings) != 1 {
		t.Fatalf("dockerproxy: %+v", proxy)
	}
	if side.Name != "Docker 2" {
		t.Fatalf("the sidecar's name should avoid the existing Docker: %q", side.Name)
	}
	why := map[string]string{}
	for _, c := range res.Checked {
		why[c.Target] = c.Error
	}
	if why["tcp://nowhere:2375"] != "nothing answered there" || why["unix:///var/run/docker.sock"] != "no socket mounted at /var/run/docker.sock" || why[SidecarEndpoint] != "" {
		t.Fatalf("checked: %+v", res.Checked)
	}
	// Only what was asked for was tested.
	if len(f.tested) != 4 {
		t.Fatalf("tested: %v", f.tested)
	}

	// Nothing but the owner's targets: no subnets, no ranges, no URLs as
	// hosts, a handful at a time.
	for _, req := range []DiscoverRequest{
		{Hosts: []string{"198.51.100.0/24"}}, {Hosts: []string{"*.lab.example"}}, {Hosts: []string{"http://nas.lab.example"}},
		{Hosts: []string{"nas.lab.example:8006"}}, {Hosts: []string{"fe80::1%eth0"}}, {Hosts: []string{"198.51.100.1-50"}}, {Hosts: []string{"2375"}}, {Hosts: strings.Fields("a b c d e f g h i")},
		{Docker: []string{"http://docker:2375"}},
	} {
		if _, err := s.Discover(t.Context(), req); !errors.Is(err, ErrInvalid) {
			t.Errorf("%+v: %v", req, err)
		}
	}
	res, err = s.Discover(t.Context(), DiscoverRequest{Docker: []string{"tcp://bad"}})
	if err != nil || len(res.Checked) != 1 || !strings.Contains(res.Checked[0].Error, "needs a host") {
		t.Fatalf("an endpoint the integration refuses: %+v %v", res, err)
	}
}

// app is a fake manifest for the host probes.
type app struct{ m integration.Manifest }

func (a app) Manifest() integration.Manifest { return a.m }
func (app) Connect(context.Context, integration.Config, integration.Deps) (integration.Instance, error) {
	return nil, errors.New("not used")
}

func manifest(id, name string) integration.Manifest {
	return integration.Manifest{ID: id, Name: name, Icon: id, Version: "0.1.0", Tier: integration.Bronze,
		ConfigSchema: integration.Object(integration.URLProp(), integration.TLSProp(), integration.Prop("api_key", integration.Secret("Key"))).Require("url", "api_key")}
}

func recipe(t *testing.T, hints string) integration.Recipe {
	t.Helper()
	r, err := integration.ParseRecipe([]byte("---\ntitle: T\nsummary: S\naccess: full\ndiscover:\n" + hints + "---\n## Step\n\nText.\n"))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestDiscoverHosts(t *testing.T) {
	reg := integration.NewRegistry()
	for id, hints := range map[string]string{
		"proxmox":  "  - {scheme: https, port: 8006, path: /, match: Proxmox Virtual Environment}\n",
		"sonarr":   "  - {scheme: http, port: 8989, path: /, match: Sonarr}\n",
		"radarr":   "  - {scheme: http, port: 7878, path: /, match: Radarr}\n",
		"opnsense": "  - {scheme: https, port: 443, path: /, match: OPNsense}\n",
	} {
		if err := reg.Register(app{manifest(id, strings.ToUpper(id[:1])+id[1:])}); err != nil {
			t.Fatal(err)
		}
		if err := reg.AddRecipe(id, recipe(t, hints)); err != nil {
			t.Fatal(err)
		}
	}

	var mu sync.Mutex
	var seen []string
	page := func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.Host+r.URL.RequestURI())
		mu.Unlock()
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "" {
			t.Errorf("a probe must be a plain GET: %s %v", r.Method, r.Header)
		}
		_, port, _ := net.SplitHostPort(r.Host)
		switch {
		case port == "8006":
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("<title>nas - Proxmox Virtual Environment</title>"))
		case port == "8989" && r.URL.Path == "/":
			http.Redirect(w, r, "/login", http.StatusFound) // followed within the origin
		case port == "8989":
			w.Write([]byte("<html><title>SONARR</title></html>"))
		case port == "7878":
			w.Write([]byte("<title>Something else</title>"))
		default:
			http.NotFound(w, r)
		}
	}
	plain := httptest.NewServer(http.HandlerFunc(page))
	defer plain.Close()
	secure := httptest.NewTLSServer(http.HandlerFunc(page))
	defer secure.Close()
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, _ := net.SplitHostPort(addr)
		if host != "nas.lab.example" {
			return nil, errors.New("connection refused")
		}
		to := plain.Listener.Addr().String()
		if port == "8006" || port == "443" {
			to = secure.Listener.Addr().String()
		}
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, to)
	}
	f := &fakeIntegrations{answers: map[string]integration.TestResult{"tcp://nas.lab.example:2375": dockerOK("27.0.0")}}
	f.insts = []runtime.InstanceView{{ID: "i_s", Integration: "sonarr", Name: "Sonarr", Config: map[string]any{"url": "http://other.lab.example:8989"}}}
	s, _ := New(Options{Store: newStore(t), Integrations: f, Registry: reg, Dial: dial})

	res, err := s.Discover(t.Context(), DiscoverRequest{Hosts: []string{"nas.lab.example", "gone.lab.example"}})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Candidate{}
	for _, c := range res.Found {
		got[c.Integration] = c
	}
	if len(res.Found) != 3 || got["radarr"].Integration != "" || got["opnsense"].Integration != "" {
		t.Fatalf("found: %+v", res.Found)
	}
	pve := got["proxmox"]
	tlsCfg, _ := pve.Config["tls"].(map[string]any)
	if pve.Config["url"] != "https://nas.lab.example:8006" || tlsCfg["mode"] != "tofu" || pve.Ready || pve.Source != "host" || pve.Target != "nas.lab.example" || pve.Name != "Proxmox" {
		t.Fatalf("proxmox: %+v", pve)
	}
	if son := got["sonarr"]; son.Config["url"] != "http://nas.lab.example:8989" || son.Config["tls"] != nil || son.Name != "Sonarr on nas.lab.example" || son.Added != "" {
		t.Fatalf("sonarr: %+v", son)
	}
	if d := got["docker"]; d.Config["endpoint"] != "tcp://nas.lab.example:2375" || !d.Ready || d.Name != "Docker on nas.lab.example" {
		t.Fatalf("docker: %+v", d)
	}
	if len(res.Checked) != 2 || res.Checked[1].Target != "nas.lab.example" || res.Checked[1].Found != 3 || res.Checked[0].Found != 0 {
		t.Fatalf("checked: %+v", res.Checked)
	}
	// One GET per hint, plus the redirect it followed; nothing for the
	// unreachable host reached a server.
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 5 {
		t.Fatalf("requests: %v", seen)
	}
	for _, r := range seen {
		if !strings.HasPrefix(r, "GET nas.lab.example:") && !strings.HasPrefix(r, "GET nas.lab.example/") {
			t.Fatalf("request %q", r)
		}
	}
}

func TestRoutes(t *testing.T) {
	f := &fakeIntegrations{answers: map[string]integration.TestResult{SidecarEndpoint: dockerOK("28.5.1")}}
	s, _ := New(Options{Store: newStore(t), Integrations: f, Registry: integration.NewRegistry()})
	mux := http.NewServeMux()
	s.Register(mux, func(h http.Handler) http.Handler { return h })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	call := func(method, path, body string) (int, map[string]any) {
		req, _ := http.NewRequest(method, srv.URL+path, bytes.NewReader([]byte(body)))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	if code, st := call("GET", "/api/onboarding", ""); code != 200 || st["status"] != "new" || st["show"] != true || st["sidecar"] != SidecarEndpoint {
		t.Fatalf("GET: %d %v", code, st)
	}
	if code, st := call("PUT", "/api/onboarding", `{"status":"started","step":"add"}`); code != 200 || st["step"] != "add" {
		t.Fatalf("PUT: %d %v", code, st)
	}
	if code, _ := call("PUT", "/api/onboarding", `{"status":"new"}`); code != 422 {
		t.Fatalf("PUT new: %d", code)
	}
	if code, _ := call("PUT", "/api/onboarding", `{"status":"done","extra":1}`); code != 400 {
		t.Fatalf("PUT with an unknown field: %d", code)
	}
	code, res := call("POST", "/api/onboarding/discover", `{"sidecar":true}`)
	found, _ := res["found"].([]any)
	if code != 200 || len(found) != 1 || found[0].(map[string]any)["ready"] != true {
		t.Fatalf("discover: %d %v", code, res)
	}
	if code, _ := call("POST", "/api/onboarding/discover", `{"hosts":["203.0.113.0/24"]}`); code != 422 {
		t.Fatalf("discover a subnet: %d", code)
	}
}
