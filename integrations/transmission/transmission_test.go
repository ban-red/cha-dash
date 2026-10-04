package transmission

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
	pass = "fake-transmission-password" // synthetic
)

// rpc is a fake Transmission RPC endpoint: basic auth, the session-id
// handshake, and answers by method from the fixture directory (or inline
// JSON when a route starts with "{").
type rpc struct {
	t      *testing.T
	dir    string
	path   string
	status int // forced HTTP status, when set

	mu        sync.Mutex
	session   string
	routes    map[string]string
	conflicts int
	methods   []string
	bodies    []map[string]any
}

func newRPC(t *testing.T, path string) (*rpc, *httptest.Server) {
	r := &rpc{t: t, dir: fixture.Dir(t, ID), path: path, session: "session-1", routes: map[string]string{
		"session-get": "session_get.json", "session-stats": "session_stats.json", "torrent-get": "torrent_get.json",
	}}
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return r, srv
}

func (r *rpc) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if req.URL.Path != r.path || req.Method != http.MethodPost {
		http.NotFound(w, req)
		return
	}
	if u, p, ok := req.BasicAuth(); !ok || u != user || p != pass {
		w.Header().Set("WWW-Authenticate", `Basic realm="Transmission"`)
		http.Error(w, "<h1>401: Unauthorized</h1>", http.StatusUnauthorized)
		return
	}
	if r.status != 0 {
		w.WriteHeader(r.status)
		return
	}
	if req.Header.Get(sessionHeader) != r.session {
		r.conflicts++
		w.Header().Set(sessionHeader, r.session)
		http.Error(w, "<h1>409: Conflict</h1>", http.StatusConflict)
		return
	}
	var body map[string]any
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		r.t.Errorf("body: %v", err)
	}
	method, _ := body["method"].(string)
	r.methods = append(r.methods, method)
	r.bodies = append(r.bodies, body)
	route, ok := r.routes[method]
	if !ok {
		w.Write([]byte(`{"arguments":{},"result":"method name not recognized"}`))
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

func connect(t *testing.T, h *integrationtest.Harness, cfg map[string]any, password string) integration.Instance {
	cfg["tls"] = map[string]any{"mode": "verify"}
	secrets := map[string]string{}
	if password != "" {
		secrets["password"] = password
	}
	return h.Connect(Integration{}, cfg, secrets)
}

func TestValidate(t *testing.T) {
	// The Homepage importer's shapes: credentials or none, rpcUrl → rpc_path.
	for _, c := range []struct {
		cfg     map[string]any
		secrets map[string]string
	}{
		{map[string]any{"url": "https://torrents.home.arpa", "tls": map[string]any{"mode": "verify"}, "username": user}, map[string]string{"password": pass}},
		{map[string]any{"url": "http://nas.home.arpa:9091", "tls": map[string]any{"mode": "verify"}, "rpc_path": "/transmission/"}, nil},
		{map[string]any{"url": "http://nas.home.arpa:9091", "tls": map[string]any{"mode": "verify"}, "rpc_path": "/"}, nil},
	} {
		if errs := integrationtest.Validate(Integration{}, c.cfg, c.secrets); len(errs) != 0 {
			t.Errorf("%v: %v", c.cfg, errs)
		}
	}
	if errs := integrationtest.Validate(Integration{}, map[string]any{"url": "http://nas.home.arpa:9091", "username": user}, nil); len(errs) != 1 || errs[0].Path != "/password" {
		t.Errorf("username without a password: %v", errs)
	}
	if errs := integrationtest.Validate(Integration{}, map[string]any{"url": "http://nas.home.arpa:9091", "rpc_path": "transmission; rm"}, nil); len(errs) != 1 || errs[0].Path != "/rpc_path" {
		t.Errorf("bad rpc_path: %v", errs)
	}
	if _, m, ok := integration.Default.Get(ID); !ok || !m.IsSecret("password") || m.IsSecret("username") {
		t.Fatalf("manifest = %+v", m)
	}
}

func TestSync(t *testing.T) {
	up, srv := newRPC(t, "/transmission/rpc")
	h := integrationtest.New(t)
	inst := connect(t, h, map[string]any{"url": srv.URL, "username": user}, pass)
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	rid := "transmission:i_test/app"
	r, ok := h.Resource(rid)
	if !ok || r.Kind != "app" || r.Name != "Test" || r.State != "running" || r.Health != integration.HealthOK {
		t.Fatalf("app = %+v", r)
	}
	a := r.Attrs
	if a["type"] != "transmission" || a["version"] != "4.0.6" || a["rpc_version"] != 18.0 || a["torrents"] != 12.0 ||
		a["stopped"] != 2.0 || a["errored"] != 0.0 || a["alt_speed"] != false || a["url"] != srv.URL {
		t.Fatalf("attrs = %v", a)
	}
	if links, _ := a["links"].([]any); len(links) != 1 || links[0].(map[string]any)["url"] != srv.URL+"/transmission/web/" {
		t.Fatalf("links = %v", a["links"])
	}
	want := map[string]float64{"dl.mbps": 18.8, "ul.mbps": 3.8, "seeding": 6, "leeching": 2, "queued": 2}
	for metric, v := range want {
		se := h.Series(rid, metric)
		unit := "count"
		if strings.HasSuffix(metric, ".mbps") {
			unit = "Mb/s"
		}
		if se == nil || se["value"] != v || se["unit"] != unit {
			t.Errorf("%s = %v, want %v", metric, se, v)
		}
	}
	if h.Graph().Relations["integration:i_test monitors "+rid] == nil {
		t.Fatal("the instance should monitor its app")
	}
	// One 409 hands out the session id; after that every call carries it.
	if up.conflicts != 1 || strings.Join(up.methods, ",") != "session-get,session-stats,torrent-get" {
		t.Fatalf("conflicts %d, methods %v", up.conflicts, up.methods)
	}
	// Only status and error are asked for: never names or paths.
	if f, _ := up.bodies[2]["arguments"].(map[string]any)["fields"].([]any); len(f) != 2 || f[0] != "status" || f[1] != "error" {
		t.Fatalf("torrent-get fields = %v", up.bodies[2]["arguments"])
	}

	// Transmission restarts: a new session id, picked up on the fly.
	up.mu.Lock()
	up.session = "session-2"
	up.mu.Unlock()
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	if up.conflicts != 2 {
		t.Fatalf("conflicts after a restart = %d", up.conflicts)
	}

	// A torrent with a local error (a missing file) is attention.
	up.set("torrent-get", `{"arguments":{"torrents":[{"status":0,"error":3},{"status":6,"error":0}]},"result":"success"}`)
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	r, _ = h.Resource(rid)
	if r.Health != integration.HealthAttention || r.Attrs["health.reason"] != "1 torrent has an error" {
		t.Fatalf("errored = %s %v", r.Health, r.Attrs["health.reason"])
	}
	// Idle is 0, never unknown.
	up.set("session-stats", `{"arguments":{"downloadSpeed":0,"uploadSpeed":0},"result":"success"}`)
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	if se := h.Series(rid, "dl.mbps"); se["value"] != 0.0 {
		t.Fatalf("idle = %v", se)
	}
}

func TestCustomRPCPath(t *testing.T) {
	_, srv := newRPC(t, "/torrents/rpc")
	res := connect(t, integrationtest.New(t), map[string]any{"url": srv.URL + "/", "username": user, "rpc_path": "/torrents"}, pass).Test(t.Context())
	if !res.OK || res.Version != "4.0.6" {
		t.Fatalf("custom path: %+v", res)
	}
}

func TestTest(t *testing.T) {
	_, srv := newRPC(t, "/transmission/rpc")
	res := connect(t, integrationtest.New(t), map[string]any{"url": srv.URL, "username": user}, pass).Test(t.Context())
	if !res.OK || res.Version != "4.0.6" || len(res.Grants) != 1 || res.Grants[0].Name != "Transmission RPC" || res.Grants[0].Access != integration.AccessAdmin {
		t.Fatalf("test = %+v", res)
	}
}

func TestFailures(t *testing.T) {
	up, srv := newRPC(t, "/transmission/rpc")
	test := func(cfg map[string]any, password string) integration.TestResult {
		return connect(t, integrationtest.New(t), cfg, password).Test(t.Context())
	}
	// A wrong password: an auth Repair on password, never quoted.
	res := test(map[string]any{"url": srv.URL, "username": user}, "wrong-password")
	if res.OK || res.ErrorCode != integration.CodeAuth || strings.Contains(res.ErrorMessage, "wrong-password") || res.ErrorMessage != "Transmission rejected the username or password" {
		t.Fatalf("wrong password: %+v", res)
	}
	// No credentials where they're needed.
	res = test(map[string]any{"url": srv.URL}, "")
	if res.OK || res.ErrorCode != integration.CodeAuth || !strings.Contains(res.ErrorMessage, "wants a sign-in") {
		t.Fatalf("no credentials: %+v", res)
	}
	// Wrong RPC path.
	res = test(map[string]any{"url": srv.URL, "username": user, "rpc_path": "/elsewhere/"}, pass)
	if res.OK || res.ErrorCode != integration.CodeAPIChanged || !strings.Contains(res.ErrorMessage, "RPC path") {
		t.Fatalf("wrong path: %+v", res)
	}
	// The whitelist refuses the hub.
	up.status = http.StatusForbidden
	res = test(map[string]any{"url": srv.URL, "username": user}, pass)
	if res.OK || res.ErrorCode != integration.CodeAuth || !strings.Contains(res.ErrorMessage, "rpc-whitelist") {
		t.Fatalf("whitelist: %+v", res)
	}
	up.status = 0
	// Too old.
	up.set("session-get", `{"arguments":{"rpc-version":14,"version":"2.52 (13304)"},"result":"success"}`)
	res = test(map[string]any{"url": srv.URL, "username": user}, pass)
	if res.OK || res.ErrorCode != integration.CodeUnsupportedVersion || !strings.Contains(res.ErrorMessage, "2.52") {
		t.Fatalf("too old: %+v", res)
	}
	// Something else at that path.
	up.set("session-get", `{"hello":"world"}`)
	res = test(map[string]any{"url": srv.URL, "username": user}, pass)
	if res.OK || res.ErrorCode != integration.CodeAPIChanged {
		t.Fatalf("not the RPC: %+v", res)
	}
}

// A server that answers every request 409 isn't Transmission (or something
// in front of it eats the header): give up after one retry.
func TestEndlessConflict(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(sessionHeader, "always-new-"+r.Header.Get(sessionHeader))
		w.WriteHeader(http.StatusConflict)
	}))
	t.Cleanup(srv.Close)
	res := connect(t, integrationtest.New(t), map[string]any{"url": srv.URL}, "").Test(t.Context())
	if res.OK || res.ErrorCode != integration.CodeAPIChanged || !strings.Contains(res.ErrorMessage, "409") {
		t.Fatalf("endless 409: %+v", res)
	}
}
