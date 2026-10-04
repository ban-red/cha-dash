package demo

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/topics"
	"dash.cha.sh/sim"
)

// docSeries decodes the metrics document's series.
func docSeries(t *testing.T, tp *topics.Set) map[string]sim.Series {
	t.Helper()
	_, doc := tp.Metrics.Snapshot()
	out := map[string]sim.Series{}
	for k, raw := range doc[topics.Series].(map[string]json.RawMessage) {
		var se sim.Series
		if err := json.Unmarshal(raw, &se); err != nil {
			t.Fatal(err)
		}
		out[k] = se
	}
	return out
}

// expectSeries compares the metrics document with a simulator snapshot.
// With full, histories must match too (the grids line up every 30 minutes).
func expectSeries(t *testing.T, tp *topics.Set, st sim.State, full bool) {
	t.Helper()
	got := docSeries(t, tp)
	if len(got) != len(st.Series) {
		t.Fatalf("metrics has %d series, snapshot %d", len(got), len(st.Series))
	}
	for _, want := range st.Series {
		k := topics.SeriesKey(want.Resource, want.Metric)
		g, ok := got[k]
		if !ok {
			t.Fatalf("metrics lacks %s", k)
		}
		if !full {
			g.History, want.History = nil, nil
		}
		if !reflect.DeepEqual(generic(t, g), generic(t, want)) {
			t.Fatalf("%s at %s:\n got %v\nwant %v", k, st.Now.Format(time.TimeOnly), generic(t, g), generic(t, want))
		}
	}
}

func expectInbox(t *testing.T, tp *topics.Set, st sim.State) {
	t.Helper()
	_, doc := tp.Inbox.Snapshot()
	want := map[string]any{topics.Now: st.Now.Format(time.RFC3339), topics.Incidents: map[string]any{}}
	for _, in := range st.Incidents {
		want[topics.Incidents].(map[string]any)[in.ID] = in
	}
	if got := generic(t, doc); !reflect.DeepEqual(got, generic(t, want)) {
		t.Fatalf("inbox at %s:\n got %v\nwant %v", st.Now.Format(time.TimeOnly), got, generic(t, want))
	}
}

func TestMirrorFollowsTheSimulator(t *testing.T) {
	s := newSim(t, 1)
	s.Skip(sim.DefaultAllOpen)
	tp := topics.New(graph.New(), time.Time{})
	m := &mirror{t: tp}
	st := s.Snapshot()
	if err := m.load(st); err != nil {
		t.Fatal(err)
	}
	if len(st.Series) == 0 || len(st.Open()) != 6 {
		t.Fatalf("unexpected starting point: %d series, %d open", len(st.Series), len(st.Open()))
	}
	expectSeries(t, tp, st, true)
	expectInbox(t, tp, st)

	// Through the afternoon's fixes (17:00 restarts debian-nas: its metrics
	// go stale for a tick) in 10-minute batches, checking the full state at
	// every point where the history grid lines up with the clock.
	stale := 0
	for i := 1; i <= 6*6; i++ {
		if err := m.apply(s.Advance(10*time.Minute), s.Now()); err != nil {
			t.Fatal(err)
		}
		st := s.Snapshot()
		expectSeries(t, tp, st, i%3 == 0)
		expectInbox(t, tp, st)
		for _, se := range docSeries(t, tp) {
			if se.Stale {
				stale++
			}
		}
	}
	if stale == 0 {
		t.Fatal("no series was ever stale; the test isn't exercising last-known values")
	}
	if open := s.Snapshot().Open(); len(open) != 0 {
		t.Fatalf("by 20:05 every incident should be resolved, %d open", len(open))
	}
	expectGraph(t, tp.Graph, s.Snapshot())
}

func TestMirrorSampleOffGrid(t *testing.T) {
	// A sample that skips grid points leaves them unknown rather than
	// inventing values.
	m := &mirror{t: topics.New(graph.New(), time.Time{}), step: 30 * time.Minute, axis: time.Unix(0, 0)}
	se := &sim.Series{Resource: "r", Metric: "m", Value: 1, AsOf: time.Unix(0, 0), History: []sim.Num{1, 2, 3, 4}}
	m.series = map[string]*sim.Series{"r#m": se}
	changed := map[string]bool{}
	m.sample(sim.Event{At: time.Unix(0, 0).Add(time.Hour), Samples: []sim.Sample{{Resource: "r", Metric: "m", Value: 9}}}, changed)
	if got := generic(t, se.History); !reflect.DeepEqual(got, generic(t, []any{3, 4, nil, 9})) {
		t.Fatalf("history = %v", got)
	}
	m.sample(sim.Event{At: time.Unix(0, 0).Add(time.Hour + 30*time.Second)}, changed)
	if !se.Stale || se.Value != 9 || !se.AsOf.Equal(time.Unix(0, 0).Add(time.Hour)) {
		t.Fatalf("missing sample should keep the last-known value, stale: %+v", se)
	}
}
