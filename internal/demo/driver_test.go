package demo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"dash.cha.sh/internal/config"
	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/jsonpatch"
	"dash.cha.sh/internal/realtime"
	"dash.cha.sh/internal/topics"
	"dash.cha.sh/sim"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newDriver(t *testing.T, opts Options) (*Driver, *topics.Set, *fakeClock) {
	t.Helper()
	clock := &fakeClock{t: time.Date(2031, 5, 6, 7, 8, 9, 0, time.UTC)} // any real time
	if opts.Clock == nil {
		opts.Clock = clock.Now
	}
	tp := topics.New(graph.New(), clock.Now())
	d, err := New(tp, opts)
	if err != nil {
		t.Fatal(err)
	}
	return d, tp, clock
}

func incident(t *testing.T, tp *topics.Set, id string) sim.Incident {
	t.Helper()
	raw := tp.Inbox.Item(topics.Incidents, id)
	if raw == nil {
		t.Fatalf("inbox has no incident %s", id)
	}
	var in sim.Incident
	if err := json.Unmarshal(raw, &in); err != nil {
		t.Fatal(err)
	}
	return in
}

func inboxNow(t *testing.T, tp *topics.Set) string {
	t.Helper()
	var s string
	json.Unmarshal(tp.Inbox.Get(topics.Now), &s)
	return s
}

func TestDefaultsAgreeWithConfig(t *testing.T) {
	if config.DefaultDemoAt != DefaultAt {
		t.Fatalf("config's default demo start %v differs from the simulator's all-open moment %v", config.DefaultDemoAt, DefaultAt)
	}
}

func TestStartsAtTheAllOpenMoment(t *testing.T) {
	d, tp, clock := newDriver(t, Options{Seed: 1, At: DefaultAt})
	if got := d.Now(); !got.Equal(time.Date(2026, 10, 3, 14, 5, 0, 0, time.UTC)) {
		t.Fatalf("Now = %v", got)
	}
	if inboxNow(t, tp) != "2026-10-03T14:05:00Z" {
		t.Fatalf("inbox now = %s", inboxNow(t, tp))
	}
	_, doc := tp.Graph.Snapshot(nil)
	if len(doc.Resources) != 129 {
		t.Fatalf("graph has %d resources", len(doc.Resources))
	}
	open := 0
	_, inbox := tp.Inbox.Snapshot()
	for id := range inbox[topics.Incidents].(map[string]json.RawMessage) {
		if incident(t, tp, id).Status == sim.StatusOpen {
			open++
		}
	}
	if open != 6 {
		t.Fatalf("%d incidents open at 14:05, want 6", open)
	}

	// Real time moves the simulated clock; a step publishes it.
	clock.Advance(95 * time.Second)
	if got := d.Now(); !got.Equal(time.Date(2026, 10, 3, 14, 6, 35, 0, time.UTC)) {
		t.Fatalf("Now after 95 s = %v", got)
	}
	v := tp.Metrics.Version()
	d.Step()
	if inboxNow(t, tp) != "2026-10-03T14:06:35Z" || tp.Metrics.Version() == v {
		t.Fatalf("step didn't publish: now %s, metrics version %d", inboxNow(t, tp), tp.Metrics.Version())
	}
	// Stepping again without time passing changes nothing.
	v = tp.Metrics.Version()
	d.Step()
	if tp.Metrics.Version() != v {
		t.Fatal("an idle step changed the metrics")
	}
}

func TestEndpoints(t *testing.T) {
	d, tp, clock := newDriver(t, Options{Seed: 3, At: DefaultAt})
	mux := http.NewServeMux()
	wrapped := 0
	d.Register(mux, func(h http.Handler) http.Handler {
		wrapped++
		return h
	})
	if wrapped != 2 {
		t.Fatalf("%d endpoints wrapped, want 2", wrapped)
	}
	srv := httptest.NewServer(mux)
	defer srv.Close()
	do := func(method, path string) (int, map[string]any) {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+path, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var body map[string]any
		json.NewDecoder(resp.Body).Decode(&body)
		return resp.StatusCode, body
	}

	clock.Advance(20 * time.Second)
	if code, body := do("GET", "/api/demo"); code != 200 || !reflect.DeepEqual(body, map[string]any{"enabled": true, "now": "2026-10-03T14:05:20Z", "seed": 3.0}) {
		t.Fatalf("GET /api/demo: %d %v", code, body)
	}

	if incident(t, tp, "plex-token-revoked").Status != sim.StatusOpen {
		t.Fatal("plex should start open")
	}
	if code, body := do("POST", "/api/demo/incidents/plex-token-revoked/fix"); code != http.StatusAccepted || body["id"] != "plex-token-revoked" {
		t.Fatalf("fix: %d %v", code, body)
	}
	// Pressing it again while it's in progress, or for nothing, is refused.
	if code, body := do("POST", "/api/demo/incidents/plex-token-revoked/fix"); code != http.StatusConflict || body["error"].(map[string]any)["code"] != "conflict" {
		t.Fatalf("second fix: %d %v", code, body)
	} else if msg := body["error"].(map[string]any)["message"].(string); strings.HasPrefix(msg, "sim:") {
		t.Fatalf("message leaks the package prefix: %q", msg)
	}
	if code, _ := do("POST", "/api/demo/incidents/no-such-thing/fix"); code != http.StatusNotFound {
		t.Fatalf("unknown incident: %d", code)
	}

	// Plex reconnects 10 s after the fix; a minute later it's resolved by
	// the action, and the integration is healthy again.
	clock.Advance(time.Minute)
	d.Step()
	in := incident(t, tp, "plex-token-revoked")
	if in.Status != sim.StatusResolved || in.ResolvedBy != "action" || !in.ResolvedAt.Equal(time.Date(2026, 10, 3, 14, 5, 30, 0, time.UTC)) {
		t.Fatalf("after the fix: %+v", in)
	}
	if r, _ := tp.Graph.Get("integration:plex"); r.Health != graph.HealthOK {
		t.Fatalf("integration:plex health = %q", r.Health)
	}
	if code, _ := do("POST", "/api/demo/incidents/plex-token-revoked/fix"); code != http.StatusConflict {
		t.Fatalf("fixing a resolved incident: %d", code)
	}
}

func TestTopicsOverWebSocket(t *testing.T) {
	d, tp, clock := newDriver(t, Options{Seed: 1, At: DefaultAt})
	hub := realtime.New(tp.Source(), realtime.Options{Coalesce: 10 * time.Millisecond})
	srv := httptest.NewServer(hub)
	t.Cleanup(func() {
		hub.Close()
		srv.Close()
	})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	ws.SetReadLimit(16 << 20) // a snapshot of the whole home is a few hundred KB
	send := func(v any) {
		b, _ := json.Marshal(v)
		if err := ws.Write(ctx, websocket.MessageText, b); err != nil {
			t.Fatal(err)
		}
	}
	next := func() map[string]any {
		for {
			_, b, err := ws.Read(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var f map[string]any
			json.Unmarshal(b, &f)
			if f["op"] != "ping" {
				return f
			}
		}
	}
	apply := func(doc map[string]any, f map[string]any) []string {
		t.Helper()
		if f["op"] != "diff" {
			t.Fatalf("want diff, got %v", f["op"])
		}
		b, _ := json.Marshal(f["patch"])
		var ops []jsonpatch.Op
		json.Unmarshal(b, &ops)
		if err := jsonpatch.Apply(doc, ops); err != nil {
			t.Fatal(err)
		}
		var paths []string
		for _, o := range ops {
			paths = append(paths, o.Path)
		}
		return paths
	}
	hubDoc := func() any {
		_, g := tp.Graph.Snapshot(nil)
		_, m := tp.Metrics.Snapshot()
		_, in := tp.Inbox.Snapshot()
		return generic(t, map[string]any{"graph": g, "metrics": m, "inbox": in})
	}
	expectDoc := func(doc map[string]any) {
		t.Helper()
		if !reflect.DeepEqual(generic(t, doc), hubDoc()) {
			t.Fatal("the client's document diverged from the hub's")
		}
	}
	// converge applies diffs until the client holds the hub's document. Each
	// topic publishes on its own, so one change can span two frames.
	converge := func(doc map[string]any) []string {
		t.Helper()
		var paths []string
		for range 5 {
			paths = append(paths, apply(doc, next())...)
			if reflect.DeepEqual(generic(t, doc), hubDoc()) {
				return paths
			}
		}
		t.Fatalf("the client's document didn't converge; paths so far %v", paths)
		return nil
	}

	send(map[string]any{"op": "hello", "v": 1})
	if f := next(); f["op"] != "hello" {
		t.Fatalf("hello: %v", f)
	}
	send(map[string]any{"op": "sub", "id": 1, "topics": []string{"graph", "metrics", "inbox"}})
	snap := next()
	if snap["op"] != "snap" {
		t.Fatalf("want snap, got %v", snap["op"])
	}
	doc := snap["data"].(map[string]any)
	series := doc["metrics"].(map[string]any)["series"].(map[string]any)
	cpu, ok := series["unraid:jupiter#cpu.pct"].(map[string]any)
	if len(series) != len(newSim(t, 1).Snapshot().Series) || !ok || len(cpu["history"].([]any)) != 48 {
		t.Fatalf("metrics snapshot: %d series, jupiter cpu %v", len(series), cpu)
	}
	if inbox := doc["inbox"].(map[string]any); inbox["now"] != "2026-10-03T14:05:00Z" || len(inbox["incidents"].(map[string]any)) != 6 {
		t.Fatalf("inbox snapshot: %v", inbox["now"])
	}
	expectDoc(doc)

	// One sample interval later: new values and a new inbox clock.
	clock.Advance(30 * time.Second)
	d.Step()
	paths := converge(doc)
	var metricsPaths, inboxPaths int
	for _, p := range paths {
		switch {
		case strings.HasPrefix(p, "/metrics/series/"):
			metricsPaths++
		case p == "/inbox/now":
			inboxPaths++
		}
	}
	if metricsPaths == 0 || inboxPaths != 1 {
		t.Fatalf("diff after a tick: %d metrics paths, %d inbox paths (%v)", metricsPaths, inboxPaths, paths)
	}
	if doc["inbox"].(map[string]any)["now"] != "2026-10-03T14:05:30Z" {
		t.Fatalf("inbox now = %v", doc["inbox"].(map[string]any)["now"])
	}
	if got := doc["metrics"].(map[string]any)["series"].(map[string]any)["unraid:jupiter#cpu.pct"].(map[string]any)["as_of"]; got != "2026-10-03T14:05:30Z" {
		t.Fatalf("jupiter cpu as_of = %v", got)
	}

	// A fix changes the graph and the inbox.
	if err := d.Fix("qbittorrent-oom"); err != nil {
		t.Fatal(err)
	}
	paths = converge(doc)
	sawGraph, sawIncident := false, false
	for _, p := range paths {
		sawGraph = sawGraph || strings.HasPrefix(p, "/graph/")
		sawIncident = sawIncident || p == "/inbox/incidents/qbittorrent-oom"
	}
	if !sawGraph || !sawIncident {
		t.Fatalf("fix diff paths: %v", paths)
	}
	expectDoc(doc)
}

func TestNewDay(t *testing.T) {
	d, tp, clock := newDriver(t, Options{Seed: 1, At: 23*time.Hour + 59*time.Minute})
	clock.Advance(2 * time.Minute)
	d.Step()
	if got := inboxNow(t, tp); got != "2026-10-04T00:01:00Z" {
		t.Fatalf("inbox now = %s", got)
	}
	_, inbox := tp.Inbox.Snapshot()
	if n := len(inbox[topics.Incidents].(map[string]json.RawMessage)); n != 0 {
		t.Fatalf("a fresh day should have no incidents yet, has %d", n)
	}
	// The script replays: the failed backup opens at 02:14.
	clock.Advance(2*time.Hour + 14*time.Minute)
	d.Step()
	if in := incident(t, tp, "backup-failed"); in.Status != sim.StatusOpen || !in.OpenedAt.Equal(time.Date(2026, 10, 4, 2, 14, 0, 0, time.UTC)) {
		t.Fatalf("day two: %+v", in)
	}
	if _, doc := tp.Graph.Snapshot(nil); len(doc.Resources) != 129 {
		t.Fatalf("graph has %d resources", len(doc.Resources))
	}
}

func TestClockJumpRestartsTheDay(t *testing.T) {
	d, tp, clock := newDriver(t, Options{Seed: 1, At: DefaultAt})
	clock.Advance(3 * time.Hour)
	d.Step()
	if got := inboxNow(t, tp); got != "2026-10-03T17:05:00Z" {
		t.Fatalf("inbox now = %s", got)
	}
	// 17:05 in the default script: the backup retry and the prune are done.
	if in := incident(t, tp, "backup-failed"); in.Status != sim.StatusResolved {
		t.Fatalf("backup-failed at 17:05: %s", in.Status)
	}
}

func TestWallClock(t *testing.T) {
	clock := &fakeClock{t: time.Date(2027, 1, 15, 9, 30, 12, 500_000_000, time.UTC)}
	d, tp, _ := newDriver(t, Options{Seed: 1, WallClock: true, Clock: clock.Now})
	if got := d.Now(); !got.Equal(time.Date(2027, 1, 15, 9, 30, 12, 0, time.UTC)) {
		t.Fatalf("Now = %v", got)
	}
	if got := inboxNow(t, tp); got != "2027-01-15T09:30:12Z" {
		t.Fatalf("inbox now = %s", got)
	}
	_, inbox := tp.Inbox.Snapshot()
	if n := len(inbox[topics.Incidents].(map[string]json.RawMessage)); n != 3 {
		t.Fatalf("at 09:30 three incidents have opened, inbox has %d", n)
	}
	clock.Advance(time.Minute)
	d.Step()
	if got := inboxNow(t, tp); got != "2027-01-15T09:31:12Z" {
		t.Fatalf("inbox now after a minute = %s", got)
	}
}

func TestBadStart(t *testing.T) {
	if _, err := New(topics.New(graph.New(), time.Now()), Options{At: 25 * time.Hour}); err == nil {
		t.Fatal("25:00 accepted as a time of day")
	}
}

func TestRun(t *testing.T) {
	tp := topics.New(graph.New(), time.Now())
	d, err := New(tp, Options{Seed: 1, At: DefaultAt, SampleInterval: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		d.Run(ctx)
		close(done)
	}()
	start := tp.Metrics.Version()
	deadline := time.Now().Add(5 * time.Second)
	for tp.Metrics.Version() == start {
		if time.Now().After(deadline) {
			t.Fatal("Run never advanced the metrics")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
}
