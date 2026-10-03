package jsonpatch

import (
	"encoding/json"
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
		{Op: Remove, Path: "/c"},            // add+remove → nothing
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
