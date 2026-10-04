package runtime

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/topics"
)

// The coordinator tests run in a synctest bubble: time is fake and only
// moves when every goroutine is blocked, so schedules are exact.

// script drives a fake instance: each Sync takes the next error (nil when
// they run out) and records when it ran and what it was asked for.
type script struct {
	mu    sync.Mutex
	errs  []error
	calls []time.Time
	wants []integration.Interest
	subs  atomic.Int32 // running Subscribe calls
}

func (s *script) next(want integration.Interest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, time.Now())
	s.wants = append(s.wants, want)
	if len(s.errs) == 0 {
		return nil
	}
	err := s.errs[0]
	s.errs = s.errs[1:]
	return err
}

func (s *script) times() []time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Time(nil), s.calls...)
}

func (s *script) want(i int) integration.Interest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.wants[i]
}

func (s *script) gaps() []time.Duration {
	ts := s.times()
	var out []time.Duration
	for i := 1; i < len(ts); i++ {
		out = append(out, ts[i].Sub(ts[i-1]))
	}
	return out
}

type fakeInteg struct {
	s         *script
	m         integration.Manifest
	subscribe bool
	panics    bool
}

func (f fakeInteg) Manifest() integration.Manifest { return f.m }

func (f fakeInteg) Connect(_ context.Context, _ integration.Config, d integration.Deps) (integration.Instance, error) {
	in := &fakeInst{s: f.s, d: d, panics: f.panics}
	if f.subscribe {
		return &subInst{in}, nil
	}
	return in, nil
}

type fakeInst struct {
	s      *script
	d      integration.Deps
	panics bool
}

func (i *fakeInst) Test(context.Context) integration.TestResult {
	return integration.TestResult{OK: true}
}
func (i *fakeInst) Close() error { return nil }

func (i *fakeInst) Sync(_ context.Context, want integration.Interest) error {
	if err := i.s.next(want); err != nil {
		return err
	}
	if i.panics {
		panic("boom")
	}
	return i.d.Emit.Replace("", integration.Facts{
		Resources: []integration.Resource{{ID: "app:fake", Kind: "app", Name: "Fake", State: "running", Health: integration.Health{Status: integration.HealthOK}}},
		Relations: []integration.Relation{{From: i.d.Emit.Self(), Type: "monitors", To: "app:fake"}},
		Metrics:   []integration.Metric{{Resource: "app:fake", Key: "requests", Unit: "count", Max: 10, Value: 3}},
	})
}

type subInst struct{ *fakeInst }

func (i *subInst) Subscribe(ctx context.Context, _ integration.Emitter) error {
	i.s.subs.Add(1)
	defer i.s.subs.Add(-1)
	<-ctx.Done()
	return nil
}

var discardLog = slog.New(slog.DiscardHandler)

func testCoordinator(f fakeInteg, poll integration.Poll) (*coordinator, *topics.Set, *Watch) {
	f.m = integration.Manifest{ID: "fake", Name: "Fake", Version: "0", Tier: integration.Bronze, Engine: integration.EngineGo,
		ConfigSchema: integration.Object(), Poll: poll.WithDefaults()}
	set := topics.New(graph.New(), time.Now())
	w := NewWatch()
	st := &state{}
	e := NewEmitter(set, "in_test", nil)
	e.report = st.report
	c := &coordinator{
		name: "Fake", integ: f, man: f.m, cfg: integration.Config{InstanceID: "in_test", Name: "Fake"},
		emit: e, st: st, watch: w, log: discardLog, jitter: func(d time.Duration) time.Duration { return d },
	}
	c.deps = func() (integration.Deps, func()) {
		return integration.Deps{Emit: e, Now: time.Now, Log: discardLog}, func() {}
	}
	return c, set, w
}

func statusErr(code int) error {
	return integration.CheckResponse(&http.Response{StatusCode: code, Request: &http.Request{Method: "GET", URL: &url.URL{Path: "/api/status"}}})
}

func series(t *testing.T, set *topics.Set, key string) seriesDoc {
	t.Helper()
	var s seriesDoc
	b := set.Metrics.Item(topics.Series, key)
	if b == nil {
		t.Fatalf("no series %s", key)
	}
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func incident(t *testing.T, set *topics.Set, id string) integration.Incident {
	t.Helper()
	var in integration.Incident
	b := set.Inbox.Item(topics.Incidents, id)
	if b == nil {
		t.Fatalf("no incident %s", id)
	}
	if err := json.Unmarshal(b, &in); err != nil {
		t.Fatal(err)
	}
	return in
}

func TestCoordinatorWatchedAndIdle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := &script{}
		c, set, w := testCoordinator(fakeInteg{s: s, subscribe: true}, integration.Poll{Interval: 30 * time.Second, Idle: 5 * time.Minute})
		t0 := time.Now()
		c.start(context.Background())
		defer c.stop()

		// The first sync comes quickly (within 2 s of jitter), then nobody is
		// watching, so the next waits the idle interval.
		time.Sleep(3 * time.Second)
		synctest.Wait()
		calls := s.times()
		if len(calls) != 1 || calls[0].Sub(t0) > 2*time.Second {
			t.Fatalf("first sync: %v after start", calls)
		}
		if s.want(0).Watched {
			t.Fatal("nobody is watching yet")
		}
		if s.subs.Load() != 0 {
			t.Fatal("the subscription runs only while watched")
		}
		time.Sleep(4 * time.Minute)
		if n := len(s.times()); n != 1 {
			t.Fatalf("%d syncs while idle, want 1", n)
		}

		// A tab subscribes: the instance is stale for the watched rate, so it
		// syncs now, then every 30 s, and the subscription starts.
		w.add("graph:kind/app,container", 1)
		synctest.Wait()
		calls = s.times()
		if len(calls) != 2 || !calls[1].Equal(time.Now()) {
			t.Fatalf("on watch: %v, now %v", calls, time.Now())
		}
		if got := s.want(1); !got.Watched || len(got.Kinds) != 2 || got.Kinds[0] != "app" {
			t.Fatalf("interest = %+v", got)
		}
		if s.subs.Load() != 1 {
			t.Fatal("the subscription should run while watched")
		}
		time.Sleep(61 * time.Second)
		gaps := s.gaps()
		if len(gaps) != 3 || gaps[1] != 30*time.Second || gaps[2] != 30*time.Second {
			t.Fatalf("watched gaps = %v", gaps)
		}

		// The tab goes away: back to the idle interval, measured from the last
		// sync; the subscription stops.
		w.add("graph:kind/app,container", -1)
		synctest.Wait()
		if s.subs.Load() != 0 {
			t.Fatal("the subscription should stop when nobody watches")
		}
		time.Sleep(4 * time.Minute)
		if n := len(s.times()); n != 4 {
			t.Fatalf("%d syncs, want 4", n)
		}
		time.Sleep(time.Minute)
		synctest.Wait()
		if gaps := s.gaps(); len(gaps) != 4 || gaps[3] != 5*time.Minute {
			t.Fatalf("idle gap = %v", gaps)
		}

		// What the sync published, and the instance's own resource.
		if r, ok := set.Graph.Get("app:fake"); !ok || r.Source != "integration:in_test" {
			t.Fatalf("app:fake = %+v %v", r, ok)
		}
		self, ok := set.Graph.Get("integration:in_test")
		if !ok || self.Health != integration.HealthOK || self.State != "connected" || self.Attrs["status"] != StatusOK {
			t.Fatalf("instance resource = %+v", self)
		}
		if _, rels := set.Graph.Snapshot(nil); rels.Relations["integration:in_test monitors app:fake"] == nil {
			t.Fatal("the monitors relation is missing")
		}
		if se := series(t, set, "app:fake#requests"); se.Value != 3 || se.Stale || len(se.History) != HistoryPoints {
			t.Fatalf("series = %+v", se)
		}
	})
}

func TestCoordinatorBackoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e502 := statusErr(502)
		s := &script{errs: []error{nil, e502, e502, e502, nil}}
		c, set, w := testCoordinator(fakeInteg{s: s}, integration.Poll{Interval: 30 * time.Second})
		w.add("metrics", 1)
		c.start(context.Background())
		defer c.stop()

		time.Sleep(3 * time.Second)
		synctest.Wait()
		if c.st.status() != StatusOK {
			t.Fatalf("after a good sync: %s", c.st.status())
		}
		want := []string{StatusDegraded, StatusDegraded, StatusError, StatusOK}
		wantGaps := []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute}
		for i, d := range wantGaps {
			time.Sleep(d)
			synctest.Wait()
			if got := c.st.status(); got != want[i] {
				t.Fatalf("after sync %d: status %s, want %s", i+2, got, want[i])
			}
			if i < 3 {
				if se := series(t, set, "app:fake#requests"); !se.Stale || se.Value != 3 {
					t.Fatalf("a failed sync keeps the last value, stale: %+v", se)
				}
			}
		}
		if gaps := s.gaps(); len(gaps) != 4 || gaps[0] != wantGaps[0] || gaps[1] != wantGaps[1] || gaps[2] != wantGaps[2] || gaps[3] != wantGaps[3] {
			t.Fatalf("gaps = %v, want %v", gaps, wantGaps)
		}
		if se := series(t, set, "app:fake#requests"); se.Stale {
			t.Fatal("a good sync clears stale")
		}
		snap := c.st.snapshot()
		if snap.LastErr == nil || snap.LastErr.Code != integration.CodeUpstream || len(snap.Attempts) != 5 {
			t.Fatalf("snapshot = %+v", snap)
		}
		// Transient errors never open a repair.
		if set.Inbox.Item(topics.Incidents, "in_test.repair") != nil {
			t.Fatal("a 502 isn't a repair")
		}
		// Back to the watched rate.
		time.Sleep(30 * time.Second)
		synctest.Wait()
		if n := len(s.times()); n != 6 {
			t.Fatalf("%d syncs, want 6", n)
		}
	})
}

func TestCoordinatorAuthRepair(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e401 := statusErr(401)
		s := &script{errs: []error{nil, e401, e401, nil}}
		c, set, w := testCoordinator(fakeInteg{s: s}, integration.Poll{Interval: 30 * time.Second})
		w.add("graph", 1)
		c.start(context.Background())
		defer c.stop()
		time.Sleep(3 * time.Second)
		synctest.Wait()

		// The credential is rejected: a Repair opens, the instance and its
		// resource say repair, the series go stale, and retries slow down.
		time.Sleep(30 * time.Second)
		synctest.Wait()
		if c.st.status() != StatusRepair {
			t.Fatalf("status %s, want repair", c.st.status())
		}
		in := incident(t, set, "in_test.repair")
		if in.Class != integration.ClassRepair || in.Status != integration.StatusOpen || in.Scenario != "auth-rejected" ||
			in.Title != "Fake rejected its credential" || in.Target != "integration:in_test" || in.Suggested.ID != "integration.reconnect" {
			t.Fatalf("repair = %+v", in)
		}
		if len(in.BlastRadius) != 1 || in.BlastRadius[0].Resource != "app:fake" {
			t.Fatalf("blast radius = %+v", in.BlastRadius)
		}
		self, _ := set.Graph.Get("integration:in_test")
		if self.Health != integration.HealthRepair || self.State != "error" {
			t.Fatalf("instance resource = %+v", self)
		}
		if r, _ := set.Graph.Get("app:fake"); r.Health != integration.HealthOK {
			t.Fatal("a repair isn't an outage: what the instance reads keeps its health")
		}
		opened := in.OpenedAt

		// Still rejected five minutes later: the same Repair, untouched.
		time.Sleep(RepairRetry)
		synctest.Wait()
		if n := len(s.times()); n != 3 {
			t.Fatalf("%d syncs, want 3 (retry after %v)", n, RepairRetry)
		}
		if again := incident(t, set, "in_test.repair"); !again.OpenedAt.Equal(opened) || !again.UpdatedAt.Equal(in.UpdatedAt) {
			t.Fatalf("the repair changed: %+v", again)
		}

		// Fixed upstream: the next try works and the Repair resolves itself.
		time.Sleep(RepairRetry)
		synctest.Wait()
		if c.st.status() != StatusOK {
			t.Fatalf("status %s after recovery", c.st.status())
		}
		done := incident(t, set, "in_test.repair")
		if done.Status != integration.StatusResolved || done.ResolvedBy != "auto" || done.Resolution == "" || done.ResolvedAt.IsZero() {
			t.Fatalf("resolved repair = %+v", done)
		}
		if self, _ := set.Graph.Get("integration:in_test"); self.Health != integration.HealthOK {
			t.Fatalf("instance resource after recovery = %+v", self)
		}
	})
}

func TestCoordinatorSurvivesPanics(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := &script{}
		c, _, w := testCoordinator(fakeInteg{s: s, panics: true}, integration.Poll{Interval: 30 * time.Second})
		w.add("graph", 1)
		c.start(context.Background())
		defer c.stop()
		time.Sleep(3 * time.Second)
		synctest.Wait()
		snap := c.st.snapshot()
		if snap.LastErr == nil || snap.LastErr.Code != integration.CodeInternal {
			t.Fatalf("a panic is an internal error: %+v", snap.LastErr)
		}
		time.Sleep(time.Minute)
		synctest.Wait()
		if n := len(s.times()); n != 2 {
			t.Fatalf("%d syncs after a panic, want 2", n)
		}
	})
}
