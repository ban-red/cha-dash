package frigate

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/coder/websocket"

	"dash.cha.sh/internal/integration"
)

// wsReadLimit caps one WebSocket message. The biggest one Frigate sends is
// the stats push (~11 kB for three cameras).
const wsReadLimit = 2 << 20

// onConnect is the only frame Subscribe ever sends: Frigate answers it by
// republishing its current state (camera_activity, …) and changes nothing.
// The one place a …/set is sent is an approved action, on a connection of
// its own (actions.go); …/ptz, restart and the rest are never sent.
const onConnect = `{"topic":"onConnect","payload":"","retain":false}`

// Subscribe implements integration.Subscriber: Frigate's /ws WebSocket,
// open only while someone is watching. It relays the MQTT topics without
// broker credentials: a heartbeat per camera every 10 s, motion, feature
// toggles, new tracked objects and the stats push. Binary frames (pictures)
// are never decoded.
func (in *instance) Subscribe(ctx context.Context, emit integration.Emitter) error {
	conn, _, err := websocket.Dial(ctx, in.base.JoinPath("ws").String(), &websocket.DialOptions{HTTPClient: in.http})
	if err != nil {
		return err
	}
	defer conn.CloseNow()
	conn.SetReadLimit(wsReadLimit)
	in.mu.Lock()
	in.wsLive = true
	in.configStale = true // Frigate restarts to apply config changes, which drops the socket
	in.mu.Unlock()
	defer func() {
		// Back to polling: forget what only the WebSocket knew.
		in.mu.Lock()
		defer in.mu.Unlock()
		in.wsLive = false
		in.statsFromWS = false
		for _, c := range in.cams {
			c.wsStatus, c.motion = "", nil
		}
		if in.synced {
			_ = emit.Replace(scopeGraph, in.graphFacts())
		}
	}()
	if err := conn.Write(ctx, websocket.MessageText, []byte(onConnect)); err != nil {
		return err
	}
	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			return err
		}
		if typ != websocket.MessageText {
			continue
		}
		var msg struct {
			Topic   string          `json:"topic"`
			Payload json.RawMessage `json:"payload"`
		}
		if json.Unmarshal(data, &msg) != nil || msg.Topic == "" {
			continue
		}
		if err := in.handle(emit, msg.Topic, payload(msg.Payload)); err != nil {
			return err
		}
	}
}

// payload unwraps Frigate's payload, which is a string (JSON documents come
// encoded inside it).
func payload(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(raw)
}

// handle applies one message and publishes what it changed.
func (in *instance) handle(emit integration.Emitter, topic, p string) error {
	in.mu.Lock()
	defer in.mu.Unlock()
	graph, metrics := false, false
	switch topic {
	case "stats":
		var st stats
		if json.Unmarshal([]byte(p), &st) != nil || st.Cameras == nil {
			return nil
		}
		in.applyStats(st, in.d.Now(), true)
		graph, metrics = true, true
	case "camera_activity":
		var act map[string]struct {
			Config struct {
				Enabled   *bool `json:"enabled"`
				Detect    *bool `json:"detect"`
				Record    *bool `json:"record"`
				Snapshots *bool `json:"snapshots"`
				Alerts    *bool `json:"alerts"`
			} `json:"config"`
			Motion *bool `json:"motion"`
		}
		if json.Unmarshal([]byte(p), &act) != nil {
			return nil
		}
		for key, a := range act {
			if c := in.cam(key, false); c != nil {
				c.enabled = boolOr(a.Config.Enabled, c.enabled)
				c.detect = boolOr(a.Config.Detect, c.detect)
				c.record = boolOr(a.Config.Record, c.record)
				if a.Config.Snapshots != nil {
					c.snapshots = a.Config.Snapshots
				}
				if a.Config.Alerts != nil {
					c.alerts = a.Config.Alerts
				}
				if a.Motion != nil {
					m := *a.Motion
					c.motion = &m
				}
				graph = true
			}
		}
	case "events":
		var ev struct {
			Type  string `json:"type"`
			After event  `json:"after"`
		}
		if json.Unmarshal([]byte(p), &ev) != nil {
			return nil
		}
		if c := in.cam(ev.After.Camera, false); c != nil && ev.Type == "new" && !boolOr(ev.After.FalsePositive, false) {
			c.noteEvent(ev.After)
			in.eventsDirty = true // the next sync recounts
			graph = true
		}
	default:
		key, rest, ok := strings.Cut(topic, "/")
		c := in.cam(key, false)
		if !ok || c == nil {
			return nil
		}
		on := strings.EqualFold(p, "ON")
		switch rest {
		case "status/detect":
			if s := strings.ToLower(p); s == "online" || s == "offline" || s == "disabled" {
				graph = c.wsStatus != s
				c.wsStatus = s
			}
		case "motion":
			graph = c.motion == nil || *c.motion != on
			c.motion = &on
		case "enabled/state":
			graph, c.enabled = c.enabled != on, on
		case "detect/state":
			graph, c.detect = c.detect != on, on
		case "recordings/state":
			graph, c.record = c.record != on, on
		case "snapshots/state":
			graph = c.snapshots == nil || *c.snapshots != on
			c.snapshots = &on
		case "review_alerts/state":
			graph = c.alerts == nil || *c.alerts != on
			c.alerts = &on
		}
	}
	if !in.synced {
		return nil // the first sync publishes the app; until then, only remember
	}
	if graph {
		if err := emit.Replace(scopeGraph, in.graphFacts()); err != nil {
			return err
		}
	}
	if metrics {
		return emit.Replace(scopeStats, in.statsFacts())
	}
	return nil
}
