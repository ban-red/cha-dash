package nodeproto

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
)

// HandshakeTimeout bounds the WebSocket upgrade plus the TLS handshake, and
// the wait for the node's hello.
const HandshakeTimeout = 10 * time.Second

// wsReadLimit bounds one WebSocket message. TLS records are at most 16 KiB
// plus overhead, and NetConn writes one message per record.
const wsReadLimit = 64 << 10

// DialOptions configure the node's side of a connection.
type DialOptions struct {
	// Identity is the node's key.
	Identity Identity
	// Pins are the hub keys the node trusts. The hub must prove one of them
	// before the node sends anything.
	Pins []string
	// HTTPClient makes the outer connection (proxies, a reverse proxy's CA).
	// Nil is http.DefaultClient.
	HTTPClient *http.Client
	// MaxFrame bounds frames (DefaultMaxFrame when 0).
	MaxFrame int
	// Header is added to the upgrade request (a User-Agent, say).
	Header http.Header
}

// Dial connects to the hub at url (ws://chadash:3696/node or
// wss://dash.example/node) and runs the TLS handshake inside the WebSocket.
// It fails, having sent no frame, unless the hub proves a pinned key.
func Dial(ctx context.Context, url string, o DialOptions) (*Channel, error) {
	if len(o.Pins) == 0 {
		return nil, errors.New("nodeproto: no hub pin to check the hub against")
	}
	hctx, cancel := context.WithTimeout(ctx, HandshakeTimeout)
	defer cancel()
	h := http.Header{}
	for k, v := range o.Header {
		h[k] = v
	}
	h.Set(PinsHeader, strings.Join(o.Pins, ","))
	ws, resp, err := websocket.Dial(hctx, url, &websocket.DialOptions{HTTPClient: o.HTTPClient, HTTPHeader: h})
	if err != nil {
		if resp != nil {
			return nil, fmt.Errorf("nodeproto: dialling the hub: %s (%w)", resp.Status, err)
		}
		return nil, fmt.Errorf("nodeproto: dialling the hub: %w", err)
	}
	// The NetConn outlives the dial's timeout: the session runs on it.
	nc := websocket.NetConn(context.WithoutCancel(ctx), ws, websocket.MessageBinary)
	ws.SetReadLimit(wsReadLimit) // after NetConn, which lifts the limit
	tc := tls.Client(nc, clientConfig(o.Identity, o.Pins))
	if err := tc.HandshakeContext(hctx); err != nil {
		ws.CloseNow()
		return nil, fmt.Errorf("nodeproto: handshake with the hub: %w", err)
	}
	ch, err := newChannel(tc, ws, o.MaxFrame)
	if err != nil {
		ws.CloseNow()
		return nil, err
	}
	return ch, nil
}

// AcceptOptions configure the hub's side of a connection.
type AcceptOptions struct {
	// Identity picks the hub key to present, given the pins the node says
	// it trusts (from PinsHeader): the newest of them, so a node that
	// missed a rotation still connects.
	Identity func(accepted []string) Identity
	// MaxFrame bounds frames (DefaultMaxFrame when 0).
	MaxFrame int
}

// Accept upgrades a request to /node and runs the hub's side of the TLS
// handshake. The returned channel's PeerPin is the node's key, proved in
// the handshake; whether that key is welcome is the caller's decision (the
// registry). It also returns the pins the node trusts.
//
// Nodes send no Origin. A browser that sends one is held to the same-origin
// rule by the WebSocket library, and it couldn't complete the handshake
// without a node's private key anyway.
func Accept(w http.ResponseWriter, r *http.Request, o AcceptOptions) (*Channel, []string, error) {
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("nodeproto: upgrade: %w", err)
	}
	var accepted []string
	for p := range strings.SplitSeq(r.Header.Get(PinsHeader), ",") {
		if p = strings.TrimSpace(p); p != "" && len(accepted) < 8 {
			accepted = append(accepted, p)
		}
	}
	ctx := r.Context()
	hctx, cancel := context.WithTimeout(ctx, HandshakeTimeout)
	defer cancel()
	nc := websocket.NetConn(context.WithoutCancel(ctx), ws, websocket.MessageBinary)
	ws.SetReadLimit(wsReadLimit)
	tc := tls.Server(nc, serverConfig(o.Identity(accepted)))
	if err := tc.HandshakeContext(hctx); err != nil {
		ws.CloseNow()
		return nil, nil, fmt.Errorf("nodeproto: handshake with a node: %w", err)
	}
	ch, err := newChannel(tc, ws, o.MaxFrame)
	if err != nil {
		ws.CloseNow()
		return nil, nil, err
	}
	return ch, accepted, nil
}
