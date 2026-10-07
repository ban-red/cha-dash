package topics

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/realtime"
)

type wholeKey struct{}

// The graph topic for household: chadash-node's resources and their
// relations left out, addresses and fingerprints stripped, in the snapshot
// and in every diff after it; viewers get it whole.
func TestHouseholdGraphTopic(t *testing.T) {
	s := New(graph.New(), time.Now())
	if _, err := s.Graph.Update(func(tx *graph.Tx) error {
		for _, r := range []graph.Resource{
			{ID: "host:pve", Kind: "host", Name: "pve", Attrs: map[string]any{"ip": "192.0.2.10", "cpu": 8}},
			{ID: "node:n1", Kind: "node", Name: "local", Attrs: map[string]any{"fingerprint": "SHA256:fake", "verbs": []any{"container.logs"}}},
		} {
			if err := tx.Upsert(r); err != nil {
				return err
			}
		}
		return tx.Relate(graph.Relation{From: "node:n1", Type: "runs_on", To: "host:pve"})
	}); err != nil {
		t.Fatal(err)
	}
	src := s.GraphSource(func(ctx context.Context) bool { w, _ := ctx.Value(wholeKey{}).(bool); return w })
	open := func(whole bool) realtime.Feed {
		t.Helper()
		f, err := realtime.OpenWith(context.WithValue(t.Context(), wholeKey{}, whole), src, "graph", make(chan struct{}, 1))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(f.Close)
		return f
	}
	enc := func(v any) string { b, _ := json.Marshal(v); return string(b) }

	viewer, household := open(true), open(false)
	_, whole := viewer.Snapshot()
	if got := enc(whole); !strings.Contains(got, "node:n1") || !strings.Contains(got, "192.0.2.10") {
		t.Fatalf("viewer's snapshot isn't whole: %s", got)
	}
	_, doc := household.Snapshot()
	got := enc(doc)
	for _, hidden := range []string{"node:n1", "192.0.2.10", "SHA256:fake", "runs_on"} {
		if strings.Contains(got, hidden) {
			t.Fatalf("household's snapshot carries %q: %s", hidden, got)
		}
	}
	if !strings.Contains(got, `"cpu":8`) {
		t.Fatalf("household lost an ordinary attribute: %s", got)
	}
	// The source graph is untouched.
	if r, _ := s.Graph.Get("host:pve"); r.Attrs["ip"] != "192.0.2.10" {
		t.Fatal("projecting changed the graph")
	}

	// Diffs: an address changes, the node changes, a new node appears.
	if _, err := s.Graph.Update(func(tx *graph.Tx) error {
		if err := tx.Upsert(graph.Resource{ID: "host:pve", Kind: "host", Name: "pve", Attrs: map[string]any{"ip": "192.0.2.11", "mac": "02:00:00:00:00:01", "cpu": 16}}); err != nil {
			return err
		}
		if err := tx.Upsert(graph.Resource{ID: "node:n1", Kind: "node", Name: "local", State: "connected"}); err != nil {
			return err
		}
		return tx.Upsert(graph.Resource{ID: "node:n2", Kind: "node", Name: "second", Attrs: map[string]any{"address": "192.0.2.12"}})
	}); err != nil {
		t.Fatal(err)
	}
	_, ops, err := household.Drain()
	if err != nil {
		t.Fatal(err)
	}
	got = enc(ops)
	if len(ops) != 1 || ops[0].Path != "/resources/host:pve" || !strings.Contains(got, `"cpu":16`) {
		t.Fatalf("household's diff = %s", got)
	}
	for _, hidden := range []string{"node:", "192.0.2.", "02:00:00"} {
		if strings.Contains(got, hidden) {
			t.Fatalf("household's diff carries %q: %s", hidden, got)
		}
	}
	if _, ops, _ := viewer.Drain(); len(ops) != 3 || !strings.Contains(enc(ops), "192.0.2.12") {
		t.Fatalf("viewer's diff = %s", enc(ops))
	}
}
