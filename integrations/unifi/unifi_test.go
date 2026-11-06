package unifi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/integration/integrationtest"
)

// Fixtures written from the classic API's documented shapes for UniFi
// Network Application 9.0.114 on UniFi OS 4.1.13 (not recorded: see
// testdata/integrations/unifi/README.md).
const fixtures = "../../testdata/integrations/unifi"

const user, pass = "chadash", "fake-pass-Zq81xV"

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(fixtures, name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// console is a fake UniFi Network: a UniFi OS console, or a standalone
// controller when standalone is set.
type console struct {
	*httptest.Server
	t          *testing.T
	standalone bool

	mu         sync.Mutex
	token      string // the session cookie's value; "" until a login
	logins     int
	logouts    int
	mfa        bool
	forbidden  bool
	health     string // the stat/health fixture
	role       string // the account's role in self/sites
	lastBodies []string
}

func newConsole(t *testing.T, standalone bool) *console {
	c := &console{t: t, standalone: standalone, health: "stat_health.json", role: "readonly"}
	c.Server = httptest.NewServer(http.HandlerFunc(c.serve))
	t.Cleanup(c.Close)
	return c
}

func (c *console) set(f func(c *console)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	f(c)
}

func (c *console) count() (logins, logouts int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.logins, c.logouts
}

func (c *console) cookie() string {
	if c.standalone {
		return "unifises"
	}
	return "TOKEN"
}

func (c *console) json(w http.ResponseWriter, status int, name string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	w.Write(fixture(c.t, name))
}

func (c *console) serve(w http.ResponseWriter, r *http.Request) {
	loginPath, logoutPath, api := "/api/auth/login", "/api/auth/logout", "/proxy/network"
	if c.standalone {
		loginPath, logoutPath, api = "/api/login", "/api/logout", ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case r.URL.Path == loginPath && r.Method == http.MethodPost:
		b, _ := io.ReadAll(r.Body)
		c.lastBodies = append(c.lastBodies, string(b))
		var body struct{ Username, Password string }
		if json.Unmarshal(b, &body) != nil || body.Username != user || body.Password != pass {
			if c.standalone {
				c.json(w, http.StatusBadRequest, "login_standalone_invalid_400.json")
			} else {
				c.json(w, http.StatusUnauthorized, "login_unifios_invalid_401.json")
			}
			return
		}
		if c.mfa {
			c.json(w, 499, "login_unifios_mfa_499.json")
			return
		}
		c.logins++
		c.token = "fake-session-" + string(rune('a'+c.logins))
		http.SetCookie(w, &http.Cookie{Name: c.cookie(), Value: c.token, Path: "/", HttpOnly: true})
		if c.standalone {
			http.SetCookie(w, &http.Cookie{Name: "csrf_token", Value: "fake-csrf", Path: "/"})
			c.json(w, http.StatusOK, "login_standalone.json")
		} else {
			w.Header().Set("X-Csrf-Token", "fake-csrf")
			c.json(w, http.StatusOK, "login_unifios.json")
		}
		return
	case r.URL.Path == logoutPath && r.Method == http.MethodPost:
		if r.Header.Get("X-Csrf-Token") == "fake-csrf" {
			c.logouts++
		}
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodGet || !strings.HasPrefix(r.URL.Path, api+"/api/") {
		http.NotFound(w, r)
		return
	}
	if ck, err := r.Cookie(c.cookie()); err != nil || c.token == "" || ck.Value != c.token {
		c.json(w, http.StatusUnauthorized, "login_required_401.json")
		return
	}
	if c.forbidden {
		c.json(w, http.StatusForbidden, "no_permission_403.json")
		return
	}
	path := strings.TrimPrefix(r.URL.Path, api)
	if path == "/api/self/sites" {
		var doc map[string]any
		json.Unmarshal(fixture(c.t, "self_sites.json"), &doc)
		doc["data"].([]any)[0].(map[string]any)["role"] = c.role
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(doc)
		return
	}
	site, stat, ok := strings.Cut(strings.TrimPrefix(path, "/api/s/"), "/")
	if !ok || (site != "default" && site != "x7k2p9qd") {
		c.json(w, http.StatusBadRequest, "no_site_400.json")
		return
	}
	switch stat {
	case "stat/sysinfo":
		c.json(w, http.StatusOK, "sysinfo.json")
	case "stat/device":
		c.json(w, http.StatusOK, "stat_device.json")
	case "stat/health":
		c.json(w, http.StatusOK, c.health)
	default:
		http.NotFound(w, r)
	}
}

func connect(t *testing.T, h *integrationtest.Harness, c *console, extra map[string]any, password string) integration.Instance {
	t.Helper()
	cfg := map[string]any{"url": c.URL, "tls": map[string]any{"mode": "verify"}, "username": user}
	if c.standalone {
		cfg["platform"] = PlatformStandalone
	}
	maps.Copy(cfg, extra)
	return h.Connect(Integration{}, cfg, map[string]string{"password": password})
}

func syncOnce(t *testing.T, in integration.Instance) {
	t.Helper()
	if err := in.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
}

func value(t *testing.T, h *integrationtest.Harness, rid, key string) any {
	t.Helper()
	s := h.Series(rid, key)
	if s == nil {
		t.Fatalf("no series %s %s", rid, key)
	}
	return s["value"]
}

const (
	gw     = "unifi:i_test/device/00005e005301"
	sw     = "unifi:i_test/device/00005e005302"
	ap     = "unifi:i_test/device/00005e005303"
	garden = "unifi:i_test/device/00005e005304"
	site   = "unifi:i_test/site"
	wan    = "unifi:i_test/wan"
)

func TestRegistered(t *testing.T) {
	_, m, ok := integration.Default.Get(ID)
	if !ok || m.Name != "UniFi Network" || strings.Join(m.SecretFields(), ",") != "password" || m.Category != integration.CatNetwork {
		t.Fatalf("manifest = %+v", m)
	}
}

func TestValidate(t *testing.T) {
	secrets := map[string]string{"password": "p"}
	for _, cfg := range []map[string]any{
		{"url": "https://10.0.40.1", "tls": map[string]any{"mode": "tofu"}, "username": "chadash"},
		{"url": "https://unifi.lab.example:8443", "platform": "standalone", "site": "x7k2p9qd", "username": "chadash"},
	} {
		if errs := integrationtest.Validate(Integration{}, cfg, secrets); len(errs) != 0 {
			t.Errorf("%v: %v", cfg, errs)
		}
	}
	for name, c := range map[string]struct {
		cfg     map[string]any
		secrets map[string]string
		path    string
	}{
		"no password":  {map[string]any{"url": "https://10.0.40.1", "username": "u"}, nil, "/password"},
		"no username":  {map[string]any{"url": "https://10.0.40.1"}, secrets, "/username"},
		"ftp":          {map[string]any{"url": "ftp://10.0.40.1", "username": "u"}, secrets, "/url"},
		"bad site":     {map[string]any{"url": "https://10.0.40.1", "username": "u", "site": "a/b"}, secrets, "/site"},
		"bad platform": {map[string]any{"url": "https://10.0.40.1", "username": "u", "platform": "cloud"}, secrets, "/platform"},
	} {
		errs := integrationtest.Validate(Integration{}, c.cfg, c.secrets)
		if len(errs) == 0 || errs[0].Path != c.path {
			t.Errorf("%s: %v, want an error at %s", name, errs, c.path)
		}
	}
}

func TestSync(t *testing.T) {
	c := newConsole(t, false)
	h := integrationtest.New(t)
	in := connect(t, h, c, nil, pass)
	syncOnce(t, in)

	s, ok := h.Resource(site)
	if !ok || s.Kind != "network" || s.Name != "Test" || s.Health != integration.HealthAttention ||
		s.Attrs[integration.AttrHealthReason] != "1 device is offline: Garden AP" || s.Attrs["version"] != "9.0.114" ||
		s.Attrs["devices"] != 4.0 || s.Attrs["devices_offline"] != 1.0 || s.Attrs["upgradable"] != 1.0 || s.Attrs["pending_adoption"] != 1.0 {
		t.Fatalf("site = %+v", s)
	}
	g, ok := h.Resource(gw)
	if !ok || g.Kind != "router" || g.Name != "Cloud Gateway" || g.State != "online" || g.Health != integration.HealthOK ||
		g.Attrs["model"] != "UDMPRO" || g.Attrs["version"] != "4.0.21.27000" || g.Attrs["upgradable"] != false ||
		g.Attrs["boot_at"] != "2026-09-19T14:04:00Z" || g.Attrs["ip"] != "10.0.40.1" || g.Labels["chadash.role"] != "network-core" {
		t.Fatalf("gateway = %+v", g)
	}
	if r, _ := h.Resource(sw); r.Kind != "switch" || r.Health != integration.HealthOK {
		t.Fatalf("switch = %+v", r)
	}
	// The upgradable access point.
	a, _ := h.Resource(ap)
	if a.Kind != "access_point" || a.Attrs["upgradable"] != true || a.Attrs["upgrade_to"] != "6.7.31.15618" || a.Attrs["version"] != "6.6.77.15402" {
		t.Fatalf("upgradable AP = %+v", a)
	}
	// The offline access point: health down, since it was last seen.
	o, _ := h.Resource(garden)
	if o.State != "offline" || o.Health != integration.HealthDown || o.Attrs["boot_at"] != nil {
		t.Fatalf("offline AP = %+v", o)
	}
	if v := value(t, h, garden, "clients.count"); v != nil {
		t.Errorf("an offline AP's clients = %v, want unknown", v)
	}
	// The one waiting for adoption isn't published.
	if _, ok := h.Resource("unifi:i_test/device/00005e005305"); ok {
		t.Fatal("a device pending adoption was published")
	}
	w, _ := h.Resource(wan)
	if w.Kind != "wan" || w.State != "online" || w.Health != integration.HealthOK || w.Attrs["isp"] != "Example ISP" {
		t.Fatalf("wan = %+v", w)
	}
	rels := h.Graph().Relations
	for _, r := range []string{
		"integration:i_test monitors " + site,
		"integration:i_test monitors " + gw,
		ap + " part_of " + site,
		wan + " part_of " + gw,
		gw + " provides_network_for " + sw,
		sw + " provides_network_for " + ap,
	} {
		if rels[r] == nil {
			t.Errorf("no relation %s", r)
		}
	}
	for _, m := range []struct {
		rid, key string
		want     float64
	}{
		{site, "clients.count", 26}, {site, "clients.wifi", 11}, {site, "clients.wired", 15}, {site, "clients.guests", 2},
		{ap, "clients.count", 11}, {sw, "clients.count", 14}, {ap, "cpu.pct", 9.1}, {gw, "mem.pct", 54.2},
		{wan, "rx.mbps", 42}, {wan, "tx.mbps", 2.5}, {wan, "latency.ms", 12},
	} {
		if v := value(t, h, m.rid, m.key); v != m.want {
			t.Errorf("%s %s = %v, want %v", m.rid, m.key, v, m.want)
		}
	}
	if _, ok := h.Incident(incidentWAN); ok {
		t.Fatal("a WAN incident while the WAN is up")
	}
	// One sign-in serves every sync.
	syncOnce(t, in)
	if logins, _ := c.count(); logins != 1 {
		t.Fatalf("%d logins for two syncs", logins)
	}
}

func TestWANDown(t *testing.T) {
	c := newConsole(t, false)
	h := integrationtest.New(t)
	in := connect(t, h, c, nil, pass)
	syncOnce(t, in)
	c.set(func(c *console) { c.health = "stat_health_wan_down.json" })
	h.Advance(time.Minute)
	syncOnce(t, in)
	w, _ := h.Resource(wan)
	if w.State != "error" || w.Health != integration.HealthDown || w.Attrs[integration.AttrHealthReason] != "The WAN is down" {
		t.Fatalf("wan = %+v", w)
	}
	if v := value(t, h, wan, "latency.ms"); v != nil {
		t.Errorf("latency while down = %v", v)
	}
	inc, ok := h.Incident(incidentWAN)
	if !ok || inc.Status != integration.StatusOpen || inc.Severity != integration.SeverityDown || inc.Class != integration.ClassProblem ||
		inc.Target != wan || len(inc.Causes) != 1 || inc.Causes[0].Resource != gw || inc.Suggested.ID != "link.open" {
		t.Fatalf("incident = %+v", inc)
	}

	c.set(func(c *console) { c.health = "stat_health.json" })
	h.Advance(time.Minute)
	syncOnce(t, in)
	if inc, _ := h.Incident(incidentWAN); inc.Status != integration.StatusResolved {
		t.Fatalf("after the WAN came back: %+v", inc)
	}
}

func TestNoGateway(t *testing.T) {
	c := newConsole(t, false)
	c.set(func(c *console) { c.health = "stat_health_no_gateway.json" })
	h := integrationtest.New(t)
	syncOnce(t, connect(t, h, c, nil, pass))
	if _, ok := h.Resource(wan); ok {
		t.Fatal("a WAN on a site without a gateway")
	}
	if v := value(t, h, site, "clients.count"); v != 4.0 {
		t.Fatalf("clients = %v", v)
	}
}

func TestStandalone(t *testing.T) {
	c := newConsole(t, true)
	h := integrationtest.New(t)
	in := connect(t, h, c, nil, pass)
	syncOnce(t, in)
	if r, ok := h.Resource(ap); !ok || r.Attrs["upgradable"] != true {
		t.Fatalf("ap = %+v", r)
	}
	if s, _ := h.Resource(site); s.Attrs["unifi_platform"] != PlatformStandalone {
		t.Fatalf("site = %+v", s)
	}
	if res := in.Test(t.Context()); !res.OK || res.Version != "9.0.114" {
		t.Fatalf("test = %+v", res)
	}
	in.Close()
}

func TestSessionExpires(t *testing.T) {
	c := newConsole(t, false)
	h := integrationtest.New(t)
	in := connect(t, h, c, nil, pass)
	syncOnce(t, in)
	c.set(func(c *console) { c.token = "ended-by-the-console" })
	syncOnce(t, in)
	if logins, _ := c.count(); logins != 2 {
		t.Fatalf("%d logins, want a second after the session ended", logins)
	}
}

func TestTestGrants(t *testing.T) {
	c := newConsole(t, false)
	h := integrationtest.New(t)
	res := connect(t, h, c, nil, pass).Test(t.Context())
	if !res.OK || res.Version != "9.0.114" || len(res.Grants) != 1 || res.Grants[0].Name != "UniFi Network: View Only" ||
		res.Grants[0].CanWrite() || !strings.Contains(res.Grants[0].Detail, "Default (default)") {
		t.Fatalf("test = %+v", res)
	}
	if _, ok := h.Resource(site); ok {
		t.Fatal("Test must not emit facts")
	}
	c.set(func(c *console) { c.role = "admin" })
	res = connect(t, h, c, nil, pass).Test(t.Context())
	if w := integration.WriteGrantWarning(res.Grants); !res.OK || w == nil {
		t.Fatalf("admin = %+v", res)
	}
	// A site the account can't see: the test names the ones it can.
	res = connect(t, h, c, map[string]any{"site": "nowhere"}, pass).Test(t.Context())
	if res.OK || res.ErrorCode != integration.CodeInvalidConfig || !strings.Contains(res.ErrorMessage, "default, x7k2p9qd") {
		t.Fatalf("no such site = %+v", res)
	}
}

func TestRepairs(t *testing.T) {
	c := newConsole(t, false)
	h := integrationtest.New(t)

	// A rejected password.
	bad := connect(t, h, c, nil, "wrong")
	err := bad.Sync(t.Context(), integration.Interest{})
	if code, class, _ := integration.Classify(err); code != integration.CodeAuth || class != integration.NeedsRepair || integration.FieldOf(err) != "password" {
		t.Fatalf("rejected = %v", err)
	}
	if res := bad.Test(t.Context()); res.OK || res.ErrorCode != integration.CodeAuth {
		t.Fatalf("rejected test = %+v", res)
	}
	// A UI.com account with two-factor authentication.
	c.set(func(c *console) { c.mfa = true })
	err = connect(t, h, c, nil, pass).Sync(t.Context(), integration.Interest{})
	if code, _, msg := integration.Classify(err); code != integration.CodeAuth || !strings.Contains(msg, "two-factor") || integration.FieldOf(err) != "username" {
		t.Fatalf("mfa = %v", err)
	}
	c.set(func(c *console) { c.mfa = false })
	// No role in UniFi Network.
	c.set(func(c *console) { c.forbidden = true })
	err = connect(t, h, c, nil, pass).Sync(t.Context(), integration.Interest{})
	if code, class, msg := integration.Classify(err); code != CodeMissingPermission || class != integration.NeedsRepair ||
		!strings.Contains(msg, "View Only") || integration.FieldOf(err) != "username" {
		t.Fatalf("forbidden = %v", err)
	}
	c.set(func(c *console) { c.forbidden = false })
	// A site that isn't there.
	err = connect(t, h, c, map[string]any{"site": "nowhere"}, pass).Sync(t.Context(), integration.Interest{})
	if code, class, _ := integration.Classify(err); code != integration.CodeInvalidConfig || class != integration.Permanent || integration.FieldOf(err) != "site" {
		t.Fatalf("no such site = %v", err)
	}
	// The wrong platform: a console set up as a standalone controller.
	err = connect(t, h, c, map[string]any{"platform": PlatformStandalone}, pass).Sync(t.Context(), integration.Interest{})
	var re *integration.RepairError
	if !errors.As(err, &re) || re.Code != integration.CodeAPIChanged || re.Field != "platform" || !strings.Contains(re.Message, "UniFi OS console") {
		t.Fatalf("wrong platform = %v", err)
	}
	// Not UniFi at all.
	other := httptest.NewServer(http.NotFoundHandler())
	defer other.Close()
	err = h.Connect(Integration{}, map[string]any{"url": other.URL, "username": user}, map[string]string{"password": pass}).Sync(t.Context(), integration.Interest{})
	if !errors.As(err, &re) || re.Code != integration.CodeAPIChanged {
		t.Fatalf("not UniFi = %v", err)
	}
}

// recordingState keeps everything saved to it.
type recordingState struct {
	integration.State
	mu    sync.Mutex
	saved []byte
}

func (s *recordingState) Save(ctx context.Context, key string, value []byte) error {
	s.mu.Lock()
	s.saved = append(append(s.saved, key...), value...)
	s.mu.Unlock()
	return s.State.Save(ctx, key, value)
}

// The password never appears in logs, errors, published facts, incidents,
// the stored config or the state; it goes only into the login's body.
func TestNoSecretLeaks(t *testing.T) {
	c := newConsole(t, false)
	h := integrationtest.New(t)
	st := &recordingState{State: h.State}
	h.State = st
	var seen []string
	in := connect(t, h, c, nil, pass)
	syncOnce(t, in)
	c.set(func(c *console) { c.health = "stat_health_wan_down.json" })
	syncOnce(t, in)
	seen = append(seen, in.Test(t.Context()).ErrorMessage)
	in.Close()

	// Failures with the real password, so any error that quoted the
	// request would carry it.
	c.set(func(c *console) { c.forbidden = true })
	f := connect(t, h, c, nil, pass)
	for _, err := range []error{f.Sync(t.Context(), integration.Interest{})} {
		_, _, msg := integration.Classify(err)
		seen = append(seen, err.Error(), msg)
	}
	seen = append(seen, f.Test(t.Context()).ErrorMessage)
	c.set(func(c *console) { c.forbidden, c.mfa = false, true })
	m := connect(t, h, c, nil, pass)
	err := m.Sync(t.Context(), integration.Interest{})
	seen = append(seen, err.Error(), m.Test(t.Context()).ErrorMessage)

	graph, _ := json.Marshal(h.Graph())
	inc, _ := h.Incident(incidentWAN)
	incJSON, _ := json.Marshal(inc)
	stored, _ := json.Marshal(h.StoredConfig())
	st.mu.Lock()
	state := string(st.saved)
	st.mu.Unlock()
	seen = append(seen, h.Logs(), string(graph), string(incJSON), string(stored), state)
	for i, s := range seen {
		if strings.Contains(s, pass) {
			t.Errorf("the password leaked into item %d: %s", i, s)
		}
	}
	// It did reach the login, so the check above means something.
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.lastBodies) == 0 || !strings.Contains(c.lastBodies[0], pass) {
		t.Fatal("the login never carried the password")
	}
}

func TestLogout(t *testing.T) {
	c := newConsole(t, false)
	t.Run("an instance's life", func(t *testing.T) {
		h := integrationtest.New(t)
		syncOnce(t, connect(t, h, c, nil, pass))
	})
	if logins, logouts := c.count(); logins != 1 || logouts != 1 {
		t.Fatalf("%d logins, %d logouts with the CSRF token", logins, logouts)
	}
}
