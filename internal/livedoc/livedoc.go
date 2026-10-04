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
// be serialised without locks. Patches only touch a whole value (/now) or a
// whole collection item (/incidents/<key>), never a path inside another,
// which is what keeps jsonpatch.Compact sound.
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

	var ops []jsonpatch.Op
	for m, v := range tx.values {
		old, had := d.values[m]
		if had && bytes.Equal(old, v) {
			continue
		}
		op := jsonpatch.Replace
		if !had {
			op = jsonpatch.Add
		}
		ops = append(ops, jsonpatch.Op{Op: op, Path: jsonpatch.Pointer(m), Value: v})
	}
	for k, v := range tx.items {
		old, had := d.colls[k.coll][k.key]
		path := jsonpatch.Pointer(k.coll, k.key)
		switch {
		case v == nil && had:
			ops = append(ops, jsonpatch.Op{Op: jsonpatch.Remove, Path: path})
		case v == nil, had && bytes.Equal(old, v):
		case had:
			ops = append(ops, jsonpatch.Op{Op: jsonpatch.Replace, Path: path, Value: v})
		default:
			ops = append(ops, jsonpatch.Op{Op: jsonpatch.Add, Path: path, Value: v})
		}
	}
	if len(ops) == 0 {
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
