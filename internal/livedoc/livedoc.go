// Package livedoc is a live JSON document for realtime topics other than
// the Home Graph. The document is one object whose members are either
// single values or keyed collections:
//
//	{"now": "2026-10-03T14:05:00Z", "incidents": {"plex-token-revoked": {…}}}
//
// Every change goes through Update, which applies a batch atomically and
// bumps the version once; a batch with no effective change doesn't. Watchers
// receive RFC 6902 patches against the snapshot they took.
//
// Values are stored encoded. That makes them immutable, lets equality be
// exact (reflect.DeepEqual would call every NaN a change) and lets snapshots
// be serialised without locks.
//
// A member that appears or disappears is added or removed whole (/now,
// /incidents/<key>). A member that changes, when it is a JSON object before
// and after, gets ops for just the top-level fields that changed
// (/series/<key>/value, /series/<key>/as_of), unless replacing it whole
// would be no bigger. A field's value is always replaced whole, arrays
// included, so patches never index into an array; jsonpatch.Compact relies
// on that.
package livedoc

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"

	"dash.cha.sh/internal/jsonpatch"
)

// Doc is safe for concurrent use.
type Doc struct {
	mu       sync.RWMutex
	version  uint64
	values   map[string]json.RawMessage
	colls    map[string]map[string]json.RawMessage
	watchers map[*Watcher]struct{}
	diff     differ // scratch space for Update
}

// New returns an empty document at version 0. The named collections are
// always present, as {} when empty, so a client can rely on the shape.
func New(collections ...string) *Doc {
	d := &Doc{values: map[string]json.RawMessage{}, colls: map[string]map[string]json.RawMessage{}, watchers: map[*Watcher]struct{}{}}
	for _, c := range collections {
		d.colls[c] = map[string]json.RawMessage{}
	}
	return d
}

// Version returns the current version.
func (d *Doc) Version() uint64 {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.version
}

// Snapshot returns the current document: each value as json.RawMessage and
// each collection as a map of them. The result is the caller's to keep.
func (d *Doc) Snapshot() (uint64, map[string]any) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.version, d.doc()
}

// doc copies the document out. The caller holds d.mu.
func (d *Doc) doc() map[string]any {
	out := make(map[string]any, len(d.values)+len(d.colls))
	for k, v := range d.values {
		out[k] = v
	}
	for k, c := range d.colls {
		out[k] = maps.Clone(c)
	}
	return out
}

// Get returns a value member's encoded JSON, or nil.
func (d *Doc) Get(member string) json.RawMessage {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.values[member]
}

// Item returns a collection item's encoded JSON, or nil.
func (d *Doc) Item(coll, key string) json.RawMessage {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.colls[coll][key]
}

// Tx stages changes inside Update.
type Tx struct {
	d      *Doc
	values map[string]json.RawMessage
	items  map[itemKey]json.RawMessage // nil: delete
	err    error
}

type itemKey struct{ coll, key string }

// Update runs fn under the write lock and applies its changes atomically.
// If fn (or any staged call) fails, nothing is applied. It returns the
// version after the update.
func (d *Doc) Update(fn func(tx *Tx) error) (uint64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	tx := &Tx{d: d, values: map[string]json.RawMessage{}, items: map[itemKey]json.RawMessage{}}
	if err := fn(tx); err != nil {
		return d.version, err
	}
	if tx.err != nil {
		return d.version, tx.err
	}

	// Patches are only built for watchers, but a change is a change either way.
	emit := len(d.watchers) > 0
	changed := false
	var ops []jsonpatch.Op
	for m, v := range tx.values {
		old, had := d.values[m]
		if had && bytes.Equal(old, v) {
			continue
		}
		changed = true
		if emit {
			ops = d.diff.memberOps(ops, jsonpatch.Pointer(m), old, v)
		}
	}
	for k, v := range tx.items {
		old, had := d.colls[k.coll][k.key]
		if (v == nil && !had) || (had && bytes.Equal(old, v)) {
			continue
		}
		changed = true
		if emit {
			ops = d.diff.memberOps(ops, jsonpatch.Pointer(k.coll, k.key), old, v)
		}
	}
	if !changed {
		return d.version, nil
	}

	d.version++
	for m, v := range tx.values {
		d.values[m] = v
	}
	for k, v := range tx.items {
		if v == nil {
			delete(d.colls[k.coll], k.key)
		} else {
			d.colls[k.coll][k.key] = v
		}
	}
	slices.SortFunc(ops, func(a, b jsonpatch.Op) int { return strings.Compare(a.Path, b.Path) })
	for w := range d.watchers {
		w.publish(d.version, ops)
	}
	return d.version, nil
}

func (tx *Tx) fail(err error) {
	if tx.err == nil {
		tx.err = err
	}
}

// Set stores a value member.
func (tx *Tx) Set(member string, v any) {
	if _, isColl := tx.d.colls[member]; isColl {
		tx.fail(fmt.Errorf("livedoc: %q is a collection", member))
		return
	}
	if member == "" {
		tx.fail(errors.New("livedoc: empty member name"))
		return
	}
	b, err := json.Marshal(v)
	if err != nil {
		tx.fail(fmt.Errorf("livedoc: %s: %w", member, err))
		return
	}
	tx.values[member] = b
}

// Put stores a collection item.
func (tx *Tx) Put(coll, key string, v any) {
	if _, ok := tx.d.colls[coll]; !ok {
		tx.fail(fmt.Errorf("livedoc: no collection %q", coll))
		return
	}
	if key == "" {
		tx.fail(fmt.Errorf("livedoc: empty key in %q", coll))
		return
	}
	b, err := json.Marshal(v)
	if err != nil {
		tx.fail(fmt.Errorf("livedoc: %s/%s: %w", coll, key, err))
		return
	}
	tx.items[itemKey{coll, key}] = b
}

// Item returns a collection item as this batch sees it: what an earlier call
// in the batch staged, else the document's value; nil when there's none. It
// lets one writer change another's item without losing a concurrent write:
// the read and the write happen under the same lock.
func (tx *Tx) Item(coll, key string) json.RawMessage {
	if v, staged := tx.items[itemKey{coll, key}]; staged {
		return v
	}
	return tx.d.colls[coll][key]
}

// Delete removes a collection item if present.
func (tx *Tx) Delete(coll, key string) {
	if _, ok := tx.d.colls[coll]; !ok {
		tx.fail(fmt.Errorf("livedoc: no collection %q", coll))
		return
	}
	tx.items[itemKey{coll, key}] = nil
}

// Clear removes every item of a collection, including ones staged earlier in
// this batch. Puts after it win, so Clear followed by Put for each item
// replaces a collection with only the differences on the wire.
func (tx *Tx) Clear(coll string) {
	c, ok := tx.d.colls[coll]
	if !ok {
		tx.fail(fmt.Errorf("livedoc: no collection %q", coll))
		return
	}
	for k := range c {
		tx.items[itemKey{coll, k}] = nil
	}
	for k := range tx.items {
		if k.coll == coll {
			tx.items[k] = nil
		}
	}
}
