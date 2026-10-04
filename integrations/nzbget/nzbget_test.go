package nzbget

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"dash.cha.sh/integrations/internal/fixture"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/integration/integrationtest"
)

const (
	user = "chadash"
	pass = "fake-nzbget-password" // synthetic
)

// rpc is a fake NZBGet: POST <base>/jsonrpc with basic auth, answers by
// method from the fixture directory (inline JSON when it starts with "{").
type rpc struct {
	t    *testing.T
	dir  string
	base string

	mu      sync.Mutex
	routes  map[string]string
	methods []string
	params  [][]any
}

func newRPC(t *testing.T, base string) (*rpc, *httptest.Server) {
	r := &rpc{t: t, dir: fixture.Dir(t, ID), base: base, routes: map[string]string{
		"version": "version.json", "status": "status.json", "listgroups": "listgroups.json",
	}}
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return r, srv
}

func (r *rpc) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if req.URL.Path != r.base+"/jsonrpc" || req.Method != http.MethodPost {
		http.NotFound(w, req)
		return
	}
	if u, p, ok := req.BasicAuth(); !ok || u != user || p != pass {
		w.Header().Set("WWW-Authenticate", `Basic realm="NZBGet"`)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var body struct {
		Method string `json:"method"`
		Params []any  `json:"params"`
	}
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		r.t.Errorf("body: %v", err)
	}
	r.methods = append(r.methods, body.Method)
	r.params = append(r.params, body.Params)
	route, ok := r.routes[body.Method]
	if !ok {
		w.Write([]byte(`{"version":"1.1","error":{"name":"JSONRPCError","code":1,"message":"Invalid procedure"}}`))
		return
	}
	b := []byte(route)
	if !strings.HasPrefix(route, "{") {
		var err error
		if b, err = os.ReadFile(filepath.Join(r.dir, route)); err != nil {
			r.t.Error(err)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(b)
}

func (r *rpc) set(method, route string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.routes[method] = route
}

func connect(t *testing.T, h *integrationtest.Harness, url, username, password string) integration.Instance {
	cfg := map[string]any{"url": url, "tls": map[string]any{"mode": "verify"}}
	secrets := map[string]string{}
	if username != "" {
		cfg["username"] = username
		secrets["password"] = password
	}
	return h.Connect(Integration{}, cfg, secrets)
}

func TestValidate(t *testing.T) {
	cfg := map[string]any{"url": "http://nas.home.arpa:6789", "tls": map[string]any{"mode": "verify"}, "username": user}
	if errs := integrationtest.Validate(Integration{}, cfg, map[string]string{"password": pass}); len(errs) != 0 {
		t.Fatalf("importer shape: %v", errs)
	}
	if errs := integrationtest.Validate(Integration{}, cfg, nil); len(errs) != 1 || errs[0].Path != "/password" {
		t.Fatalf("no password: %v", errs)
	}
	if _, m, ok := integration.Default.Get(ID); !ok || !m.IsSecret("password") || m.IsSecret("username") {
		t.Fatalf("manifest = %+v", m)
	}
}

func TestSync(t *testing.T) {
	up, srv := newRPC(t, "/nzbget")
	h := integrationtest.New(t)
	inst := connect(t, h, srv.URL+"/nzbget/", user, pass)
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	rid := "nzbget:i_test/app"
	r, ok := h.Resource(rid)
	if !ok || r.Kind != "app" || r.Name != "Test" || r.State != "running" || r.Health != integration.HealthOK {
		t.Fatalf("app = %+v", r)
	}
	a := r.Attrs
	if a["type"] != "nzbget" || a["version"] != "24.8" || a["paused"] != false || a["quota_reached"] != false ||
		a["free_disk_gib"] != 1257.4 || a["post_jobs"] != 1.0 || a["url"] != srv.URL+"/nzbget" {
		t.Fatalf("attrs = %v", a)
	}
	if caps, _ := a["caps"].([]any); len(caps) != 1 || caps[0] != "downloads.queue" {
		t.Fatalf("caps = %v", a["caps"])
	}
	// SABnzbd's keys: 2 350 000 B/s is 18.8 Mb/s, 6839 MiB is 6.7 GiB.
	for metric, v := range map[string]float64{"dl.mbps": 18.8, "queue": 3, "left.gib": 6.7} {
		if se := h.Series(rid, metric); se == nil || se["value"] != v {
			t.Errorf("%s = %v, want %v", metric, se, v)
		}
	}
	if h.Graph().Relations["integration:i_test monitors "+rid] == nil {
		t.Fatal("the instance should monitor its app")
	}
	if got := strings.Join(up.methods, ","); got != "version,status,listgroups" {
		t.Fatalf("methods = %s", got)
	}
	// listgroups' one parameter is deprecated and must be 0.
	if p := up.params[2]; len(p) != 1 || p[0] != 0.0 {
		t.Fatalf("listgroups params = %v", p)
	}
	// The version is read once; then status and listgroups each sync.
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(up.methods, ","); got != "version,status,listgroups,status,listgroups" {
		t.Fatalf("methods = %s", got)
	}

	// Paused: state paused, still ok, 0 Mb/s and not unknown; NZBGet before
	// 24 has only DownloadRate.
	up.set("status", `{"version":"1.1","result":{"DownloadRate":0,"RemainingSizeMB":6839,"DownloadPaused":true}}`)
	up.set("listgroups", `{"version":"1.1","result":[]}`)
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	r, _ = h.Resource(rid)
	if r.State != "paused" || r.Health != integration.HealthOK || r.Attrs["paused"] != true {
		t.Fatalf("paused = %+v", r)
	}
	for metric, v := range map[string]float64{"dl.mbps": 0, "queue": 0} {
		if se := h.Series(rid, metric); se["value"] != v {
			t.Errorf("paused %s = %v", metric, se)
		}
	}
	// The quota stops downloading: attention.
	up.set("status", `{"version":"1.1","result":{"DownloadRate":0,"RemainingSizeMB":10,"QuotaReached":true}}`)
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	r, _ = h.Resource(rid)
	if r.Health != integration.HealthAttention || !strings.Contains(r.Attrs["health.reason"].(string), "quota") {
		t.Fatalf("quota = %s %v", r.Health, r.Attrs["health.reason"])
	}
}

func TestTest(t *testing.T) {
	_, srv := newRPC(t, "")
	res := connect(t, integrationtest.New(t), srv.URL, user, pass).Test(t.Context())
	if !res.OK || res.Version != "24.8" || len(res.Grants) != 1 || res.Grants[0].Name != "NZBGet user" || res.Grants[0].Access != integration.AccessReadWrite {
		t.Fatalf("test = %+v", res)
	}
}

func TestFailures(t *testing.T) {
	up, srv := newRPC(t, "")
	test := func(url, username, password string) integration.TestResult {
		return connect(t, integrationtest.New(t), url, username, password).Test(t.Context())
	}
	res := test(srv.URL, user, "wrong-password")
	if res.OK || res.ErrorCode != integration.CodeAuth || strings.Contains(res.ErrorMessage, "wrong-password") || res.ErrorMessage != "NZBGet rejected the username or password" {
		t.Fatalf("wrong password: %+v", res)
	}
	res = test(srv.URL, "", "")
	if res.OK || res.ErrorCode != integration.CodeAuth || !strings.Contains(res.ErrorMessage, "wants a sign-in") {
		t.Fatalf("no credentials: %+v", res)
	}
	res = test(srv.URL+"/elsewhere", user, pass)
	if res.OK || res.ErrorCode != integration.CodeAPIChanged {
		t.Fatalf("wrong url: %+v", res)
	}
	// The add-only user can't read status.
	up.set("status", `{"version":"1.1","error":{"name":"JSONRPCError","code":1,"message":"Access denied"}}`)
	res = test(srv.URL, user, pass)
	if res.OK || res.ErrorCode != integration.CodeAuth || !strings.Contains(res.ErrorMessage, "AddUsername") || res.Version != "24.8" {
		t.Fatalf("add user: %+v", res)
	}
	up.set("status", "status.json")
	up.set("version", `{"version":"1.1","result":"15.0"}`)
	res = test(srv.URL, user, pass)
	if res.OK || res.ErrorCode != integration.CodeUnsupportedVersion {
		t.Fatalf("too old: %+v", res)
	}
	up.set("version", `{"hello":"world"}`)
	res = test(srv.URL, user, pass)
	if res.OK || res.ErrorCode != integration.CodeAPIChanged {
		t.Fatalf("not JSON-RPC: %+v", res)
	}
}
