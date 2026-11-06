package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// SSE writes a text/event-stream response (server-sent events): named
// events with a JSON payload each, and comment pings that keep proxies
// from closing an idle stream. Safe for one writer and a pinger at once.
type SSE struct {
	mu sync.Mutex
	w  http.ResponseWriter
	rc *http.ResponseController
}

// StartSSE sends the stream's headers. Nothing is cached or buffered on
// the way: X-Accel-Buffering tells nginx not to.
func StartSSE(w http.ResponseWriter) *SSE {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	s := &SSE{w: w, rc: http.NewResponseController(w)}
	_ = s.rc.Flush()
	return s
}

// Event sends one event. name has no newline; v is marshalled as JSON,
// which has none either, so it's one data line.
func (s *SSE) Event(name string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if strings.ContainsAny(name, "\r\n") {
		return fmt.Errorf("httpx: an event name has a newline")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", name, b); err != nil {
		return err
	}
	return s.rc.Flush()
}

// Ping sends a comment, which clients ignore.
func (s *SSE) Ping() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.w.Write([]byte(": ping\n\n")); err != nil {
		return err
	}
	return s.rc.Flush()
}

// Follow limits.
const (
	// FollowLimit is how long one followed stream lasts.
	FollowLimit = 30 * time.Minute
	// FollowPing is how often an idle stream gets a ping, and the limit
	// is checked.
	FollowPing = 15 * time.Second
	// followStart is how long Follow waits for the first line, or for
	// produce to fail, before it starts the stream anyway.
	followStart = 500 * time.Millisecond
)

// FollowOptions shape Follow. Zero values take the defaults above and
// time.Now.
type FollowOptions struct {
	Now   func() time.Time
	Limit time.Duration
	Ping  time.Duration
	// LimitMessage is the end event's sentence when the limit stops it.
	LimitMessage string
	// ErrorText turns produce's error into the end event's sentence.
	ErrorText func(error) string
	// Refuse answers an error produce returns before the stream started,
	// as JSON with a status (a log that doesn't exist). Nil: the stream
	// starts and ends with the error.
	Refuse func(w http.ResponseWriter, err error)
}

// FollowEnd is the last event of a followed stream: why it ended.
type FollowEnd struct {
	Reason  string `json:"reason"` // done, limit or error
	Message string `json:"message,omitempty"`
}

// Follow streams what produce emits as "line" events until produce
// returns, the client leaves, or the limit passes on the clock (a test
// clock moves it), then sends an "end" event. The stream starts at the
// first line, or half a second in: an error before that goes to Refuse,
// so a log that doesn't exist answers 404, not an empty stream.
func Follow(w http.ResponseWriter, r *http.Request, o FollowOptions, produce func(ctx context.Context, emit func(any)) error) {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Limit <= 0 {
		o.Limit = FollowLimit
	}
	if o.Ping <= 0 {
		o.Ping = FollowPing
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	var (
		mu      sync.Mutex
		s       *SSE // nil until the stream starts
		limited atomic.Bool
	)
	deadline := o.Now().Add(o.Limit)
	stream := func() *SSE { // mu is held
		if s == nil {
			s = StartSSE(w)
		}
		return s
	}
	go func() {
		first := time.NewTimer(min(followStart, o.Ping))
		defer first.Stop()
		t := time.NewTicker(o.Ping)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-first.C:
				mu.Lock()
				err := stream().Ping()
				mu.Unlock()
				if err != nil {
					cancel()
					return
				}
			case <-t.C:
				if !o.Now().Before(deadline) {
					limited.Store(true)
					cancel()
					return
				}
				mu.Lock()
				err := stream().Ping()
				mu.Unlock()
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()
	err := produce(ctx, func(v any) {
		if ctx.Err() != nil {
			return
		}
		if !o.Now().Before(deadline) {
			limited.Store(true)
			cancel()
			return
		}
		mu.Lock()
		err := stream().Event("line", v)
		mu.Unlock()
		if err != nil {
			cancel() // the client went away
		}
	})
	cancel()
	mu.Lock()
	defer mu.Unlock()
	if s == nil && err != nil && o.Refuse != nil && !limited.Load() && r.Context().Err() == nil {
		o.Refuse(w, err)
		return
	}
	end := FollowEnd{Reason: "done"}
	switch {
	case limited.Load():
		end = FollowEnd{Reason: "limit", Message: o.LimitMessage}
	case r.Context().Err() != nil:
		return // nobody to tell
	case err != nil && !errors.Is(err, context.Canceled):
		end.Reason, end.Message = "error", "The stream stopped."
		if o.ErrorText != nil {
			end.Message = o.ErrorText(err)
		}
	}
	_ = stream().Event("end", end)
}
