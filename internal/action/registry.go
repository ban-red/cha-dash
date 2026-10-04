package action

import (
	"fmt"
	"regexp"
	"slices"
	"sync"
)

// idRE is the shape of an action ID: lowercase dot-separated segments, at
// least two (<integration>.<kind>.<verb>, or the Demo Home's shorter ones).
var idRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*(\.[a-z0-9][a-z0-9_-]*)+$`)

// Registry holds the actions every source contributed. It's safe for
// concurrent use; sources may come and go (an integration's instances do).
type Registry struct {
	mu     sync.RWMutex
	defs   map[string]*Def
	source map[string]string // action ID → source
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{defs: map[string]*Def{}, source: map[string]string{}}
}

// Register adds a source's actions. An ID can belong to one source only;
// registering a source again replaces its actions.
func (r *Registry) Register(source string, defs ...Def) error {
	if source == "" {
		return fmt.Errorf("action: a source needs a name")
	}
	seen := map[string]bool{}
	for i := range defs {
		d := &defs[i]
		switch {
		case !idRE.MatchString(d.ID):
			return fmt.Errorf("action: %s: invalid action ID %q", source, d.ID)
		case seen[d.ID]:
			return fmt.Errorf("action: %s: %s registered twice", source, d.ID)
		case d.Execute == nil:
			return fmt.Errorf("action: %s: %s has no Execute", source, d.ID)
		case d.Tier > T4:
			return fmt.Errorf("action: %s: %s has tier %d", source, d.ID, d.Tier)
		}
		seen[d.ID] = true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for id := range seen {
		if s, ok := r.source[id]; ok && s != source {
			return fmt.Errorf("action: %s: %s already belongs to %s", source, id, s)
		}
	}
	r.remove(source)
	for i := range defs {
		d := defs[i] // a copy the caller can't change afterwards
		r.defs[d.ID] = &d
		r.source[d.ID] = source
	}
	return nil
}

// Unregister removes a source's actions.
func (r *Registry) Unregister(source string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.remove(source)
}

func (r *Registry) remove(source string) {
	for id, s := range r.source {
		if s == source {
			delete(r.defs, id)
			delete(r.source, id)
		}
	}
}

// Get returns an action by ID.
func (r *Registry) Get(id string) (*Def, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	d, ok := r.defs[id]
	return d, ok
}

// IDs lists every registered action, sorted.
func (r *Registry) IDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := make([]string, 0, len(r.defs))
	for id := range r.defs {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}
