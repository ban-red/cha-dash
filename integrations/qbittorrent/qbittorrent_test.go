package qbittorrent

import (
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"dash.cha.sh/integrations/internal/fixture"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/integration/integrationtest"
)

const (
	user = "admin"
	pass = "synthetic-webui-pass-0001"
)

// webui is a fake qBittorrent WebUI serving the synthetic fixtures: a
// cookie login (the cookie named as qBittorrent 5 names it), 403 without a
// session, and a sync/maindata feed that goes full → delta → no change.
type webui struct {
	*httptest.Server
	dir string

	mu        sync.Mutex
	sids      map[string]bool
	open      bool // the WebUI bypasses sign-in for the hub's address
	banned    bool
	webAPI    string // overrides webapi_version.txt
	logins    int
	logouts   int
	referers  int
	maindata  map[string]string // rid → body, overriding the fixture files
	maindatas []string          // the rids asked for

	// For actions (actions_test.go): how many torrents run and are
	// stopped, and the POSTs received.
	running, stopped int
	posts            []string
}

func newWebUI(t *testing.T) *webui {
	w := &webui{dir: fixture.Dir(t, ID), sids: map[string]bool{}, maindata: map[string]string{}, running: 11, stopped: 1}
	w.Server = httptest.NewServer(http.HandlerFunc(w.serve))
	t.Cleanup(w.Close)
	return w
}

func (w *webui) file(name string) []byte {
	b, err := os.ReadFile(filepath.Join(w.dir, name))
	if err != nil {
		panic(err)
	}
	return b
}

func (w *webui) serve(rw http.ResponseWriter, r *http.Request) {
	w.mu.Lock()
	defer w.mu.Unlock()
	switch r.URL.Path {
	case "/api/v2/auth/login":
		w.logins++
		if r.Header.Get("Referer") != "" || r.Header.Get("Origin") != "" {
			w.referers++
		}
		switch {
		case w.banned:
			http.Error(rw, "Your IP address has been banned after too many failed authentication attempts.", http.StatusForbidden)
		case r.PostFormValue("username") != user || r.PostFormValue("password") != pass:
			rw.Write([]byte("Fails."))
		default:
			sid := strings.ToLower(rand.Text())
			w.sids[sid] = true
			http.SetCookie(rw, &http.Cookie{Name: "QBT_SID_8080", Value: sid, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
			rw.Write([]byte("Ok."))
		}
		return
	case "/api/v2/auth/logout":
		w.logouts++
		if c, err := r.Cookie("QBT_SID_8080"); err == nil {
			delete(w.sids, c.Value)
		}
		return
	}
	if c, err := r.Cookie("QBT_SID_8080"); !w.open && (err != nil || !w.sids[c.Value]) {
		http.Error(rw, "Forbidden", http.StatusForbidden)
		return
	}
	switch r.URL.Path {
	case "/api/v2/app/version":
		rw.Header().Set("Content-Type", "text/plain; charset=UTF-8")
		rw.Write(w.file("app_version.txt"))
	case "/api/v2/app/webapiVersion":
		rw.Header().Set("Content-Type", "text/plain; charset=UTF-8")
		if w.webAPI != "" {
			rw.Write([]byte(w.webAPI))
			return
		}
		rw.Write(w.file("webapi_version.txt"))
	case "/api/v2/sync/maindata":
		rid := r.URL.Query().Get("rid")
		w.maindatas = append(w.maindatas, rid)
		rw.Header().Set("Content-Type", "application/json")
		if body, ok := w.maindata[rid]; ok {
			rw.Write([]byte(body))
			return
		}
		switch rid {
		case "1":
			rw.Write(w.file("maindata_delta.json"))
		case "2", "3":
			rw.Write([]byte(`{"rid":3}`))
		default:
			rw.Write(w.file("maindata_full.json"))
		}
	case "/api/v2/torrents/info":
		w.torrents(rw, r)
	case "/api/v2/torrents/stop", "/api/v2/torrents/start", "/api/v2/torrents/pause", "/api/v2/torrents/resume":
		w.control(rw, r)
	default:
		http.NotFound(rw, r)
	}
}

func (w *webui) with(f func(w *webui)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	f(w)
}

func (w *webui) count() (logins, logouts, referers int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.logins, w.logouts, w.referers
}

func connect(t *testing.T, h *integrationtest.Harness, url, password string) integration.Instance {
	return h.Connect(Integration{}, map[string]any{"url": url, "tls": map[string]any{"mode": "verify"}, "username": user}, map[string]string{"password": password})
}

func TestValidate(t *testing.T) {
	// The importer's shape.
	cfg := map[string]any{"url": "https://torrents.lab.example", "tls": map[string]any{"mode": "verify"}, "username": "admin"}
	if errs := integrationtest.Validate(Integration{}, cfg, map[string]string{"password": pass}); len(errs) != 0 {
		t.Fatalf("importer shape: %v", errs)
	}
	if errs := integrationtest.Validate(Integration{}, cfg, nil); len(errs) != 1 || errs[0].Path != "/password" {
		t.Fatalf("username without password: %v", errs)
	}
	if errs := integrationtest.Validate(Integration{}, map[string]any{"url": "https://torrents.lab.example"}, map[string]string{"password": pass}); len(errs) != 1 || errs[0].Path != "/username" {
		t.Fatalf("password without username: %v", errs)
	}
	if errs := integrationtest.Validate(Integration{}, map[string]any{"url": "https://torrents.lab.example"}, nil); len(errs) != 0 {
		t.Fatalf("no credentials (the WebUI's bypass): %v", errs)
	}
	_, m, ok := integration.Default.Get(ID)
	if !ok || !reflect.DeepEqual(m.SecretFields(), []string{"password"}) || m.ConfigSchema.Property("username").WriteOnly {
		t.Fatalf("manifest = %+v", m)
	}
}

func TestSync(t *testing.T) {
	w := newWebUI(t)
	h := integrationtest.New(t)
	t.Cleanup(func() { // runs after the harness has closed the instance
		if _, logouts, _ := w.count(); logouts != 1 {
			t.Errorf("%d logouts when the instance stopped", logouts)
		}
	})
	inst := connect(t, h, w.URL+"/", pass)
	sync := func() {
		t.Helper()
		if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
			t.Fatal(err)
		}
	}
	rid := "qbittorrent:i_test/app"
	check := func(step string, health string, want map[string]float64) {
		t.Helper()
		r, ok := h.Resource(rid)
		if !ok || r.Kind != "app" || r.Name != "Test" || r.Attrs["type"] != "qbittorrent" || r.Attrs["version"] != "5.1.2" ||
			r.Attrs["connection_status"] != "connected" || string(r.Health) != health {
			t.Fatalf("%s: app = %+v", step, r)
		}
		for metric, v := range want {
			if se := h.Series(rid, metric); se == nil || se["value"] != v {
				t.Errorf("%s: %s = %v", step, metric, se)
			}
		}
	}

	// The full update: one torrent is missing its files.
	sync()
	check("full", integration.HealthAttention, map[string]float64{"dl.mbps": 18.8, "ul.mbps": 7.6, "seeding": 5, "leeching": 3, "queued": 2})
	if r, _ := h.Resource(rid); r.Attrs["health.reason"] != "1 torrent has an error" || r.Attrs["torrents"] != 12.0 {
		t.Fatalf("full: %v", r.Attrs)
	}
	if se := h.Series(rid, "dl.mbps"); se["unit"] != "Mb/s" {
		t.Fatalf("dl.mbps = %v", se)
	}
	if h.Graph().Relations["integration:i_test monitors "+rid] == nil {
		t.Fatal("the instance should monitor its app")
	}
	// The delta: Debian finished, Rocky started, the broken one was removed,
	// and only the download speed changed on the server.
	sync()
	check("delta", integration.HealthOK, map[string]float64{"dl.mbps": 32.8, "ul.mbps": 7.6, "seeding": 6, "leeching": 3, "queued": 2})
	// Nothing changed.
	sync()
	check("no change", integration.HealthOK, map[string]float64{"seeding": 6, "leeching": 3})
	w.with(func(w *webui) {
		if !reflect.DeepEqual(w.maindatas, []string{"0", "1", "2"}) {
			t.Errorf("rids asked for: %v", w.maindatas)
		}
	})
	if logins, _, referers := w.count(); logins != 1 || referers != 0 {
		t.Fatalf("%d logins, %d with a Referer or Origin", logins, referers)
	}

	// The session expires: one 403, a new login, the request again.
	w.with(func(w *webui) { clear(w.sids) })
	sync()
	if logins, _, _ := w.count(); logins != 2 {
		t.Fatalf("%d logins after the session expired", logins)
	}
	if strings.Contains(h.Logs(), pass) {
		t.Fatal("the password reached the log")
	}
}

func TestDisconnected(t *testing.T) {
	w := newWebUI(t)
	w.with(func(w *webui) {
		w.maindata["0"] = `{"rid":1,"full_update":true,"torrents":{},"server_state":{"connection_status":"disconnected","dl_info_speed":0,"up_info_speed":0}}`
	})
	h := integrationtest.New(t)
	if err := connect(t, h, w.URL, pass).Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	r, _ := h.Resource("qbittorrent:i_test/app")
	if r.Health != integration.HealthAttention || r.Attrs["health.reason"] != "qBittorrent has no network connection" {
		t.Fatalf("disconnected = %s %v", r.Health, r.Attrs)
	}
	// Idle is 0, never unknown.
	for _, metric := range []string{"dl.mbps", "seeding", "leeching", "queued"} {
		if se := h.Series("qbittorrent:i_test/app", metric); se["value"] != 0.0 {
			t.Errorf("%s = %v", metric, se)
		}
	}
}

func TestAuthFailures(t *testing.T) {
	w := newWebUI(t)
	h := integrationtest.New(t)
	err := connect(t, h, w.URL, "wrong-password-0002").Sync(t.Context(), integrationtest.Watched)
	if code, _, msg := integration.Classify(err); code != integration.CodeAuth || integration.FieldOf(err) != "password" || msg != "qBittorrent rejected the username or password" {
		t.Fatalf("wrong password: %s %q (%s)", code, msg, integration.FieldOf(err))
	}
	if res := connect(t, integrationtest.New(t), w.URL, "wrong-password-0002").Test(t.Context()); res.OK || res.ErrorCode != integration.CodeAuth {
		t.Fatalf("test with the wrong password = %+v", res)
	}

	w.with(func(w *webui) { w.banned = true })
	err = connect(t, integrationtest.New(t), w.URL, pass).Sync(t.Context(), integrationtest.Watched)
	if code, _, msg := integration.Classify(err); code != integration.CodeAuth || !strings.Contains(msg, "banned") {
		t.Fatalf("banned: %s %q", code, msg)
	}

	// No credentials: fine when the WebUI lets the hub in, a Repair on the
	// username when it doesn't.
	w.with(func(w *webui) { w.banned = false })
	h = integrationtest.New(t)
	inst := h.Connect(Integration{}, map[string]any{"url": w.URL}, nil)
	err = inst.Sync(t.Context(), integrationtest.Watched)
	if code, _, _ := integration.Classify(err); code != integration.CodeAuth || integration.FieldOf(err) != "username" {
		t.Fatalf("sign-in wanted: %v (%s)", err, integration.FieldOf(err))
	}
	w.with(func(w *webui) { w.open = true })
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	res := inst.Test(t.Context())
	if !res.OK || !strings.Contains(res.Grants[0].Detail, "without signing in") {
		t.Fatalf("bypass test = %+v", res)
	}
}

func TestTest(t *testing.T) {
	w := newWebUI(t)
	h := integrationtest.New(t)
	res := connect(t, h, w.URL, pass).Test(t.Context())
	if !res.OK || res.Version != "5.1.2" || len(res.Grants) != 1 || res.Grants[0].Access != integration.AccessAdmin {
		t.Fatalf("test = %+v", res)
	}
	if _, ok := h.Resource("qbittorrent:i_test/app"); ok {
		t.Fatal("Test must not emit facts")
	}
	// An old Web API.
	w.with(func(w *webui) { w.open, w.webAPI = true, "1.9" })
	res = h.Connect(Integration{}, map[string]any{"url": w.URL}, nil).Test(t.Context())
	if res.OK || res.ErrorCode != integration.CodeUnsupportedVersion {
		t.Fatalf("old api = %+v", res)
	}
	// Not qBittorrent at that URL.
	res = h.Connect(Integration{}, map[string]any{"url": w.URL + "/elsewhere"}, nil).Test(t.Context())
	if res.OK || res.ErrorCode != integration.CodeAPIChanged {
		t.Fatalf("wrong url = %+v", res)
	}
}
