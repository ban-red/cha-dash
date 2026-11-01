package livedoc

import (
	"encoding/json"
	"testing"
)

// TestKeep: a collection's Keep function sees each Put's current and new
// value and decides what's stored, also after a Clear in the same batch;
// other collections are untouched.
func TestKeep(t *testing.T) {
	d := New("items", "other")
	var seen []string
	d.Keep("items", func(cur, next json.RawMessage) json.RawMessage {
		seen = append(seen, string(cur)+"→"+string(next))
		if cur == nil {
			return next
		}
		var c, n map[string]any
		_ = json.Unmarshal(cur, &c)
		_ = json.Unmarshal(next, &n)
		if v, ok := c["mark"]; ok {
			if _, has := n["mark"]; !has {
				n["mark"] = v
			}
		}
		b, _ := json.Marshal(n)
		return b
	})
	put := func(coll string, v any) {
		t.Helper()
		if _, err := d.Update(func(tx *Tx) error { tx.Put(coll, "a", v); return nil }); err != nil {
			t.Fatal(err)
		}
	}
	put("items", map[string]any{"v": 1, "mark": "x"})
	put("items", map[string]any{"v": 2})
	if got := string(d.Item("items", "a")); got != `{"mark":"x","v":2}` {
		t.Fatalf("kept: %s", got)
	}
	// A Clear then Put (a writer replacing its collection) keeps it too.
	if _, err := d.Update(func(tx *Tx) error {
		tx.Clear("items")
		tx.Put("items", "a", map[string]any{"v": 3})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := string(d.Item("items", "a")); got != `{"mark":"x","v":3}` {
		t.Fatalf("after a clear: %s", got)
	}
	// Other collections don't call it.
	n := len(seen)
	put("other", map[string]any{"v": 1})
	if len(seen) != n || string(d.Item("other", "a")) != `{"v":1}` {
		t.Fatalf("other collection: %v %s", seen[n:], d.Item("other", "a"))
	}
	// Removed, Put stores what it's given.
	d.Keep("items", nil)
	put("items", map[string]any{"v": 4})
	if got := string(d.Item("items", "a")); got != `{"v":4}` {
		t.Fatalf("without keep: %s", got)
	}
}
