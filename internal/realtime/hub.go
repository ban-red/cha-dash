package realtime

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"dash.cha.sh/internal/jsonpatch"
)

// Options tune the gateway. Zero values pick the defaults noted.
type Options struct {
	// OriginPatterns are extra origins allowed to open a socket (same-origin is
	// always allowed). Entries are "scheme://host[:port]" or host patterns as
	// understood by websocket.AcceptOptions.
	OriginPatterns []string
	PingInterval   time.Duration // 30 s
	HelloTimeout   time.Duration // 10 s
	WriteTimeout   time.Duration // 10 s; a client that can't take a frame this long is dropped
	Coalesce       time.Duration // 250 ms: at most 4 diffs per subscription per second
	SendQueue      int           // 64 frames per connection
	MaxSubs        int           // 32 per connection
	MaxTopics      int           // 16 per subscription
	ReadLimit      int64         // 64 KiB per client frame
	Server         string        // reported in hello
	Log            *slog.Logger
}

func (o *Options) defaults() {
	set := func(d *time.Duration, v time.Duration) {
		if *d <= 0 {
			*d = v
		}
	}
	set(&o.PingInterval, 30*time.Second)
	set(&o.HelloTimeout, 10*time.Second)
	set(&o.WriteTimeout, 10*time.Second)
	set(&o.Coalesce, 250*time.Millisecond)
	if o.SendQueue <= 0 {
		o.SendQueue = 64
	}
	if o.MaxSubs <= 0 {
		o.MaxSubs = 32
	}
	if o.MaxTopics <= 0 {
		o.MaxTopics = 16
	}
	if o.ReadLimit <= 0 {
		o.ReadLimit = 64 << 10
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
}

// Hub accepts WebSocket connections and serves subscriptions from a Source.
// Authentication happens before ServeHTTP (the server wraps it in the
// session middleware); the hub checks Origin.
type Hub struct {
	src   Source
	opts  Options
	epoch string

	mu     sync.Mutex
	conns  map[*conn]struct{}
	closed bool
}

// New returns a Hub serving src.
func New(src Source, opts Options) *Hub {
	opts.defaults()
	return &Hub{src: src, opts: opts, epoch: strings.ToLower(rand.Text())[:12], conns: map[*conn]struct{}{}}
}

// ServeHTTP upgrades the request and runs the connection until it ends.
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: h.opts.OriginPatterns})
	if err != nil {
		// Accept has already replied (403 for a foreign Origin, 400/426 otherwise).
		h.opts.Log.Debug("websocket upgrade rejected", "err", err)
		return
	}
	ws.SetReadLimit(h.opts.ReadLimit)

	ctx, cancel := context.WithCancel(r.Context())
	c := &conn{h: h, ws: ws, ctx: ctx, cancel: cancel, out: make(chan []byte, h.opts.SendQueue), subs: map[int64]*sub{}}
	if !h.add(c) {
		ws.Close(websocket.StatusTryAgainLater, "server shutting down")
		cancel()
		return
	}
	defer h.remove(c)
	c.serve()
}

// Close tells every client the server is going away and stops accepting new
// ones. It's meant for http.Server.RegisterOnShutdown, since hijacked
// connections aren't covered by Shutdown.
func (h *Hub) Close() {
	h.mu.Lock()
	h.closed = true
	conns := make([]*conn, 0, len(h.conns))
	for c := range h.conns {
		conns = append(conns, c)
	}
	h.mu.Unlock()
	var wg sync.WaitGroup
	for _, c := range conns {
		wg.Go(func() { c.closeWith(websocket.StatusGoingAway, "server shutting down") })
	}
	wg.Wait()
}

func (h *Hub) add(c *conn) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return false
	}
	h.conns[c] = struct{}{}
	return true
}

func (h *Hub) remove(c *conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.conns, c)
}

// conn is one browser tab.
type conn struct {
	h      *Hub
	ws     *websocket.Conn
	ctx    context.Context
	cancel context.CancelFunc
	out    chan []byte      // frames for the writer
	subs   map[int64]*sub   // owned by the read loop
	wg     sync.WaitGroup   // writer and subscription goroutines
	closer sync.Once
}

// closeWith runs the close handshake, then stops the connection's goroutines.
// The order matters: cancelling first would abort the pending Read, and
// coder/websocket then drops the TCP connection before the close frame (and
// its status code) reaches the peer.
func (c *conn) closeWith(code websocket.StatusCode, reason string) {
	c.closer.Do(func() {
		c.ws.Close(code, reason)
		c.cancel()
	})
}

func (c *conn) serve() {
	log := c.h.opts.Log
	c.wg.Go(c.writeLoop)
	defer func() {
		c.cancel()
		c.wg.Wait()
		c.closeWith(websocket.StatusNormalClosure, "")
	}()

	if err := c.handshake(); err != nil {
		log.Debug("websocket handshake failed", "err", err)
		return
	}
	for {
		m, err := c.read(c.ctx)
		if err != nil {
			var perr protocolError
			if errors.As(err, &perr) {
				c.trySend(errorMsg{Op: "error", Code: codeBadRequest, Msg: perr.msg})
				continue
			}
			return // closed, timed out or shutting down
		}
		switch m.Op {
		case "sub":
			c.subscribe(m)
		case "unsub":
			c.unsubscribe(m.ID)
		case "ping":
			c.trySend(opMsg{Op: "pong"})
		case "pong":
		case "hello":
			c.trySend(errorMsg{Op: "error", Code: codeBadRequest, Msg: "already said hello"})
		default:
			c.trySend(errorMsg{Op: "error", Code: codeUnknownOp, Msg: fmt.Sprintf("unknown op %q", m.Op)})
		}
	}
}

// handshake expects hello first and answers it.
func (c *conn) handshake() error {
	// A timer rather than a Read deadline, so a silent client gets a proper
	// policy-violation close (see closeWith).
	timer := time.AfterFunc(c.h.opts.HelloTimeout, func() {
		c.closeWith(websocket.StatusPolicyViolation, "expected hello")
	})
	m, err := c.read(c.ctx)
	timer.Stop()
	if err != nil || m.Op != "hello" {
		c.closeWith(websocket.StatusPolicyViolation, "expected hello")
		return fmt.Errorf("no hello: %v", err)
	}
	if m.V != ProtocolVersion {
		// Say why before closing, so the client can show "please reload".
		b, _ := json.Marshal(errorMsg{Op: "error", Code: codeVersion, Msg: fmt.Sprintf("this hub speaks protocol v%d", ProtocolVersion)})
		wctx, wcancel := context.WithTimeout(c.ctx, c.h.opts.WriteTimeout)
		_ = c.ws.Write(wctx, websocket.MessageText, b)
		wcancel()
		c.closeWith(websocket.StatusPolicyViolation, "unsupported protocol version")
		return fmt.Errorf("client wants protocol v%d", m.V)
	}
	return c.send(c.ctx, helloMsg{Op: "hello", V: ProtocolVersion, Epoch: c.h.epoch, Server: c.h.opts.Server, Ping: int(c.h.opts.PingInterval / time.Second)})
}

type protocolError struct{ msg string }

func (e protocolError) Error() string { return e.msg }

func (c *conn) read(ctx context.Context) (clientMsg, error) {
	typ, b, err := c.ws.Read(ctx)
	if err != nil {
		return clientMsg{}, err
	}
	if typ != websocket.MessageText {
		return clientMsg{}, protocolError{"frames must be JSON text"}
	}
	var m clientMsg
	if err := json.Unmarshal(b, &m); err != nil {
		return clientMsg{}, protocolError{"malformed JSON frame"}
	}
	return m, nil
}

// writeLoop is the only writer of data frames. It also pings: an op frame
// for the browser's watchdog, and a protocol ping that drops peers which have
// silently vanished.
func (c *conn) writeLoop() {
	ping := time.NewTicker(c.h.opts.PingInterval)
	defer ping.Stop()
	pingFrame, _ := json.Marshal(opMsg{Op: "ping"})
	for {
		select {
		case <-c.ctx.Done():
			return
		case b := <-c.out:
			if err := c.write(b); err != nil {
				c.cancel()
				return
			}
		case <-ping.C:
			if err := c.write(pingFrame); err != nil {
				c.cancel()
				return
			}
			go func() {
				ctx, cancel := context.WithTimeout(c.ctx, c.h.opts.PingInterval)
				defer cancel()
				if err := c.ws.Ping(ctx); err != nil && c.ctx.Err() == nil {
					c.h.opts.Log.Debug("websocket peer stopped answering pings", "err", err)
					c.cancel()
				}
			}()
		}
	}
}

func (c *conn) write(b []byte) error {
	ctx, cancel := context.WithTimeout(c.ctx, c.h.opts.WriteTimeout)
	defer cancel()
	return c.ws.Write(ctx, websocket.MessageText, b)
}

// trySend queues a frame without blocking. False means the queue is full.
func (c *conn) trySend(v any) bool {
	b, err := json.Marshal(v)
	if err != nil {
		c.h.opts.Log.Error("realtime: marshal frame", "err", err)
		return false
	}
	select {
	case c.out <- b:
		return true
	default:
		return false
	}
}

// send queues a frame, waiting for room.
func (c *conn) send(ctx context.Context, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	select {
	case c.out <- b:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *conn) subscribe(m clientMsg) {
	fail := func(code, msg string) { c.trySend(errorMsg{Op: "error", ID: m.ID, Code: code, Msg: msg}) }
	switch {
	case m.ID <= 0:
		fail(codeBadRequest, "id must be a positive integer")
		return
	case c.subs[m.ID] != nil:
		fail(codeDuplicateID, "subscription id already in use")
		return
	case len(c.subs) >= c.h.opts.MaxSubs:
		fail(codeTooManySubs, fmt.Sprintf("at most %d subscriptions per connection", c.h.opts.MaxSubs))
		return
	case len(m.Topics) == 0 || len(m.Topics) > c.h.opts.MaxTopics:
		fail(codeBadRequest, fmt.Sprintf("a subscription needs 1–%d topics", c.h.opts.MaxTopics))
		return
	}

	ctx, cancel := context.WithCancel(c.ctx)
	s := &sub{id: m.ID, ctx: ctx, cancel: cancel, notify: make(chan struct{}, 1)}
	seen := map[string]bool{}
	for _, t := range m.Topics {
		if seen[t] {
			continue
		}
		seen[t] = true
		f, err := c.h.src.Open(t, s.notify)
		if err != nil {
			s.close()
			code := codeBadRequest
			if errors.Is(err, ErrUnknownTopic) {
				code = codeUnknownTopic
			}
			fail(code, err.Error())
			return
		}
		s.feeds = append(s.feeds, topicFeed{topic: t, prefix: jsonpatch.Pointer(t), feed: f})
	}

	// The snapshot is queued before the loop starts, so no diff can overtake it.
	seq, data := s.snapshot()
	if err := c.send(ctx, snapMsg{Op: "snap", ID: s.id, Seq: seq, Data: data}); err != nil {
		s.close()
		return
	}
	c.subs[s.id] = s
	c.wg.Go(func() { s.run(c) })
}

func (c *conn) unsubscribe(id int64) {
	s := c.subs[id]
	if s == nil {
		c.trySend(errorMsg{Op: "error", ID: id, Code: codeUnknownSub, Msg: "no such subscription"})
		return
	}
	delete(c.subs, id)
	s.cancel() // run closes the feeds on its way out
}
