package jsonpatch

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestCompactKeepsAppends(t *testing.T) {
	const log = "/tasks/t_1/log"
	ops := []Op{
		{Op: Add, Path: log + "/-", Value: "one"},
		{Op: Add, Path: log + "/-", Value: "two"},
		{Op: Replace, Path: "/tasks/t_1/step", Value: "a"},
		{Op: Add, Path: log + "/-", Value: "three"},
		{Op: Replace, Path: "/tasks/t_1/step", Value: "b"},
	}
	got := Compact(ops)
	want := []Op{
		{Op: Add, Path: log + "/-", Value: "one"},
		{Op: Add, Path: log + "/-", Value: "two"},
		{Op: Replace, Path: "/tasks/t_1/step", Value: "b"},
		{Op: Add, Path: log + "/-", Value: "three"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Compact = %+v\nwant %+v", got, want)
	}

	// Replacing the array makes earlier appends moot; later ones stay after it.
	got = Compact([]Op{
		{Op: Add, Path: log + "/-", Value: "one"},
		{Op: Replace, Path: log, Value: []any{"two"}},
		{Op: Add, Path: log + "/-", Value: "three"},
	})
	want = []Op{
		{Op: Replace, Path: log, Value: []any{"two"}},
		{Op: Add, Path: log + "/-", Value: "three"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Compact = %+v\nwant %+v", got, want)
	}
	// So does replacing a member above it.
	got = Compact([]Op{
		{Op: Add, Path: log + "/-", Value: "one"},
		{Op: Replace, Path: "/tasks/t_1", Value: map[string]any{"log": []any{"x"}}},
	})
	if len(got) != 1 || got[0].Path != "/tasks/t_1" {
		t.Fatalf("Compact = %+v", got)
	}
}

func TestApplyAppends(t *testing.T) {
	var doc map[string]any
	_ = json.Unmarshal([]byte(`{"tasks":{"t_1":{"log":["one"]}}}`), &doc)
	if err := Apply(doc, []Op{{Op: Add, Path: "/tasks/t_1/log/-", Value: "two"}, {Op: Add, Path: "/tasks/t_1/log/-", Value: "three"}}); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(doc)
	if string(b) != `{"tasks":{"t_1":{"log":["one","two","three"]}}}` {
		t.Fatalf("doc = %s", b)
	}
	if err := Apply(doc, []Op{{Op: Add, Path: "/tasks/t_1/-", Value: "x"}}); err != nil {
		// an object member called "-": an ordinary add
		t.Fatal(err)
	}
	if err := Apply(doc, []Op{{Op: Add, Path: "/nope/-", Value: "x"}}); err == nil {
		t.Fatal("appended to a missing array")
	}
}
