// Package graph is the Home Graph v0: an in-memory set of resources and the
// relations between them, with a monotonic version and watchers that receive
// diffs.
//
// Every change goes through Update, which applies a batch atomically and bumps
// the version once. Stored values are immutable: Upsert copies its input and
// readers get shared, read-only values, so snapshots and diffs can be
// serialised without holding locks.
//
// Invariant: a relation's endpoints always exist. Relate rejects unknown
// endpoints and Remove deletes a resource's relations with it.
package graph

import (
	"errors"
	"fmt"
	"maps"
	"reflect"
	"strings"
	"sync"
	"unicode"
)

// RID is a stable, namespaced resource ID, e.g. "docker:jupiter/c/4f2a", "pve:mars/lxc/101".
type RID string

// Kind is a resource type: host, vm, lxc, engine, container, app, …
type Kind string

// RelType is a relation type: runs_on, part_of, exposes, depends_on, …
type RelType string

// Health is the normalised health every widget understands.
type Health string

const (
	HealthOK          Health = "ok"
	HealthAttention   Health = "attention"
	HealthDown        Health = "down"
	HealthUnknown     Health = "unknown"
	HealthMaintenance Health = "maintenance"
)

// Resource is one thing in the home. Attrs should hold JSON-like values
// (maps, slices, strings, numbers, bools); maps and slices are deep-copied on
// Upsert, other reference types are shared and must not be mutated.
type Resource struct {
	ID     RID               `json:"id"`
	Kind   Kind              `json:"kind"`
	Name   string            `json:"name,omitempty"`
	Source string            `json:"source,omitempty"` // integration instance that asserted it
	Labels map[string]string `json:"labels,omitempty"`
	Attrs  map[string]any    `json:"attrs,omitempty"`
	State  string            `json:"state,omitempty"` // actual state: running, stopped, …
	Health Health            `json:"health,omitempty"`
}

// Relation is a typed edge. From, Type and To identify it; Source is provenance.
type Relation struct {
	From   RID     `json:"from"`
	To     RID     `json:"to"`
	Type   RelType `json:"type"`
	Source string  `json:"source,omitempty"`
}

// Key identifies a relation. IDs and types can't contain spaces.
func (r Relation) Key() string { return string(r.From) + " " + string(r.Type) + " " + string(r.To) }

// Doc is the snapshot document a watcher or subscriber sees. Diffs are JSON
// patches against it: /resources/<id> and /relations/<key>.
type Doc struct {
	Resources map[RID]*Resource    `json:"resources"`
	Relations map[string]*Relation `json:"relations"`
}

// Graph is safe for concurrent use.
type Graph struct {
	mu        sync.RWMutex
	version   uint64
	resources map[RID]*Resource
	relations map[string]*Relation
	watchers  map[*Watcher]struct{}
}

// New returns an empty graph at version 0.
func New() *Graph {
	return &Graph{
		resources: map[RID]*Resource{},
		relations: map[string]*Relation{},
		watchers:  map[*Watcher]struct{}{},
	}
}

// Version returns the current version. It increases by one per effective Update.
func (g *Graph) Version() uint64 {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.version
}

// Get returns a resource. The returned value's maps are shared: don't modify them.
func (g *Graph) Get(id RID) (Resource, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	r, ok := g.resources[id]
	if !ok {
		return Resource{}, false
	}
	return *r, true
}

// Snapshot returns the current document, filtered (nil means everything).
func (g *Graph) Snapshot(f Filter) (uint64, Doc) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	doc, _, _ := g.view(f)
	return g.version, doc
}

// view computes the filtered document plus the visible ID and relation sets.
// The caller holds g.mu.
func (g *Graph) view(f Filter) (Doc, map[RID]struct{}, map[string]struct{}) {
	doc := Doc{Resources: map[RID]*Resource{}, Relations: map[string]*Relation{}}
	vis := map[RID]struct{}{}
	vrel := map[string]struct{}{}
	for id, r := range g.resources {
		if f == nil || f(r) {
			doc.Resources[id] = r
			vis[id] = struct{}{}
		}
	}
	for k, rel := range g.relations {
		if _, ok := vis[rel.From]; !ok {
			continue
		}
		if _, ok := vis[rel.To]; !ok {
			continue
		}
		doc.Relations[k] = rel
		vrel[k] = struct{}{}
	}
	return doc, vis, vrel
}

// Tx stages changes inside Update.
type Tx struct {
	g   *Graph
	res map[RID]*Resource    // nil value: removed
	rel map[string]*Relation // nil value: removed
}

// Update runs fn under the graph's write lock and applies its changes
// atomically. If fn returns an error nothing is applied. fn must be quick and
// must not call back into the graph. It returns the version after the update;
// a batch with no effective change doesn't bump it.
func (g *Graph) Update(fn func(tx *Tx) error) (uint64, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	tx := &Tx{g: g, res: map[RID]*Resource{}, rel: map[string]*Relation{}}
	if err := fn(tx); err != nil {
		return g.version, err
	}

	rc := map[RID]change{}
	for id, n := range tx.res {
		o := g.resources[id]
		if o == nil && n == nil || o != nil && n != nil && reflect.DeepEqual(*o, *n) {
			continue
		}
		rc[id] = change{old: o, new: n}
	}
	lc := map[string]relChange{}
	for k, n := range tx.rel {
		o := g.relations[k]
		if o == nil && n == nil || o != nil && n != nil && *o == *n {
			continue
		}
		lc[k] = relChange{old: o, new: n}
	}
	if len(rc) == 0 && len(lc) == 0 {
		return g.version, nil
	}

	g.version++
	for id, c := range rc {
		if c.new == nil {
			delete(g.resources, id)
		} else {
			g.resources[id] = c.new
		}
	}
	for k, c := range lc {
		if c.new == nil {
			delete(g.relations, k)
		} else {
			g.relations[k] = c.new
		}
	}
	for w := range g.watchers {
		w.publish(g, rc, lc)
	}
	return g.version, nil
}

type change struct{ old, new *Resource }
type relChange struct{ old, new *Relation }

// Get reads a resource as the transaction currently sees it.
func (tx *Tx) Get(id RID) (Resource, bool) {
	r := tx.lookup(id)
	if r == nil {
		return Resource{}, false
	}
	return *r, true
}

func (tx *Tx) lookup(id RID) *Resource {
	if r, staged := tx.res[id]; staged {
		return r
	}
	return tx.g.resources[id]
}

// Upsert creates or replaces a resource.
func (tx *Tx) Upsert(r Resource) error {
	if err := checkToken("resource id", string(r.ID)); err != nil {
		return err
	}
	if r.Kind == "" {
		return fmt.Errorf("graph: resource %s has no kind", r.ID)
	}
	tx.res[r.ID] = clone(r)
	return nil
}

// Remove deletes a resource and every relation touching it. Removing a
// missing resource is a no-op.
func (tx *Tx) Remove(id RID) {
	tx.res[id] = nil
	for k, rel := range tx.g.relations {
		if rel.From == id || rel.To == id {
			if _, staged := tx.rel[k]; !staged {
				tx.rel[k] = nil
			}
		}
	}
	for k, rel := range tx.rel {
		if rel != nil && (rel.From == id || rel.To == id) {
			tx.rel[k] = nil
		}
	}
}

// Relate adds or updates a relation. Both endpoints must exist.
func (tx *Tx) Relate(rel Relation) error {
	if err := checkToken("relation type", string(rel.Type)); err != nil {
		return err
	}
	for _, id := range []RID{rel.From, rel.To} {
		if tx.lookup(id) == nil {
			return fmt.Errorf("graph: relation %s: %w: %s", rel.Key(), ErrNoEndpoint, id)
		}
	}
	tx.rel[rel.Key()] = &rel
	return nil
}

// Unrelate removes a relation if present.
func (tx *Tx) Unrelate(from RID, typ RelType, to RID) {
	tx.rel[Relation{From: from, Type: typ, To: to}.Key()] = nil
}

// ErrNoEndpoint is returned by Relate when an endpoint doesn't exist.
var ErrNoEndpoint = errors.New("endpoint does not exist")

func checkToken(what, s string) error {
	if s == "" || len(s) > 512 {
		return fmt.Errorf("graph: %s must be 1–512 bytes", what)
	}
	if strings.IndexFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return fmt.Errorf("graph: %s %q contains whitespace or control characters", what, s)
	}
	return nil
}

// clone deep-copies the caller's maps so later mutation can't reach the graph.
// Empty maps become nil so equality checks don't see spurious changes.
func clone(r Resource) *Resource {
	if len(r.Labels) == 0 {
		r.Labels = nil
	} else {
		r.Labels = maps.Clone(r.Labels)
	}
	if len(r.Attrs) == 0 {
		r.Attrs = nil
	} else {
		r.Attrs = cloneValue(r.Attrs).(map[string]any)
	}
	return &r
}

func cloneValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(t))
		for k, x := range t {
			m[k] = cloneValue(x)
		}
		return m
	case []any:
		s := make([]any, len(t))
		for i, x := range t {
			s[i] = cloneValue(x)
		}
		return s
	default:
		return v
	}
}
