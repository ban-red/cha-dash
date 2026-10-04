package livedoc

import (
	"bytes"
	"encoding/json"

	"dash.cha.sh/internal/jsonpatch"
)

// opBytes is roughly what an op costs on the wire besides its path and
// value: {"op":"replace","path":"","value":},
const opBytes = len(`{"op":"replace","path":"","value":},`)

// differ holds scratch space for memberOps, reused across calls. Doc keeps
// one and uses it under its write lock.
type differ struct {
	old, new []member
	matched  []bool
}

// memberOps appends the ops that turn a member's old encoding into new, at
// path. nil means absent, and old and new differ. A member that appears or
// disappears is added or removed whole. One that changes gets an op for each
// top-level field that changed, when it is an object before and after and
// those ops are smaller than replacing it whole. Field values are replaced
// whole, arrays included. Op values share memory with new.
func (d *differ) memberOps(ops []jsonpatch.Op, path string, old, new json.RawMessage) []jsonpatch.Op {
	switch {
	case new == nil:
		return append(ops, jsonpatch.Op{Op: jsonpatch.Remove, Path: path})
	case old == nil:
		return append(ops, jsonpatch.Op{Op: jsonpatch.Add, Path: path, Value: new})
	}
	whole := jsonpatch.Op{Op: jsonpatch.Replace, Path: path, Value: new}
	var ok bool
	if d.old, ok = members(old, d.old[:0]); !ok {
		return append(ops, whole)
	}
	if d.new, ok = members(new, d.new[:0]); !ok {
		return append(ops, whole)
	}
	om, nm := d.old, d.new
	d.matched = append(d.matched[:0], make([]bool, len(om))...)
	matched := d.matched

	start, size, budget := len(ops), 0, opBytes+len(path)+len(new)
	emit := func(op string, key []byte, v json.RawMessage) bool {
		o := jsonpatch.Op{Op: op, Path: path + jsonpatch.Pointer(string(key))}
		if op != jsonpatch.Remove {
			o.Value = v
		}
		ops = append(ops, o)
		size += opBytes + len(o.Path) + len(v)
		return size < budget
	}
	next := 0 // fields mostly keep their order, so look there first
	for _, n := range nm {
		i := find(om, n.key, next)
		switch {
		case i < 0:
			if !emit(jsonpatch.Add, n.key, n.val) {
				return append(ops[:start], whole)
			}
			continue
		case matched[i]: // a duplicate key: json.Marshal never writes one
			return append(ops[:start], whole)
		}
		matched[i], next = true, i+1
		if !bytes.Equal(om[i].val, n.val) && !emit(jsonpatch.Replace, n.key, n.val) {
			return append(ops[:start], whole)
		}
	}
	for i, o := range om {
		if matched[i] {
			continue
		}
		if find(nm, o.key, 0) >= 0 || !emit(jsonpatch.Remove, o.key, nil) {
			return append(ops[:start], whole) // a duplicate key, or too big
		}
	}
	return ops
}

// find returns the index of the member with key in ms, looking from start
// onwards and then wrapping around, or -1.
func find(ms []member, key []byte, start int) int {
	for k := range ms {
		i := (start + k) % len(ms)
		if bytes.Equal(ms[i].key, key) {
			return i
		}
	}
	return -1
}

// member is one member of an encoded JSON object.
type member struct {
	key []byte          // decoded, without quotes
	val json.RawMessage // encoded, a subslice of the object's encoding
}

// members appends the members of an encoded JSON object to ms, without
// copying values. b must be valid JSON, as json.Marshal writes it. ok is
// false for anything but an object, and for anything the scan doesn't
// follow: the caller then replaces the member whole, which is always correct.
func members(b []byte, ms []member) (_ []member, ok bool) {
	i := skipSpace(b, 0)
	if i >= len(b) || b[i] != '{' {
		return ms, false
	}
	i = skipSpace(b, i+1)
	if i < len(b) && b[i] == '}' {
		return ms, skipSpace(b, i+1) == len(b)
	}
	for {
		if i >= len(b) || b[i] != '"' {
			return ms, false
		}
		end, ok := skipString(b, i)
		if !ok {
			return ms, false
		}
		key, ok := decodeKey(b[i:end])
		if !ok {
			return ms, false
		}
		i = skipSpace(b, end)
		if i >= len(b) || b[i] != ':' {
			return ms, false
		}
		i = skipSpace(b, i+1)
		end, ok = skipValue(b, i)
		if !ok {
			return ms, false
		}
		ms = append(ms, member{key: key, val: b[i:end:end]})
		i = skipSpace(b, end)
		if i >= len(b) {
			return ms, false
		}
		switch b[i] {
		case ',':
			i = skipSpace(b, i+1)
		case '}':
			return ms, skipSpace(b, i+1) == len(b)
		default:
			return ms, false
		}
	}
}

// decodeKey decodes an encoded JSON string, quotes included.
func decodeKey(raw []byte) ([]byte, bool) {
	if bytes.IndexByte(raw, '\\') < 0 {
		return raw[1 : len(raw)-1], true
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, false
	}
	return []byte(s), true
}

func skipSpace(b []byte, i int) int {
	for i < len(b) && (b[i] == ' ' || b[i] == '\t' || b[i] == '\n' || b[i] == '\r') {
		i++
	}
	return i
}

// skipString returns the index just past the string starting at b[i], a '"'.
func skipString(b []byte, i int) (int, bool) {
	for j := i + 1; j < len(b); j++ {
		switch b[j] {
		case '\\':
			j++
		case '"':
			return j + 1, true
		}
	}
	return 0, false
}

// skipValue returns the index just past the value starting at b[i].
func skipValue(b []byte, i int) (int, bool) {
	if i >= len(b) {
		return 0, false
	}
	switch b[i] {
	case '"':
		return skipString(b, i)
	case '{', '[':
		depth := 0
		for j := i; j < len(b); j++ {
			switch b[j] {
			case '"':
				end, ok := skipString(b, j)
				if !ok {
					return 0, false
				}
				j = end - 1
			case '{', '[':
				depth++
			case '}', ']':
				if depth--; depth == 0 {
					return j + 1, true
				}
			}
		}
		return 0, false
	default: // a number, true, false or null
		j := i
		for j < len(b) && !endsScalar(b[j]) {
			j++
		}
		return j, j > i
	}
}

func endsScalar(c byte) bool {
	switch c {
	case ',', '}', ']', ' ', '\t', '\n', '\r':
		return true
	}
	return false
}
