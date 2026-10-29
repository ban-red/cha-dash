package topics

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/livedoc"
	"dash.cha.sh/internal/realtime"
)

func TestSetShapes(t *testing.T) {
	s := New(graph.New(), time.Date(2026, 10, 3, 14, 5, 0, 500, time.FixedZone("x", 3600)))
	src := s.Source()
	for topic, want := range map[string]string{
		Graph:             `{"resources":{},"relations":{}}`,
		"graph:kind/host": `{"resources":{},"relations":{}}`,
		Metrics:           `{"series":{}}`,
		Inbox:             `{"incidents":{},"now":"2026-10-03T13:05:00Z"}`,
	} {
		f, err := src.Open(topic, nil)
		if err != nil {
			t.Fatalf("%s: %v", topic, err)
		}
		_, doc := f.Snapshot()
		f.Close()
		b, _ := json.Marshal(doc)
		if string(b) != want {
			t.Errorf("%s = %s, want %s", topic, b, want)
		}
	}
	if _, err := src.Open("tasks", nil); !errors.Is(err, realtime.ErrUnknownTopic) {
		t.Fatalf("unknown topic: %v", err)
	}
	if k := SeriesKey("pve:mars-1", "cpu.pct"); k != "pve:mars-1#cpu.pct" {
		t.Fatal(k)
	}
}

func TestRunClock(t *testing.T) {
	s := New(graph.New(), time.Unix(0, 0))
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		s.RunClock(ctx, time.Millisecond, func() time.Time { return time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC) })
		close(done)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for string(s.Inbox.Get(Now)) != `"2026-10-03T09:00:00Z"` {
		if time.Now().After(deadline) {
			t.Fatalf("clock never ticked: %s", s.Inbox.Get(Now))
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
}

// TestResourceMetrics: metrics:resource/<id> carries that resource's series
// and nothing else, from the snapshot on.
func TestResourceMetrics(t *testing.T) {
	s := New(graph.New(), time.Now())
	put := func(key string, v float64) {
		t.Helper()
		if _, err := s.Metrics.Update(func(tx *livedoc.Tx) error {
			tx.Put(Series, key, map[string]any{"value": v})
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	put(SeriesKey("docker:i_d/container/db", "cpu.pct"), 3)
	put(SeriesKey("docker:i_d/container/dbx", "cpu.pct"), 4)
	f, err := s.Source().Open(ResourceMetrics+"docker:i_d/container/db", make(chan struct{}, 1))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	_, doc := f.Snapshot()
	if b, _ := json.Marshal(doc); string(b) != `{"series":{"docker:i_d/container/db#cpu.pct":{"value":3}}}` {
		t.Fatalf("snapshot = %s", b)
	}
	put(SeriesKey("docker:i_d/container/dbx", "mem.mib"), 9)
	put(SeriesKey("docker:i_d/container/db", "mem.mib"), 8)
	_, ops, err := f.Drain()
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := json.Marshal(ops); !strings.Contains(string(b), "container~1db#mem.mib") || strings.Contains(string(b), "dbx") {
		t.Fatalf("ops = %s", b)
	}
}
