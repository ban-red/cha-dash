package jsonpatch

import (
	"encoding/json"
	"math/rand/v2"
	"reflect"
	"testing"
)

func TestPointer(t *testing.T) {
	if got := Pointer("resources", "pve:mars/lxc/101", "a~b"); got != "/resources/pve:mars~1lxc~1101/a~0b" {
		t.Fatalf("Pointer = %q", got)
	}
	segs, err := split("/resources/pve:mars~1lxc~1101/a~0b")
	if err != nil || !reflect.DeepEqual(segs, []string{"resources", "pve:mars/lxc/101", "a~b"}) {
		t.Fatalf("split = %v, %v", segs, err)
	}
}

func TestCompact(t *testing.T) {
	in := []Op{
		{Op: Add, Path: "/a", Value: 1},
		{Op: Replace, Path: "/b", Value: 1},
		{Op: Replace, Path: "/a", Value: 2}, // add+replace → add(2)
		{Op: Add, Path: "/c", Value: 1},
		{Op: Remove, Path: "/c"}, // add+remove → nothing
		{Op: Remove, Path: "/d"},
		{Op: Add, Path: "/d", Value: 9},     // remove+add → replace(9)
		{Op: Replace, Path: "/b", Value: 3}, // replace+replace → replace(3)
		{Op: Remove, Path: "/e"},
	}
	want := []Op{
		{Op: Add, Path: "/a", Value: 2},
		{Op: Replace, Path: "/b", Value: 3},
		{Op: Replace, Path: "/d", Value: 9},
		{Op: Remove, Path: "/e"},
	}
	if got := Compact(in); !reflect.DeepEqual(got, want) {
		t.Fatalf("Compact =\n%v\nwant\n%v", got, want)
	}
}

// Compacting must not change what a patch does.
func TestCompactEquivalence(t *testing.T) {
	base := func() map[string]any {
		return map[string]any{"m": map[string]any{"b": 0.0, "d": 0.0, "e": 0.0}}
	}
	ops := Prefix("/m", []Op{
		{Op: Add, Path: "/a", Value: 1},
		{Op: Replace, Path: "/b", Value: 1},
		{Op: Replace, Path: "/a", Value: 2},
		{Op: Add, Path: "/c", Value: 1},
		{Op: Remove, Path: "/c"},
		{Op: Remove, Path: "/d"},
		{Op: Add, Path: "/d", Value: 9},
		{Op: Remove, Path: "/e"},
	})
	a, b := base(), base()
	if err := Apply(a, ops); err != nil {
		t.Fatal(err)
	}
	if err := Apply(b, Compact(ops)); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("raw %v != compacted %v", a, b)
	}
}

// Ops on a member and ops inside it: a later op on the member makes earlier
// ones inside it moot, and later ones inside it must stay after it.
func TestCompactNested(t *testing.T) {
	for name, tc := range map[string]struct{ in, want []Op }{
		"field, member, field": {
			in: []Op{
				{Op: Replace, Path: "/s/k/value", Value: 1},
				{Op: Replace, Path: "/s/k", Value: map[string]any{"value": 2}},
				{Op: Replace, Path: "/s/k/value", Value: 3},
			},
			want: []Op{
				{Op: Replace, Path: "/s/k", Value: map[string]any{"value": 2}},
				{Op: Replace, Path: "/s/k/value", Value: 3},
			},
		},
		"a field added, the member replaced, the field removed": {
			in: []Op{
				{Op: Add, Path: "/s/k/stale", Value: true},
				{Op: Replace, Path: "/s/k", Value: map[string]any{"stale": true}},
				{Op: Remove, Path: "/s/k/stale"},
			},
			want: []Op{
				{Op: Replace, Path: "/s/k", Value: map[string]any{"stale": true}},
				{Op: Remove, Path: "/s/k/stale"},
			},
		},
		"fields of a member that goes away": {
			in: []Op{
				{Op: Replace, Path: "/s/k/value", Value: 1},
				{Op: Add, Path: "/s/k/stale", Value: true},
				{Op: Remove, Path: "/s/k"},
			},
			want: []Op{{Op: Remove, Path: "/s/k"}},
		},
		"a member that comes and goes": {
			in: []Op{
				{Op: Add, Path: "/s/k", Value: map[string]any{"value": 1}},
				{Op: Replace, Path: "/s/k/value", Value: 2},
				{Op: Remove, Path: "/s/k"},
			},
			want: []Op{},
		},
		"a member that goes and comes back, then changes": {
			in: []Op{
				{Op: Replace, Path: "/s/k/value", Value: 1},
				{Op: Remove, Path: "/s/k"},
				{Op: Add, Path: "/s/k", Value: map[string]any{"value": 2}},
				{Op: Replace, Path: "/s/k/value", Value: 3},
			},
			want: []Op{
				{Op: Replace, Path: "/s/k", Value: map[string]any{"value": 2}},
				{Op: Replace, Path: "/s/k/value", Value: 3},
			},
		},
		"siblings and look-alike paths are independent": {
			in: []Op{
				{Op: Replace, Path: "/s/k/value", Value: 1},
				{Op: Replace, Path: "/s/k2", Value: 1},
				{Op: Replace, Path: "/s/k/value", Value: 2},
				{Op: Replace, Path: "/s/k~1value", Value: 1}, // the member "k/value", not k's field
			},
			want: []Op{
				{Op: Replace, Path: "/s/k/value", Value: 2},
				{Op: Replace, Path: "/s/k2", Value: 1},
				{Op: Replace, Path: "/s/k~1value", Value: 1},
			},
		},
	} {
		if got := Compact(tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: Compact =\n%v\nwant\n%v", name, got, tc.want)
		}
	}
}

// TestCompactRandom checks Compact's contract on random valid sequences of
// ops on nested objects: the compacted patch does what the whole sequence
// does, and is never longer.
func TestCompactRandom(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 4))
	keys := []string{"a", "b", "c/d", "e~f"}
	var value func(depth int) any
	value = func(depth int) any {
		if depth >= 3 || rng.IntN(3) > 0 {
			return float64(rng.IntN(100))
		}
		m := map[string]any{}
		for _, k := range keys {
			if rng.IntN(2) == 0 {
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
	// objects lists every object in m, m included, with its pointer.
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
	clone := func(m map[string]any) map[string]any {
		v, err := generic(m)
		if err != nil {
			t.Fatal(err)
		}
		return v.(map[string]any)
	}

	for trial := range 2000 {
		start, ok := value(0).(map[string]any)
		if !ok {
			start = map[string]any{}
		}
		doc := clone(start)
		var ops []Op
		for range 1 + rng.IntN(40) {
			objs := objects("", 0, doc, nil)
			o := objs[rng.IntN(len(objs))]
			k := keys[rng.IntN(len(keys))]
			op := Op{Path: o.path + Pointer(k)}
			switch _, exists := o.m[k]; {
			case !exists:
				op.Op, op.Value = Add, value(o.depth+1)
			case rng.IntN(3) == 0:
				op.Op = Remove
			default:
				op.Op, op.Value = Replace, value(o.depth+1)
			}
			if err := Apply(doc, []Op{op}); err != nil {
				t.Fatalf("trial %d: generated an invalid op %v: %v", trial, op, err)
			}
			ops = append(ops, op)
		}
		got := clone(start)
		compact := Compact(ops)
		if err := Apply(got, compact); err != nil {
			t.Fatalf("trial %d: the compacted patch doesn't apply: %v\nops %v\ncompact %v", trial, err, ops, compact)
		}
		if !reflect.DeepEqual(got, doc) {
			t.Fatalf("trial %d: the compacted patch differs\nops %v\ncompact %v\n got %v\nwant %v", trial, ops, compact, got, doc)
		}
		if len(compact) > len(ops) {
			t.Fatalf("trial %d: Compact grew the patch from %d to %d ops", trial, len(ops), len(compact))
		}
	}
}

func TestApplyErrors(t *testing.T) {
	doc := map[string]any{"m": map[string]any{}}
	for _, o := range []Op{
		{Op: Replace, Path: "/m/missing", Value: 1},
		{Op: Remove, Path: "/m/missing"},
		{Op: Add, Path: "/nope/x", Value: 1},
		{Op: "move", Path: "/m/x"},
		{Op: Add, Path: "no-slash", Value: 1},
	} {
		if err := Apply(doc, []Op{o}); err == nil {
			t.Errorf("Apply(%+v) should fail", o)
		}
	}
}

func TestMarshal(t *testing.T) {
	b, _ := json.Marshal([]Op{{Op: Remove, Path: "/a", Value: 1}, {Op: Add, Path: "/b"}})
	if string(b) != `[{"op":"remove","path":"/a"},{"op":"add","path":"/b","value":null}]` {
		t.Fatalf("marshal = %s", b)
	}
}
