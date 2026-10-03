package sim

import (
	"cmp"
	"slices"
)

// BlastRadius walks the relations from origin and returns every resource
// that would be affected if origin went away, sorted by resource ID. Depth
// is the shortest distance from origin.
//
// The walk follows 03-architecture → "What the graph enables":
//   - Dependents, transitively: anything that runs_on, is part_of,
//     depends_on or stores_on an affected resource is affected too.
//   - Dependents, one step only: a guest backed_up_by an affected job loses
//     its backups, and a route that routes_to an affected app breaks, but
//     neither takes anything else down with it.
//   - Providers, forwards: if the router is affected, everything it
//     provides_network_for is too; if Frigate is, the cameras it exposes are.
//   - Monitors, one step forwards: a broken integration makes the resources
//     it reads stale.
func BlastRadius(rels []Relation, origin string) []Impact {
	type edge struct {
		to   string
		via  RelType
		deep bool // keep walking from the far end
	}
	adj := map[string][]edge{}
	for _, r := range rels {
		switch r.Type {
		case RelRunsOn, RelPartOf, RelDependsOn, RelStoresOn:
			adj[r.To] = append(adj[r.To], edge{r.From, r.Type, true})
		case RelBackedUpBy, RelRoutesTo:
			adj[r.To] = append(adj[r.To], edge{r.From, r.Type, false})
		case RelProvidesNetworkFor, RelExposes:
			adj[r.From] = append(adj[r.From], edge{r.To, r.Type, true})
		case RelMonitors:
			adj[r.From] = append(adj[r.From], edge{r.To, r.Type, false})
		}
	}
	found := map[string]*Impact{}
	expanded := map[string]bool{origin: true}
	frontier := []string{origin}
	for depth := 1; len(frontier) > 0; depth++ {
		var next []string
		for _, id := range frontier {
			for _, e := range adj[id] {
				if e.to == origin {
					continue
				}
				if found[e.to] == nil {
					found[e.to] = &Impact{Resource: e.to, Via: e.via, Depth: depth}
				}
				if e.deep && !expanded[e.to] {
					expanded[e.to] = true
					if f := found[e.to]; f.Depth == depth {
						f.Via = e.via // prefer the edge that carries the outage on
					}
					next = append(next, e.to)
				}
			}
		}
		slices.Sort(next)
		frontier = slices.Compact(next)
	}
	out := make([]Impact, 0, len(found))
	for _, im := range found {
		out = append(out, *im)
	}
	slices.SortFunc(out, func(a, b Impact) int { return cmp.Compare(a.Resource, b.Resource) })
	return out
}
