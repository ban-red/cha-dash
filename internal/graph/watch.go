package graph

import (
	"errors"
	"slices"
	"strings"
	"sync"

	"dash.cha.sh/internal/jsonpatch"
)

// Filter selects the resources a watcher sees; nil means all. A relation is
// visible when both of its endpoints are, so a filtered view is always a
// closed subgraph with no dangling references.
type Filter func(*Resource) bool

// KindFilter selects resources of the given kinds.
func KindFilter(kinds ...Kind) Filter {
	return func(r *Resource) bool { return slices.Contains(kinds, r.Kind) }
}

// ErrLagged means the watcher's pending diffs overflowed and were dropped; the
// consumer must call Snapshot to resynchronise.
var ErrLagged = errors.New("graph: watcher lagged; resync required")

// DefaultMaxPending is the default bound on undelivered patch operations.
const DefaultMaxPending = 4096

// Watcher accumulates patches for one filtered view of the graph. The
// consumer waits on its notify channel, then calls Drain. Memory is bounded:
// past maxPending queued operations the watcher drops them and reports
// ErrLagged until Snapshot is called.
type Watcher struct {
	g      *Graph
	filter Filter
	notify chan<- struct{}
	max    int

	mu      sync.Mutex
	vis     map[RID]struct{}    // resources visible to this view
	vrel    map[string]struct{} // relations visible to this view
	pending []jsonpatch.Op
	seq     uint64 // graph version the view reflects once pending is applied
	lagged  bool
	closed  bool
}

// Watch registers a watcher. notify (which may be shared by several watchers)
// receives a non-blocking signal whenever operations are pending; it should
// have a buffer of 1. Call Snapshot to get the starting document.
func (g *Graph) Watch(f Filter, notify chan<- struct{}, maxPending int) *Watcher {
	if maxPending <= 0 {
		maxPending = DefaultMaxPending
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	_, vis, vrel := g.view(f)
	w := &Watcher{g: g, filter: f, notify: notify, max: maxPending, vis: vis, vrel: vrel, seq: g.version}
	g.watchers[w] = struct{}{}
	return w
}

// Snapshot returns the current document for this view and discards anything
// pending: the snapshot already includes it. It also clears a lag.
func (w *Watcher) Snapshot() (uint64, Doc) {
	w.g.mu.RLock()
	defer w.g.mu.RUnlock()
	w.mu.Lock()
	defer w.mu.Unlock()
	doc, vis, vrel := w.g.view(w.filter)
	w.vis, w.vrel = vis, vrel
	w.pending, w.lagged, w.seq = nil, false, w.g.version
	return w.seq, doc
}

// Drain returns and clears the pending operations, with the graph version they
// bring the view up to. It returns ErrLagged if operations were dropped.
func (w *Watcher) Drain() (uint64, []jsonpatch.Op, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.lagged {
		return 0, nil, ErrLagged
	}
	ops := w.pending
	w.pending = nil
	return w.seq, ops, nil
}

// Close unregisters the watcher.
func (w *Watcher) Close() {
	w.g.mu.Lock()
	defer w.g.mu.Unlock()
	delete(w.g.watchers, w)
	w.mu.Lock()
	w.closed = true
	w.pending = nil
	w.mu.Unlock()
}

func (w *Watcher) signal() {
	if w.notify == nil {
		return
	}
	select {
	case w.notify <- struct{}{}:
	default:
	}
}

func (w *Watcher) visible(r *Resource) bool { return w.filter == nil || w.filter(r) }

// publish is called by Update with g.mu held for writing, after the change
// has been applied to the graph.
func (w *Watcher) publish(g *Graph, rc map[RID]change, lc map[string]relChange) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || w.lagged {
		return // a lagged view is rebuilt from scratch by Snapshot
	}
	ops := w.diff(g, rc, lc)
	if len(ops) == 0 {
		return
	}
	if len(w.pending)+len(ops) > w.max {
		w.pending, w.lagged = nil, true
		w.signal()
		return
	}
	w.pending = append(w.pending, ops...)
	w.seq = g.version
	w.signal()
}

// diff turns one committed change set into patch operations for this view,
// updating the view's visible sets as it goes.
func (w *Watcher) diff(g *Graph, rc map[RID]change, lc map[string]relChange) []jsonpatch.Op {
	var resOps, relOps []jsonpatch.Op
	flipped := map[RID]struct{}{} // resources that became visible or invisible

	for id, c := range rc {
		_, was := w.vis[id]
		now := c.new != nil && w.visible(c.new)
		path := jsonpatch.Pointer("resources", string(id))
		switch {
		case !was && now:
			w.vis[id] = struct{}{}
			flipped[id] = struct{}{}
			resOps = append(resOps, jsonpatch.Op{Op: jsonpatch.Add, Path: path, Value: c.new})
		case was && now:
			resOps = append(resOps, jsonpatch.Op{Op: jsonpatch.Replace, Path: path, Value: c.new})
		case was && !now:
			delete(w.vis, id)
			flipped[id] = struct{}{}
			resOps = append(resOps, jsonpatch.Op{Op: jsonpatch.Remove, Path: path})
		}
	}

	// Relations to re-check: the ones that changed, plus, in a filtered view,
	// existing ones whose endpoint's visibility flipped. (Unfiltered, an
	// endpoint only flips by being added or removed, and Relate/Remove already
	// put those relations in lc.)
	candidates := map[string]struct{}{}
	for k := range lc {
		candidates[k] = struct{}{}
	}
	if w.filter != nil && len(flipped) > 0 {
		for k, rel := range g.relations {
			_, f1 := flipped[rel.From]
			_, f2 := flipped[rel.To]
			if f1 || f2 {
				candidates[k] = struct{}{}
			}
		}
	}
	for k := range candidates {
		_, was := w.vrel[k]
		cur := g.relations[k]
		now := false
		if cur != nil {
			_, a := w.vis[cur.From]
			_, b := w.vis[cur.To]
			now = a && b
		}
		path := jsonpatch.Pointer("relations", k)
		switch {
		case !was && now:
			w.vrel[k] = struct{}{}
			relOps = append(relOps, jsonpatch.Op{Op: jsonpatch.Add, Path: path, Value: cur})
		case was && now:
			if _, changed := lc[k]; changed {
				relOps = append(relOps, jsonpatch.Op{Op: jsonpatch.Replace, Path: path, Value: cur})
			}
		case was && !now:
			delete(w.vrel, k)
			relOps = append(relOps, jsonpatch.Op{Op: jsonpatch.Remove, Path: path})
		}
	}

	byPath := func(a, b jsonpatch.Op) int { return strings.Compare(a.Path, b.Path) }
	slices.SortFunc(resOps, byPath)
	slices.SortFunc(relOps, byPath)
	return append(resOps, relOps...)
}
