package export

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
)

// FieldChange is one difference inside an object, in JSON Patch's words.
// Path is a JSON Pointer into the object, except that an array whose
// elements are objects with a unique "id" (or "item_id") is addressed by
// that id rather than by index: /sections/media/items/plex@app:plex/size.
// Such an array whose common elements changed order gets one "reorder"
// with the ids before and after.
//
// From and To are non-secret values: secrets are compared apart from
// fields and reported as SecretChange, by name.
type FieldChange struct {
	Path string `json:"path"`
	Op   string `json:"op"` // add, replace, remove, reorder
	From any    `json:"from,omitempty"`
	To   any    `json:"to,omitempty"`
}

// Field operations.
const (
	OpAdd     = "add"
	OpReplace = "replace"
	OpRemove  = "remove"
	OpReorder = "reorder"
)

// maxFieldChanges bounds the field changes listed for one object; the
// rest are summed up by Change.Truncated.
const maxFieldChanges = 100

// differ collects field changes up to maxFieldChanges.
type differ struct {
	out       []FieldChange
	truncated bool
}

func (d *differ) emit(c FieldChange) {
	if len(d.out) >= maxFieldChanges {
		d.truncated = true
		return
	}
	d.out = append(d.out, c)
}

// diff compares two decoded JSON values (numbers as json.Number).
func (d *differ) diff(path string, a, b any) {
	switch x := a.(type) {
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok {
			break
		}
		keys := make([]string, 0, len(x)+len(y))
		for k := range x {
			keys = append(keys, k)
		}
		for k := range y {
			if _, ok := x[k]; !ok {
				keys = append(keys, k)
			}
		}
		slices.Sort(keys)
		for _, k := range keys {
			p := path + "/" + escape(k)
			av, inA := x[k]
			bv, inB := y[k]
			switch {
			case !inB:
				d.emit(FieldChange{Path: p, Op: OpRemove, From: av})
			case !inA:
				d.emit(FieldChange{Path: p, Op: OpAdd, To: bv})
			default:
				d.diff(p, av, bv)
			}
		}
		return
	case []any:
		y, ok := b.([]any)
		if !ok {
			break
		}
		if key := idKey(x, y); key != "" {
			d.keyed(path, key, x, y)
			return
		}
	}
	if !equalJSON(a, b) {
		d.emit(FieldChange{Path: path, Op: OpReplace, From: a, To: b})
	}
}

// keyed diffs two arrays of objects by their id member.
func (d *differ) keyed(path, key string, a, b []any) {
	byID := func(xs []any) (map[string]any, []string) {
		m := make(map[string]any, len(xs))
		order := make([]string, 0, len(xs))
		for _, v := range xs {
			id := v.(map[string]any)[key].(string)
			m[id] = v
			order = append(order, id)
		}
		return m, order
	}
	am, aOrder := byID(a)
	bm, bOrder := byID(b)
	for _, id := range aOrder {
		if _, ok := bm[id]; !ok {
			d.emit(FieldChange{Path: path + "/" + escape(id), Op: OpRemove, From: am[id]})
		}
	}
	var aCommon, bCommon []string
	for _, id := range aOrder {
		if _, ok := bm[id]; ok {
			aCommon = append(aCommon, id)
		}
	}
	for _, id := range bOrder {
		if _, ok := am[id]; !ok {
			d.emit(FieldChange{Path: path + "/" + escape(id), Op: OpAdd, To: bm[id]})
			continue
		}
		bCommon = append(bCommon, id)
		d.diff(path+"/"+escape(id), am[id], bm[id])
	}
	if !slices.Equal(aCommon, bCommon) {
		d.emit(FieldChange{Path: path, Op: OpReorder, From: aCommon, To: bCommon})
	}
}

// idKey returns the member that identifies the elements of both arrays,
// when every element of each is an object with a unique string under it.
func idKey(a, b []any) string {
	if len(a) == 0 && len(b) == 0 {
		return ""
	}
	for _, key := range []string{"id", "item_id"} {
		if uniqueBy(a, key) && uniqueBy(b, key) {
			return key
		}
	}
	return ""
}

func uniqueBy(xs []any, key string) bool {
	seen := make(map[string]bool, len(xs))
	for _, v := range xs {
		m, ok := v.(map[string]any)
		if !ok {
			return false
		}
		id, ok := m[key].(string)
		if !ok || id == "" || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

// escape is JSON Pointer's token escaping.
func escape(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "~", "~0"), "/", "~1")
}

// decodeJSON decodes raw with numbers kept as json.Number; empty is nil.
func decodeJSON(raw json.RawMessage) (any, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

// equalJSON compares decoded values; map key order never counts.
func equalJSON(a, b any) bool {
	x, err1 := json.Marshal(a)
	y, err2 := json.Marshal(b)
	return err1 == nil && err2 == nil && bytes.Equal(x, y)
}

// sameRaw compares two JSON documents as values.
func sameRaw(a, b json.RawMessage) bool {
	x, err1 := decodeJSON(a)
	y, err2 := decodeJSON(b)
	return err1 == nil && err2 == nil && equalJSON(x, y)
}
