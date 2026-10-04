package topics

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"dash.cha.sh/internal/graph"
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
