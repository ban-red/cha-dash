package topics

import (
	"context"
	"maps"
	"slices"

	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/realtime"
)

// The household's view of the Home Graph. Boards render from the graph, so
// household keeps it, minus what only the people running the hub need:
//
//   - chadash-node's resources (kind node: its key fingerprint, address,
//     enrolment and the verbs its policy allows) are left out, and with
//     them their relations;
//   - address-like attributes (IP and MAC addresses, key fingerprints) are
//     stripped from every other resource. The web app never shows them on
//     a board; they help someone map the network, not read a tile.
//
// Kinds, names, states, health, labels and the other attributes stay, so
// every widget renders as it does for a viewer.

// HouseholdHiddenKinds are the resource kinds household never sees.
var HouseholdHiddenKinds = []graph.Kind{"node"}

// householdHiddenAttrs are the attributes stripped for household.
var householdHiddenAttrs = []string{
	"address", "addresses", "ip", "ips", "ipv4", "ipv6", "ip_address", "ip_addresses",
	"mac", "macs", "mac_address", "fingerprint", "public_key", "verbs",
}

// HouseholdFilter keeps every resource household may see.
func HouseholdFilter(r *graph.Resource) bool {
	return !slices.Contains(HouseholdHiddenKinds, r.Kind)
}

// HouseholdResource is household's view of r: r itself when it has none of
// the stripped attributes, else a copy without them.
func HouseholdResource(r *graph.Resource) *graph.Resource {
	hit := false
	for _, k := range householdHiddenAttrs {
		if _, ok := r.Attrs[k]; ok {
			hit = true
			break
		}
	}
	if !hit {
		return r
	}
	out := *r
	out.Attrs = maps.Clone(r.Attrs)
	for _, k := range householdHiddenAttrs {
		delete(out.Attrs, k)
	}
	if len(out.Attrs) == 0 {
		out.Attrs = nil
	}
	return &out
}

// HouseholdGraph is household's view of a graph snapshot: hidden kinds and
// their relations out, attributes stripped. doc isn't changed.
func HouseholdGraph(doc graph.Doc) graph.Doc {
	out := graph.Doc{Resources: make(map[graph.RID]*graph.Resource, len(doc.Resources)), Relations: make(map[string]*graph.Relation, len(doc.Relations))}
	for id, r := range doc.Resources {
		if HouseholdFilter(r) {
			out.Resources[id] = HouseholdResource(r)
		}
	}
	for k, rel := range doc.Relations {
		if _, ok := out.Resources[rel.From]; !ok {
			continue
		}
		if _, ok := out.Resources[rel.To]; ok {
			out.Relations[k] = rel
		}
	}
	return out
}

// GraphSource serves the graph topic per viewer: whole when whole(ctx)
// says so (viewer and up), household's view otherwise, in snapshots and
// diffs alike.
func (s *Set) GraphSource(whole func(ctx context.Context) bool) realtime.Source {
	return realtime.GraphSource{Graph: s.Graph, View: func(ctx context.Context) realtime.GraphView {
		if whole(ctx) {
			return realtime.GraphView{}
		}
		return realtime.GraphView{Filter: HouseholdFilter, Project: HouseholdResource}
	}}
}
