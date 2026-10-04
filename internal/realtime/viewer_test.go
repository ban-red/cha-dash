package realtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"dash.cha.sh/internal/jsonpatch"
	"dash.cha.sh/internal/livedoc"
)

type viewerKey struct{}

// TestViewerDocSource: each viewer gets only its own items, in the snapshot
// and in every later op, and the ops it gets always apply to the document
// it holds.
func TestViewerDocSource(t *testing.T) {
	doc := livedoc.New("tasks")
	put := func(id, owner, state string) {
		doc.Update(func(tx *livedoc.Tx) error {
			tx.Put("tasks", id, map[string]string{"owner": owner, "state": state, "pad": strings.Repeat("x", 64)})
			return nil
		})
	}
	put("t_1", "sam", "running")
	put("t_2", "kim", "running")
	src := Mux{"tasks": ViewerDocSource{Doc: doc, Topic: "tasks", Coll: "tasks",
		Visible: func(ctx context.Context) func(string, json.RawMessage) bool {
			who, _ := ctx.Value(viewerKey{}).(string)
			return func(_ string, item json.RawMessage) bool {
				var v struct{ Owner string }
				json.Unmarshal(item, &v)
				return who == "admin" || v.Owner == who
			}
		}}}

	type viewer struct {
		name string
		feed Feed
		doc  map[string]any
	}
	var viewers []*viewer
	for _, name := range []string{"sam", "kim", "admin", ""} {
		ctx := context.WithValue(context.Background(), viewerKey{}, name)
		f, err := OpenWith(ctx, src, "tasks", make(chan struct{}, 1))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		_, snap := f.Snapshot()
		b, _ := json.Marshal(snap)
		var m map[string]any
		json.Unmarshal(b, &m)
		viewers = append(viewers, &viewer{name: name, feed: f, doc: m})
	}
	keys := func(v *viewer) string {
		var ks []string
		for _, k := range []string{"t_1", "t_2", "t_3"} {
			if _, ok := v.doc["tasks"].(map[string]any)[k]; ok {
				ks = append(ks, k)
			}
		}
		return strings.Join(ks, ",")
	}
	sync := func() {
		t.Helper()
		for _, v := range viewers {
			_, ops, err := v.feed.Drain()
			if err != nil {
				t.Fatal(err)
			}
			b, _ := json.Marshal(ops)
			var generic []jsonpatch.Op
			json.Unmarshal(b, &generic)
			if err := jsonpatch.Apply(v.doc, generic); err != nil {
				t.Fatalf("%q got ops that don't apply: %v (%s)", v.name, err, b)
			}
		}
	}
	want := func(step string, w map[string]string) {
		t.Helper()
		for _, v := range viewers {
			if got := keys(v); got != w[v.name] {
				t.Errorf("%s: %q sees %q, want %q", step, v.name, got, w[v.name])
			}
		}
	}
	want("snapshot", map[string]string{"sam": "t_1", "kim": "t_2", "admin": "t_1,t_2", "": ""})

	put("t_3", "sam", "queued")    // a new item, added whole
	put("t_2", "kim", "verifying") // a field change on an item only some hold
	sync()
	want("after adds", map[string]string{"sam": "t_1,t_3", "kim": "t_2", "admin": "t_1,t_2,t_3", "": ""})
	if st := viewers[1].doc["tasks"].(map[string]any)["t_2"].(map[string]any)["state"]; st != "verifying" {
		t.Fatalf("kim's t_2 state = %v", st)
	}

	doc.Update(func(tx *livedoc.Tx) error {
		tx.Delete("tasks", "t_1")
		tx.Delete("tasks", "t_2")
		return nil
	})
	sync()
	want("after removes", map[string]string{"sam": "t_3", "kim": "", "admin": "t_3", "": ""})
}
