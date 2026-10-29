package jsonpatch

import (
	"reflect"
	"testing"
)

// fuzzChoices turns the fuzzer's bytes into choices; when they run out the
// sequence ends.
type fuzzChoices struct{ b []byte }

func (c *fuzzChoices) more() bool { return len(c.b) > 0 }

func (c *fuzzChoices) next() int {
	if len(c.b) == 0 {
		return 0
	}
	n := int(c.b[0])
	c.b = c.b[1:]
	return n
}

// FuzzCompact is TestCompactRandom's contract with the fuzzer choosing the
// ops, appends included (task logs stream as …/log/- appends): for any valid
// sequence, the compacted patch does to the document what the sequence
// does, and is never longer.
//
//	go test -run '^$' -fuzz '^FuzzCompact$' -fuzztime 60s ./internal/jsonpatch/
func FuzzCompact(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9})
	f.Add([]byte{1, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 2, 0, 0, 0})
	f.Add([]byte("appends, replaces and removes on nested members"))
	f.Fuzz(func(t *testing.T, data []byte) {
		c := &fuzzChoices{b: data}
		keys := []string{"a", "b", "c/d", "e~f"}
		var value func(depth int) any
		value = func(depth int) any {
			n := c.next()
			switch {
			case depth >= 3 || n%4 == 0:
				return float64(n)
			case n%4 == 1:
				return []any{float64(n)} // an array, for appends
			}
			m := map[string]any{}
			for _, k := range keys {
				if c.next()%2 == 0 {
					m[k] = value(depth + 1)
				}
			}
			return m
		}
		type object struct {
			path  string
			depth int
			m     map[string]any
		}
		var objects func(path string, depth int, m map[string]any, out []object) []object
		objects = func(path string, depth int, m map[string]any, out []object) []object {
			out = append(out, object{path, depth, m})
			for _, k := range keys {
				if child, ok := m[k].(map[string]any); ok {
					out = objects(path+Pointer(k), depth+1, child, out)
				}
			}
			return out
		}
		clone := func(v any) any {
			g, err := generic(v)
			if err != nil {
				t.Fatal(err)
			}
			return g
		}

		doc := map[string]any{}
		var ops []Op
		for len(ops) < 64 && c.more() {
			objs := objects("", 0, doc, nil)
			o := objs[c.next()%len(objs)]
			k := keys[c.next()%len(keys)]
			path := o.path + Pointer(k)
			var op Op
			cur, exists := o.m[k]
			switch _, isArray := cur.([]any); {
			case !exists:
				op = Op{Op: Add, Path: path, Value: value(o.depth + 1)}
			case isArray && c.next()%2 == 0:
				op = Op{Op: Add, Path: path + "/-", Value: float64(c.next())}
			case c.next()%3 == 0:
				op = Op{Op: Remove, Path: path}
			default:
				op = Op{Op: Replace, Path: path, Value: value(o.depth + 1)}
			}
			// What goes on the wire is a copy: later ops mustn't reach
			// back into an earlier op's value through the document.
			sent := Op{Op: op.Op, Path: op.Path, Value: clone(op.Value)}
			if err := Apply(doc, []Op{op}); err != nil {
				t.Fatalf("generated an invalid op %v: %v", op, err)
			}
			ops = append(ops, sent)
		}
		got := map[string]any{}
		compact := Compact(ops)
		if err := Apply(got, compact); err != nil {
			t.Fatalf("the compacted patch doesn't apply: %v\nops %v\ncompact %v", err, ops, compact)
		}
		if !reflect.DeepEqual(got, clone(doc)) {
			t.Fatalf("the compacted patch differs\nops %v\ncompact %v\n got %v\nwant %v", ops, compact, got, doc)
		}
		if len(compact) > len(ops) {
			t.Fatalf("Compact grew the patch from %d to %d ops", len(ops), len(compact))
		}
	})
}
