package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/coder/websocket"
)

// wsMsg is one websocket message as recorded. Binary frames are never kept:
// only their size is.
type wsMsg struct {
	AtMillis int64  `json:"at_ms"`
	Type     string `json:"type"` // text | binary
	Bytes    int    `json:"bytes"`
	Topic    string `json:"topic,omitempty"`
	Payload  any    `json:"payload,omitempty"` // scrubbed; decoded when it is JSON-in-a-string
}

// readOnlyTopics are the only messages this spike ever sends. Frigate's
// dispatcher answers "onConnect" by republishing current state
// (camera_activity, model_state, …) and changes nothing. Anything ending in
// /set, /ptz, /suspend, or "restart" changes state and is never sent.
var readOnlyTopics = map[string]bool{"onConnect": true}

func runWS(args []string) error {
	fs := flag.NewFlagSet("ws", flag.ExitOnError)
	envPath := fs.String("env", "", "dotenv file with FRIGATE_URL")
	out := fs.String("out", "", "directory for scrubbed raw captures (outside the repo)")
	secPath := fs.String("secrets", "", "0600 needle file outside the repo")
	window := fs.Duration("window", 45*time.Second, "listen window (capped at 55s)")
	path := fs.String("path", "/ws", "websocket path")
	onConnect := fs.Bool("onconnect", true, `send the read-only {"topic":"onConnect"} request after connecting`)
	_ = fs.Parse(args)
	if *envPath == "" || *out == "" {
		return fmt.Errorf("ws: --env and --out are required")
	}
	if *window > 55*time.Second {
		*window = 55 * time.Second
	}
	raw, err := baseURL(*envPath)
	if err != nil {
		return err
	}
	base, err := url.Parse(raw)
	if err != nil {
		return err
	}
	ws := *base
	switch base.Scheme {
	case "http":
		ws.Scheme = "ws"
	case "https":
		ws.Scheme = "wss"
	}
	ws.Path = *path
	c, err := NewClient(raw, 0, *window)
	if err != nil {
		return err
	}
	sec := &secrets{}

	ctx, cancel := context.WithTimeout(context.Background(), *window)
	defer cancel()
	t0 := time.Now()
	conn, resp, err := websocket.Dial(ctx, ws.String(), &websocket.DialOptions{
		HTTPClient: &http.Client{Transport: c.tr, Timeout: 10 * time.Second},
		HTTPHeader: http.Header{"User-Agent": []string{"chadash-spike-4.5 (read-only)"}},
	})
	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		log.Printf("ws dial failed: status %d: %s", status, c.scrub(err.Error()))
		rec := map[string]any{"path": *path, "status": status, "error": c.scrub(err.Error())}
		b, _ := json.MarshalIndent(rec, "", "  ")
		return os.WriteFile(filepath.Join(*out, "ws_"+slug(*path)+".raw.json"), b, 0o600)
	}
	defer conn.CloseNow()
	conn.SetReadLimit(4 << 20)
	dialMs := time.Since(t0).Milliseconds()
	log.Printf("ws connected in %dms (status %d)", dialMs, resp.StatusCode)

	if *onConnect {
		msg := map[string]any{"topic": "onConnect", "payload": "", "retain": false}
		if !readOnlyTopics[msg["topic"].(string)] {
			return errors.New("refused: not a read-only topic")
		}
		b, _ := json.Marshal(msg)
		if err := conn.Write(ctx, websocket.MessageText, b); err != nil {
			log.Printf("ws write: %s", c.scrub(err.Error()))
		}
	}

	var msgs []wsMsg
	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("ws read ended: %s", c.scrub(err.Error()))
			}
			break
		}
		m := wsMsg{AtMillis: time.Since(t0).Milliseconds(), Bytes: len(data)}
		if typ == websocket.MessageBinary {
			m.Type = "binary" // never decoded, never stored
			msgs = append(msgs, m)
			continue
		}
		m.Type = "text"
		var env struct {
			Topic   string `json:"topic"`
			Payload any    `json:"payload"`
		}
		if json.Unmarshal(data, &env) == nil {
			m.Topic = env.Topic
			p := env.Payload
			if s, ok := p.(string); ok {
				var inner any
				if json.Unmarshal([]byte(s), &inner) == nil {
					p = inner
				}
			}
			b, _ := json.Marshal(p)
			var clean any
			_ = json.Unmarshal(scrubImages(b, sec), &clean)
			m.Payload = clean
		} else {
			m.Payload = fmt.Sprintf("<non-JSON text frame, %d bytes>", len(data))
		}
		msgs = append(msgs, m)
	}
	conn.Close(websocket.StatusNormalClosure, "")

	counts := map[string]int{}
	bytesBy := map[string]int{}
	for _, m := range msgs {
		k := m.Topic
		if m.Type == "binary" {
			k = "<binary>"
		}
		counts[k]++
		bytesBy[k] += m.Bytes
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	log.Printf("%d messages in %s", len(msgs), window.String())
	for _, k := range keys {
		log.Printf("  topic %-40s n=%3d bytes=%7d", maskTopic(k), counts[k], bytesBy[k])
	}
	if err := sec.save(*secPath); err != nil {
		return err
	}
	rec := map[string]any{"path": *path, "status": resp.StatusCode, "dial_ms": dialMs, "window": window.String(), "onconnect_sent": *onConnect, "messages": msgs}
	b, _ := json.MarshalIndent(rec, "", "  ")
	return os.WriteFile(filepath.Join(*out, "ws_"+slug(*path)+".raw.json"), b, 0o600)
}

// maskTopic hides the camera part of per-camera topics in terminal output
// ("<camera>/detect/state").
func maskTopic(t string) string {
	for i := 0; i < len(t); i++ {
		if t[i] == '/' {
			return "<camera>" + t[i:]
		}
	}
	return t
}
