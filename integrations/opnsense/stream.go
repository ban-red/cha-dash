package opnsense

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"dash.cha.sh/internal/integration"
)

// errStalled is a stream that stopped sending events.
var errStalled = errors.New("the traffic stream stopped sending events")

// Subscribe implements integration.Subscriber. The runtime runs it only
// while someone watches, and cancels ctx when nobody does; an open stream
// holds a PHP worker and a configd process on the router, so it's never
// open otherwise. Each event carries every interface's byte deltas for the
// last interval, and the WAN's become the live rate. A stream is reopened
// after a window, and returns an error (retried with backoff) when it
// fails or stalls.
func (in *instance) Subscribe(ctx context.Context, emit integration.Emitter) error {
	for {
		err := in.streamOnce(ctx, emit)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// trafficEvent is one event of traffic/stream: deltas for the interval,
// and the router's clock.
type trafficEvent struct {
	Interfaces map[string]struct {
		InBytes  float64 `json:"inbytes"`
		OutBytes float64 `json:"outbytes"`
	} `json:"interfaces"`
	Time float64 `json:"time"`
}

// streamOnce reads one stream until its window ends (nil), it fails or it
// stalls.
func (in *instance) streamOnce(ctx context.Context, emit integration.Emitter) error {
	wctx, end := context.WithTimeout(ctx, in.window)
	defer end()
	sctx, stop := context.WithCancelCause(wctx)
	defer stop(nil)
	watchdog := time.AfterFunc(in.firstWait, func() { stop(errStalled) })
	defer watchdog.Stop()

	req, err := http.NewRequestWithContext(sctx, http.MethodGet, in.base+streamPath, nil)
	if err != nil {
		return err
	}
	req.SetBasicAuth(in.key, in.secret)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := in.stream.Do(req)
	if err != nil {
		return streamEnd(ctx, wctx, sctx, err)
	}
	defer resp.Body.Close()
	if err := in.check(resp, "Reporting: Traffic"); err != nil {
		return err
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		return fmt.Errorf("the traffic stream answered %q, not an event stream", ct)
	}

	// Server-sent events: "data:" lines up to a blank line make one event;
	// lines starting with ":" are comments. Rates come from the deltas over
	// the payload's own clock, so the first event, a partial interval,
	// only sets the clock.
	var prev float64
	var data bytes.Buffer
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) > 0 {
			if v, ok := bytes.CutPrefix(line, []byte("data:")); ok {
				if data.Len() > 0 {
					data.WriteByte('\n')
				}
				data.Write(bytes.TrimPrefix(v, []byte(" ")))
			}
			continue
		}
		if data.Len() == 0 {
			continue
		}
		var ev trafficEvent
		err := json.Unmarshal(data.Bytes(), &ev)
		data.Reset()
		if err != nil {
			return fmt.Errorf("a traffic event isn't JSON: %w", err)
		}
		watchdog.Reset(in.gapWait)
		w, ok := ev.Interfaces[in.wan]
		if !ok {
			continue
		}
		if dt := ev.Time - prev; prev > 0 && dt > 0 {
			if err := emit.Replace(scopeRate, integration.Facts{Metrics: in.rates(mbps(w.InBytes, dt), mbps(w.OutBytes, dt))}); err != nil {
				return err
			}
			in.live.Store(in.d.Now().UnixNano())
		}
		prev = ev.Time
	}
	err = sc.Err()
	if err == nil {
		err = errors.New("the router closed the traffic stream")
	}
	return streamEnd(ctx, wctx, sctx, err)
}

// streamEnd says why a stream ended: its window ran out (nil), the
// watchdog fired, or err.
func streamEnd(ctx, window, stream context.Context, err error) error {
	switch {
	case ctx.Err() != nil:
		return nil
	case errors.Is(context.Cause(stream), errStalled):
		return errStalled
	case window.Err() != nil:
		return nil
	}
	return err
}
