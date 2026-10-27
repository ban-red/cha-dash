package opnsense

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/integration/integrationtest"
)

// The spike 4.5 capture of the dogfood router (OPNsense 26.7, a key with
// Reporting: Traffic and Diagnostics: System Activity).
const fixtures = "../../testdata/integrations/opnsense"

const key, secret = "fake-key", "fake-secret"

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(fixtures, name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// router is a fake OPNsense API serving the captures.
type router struct {
	*httptest.Server
	t *testing.T

	mu        sync.Mutex
	rx, tx    uint64  // the WAN's counters
	clock     float64 // the router's clock
	link      string
	forbidden map[string]bool // paths answering 403
	menu      []byte
	sse       []byte        // the stream's body
	hold      chan struct{} // closed to end held streams; nil: end after the body
	streams   atomic.Int32
}

func newRouter(t *testing.T) *router {
	r := &router{t: t, rx: 864750560183, tx: 423156125772, clock: 1791078378.847845, link: "2",
		forbidden: map[string]bool{}, menu: fixture(t, "menu_tree.json"), sse: fixture(t, "traffic_stream_5s.sse"), hold: make(chan struct{})}
	t.Cleanup(func() { r.release() })
	r.Server = httptest.NewServer(http.HandlerFunc(r.serve))
	t.Cleanup(r.Close)
	return r
}

func (r *router) release() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.hold != nil {
		close(r.hold)
		r.hold = nil
	}
}

func (r *router) serve(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		http.Error(w, "read-only", http.StatusMethodNotAllowed)
		return
	}
	if u, p, ok := req.BasicAuth(); !ok || u != key || p != secret {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	r.mu.Lock()
	forbidden := r.forbidden[req.URL.Path]
	r.mu.Unlock()
	w.Header().Set("Set-Cookie", "PHPSESSID=ignored")
	if forbidden {
		w.Header().Set("Content-Type", "application/json; charset=UTF-8")
		w.WriteHeader(http.StatusForbidden)
		w.Write(fixture(r.t, "forbidden_403.json"))
		return
	}
	switch req.URL.Path {
	case countersPath:
		w.Header().Set("Content-Type", "application/json; charset=UTF-8")
		w.Write(r.counters())
	case menuPath:
		r.mu.Lock()
		menu := r.menu
		r.mu.Unlock()
		w.Write(menu)
	case streamPath:
		r.streams.Add(1)
		r.mu.Lock()
		body, hold := r.sse, r.hold
		r.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream;charset=UTF-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(body)
		w.(http.Flusher).Flush()
		if hold != nil {
			select {
			case <-hold:
			case <-req.Context().Done():
			}
		}
	default:
		http.NotFound(w, req)
	}
}

// counters is traffic_interface.json with the WAN's counters, link state
// and clock replaced by the router's current ones.
func (r *router) counters() []byte {
	var doc map[string]any
	if err := json.Unmarshal(fixture(r.t, "traffic_interface.json"), &doc); err != nil {
		r.t.Fatal(err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	wan := doc["interfaces"].(map[string]any)["wan"].(map[string]any)
	wan["bytes received"] = strconv.FormatUint(r.rx, 10)
	wan["bytes transmitted"] = strconv.FormatUint(r.tx, 10)
	wan["link state"] = r.link
	doc["time"] = r.clock
	b, _ := json.Marshal(doc)
	return b
}

// pass moves the router's clock and counters on.
func (r *router) pass(seconds float64, rx, tx uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clock += seconds
	r.rx += rx
	r.tx += tx
}

func (r *router) set(f func(r *router)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	f(r)
}

func connect(t *testing.T, h *integrationtest.Harness, r *router, extra map[string]any) *instance {
	t.Helper()
	cfg := map[string]any{"url": r.URL, "tls": map[string]any{"mode": "verify"}}
	maps.Copy(cfg, extra)
	return h.Connect(Integration{}, cfg, map[string]string{"key": key, "secret": secret}).(*instance)
}

func value(t *testing.T, h *integrationtest.Harness, key string) any {
	t.Helper()
	s := h.Series("opnsense:i_test/wan", key)
	if s == nil {
		t.Fatalf("no series %s", key)
	}
	return s["value"]
}

func TestRegistered(t *testing.T) {
	_, m, ok := integration.Default.Get(ID)
	if !ok || m.Name != "OPNsense" || strings.Join(m.SecretFields(), ",") != "key,secret" || m.Poll.Idle != time.Minute {
		t.Fatalf("manifest = %+v", m)
	}
}

func TestValidate(t *testing.T) {
	secrets := map[string]string{"key": "k", "secret": "s"}
	// The importer's shape (testdata/importers/homepage/dogfood): Homepage's
	// username and password become key and secret.
	for _, cfg := range []map[string]any{
		{"url": "http://10.0.11.1", "tls": map[string]any{"mode": "verify"}},
		{"url": "https://opnsense.lab.example", "tls": map[string]any{"mode": "tofu"}, "wan": "opt1"},
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
		"no secret": {map[string]any{"url": "http://10.0.11.1"}, map[string]string{"key": "k"}, "/secret"},
		"no url":    {map[string]any{}, secrets, "/url"},
		"ftp":       {map[string]any{"url": "ftp://10.0.11.1"}, secrets, "/url"},
		"bad wan":   {map[string]any{"url": "http://10.0.11.1", "wan": "WAN!"}, secrets, "/wan"},
	} {
		errs := integrationtest.Validate(Integration{}, c.cfg, c.secrets)
		if len(errs) == 0 || errs[0].Path != c.path {
			t.Errorf("%s: %v, want an error at %s", name, errs, c.path)
		}
	}
}

func TestSync(t *testing.T) {
	r := newRouter(t)
	h := integrationtest.New(t)
	in := connect(t, h, r, nil)
	if err := in.Sync(t.Context(), integration.Interest{}); err != nil {
		t.Fatal(err)
	}
	rt, ok := h.Resource("opnsense:i_test/router")
	if !ok || rt.Kind != "router" || rt.Name != "Test" || rt.State != "running" || rt.Health != integration.HealthOK ||
		rt.Attrs["interfaces"] != 8.0 || rt.Attrs["ip"] != "127.0.0.1" || rt.Labels["chadash.role"] != "network-core" {
		t.Fatalf("router = %+v", rt)
	}
	wan, ok := h.Resource("opnsense:i_test/wan")
	if !ok || wan.Kind != "wan" || wan.Name != "WAN" || wan.State != "online" || wan.Health != integration.HealthOK ||
		wan.Attrs["interface"] != "wan" || wan.Attrs["device"] != "vtnet1" {
		t.Fatalf("wan = %+v", wan)
	}
	rels := h.Graph().Relations
	if rels["opnsense:i_test/wan part_of opnsense:i_test/router"] == nil || rels["integration:i_test monitors opnsense:i_test/router"] == nil {
		t.Fatalf("relations = %v", rels)
	}
	// One read gives neither a rate nor totals: unknown, never 0.
	for _, k := range []string{"rx.mbps", "tx.mbps", "rx.gb_30d", "tx.gb_30d"} {
		if v := value(t, h, k); v != nil {
			t.Errorf("%s = %v after one read", k, v)
		}
	}

	// A minute later: 600 MB down and 150 MB up.
	h.Advance(time.Minute)
	r.pass(60, 600e6, 150e6)
	if err := in.Sync(t.Context(), integration.Interest{}); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]float64{"rx.mbps": 80, "tx.mbps": 20, "rx.gb_30d": 0.6, "tx.gb_30d": 0.15} {
		if v := value(t, h, k); v != want {
			t.Errorf("%s = %v, want %v", k, v, want)
		}
	}
	if s := h.Series("opnsense:i_test/wan", "rx.gb_30d"); s["unit"] != "GB" {
		t.Errorf("rx.gb_30d = %v", s)
	}
	if wan, _ := h.Resource("opnsense:i_test/wan"); wan.Attrs["totals_since"] != "2026-10-03T14:05:00Z" {
		t.Errorf("totals_since = %v", wan.Attrs["totals_since"])
	}

	// The link goes down.
	r.set(func(r *router) { r.link = "1" })
	h.Advance(time.Minute)
	if err := in.Sync(t.Context(), integration.Interest{}); err != nil {
		t.Fatal(err)
	}
	if wan, _ := h.Resource("opnsense:i_test/wan"); wan.Health != integration.HealthDown || wan.State != "error" {
		t.Fatalf("link down = %+v", wan)
	}
}

func TestTotalsSurviveRestart(t *testing.T) {
	r := newRouter(t)
	h := integrationtest.New(t)
	in := connect(t, h, r, nil)
	sync := func(in *instance) {
		t.Helper()
		if err := in.Sync(t.Context(), integration.Interest{}); err != nil {
			t.Fatal(err)
		}
	}
	sync(in)
	h.Advance(time.Minute)
	r.pass(60, 1e9, 2e8)
	sync(in)
	in.Close() // the hub stops; Close saves what's unsaved

	// Two hours later the hub is back; 3 GB passed meanwhile.
	h.Advance(2 * time.Hour)
	r.pass(7200, 3e9, 1e9)
	in = connect(t, h, r, nil)
	sync(in)
	if v := value(t, h, "rx.gb_30d"); v != 4.0 {
		t.Fatalf("after a restart rx.gb_30d = %v, want what passed before and during it", v)
	}
	if v := value(t, h, "tx.gb_30d"); v != 1.2 {
		t.Fatalf("tx.gb_30d = %v", v)
	}
	if v := value(t, h, "rx.mbps"); v != nil {
		t.Fatalf("a rate over a restart = %v", v)
	}

	// The router reboots: its counters start again from zero.
	h.Advance(time.Minute)
	r.set(func(r *router) { r.rx, r.tx, r.clock = 5e8, 1e8, r.clock+60 })
	sync(in)
	if v := value(t, h, "rx.gb_30d"); v != 4.5 {
		t.Fatalf("after a reboot rx.gb_30d = %v", v)
	}
	if v := value(t, h, "rx.mbps"); v != nil {
		t.Fatalf("a rate over a reboot = %v", v)
	}

	// Another WAN interface starts over.
	in = connect(t, h, r, map[string]any{"wan": "lan"})
	sync(in)
	if v := value(t, h, "rx.gb_30d"); v != nil {
		t.Fatalf("a new interface's totals = %v", v)
	}
}

func TestTotalsWindow(t *testing.T) {
	day := func(d, h int) time.Time { return time.Date(2026, 10, d, h, 0, 0, 0, time.UTC) }
	var tt totals
	tt.add(day(1, 23), "wan", &counters{rx: 0, tx: 0})
	// 2 GB over two hours across midnight: half on each day.
	tt.add(day(2, 1), "wan", &counters{rx: 2e9, tx: 0})
	if tt.Days["2026-10-01"][0] != 1e9 || tt.Days["2026-10-02"][0] != 1e9 {
		t.Fatalf("days = %v", tt.Days)
	}
	// Forty days later: the gap spreads over it, and only the last 31
	// days are kept; the sum covers 30.
	tt.add(day(2, 1).AddDate(0, 0, 40), "wan", &counters{rx: 2e9 + 40e9})
	if len(tt.Days) != keepDays {
		t.Fatalf("%d days kept", len(tt.Days))
	}
	rx, _ := tt.sum(day(2, 1).AddDate(0, 0, 40))
	// 1 GB a day, today's first hour 1/24 of one.
	if want := 29e9 + 1e9/24; math.Abs(rx-want) > 1 {
		t.Fatalf("30-day sum = %v, want %v", rx, want)
	}
}

func TestSubscribe(t *testing.T) {
	r := newRouter(t)
	h := integrationtest.New(t)
	in := connect(t, h, r, nil)
	rec := &recorder{self: h.Emitter.Self()}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- in.Subscribe(ctx, rec) }()
	// Four events: the first is a partial interval and only sets the clock,
	// so three rates, from the deltas over the payload's clock.
	want := [][2]float64{{0.72, 0.42}, {0.79, 0.23}, {1, 0.59}}
	waitFor(t, "three rates", func() bool { return len(rec.rates()) == 3 })
	for i, got := range rec.rates() {
		if got != want[i] {
			t.Errorf("rate %d = %v, want %v", i, got, want[i])
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Subscribe after cancel = %v", err)
	}

	// While the stream is live, the counter poll leaves the rate alone.
	go func() { done <- in.Subscribe(t.Context(), h.Emitter) }()
	waitFor(t, "the live rate", func() bool { s := h.Series("opnsense:i_test/wan", "rx.mbps"); return s != nil && s["value"] == 1.0 })
	r.pass(60, 600e6, 0)
	if err := in.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	if v := value(t, h, "rx.mbps"); v != 1.0 {
		t.Fatalf("rx.mbps = %v, want the stream's while it's live", v)
	}
	h.Advance(time.Minute)
	r.pass(60, 600e6, 0)
	if err := in.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	// A minute on with no new event, the poll takes over again.
	if v := value(t, h, "rx.mbps"); v != 80.0 {
		t.Fatalf("rx.mbps = %v, want the polled rate once the stream is quiet", v)
	}
	r.release()
	if err := <-done; err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("a stream the router closes = %v", err)
	}
}

func TestStreamStalls(t *testing.T) {
	r := newRouter(t)
	h := integrationtest.New(t)
	in := connect(t, h, r, nil)
	in.firstWait, in.gapWait = 2*time.Second, 100*time.Millisecond
	// Events, then silence: three missed intervals end it.
	start := time.Now()
	if err := in.Subscribe(t.Context(), h.Emitter); !errors.Is(err, errStalled) || time.Since(start) > 5*time.Second {
		t.Fatalf("stalled stream = %v after %v", err, time.Since(start))
	}
	// No event at all.
	r.set(func(r *router) { r.sse = nil })
	in.firstWait = 100 * time.Millisecond
	if err := in.Subscribe(t.Context(), h.Emitter); !errors.Is(err, errStalled) {
		t.Fatalf("silent stream = %v", err)
	}
}

func TestStreamWindow(t *testing.T) {
	r := newRouter(t)
	h := integrationtest.New(t)
	in := connect(t, h, r, nil)
	in.window = 100 * time.Millisecond
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- in.Subscribe(ctx, h.Emitter) }()
	waitFor(t, "the stream reopened", func() bool { return r.streams.Load() >= 3 })
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Subscribe = %v", err)
	}
}

func TestStreamRefused(t *testing.T) {
	r := newRouter(t)
	r.set(func(r *router) { r.forbidden[streamPath] = true })
	h := integrationtest.New(t)
	err := connect(t, h, r, nil).Subscribe(t.Context(), h.Emitter)
	if code, _, msg := integration.Classify(err); code != CodeMissingPermission || !strings.Contains(msg, "Reporting: Traffic") {
		t.Fatalf("refused stream = %v", err)
	}
}

// The watchdog and window follow the timing captures.
func TestStreamTiming(t *testing.T) {
	for name, interval := range map[string]time.Duration{"traffic_stream_5s_timing.json": 5 * time.Second, "traffic_stream_1s_timing.json": time.Second} {
		var capture struct {
			Path   string `json:"path"`
			Events []struct {
				AtMS  int `json:"at_ms"`
				Bytes int `json:"bytes"`
			} `json:"events"`
		}
		if err := json.Unmarshal(fixture(t, name), &capture); err != nil {
			t.Fatal(err)
		}
		if first := time.Duration(capture.Events[0].AtMS) * time.Millisecond; first >= firstEventWait {
			t.Errorf("%s: first event after %v", name, first)
		}
		for i := 1; i < len(capture.Events); i++ {
			gap := time.Duration(capture.Events[i].AtMS-capture.Events[i-1].AtMS) * time.Millisecond
			if gap < interval || gap > interval+100*time.Millisecond {
				t.Errorf("%s: gap %d is %v", name, i, gap)
			}
			if interval == streamInterval && gap >= eventGapWait {
				t.Errorf("%s: gap %d (%v) would trip the watchdog", name, i, gap)
			}
		}
		if interval == streamInterval {
			if capture.Path != streamPath {
				t.Errorf("the stream is %s, the capture %s", streamPath, capture.Path)
			}
			// About 1 kB an event: a window stays far below the client's cap.
			if perWindow := int(streamWindow/interval) * 1100; perWindow > 1<<20 {
				t.Errorf("%d bytes a window", perWindow)
			}
		}
	}
}

func TestTestGrants(t *testing.T) {
	r := newRouter(t)
	h := integrationtest.New(t)
	res := connect(t, h, r, nil).Test(t.Context())
	// The dogfood key: Reporting: Traffic and Diagnostics: System Activity,
	// both read-only. Logout is everyone's and isn't a grant.
	if !res.OK || len(res.Grants) != 2 || res.Grants[0].Name != "Reporting: Traffic" || res.Grants[1].Name != "Diagnostics: System Activity" {
		t.Fatalf("test = %+v", res)
	}
	if w := integration.WriteGrantWarning(res.Grants); w != nil {
		t.Fatalf("read-only key flagged: %v", w)
	}
	if _, ok := h.Resource("opnsense:i_test/router"); ok {
		t.Fatal("Test must not emit facts")
	}

	// A menu with a settings page: a write grant.
	var menu []menuItem
	if err := json.Unmarshal(fixture(t, "menu_tree.json"), &menu); err != nil {
		t.Fatal(err)
	}
	menu = append(menu, menuItem{ID: "System", VisibleName: "System", Children: []menuItem{
		{ID: "Gateways", VisibleName: "Gateways", Children: []menuItem{{ID: "Configuration", VisibleName: "Configuration", URL: "/ui/routing/configuration"}}}}})
	b, _ := json.Marshal(menu)
	r.set(func(r *router) { r.menu = b })
	res = connect(t, h, r, nil).Test(t.Context())
	w := integration.WriteGrantWarning(res.Grants)
	if !res.OK || w == nil || !strings.Contains(w.Message, "System › Gateways › Configuration") {
		t.Fatalf("settings page = %+v, %v", res, w)
	}

	// Without Reporting: Traffic the test fails, and still lists the grants.
	r.set(func(r *router) { r.forbidden[countersPath] = true })
	res = connect(t, h, r, nil).Test(t.Context())
	if res.OK || res.ErrorCode != CodeMissingPermission || !strings.Contains(res.ErrorMessage, "Reporting: Traffic") || len(res.Grants) != 3 {
		t.Fatalf("no traffic privilege = %+v", res)
	}
	// The menu refused: grants unknown, not a failure.
	r.set(func(r *router) { r.forbidden = map[string]bool{menuPath: true} })
	res = connect(t, h, r, nil).Test(t.Context())
	if !res.OK || len(res.Warnings) != 1 || res.Warnings[0].Code != "grants_unknown" {
		t.Fatalf("no menu = %+v", res)
	}
}

func TestRepairs(t *testing.T) {
	r := newRouter(t)
	h := integrationtest.New(t)
	// A rejected key or secret.
	bad := h.Connect(Integration{}, map[string]any{"url": r.URL}, map[string]string{"key": key, "secret": "wrong"})
	err := bad.Sync(t.Context(), integration.Interest{})
	if code, class, _ := integration.Classify(err); code != integration.CodeAuth || class != integration.NeedsRepair {
		t.Fatalf("rejected = %v", err)
	}
	if res := bad.Test(t.Context()); res.OK || res.ErrorCode != integration.CodeAuth {
		t.Fatalf("rejected test = %+v", res)
	}
	// The privilege missing.
	r.set(func(r *router) { r.forbidden[countersPath] = true })
	err = connect(t, h, r, nil).Sync(t.Context(), integration.Interest{})
	if code, class, msg := integration.Classify(err); code != CodeMissingPermission || class != integration.NeedsRepair ||
		!strings.Contains(msg, "Reporting: Traffic") || integration.FieldOf(err) != "key" {
		t.Fatalf("forbidden = %v", err)
	}
	// A WAN that isn't there.
	r.set(func(r *router) { r.forbidden = map[string]bool{} })
	in := connect(t, h, r, map[string]any{"wan": "opt9"})
	err = in.Sync(t.Context(), integration.Interest{})
	if code, class, msg := integration.Classify(err); code != integration.CodeInvalidConfig || class != integration.Permanent ||
		!strings.Contains(msg, "lan, opt1, opt2, opt3, opt4, opt5, opt6, wan") || integration.FieldOf(err) != "wan" {
		t.Fatalf("no such wan = %v", err)
	}
	if res := in.Test(t.Context()); res.OK || res.ErrorCode != integration.CodeInvalidConfig {
		t.Fatalf("no such wan test = %+v", res)
	}
	// Not OPNsense at all.
	other := httptest.NewServer(http.NotFoundHandler())
	defer other.Close()
	err = h.Connect(Integration{}, map[string]any{"url": other.URL}, map[string]string{"key": key, "secret": secret}).Sync(t.Context(), integration.Interest{})
	var re *integration.RepairError
	if !errors.As(err, &re) || re.Code != integration.CodeAPIChanged || re.Field != "url" {
		t.Fatalf("not OPNsense = %v", err)
	}
}

// recorder is an Emitter that keeps the rates published to it.
type recorder struct {
	self string
	mu   sync.Mutex
	got  [][2]float64
}

func (r *recorder) Self() string { return r.self }
func (r *recorder) Replace(scope string, f integration.Facts) error {
	if scope != scopeRate || len(f.Metrics) != 2 || f.Metrics[0].Key != "rx.mbps" || f.Metrics[1].Key != "tx.mbps" {
		return errors.New("unexpected facts")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = append(r.got, [2]float64{f.Metrics[0].Value, f.Metrics[1].Value})
	return nil
}
func (r *recorder) Raise(integration.Incident) error  { return nil }
func (r *recorder) Resolve(string, string) error      { return nil }
func (r *recorder) SetVersion(string)                 {}
func (r *recorder) SetWarnings([]integration.Warning) {}
func (r *recorder) rates() [][2]float64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][2]float64(nil), r.got...)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !cond(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}
