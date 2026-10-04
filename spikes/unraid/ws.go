package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/coder/websocket"
	"github.com/vektah/gqlparser/v2/ast"
)

// The Unraid API serves subscriptions over graphql-ws (the "graphql-transport-ws"
// subprotocol) on the same /graphql path. Auth goes in the connection_init payload: the
// server merges connectionParams into the request headers, so {"x-api-key": "..."} works.
const wsSubprotocol = "graphql-transport-ws"

// maxWindow is the hard cap on how long a subscription socket stays open, close included.
const maxWindow = 60 * time.Second

// closeMargin is kept back from the window for completing subscriptions and the close handshake.
const closeMargin = 3 * time.Second

type wsMsg struct {
	ID      string          `json:"id,omitempty"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// Sub is one subscription to open on the shared socket.
type Sub struct {
	Name string // also the operation name
	Doc  string
}

// SubEvent is one message received for a subscription, kept for the raw capture.
type SubEvent struct {
	AtMS    int64           `json:"at_ms"` // since the subscribe message was sent
	Type    string          `json:"type"`  // next, error, complete
	Payload json.RawMessage `json:"payload,omitempty"`
}

// SubResult summarizes one subscription over the window.
type SubResult struct {
	Name     string     `json:"name"`
	Refused  string     `json:"refused,omitempty"` // the guard refused it; nothing was sent
	Next     int        `json:"next"`
	Errors   int        `json:"errors"`
	Codes    []string   `json:"codes,omitempty"`
	Complete bool       `json:"complete"`
	First    string     `json:"first_after,omitempty"`
	MeanGap  string     `json:"mean_gap,omitempty"`
	Events   []SubEvent `json:"events,omitempty"`
}

// WSReport is the outcome of one subscription window.
type WSReport struct {
	HandshakeStatus int         `json:"handshake_status"`
	Subprotocol     string      `json:"subprotocol"`
	Acked           bool        `json:"acked"`
	AckAfter        string      `json:"ack_after,omitempty"`
	CloseCode       int         `json:"close_code,omitempty"`
	CloseReason     string      `json:"close_reason,omitempty"`
	Window          string      `json:"window"`
	OpenFor         string      `json:"open_for"`
	Subs            []SubResult `json:"subs"`
}

// maxEventsKept bounds what a chatty subscription (CPU every second) keeps in memory.
const maxEventsKept = 120

func (c *Client) wsURL() string {
	u := *c.base
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	u.Path = "/graphql"
	u.RawQuery = ""
	return u.String()
}

// Subscribe opens one socket, sends connection_init (with the key, if any), subscribes to
// every sub that passes the guard, listens until the window (capped at 60 s, counted from
// the dial) is nearly over, then completes each subscription and closes the socket normally.
// With no subs it only checks the handshake and the ack.
func (c *Client) Subscribe(ctx context.Context, subs []Sub, window time.Duration) (*WSReport, error) {
	if window <= closeMargin || window > maxWindow {
		window = maxWindow
	}
	rep := &WSReport{Window: window.String()}

	// Guard first: a refused document is reported and never sent.
	var send []Sub
	for _, s := range subs {
		if err := checkReadOnly(s.Doc, ast.Subscription); err != nil {
			rep.Subs = append(rep.Subs, SubResult{Name: s.Name, Refused: err.Error()})
			continue
		}
		send = append(send, s)
	}
	if len(subs) > 0 && len(send) == 0 {
		return rep, errors.New("every subscription was refused by the guard; nothing was sent")
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.pace(ctx); err != nil {
		return rep, err
	}
	defer func() { c.last = time.Now() }()

	opened := time.Now()
	deadline := opened.Add(window - closeMargin)
	defer func() { rep.OpenFor = time.Since(opened).Round(time.Millisecond).String() }()

	dialCtx, cancelDial := context.WithDeadline(ctx, deadline)
	defer cancelDial()
	conn, resp, err := websocket.Dial(dialCtx, c.wsURL(), &websocket.DialOptions{
		HTTPClient:   &http.Client{Transport: c.hc.Transport, CheckRedirect: c.hc.CheckRedirect},
		HTTPHeader:   http.Header{"User-Agent": {"chadash-unraid-spike/0 (read-only)"}},
		Subprotocols: []string{wsSubprotocol},
	})
	if resp != nil {
		rep.HandshakeStatus = resp.StatusCode
	}
	if err != nil {
		return rep, fmt.Errorf("websocket dial: %s", c.red.Err(err))
	}
	defer conn.CloseNow()
	conn.SetReadLimit(maxBody)
	rep.Subprotocol = conn.Subprotocol()

	// One reader goroutine; this goroutine is the only writer. The reader's context outlives
	// the window so that ending the window doesn't tear the socket down mid-close.
	readCtx, cancelRead := context.WithDeadline(ctx, opened.Add(window))
	defer cancelRead()
	msgs := make(chan wsMsg, 64)
	var readErr error
	go func() {
		defer close(msgs)
		for {
			m, err := readMsg(readCtx, conn)
			if err != nil {
				readErr = err
				return
			}
			select {
			case msgs <- m:
			case <-readCtx.Done():
				readErr = readCtx.Err()
				return
			}
		}
	}()
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()

	writeCtx, cancelWrite := context.WithDeadline(ctx, opened.Add(window))
	defer cancelWrite()
	initPayload := map[string]string{}
	if c.apiKey != "" {
		initPayload["x-api-key"] = c.apiKey
	}
	if err := writeMsg(writeCtx, conn, wsMsg{Type: "connection_init", Payload: mustJSON(initPayload)}); err != nil {
		return rep, fmt.Errorf("connection_init: %s", c.red.Err(err))
	}
	initAt := time.Now()

	// Wait for the ack (10 s at most), answering pings.
	ackTimeout := time.NewTimer(10 * time.Second)
	defer ackTimeout.Stop()
	for !rep.Acked {
		select {
		case m, ok := <-msgs:
			if !ok { // readErr is set before msgs closes
				rep.CloseCode, rep.CloseReason = closeInfo(readErr)
				return rep, fmt.Errorf("socket closed before connection_ack: %s", c.red.Err(readErr))
			}
			switch m.Type {
			case "connection_ack":
				rep.Acked = true
				rep.AckAfter = time.Since(initAt).Round(time.Millisecond).String()
			case "ping":
				_ = writeMsg(writeCtx, conn, wsMsg{Type: "pong"})
			}
		case <-ackTimeout.C:
			_ = conn.Close(websocket.StatusNormalClosure, "no ack")
			return rep, errors.New("no connection_ack within 10s")
		case <-timer.C:
			_ = conn.Close(websocket.StatusNormalClosure, "window over")
			return rep, errors.New("window ended before connection_ack")
		}
	}

	if len(send) == 0 {
		_ = conn.Close(websocket.StatusNormalClosure, "probe done")
		return rep, nil
	}

	results := map[string]*SubResult{}
	sentAt := map[string]time.Time{}
	lastAt := map[string]time.Time{}
	gaps := map[string]time.Duration{}
	var order []string
	for i, s := range send {
		id := strconv.Itoa(i + 1)
		results[id] = &SubResult{Name: s.Name}
		order = append(order, id)
		payload := mustJSON(gqlRequest{Query: s.Doc, OperationName: s.Name})
		if err := writeMsg(writeCtx, conn, wsMsg{ID: id, Type: "subscribe", Payload: payload}); err != nil {
			return rep, fmt.Errorf("subscribe %s: %s", s.Name, c.red.Err(err))
		}
		sentAt[id] = time.Now()
	}

	open := len(order)
listen:
	for open > 0 {
		select {
		case <-timer.C:
			break listen
		case m, ok := <-msgs:
			if !ok {
				rep.CloseCode, rep.CloseReason = closeInfo(readErr)
				break listen
			}
			if m.Type == "ping" {
				_ = writeMsg(writeCtx, conn, wsMsg{Type: "pong"})
				continue
			}
			r := results[m.ID]
			if r == nil || r.Complete {
				continue
			}
			now := time.Now()
			if len(r.Events) < maxEventsKept {
				r.Events = append(r.Events, SubEvent{AtMS: now.Sub(sentAt[m.ID]).Milliseconds(), Type: m.Type, Payload: m.Payload})
			}
			switch m.Type {
			case "next":
				r.Next++
				if r.Next == 1 {
					r.First = now.Sub(sentAt[m.ID]).Round(time.Millisecond).String()
				} else {
					gaps[m.ID] += now.Sub(lastAt[m.ID])
				}
				lastAt[m.ID] = now
				var p gqlResponse
				if json.Unmarshal(m.Payload, &p) == nil && len(p.Errors) > 0 {
					r.Errors++
					r.Codes = appendCodes(r.Codes, p.Errors)
				}
			case "error":
				r.Errors++
				var errs []gqlError
				if json.Unmarshal(m.Payload, &errs) == nil {
					r.Codes = appendCodes(r.Codes, errs)
				}
				r.Complete = true
				open--
			case "complete":
				r.Complete = true
				open--
			}
		}
	}

	// Tidy up: complete whatever is still open, then close the socket normally.
	for _, id := range order {
		r := results[id]
		if !r.Complete && rep.CloseCode == 0 {
			_ = writeMsg(writeCtx, conn, wsMsg{ID: id, Type: "complete"})
		}
		if r.Next > 1 {
			r.MeanGap = (gaps[id] / time.Duration(r.Next-1)).Round(time.Millisecond).String()
		}
		rep.Subs = append(rep.Subs, *r)
	}
	if rep.CloseCode == 0 {
		_ = conn.Close(websocket.StatusNormalClosure, "window over")
	}
	return rep, nil
}

func appendCodes(have []string, errs []gqlError) []string {
	for _, e := range errs {
		code, _ := e.Extensions["code"].(string)
		if code == "" {
			code = "NO_CODE"
		}
		dup := false
		for _, h := range have {
			if h == code {
				dup = true
				break
			}
		}
		if !dup {
			have = append(have, code)
		}
	}
	return have
}

func writeMsg(ctx context.Context, c *websocket.Conn, m wsMsg) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return c.Write(ctx, websocket.MessageText, b)
}

func readMsg(ctx context.Context, c *websocket.Conn) (wsMsg, error) {
	_, b, err := c.Read(ctx)
	if err != nil {
		return wsMsg{}, err
	}
	var m wsMsg
	if err := json.Unmarshal(b, &m); err != nil {
		return wsMsg{}, fmt.Errorf("bad message from server: %w", err)
	}
	return m, nil
}

func closeInfo(err error) (int, string) {
	var ce websocket.CloseError
	if errors.As(err, &ce) {
		return int(ce.Code), ce.Reason
	}
	return 0, ""
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
