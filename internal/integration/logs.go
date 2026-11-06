package integration

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
)

// LogStreamer is an instance that can read a resource's log: a
// container's, through chadash-node. The hub's logs route serves it to
// operators and up as it arrives, and keeps none of it.
type LogStreamer interface {
	// Logs sends rid's last opts.Tail lines to emit, then with
	// opts.Follow every new one until ctx ends or the source stops. It
	// returns nil when the log ended (a tail sent, a container gone), the
	// context's error when the caller left. ErrNoLogs: rid has no log here.
	Logs(ctx context.Context, rid string, opts LogOptions, emit func(LogLine)) error
}

// LogOptions say how much of a log to read.
type LogOptions struct {
	Tail   int  // past lines, at most MaxLogTail
	Follow bool // then keep sending new ones
}

// LogLine is one line of a log.
type LogLine struct {
	Stream  string    `json:"stream"`      // stdout or stderr
	Line    string    `json:"line"`        // without its newline
	At      time.Time `json:"at,omitzero"` // when the source wrote it, when it says
	Service string    `json:"service,omitempty"`
}

// Log limits.
const (
	DefaultLogTail = 200
	MaxLogTail     = 10000
)

// ErrNoLogs is a resource the instance has no log for.
var ErrNoLogs = errors.New("integration: no logs for that resource")

// NodeLogs reads container's log from node with container.logs, with
// timestamps, and emits each line tagged with service (for a stack's
// interleaved log; "" for one container). It returns the node's error, or
// nil when the log ended.
func NodeLogs(ctx context.Context, nodes NodeCaller, node, container, service string, opts LogOptions, emit func(LogLine)) error {
	req := map[string]any{"id": container, "tail": strconv.Itoa(opts.Tail), "follow": opts.Follow, "timestamps": true}
	_, err := nodes.Call(ctx, node, "container.logs", req, func(b json.RawMessage) {
		var l struct{ Stream, Line string }
		if json.Unmarshal(b, &l) != nil {
			return
		}
		out := LogLine{Stream: l.Stream, Line: l.Line, Service: service}
		if ts, rest, ok := strings.Cut(l.Line, " "); ok {
			if at, err := time.Parse(time.RFC3339Nano, ts); err == nil {
				out.At, out.Line = at, rest
			}
		}
		if out.Stream != "stderr" {
			out.Stream = "stdout"
		}
		emit(out)
	})
	if err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}
