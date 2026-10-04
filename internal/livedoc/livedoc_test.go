package livedoc

import (
	"encoding/json"
	"errors"
	"math"
	"reflect"
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
