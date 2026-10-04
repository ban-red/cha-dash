package core

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Event is something the hub or node did, for tests and logs.
type Event struct {
	Kind string // handshake_failed, refused, enrolled, pending, approved, session, session_ended, node_rotating
	Node string
	Err  error
}

// ErrNotConnected is returned by Hub.Call for a node without a live session.
var ErrNotConnected = errors.New("node not connected")

// Hub serves the /node WebSocket endpoint.
type Hub struct {
	Crypto           HubCrypto
	Reg              *Registry
	Handler          Handler // node → hub verbs beyond the built-in ones
	OnEvent          func(Event)
	HandshakeTimeout time.Duration // 10 s

	mu   sync.Mutex
	live map[string]*Session
}

func NewHub(c HubCrypto) *Hub {
	return &Hub{Crypto: c, Reg: NewRegistry(), live: map[string]*Session{}}
}

// JoinToken mints a single-use token for "Add node". It carries the hub's pin,
// so the node authenticates the hub before it reveals the secret: enrollment
// isn't trust-on-first-use.
func (h *Hub) JoinToken(ttl time.Duration) string {
	return "cdj_" + h.Reg.MintToken(ttl) + "." + h.Crypto.Pin()
}

// ParseToken splits a join token into its secret and the hub pin.
func ParseToken(tok string) (secret, pin string, err error) {
	rest, ok := strings.CutPrefix(tok, "cdj_")
	secret, pin, ok2 := strings.Cut(rest, ".")
	if !ok || !ok2 || secret == "" || pin == "" {
		return "", "", errors.New("malformed join token")
	}
	return secret, pin, nil
}

func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ws, err := websocket.Accept(w, r, nil) // nodes send no Origin; browsers can't reach this without a session anyway
	if err != nil {
		return
	}
	ctx := r.Context()
	accepted := strings.Split(r.Header.Get(PinsHeader), ",")
	hctx, cancel := context.WithTimeout(ctx, cmp(h.HandshakeTimeout, 10*time.Second))
	ch, peer, err := h.Crypto.Handshake(hctx, ws, accepted)
	cancel()
	if err != nil {
		h.emit(Event{Kind: "handshake_failed", Err: err})
		ws.Close(websocket.StatusPolicyViolation, "handshake failed")
		return
	}
	defer ch.Close()

	// The node speaks first, so a silent peer can't hold the slot open.
	rctx, cancel := context.WithTimeout(ctx, cmp(h.HandshakeTimeout, 10*time.Second))
	f, err := recvFrame(rctx, ch)
	cancel()
	if err != nil || f.Op != "hello" {
		h.refuse(ctx, ch, "", "expected hello")
		return
	}
	var hello helloMsg
	json.Unmarshal(f.Data, &hello)

	n, ok := h.Reg.Lookup(peer.Key)
	switch {
	case !ok && hello.Token == "":
		h.refuse(ctx, ch, "", "unknown node")
	case !ok:
		h.enroll(ctx, ch, peer, hello)
	case n.State == Revoked:
		h.refuse(ctx, ch, n.ID, "revoked")
	case n.State == Pending:
		h.awaitApproval(ctx, ch, n.ID)
	case peer.NeedsCredential:
		h.sendCredential(ctx, ch, n)
	default:
		h.serve(ctx, ch, n.ID, accepted)
	}
}

// enroll handles a key the hub has never seen, presenting a join token.
func (h *Hub) enroll(ctx context.Context, ch Channel, peer Peer, m helloMsg) {
	secret, _, err := ParseToken(m.Token)
	if err != nil {
		h.refuse(ctx, ch, "", ErrBadToken.Error())
		return
	}
	// Check the material before spending the token, so a bad CSR doesn't burn it.
	if err := h.Crypto.CheckEnroll(peer, m.Material); err != nil {
		h.refuse(ctx, ch, "", "bad enrollment material: "+err.Error())
		return
	}
	n, err := h.Reg.Enroll(secret, peer.Key)
	if err != nil {
		h.refuse(ctx, ch, "", err.Error())
		return
	}
	h.emit(Event{Kind: "enrolled", Node: n.ID})
	h.awaitApproval(ctx, ch, n.ID)
}

// awaitApproval holds a pending node's connection until an admin decides.
func (h *Hub) awaitApproval(ctx context.Context, ch Channel, id string) {
	if sendFrame(ctx, ch, Frame{Op: "pending"}, nodeMsg{Node: id}) != nil {
		return
	}
	h.emit(Event{Kind: "pending", Node: id})
	gone := make(chan struct{})
	go func() { recvFrame(ctx, ch); close(gone) }() // a pending node has nothing to say; any read result means it left
	for {
		changed := h.Reg.Changed()
		n, ok := h.Reg.Get(id)
		switch {
		case !ok || n.State == Revoked:
			h.refuse(ctx, ch, id, "revoked")
			return
		case n.State == Approved:
			h.sendCredential(ctx, ch, n)
			return
		}
		select {
		case <-changed:
		case <-gone:
			return
		case <-ctx.Done():
			return
		}
	}
}

// sendCredential gives an approved node what it needs and ends the
// connection; the node reconnects with it.
func (h *Hub) sendCredential(ctx context.Context, ch Channel, n Record) {
	cred, err := h.Crypto.Issue(n.ID, n.Key)
	if err != nil {
		h.refuse(ctx, ch, n.ID, "can't issue credential")
		return
	}
	if sendFrame(ctx, ch, Frame{Op: "approved"}, approvedMsg{Node: n.ID, Cred: cred}) == nil {
		h.emit(Event{Kind: "approved", Node: n.ID})
	}
}

func (h *Hub) serve(ctx context.Context, ch Channel, id string, accepted []string) {
	s := NewSession(ch, h.handlerFor(id))
	h.mu.Lock()
	if old := h.live[id]; old != nil {
		old.ch.Close() // one session per node; the newest wins
	}
	h.live[id] = s
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		if h.live[id] == s {
			delete(h.live, id)
		}
		h.mu.Unlock()
	}()
	// Revoke may have run between Lookup and registering the session.
	if n, ok := h.Reg.Get(id); !ok || n.State != Approved {
		h.refuse(ctx, ch, id, "revoked")
		return
	}
	h.emit(Event{Kind: "session", Node: id})
	if err := sendFrame(ctx, ch, Frame{Op: "welcome"}, nodeMsg{Node: id}); err != nil {
		h.emit(Event{Kind: "session_ended", Node: id, Err: err})
		return
	}
	// A node that doesn't know the hub's newest identity yet learns it now.
	if pin := h.Crypto.Pin(); !slices.Contains(accepted, pin) {
		go s.Call(ctx, "hub.pin", pinMsg{Pin: pin}, nil)
	}
	err := s.Run(ctx)
	h.emit(Event{Kind: "session_ended", Node: id, Err: err})
}

func (h *Hub) handlerFor(id string) Handler {
	return func(ctx context.Context, method string, params json.RawMessage, emit func(any) error) (any, error) {
		if method == "node.rotate" {
			var m rotateMsg
			json.Unmarshal(params, &m)
			key, cred, err := h.Crypto.NextKey(id, m.Material)
			if err != nil {
				return nil, err
			}
			if err := h.Reg.setNext(id, key); err != nil {
				return nil, err
			}
			h.emit(Event{Kind: "node_rotating", Node: id})
			return credMsg{Cred: cred}, nil
		}
		if h.Handler != nil {
			return h.Handler(ctx, method, params, emit)
		}
		return nil, errors.New("unknown method")
	}
}

func (h *Hub) refuse(ctx context.Context, ch Channel, id, reason string) {
	sendFrame(ctx, ch, Frame{Op: "refused", Error: reason}, nil)
	h.emit(Event{Kind: "refused", Node: id, Err: errors.New(reason)})
}

func (h *Hub) session(id string) *Session {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.live[id]
}

// Connected reports whether a node has a live session.
func (h *Hub) Connected(id string) bool { return h.session(id) != nil }

// Call invokes a verb on a connected node.
func (h *Hub) Call(ctx context.Context, id, method string, params any, onItem func(json.RawMessage)) (json.RawMessage, error) {
	s := h.session(id)
	if s == nil {
		return nil, ErrNotConnected
	}
	return s.Call(ctx, method, params, onItem)
}

// Approve is the admin approving a pending node in the Inbox.
func (h *Hub) Approve(id string) error { return h.Reg.approve(id) }

// Revoke cuts a node off now and for good.
func (h *Hub) Revoke(ctx context.Context, id string) error {
	if err := h.Reg.revoke(id); err != nil {
		return err
	}
	if s := h.session(id); s != nil {
		s.Refuse(ctx, "revoked")
	}
	return nil
}

// RotateKey makes a new hub identity and pushes its pin to every connected
// node over the channel the old identity authenticates. The push is best
// effort: a node that misses it (offline, or its session just ended) gets it
// on its next connection, which the hub still answers with the old identity
// until RetireOldKeys. It returns how many nodes took the new pin.
func (h *Hub) RotateKey(ctx context.Context) (int, error) {
	if err := h.Crypto.Rotate(); err != nil {
		return 0, err
	}
	pin := h.Crypto.Pin()
	h.mu.Lock()
	live := make([]*Session, 0, len(h.live))
	for _, s := range h.live {
		live = append(live, s)
	}
	h.mu.Unlock()
	took := 0
	for _, s := range live {
		if _, err := s.Call(ctx, "hub.pin", pinMsg{Pin: pin}, nil); err == nil {
			took++
		}
	}
	return took, nil
}

// RetireOldKeys drops every hub identity but the newest.
func (h *Hub) RetireOldKeys() { h.Crypto.RetireOld() }

func (h *Hub) emit(e Event) {
	if h.OnEvent != nil {
		h.OnEvent(e)
	}
}

func cmp(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}

type nodeMsg struct {
	Node string `json:"node"`
}

type approvedMsg struct {
	Node string `json:"node"`
	Cred []byte `json:"cred,omitempty"`
}

// helloMsg is the node's first frame. Token and Material are set only by a
// node that isn't enrolled yet.
type helloMsg struct {
	V        int    `json:"v"`
	Token    string `json:"token,omitempty"`
	Material []byte `json:"material,omitempty"`
}

type pinMsg struct {
	Pin string `json:"pin"`
}

type rotateMsg struct {
	Material []byte `json:"material"`
}

type credMsg struct {
	Cred []byte `json:"cred,omitempty"`
}
