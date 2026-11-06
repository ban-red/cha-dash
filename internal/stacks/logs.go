package stacks

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"sync"

	"dash.cha.sh/internal/httpx"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/msg"
	"dash.cha.sh/internal/stackproto"
)

// nodeCaller adapts the stacks' caller to integration.NodeCaller.
type nodeCaller struct{ s *Service }

func (c nodeCaller) Call(ctx context.Context, node, verb string, req any, progress func(json.RawMessage)) (json.RawMessage, error) {
	if progress == nil {
		progress = func(json.RawMessage) {}
	}
	return c.s.caller.Call(ctx, node, verb, req, progress)
}

// handleLogs serves GET /api/stacks/{node}/{stack}/logs?tail=<n|all>&
// follow=1: every container of the stack's log, interleaved as the lines
// arrive, each tagged with its service, as server-sent events
// (httpx.Follow). It reads with the node's container.logs, so the node's
// policy needs docker.logs. A followed stream ends after 30 minutes, and
// nothing is kept.
func (s *Service) handleLogs(w http.ResponseWriter, r *http.Request) {
	node, name, ok := pathStack(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	opts := integration.LogOptions{Tail: integration.DefaultLogTail, Follow: q.Get("follow") == "1" || q.Get("follow") == "true"}
	switch t := q.Get("tail"); t {
	case "":
	case "all":
		opts.Tail = integration.MaxLogTail
	default:
		n, err := strconv.Atoi(t)
		if err != nil || n < 0 {
			httpx.Say(w, http.StatusBadRequest, "bad_request", msg.New("logs.bad_tail"))
			return
		}
		opts.Tail = min(n, integration.MaxLogTail)
	}
	var st stackproto.Stack
	if err := s.call(r.Context(), node, stackproto.VerbGet, stackproto.GetRequest{Stack: name}, &st, s.timeout, nil); err != nil {
		s.fail(w, err)
		return
	}
	var cs []stackproto.Service
	for _, c := range st.Containers {
		if c.Container != "" {
			cs = append(cs, c)
		}
	}
	if len(cs) == 0 {
		httpx.Say(w, http.StatusNotFound, "no_logs", msg.New("stacks.no_logs", "stack", name))
		return
	}
	httpx.Follow(w, r, httpx.FollowOptions{
		Now:          s.now,
		Refuse:       s.fail,
		LimitMessage: msg.New("logs.follow_limit").Text(),
		ErrorText: func(err error) string {
			_, _, m := statusOf(err)
			return m
		},
	}, func(ctx context.Context, emit func(any)) error {
		// One stream per container; the first error ends them all.
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		var mu sync.Mutex
		var first error
		var wg sync.WaitGroup
		for _, c := range cs {
			wg.Go(func() {
				err := integration.NodeLogs(ctx, nodeCaller{s}, node, c.Container, c.Name, opts, func(l integration.LogLine) {
					mu.Lock()
					defer mu.Unlock()
					emit(l)
				})
				if err != nil && ctx.Err() == nil {
					mu.Lock()
					if first == nil {
						first = err
					}
					mu.Unlock()
					cancel()
				}
			})
		}
		wg.Wait()
		return first
	})
}
