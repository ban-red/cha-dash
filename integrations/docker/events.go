package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"

	"dash.cha.sh/internal/integration"
)

// eventFilter keeps the stream to lifecycle and health changes, filtered by
// the engine: health-check execs, most of the traffic and carrying the
// check's command line, never cross the wire. Docker matches health_status
// against "health_status: healthy" and the like.
const eventFilter = `{"type":["container"],"event":["create","start","restart","die","stop","kill","oom","pause","unpause","destroy","rename","update","health_status"]}`

// event is the part of a Docker event the integration reads. The actor's
// attributes (every label of the container, secrets included) aren't even
// decoded, and events are never logged.
type event struct {
	Type   string `json:"Type"`
	Action string `json:"Action"`
	Actor  struct {
		ID string `json:"ID"`
	} `json:"Actor"`
}

// Subscribe implements integration.Subscriber: while someone watches, follow
// the event stream, re-inspecting the containers events name. Every
// (re)connect re-lists everything instead of replaying with since=: the
// engine buffers only 256 events, about five minutes of health-check noise.
// The stream ending (a proxy cutting an idle connection) is an error, so
// the runtime reconnects with backoff.
func (in *instance) Subscribe(ctx context.Context, _ integration.Emitter) error {
	in.mu.Lock()
	if in.api == "" {
		if err := in.negotiate(ctx); err != nil {
			in.mu.Unlock()
			return err
		}
	}
	api := in.api
	in.mu.Unlock()

	resp, err := in.get(ctx, in.stream, api+"/events?filters="+url.QueryEscape(eventFilter))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if denied(resp.StatusCode) {
		return &deniedError{path: api + "/events", status: resp.StatusCode}
	}
	if err := integration.CheckResponse(resp); err != nil {
		return err
	}
	in.live.Store(true)
	defer in.live.Store(false)

	// The stream is open, so nothing that happens from here on is missed.
	if err := in.refreshLocked(ctx, nil); err != nil {
		return err
	}
	events := make(chan event)
	failed := make(chan error, 1)
	go func() {
		dec := json.NewDecoder(resp.Body)
		for {
			var ev event
			if err := dec.Decode(&ev); err != nil {
				failed <- err
				return
			}
			select {
			case events <- ev:
			case <-ctx.Done():
				return
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-failed:
			if ctx.Err() != nil {
				return nil
			}
			if errors.Is(err, io.EOF) {
				return errors.New("the event stream ended")
			}
			return fmt.Errorf("reading events: %w", err)
		case ev := <-events:
			force := map[string]bool{}
			note := func(ev event) {
				if ev.Type == "container" && ev.Actor.ID != "" {
					force[ev.Actor.ID] = true
				}
			}
			note(ev)
			// A compose up arrives as a burst: take what's already waiting.
		burst:
			for {
				select {
				case ev := <-events:
					note(ev)
				default:
					break burst
				}
			}
			if err := in.refreshLocked(ctx, force); err != nil {
				return err
			}
		}
	}
}

// refreshLocked is refresh under the lock. A refresh cut short because the
// subscription is ending isn't an error.
func (in *instance) refreshLocked(ctx context.Context, force map[string]bool) error {
	in.mu.Lock()
	defer in.mu.Unlock()
	if err := in.refresh(ctx, force); err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}
