// Package jsonpatch is the subset of RFC 6902 the hub speaks on the wire:
// add, replace and remove on object members. Realtime diffs are patches
// against the snapshot a subscription received, so the browser can apply them
// with any JSON Patch implementation.
//
// The hub's patches touch whole members at fixed depths (a resource, a
// relation) and never a path inside another touched path. That invariant is
// what makes Compact sound.
package jsonpatch

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	Add     = "add"
	Replace = "replace"
	Remove  = "remove"
)

// Op is one patch operation.
type Op struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value,omitempty"`
}

// MarshalJSON always includes value for add and replace, even when it is nil
// (JSON null), and never for remove.
func (o Op) MarshalJSON() ([]byte, error) {
	if o.Op == Remove {
		return json.Marshal(struct {
			Op   string `json:"op"`
			Path string `json:"path"`
		}{o.Op, o.Path})
	}
	return json.Marshal(struct {
		Op    string `json:"op"`
		Path  string `json:"path"`
		Value any    `json:"value"`
	}{o.Op, o.Path, o.Value})
}

var escaper = strings.NewReplacer("~", "~0", "/", "~1")
var unescaper = strings.NewReplacer("~1", "/", "~0", "~")

// Pointer builds a JSON Pointer (RFC 6901) from raw segments.
func Pointer(segments ...string) string {
	var b strings.Builder
	for _, s := range segments {
		b.WriteByte('/')
		b.WriteString(escaper.Replace(s))
	}
	return b.String()
}

// Prefix returns ops with prefix (a JSON Pointer) prepended to each path.
func Prefix(prefix string, ops []Op) []Op {
	out := make([]Op, len(ops))
	for i, o := range ops {
		o.Path = prefix + o.Path
		out[i] = o
	}
	return out
}

// Compact folds a sequence of ops into the shortest equivalent one: per path,
// only the net effect survives (add then remove cancels out; remove then add
// becomes replace). Output keeps the order in which paths first appeared.
func Compact(ops []Op) []Op {
	type net struct {
		before, after bool // member existed before the sequence / exists after it
		value         any
	}
	states := make(map[string]*net, len(ops))
	var order []string
	for _, o := range ops {
		s, ok := states[o.Path]
		if !ok {
			s = &net{before: o.Op != Add}
			states[o.Path] = s
			order = append(order, o.Path)
		}
		if o.Op == Remove {
			s.after, s.value = false, nil
		} else {
			s.after, s.value = true, o.Value
		}
	}
	out := make([]Op, 0, len(order))
	for _, p := range order {
		s := states[p]
		switch {
		case s.before && s.after:
			out = append(out, Op{Op: Replace, Path: p, Value: s.value})
		case !s.before && s.after:
			out = append(out, Op{Op: Add, Path: p, Value: s.value})
		case s.before && !s.after:
			out = append(out, Op{Op: Remove, Path: p})
		}
	}
	return out
}

// Apply applies ops to a document decoded by encoding/json (nested
// map[string]any). It covers the subset above and exists for tests and tools.
func Apply(doc map[string]any, ops []Op) error {
	for _, o := range ops {
		segs, err := split(o.Path)
		if err != nil {
			return err
		}
		parent := doc
		for _, s := range segs[:len(segs)-1] {
			next, ok := parent[s].(map[string]any)
			if !ok {
				return fmt.Errorf("jsonpatch: %s: parent %q is not an object", o.Path, s)
			}
			parent = next
		}
		key := segs[len(segs)-1]
		_, exists := parent[key]
		switch o.Op {
		case Add, Replace:
			if o.Op == Replace && !exists {
				return fmt.Errorf("jsonpatch: replace %s: no such member", o.Path)
			}
			v, err := generic(o.Value)
			if err != nil {
				return err
			}
			parent[key] = v
		case Remove:
			if !exists {
				return fmt.Errorf("jsonpatch: remove %s: no such member", o.Path)
			}
			delete(parent, key)
		default:
			return fmt.Errorf("jsonpatch: unsupported op %q", o.Op)
		}
	}
	return nil
}

func split(p string) ([]string, error) {
	if !strings.HasPrefix(p, "/") {
		return nil, errors.New("jsonpatch: path must start with /")
	}
	segs := strings.Split(p[1:], "/")
	for i, s := range segs {
		segs[i] = unescaper.Replace(s)
	}
	return segs, nil
}

// generic converts a Go value to its encoding/json-decoded form.
func generic(v any) (any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out any
	return out, json.Unmarshal(b, &out)
}
