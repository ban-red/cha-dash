package runtime

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"dash.cha.sh/internal/httpx"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/msg"
)

// Logs streams resource rid's log from the instance id that owns it (an
// integration.LogStreamer) to emit, until it ends, ctx ends or the
// instance stops. Errors before any line: ErrNotFound, ErrNotRunning,
// integration.ErrNoLogs.
func (m *Manager) Logs(ctx context.Context, id, rid string, opts integration.LogOptions, emit func(integration.LogLine)) (err error) {
	c, s, err := m.logStreamer(id, rid)
	if err != nil {
		return err
	}
	defer c.uses.Done()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// The stream ends when the instance stops or is reconfigured.
	defer context.AfterFunc(c.ctx, cancel)()
	opts.Tail = min(max(opts.Tail, 0), integration.MaxLogTail)
	defer func() {
		if v := recover(); v != nil {
			err = &panicError{v}
		}
	}()
	return s.Logs(ctx, rid, opts, emit)
}

// logStreamer borrows the running instance id as a LogStreamer; the
// caller releases it with c.uses.Done.
func (m *Manager) logStreamer(id, rid string) (*coordinator, integration.LogStreamer, error) {
	mi := m.get(id)
	if mi == nil {
		return nil, nil, ErrNotFound
	}
	if rid == "" {
		return nil, nil, integration.ErrNoLogs
	}
	mi.life.Lock()
	c := mi.coord
	mi.life.Unlock()
	if c == nil {
		return nil, nil, ErrNotRunning
	}
	inst := c.borrow()
	if inst == nil {
		return nil, nil, ErrNotRunning
	}
	s, ok := inst.(integration.LogStreamer)
	if !ok {
		c.uses.Done()
		return nil, nil, integration.ErrNoLogs
	}
	return c, s, nil
}

// handleLogs serves GET /api/integrations/instances/{id}/logs?resource=
// <rid>&tail=<n|all>&follow=1 as server-sent events (httpx.Follow): a
// "line" event per log line, then an "end" event. A followed stream ends
// after 30 minutes. Nothing is kept.
func (m *Manager) handleLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	opts, ok := logOptions(w, q.Get("tail"), q.Get("follow"))
	if !ok {
		return
	}
	id, rid := r.PathValue("id"), q.Get("resource")
	// Refusals answer as JSON, before the stream starts.
	c, _, err := m.logStreamer(id, rid)
	switch {
	case errors.Is(err, integration.ErrNoLogs):
		httpx.Say(w, http.StatusNotFound, "no_logs", msg.New("integrations.no_logs"))
		return
	case err != nil:
		m.writeError(w, err)
		return
	}
	c.uses.Done()
	name := c.name
	httpx.Follow(w, r, httpx.FollowOptions{
		Now: m.o.Now,
		Refuse: func(w http.ResponseWriter, err error) {
			if errors.Is(err, integration.ErrNoLogs) {
				httpx.Say(w, http.StatusNotFound, "no_logs", msg.New("integrations.no_logs"))
				return
			}
			m.writeError(w, err)
		},
		LimitMessage: msg.New("logs.follow_limit").Text(),
		ErrorText: func(err error) string {
			if errors.Is(err, integration.ErrNoLogs) {
				return msg.New("integrations.no_logs").Text()
			}
			c.log.Debug("a log stream failed", "resource", rid, "err", c.red.scrub(err.Error()))
			return fmt.Sprintf("Cha Dash couldn't read the log from %s.", name)
		},
	}, func(ctx context.Context, emit func(any)) error {
		return m.Logs(ctx, id, rid, opts, func(l integration.LogLine) { emit(l) })
	})
}

// logOptions reads tail and follow; a bad tail answers 400.
func logOptions(w http.ResponseWriter, tail, follow string) (integration.LogOptions, bool) {
	o := integration.LogOptions{Tail: integration.DefaultLogTail, Follow: follow == "1" || follow == "true"}
	switch tail {
	case "":
	case "all":
		o.Tail = integration.MaxLogTail
	default:
		n, err := strconv.Atoi(tail)
		if err != nil || n < 0 {
			httpx.Say(w, http.StatusBadRequest, "bad_request", msg.New("logs.bad_tail"))
			return o, false
		}
		o.Tail = min(n, integration.MaxLogTail)
	}
	return o, true
}
