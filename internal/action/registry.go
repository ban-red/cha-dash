package action

import (
	"encoding/json"
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
		case d.Resumes && d.Resume == nil:
			return fmt.Errorf("action: %s: %s resumes but has no Resume", source, d.ID)
		case d.Tier > T4:
			return fmt.Errorf("action: %s: %s has tier %d", source, d.ID, d.Tier)
		}
		if where, err := nestedSecret(d.Params); err != nil {
			return fmt.Errorf("action: %s: %s: params schema: %w", source, d.ID, err)
		} else if where != "" {
			// The audit log and plans redact top-level secret params only
			// (Schema.Secrets), so a secret any deeper would be kept.
			return fmt.Errorf("action: %s: %s has a secret param below the top level (%s); secret params must be top-level properties", source, d.ID, where)
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

// nestedSecret returns where s declares a write-only (secret) property
// anywhere but directly under the top-level object's properties, or "".
func nestedSecret(s Schema) (string, error) {
	if s == nil {
		return "", nil
	}
	raw, err := s.MarshalJSON()
	if err != nil {
		return "", err
	}
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return "", err
	}
	for k, v := range root {
		if k != "properties" {
			if p := writeOnlyIn(v, "/"+k); p != "" {
				return p, nil
			}
			continue
		}
		props, _ := v.(map[string]any)
		for name, prop := range props {
			pm, _ := prop.(map[string]any)
			for pk, pv := range pm {
				if pk == "writeOnly" {
					continue // a top-level secret: redacted
				}
				if p := writeOnlyIn(pv, "/properties/"+name+"/"+pk); p != "" {
					return p, nil
				}
			}
		}
	}
	return "", nil
}

// writeOnlyIn finds "writeOnly": true anywhere in v.
func writeOnlyIn(v any, path string) string {
	switch t := v.(type) {
	case map[string]any:
		if w, _ := t["writeOnly"].(bool); w {
			return path
		}
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			if p := writeOnlyIn(t[k], path+"/"+k); p != "" {
				return p
			}
		}
	case []any:
		for i, e := range t {
			if p := writeOnlyIn(e, fmt.Sprintf("%s/%d", path, i)); p != "" {
				return p
			}
		}
	}
	return ""
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
