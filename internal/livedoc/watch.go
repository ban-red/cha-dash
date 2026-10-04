package livedoc

import (
	"errors"
	"sync"

	"dash.cha.sh/internal/jsonpatch"
)

// ErrLagged means a watcher's pending patches overflowed and were dropped;
// the consumer must call Snapshot to resynchronise.
var ErrLagged = errors.New("livedoc: watcher lagged; resync required")

// DefaultMaxPending is the default bound on undelivered patch operations.
const DefaultMaxPending = 4096

// Watcher accumulates patches for one consumer. It mirrors graph.Watcher:
// wait on notify, then Drain; past maxPending operations it drops them and
// reports ErrLagged until Snapshot is called.
type Watcher struct {
	d      *Doc
	notify chan<- struct{}
	max    int

	mu      sync.Mutex
	pending []jsonpatch.Op
	seq     uint64
	lagged  bool
	closed  bool
}

// Watch registers a watcher. notify (which may be shared) receives a
// non-blocking signal whenever operations are pending; give it a buffer of 1.
// Call Snapshot for the starting document.
func (d *Doc) Watch(notify chan<- struct{}, maxPending int) *Watcher {
	if maxPending <= 0 {
		maxPending = DefaultMaxPending
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	w := &Watcher{d: d, notify: notify, max: maxPending, seq: d.version}
	d.watchers[w] = struct{}{}
	return w
}

// Snapshot returns the current document and discards anything pending, which
// it already includes. It also clears a lag.
func (w *Watcher) Snapshot() (uint64, map[string]any) {
	w.d.mu.RLock()
	defer w.d.mu.RUnlock()
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pending, w.lagged, w.seq = nil, false, w.d.version
	return w.seq, w.d.doc()
}

// Drain returns and clears the pending operations, with the version they
// bring the consumer up to.
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
	w.d.mu.Lock()
	defer w.d.mu.Unlock()
	delete(w.d.watchers, w)
	w.mu.Lock()
	w.closed, w.pending = true, nil
	w.mu.Unlock()
}

// publish is called by Update with d.mu held for writing. ops is shared
// between watchers and never modified.
func (w *Watcher) publish(version uint64, ops []jsonpatch.Op) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || w.lagged {
		return
	}
	if len(w.pending)+len(ops) > w.max {
		w.pending, w.lagged = nil, true
	} else {
		w.pending = append(w.pending, ops...)
		w.seq = version
	}
	select {
	case w.notify <- struct{}{}:
	default:
	}
}
