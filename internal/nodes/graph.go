package nodes

import (
	"time"

	"dash.cha.sh/internal/graph"
)

// Source is the graph source the hub's node resources carry.
const Source = "nodes"

// ResourceID is a node's resource in the Home Graph: node:<id>, kind
// "node", so it never collides with a Proxmox host (those are hosts).
func ResourceID(id string) graph.RID { return graph.RID("node:" + id) }

// publish puts an approved node in the graph, connected or not; a revoked
// one comes out, and a pending one stays out until it's approved.
func (h *Hub) publish(r Record, connected bool) {
	g := h.o.Graph
	if g == nil {
		return
	}
	if r.State != StateApproved {
		h.graphRemove(r.ID)
		return
	}
	state, health := "disconnected", graph.HealthDown
	if connected {
		state, health = "connected", graph.HealthOK
	}
	attrs := map[string]any{"fingerprint": r.Fingerprint, "enrolled_via": r.EnrolledVia, "node_state": r.State}
	if r.Version != "" {
		attrs["version"] = r.Version
	}
	if r.Address != "" {
		attrs["address"] = r.Address
	}
	if r.LastSeenAt != nil {
		attrs["last_seen"] = r.LastSeenAt.UTC().Format(time.RFC3339)
	}
	allowed := []string{}
	for _, v := range r.Verbs {
		if v.Allowed {
			allowed = append(allowed, v.Name)
		}
	}
	attrs["verbs"] = allowed
	_, err := g.Update(func(tx *graph.Tx) error {
		return tx.Upsert(graph.Resource{ID: ResourceID(r.ID), Kind: "node", Name: r.Name, Source: Source, State: state, Health: health, Attrs: attrs})
	})
	if err != nil {
		h.log.Error("publishing a node to the graph failed", "err", err)
	}
}

func (h *Hub) graphRemove(id string) {
	if h.o.Graph == nil {
		return
	}
	_, _ = h.o.Graph.Update(func(tx *graph.Tx) error {
		tx.Remove(ResourceID(id))
		return nil
	})
}
