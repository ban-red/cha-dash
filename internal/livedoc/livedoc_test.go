package livedoc

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"reflect"
	"slices"
	"strings"
	"testing"

	"dash.cha.sh/internal/jsonpatch"
)

// generic round-trips v through JSON, the way a client sees it.
func generic(t *testing.T, v any) map[string]any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func update(t *testing.T, d *Doc, fn func(tx *Tx)) uint64 {
	t.Helper()
	v, err := d.Update(func(tx *Tx) error { fn(tx); return nil })
	if err != nil {
		t.Fatal(err)
	}
	return v
}

type nanBox struct {
	V float64 `json:"v"`
}

func (n nanBox) MarshalJSON() ([]byte, error) {
	if math.IsNaN(n.V) {
		return []byte(`{"v":null}`), nil
	}
	return json.Marshal(struct {
		V float64 `json:"v"`
	}{n.V})
}

func TestShapeAndVersion(t *testing.T) {
	d := New("incidents")
	v, doc := d.Snapshot()
	if v != 0 || !reflect.DeepEqual(generic(t, doc), map[string]any{"incidents": map[string]any{}}) {
		t.Fatalf("empty doc = %d %v", v, generic(t, doc))
	}
	if got := update(t, d, func(tx *Tx) {
		tx.Set("now", "2026-10-03T14:05:00Z")
		tx.Put("incidents", "a", map[string]any{"status": "open"})
	}); got != 1 {
		t.Fatalf("version %d, want 1", got)
	}
	// Re-asserting the same facts is free, including values that hold NaN.
	update(t, d, func(tx *Tx) { tx.Put("incidents", "n", nanBox{math.NaN()}) })
	if got := update(t, d, func(tx *Tx) {
		tx.Set("now", "2026-10-03T14:05:00Z")
		tx.Put("incidents", "a", map[string]any{"status": "open"})
		tx.Put("incidents", "n", nanBox{math.NaN()})
		tx.Delete("incidents", "missing")
	}); got != 2 {
		t.Fatalf("no-op batch bumped the version to %d", got)
	}
	want := map[string]any{"now": "2026-10-03T14:05:00Z", "incidents": map[string]any{
		"a": map[string]any{"status": "open"},
		"n": map[string]any{"v": nil},
	}}
	if _, doc := d.Snapshot(); !reflect.DeepEqual(generic(t, doc), want) {
		t.Fatalf("doc = %v", generic(t, doc))
	}
	if string(d.Get("now")) != `"2026-10-03T14:05:00Z"` || string(d.Item("incidents", "a")) != `{"status":"open"}` {
		t.Fatalf("Get/Item: %s %s", d.Get("now"), d.Item("incidents", "a"))
	}
}

func TestErrorsApplyNothing(t *testing.T) {
	d := New("series")
	for name, fn := range map[string]func(tx *Tx){
		"set on a collection": func(tx *Tx) { tx.Set("series", 1) },
		"unknown collection":  func(tx *Tx) { tx.Put("nope", "k", 1) },
		"empty key":           func(tx *Tx) { tx.Put("series", "", 1) },
		"unmarshalable":       func(tx *Tx) { tx.Put("series", "k", math.Inf(1)) },
		"clear unknown":       func(tx *Tx) { tx.Clear("nope") },
	} {
		_, err := d.Update(func(tx *Tx) error {
			tx.Put("series", "ok", 1)
			fn(tx)
			return nil
		})
		if err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if _, err := d.Update(func(tx *Tx) error { tx.Put("series", "ok", 1); return errors.New("boom") }); err == nil {
		t.Error("fn error not returned")
	}
	if d.Version() != 0 || d.Item("series", "ok") != nil {
		t.Fatal("a failed batch was applied")
	}
}

func TestWatcherPatchesReplay(t *testing.T) {
	d := New("incidents", "series")
	update(t, d, func(tx *Tx) {
		tx.Set("now", "t0")
		tx.Put("incidents", "a", map[string]any{"n": 1})
		tx.Put("incidents", "b", map[string]any{"n": 1})
	})
	notify := make(chan struct{}, 1)
	w := d.Watch(notify, 0)
	defer w.Close()
	_, snap := w.Snapshot()
	client := generic(t, snap)

	update(t, d, func(tx *Tx) {
		tx.Set("now", "t1")
		tx.Put("incidents", "a", map[string]any{"n": 2})
		tx.Delete("incidents", "b")
		tx.Put("incidents", "c/with~slash", map[string]any{"n": 1})
		tx.Put("series", "x#cpu.pct", 3)
	})
	// Clear then Put sends only the difference.
	update(t, d, func(tx *Tx) {
		tx.Clear("incidents")
		tx.Put("incidents", "a", map[string]any{"n": 2})
		tx.Put("incidents", "d", map[string]any{"n": 1})
	})
	select {
	case <-notify:
	default:
		t.Fatal("no notification")
	}
	seq, ops, err := w.Drain()
	if err != nil || seq != 3 {
		t.Fatalf("drain: %d %v", seq, err)
	}
	for _, o := range ops {
		if o.Path == "/incidents/a" && o.Op != jsonpatch.Replace {
			t.Fatalf("unexpected op %v", o)
		}
	}
	if err := jsonpatch.Apply(client, jsonpatch.Compact(ops)); err != nil {
		t.Fatal(err)
	}
	_, now := d.Snapshot()
	if !reflect.DeepEqual(client, generic(t, now)) {
		t.Fatalf("patched client %v\nwant %v", client, generic(t, now))
	}
}

func TestWatcherLags(t *testing.T) {
	d := New("series")
	w := d.Watch(nil, 3)
	defer w.Close()
	update(t, d, func(tx *Tx) {
		for _, k := range []string{"a", "b", "c", "d"} {
			tx.Put("series", k, 1)
		}
	})
	if _, _, err := w.Drain(); !errors.Is(err, ErrLagged) {
		t.Fatalf("want ErrLagged, got %v", err)
	}
	if v, doc := w.Snapshot(); v != 1 || len(doc["series"].(map[string]json.RawMessage)) != 4 {
		t.Fatalf("resync snapshot: %d %v", v, doc)
	}
	if _, ops, err := w.Drain(); err != nil || len(ops) != 0 {
		t.Fatalf("after resync: %v %v", ops, err)
	}
}

func TestClosedWatcherStops(t *testing.T) {
	d := New("series")
	w := d.Watch(nil, 0)
	w.Close()
	update(t, d, func(tx *Tx) { tx.Put("series", "a", 1) })
	if _, ops, _ := w.Drain(); len(ops) != 0 {
		t.Fatal("closed watcher received ops")
	}
	if len(d.watchers) != 0 {
		t.Fatal("watcher still registered")
	}
}

func TestSnapshotIsACopy(t *testing.T) {
	d := New("series")
	update(t, d, func(tx *Tx) { tx.Put("series", "a", 1) })
	_, doc := d.Snapshot()
	update(t, d, func(tx *Tx) { tx.Put("series", "b", 2) })
	if len(doc["series"].(map[string]json.RawMessage)) != 1 {
		t.Fatal("a snapshot changed after a later update")
	}
}

// opStrings renders ops as "op path value" for comparisons.
func opStrings(t *testing.T, ops []jsonpatch.Op) []string {
	t.Helper()
	out := make([]string, len(ops))
	for i, o := range ops {
		out[i] = o.Op + " " + o.Path
		if o.Op != jsonpatch.Remove {
			b, err := json.Marshal(o.Value)
			if err != nil {
				t.Fatal(err)
			}
			out[i] += " " + string(b)
		}
	}
	return out
}

func TestChangedFieldsOnly(t *testing.T) {
	type series struct {
		Unit    string    `json:"unit"`
		Value   float64   `json:"value"`
		AsOf    string    `json:"as_of"`
		Stale   bool      `json:"stale,omitempty"`
		History []float64 `json:"history"`
	}
	d := New("series")
	w := d.Watch(nil, 0)
	defer w.Close()
	step := func(name string, fn func(tx *Tx), want ...string) {
		t.Helper()
		update(t, d, fn)
		_, ops, err := w.Drain()
		if err != nil {
			t.Fatal(err)
		}
		if got := opStrings(t, ops); !slices.Equal(got, want) {
			t.Errorf("%s:\n got %q\nwant %q", name, got, want)
		}
	}
	js := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	const k = "pve:mars/lxc/101#cpu.pct"
	const p = "/series/pve:mars~1lxc~1101#cpu.pct"
	s := series{Unit: "%", Value: 1, AsOf: "t0"}
	for i := range 48 { // a day of history, like the demo's
		s.History = append(s.History, 40+float64(i)/8)
	}
	put := func(tx *Tx) { tx.Put("series", k, s) }

	step("a member appears whole", put, `add `+p+` `+js(s))
	s.Value, s.AsOf = 2, "t1"
	step("a tick sends the fields that changed", put, `replace `+p+`/as_of "t1"`, `replace `+p+`/value 2`)
	s.AsOf = "t2"
	step("an unchanged value isn't sent", put, `replace `+p+`/as_of "t2"`)
	s.Stale = true
	step("a field appears", put, `add `+p+`/stale true`)
	s.Stale, s.Value = false, 3
	step("a field disappears", put, `remove `+p+`/stale`, `replace `+p+`/value 3`)
	s.History = append(s.History[1:], 9)
	step("an array is replaced whole", put, `replace `+p+`/history `+js(s.History))

	step("a small member", func(tx *Tx) { tx.Put("series", "b", map[string]int{"x": 1, "y": 2}) }, `add /series/b {"x":1,"y":2}`)
	step("most of a small member changes: whole is smaller", func(tx *Tx) { tx.Put("series", "b", map[string]int{"x": 3, "y": 4}) },
		`replace /series/b {"x":3,"y":4}`)
	step("an object becomes a number", func(tx *Tx) { tx.Put("series", "b", 5) }, `replace /series/b 5`)
	step("a number changes", func(tx *Tx) { tx.Put("series", "b", 6) }, `replace /series/b 6`)
	step("a number becomes an object", func(tx *Tx) { tx.Put("series", "b", map[string]int{"x": 1}) }, `replace /series/b {"x":1}`)
	step("a member disappears whole", func(tx *Tx) { tx.Delete("series", "b") }, `remove /series/b`)

	// Value members are diffed the same way.
	cfg := map[string]any{"a": strings.Repeat("x", 40), "b": 1}
	step("an object value", func(tx *Tx) { tx.Set("cfg", cfg) }, `add /cfg {"a":"`+strings.Repeat("x", 40)+`","b":1}`)
	cfg["b"] = 2
	step("a field of an object value", func(tx *Tx) { tx.Set("cfg", cfg) }, `replace /cfg/b 2`)

	// Clear then Put sends only the differences, field by field.
	s.Value = 4
	step("clear and refill", func(tx *Tx) {
		tx.Clear("series")
		tx.Put("series", "b", map[string]int{"x": 1})
		tx.Put("series", k, s)
	}, `add /series/b {"x":1}`, `replace `+p+`/value 4`)
}

func TestNoWatchersStillVersions(t *testing.T) {
	// Patches are built only for watchers; versions move regardless.
	d := New("series")
	update(t, d, func(tx *Tx) { tx.Put("series", "a", map[string]int{"x": 1, "y": 1}) })
	if v := update(t, d, func(tx *Tx) { tx.Put("series", "a", map[string]int{"x": 1, "y": 2}) }); v != 2 {
		t.Fatalf("version %d, want 2", v)
	}
	if v := update(t, d, func(tx *Tx) { tx.Put("series", "a", map[string]int{"x": 1, "y": 2}) }); v != 2 {
		t.Fatalf("a no-op bumped the version to %d", v)
	}
}

// TestPatchesReproduceDoc is the core contract, as for the graph: snapshot +
// every drained patch == a fresh snapshot, through random histories of
// members that appear, disappear and change field by field, with drains
// coalesced and compacted the way the realtime layer does.
func TestPatchesReproduceDoc(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	colls := []string{"series", "incidents"}
	keys := []string{"k0", "k1", "k2", "k3", "x/y", "a~b"}
	fieldNames := []string{"value", "as_of", "stale", "history", "unit", "a/b", "c~d"}
	scalar := func() any {
		switch rng.IntN(5) {
		case 0:
			return fmt.Sprintf("t%d", rng.IntN(3))
		case 1:
			return []any{rng.IntN(3), nil, rng.IntN(3)}
		case 2:
			return map[string]any{"n": rng.IntN(2)}
		case 3:
			return nil
		default:
			return rng.IntN(3)
		}
	}
	object := func() map[string]any {
		m := map[string]any{}
		for _, f := range fieldNames {
			if rng.IntN(2) == 0 {
				m[f] = scalar()
			}
		}
		return m
	}
	// model mirrors the document's items, to make field-level edits.
	model := map[string]map[string]any{"series": {}, "incidents": {}}

	d := New(colls...)
	w := d.Watch(nil, 1<<20)
	defer w.Close()
	_, start := w.Snapshot()
	client := generic(t, start)
	var pending []jsonpatch.Op
	for step := range 1000 {
		update(t, d, func(tx *Tx) {
			for range 1 + rng.IntN(5) {
				coll, key := colls[rng.IntN(2)], keys[rng.IntN(len(keys))]
				switch rng.IntN(10) {
				case 0:
					tx.Delete(coll, key)
					delete(model[coll], key)
				case 1:
					if rng.IntN(5) == 0 {
						tx.Clear(coll)
						model[coll] = map[string]any{}
					}
				case 2:
					tx.Set("now", scalar())
				case 3:
					v := scalar()
					tx.Put(coll, key, v)
					model[coll][key] = v
				case 4:
					v := object()
					tx.Put(coll, key, v)
					model[coll][key] = v
				default: // edit a field or two
					cur, ok := model[coll][key].(map[string]any)
					if !ok {
						cur = object()
					}
					next := make(map[string]any, len(cur))
					for f, v := range cur {
						next[f] = v
					}
					for range 1 + rng.IntN(2) {
						f := fieldNames[rng.IntN(len(fieldNames))]
						if rng.IntN(4) == 0 {
							delete(next, f)
						} else {
							next[f] = scalar()
						}
					}
					tx.Put(coll, key, next)
					model[coll][key] = next
				}
			}
		})
		drained := false
		if rng.IntN(3) == 0 || step == 999 {
			_, ops, err := w.Drain()
			if err != nil {
				t.Fatal(err)
			}
			pending = append(pending, ops...)
			drained = true
		}
		if rng.IntN(4) == 0 || step == 999 {
			b, _ := json.Marshal(jsonpatch.Compact(pending))
			var wire []jsonpatch.Op
			if err := json.Unmarshal(b, &wire); err != nil {
				t.Fatal(err)
			}
			if err := jsonpatch.Apply(client, wire); err != nil {
				t.Fatalf("step %d: %v", step, err)
			}
			pending = nil
			if drained {
				if _, now := d.Snapshot(); !reflect.DeepEqual(client, generic(t, now)) {
					t.Fatalf("step %d: patched client %v\nwant %v", step, client, generic(t, now))
				}
			}
		}
	}
	if _, now := d.Snapshot(); !reflect.DeepEqual(client, generic(t, now)) {
		t.Fatalf("patched client %v\nwant %v", client, generic(t, now))
	}
}
