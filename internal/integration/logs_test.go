package integration

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

type logNode struct {
	req   map[string]any
	lines []string
	err   error
}

func (n *logNode) Call(_ context.Context, node, verb string, req any, progress func(json.RawMessage)) (json.RawMessage, error) {
	b, _ := json.Marshal(req)
	_ = json.Unmarshal(b, &n.req)
	n.req["node"], n.req["verb"] = node, verb
	for _, l := range n.lines {
		progress(json.RawMessage(l))
	}
	return json.RawMessage(`{}`), n.err
}

// NodeLogs asks container.logs with timestamps and splits each one off
// into At; a line without one keeps its text; anything but stderr is stdout.
func TestNodeLogs(t *testing.T) {
	n := &logNode{lines: []string{
		`{"stream":"stdout","line":"2026-11-05T12:00:00.5Z listening on :80"}`,
		`{"stream":"stderr","line":"no timestamp here"}`,
		`{"stream":"","line":"2026-11-05T12:00:01Z odd"}`,
		`not json`,
	}}
	var got []LogLine
	err := NodeLogs(t.Context(), n, "jupiter", "media-web-1", "web", LogOptions{Tail: 50, Follow: true}, func(l LogLine) { got = append(got, l) })
	if err != nil {
		t.Fatal(err)
	}
	if n.req["verb"] != "container.logs" || n.req["node"] != "jupiter" || n.req["id"] != "media-web-1" || n.req["tail"] != "50" || n.req["follow"] != true || n.req["timestamps"] != true {
		t.Errorf("request %v", n.req)
	}
	want := []LogLine{
		{Stream: "stdout", Line: "listening on :80", At: time.Date(2026, 11, 5, 12, 0, 0, 5e8, time.UTC), Service: "web"},
		{Stream: "stderr", Line: "no timestamp here", Service: "web"},
		{Stream: "stdout", Line: "odd", At: time.Date(2026, 11, 5, 12, 0, 1, 0, time.UTC), Service: "web"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i].Stream != want[i].Stream || got[i].Line != want[i].Line || !got[i].At.Equal(want[i].At) || got[i].Service != want[i].Service {
			t.Errorf("line %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	n.err = errors.New("docker.logs not allowed")
	if err := NodeLogs(t.Context(), n, "jupiter", "x", "", LogOptions{}, func(LogLine) {}); err == nil {
		t.Error("the node's error is returned")
	}
}
