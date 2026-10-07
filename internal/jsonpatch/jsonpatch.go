// Package jsonpatch is the subset of RFC 6902 the hub speaks on the wire:
// add, replace and remove on object members, and add at the end of an array
// (path …/-). Realtime diffs are patches
// against the snapshot a subscription received, so the browser can apply them
// with any JSON Patch implementation.
//
// The hub's patches address object members: whole members (a resource, an
// incident) or, for live documents, the top-level fields of one
// (/series/<key>/value). They never index into an array: an array is
// replaced whole, or grows by appends (/tasks/<id>/log/-), which add an
// element at its end. Compact relies on that.
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

// Compact folds a sequence of ops into a shorter equivalent one:
//   - per path, only the net effect survives (add then remove cancels out;
//     remove then add becomes replace);
//   - an op on a member makes earlier ops inside it moot (replacing
//     /series/k discards an earlier replace of /series/k/value), and ops
//     inside it that come later stay after it.
//
// Output keeps the order in which paths first appeared, or reappeared after
// being made moot, which keeps every op inside a member after the op on the
// member itself.
//
// An append (add at …/-) is never folded: each one is kept, in order,
// until an op on the array or a member above it makes it moot.
//
// Paths must otherwise address object members: Compact doesn't understand
// array indices, which shift, so it must never see any other op inside an
// array.
func Compact(ops []Op) []Op {
	type net struct {
		path          string
		before, after bool // member existed before the sequence / exists after it
		value         any
		moot          bool
		append        bool // an append, kept as is
	}
	live := make(map[string]*net, len(ops))
	below := map[string][]*net{} // a path → the nets created inside it
	order := make([]*net, 0, len(ops))
	for _, o := range ops {
		for _, s := range below[o.Path] {
			if !s.moot {
				s.moot = true
				delete(live, s.path)
			}
		}
		delete(below, o.Path)
		if IsAppend(o) {
			s := &net{path: o.Path, after: true, value: o.Value, append: true}
			order = append(order, s)
			for p := parentOf(o.Path); p != ""; p = parentOf(p) {
				below[p] = append(below[p], s)
			}
			continue
		}
		s, ok := live[o.Path]
		if !ok {
			s = &net{path: o.Path, before: o.Op != Add}
			live[o.Path] = s
			order = append(order, s)
			for p := parentOf(o.Path); p != ""; p = parentOf(p) {
				below[p] = append(below[p], s)
			}
		}
		if o.Op == Remove {
			s.after, s.value = false, nil
		} else {
			s.after, s.value = true, o.Value
		}
	}
	out := make([]Op, 0, len(order))
	for _, s := range order {
		switch {
		case s.moot:
		case s.append:
			out = append(out, Op{Op: Add, Path: s.path, Value: s.value})
		case s.before && s.after:
			out = append(out, Op{Op: Replace, Path: s.path, Value: s.value})
		case !s.before && s.after:
			out = append(out, Op{Op: Add, Path: s.path, Value: s.value})
		case s.before && !s.after:
			out = append(out, Op{Op: Remove, Path: s.path})
		}
	}
	return out
}

// IsAppend reports whether o adds an element at the end of an array.
func IsAppend(o Op) bool { return o.Op == Add && strings.HasSuffix(o.Path, "/-") }

// parentOf returns the pointer of p's parent: "/a" for "/a/b", "" for "/a".
func parentOf(p string) string {
	if i := strings.LastIndexByte(p, '/'); i > 0 {
		return p[:i]
	}
	return ""
}

// Apply applies ops to a document decoded by encoding/json (nested
// map[string]any). It covers the subset above and exists for tests and tools.
func Apply(doc map[string]any, ops []Op) error {
	for _, o := range ops {
		segs, err := split(o.Path)
		if err != nil {
			return err
		}
		if IsAppend(o) {
			done, err := appendAt(doc, segs[:len(segs)-1], o.Value)
			if err != nil {
				return err
			}
			if done {
				continue
			}
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

// appendAt appends v to the array at segs (an append's path without its
// final "-"). done is false when there's no array there: "-" is then an
// object member's name.
func appendAt(doc map[string]any, segs []string, v any) (done bool, _ error) {
	if len(segs) == 0 {
		return false, nil
	}
	parent := doc
	for _, s := range segs[:len(segs)-1] {
		next, ok := parent[s].(map[string]any)
		if !ok {
			return false, nil
		}
		parent = next
	}
	key := segs[len(segs)-1]
	arr, ok := parent[key].([]any)
	if !ok {
		return false, nil
	}
	g, err := generic(v)
	if err != nil {
		return true, err
	}
	parent[key] = append(arr, g)
	return true, nil
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
