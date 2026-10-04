package demo

import (
	"encoding/json"
	"maps"
	"reflect"
	"testing"
	"time"

	"dash.cha.sh/internal/graph"
	"dash.cha.sh/sim"
)

func newSim(t *testing.T, seed int64) *sim.Sim {
	t.Helper()
	s, err := sim.New(seed)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// generic round-trips v through JSON, the way a client sees it.
func generic(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// back rebuilds a simulator resource's JSON from a graph resource, to prove
// the mapping keeps every field.
func back(t *testing.T, r graph.Resource) any {
	t.Helper()
	attrs := maps.Clone(r.Attrs)
	take := func(k string) (any, bool) {
		v, ok := attrs[k]
		delete(attrs, k)
		return v, ok
	}
	health := map[string]any{"status": string(r.Health), "since": "0001-01-01T00:00:00Z"}
	out := map[string]any{"id": string(r.ID), "kind": string(r.Kind), "name": r.Name, "source": r.Source, "state": r.State, "health": health}
	if v, ok := take(AttrDesired); ok {
		out["desired"] = v
	}
	if v, ok := take(AttrHealthReason); ok {
		health["reason"] = v
	}
	if v, ok := take(AttrHealthSince); ok {
		health["since"] = v
	}
	if v, ok := take(AttrLinks); ok {
		out["links"] = v
	}
	if v, ok := take(AttrCaps); ok {
		out["caps"] = v
	}
	if len(r.Labels) > 0 {
		out["labels"] = r.Labels
	}
	if len(attrs) > 0 {
		out["attrs"] = attrs
	}
	return generic(t, out)
}

func TestResourceMappingIsLossless(t *testing.T) {
	s := newSim(t, 1)
	check := func() {
		for _, r := range s.Snapshot().Resources {
			gr, err := Resource(r)
			if err != nil {
				t.Fatal(err)
			}
			if got, want := back(t, gr), generic(t, r); !reflect.DeepEqual(got, want) {
				t.Fatalf("%s doesn't survive the mapping:\n got %v\nwant %v", r.ID, got, want)
			}
		}
	}
	check()
	s.Skip(sim.DefaultAllOpen) // repairs, attention and stale reasons
	check()
}

// applyAll applies a batch of events to g the way the driver does: one
// graph batch per Advance.
func applyAll(t *testing.T, g *graph.Graph, events []sim.Event) {
	t.Helper()
	if _, err := g.Update(func(tx *graph.Tx) error {
		for _, ev := range events {
			if err := ApplyGraph(tx, ev); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// expectGraph checks that g holds exactly the snapshot's resources and
// relations.
func expectGraph(t *testing.T, g *graph.Graph, st sim.State) {
	t.Helper()
	_, doc := g.Snapshot(nil)
	if len(doc.Resources) != len(st.Resources) || len(doc.Relations) != len(st.Relations) {
		t.Fatalf("graph has %d resources and %d relations; snapshot %d and %d",
			len(doc.Resources), len(doc.Relations), len(st.Resources), len(st.Relations))
	}
	for _, r := range st.Resources {
		want, err := Resource(r)
		if err != nil {
			t.Fatal(err)
		}
		got, ok := doc.Resources[graph.RID(r.ID)]
		if !ok {
			t.Fatalf("graph lacks %s", r.ID)
		}
		if !reflect.DeepEqual(generic(t, got), generic(t, want)) {
			t.Fatalf("%s differs:\n got %v\nwant %v", r.ID, generic(t, got), generic(t, want))
		}
	}
	for _, rel := range st.Relations {
		if _, ok := doc.Relations[Relation(rel).Key()]; !ok {
			t.Fatalf("graph lacks relation %v", rel)
		}
	}
}

func TestReplayMatchesSnapshot(t *testing.T) {
	s := newSim(t, 7)
	g := graph.New()
	// Anything already in the graph that the home doesn't have goes.
	g.Update(func(tx *graph.Tx) error { return tx.Upsert(graph.Resource{ID: "chadash:hub", Kind: "app"}) })
	if err := LoadGraph(g, s.Snapshot()); err != nil {
		t.Fatal(err)
	}
	expectGraph(t, g, s.Snapshot())

	seen := map[sim.EventType]int{}
	step := func(d time.Duration) {
		events := s.Advance(d)
		for _, ev := range events {
			seen[ev.Type]++
		}
		applyAll(t, g, events)
	}
	// Uneven steps up to the moment everything is open, a fix pressed by
	// hand (qBittorrent's recreates its container: remove, then add), then
	// the rest of the day and into the next.
	for _, d := range []time.Duration{time.Minute, 37 * time.Minute, 2 * time.Hour, 11*time.Hour + 27*time.Minute} {
		step(d)
	}
	fix, err := s.Fix("qbittorrent-oom")
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range fix {
		seen[ev.Type]++
	}
	applyAll(t, g, fix)
	expectGraph(t, g, s.Snapshot()) // mid-recreate: the container is gone
	for range 32 {
		step(30 * time.Minute)
	}
	expectGraph(t, g, s.Snapshot())

	for _, typ := range []sim.EventType{sim.EventResourceUpsert, sim.EventResourceRemove, sim.EventRelationUpsert, sim.EventRelationRemove} {
		if seen[typ] == 0 {
			t.Errorf("the replay never exercised %s", typ)
		}
	}
}
