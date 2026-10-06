package realtime

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"dash.cha.sh/internal/jsonpatch"
	"dash.cha.sh/internal/livedoc"
)

// TestProjectedDocSource: a projected viewer gets each item through the
// projection, in the snapshot and after every change, including items that
// become visible or stop being so; a viewer without a projection gets the
// document as published; the clock passes through to both.
func TestProjectedDocSource(t *testing.T) {
	doc := livedoc.New("items")
	put := func(id string, v map[string]any) {
		doc.Update(func(tx *livedoc.Tx) error {
			tx.Put("items", id, v)
			return nil
		})
	}
	doc.Update(func(tx *livedoc.Tx) error {
		tx.Set("now", "14:05")
		return nil
	})
	put("a", map[string]any{"title": "plex down", "public": true, "secret": "token expired"})
	put("b", map[string]any{"title": "disk full", "public": false, "secret": "/mnt/disk3"})

	src := Mux{"inbox": ProjectedDocSource{Doc: doc, Topic: "inbox", Coll: "items",
		Project: func(ctx context.Context) func(string, json.RawMessage) (json.RawMessage, bool) {
			if who, _ := ctx.Value(viewerKey{}).(string); who == "admin" {
				return nil
			}
			return func(_ string, raw json.RawMessage) (json.RawMessage, bool) {
				var v struct {
					Title  string `json:"title"`
					Public bool   `json:"public"`
				}
				if json.Unmarshal(raw, &v) != nil || !v.Public {
					return nil, false
				}
				b, _ := json.Marshal(map[string]string{"title": v.Title})
				return b, true
			}
		}}}

	open := func(who string) (Feed, map[string]any) {
		t.Helper()
		f, err := OpenWith(context.WithValue(context.Background(), viewerKey{}, who), src, "inbox", make(chan struct{}, 1))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(f.Close)
		_, snap := f.Snapshot()
		b, _ := json.Marshal(snap)
		var m map[string]any
		json.Unmarshal(b, &m)
		return f, m
	}
	sync := func(f Feed, m map[string]any) []jsonpatch.Op {
		t.Helper()
		_, ops, err := f.Drain()
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(ops)
		var generic []jsonpatch.Op
		json.Unmarshal(b, &generic)
		if err := jsonpatch.Apply(m, generic); err != nil {
			t.Fatalf("ops don't apply: %v (%s)", err, b)
		}
		return generic
	}
	hh, hdoc := open("household")
	ad, adoc := open("admin")
	want := func(step string, got map[string]any, items map[string]any) {
		t.Helper()
		if !reflect.DeepEqual(got["items"], items) {
			t.Errorf("%s: items = %v, want %v", step, got["items"], items)
		}
	}
	want("snapshot", hdoc, map[string]any{"a": map[string]any{"title": "plex down"}})
	if hdoc["now"] != "14:05" {
		t.Fatalf("clock = %v", hdoc["now"])
	}
	if len(adoc["items"].(map[string]any)) != 2 {
		t.Fatalf("admin's snapshot = %v", adoc)
	}

	// A change to a field the projection drops sends nothing.
	put("a", map[string]any{"title": "plex down", "public": true, "secret": "token revoked"})
	if ops := sync(hh, hdoc); len(ops) != 0 {
		t.Fatalf("hidden field change sent %v", ops)
	}
	// b becomes visible; a's title changes; the clock ticks.
	put("b", map[string]any{"title": "disk full", "public": true, "secret": "/mnt/disk3"})
	put("a", map[string]any{"title": "plex reconnecting", "public": true, "secret": "x"})
	doc.Update(func(tx *livedoc.Tx) error {
		tx.Set("now", "14:06")
		return nil
	})
	sync(hh, hdoc)
	want("after changes", hdoc, map[string]any{"a": map[string]any{"title": "plex reconnecting"}, "b": map[string]any{"title": "disk full"}})
	if hdoc["now"] != "14:06" {
		t.Fatalf("clock = %v", hdoc["now"])
	}
	// a stops being visible; b is deleted.
	put("a", map[string]any{"title": "plex reconnecting", "public": false})
	doc.Update(func(tx *livedoc.Tx) error {
		tx.Delete("items", "b")
		return nil
	})
	sync(hh, hdoc)
	want("after hiding", hdoc, map[string]any{})

	// The admin's feed is the published document.
	sync(ad, adoc)
	if got := adoc["items"].(map[string]any)["a"].(map[string]any)["public"]; got != false {
		t.Fatalf("admin's a = %v", adoc["items"])
	}
	if _, ok := adoc["items"].(map[string]any)["b"]; ok {
		t.Fatalf("admin still has b: %v", adoc["items"])
	}
}
