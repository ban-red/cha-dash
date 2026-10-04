// Package noisews is design 2: a Noise XX handshake (flynn/noise) over
// WebSocket binary messages, with static X25519 keys pinned at enrollment. The
// session is end to end inside the WebSocket, so any proxy that passes
// WebSockets passes it.
package noisews

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"slices"
	"strings"
	"sync"

	"github.com/coder/websocket"
	"github.com/flynn/noise"

	"dash.cha.sh/spikes/node-transport/core"
)

// Noise_XX_25519_ChaChaPoly_BLAKE2s. XX because both sides send their static
// key encrypted, the node checks the hub's against its pins before it sends
// anything of its own, and the hub can answer with whichever key the node
// pins, which keeps rotation simple. The prologue binds the protocol version.
var (
	suite    = noise.NewCipherSuite(noise.DH25519, noise.CipherChaChaPoly, noise.HashBLAKE2s)
	prologue = []byte("chadash-node/1")
)

const (
	// A Noise transport message holds at most 65535 bytes including the
	// 16-byte tag. One byte per chunk says whether more follow.
	chunk    = noise.MaxMsgLen - 16 - 1
	maxFrame = 16 << 20
)

// Pin is a hub static public key, as the node pins it.
func Pin(pub []byte) string { return "x25519:" + base64.RawURLEncoding.EncodeToString(pub) }

func parsePin(pin string) ([]byte, error) {
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(pin, "x25519:"))
	if err != nil || len(b) != 32 || !strings.HasPrefix(pin, "x25519:") {
		return nil, errors.New("malformed hub pin")
	}
	return b, nil
}

// Hub is the hub side: its static keys, newest last.
type Hub struct {
	mu   sync.Mutex
	keys []noise.DHKey
}

func NewHub() (*Hub, error) {
	h := &Hub{}
	return h, h.Rotate()
}

func (h *Hub) Rotate() error {
	k, err := suite.GenerateKeypair(rand.Reader)
	if err != nil {
		return err
	}
	h.mu.Lock()
	h.keys = append(h.keys, k)
	h.mu.Unlock()
	return nil
}

func (h *Hub) RetireOld() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.keys = h.keys[len(h.keys)-1:]
}

func (h *Hub) Pin() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return Pin(h.keys[len(h.keys)-1].Public)
}

func (h *Hub) pick(accepted []string) noise.DHKey {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, k := range slices.Backward(h.keys) {
		if slices.Contains(accepted, Pin(k.Public)) {
			return k
		}
	}
	return h.keys[len(h.keys)-1]
}

// Handshake is the responder side: ← e; → e, ee, s, es; ← s, se.
func (h *Hub) Handshake(ctx context.Context, ws *websocket.Conn, accepted []string) (core.Channel, core.Peer, error) {
	ws.SetReadLimit(noise.MaxMsgLen)
	hs, err := noise.NewHandshakeState(noise.Config{CipherSuite: suite, Pattern: noise.HandshakeXX, Prologue: prologue, StaticKeypair: h.pick(accepted)})
	if err != nil {
		return nil, core.Peer{}, err
	}
	if _, err := readHS(ctx, ws, hs); err != nil {
		return nil, core.Peer{}, err
	}
	if _, _, err := writeHS(ctx, ws, hs); err != nil {
		return nil, core.Peer{}, err
	}
	cs, err := readHS(ctx, ws, hs)
	if err != nil {
		return nil, core.Peer{}, err
	}
	// The responder decrypts with the first cipher state and encrypts with the second.
	return &conn{ws: ws, enc: cs[1], dec: cs[0]}, core.Peer{Key: hs.PeerStatic()}, nil
}

// Noise has no credential beyond the key itself.
func (h *Hub) CheckEnroll(_ core.Peer, material []byte) error {
	if len(material) != 0 {
		return errors.New("unexpected enrollment material")
	}
	return nil
}

func (h *Hub) Issue(string, []byte) ([]byte, error) { return nil, nil }

// NextKey takes the new public key as is: possession is proved when the node
// first connects with it, and until then the hub keeps accepting the old one.
func (h *Hub) NextKey(_ string, material []byte) ([]byte, []byte, error) {
	if len(material) != 32 {
		return nil, nil, errors.New("next key must be 32 bytes")
	}
	return material, nil, nil
}

// Node is the node side: its static key and the pinned hub keys.
type Node struct {
	mu   sync.Mutex
	key  noise.DHKey
	next *noise.DHKey
	pins [][]byte
}

func NewNode() (*Node, error) {
	k, err := suite.GenerateKeypair(rand.Reader)
	return &Node{key: k}, err
}

// Handshake is the initiator side. It checks the hub's static key against the
// pins after the second message, before sending its own key or any data.
func (n *Node) Handshake(ctx context.Context, ws *websocket.Conn) (core.Channel, error) {
	ws.SetReadLimit(noise.MaxMsgLen)
	n.mu.Lock()
	key, pins := n.key, slices.Clone(n.pins)
	n.mu.Unlock()
	hs, err := noise.NewHandshakeState(noise.Config{CipherSuite: suite, Pattern: noise.HandshakeXX, Initiator: true, Prologue: prologue, StaticKeypair: key})
	if err != nil {
		return nil, err
	}
	if _, _, err := writeHS(ctx, ws, hs); err != nil {
		return nil, err
	}
	if _, err := readHS(ctx, ws, hs); err != nil {
		return nil, err
	}
	if !slices.ContainsFunc(pins, func(p []byte) bool { return string(p) == string(hs.PeerStatic()) }) {
		return nil, errors.New("hub key not pinned")
	}
	enc, dec, err := writeHS(ctx, ws, hs)
	if err != nil {
		return nil, err
	}
	return &conn{ws: ws, enc: enc, dec: dec}, nil
}

func (n *Node) Pins() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]string, len(n.pins))
	for i, p := range n.pins {
		out[i] = Pin(p)
	}
	return out
}

func (n *Node) AddPin(pin string) error {
	b, err := parsePin(pin)
	if err != nil {
		return err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if !slices.ContainsFunc(n.pins, func(p []byte) bool { return string(p) == string(b) }) {
		n.pins = append(n.pins, b)
	}
	return nil
}

func (n *Node) EnrollMaterial() ([]byte, error) { return nil, nil }

func (n *Node) Install([]byte) error { return nil }

func (n *Node) BeginRotate() ([]byte, error) {
	k, err := suite.GenerateKeypair(rand.Reader)
	if err != nil {
		return nil, err
	}
	n.mu.Lock()
	n.next = &k
	n.mu.Unlock()
	return k.Public, nil
}

func (n *Node) CommitRotate([]byte) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.next == nil {
		return errors.New("no rotation in progress")
	}
	n.key, n.next = *n.next, nil
	return nil
}

func writeHS(ctx context.Context, ws *websocket.Conn, hs *noise.HandshakeState) (*noise.CipherState, *noise.CipherState, error) {
	msg, c1, c2, err := hs.WriteMessage(nil, nil)
	if err != nil {
		return nil, nil, err
	}
	return c1, c2, ws.Write(ctx, websocket.MessageBinary, msg)
}

func readHS(ctx context.Context, ws *websocket.Conn, hs *noise.HandshakeState) ([2]*noise.CipherState, error) {
	typ, msg, err := ws.Read(ctx)
	if err != nil {
		return [2]*noise.CipherState{}, err
	}
	if typ != websocket.MessageBinary {
		return [2]*noise.CipherState{}, errors.New("handshake message must be binary")
	}
	_, c1, c2, err := hs.ReadMessage(nil, msg)
	return [2]*noise.CipherState{c1, c2}, err
}

// conn carries frames as Noise transport messages, one or more per frame.
// Encrypting and writing share one lock: the nonce is implicit, so messages
// must reach the wire in the order they were encrypted.
type conn struct {
	ws  *websocket.Conn
	wmu sync.Mutex
	enc *noise.CipherState
	dec *noise.CipherState // used only by the single reader
}

func (c *conn) Send(ctx context.Context, msg []byte) error {
	if len(msg) > maxFrame {
		return errors.New("frame too large")
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	for first := true; first || len(msg) > 0; first = false {
		n := min(len(msg), chunk)
		more := byte(0)
		if n < len(msg) {
			more = 1
		}
		ct, err := c.enc.Encrypt(nil, nil, append([]byte{more}, msg[:n]...))
		if err != nil {
			return err
		}
		if err := c.ws.Write(ctx, websocket.MessageBinary, ct); err != nil {
			return err
		}
		msg = msg[n:]
	}
	return nil
}

func (c *conn) Recv(ctx context.Context) ([]byte, error) {
	var out []byte
	for {
		typ, ct, err := c.ws.Read(ctx)
		if err != nil {
			return nil, err
		}
		if typ != websocket.MessageBinary {
			return nil, errors.New("unexpected text message")
		}
		pt, err := c.dec.Decrypt(nil, nil, ct)
		if err != nil || len(pt) == 0 {
			c.ws.Close(websocket.StatusPolicyViolation, "bad message")
			return nil, errors.New("message failed authentication")
		}
		out = append(out, pt[1:]...)
		if len(out) > maxFrame {
			return nil, errors.New("frame too large")
		}
		if pt[0] == 0 {
			return out, nil
		}
	}
}

func (c *conn) Close() error { return c.ws.Close(websocket.StatusNormalClosure, "") }

// Test hooks.

// Key returns the node's current public key.
func (n *Node) Key() []byte {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.key.Public
}

// Clone copies the node's identity, as a restart that reloads it from disk would.
func (n *Node) Clone() *Node {
	n.mu.Lock()
	defer n.mu.Unlock()
	return &Node{key: n.key, pins: slices.Clone(n.pins)}
}

// UnsafeImpersonate returns a node that claims victim's public key without
// its private key, as an attacker who read the key from the hub's UI would.
func UnsafeImpersonate(victim *Node) *Node {
	fake, _ := NewNode()
	v := victim.Clone()
	v.key.Private = fake.key.Private
	return v
}
