package demo

import (
	"encoding/json"
	"fmt"
	"time"

	"dash.cha.sh/internal/graph"
	"dash.cha.sh/sim"
)

// Attrs that carry the simulator's resource fields graph.Resource doesn't
// have yet. No simulator attr uses these keys.
const (
	AttrDesired      = "desired"       // desired state: running, stopped, …
	AttrHealthReason = "health.reason" // why the health is what it is
	AttrHealthSince  = "health.since"  // RFC 3339: when the health last changed
	AttrLinks        = "links"         // [{"title","url"}]: deep links
	AttrCaps         = "caps"          // ["media.library", …]: what widgets can bind to
)

// Resource maps a simulator resource onto a graph resource. Fields the graph
// doesn't model go into Attrs (see the Attr constants), and every attr is
// normalised to plain JSON values, so nothing is lost and equality is exact.
func Resource(r sim.Resource) (graph.Resource, error) {
	attrs := map[string]any{}
	if len(r.Attrs) > 0 {
		// Simulator attrs include time.Time and Go numeric types; the graph
		// wants JSON-like values, and JSON is what clients see anyway.
		b, err := json.Marshal(r.Attrs)
		if err != nil {
			return graph.Resource{}, fmt.Errorf("demo: %s attrs: %w", r.ID, err)
		}
		if err := json.Unmarshal(b, &attrs); err != nil {
			return graph.Resource{}, fmt.Errorf("demo: %s attrs: %w", r.ID, err)
		}
	}
	if r.Desired != "" {
		attrs[AttrDesired] = r.Desired
	}
	if r.Health.Reason != "" {
		attrs[AttrHealthReason] = r.Health.Reason
	}
	if !r.Health.Since.IsZero() {
		attrs[AttrHealthSince] = r.Health.Since.UTC().Format(time.RFC3339Nano)
	}
	if len(r.Links) > 0 {
		links := make([]any, len(r.Links))
		for i, l := range r.Links {
			links[i] = map[string]any{"title": l.Title, "url": l.URL}
		}
		attrs[AttrLinks] = links
	}
	if len(r.Caps) > 0 {
		caps := make([]any, len(r.Caps))
		for i, c := range r.Caps {
			caps[i] = c
		}
		attrs[AttrCaps] = caps
	}
	return graph.Resource{
		ID:     graph.RID(r.ID),
		Kind:   graph.Kind(r.Kind),
		Name:   r.Name,
		Source: r.Source,
		Labels: r.Labels,
		Attrs:  attrs,
		State:  r.State,
		// The simulator adds "repair" to the graph's five statuses: an
		// integration Cha Dash can't read, which isn't an outage.
		Health: graph.Health(r.Health.Status),
	}, nil
}

// Relation maps a simulator relation onto a graph relation.
func Relation(r sim.Relation) graph.Relation {
	return graph.Relation{From: graph.RID(r.From), To: graph.RID(r.To), Type: graph.RelType(r.Type)}
}

// LoadGraph makes g hold exactly the snapshot's resources and relations,
// removing anything else, in one batch. Demo mode owns the graph.
func LoadGraph(g *graph.Graph, st sim.State) error {
	want := make(map[graph.RID]graph.Resource, len(st.Resources))
	for _, r := range st.Resources {
		gr, err := Resource(r)
		if err != nil {
			return err
		}
		want[gr.ID] = gr
	}
	rels := make(map[string]graph.Relation, len(st.Relations))
	for _, r := range st.Relations {
		gr := Relation(r)
		rels[gr.Key()] = gr
	}
	_, cur := g.Snapshot(nil)
	_, err := g.Update(func(tx *graph.Tx) error {
		for k, rel := range cur.Relations {
			if _, ok := rels[k]; !ok {
				tx.Unrelate(rel.From, rel.Type, rel.To)
			}
		}
		for id := range cur.Resources {
			if _, ok := want[id]; !ok {
				tx.Remove(id)
			}
		}
		for _, r := range want {
			if err := tx.Upsert(r); err != nil {
				return err
			}
		}
		for _, rel := range rels {
			if err := tx.Relate(rel); err != nil {
				return err
			}
		}
		return nil
	})
	return err
}

// ApplyGraph applies one event to a graph batch. Events that aren't about
// resources or relations are ignored. The simulator orders events so that a
// relation never outlives or precedes its endpoints, which Relate requires.
func ApplyGraph(tx *graph.Tx, ev sim.Event) error {
	switch ev.Type {
	case sim.EventResourceUpsert:
		r, err := Resource(*ev.Resource)
		if err != nil {
			return err
		}
		return tx.Upsert(r)
	case sim.EventResourceRemove:
		tx.Remove(graph.RID(ev.ID))
	case sim.EventRelationUpsert:
		return tx.Relate(Relation(*ev.Relation))
	case sim.EventRelationRemove:
		r := Relation(*ev.Relation)
		tx.Unrelate(r.From, r.Type, r.To)
	}
	return nil
}
