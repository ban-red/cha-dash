package correlate

import (
	"cmp"
	"slices"

	"dash.cha.sh/internal/graph"
)

// Readers' helpers: one of each thing. Every resource the sources publish
// stays in the graph; an identity seen by several sources has one primary,
// and each other member is same_as it. So:
//
//   - one of each: the resources with no outgoing same_as (Distinct);
//   - the thing a resource is: its same_as target, or itself (Canonical);
//   - everything the sources say about it: the primary plus whatever is
//     same_as it (Members), each with its own source.
//
// The web app does the same in web/src/model/graph.ts.

// Canonical returns the resource that stands for id's identity.
func Canonical(doc graph.Doc, id graph.RID) graph.RID {
	for _, rel := range doc.Relations {
		if rel.From == id && rel.Type == SameAs {
			return rel.To
		}
	}
	return id
}

// Members returns id's identity: the canonical resource first, then the
// others by ID.
func Members(doc graph.Doc, id graph.RID) []graph.RID {
	p := Canonical(doc, id)
	var rest []graph.RID
	for _, rel := range doc.Relations {
		if rel.To == p && rel.Type == SameAs {
			rest = append(rest, rel.From)
		}
	}
	slices.Sort(rest)
	return append([]graph.RID{p}, rest...)
}

// Distinct returns one resource per identity, of the given kinds (all when
// none are given), by ID.
func Distinct(doc graph.Doc, kinds ...graph.Kind) []*graph.Resource {
	alias := map[graph.RID]bool{}
	for _, rel := range doc.Relations {
		if rel.Type == SameAs {
			alias[rel.From] = true
		}
	}
	var out []*graph.Resource
	for id, r := range doc.Resources {
		if !alias[id] && (len(kinds) == 0 || slices.Contains(kinds, r.Kind)) {
			out = append(out, r)
		}
	}
	slices.SortFunc(out, func(a, b *graph.Resource) int { return cmp.Compare(a.ID, b.ID) })
	return out
}
