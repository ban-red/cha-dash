package graph

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"reflect"
	"testing"

	"dash.cha.sh/internal/jsonpatch"
)

func upsert(t *testing.T, g *Graph, rs ...Resource) uint64 {
	t.Helper()
	v, err := g.Update(func(tx *Tx) error {
		for _, r := range rs {
			if err := tx.Upsert(r); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func relate(t *testing.T, g *Graph, rels ...Relation) {
	t.Helper()
	if _, err := g.Update(func(tx *Tx) error {
		for _, r := range rels {
			if err := tx.Relate(r); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateAndVersion(t *testing.T) {
	g := New()
	if g.Version() != 0 {
		t.Fatal("new graph should be at version 0")
	}
	attrs := map[string]any{"cpu": 0.5, "tags": []any{"a"}}
	labels := map[string]string{"chadash.group": "Media"}
	v := upsert(t, g, Resource{ID: "container:sonarr", Kind: "container", Name: "sonarr", Attrs: attrs, Labels: labels, State: "running", Health: HealthOK})
	if v != 1 {
		t.Fatalf("version = %d, want 1", v)
	}

	// The graph keeps its own copy.
	attrs["cpu"] = 0.9
	attrs["tags"].([]any)[0] = "mutated"
	labels["chadash.group"] = "mutated"
	r, ok := g.Get("container:sonarr")
	if !ok || r.Attrs["cpu"] != 0.5 || r.Attrs["tags"].([]any)[0] != "a" || r.Labels["chadash.group"] != "Media" {
		t.Fatalf("graph value changed through caller's maps: %+v", r)
	}

	// Re-asserting the same facts is not a change.
	if v := upsert(t, g, Resource{ID: "container:sonarr", Kind: "container", Name: "sonarr", Attrs: map[string]any{"cpu": 0.5, "tags": []any{"a"}}, Labels: map[string]string{"chadash.group": "Media"}, State: "running", Health: HealthOK}); v != 1 {
		t.Fatalf("no-op update bumped version to %d", v)
	}
	// Empty maps equal nil maps.
	upsert(t, g, Resource{ID: "host:x", Kind: "host"})
	if v := upsert(t, g, Resource{ID: "host:x", Kind: "host", Labels: map[string]string{}, Attrs: map[string]any{}}); v != 2 {
		t.Fatalf("empty maps counted as a change (version %d)", v)
	}

	// A failing batch applies nothing.
	_, err := g.Update(func(tx *Tx) error {
		tx.Upsert(Resource{ID: "app:half", Kind: "app"})
		return errors.New("boom")
	})
	if err == nil || g.Version() != 2 {
		t.Fatalf("err=%v version=%d", err, g.Version())
	}
	if _, ok := g.Get("app:half"); ok {
		t.Fatal("rolled-back resource is visible")
	}
}

func TestValidation(t *testing.T) {
	g := New()
	cases := []func(tx *Tx) error{
		func(tx *Tx) error { return tx.Upsert(Resource{ID: "", Kind: "host"}) },
		func(tx *Tx) error { return tx.Upsert(Resource{ID: "has space", Kind: "host"}) },
		func(tx *Tx) error { return tx.Upsert(Resource{ID: "host:a"}) },
		func(tx *Tx) error { return tx.Relate(Relation{From: "x", To: "y", Type: "runs_on"}) },
	}
	for i, fn := range cases {
		if _, err := g.Update(fn); err == nil {
			t.Errorf("case %d: expected an error", i)
		}
	}
	upsert(t, g, Resource{ID: "a", Kind: "app"}, Resource{ID: "b", Kind: "app"})
	if _, err := g.Update(func(tx *Tx) error { return tx.Relate(Relation{From: "a", To: "b", Type: "depends on"}) }); err == nil {
		t.Error("relation type with a space should fail")
	}
	if _, err := g.Update(func(tx *Tx) error { return tx.Relate(Relation{From: "a", To: "nope", Type: "depends_on"}) }); !errors.Is(err, ErrNoEndpoint) {
		t.Errorf("missing endpoint: %v", err)
	}
}

func TestRemoveCascades(t *testing.T) {
	g := New()
	upsert(t, g, Resource{ID: "host:jupiter", Kind: "host"}, Resource{ID: "engine:jupiter", Kind: "engine"}, Resource{ID: "container:plex", Kind: "container"})
	relate(t, g,
		Relation{From: "engine:jupiter", To: "host:jupiter", Type: "runs_on"},
		Relation{From: "container:plex", To: "engine:jupiter", Type: "runs_on"},
	)
	g.Update(func(tx *Tx) error { tx.Remove("engine:jupiter"); return nil })
	_, doc := g.Snapshot(nil)
	if len(doc.Relations) != 0 || len(doc.Resources) != 2 {
		t.Fatalf("after remove: %d resources, %d relations", len(doc.Resources), len(doc.Relations))
	}
}

func drainOps(t *testing.T, w *Watcher) (uint64, []jsonpatch.Op) {
	t.Helper()
	seq, ops, err := w.Drain()
	if err != nil {
		t.Fatal(err)
	}
	return seq, ops
}

func TestWatcherDiffs(t *testing.T) {
	g := New()
	notify := make(chan struct{}, 1)
	w := g.Watch(nil, notify, 0)
	defer w.Close()

	upsert(t, g, Resource{ID: "host:jupiter", Kind: "host"}, Resource{ID: "container:plex", Kind: "container", State: "running"})
	select {
	case <-notify:
	default:
		t.Fatal("no notification")
	}
	seq, ops := drainOps(t, w)
	if seq != 1 || len(ops) != 2 || ops[0].Op != "add" || ops[0].Path != "/resources/container:plex" || ops[1].Path != "/resources/host:jupiter" {
		t.Fatalf("seq=%d ops=%+v", seq, ops)
	}

	relate(t, g, Relation{From: "container:plex", To: "host:jupiter", Type: "runs_on"})
	upsert(t, g, Resource{ID: "container:plex", Kind: "container", State: "exited", Health: HealthDown})
	seq, ops = drainOps(t, w)
	if seq != 3 || len(ops) != 2 {
		t.Fatalf("seq=%d ops=%+v", seq, ops)
	}
	if ops[0].Op != "add" || ops[0].Path != "/relations/container:plex runs_on host:jupiter" {
		t.Fatalf("relation op = %+v", ops[0])
	}
	if ops[1].Op != "replace" || ops[1].Value.(*Resource).State != "exited" {
		t.Fatalf("replace op = %+v", ops[1])
	}

	g.Update(func(tx *Tx) error { tx.Remove("host:jupiter"); return nil })
	_, ops = drainOps(t, w)
	want := []string{"remove /resources/host:jupiter", "remove /relations/container:plex runs_on host:jupiter"}
	if got := opStrings(ops); !reflect.DeepEqual(got, want) {
		t.Fatalf("ops = %v, want %v", got, want)
	}

	// Nothing pending: Drain returns the current seq and no ops.
	if seq, ops = drainOps(t, w); seq != 4 || len(ops) != 0 {
		t.Fatalf("idle drain seq=%d ops=%v", seq, ops)
	}
}

func TestFilteredWatcher(t *testing.T) {
	g := New()
	upsert(t, g, Resource{ID: "host:jupiter", Kind: "host"}, Resource{ID: "c1", Kind: "container"}, Resource{ID: "c2", Kind: "container"})
	relate(t, g, Relation{From: "c1", To: "host:jupiter", Type: "runs_on"}, Relation{From: "c1", To: "c2", Type: "depends_on"})

	w := g.Watch(KindFilter("container"), nil, 0)
	defer w.Close()
	_, doc := w.Snapshot()
	if len(doc.Resources) != 2 || len(doc.Relations) != 1 || doc.Relations["c1 depends_on c2"] == nil {
		t.Fatalf("filtered snapshot: %+v", doc)
	}

	// c2 stops being a container: it and its relation leave the view.
	upsert(t, g, Resource{ID: "c2", Kind: "vm"})
	_, ops := drainOps(t, w)
	if got, want := opStrings(ops), []string{"remove /resources/c2", "remove /relations/c1 depends_on c2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ops = %v, want %v", got, want)
	}
	// The host becomes a container: it and the existing runs_on relation enter.
	upsert(t, g, Resource{ID: "host:jupiter", Kind: "container"})
	_, ops = drainOps(t, w)
	if got, want := opStrings(ops), []string{"add /resources/host:jupiter", "add /relations/c1 runs_on host:jupiter"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ops = %v, want %v", got, want)
	}
	// Invisible changes produce nothing.
	upsert(t, g, Resource{ID: "c2", Kind: "vm", State: "running"})
	if _, ops = drainOps(t, w); len(ops) != 0 {
		t.Fatalf("invisible change leaked: %v", ops)
	}
}

func TestWatcherLag(t *testing.T) {
	g := New()
	notify := make(chan struct{}, 1)
	w := g.Watch(nil, notify, 3)
	defer w.Close()
	for i := range 5 {
		upsert(t, g, Resource{ID: RID(fmt.Sprintf("app:%d", i)), Kind: "app"})
	}
	if _, _, err := w.Drain(); !errors.Is(err, ErrLagged) {
		t.Fatalf("Drain = %v, want ErrLagged", err)
	}
	seq, doc := w.Snapshot()
	if seq != 5 || len(doc.Resources) != 5 {
		t.Fatalf("resync snapshot seq=%d n=%d", seq, len(doc.Resources))
	}
	upsert(t, g, Resource{ID: "app:new", Kind: "app"})
	if _, ops := drainOps(t, w); len(ops) != 1 {
		t.Fatalf("after resync ops = %v", ops)
	}
}

func TestClosedWatcherIsDetached(t *testing.T) {
	g := New()
	notify := make(chan struct{}, 1)
	w := g.Watch(nil, notify, 0)
	w.Close()
	upsert(t, g, Resource{ID: "a", Kind: "app"})
	select {
	case <-notify:
		t.Fatal("closed watcher was notified")
	default:
	}
	if len(g.watchers) != 0 {
		t.Fatal("watcher still registered")
	}
}

// TestPatchesReproduceGraph is the core contract: snapshot + every drained
// patch == a fresh snapshot, for random histories, filtered or not.
func TestPatchesReproduceGraph(t *testing.T) {
	filters := map[string]Filter{
		"all":       nil,
		"container": KindFilter("container"),
		"odd-state": func(r *Resource) bool { return r.State == "s1" || r.State == "s3" },
	}
	for name, f := range filters {
		t.Run(name, func(t *testing.T) {
			rng := rand.New(rand.NewPCG(1, 2))
			g := New()
			w := g.Watch(f, nil, 1<<20)
			defer w.Close()
			_, start := w.Snapshot()
			client := toGeneric(t, start)

			kinds := []Kind{"container", "host", "vm"}
			var pending []jsonpatch.Op
			for step := range 400 {
				_, err := g.Update(func(tx *Tx) error {
					for range 1 + rng.IntN(4) {
						id := RID(fmt.Sprintf("r%d", rng.IntN(12)))
						switch rng.IntN(5) {
						case 0, 1:
							tx.Upsert(Resource{ID: id, Kind: kinds[rng.IntN(3)], State: fmt.Sprintf("s%d", rng.IntN(4))})
						case 2:
							tx.Remove(id)
						case 3:
							to := RID(fmt.Sprintf("r%d", rng.IntN(12)))
							_ = tx.Relate(Relation{From: id, To: to, Type: RelType([]string{"runs_on", "depends_on"}[rng.IntN(2)]), Source: fmt.Sprint(rng.IntN(2))})
						case 4:
							tx.Unrelate(id, "runs_on", RID(fmt.Sprintf("r%d", rng.IntN(12))))
						}
					}
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
				// Drain now and then, and coalesce several drains before
				// applying, as the realtime layer does.
				if rng.IntN(3) == 0 || step == 399 {
					_, ops := drainOps(t, w)
					pending = append(pending, ops...)
				}
				if rng.IntN(4) == 0 || step == 399 {
					if err := jsonpatch.Apply(client, roundTrip(t, jsonpatch.Compact(pending))); err != nil {
						t.Fatalf("step %d: %v", step, err)
					}
					pending = nil
				}
			}
			_, want := g.Snapshot(f)
			if !reflect.DeepEqual(client, toGeneric(t, want)) {
				t.Fatalf("patched client state diverged\n got: %v\nwant: %v", client, toGeneric(t, want))
			}
		})
	}
}

func opStrings(ops []jsonpatch.Op) []string {
	out := make([]string, len(ops))
	for i, o := range ops {
		out[i] = o.Op + " " + o.Path
	}
	return out
}

func toGeneric(t *testing.T, v any) map[string]any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// roundTrip sends ops through JSON, as the wire does.
func roundTrip(t *testing.T, ops []jsonpatch.Op) []jsonpatch.Op {
	t.Helper()
	b, err := json.Marshal(ops)
	if err != nil {
		t.Fatal(err)
	}
	var out []jsonpatch.Op
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}
