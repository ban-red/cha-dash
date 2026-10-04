package core

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// ErrReconnect means the hub just issued this node its credential: dial again.
var ErrReconnect = errors.New("approved: reconnect with the new credential")

// RefusedError is the hub turning the node away (revoked, unknown, bad token).
type RefusedError struct{ Reason string }

func (e *RefusedError) Error() string { return "hub refused: " + e.Reason }

// Node is the chadash-node end of the channel. It always dials out.
type Node struct {
	Crypto     NodeCrypto
	URL        string       // ws://chadash:3696/node or wss://dash.example/node
	Handler    Handler      // hub → node verbs (container.restart, logs.tail, …)
	HTTPClient *http.Client // the outer connection: proxies, the proxy's CA

	mu    sync.Mutex
	token string // join token, until the hub has spent it
	id    string
	sess  *Session
}

// NewNode prepares a node. A join token's pin becomes the node's first pinned
// hub identity.
func NewNode(c NodeCrypto, url, token string) (*Node, error) {
	if token != "" {
		_, pin, err := ParseToken(token)
		if err != nil {
			return nil, err
		}
		if err := c.AddPin(pin); err != nil {
			return nil, err
		}
	}
	return &Node{Crypto: c, URL: url, token: token}, nil
}

// Resume sets the node ID saved by an earlier run, so the node doesn't try to
// enroll again.
func (n *Node) Resume(id string) { n.setID(id) }

// ID is the node ID the hub assigned, once enrolled.
func (n *Node) ID() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.id
}

// Dial connects once. After "welcome" it returns the running session, which
// lives until ctx ends or either side closes it. While pending it blocks until
// an admin decides. ErrReconnect means dial again; *RefusedError means stop.
func (n *Node) Dial(ctx context.Context) (*Session, error) {
	ws, _, err := websocket.Dial(ctx, n.URL, &websocket.DialOptions{
		HTTPClient: n.HTTPClient,
		HTTPHeader: http.Header{PinsHeader: {strings.Join(n.Crypto.Pins(), ",")}},
	})
	if err != nil {
		return nil, err
	}
	ch, err := n.Crypto.Handshake(ctx, ws)
	if err != nil {
		ws.Close(websocket.StatusPolicyViolation, "hub identity not pinned")
		return nil, err
	}
	n.mu.Lock()
	id, token := n.id, n.token
	n.mu.Unlock()
	hello := helloMsg{V: 1}
	if id == "" {
		if token == "" {
			ch.Close()
			return nil, errors.New("not enrolled and no join token")
		}
		hello.Token = token
		if hello.Material, err = n.Crypto.EnrollMaterial(); err != nil {
			ch.Close()
			return nil, err
		}
	}
	if err := sendFrame(ctx, ch, Frame{Op: "hello"}, hello); err != nil {
		ch.Close()
		return nil, err
	}
	for {
		f, err := recvFrame(ctx, ch)
		if err != nil {
			ch.Close()
			return nil, err
		}
		switch f.Op {
		case "pending":
			var m nodeMsg
			json.Unmarshal(f.Data, &m)
			n.setID(m.Node) // the hub knows this key now; the token is spent
		case "approved":
			var m approvedMsg
			json.Unmarshal(f.Data, &m)
			ch.Close()
			if err := n.Crypto.Install(m.Cred); err != nil {
				return nil, err
			}
			n.setID(m.Node)
			return nil, ErrReconnect
		case "welcome":
			var m nodeMsg
			json.Unmarshal(f.Data, &m)
			n.setID(m.Node)
			s := NewSession(ch, n.handle)
			n.mu.Lock()
			n.sess = s
			n.mu.Unlock()
			go s.Run(ctx)
			return s, nil
		case "refused":
			ch.Close()
			return nil, &RefusedError{f.Error}
		default:
			ch.Close()
			return nil, errors.New("unexpected frame " + f.Op)
		}
	}
}

func (n *Node) setID(id string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.id, n.token = id, ""
}

// Run keeps the node connected, with capped exponential backoff, until ctx
// ends or the hub refuses it.
func (n *Node) Run(ctx context.Context) error {
	backoff := 100 * time.Millisecond
	for {
		s, err := n.Dial(ctx)
		var refused *RefusedError
		switch {
		case err == nil:
			<-s.Done()
			backoff = 100 * time.Millisecond
		case errors.Is(err, ErrReconnect):
			continue
		case errors.As(err, &refused):
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

// RotateKey replaces the node's key over the live session. The hub accepts
// both keys until it first sees the new one, so a crash between the two steps
// leaves the node on its old key, still connected.
func (n *Node) RotateKey(ctx context.Context) error {
	n.mu.Lock()
	s := n.sess
	n.mu.Unlock()
	if s == nil {
		return ErrNotConnected
	}
	mat, err := n.Crypto.BeginRotate()
	if err != nil {
		return err
	}
	res, err := s.Call(ctx, "node.rotate", rotateMsg{Material: mat}, nil)
	if err != nil {
		return err
	}
	var m credMsg
	if err := json.Unmarshal(res, &m); err != nil {
		return err
	}
	return n.Crypto.CommitRotate(m.Cred)
}

// handle serves hub → node calls. hub.pin arrives over a channel the current
// pin authenticates; the real node lets local policy forbid it.
func (n *Node) handle(ctx context.Context, method string, params json.RawMessage, emit func(any) error) (any, error) {
	if method == "hub.pin" {
		var m pinMsg
		json.Unmarshal(params, &m)
		return struct{}{}, n.Crypto.AddPin(m.Pin)
	}
	if n.Handler != nil {
		return n.Handler(ctx, method, params, emit)
	}
	return nil, errors.New("unknown method")
}
