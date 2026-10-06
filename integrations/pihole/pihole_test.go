package pihole

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"dash.cha.sh/integrations/internal/fixture"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/integration/integrationtest"
)

const (
	pass = "synthetic-app-password-0001"
	sid  = "c3ludGhldGljLXNpZC0wMDE=" // auth_ok.json's
)

// pihole is a fake Pi-hole v6 serving the synthetic fixtures: POST
// /api/auth with the password answers a session id, the other routes want
// it in X-FTL-SID, and DELETE /api/auth ends it.
type pihole struct {
	*httptest.Server
	dir string

	mu         sync.Mutex
	noPassword bool              // the Pi-hole has no password set
	v5         bool              // a Pi-hole v5: no /api, but /admin/api.php
	seatsFull  bool              // POST /api/auth answers 429
	sessions   map[string]bool   // live session ids
	bodies     map[string]string // path → body, overriding the files
	logins     int
	logouts    int
	paths      []string
	posts      []string // bodies POSTed to /api/dns/blocking (actions_test.go)
}

func newPihole(t *testing.T) *pihole {
	p := &pihole{dir: fixture.Dir(t, ID), sessions: map[string]bool{}, bodies: map[string]string{}}
	p.Server = httptest.NewServer(http.HandlerFunc(p.serve))
	t.Cleanup(p.Close)
	return p
}

func (p *pihole) file(name string) []byte {
	b, err := os.ReadFile(filepath.Join(p.dir, name))
	if err != nil {
		panic(err)
	}
	return b
}

var files = map[string]string{
	"/api/stats/summary": "stats_summary.json",
	"/api/dns/blocking":  "dns_blocking.json",
	"/api/info/version":  "info_version.json",
}

func (p *pihole) serve(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.paths = append(p.paths, r.Method+" "+r.URL.Path)
	w.Header().Set("Content-Type", "application/json")
	if p.v5 {
		if r.URL.Path == "/admin/api.php" {
			if _, ok := r.URL.Query()["versions"]; ok {
				w.Write(p.file("v5_versions.json"))
				return
			}
			w.Write([]byte("[]"))
			return
		}
		http.NotFound(w, r)
		return
	}
	if r.URL.Path == "/api/auth" {
		switch r.Method {
		case http.MethodPost:
			p.logins++
			var body struct{ Password string }
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &body)
			switch {
			case p.seatsFull:
				w.WriteHeader(http.StatusTooManyRequests)
				w.Write(p.file("seats_exceeded.json"))
			case p.noPassword:
				w.Write(p.file("auth_no_password.json"))
			case body.Password != pass:
				w.WriteHeader(http.StatusUnauthorized)
				w.Write(p.file("unauthorized.json"))
			default:
				p.sessions[sid] = true
				w.Write(p.file("auth_ok.json"))
			}
		case http.MethodDelete:
			p.logouts++
			delete(p.sessions, r.Header.Get("X-FTL-SID"))
			w.WriteHeader(http.StatusNoContent)
		}
		return
	}
	name, ok := files[r.URL.Path]
	if !ok {
		http.NotFound(w, r)
		return
	}
	if !p.noPassword && !p.sessions[r.Header.Get("X-FTL-SID")] {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write(p.file("unauthorized.json"))
		return
	}
	if r.Method == http.MethodPost && r.URL.Path == "/api/dns/blocking" {
		p.setBlocking(w, r)
		return
	}
	if body, ok := p.bodies[r.URL.Path]; ok {
		w.Write([]byte(body))
		return
	}
	w.Write(p.file(name))
}

func (p *pihole) with(f func(p *pihole)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	f(p)
}

func (p *pihole) counts() (logins, logouts int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.logins, p.logouts
}

func connect(t *testing.T, h *integrationtest.Harness, url, password string) integration.Instance {
	t.Helper()
	var secrets map[string]string
	if password != "" {
		secrets = map[string]string{"password": password}
	}
	return h.Connect(Integration{}, map[string]any{"url": url, "tls": map[string]any{"mode": "verify"}}, secrets)
}

func TestValidate(t *testing.T) {
	cfg := map[string]any{"url": "https://pihole.lab.example/admin/", "tls": map[string]any{"mode": "verify"}}
	if errs := integrationtest.Validate(Integration{}, cfg, map[string]string{"password": pass}); len(errs) != 0 {
		t.Fatalf("valid config: %v", errs)
	}
	if errs := integrationtest.Validate(Integration{}, cfg, nil); len(errs) != 0 {
		t.Fatalf("no password (a Pi-hole without one): %v", errs)
	}
	_, m, ok := integration.Default.Get(ID)
	if !ok || !reflect.DeepEqual(m.SecretFields(), []string{"password"}) || !reflect.DeepEqual(m.Capabilities, []integration.Capability{"dns.filter"}) {
		t.Fatalf("manifest = %+v", m)
	}
}

func TestSync(t *testing.T) {
	p := newPihole(t)
	h := integrationtest.New(t)
	t.Cleanup(func() { // runs after the harness has closed the instance
		if _, logouts := p.counts(); logouts != 1 {
			t.Errorf("%d logouts when the instance stopped", logouts)
		}
	})
	// The admin page's address works: /admin is dropped.
	inst := connect(t, h, p.URL+"/admin/", pass)
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	rid := "pihole:i_test/app"
	r, ok := h.Resource(rid)
	if !ok || r.Kind != "app" || r.Name != "Test" || r.State != "running" || r.Health != integration.HealthOK || r.Source != "integration:i_test" {
		t.Fatalf("app = %+v", r)
	}
	want := map[string]any{
		"type": "pihole", "url": p.URL, "version": "6.1.4", "ftl_version": "6.2.3", "web_version": "6.2.1",
		"update_available": true, "blocking": "enabled", "gravity_updated_at": "2026-09-28T03:12:00Z", "clients_total": 24.0,
	}
	for k, v := range want {
		if r.Attrs[k] != v {
			t.Errorf("attrs[%s] = %v, want %v", k, r.Attrs[k], v)
		}
	}
	if _, ok := r.Attrs["blocking_timer_s"]; ok {
		t.Error("no timer, no blocking_timer_s")
	}
	if caps, _ := r.Attrs["caps"].([]any); len(caps) != 1 || caps[0] != "dns.filter" {
		t.Errorf("caps = %v", r.Attrs["caps"])
	}
	if h.Graph().Relations["integration:i_test monitors "+rid] == nil {
		t.Fatal("the instance should monitor its app")
	}
	for metric, w := range map[string]struct {
		v    float64
		unit string
	}{
		"queries.24h": {48210, "count"}, "blocked.24h": {9020, "count"}, "blocked.pct": {18.7, "%"},
		"blocklist": {182544, "count"}, "clients": {17, "count"},
	} {
		if se := h.Series(rid, metric); se == nil || se["value"] != w.v || se["unit"] != w.unit {
			t.Errorf("%s = %v, want %v %s", metric, se, w.v, w.unit)
		}
	}
	if logins, _ := p.counts(); logins != 1 {
		t.Fatalf("%d logins", logins)
	}

	// The session expires: one 401, a new login, the request again. The
	// versions aren't read again within the hour.
	p.with(func(p *pihole) { clear(p.sessions) })
	h.Advance(time.Minute)
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	if logins, _ := p.counts(); logins != 2 {
		t.Fatalf("%d logins after the session expired", logins)
	}
	p.with(func(p *pihole) {
		n := 0
		for _, path := range p.paths {
			if path == "GET /api/info/version" {
				n++
			}
		}
		if n != 1 {
			t.Errorf("versions read %d times", n)
		}
	})
	if strings.Contains(h.Logs(), pass) {
		t.Fatal("the password reached the log")
	}
}

func TestBlockingOff(t *testing.T) {
	p := newPihole(t)
	h := integrationtest.New(t)
	inst := connect(t, h, p.URL, pass)
	rid := "pihole:i_test/app"
	for _, c := range []struct{ body, reason string }{
		{`{"blocking":"disabled","timer":241.5,"took":0.0001}`, "Blocking is paused for another 5 min"},
		{`{"blocking":"disabled","timer":null,"took":0.0001}`, "Blocking is off"},
		{`{"blocking":"failed","timer":null,"took":0.0001}`, "Pi-hole couldn't turn blocking on"},
	} {
		p.with(func(p *pihole) { p.bodies["/api/dns/blocking"] = c.body })
		if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
			t.Fatal(err)
		}
		r, _ := h.Resource(rid)
		if r.Health != integration.HealthAttention || r.Attrs["health.reason"] != c.reason {
			t.Errorf("%s: %s %v", c.body, r.Health, r.Attrs["health.reason"])
		}
	}
	if r, _ := h.Resource(rid); r.Attrs["blocking"] != "failed" {
		t.Errorf("blocking = %v", r.Attrs["blocking"])
	}

	// An empty blocklist; -1 (gravity unreadable) is unknown, not a size.
	p.with(func(p *pihole) {
		delete(p.bodies, "/api/dns/blocking")
		p.bodies["/api/stats/summary"] = `{"queries":{"total":0,"blocked":0,"percent_blocked":0},"clients":{"active":0,"total":0},"gravity":{"domains_being_blocked":-1,"last_update":0}}`
	})
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	r, _ := h.Resource(rid)
	if r.Health != integration.HealthAttention || r.Attrs["health.reason"] != "The blocklist is empty: update gravity" {
		t.Errorf("empty blocklist: %s %v", r.Health, r.Attrs["health.reason"])
	}
	if se := h.Series(rid, "blocklist"); se["value"] != nil {
		t.Errorf("blocklist = %v", se)
	}
	// A quiet resolver is 0 queries, not unknown.
	if se := h.Series(rid, "queries.24h"); se["value"] != 0.0 {
		t.Errorf("queries.24h = %v", se)
	}
	if _, ok := r.Attrs["gravity_updated_at"]; ok {
		t.Error("last_update 0 isn't a time")
	}
}

func TestNoPassword(t *testing.T) {
	p := newPihole(t)

	// A Pi-hole without a password needs no session.
	p.with(func(p *pihole) { p.noPassword = true })
	h := integrationtest.New(t)
	inst := connect(t, h, p.URL, "")
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	res := inst.Test(t.Context())
	if !res.OK || !strings.Contains(res.Grants[0].Detail, "no password") {
		t.Fatalf("test = %+v", res)
	}
	if logins, _ := p.counts(); logins != 0 {
		t.Fatalf("%d logins without a password", logins)
	}
	// A password configured for a Pi-hole that has none still works: the
	// session has no id.
	if err := connect(t, integrationtest.New(t), p.URL, pass).Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}

	// The Pi-hole wants one after all.
	p.with(func(p *pihole) { p.noPassword = false })
	err := inst.Sync(t.Context(), integrationtest.Watched)
	if code, _, msg := integration.Classify(err); code != integration.CodeAuth || integration.FieldOf(err) != "password" || msg != "Pi-hole wants a password: enter an app password" {
		t.Fatalf("password wanted: %s %q (%s)", code, msg, integration.FieldOf(err))
	}
}

func TestFailures(t *testing.T) {
	p := newPihole(t)

	// A wrong password.
	err := connect(t, integrationtest.New(t), p.URL, "wrong-password-0002").Sync(t.Context(), integrationtest.Watched)
	if code, _, msg := integration.Classify(err); code != integration.CodeAuth || integration.FieldOf(err) != "password" || !strings.HasPrefix(msg, "Pi-hole rejected the password") {
		t.Fatalf("wrong password: %s %q (%s)", code, msg, integration.FieldOf(err))
	}
	if res := connect(t, integrationtest.New(t), p.URL, "wrong-password-0002").Test(t.Context()); res.OK || res.ErrorCode != integration.CodeAuth {
		t.Fatalf("test with the wrong password = %+v", res)
	}

	// No free sessions: transient, and it says why.
	p.with(func(p *pihole) { p.seatsFull = true })
	err = connect(t, integrationtest.New(t), p.URL, pass).Sync(t.Context(), integrationtest.Watched)
	if code, class, msg := integration.Classify(err); code != integration.CodeUpstream || class != integration.Transient || !strings.Contains(msg, "no free API sessions") {
		t.Fatalf("seats: %s %d %q", code, class, msg)
	}
	p.with(func(p *pihole) { p.seatsFull = false })

	// Pi-hole v5 is named as such.
	p.with(func(p *pihole) { p.v5 = true })
	res := connect(t, integrationtest.New(t), p.URL, pass).Test(t.Context())
	if res.OK || res.ErrorCode != integration.CodeUnsupportedVersion || !strings.HasPrefix(res.ErrorMessage, "Pi-hole v5.18.4 isn't supported") {
		t.Fatalf("v5 = %+v", res)
	}
	res = connect(t, integrationtest.New(t), p.URL, "").Test(t.Context())
	if res.OK || res.ErrorCode != integration.CodeUnsupportedVersion {
		t.Fatalf("v5 without a password = %+v", res)
	}
	p.with(func(p *pihole) { p.v5 = false })

	// Not a Pi-hole at that URL.
	res = connect(t, integrationtest.New(t), p.URL+"/elsewhere", "").Test(t.Context())
	if res.OK || res.ErrorCode != integration.CodeAPIChanged || !strings.Contains(res.ErrorMessage, "Pi-hole v6 or later") {
		t.Fatalf("wrong url = %+v", res)
	}
}

func TestTest(t *testing.T) {
	p := newPihole(t)
	h := integrationtest.New(t)
	res := connect(t, h, p.URL, pass).Test(t.Context())
	if !res.OK || res.Version != "6.1.4" || len(res.Grants) != 1 || res.Grants[0].Access != integration.AccessAdmin {
		t.Fatalf("test = %+v", res)
	}
	if _, ok := h.Resource("pihole:i_test/app"); ok {
		t.Fatal("Test must not emit facts")
	}
}

func TestUpdateAvailable(t *testing.T) {
	s := func(v string) *string { return &v }
	var v versions
	v.Version.Core.Local.Version, v.Version.Core.Remote.Version = s("v6.1.4"), s("v6.1.4")
	if v.updateAvailable() {
		t.Fatal("same versions")
	}
	v.Version.Web.Local.Version, v.Version.Web.Remote.Version = s("v6.2"), nil
	if v.updateAvailable() {
		t.Fatal("no remote version known")
	}
	// In Docker, the image tag decides.
	v.Version.FTL.Local.Version, v.Version.FTL.Remote.Version = s("v6.2"), s("v6.3")
	v.Version.Docker.Local, v.Version.Docker.Remote = s("2026.09.0"), s("2026.09.0")
	if v.updateAvailable() {
		t.Fatal("docker: same tag")
	}
	v.Version.Docker.Remote = s("2026.10.0")
	if !v.updateAvailable() {
		t.Fatal("docker: newer tag")
	}
}
