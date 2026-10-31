package topics

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"sync"
	"time"

	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/livedoc"
	"dash.cha.sh/internal/realtime"
)

// Cause is a cause hint: why a resource that isn't ok may not be, as a
// sentence and the resource it points at. It is a hint, never a verdict:
// it changes no health and opens no incident.
//
// The causes topic is {"causes": {"<resource>": [Cause…]}}, the first cause
// first, for the resources that have any; Path is left out there.
// GET /api/causes?resource=<id> answers one resource's causes with Path.
type Cause struct {
	Text     string `json:"text"`
	Resource string `json:"resource"`
	// At is when the cause changed state, if the cause is a change.
	At time.Time `json:"at,omitzero"`
	// Rule is what found it: the relation walked, or the event read.
	Rule string `json:"rule"`
	// Message and Args name Text's sentence for translation.
	Message string         `json:"message,omitempty"`
	Args    map[string]any `json:"args,omitempty"`
	// Path is the walk from the symptom to Resource, one relation per step.
	Path []Step `json:"path,omitempty"`
}

// Step is one relation on a cause's path, as the graph has it: the walk can
// follow a relation either way (a route routes_to what it needs, a check
// monitors it).
type Step struct {
	From string        `json:"from"`
	Type graph.RelType `json:"type"`
	To   string        `json:"to"`
}

// CauseSet holds the cause hints: the topic's document, and the paths the
// topic leaves out. Causes are computed, never stored.
type CauseSet struct {
	Doc *livedoc.Doc

	mu   sync.Mutex
	full map[string][]Cause
}

// NewCauseSet returns an empty set.
func NewCauseSet() *CauseSet {
	return &CauseSet{Doc: livedoc.New(Causes), full: map[string][]Cause{}}
}

// Replace makes all the causes there are, keyed by symptom: resources left
// out lose theirs.
func (c *CauseSet) Replace(all map[string][]Cause) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err := c.Doc.Update(func(tx *livedoc.Tx) error {
		for id := range c.full {
			if len(all[id]) == 0 {
				tx.Delete(Causes, id)
			}
		}
		for _, id := range slices.Sorted(maps.Keys(all)) {
			if cs := all[id]; len(cs) > 0 {
				tx.Put(Causes, id, withoutPaths(cs))
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	c.full = make(map[string][]Cause, len(all))
	for id, cs := range all {
		if len(cs) > 0 {
			c.full[id] = slices.Clone(cs)
		}
	}
	return nil
}

// Of returns a resource's causes with their paths, or nil.
func (c *CauseSet) Of(id string) []Cause {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.full[id])
}

func withoutPaths(cs []Cause) []Cause {
	out := slices.Clone(cs)
	for i := range out {
		out[i].Path = nil
	}
	return out
}

// CausesSource serves the causes topic per viewer: whole when whole(ctx)
// says so (viewer and up), and for household only the causes of resources
// its graph shows. A cause's own resource isn't filtered: household sees
// the sentence, which names it as a tile would.
func (s *Set) CausesSource(whole func(ctx context.Context) bool) realtime.Source {
	return realtime.ViewerDocSource{Doc: s.Causes.Doc, Topic: Causes, Coll: Causes,
		Visible: func(ctx context.Context) func(string, json.RawMessage) bool {
			if whole(ctx) {
				return func(string, json.RawMessage) bool { return true }
			}
			return func(id string, _ json.RawMessage) bool { return s.HouseholdSees(id) }
		}}
}

// HouseholdSees reports whether household's graph has the resource id.
func (s *Set) HouseholdSees(id string) bool {
	r, ok := s.Graph.Get(graph.RID(id))
	return ok && HouseholdFilter(&r)
}
