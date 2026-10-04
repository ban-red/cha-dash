package realtime

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/livedoc"
)

func TestMuxAndDocSource(t *testing.T) {
	g := graph.New()
	upsert(t, g, graph.Resource{ID: "host:jupiter", Kind: "host"})
	inbox := livedoc.New("incidents")
	inbox.Update(func(tx *livedoc.Tx) error {
		tx.Set("now", "2026-10-03T14:05:00Z")
		return nil
	})
	src := Mux{"graph": GraphSource{Graph: g}, "inbox": DocSource{Doc: inbox, Topic: "inbox"}}
	_, url := newServer(t, src, Options{Coalesce: 10 * time.Millisecond})
	c := dial(t, url)
	c.hello()

	// Unknown names and unknown sub-topics of a known name are both refused.
	for i, topic := range []string{"metrics", "inbox:x", "graph:nope"} {
		c.send(frame{"op": "sub", "id": i + 10, "topics": []string{topic}})
		if f := c.next(); f["op"] != "error" || f["code"] != "unknown_topic" {
			t.Fatalf("%s: %v", topic, f)
		}
	}

	c.send(frame{"op": "sub", "id": 1, "topics": []string{"graph:kind/host", "inbox"}})
	snap := c.next()
	expectOp(t, snap, "snap")
	doc := snap["data"].(frame)
	if doc["inbox"].(frame)["now"] != "2026-10-03T14:05:00Z" || len(doc["inbox"].(frame)["incidents"].(frame)) != 0 {
		t.Fatalf("inbox snapshot = %v", doc["inbox"])
	}

	inbox.Update(func(tx *livedoc.Tx) error {
		tx.Set("now", "2026-10-03T14:05:30Z")
		tx.Put("incidents", "plex-token-revoked", map[string]any{"status": "open"})
		return nil
	})
	d := c.next()
	expectOp(t, d, "diff")
	var paths []string
	for _, op := range d["patch"].([]any) {
		paths = append(paths, op.(frame)["path"].(string))
	}
	if want := []string{"/inbox/incidents/plex-token-revoked", "/inbox/now"}; !reflect.DeepEqual(paths, want) {
		t.Fatalf("paths = %v, want %v", paths, want)
	}
	apply(t, doc, d)
	_, hosts := g.Snapshot(graph.KindFilter("host"))
	_, in := inbox.Snapshot()
	if !reflect.DeepEqual(generic(t, doc), generic(t, map[string]any{"graph:kind/host": hosts, "inbox": in})) {
		t.Fatalf("client document diverged: %v", doc)
	}
}

func TestDocFeedResync(t *testing.T) {
	d := livedoc.New("series")
	f, err := DocSource{Doc: d, Topic: "metrics", MaxPending: 1}.Open("metrics", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	d.Update(func(tx *livedoc.Tx) error {
		tx.Put("series", "a", 1)
		tx.Put("series", "b", 2)
		return nil
	})
	if _, _, err := f.Drain(); !errors.Is(err, ErrResync) {
		t.Fatalf("overflow should ask for a resync, got %v", err)
	}
	if seq, data := f.Snapshot(); seq != 1 || data == nil {
		t.Fatalf("snapshot after resync: %d %v", seq, data)
	}
}
