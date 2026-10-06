package action

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"slices"

	"dash.cha.sh/internal/graph"
)

// GraphImpact is the blast radius of acting on origin, over the Home
// Graph's relations: what runs on it, is part of it, depends on it or
// stores on it (and what depends on those in turn), what it routes to,
// backs up or provides a network for, and what monitors it. It's the same
// walk as the Demo Home's (sim.BlastRadius), so plans and the demo's
// incidents agree.
func GraphImpact(g *graph.Graph, origin string) []Impact {
	if g == nil {
		return nil
	}
	_, doc := g.Snapshot(nil)
	type edge struct {
		to   string
		via  string
		deep bool // keep walking from the far end
	}
	adj := map[string][]edge{}
	for _, r := range doc.Relations {
		from, to, t := string(r.From), string(r.To), string(r.Type)
		switch t {
		case "runs_on", "part_of", "depends_on", "stores_on":
			adj[to] = append(adj[to], edge{from, t, true})
		case "backed_up_by", "routes_to":
			adj[to] = append(adj[to], edge{from, t, false})
		case "provides_network_for", "exposes":
			adj[from] = append(adj[from], edge{to, t, true})
		case "monitors":
			adj[from] = append(adj[from], edge{to, t, false})
		}
	}
	for k := range adj {
		// Map order is random; a stable walk gives a stable Via.
		slices.SortFunc(adj[k], func(a, b edge) int { return cmp.Or(cmp.Compare(a.to, b.to), cmp.Compare(a.via, b.via)) })
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

// ResolveInGraph resolves id in the Home Graph the way the engine does for
// a Def without Resolve: the resource must exist and be of one of d's
// Targets kinds. A source that wraps Defs (the integration runtime) uses
// it to keep that default.
func ResolveInGraph(g *graph.Graph, d *Def, id string) (Target, error) {
	if id == "" {
		return Target{}, ErrNotFound
	}
	return graphTarget(g, d, id)
}

// graphTarget resolves a target in the Home Graph.
func graphTarget(g *graph.Graph, d *Def, id string) (Target, error) {
	if g == nil {
		return Target{}, ErrNotFound
	}
	r, ok := g.Get(graph.RID(id))
	if !ok || (len(d.Targets) > 0 && !slices.Contains(d.Targets, string(r.Kind))) {
		return Target{}, ErrNotFound
	}
	name := r.Name
	if name == "" {
		name = id
	}
	// Kind, name and state: what an approval must find unchanged. Health
	// and attrs move with every sync and would expire plans for nothing.
	sum := sha256.Sum256([]byte(string(r.Kind) + "\x00" + r.Name + "\x00" + r.State))
	return Target{ID: id, Kind: string(r.Kind), Name: name, State: hex.EncodeToString(sum[:8])}, nil
}
