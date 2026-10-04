// Package pinnedtls is design 1b: design 1 without the CA. TLS 1.3 runs
// inside the WebSocket as in mtlsws, but both ends present self-signed
// certificates and are identified by their Ed25519 public keys alone: the node
// pins the hub's key, the hub's registry pins the node's. Nothing is issued,
// nothing expires, and no clock is consulted.
package pinnedtls

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"math/big"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"dash.cha.sh/spikes/node-transport/core"
)

const (
	alpn     = "chadash-node/1"
	maxFrame = 16 << 20
)

// Pin is a public key as the other side pins it: the form the architecture
// doc's policy.yaml already shows.
func Pin(pub ed25519.PublicKey) string { return "ed25519:" + base64.RawURLEncoding.EncodeToString(pub) }

// identity is a key and the self-signed certificate that carries it. The
// certificate is only an envelope TLS needs; its dates and signature are
// never checked.
type identity struct {
	cert tls.Certificate
	pin  string
}

func newIdentity(key ed25519.PrivateKey) (identity, error) {
	if key == nil {
		var err error
		if _, key, err = ed25519.GenerateKey(rand.Reader); err != nil {
			return identity{}, err
		}
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "chadash"},
		NotBefore:    time.Unix(0, 0),
		NotAfter:     time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		return identity{}, err
	}
	return identity{cert: tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pin: Pin(key.Public().(ed25519.PublicKey))}, nil
}

// peerKey returns the Ed25519 key a TLS peer proved it holds.
func peerKey(cs tls.ConnectionState) (ed25519.PublicKey, error) {
	if len(cs.PeerCertificates) == 0 {
		return nil, errors.New("no certificate")
	}
	k, ok := cs.PeerCertificates[0].PublicKey.(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("key must be Ed25519")
	}
	return k, nil
}

// Hub is the hub side: its identities, newest last.
type Hub struct {
	mu  sync.Mutex
	ids []identity
}

func NewHub() (*Hub, error) {
	h := &Hub{}
	return h, h.Rotate()
}

func (h *Hub) Rotate() error {
	id, err := newIdentity(nil)
	if err != nil {
		return err
	}
	h.mu.Lock()
	h.ids = append(h.ids, id)
	h.mu.Unlock()
	return nil
}

func (h *Hub) RetireOld() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.ids = h.ids[len(h.ids)-1:]
}

func (h *Hub) Pin() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.ids[len(h.ids)-1].pin
}

// ServerConfig serves the newest identity the node pins.
func (h *Hub) ServerConfig(accepted []string) *tls.Config {
	h.mu.Lock()
	id := h.ids[len(h.ids)-1]
	for _, c := range slices.Backward(h.ids) {
		if slices.Contains(accepted, c.pin) {
			id = c
			break
		}
	}
	h.mu.Unlock()
	return &tls.Config{
		MinVersion:             tls.VersionTLS13,
		Certificates:           []tls.Certificate{id.cert},
		ClientAuth:             tls.RequireAnyClientCert, // proves the key; the registry decides
		NextProtos:             []string{alpn},
		SessionTicketsDisabled: true,
	}
}

func (h *Hub) Handshake(ctx context.Context, ws *websocket.Conn, accepted []string) (core.Channel, core.Peer, error) {
	nc := websocket.NetConn(context.WithoutCancel(ctx), ws, websocket.MessageBinary)
	tc := tls.Server(nc, h.ServerConfig(accepted))
	if err := tc.HandshakeContext(ctx); err != nil {
		nc.Close()
		return nil, core.Peer{}, err
	}
	k, err := peerKey(tc.ConnectionState())
	if err != nil {
		tc.Close()
		return nil, core.Peer{}, err
	}
	return &conn{tc: tc}, core.Peer{Key: k}, nil
}

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
	if len(material) != ed25519.PublicKeySize {
		return nil, nil, errors.New("next key must be an Ed25519 public key")
	}
	return material, nil, nil
}

// Node is the node side: its identity and the pinned hub keys.
type Node struct {
	mu   sync.Mutex
	id   identity
	next *identity
	pins []string
}

func NewNode() (*Node, error) {
	id, err := newIdentity(nil)
	return &Node{id: id}, err
}

// ClientConfig checks the hub's key against the pins, and nothing else.
func (n *Node) ClientConfig() *tls.Config {
	n.mu.Lock()
	defer n.mu.Unlock()
	pins := slices.Clone(n.pins)
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		NextProtos:   []string{alpn},
		Certificates: []tls.Certificate{n.id.cert},
		// No CA, no names, no dates: VerifyConnection checks the pinned key.
		InsecureSkipVerify: true,
		VerifyConnection: func(cs tls.ConnectionState) error {
			k, err := peerKey(cs)
			if err == nil && !slices.Contains(pins, Pin(k)) {
				err = errors.New("hub key not pinned")
			}
			return err
		},
	}
}

func (n *Node) Handshake(ctx context.Context, ws *websocket.Conn) (core.Channel, error) {
	nc := websocket.NetConn(context.WithoutCancel(ctx), ws, websocket.MessageBinary)
	tc := tls.Client(nc, n.ClientConfig())
	if err := tc.HandshakeContext(ctx); err != nil {
		nc.Close()
		return nil, err
	}
	return &conn{tc: tc}, nil
}

func (n *Node) Pins() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return slices.Clone(n.pins)
}

func (n *Node) AddPin(pin string) error {
	if b, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(pin, "ed25519:")); err != nil || len(b) != ed25519.PublicKeySize || !strings.HasPrefix(pin, "ed25519:") {
		return errors.New("malformed hub pin")
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if !slices.Contains(n.pins, pin) {
		n.pins = append(n.pins, pin)
	}
	return nil
}

func (n *Node) EnrollMaterial() ([]byte, error) { return nil, nil }

func (n *Node) Install([]byte) error { return nil }

func (n *Node) BeginRotate() ([]byte, error) {
	id, err := newIdentity(nil)
	if err != nil {
		return nil, err
	}
	n.mu.Lock()
	n.next = &id
	n.mu.Unlock()
	return id.cert.PrivateKey.(ed25519.PrivateKey).Public().(ed25519.PublicKey), nil
}

func (n *Node) CommitRotate([]byte) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.next == nil {
		return errors.New("no rotation in progress")
	}
	n.id, n.next = *n.next, nil
	return nil
}

// conn frames messages on the TLS stream with a 4-byte length.
type conn struct {
	tc  *tls.Conn
	wmu sync.Mutex
}

func (c *conn) Send(ctx context.Context, msg []byte) error {
	if len(msg) > maxFrame {
		return errors.New("frame too large")
	}
	buf := make([]byte, 4+len(msg))
	binary.BigEndian.PutUint32(buf, uint32(len(msg)))
	copy(buf[4:], msg)
	c.wmu.Lock()
	defer c.wmu.Unlock()
	stop := context.AfterFunc(ctx, func() { c.tc.SetWriteDeadline(time.Unix(1, 0)) })
	defer stop()
	_, err := c.tc.Write(buf)
	return err
}

func (c *conn) Recv(ctx context.Context) ([]byte, error) {
	stop := context.AfterFunc(ctx, func() { c.tc.SetReadDeadline(time.Unix(1, 0)) })
	defer stop()
	var hdr [4]byte
	if _, err := io.ReadFull(c.tc, hdr[:]); err != nil {
		return nil, err
	}
	size := binary.BigEndian.Uint32(hdr[:])
	if size > maxFrame {
		return nil, errors.New("frame too large")
	}
	buf := make([]byte, size)
	_, err := io.ReadFull(c.tc, buf)
	return buf, err
}

func (c *conn) Close() error { return c.tc.Close() }

// Test hooks.

// Key returns the node's current public key.
func (n *Node) Key() ed25519.PublicKey {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.id.cert.PrivateKey.(ed25519.PrivateKey).Public().(ed25519.PublicKey)
}

// Clone copies the node's identity, as a restart that reloads it from disk would.
func (n *Node) Clone() *Node {
	n.mu.Lock()
	defer n.mu.Unlock()
	return &Node{id: n.id, pins: slices.Clone(n.pins)}
}

// UnsafeImpersonate returns a node that presents victim's (public)
// certificate with a key of its own.
func UnsafeImpersonate(victim *Node) *Node {
	fake, _ := NewNode()
	v := victim.Clone()
	v.id.cert.PrivateKey = fake.id.cert.PrivateKey
	return v
}
