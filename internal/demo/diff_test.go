package demo

import (
	"encoding/json"
	"math/rand/v2"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"dash.cha.sh/internal/jsonpatch"
	"dash.cha.sh/internal/realtime"
	"dash.cha.sh/internal/topics"
	"dash.cha.sh/sim"
)

// client is one subscription to some topics, fed the way realtime's sub
// feeds a browser tab: each feed's patches, prefixed with the topic, are
// gathered until the next frame and compacted into it.
type client struct {
	t       *testing.T
	topics  []string
	feeds   []realtime.Feed
	doc     map[string]any // what the browser holds
	seq     uint64
	pending []jsonpatch.Op
}

func subscribe(t *testing.T, src realtime.Source, names ...string) *client {
	t.Helper()
	c := &client{t: t, topics: names, doc: map[string]any{}}
	for _, name := range names {
		f, err := src.Open(name, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(f.Close)
		c.feeds = append(c.feeds, f)
		_, d := f.Snapshot()
		c.doc[name] = generic(t, d)
	}
	return c
}

// drain collects every feed's pending patches.
func (c *client) drain() {
	c.t.Helper()
	for i, f := range c.feeds {
		seq, ops, err := f.Drain()
		if err != nil {
			c.t.Fatal(err)
		}
		c.seq = max(c.seq, seq)
		c.pending = append(c.pending, jsonpatch.Prefix(jsonpatch.Pointer(c.topics[i]), ops)...)
	}
}

// flush sends what drain collected as one diff frame and returns its patch
// and its size on the wire; a nil patch means there was nothing to send.
// With apply, the client applies the frame as the browser would.
func (c *client) flush(apply bool) ([]jsonpatch.Op, int) {
	t := c.t
	t.Helper()
	patch := jsonpatch.Compact(c.pending)
	c.pending = nil
	if len(patch) == 0 {
		return nil, 0
	}
	// realtime's diff frame.
	b, err := json.Marshal(struct {
		Op    string         `json:"op"`
		ID    int64          `json:"id"`
		Seq   uint64         `json:"seq"`
		Patch []jsonpatch.Op `json:"patch"`
	}{"diff", 1, c.seq, patch})
	if err != nil {
		t.Fatal(err)
	}
	if apply {
		// Apply what went over the wire, not the Go values.
		var wire struct{ Patch []jsonpatch.Op }
		if err := json.Unmarshal(b, &wire); err != nil {
			t.Fatal(err)
		}
		if err := jsonpatch.Apply(c.doc, wire.Patch); err != nil {
			t.Fatal(err)
		}
	}
	return patch, len(b)
}

// expectFresh checks the client's document against a fresh snapshot of
// each of its topics.
func (c *client) expectFresh(tp *topics.Set, when string) {
	t := c.t
	t.Helper()
	src := tp.Source()
	for _, name := range c.topics {
		f, err := src.Open(name, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, d := f.Snapshot()
		f.Close()
		if !reflect.DeepEqual(c.doc[name], generic(t, d)) {
			t.Fatalf("%s: the client's %s diverged from a fresh snapshot", when, name)
		}
	}
}

// TestDiffBytesPerMinute measures what a browser tab receives: one client
// subscribed to every topic, one frame per sample tick, over the footprint
// spike's window (13:00 to 20:30 of the demo day, 900 ticks). A metrics tick
// should cost only the series fields that changed. (Correctness is
// TestDiffsReproduceTopicsThroughADay's job; this one doesn't apply frames.)
func TestDiffBytesPerMinute(t *testing.T) {
	const ticks = 900
	d, tp, clock := newDriver(t, Options{Seed: 1, At: 13 * time.Hour})
	c := subscribe(t, tp.Source(), topics.Graph, topics.Metrics, topics.Inbox)
	frames, total := 0, 0
	for range ticks {
		clock.Advance(30 * time.Second)
		d.Step()
		c.drain()
		patch, n := c.flush(false)
		if patch == nil {
			continue
		}
		frames++
		total += n
		// The history gains a point every 30 minutes; then a series may go
		// out whole, when that's smaller than its changed fields. Between
		// points, only fields other than the history change.
		if d.Now().Truncate(30 * time.Minute).Equal(d.Now()) {
			continue
		}
		for _, o := range patch {
			if p, ok := strings.CutPrefix(o.Path, "/metrics/series/"); ok && (!strings.Contains(p, "/") || strings.HasSuffix(p, "/history")) {
				t.Fatalf("at %s, between history points: %s %s", d.Now().Format(time.TimeOnly), o.Op, o.Path)
			}
		}
	}
	perMinute := float64(total) / (ticks / 2) / 1000
	t.Logf("%d frames, %.1f kB per client per simulated minute", frames, perMinute)
	// Whole series, 48-point history included, came to ~128 kB a minute.
	if perMinute > 60 {
		t.Errorf("%.1f kB per simulated minute; are whole series back?", perMinute)
	}
}

// TestDiffsReproduceTopicsThroughADay is the core contract for the live
// documents under the demo: snapshot + every diff == a fresh snapshot. It
// runs a whole simulated day from 14:05, through midnight's restart and
// fixes pressed at random, stepping a tick or many at a time, while a client
// drains, coalesces and applies at random moments.
func TestDiffsReproduceTopicsThroughADay(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 8))
	d, tp, clock := newDriver(t, Options{Seed: 1, At: DefaultAt})
	// Feeds with room for long gaps between drains, so diffs coalesce across
	// many steps: history points, fixes, midnight.
	c := subscribe(t, realtime.Mux{
		topics.Metrics: realtime.DocSource{Doc: tp.Metrics, Topic: topics.Metrics, MaxPending: 1 << 20},
		topics.Inbox:   realtime.DocSource{Doc: tp.Inbox, Topic: topics.Inbox, MaxPending: 1 << 20},
	}, topics.Metrics, topics.Inbox)
	end := d.Now().Add(24 * time.Hour)
	steps, checks, fixes := 0, 0, 0
	for d.Now().Before(end) {
		// Half the steps are one sample tick, as in real time; the rest
		// catch up on up to 20 minutes at once, as after a stall.
		n := 1
		if rng.IntN(2) == 0 {
			n += rng.IntN(40)
		}
		clock.Advance(min(time.Duration(n)*30*time.Second, end.Sub(d.Now())))
		d.Step()
		steps++
		if rng.IntN(10) == 0 {
			if id := openIncident(t, tp, rng); id != "" && d.Fix(id) == nil {
				fixes++
			}
		}
		drained := rng.IntN(3) == 0
		if drained {
			c.drain()
		}
		if rng.IntN(4) == 0 {
			c.flush(true)
			if drained { // nothing has changed since the drain
				c.expectFresh(tp, d.Now().Format(time.DateTime))
				checks++
			}
		}
	}
	c.drain()
	c.flush(true)
	c.expectFresh(tp, "end of the day")
	if got := d.Now(); !got.Equal(time.Date(2026, 10, 4, 14, 5, 0, 0, time.UTC)) {
		t.Fatalf("simulated clock at %s, want 14:05 the next day", got)
	}
	if fixes == 0 || checks < 10 {
		t.Fatalf("%d steps, %d fixes and %d checks: the test isn't exercising enough", steps, fixes, checks)
	}
}

// openIncident picks an open incident from the inbox at random, or "".
func openIncident(t *testing.T, tp *topics.Set, rng *rand.Rand) string {
	t.Helper()
	_, doc := tp.Inbox.Snapshot()
	var open []string
	for id, raw := range doc[topics.Incidents].(map[string]json.RawMessage) {
		var in sim.Incident
		if err := json.Unmarshal(raw, &in); err != nil {
			t.Fatal(err)
		}
		if in.Status == sim.StatusOpen {
			open = append(open, id)
		}
	}
	if len(open) == 0 {
		return ""
	}
	slices.Sort(open)
	return open[rng.IntN(len(open))]
}
