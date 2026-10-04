package checks

import (
	"context"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/integration/integrationtest"
)

// app is a fake web app whose status the test controls.
type app struct {
	*httptest.Server
	status atomic.Int32
}

func newApp(t *testing.T) *app {
	a := &app{}
	a.status.Store(200)
	a.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch code := int(a.status.Load()); code {
		case 302:
			http.Redirect(w, r, "https://sso.lab.example/login", http.StatusFound)
		default:
			w.WriteHeader(code)
			io.WriteString(w, "hello")
		}
	}))
	t.Cleanup(a.Close)
	return a
}

// closedPort is a local port nothing listens on.
func closedPort(t *testing.T) int {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}

func TestRegistered(t *testing.T) {
	_, m, ok := integration.Default.Get(ID)
	if !ok || m.Name != "Checks" || !m.Poll.Background || len(m.SecretFields()) != 0 {
		t.Fatalf("manifest = %+v", m)
	}
}

func TestValidate(t *testing.T) {
	// The importer's shape (docs/plan/07-v0.1.md): no expect_status.
	ok := map[string]any{"targets": []any{
		map[string]any{"name": "Sonarr", "kind": "http", "url": "https://sonarr.lab.example", "interval_s": 60},
		map[string]any{"name": "SSH on pluto", "kind": "tcp", "host": "10.0.11.10", "port": 22, "interval_s": 60},
	}}
	if errs := integrationtest.Validate(Integration{}, ok, nil); len(errs) != 0 {
		t.Fatalf("valid: %v", errs)
	}
	cases := map[string]struct {
		target map[string]any
		path   string
	}{
		"http without url":  {map[string]any{"name": "A", "kind": "http"}, "/targets/0/url"},
		"http with ftp url": {map[string]any{"name": "A", "kind": "http", "url": "ftp://a.lab.example"}, "/targets/0/url"},
		"tcp without host":  {map[string]any{"name": "A", "kind": "tcp", "port": 22}, "/targets/0/host"},
		"tcp without port":  {map[string]any{"name": "A", "kind": "tcp", "host": "a"}, "/targets/0/port"},
		"bad kind":          {map[string]any{"name": "A", "kind": "icmp"}, "/targets/0/kind"},
		"bad port":          {map[string]any{"name": "A", "kind": "tcp", "host": "a", "port": 70000}, "/targets/0/port"},
		"fast interval":     {map[string]any{"name": "A", "kind": "tcp", "host": "a", "port": 1, "interval_s": 1}, "/targets/0/interval_s"},
		"bad status":        {map[string]any{"name": "A", "kind": "http", "url": "http://a", "expect_status": 42}, "/targets/0/expect_status"},
		"symbols only":      {map[string]any{"name": "!!", "kind": "http", "url": "http://a"}, "/targets/0/name"},
		"unknown key":       {map[string]any{"name": "A", "kind": "http", "url": "http://a", "method": "HEAD"}, "/targets/0/method"},
		"bad tls":           {map[string]any{"name": "A", "kind": "http", "url": "https://a", "tls": map[string]any{"mode": "pin"}}, "/targets/0/tls"},
	}
	for name, c := range cases {
		errs := integrationtest.Validate(Integration{}, map[string]any{"targets": []any{c.target}}, nil)
		if len(errs) == 0 || errs[0].Path != c.path {
			t.Errorf("%s: %v, want an error at %s", name, errs, c.path)
		}
	}
	dup := map[string]any{"targets": []any{
		map[string]any{"name": "Sonarr", "kind": "http", "url": "http://a"},
		map[string]any{"name": "Sonarr", "kind": "http", "url": "http://b"},
	}}
	if errs := integrationtest.Validate(Integration{}, dup, nil); len(errs) != 1 || errs[0].Path != "/targets/1/name" {
		t.Fatalf("same name twice: %v", errs)
	}
	// Names that differ but slug the same are fine: the later one is numbered.
	h := integrationtest.New(t)
	inst := h.Connect(Integration{}, map[string]any{"targets": []any{
		map[string]any{"name": "Sonarr", "kind": "tcp", "host": "127.0.0.1", "port": closedPort(t)},
		map[string]any{"name": "sonarr!", "kind": "tcp", "host": "127.0.0.1", "port": closedPort(t)},
	}}, nil)
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.Resource("check:i_test/sonarr-2"); !ok {
		t.Fatalf("resources = %v", h.Graph().Resources)
	}
	if errs := integrationtest.Validate(Integration{}, map[string]any{"targets": []any{}}, nil); len(errs) == 0 {
		t.Fatal("no targets")
	}
}

func TestEndToEnd(t *testing.T) {
	web := newApp(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var accepts atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepts.Add(1)
			c.Close()
		}
	}()
	tcpPort := ln.Addr().(*net.TCPAddr).Port

	h := integrationtest.New(t)
	inst := h.Connect(Integration{}, map[string]any{"targets": []any{
		map[string]any{"name": "Sonarr", "kind": "http", "url": web.URL + "/health", "interval_s": 30},
		map[string]any{"name": "SSH", "kind": "tcp", "host": "127.0.0.1", "port": tcpPort, "interval_s": 120},
		map[string]any{"name": "Old NAS", "kind": "tcp", "host": "127.0.0.1", "port": closedPort(t), "fail_after": 1},
	}}, nil)
	if iv := inst.(integration.Intervaler).Interval(); iv != 30*time.Second {
		t.Fatalf("interval = %v, want the fastest target's", iv)
	}
	sync := func() {
		t.Helper()
		if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
			t.Fatal(err)
		}
	}
	sync()

	sonarr, ok := h.Resource("check:i_test/sonarr")
	if !ok || sonarr.Kind != "check" || sonarr.Health != integration.HealthOK || sonarr.State != "online" || sonarr.Source != "integration:i_test" ||
		sonarr.Attrs["check"] != "http" || sonarr.Attrs["status_code"] != 200.0 || sonarr.Attrs["target"] != web.URL+"/health" {
		t.Fatalf("sonarr = %+v", sonarr)
	}
	if links := sonarr.Attrs["links"].([]any); links[0].(map[string]any)["url"] != web.URL+"/health" {
		t.Fatalf("links = %v", links)
	}
	if ssh, _ := h.Resource("check:i_test/ssh"); ssh.Health != integration.HealthOK {
		t.Fatalf("ssh = %+v", ssh)
	}
	if h.Graph().Relations["integration:i_test monitors check:i_test/sonarr"] == nil {
		t.Fatal("the instance should monitor its checks")
	}
	se := h.Series("check:i_test/sonarr", "http.ms")
	if v, ok := se["value"].(float64); !ok || v <= 0 || se["unit"] != "ms" || len(se["history"].([]any)) != 48 {
		t.Fatalf("http.ms = %v", se)
	}
	if se := h.Series("check:i_test/ssh", "tcp.ms"); se["value"] == nil {
		t.Fatalf("tcp.ms = %v", se)
	}

	// fail_after 1: the closed port is down at once, with a problem.
	nas, _ := h.Resource("check:i_test/old-nas")
	if nas.Health != integration.HealthDown || nas.State != "error" || !strings.Contains(nas.Attrs["health.reason"].(string), "refused") {
		t.Fatalf("old nas = %+v", nas)
	}
	if se := h.Series("check:i_test/old-nas", "tcp.ms"); se["value"] != nil {
		t.Fatalf("a failed check is unknown, never 0: %v", se)
	}
	if in, ok := h.Incident("check.old-nas"); !ok || in.Class != "problem" || in.Severity != "down" || in.Title != "Old NAS isn't answering" {
		t.Fatalf("old nas problem = %+v %v", in, ok)
	}

	// Sonarr starts failing: attention, then down with a problem on the
	// third failure (the default fail_after).
	web.status.Store(503)
	for i, want := range []string{integration.HealthAttention, integration.HealthAttention, integration.HealthDown} {
		h.Advance(30 * time.Second)
		sync()
		r, _ := h.Resource("check:i_test/sonarr")
		if string(r.Health) != want {
			t.Fatalf("failure %d: health %s, want %s (%v)", i+1, r.Health, want, r.Attrs)
		}
		_, raised := h.Incident("check.sonarr")
		if raised != (i == 2) {
			t.Fatalf("failure %d: problem raised = %v", i+1, raised)
		}
	}
	in, _ := h.Incident("check.sonarr")
	if in.Status != "open" || in.Target != "check:i_test/sonarr" || in.Suggested.ID != "link.open" || in.Facts["status_code"] != 503.0 ||
		!strings.Contains(in.Summary, "answered 503 Service Unavailable") || !in.OpenedAt.Equal(time.Date(2026, 10, 3, 14, 5, 30, 0, time.UTC)) {
		t.Fatalf("sonarr problem = %+v", in)
	}
	if se := h.Series("check:i_test/sonarr", "http.ms"); se["value"] != nil {
		t.Fatalf("down means unknown latency: %v", se)
	}
	// The SSH target (every 120 s) wasn't due on those 30 s ticks; on the
	// fourth it is.
	waitAccepts := func(n int32) {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); accepts.Load() != n; {
			if time.Now().After(deadline) {
				t.Fatalf("%d tcp checks, want %d", accepts.Load(), n)
			}
			time.Sleep(time.Millisecond)
		}
	}
	waitAccepts(1)
	// Recovery resolves the problem.
	web.status.Store(200)
	h.Advance(30 * time.Second)
	sync()
	waitAccepts(2)
	if r, _ := h.Resource("check:i_test/sonarr"); r.Health != integration.HealthOK {
		t.Fatalf("recovered = %+v", r)
	}
	if in, _ := h.Incident("check.sonarr"); in.Status != "resolved" || in.ResolvedBy != "auto" || !strings.Contains(in.Resolution, "Sonarr answered again") {
		t.Fatalf("resolved = %+v", in)
	}
}

func TestExpectStatusAndRedirects(t *testing.T) {
	web := newApp(t)
	h := integrationtest.New(t)
	inst := h.Connect(Integration{}, map[string]any{"targets": []any{
		map[string]any{"name": "Login", "kind": "http", "url": web.URL},
		map[string]any{"name": "Teapot", "kind": "http", "url": web.URL, "expect_status": 418},
	}}, nil)
	web.status.Store(302) // a redirect to SSO means the app is up; it isn't followed
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	if r, _ := h.Resource("check:i_test/login"); r.Health != integration.HealthOK || r.Attrs["status_code"] != 302.0 {
		t.Fatalf("302 = %+v", r)
	}
	if r, _ := h.Resource("check:i_test/teapot"); r.Health != integration.HealthAttention || !strings.Contains(r.Attrs["health.reason"].(string), "expected 418") {
		t.Fatalf("expect_status = %+v", r)
	}
}

func TestTest(t *testing.T) {
	web := newApp(t)
	h := integrationtest.New(t)
	targets := []any{
		map[string]any{"name": "Web", "kind": "http", "url": web.URL},
		map[string]any{"name": "Gone", "kind": "tcp", "host": "127.0.0.1", "port": closedPort(t)},
	}
	res := h.Connect(Integration{}, map[string]any{"targets": targets[:1]}, nil).Test(t.Context())
	if !res.OK {
		t.Fatalf("all up: %+v", res)
	}
	res = h.Connect(Integration{}, map[string]any{"targets": targets}, nil).Test(t.Context())
	if res.OK || res.ErrorCode != integration.CodeUnreachable || !strings.HasPrefix(res.ErrorMessage, "1 of 2 checks failed: Gone: ") {
		t.Fatalf("one down: %+v", res)
	}
	// Test publishes nothing.
	if _, ok := h.Resource("check:i_test/web"); ok {
		t.Fatal("Test must not emit facts")
	}
}

func TestTOFUPerTarget(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.StartTLS()
	defer srv.Close()
	h := integrationtest.New(t)
	inst := h.Connect(Integration{}, map[string]any{
		"tls":     map[string]any{"mode": "tofu"},
		"targets": []any{map[string]any{"name": "PVE", "kind": "http", "url": srv.URL}},
	}, nil)
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	if r, _ := h.Resource("check:i_test/pve"); r.Health != integration.HealthOK {
		t.Fatalf("tofu target = %+v", r)
	}
	target := h.StoredConfig()["targets"].([]any)[0].(map[string]any)
	if pin := target["tls"].(map[string]any)["pin"]; pin != integration.Fingerprint(srv.Certificate()) {
		t.Fatalf("the pin should be recorded on the target: %v", target)
	}
	if _, ok := h.StoredConfig()["tls"].(map[string]any)["pin"]; ok {
		t.Fatal("a target's pin doesn't belong on the instance")
	}
	// Verify mode against the same self-signed server: down, untrusted.
	h2 := integrationtest.New(t)
	inst = h2.Connect(Integration{}, map[string]any{"targets": []any{map[string]any{"name": "PVE", "kind": "http", "url": srv.URL, "fail_after": 1}}}, nil)
	if err := inst.Sync(context.Background(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	r, _ := h2.Resource("check:i_test/pve")
	if r.Health != integration.HealthDown || !strings.Contains(r.Attrs["health.reason"].(string), "isn't trusted") {
		t.Fatalf("verify against self-signed = %+v", r)
	}
}
