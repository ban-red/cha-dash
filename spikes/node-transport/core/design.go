// Package core is the half of the node channel that doesn't depend on the
// handshake: join tokens, the node registry, RPC framing, and the hub and node
// connection loops. Each design (mtlsws, noisews) plugs in a HubCrypto and a
// NodeCrypto, so the two prototypes share everything except identity and
// encryption, and the line counts compare only what differs.
package core

import (
	"context"

	"github.com/coder/websocket"
)

// Channel is an authenticated, encrypted, ordered message pipe between the hub
// and one node, carried inside a WebSocket.
type Channel interface {
	Send(ctx context.Context, msg []byte) error
	Recv(ctx context.Context) ([]byte, error)
	Close() error
}

// Peer is what a handshake proved about the node: possession of the private
// half of Key.
type Peer struct {
	// Key is the node's public key, the identity the registry pins.
	Key []byte
	// NeedsCredential is set when the node didn't present a hub-issued
	// credential the hub still accepts (mTLS: a self-signed bootstrap
	// certificate, or one that expired or came from a retired CA). An approved
	// node in that state is sent a fresh credential. Noise never sets it.
	NeedsCredential bool
}

// HubCrypto is one design's hub side.
type HubCrypto interface {
	// Pin is the pin of the hub's newest identity, as join tokens and pin
	// pushes carry it.
	Pin() string
	// Handshake runs the hub side of the handshake on a fresh WebSocket.
	// accepted lists the hub pins the node trusts; the hub answers with the
	// newest identity among them, so nodes that missed a rotation still connect.
	Handshake(ctx context.Context, ws *websocket.Conn, accepted []string) (Channel, Peer, error)
	// CheckEnroll validates the material a node sends with its join token
	// (mTLS: a CSR for the key it just proved; Noise: nothing).
	CheckEnroll(p Peer, material []byte) error
	// Issue returns the credential an approved node needs (mTLS: a client
	// certificate), or nil.
	Issue(nodeID string, key []byte) ([]byte, error)
	// NextKey validates a node's key-rotation material and returns the new
	// public key and its credential.
	NextKey(nodeID string, material []byte) (key, cred []byte, err error)
	// Rotate makes a new hub identity the newest. RetireOld drops the rest.
	Rotate() error
	RetireOld()
}

// NodeCrypto is one design's node side.
type NodeCrypto interface {
	// Handshake runs the node side and fails unless the hub proves an identity
	// whose pin the node holds. Nothing is sent after a failed check.
	Handshake(ctx context.Context, ws *websocket.Conn) (Channel, error)
	Pins() []string
	AddPin(pin string) error
	// EnrollMaterial is sent with the join token.
	EnrollMaterial() ([]byte, error)
	// Install stores the credential the hub issued at approval.
	Install(cred []byte) error
	// BeginRotate makes the next key and returns what the hub needs to pin
	// it. CommitRotate switches to it once the hub has answered.
	BeginRotate() ([]byte, error)
	CommitRotate(cred []byte) error
}

// PinsHeader carries the hub pins a node accepts on the WebSocket upgrade.
// Pins are public, so a proxy seeing them learns nothing it can use.
const PinsHeader = "Chadash-Hub-Pins"
