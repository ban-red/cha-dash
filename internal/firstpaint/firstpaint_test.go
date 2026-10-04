package firstpaint

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"dash.cha.sh/internal/boards"
	"dash.cha.sh/internal/demo"
	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/livedoc"
	"dash.cha.sh/internal/store"
	"dash.cha.sh/internal/topics"
)

var owner = User{ID: "u_1", Username: "owner", Role: "owner"}

func newBuilder(t *testing.T) *Builder {
	t.Helper()
	st, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "chadash.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 3, 14, 5, 0, 0, time.UTC)
	return &Builder{Topics: topics.New(graph.New(), now), Boards: boards.New(st)}
}

func decode(t *testing.T, b []byte) Snapshot {
	t.Helper()
	var s Snapshot
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, b)
	}
	return s
}

func TestEscaping(t *testing.T) {
	b := newBuilder(t)
	lineSep := string(rune(0x2028))
	hostile := `</script><script>alert(1)</script><!-- & ]]> ` + lineSep
	if _, err := b.Topics.Graph.Update(func(tx *graph.Tx) error {
		return tx.Upsert(graph.Resource{ID: "app:x", Kind: "app", Name: hostile, Labels: map[string]string{"note": "</SCRIPT >"}})
	}); err != nil {
		t.Fatal(err)
	}
	b.Topics.Inbox.Update(func(tx *livedoc.Tx) error {
		tx.Put(topics.Incidents, "i1", map[string]any{"id": "i1", "status": "open", "title": hostile})
		return nil
	})
	out, err := b.Build(t.Context(), User{ID: "u_1", Username: "<b>owner</b>", Role: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	if i := bytes.IndexAny(out, "<>&"); i >= 0 {
		t.Fatalf("unescaped %q at %d in %s", out[i], i, out)
	}
	if bytes.Contains(out, []byte(lineSep)) {
		t.Fatal("unescaped U+2028")
	}
	s := decode(t, out)
	if got := s.Graph.Resources["app:x"].Name; got != hostile {
		t.Fatalf("name round-tripped as %q", got)
	}
	if s.User.Username != "<b>owner</b>" {
		t.Fatalf("username round-tripped as %q", s.User.Username)
	}
}

func TestBoard(t *testing.T) {
	b := newBuilder(t)
	s := decode(t, must(b.Build(t.Context(), owner)))
	if s.V != Version || s.User != owner {
		t.Fatalf("header %+v", s)
	}
	// The first call creates Home, as GET /api/boards does; its layout is null until saved.
	if len(s.Boards) != 1 || s.Board == nil || s.Board.ID != s.Boards[0].ID || s.Board.Name != "Home" || string(s.Board.Layout) != "null" || s.Board.Revision != 0 {
		t.Fatalf("new board: %+v %+v", s.Boards, s.Board)
	}
	layout := json.RawMessage(`{"v":1,"sections":[]}`)
	if _, err := b.Boards.SaveLayout(t.Context(), s.Board.ID, 0, 1, layout, ""); err != nil {
		t.Fatal(err)
	}
	s = decode(t, must(b.Build(t.Context(), owner)))
	if s.Board.Revision != 1 || s.Board.FormatVersion != 1 || string(s.Board.Layout) != string(layout) {
		t.Fatalf("saved board: %+v", s.Board)
	}
}

func TestTrimming(t *testing.T) {
	b := newBuilder(t)
	if _, err := b.Topics.Graph.Update(func(tx *graph.Tx) error {
		return tx.Upsert(graph.Resource{ID: "host:a", Kind: "host"})
	}); err != nil {
		t.Fatal(err)
	}
	b.Topics.Metrics.Update(func(tx *livedoc.Tx) error {
		tx.Put(topics.Series, topics.SeriesKey("host:a", "cpu.pct"), map[string]any{"value": 12})
		tx.Put(topics.Series, topics.SeriesKey("host:gone", "cpu.pct"), map[string]any{"value": 99})
		tx.Set("history", map[string]any{"points": 48})
		return nil
	})
	b.Topics.Inbox.Update(func(tx *livedoc.Tx) error {
		tx.Put(topics.Incidents, "open", map[string]any{"status": "open", "opened_at": "2026-10-03T09:00:00Z"})
		tx.Put(topics.Incidents, "old", map[string]any{"status": "resolved", "opened_at": "2026-10-03T08:00:00Z", "resolved_at": "2026-10-03T08:30:00Z"})
		tx.Put(topics.Incidents, "newest", map[string]any{"status": "resolved", "opened_at": "2026-10-03T07:00:00Z", "resolved_at": "2026-10-03T12:00:00Z"})
		tx.Put(topics.Incidents, "by-update", map[string]any{"status": "resolved", "opened_at": "2026-10-03T06:00:00Z", "updated_at": "2026-10-03T11:00:00Z"})
		return nil
	})
	s := decode(t, must(b.Build(t.Context(), owner)))
	if got := keys(s.Metrics[topics.Series]); !reflect.DeepEqual(got, []string{"host:a#cpu.pct"}) {
		t.Errorf("series %v: a series for a resource the graph doesn't have can't be shown", got)
	}
	if s.Metrics["history"] == nil {
		t.Error("the shared history axis was dropped")
	}
	if got := keys(s.Inbox[topics.Incidents]); !reflect.DeepEqual(got, []string{"newest", "open"}) {
		t.Errorf("incidents %v: want every open one and the newest resolution", got)
	}
	if s.Inbox[topics.Now] != "2026-10-03T14:05:00Z" {
		t.Errorf("inbox clock %v", s.Inbox[topics.Now])
	}
}

func TestSizeCap(t *testing.T) {
	b := newBuilder(t)
	full := must(b.Build(t.Context(), owner))
	b.MaxBytes = len(full)
	if _, err := b.Build(t.Context(), owner); err != nil {
		t.Fatalf("exactly at the cap: %v", err)
	}
	b.MaxBytes = len(full) - 1
	_, err := b.Build(t.Context(), owner)
	var big *TooLargeError
	if !errors.As(err, &big) || big.Size != len(full) || big.Max != len(full)-1 {
		t.Fatalf("over the cap: %v", err)
	}
}

// TestDemoHomeMatchesTheSocket checks the snapshot against what a tab's
// subscriptions would get, so the socket's first snapshots change nothing
// on screen: the graph exactly, every series (the Demo Home has no orphans),
// and the inbox's open incidents.
func TestDemoHomeMatchesTheSocket(t *testing.T) {
	b := newBuilder(t)
	d, err := demo.New(b.Topics, demo.Options{Seed: 1, At: 14*time.Hour + 5*time.Minute, Clock: func() time.Time { return time.Date(2031, 5, 6, 7, 8, 9, 0, time.UTC) }})
	if err != nil {
		t.Fatal(err)
	}
	b.Demo = true
	out := must(b.Build(t.Context(), owner))
	t.Logf("Demo Home at %s: %.1f kB inline", d.Now().Format(time.TimeOnly), float64(len(out))/1000)
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if got["demo"] != true {
		t.Error("demo flag missing")
	}
	src := b.Topics.Source()
	for _, topic := range []string{topics.Graph, topics.Metrics, topics.Inbox} {
		f, err := src.Open(topic, make(chan struct{}, 1))
		if err != nil {
			t.Fatal(err)
		}
		_, doc := f.Snapshot()
		f.Close()
		raw, _ := json.Marshal(realtimeFrame(topic, doc))
		var want map[string]any
		if err := json.Unmarshal(raw, &want); err != nil {
			t.Fatal(err)
		}
		w, g := want[topic].(map[string]any), got[topic].(map[string]any)
		switch topic {
		case topics.Inbox:
			for id, in := range w[topics.Incidents].(map[string]any) {
				if in.(map[string]any)["status"] == "open" && !reflect.DeepEqual(in, g[topics.Incidents].(map[string]any)[id]) {
					t.Errorf("open incident %s differs from the socket's", id)
				}
			}
			if w[topics.Now] != g[topics.Now] {
				t.Errorf("inbox clock %v, socket %v", g[topics.Now], w[topics.Now])
			}
		default:
			if !reflect.DeepEqual(w, g) {
				t.Errorf("%s differs from the socket's snapshot", topic)
			}
		}
	}
}

// realtimeFrame is a snap frame's data for one topic.
func realtimeFrame(topic string, doc any) map[string]any { return map[string]any{topic: doc} }

func keys(v any) []string {
	m, _ := v.(map[string]any)
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func must(b []byte, err error) []byte {
	if err != nil {
		panic(err)
	}
	return b
}
