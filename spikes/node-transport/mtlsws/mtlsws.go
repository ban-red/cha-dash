// Package mtlsws is design 1: the hub runs a small certificate authority, nodes
// get client certificates at approval, and the node pins the CA. The TLS 1.3
// session runs inside the WebSocket (TLS in WebSocket), not on the outer
// connection, so it survives reverse proxies that terminate the outer TLS.
package mtlsws

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
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
	// HubName is the name in the hub's inner certificate. The inner session
	// is end to end, so it never needs the public hostname the proxy serves;
	// .invalid is reserved and can't collide with a real one.
	HubName  = "hub.chadash.invalid"
	alpn     = "chadash-node/1"
	maxFrame = 16 << 20
)

// Pin is a CA's pin: the SHA-256 of its SubjectPublicKeyInfo.
func Pin(c *x509.Certificate) string {
	h := sha256.Sum256(c.RawSubjectPublicKeyInfo)
	return "sha256:" + base64.RawURLEncoding.EncodeToString(h[:])
}

// identity is one hub CA and the leaf it serves with.
type identity struct {
	caKey ed25519.PrivateKey
	ca    *x509.Certificate
	leaf  tls.Certificate
	pin   string
}

// Hub is the hub side: CAs (newest last) and node certificate issuing.
type Hub struct {
	Now     func() time.Time
	CATTL   time.Duration // 20 years
	LeafTTL time.Duration // 1 year
	NodeTTL time.Duration // 1 year

	mu  sync.Mutex
	ids []*identity
}

func NewHub() (*Hub, error) {
	h := &Hub{}
	return h, h.Rotate()
}

func (h *Hub) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

func ttl(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}

// Rotate creates a new CA and leaf; the CA's pin becomes the newest.
func (h *Hub) Rotate() error {
	_, caKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	now := h.now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: "Cha Dash node CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(ttl(h.CATTL, 20*365*24*time.Hour)),
		IsCA:                  true,
		BasicConstraintsValid: true,
		MaxPathLenZero:        true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, caKey.Public(), caKey)
	if err != nil {
		return err
	}
	ca, err := x509.ParseCertificate(der)
	if err != nil {
		return err
	}
	id := &identity{caKey: caKey, ca: ca, pin: Pin(ca)}
	if err := h.newLeaf(id); err != nil {
		return err
	}
	h.mu.Lock()
	h.ids = append(h.ids, id)
	h.mu.Unlock()
	return nil
}

// RotateLeaf replaces the newest CA's serving key. Nodes pin the CA, so they
// don't notice: this is the rotation a CA makes free.
func (h *Hub) RotateLeaf() error {
	h.mu.Lock()
	id := h.ids[len(h.ids)-1]
	h.mu.Unlock()
	return h.newLeaf(id)
}

func (h *Hub) newLeaf(id *identity) error {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	now := h.now()
	tmpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: HubName},
		DNSNames:     []string{HubName},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(ttl(h.LeafTTL, 365*24*time.Hour)),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, id.ca, key.Public(), id.caKey)
	if err != nil {
		return err
	}
	h.mu.Lock()
	id.leaf = tls.Certificate{Certificate: [][]byte{der, id.ca.Raw}, PrivateKey: key}
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

// ServerConfig is the inner TLS config, serving the newest CA the node pins.
func (h *Hub) ServerConfig(accepted []string) *tls.Config {
	h.mu.Lock()
	id := h.ids[len(h.ids)-1]
	for _, c := range slices.Backward(h.ids) {
		if slices.Contains(accepted, c.pin) {
			id = c
			break
		}
	}
	leaf := id.leaf
	h.mu.Unlock()
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{leaf},
		// Any certificate: self-signed before approval, ours after. The
		// handshake proves the key; the registry decides what it may do.
		ClientAuth:             tls.RequireAnyClientCert,
		NextProtos:             []string{alpn},
		SessionTicketsDisabled: true,
		Time:                   h.Now,
	}
}

func (h *Hub) Handshake(ctx context.Context, ws *websocket.Conn, accepted []string) (core.Channel, core.Peer, error) {
	nc := websocket.NetConn(context.WithoutCancel(ctx), ws, websocket.MessageBinary)
	tc := tls.Server(nc, h.ServerConfig(accepted))
	if err := tc.HandshakeContext(ctx); err != nil {
		nc.Close()
		return nil, core.Peer{}, err
	}
	cert := tc.ConnectionState().PeerCertificates[0]
	pub, ok := cert.PublicKey.(ed25519.PublicKey)
	if !ok {
		tc.Close()
		return nil, core.Peer{}, errors.New("node key must be Ed25519")
	}
	return &conn{tc: tc}, core.Peer{Key: pub, NeedsCredential: !h.issued(cert)}, nil
}

// issued reports whether cert is a valid client certificate from one of the
// hub's CAs. Revocation isn't checked here: the registry is the CRL.
func (h *Hub) issued(cert *x509.Certificate) bool {
	pool := x509.NewCertPool()
	h.mu.Lock()
	for _, id := range h.ids {
		pool.AddCert(id.ca)
	}
	h.mu.Unlock()
	_, err := cert.Verify(x509.VerifyOptions{Roots: pool, CurrentTime: h.now(), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}})
	return err == nil
}

// CheckEnroll requires a CSR for the key the node just proved in the handshake.
func (h *Hub) CheckEnroll(p core.Peer, material []byte) error {
	key, err := csrKey(material)
	if err != nil {
		return err
	}
	if !key.Equal(ed25519.PublicKey(p.Key)) {
		return errors.New("CSR key differs from the handshake key")
	}
	return nil
}

// Issue signs a client certificate for an approved node. Only the key comes
// from the CSR; the hub chooses the subject and lifetime.
func (h *Hub) Issue(nodeID string, key []byte) ([]byte, error) {
	h.mu.Lock()
	id := h.ids[len(h.ids)-1]
	h.mu.Unlock()
	now := h.now()
	tmpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: nodeID},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(ttl(h.NodeTTL, 365*24*time.Hour)),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	return x509.CreateCertificate(rand.Reader, tmpl, id.ca, ed25519.PublicKey(key), id.caKey)
}

func (h *Hub) NextKey(nodeID string, material []byte) ([]byte, []byte, error) {
	key, err := csrKey(material)
	if err != nil {
		return nil, nil, err
	}
	cert, err := h.Issue(nodeID, key)
	return key, cert, err
}

func csrKey(der []byte) (ed25519.PublicKey, error) {
	csr, err := x509.ParseCertificateRequest(der)
	if err != nil {
		return nil, err
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, err
	}
	key, ok := csr.PublicKey.(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("CSR key must be Ed25519")
	}
	return key, nil
}

// Node is the node side: an Ed25519 key, the certificate the hub issued, and
// the pinned hub CAs.
type Node struct {
	Now func() time.Time

	mu   sync.Mutex
	key  ed25519.PrivateKey
	cert []byte // hub-issued; nil until approved
	next ed25519.PrivateKey
	pins []string
}

func NewNode() (*Node, error) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	return &Node{key: key}, err
}

func (n *Node) now() time.Time {
	if n.Now != nil {
		return n.Now()
	}
	return time.Now()
}

// ClientConfig is the inner TLS config. Before approval the node presents a
// self-signed certificate, which proves its key the same way a CSR does.
func (n *Node) ClientConfig() (*tls.Config, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	der := n.cert
	if der == nil {
		var err error
		if der, err = selfSigned(n.key); err != nil {
			return nil, err
		}
	}
	pins := slices.Clone(n.pins)
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		ServerName:   HubName,
		NextProtos:   []string{alpn},
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: n.key}},
		// The system roots mean nothing here: VerifyConnection checks the
		// hub's chain against the pinned CAs instead.
		InsecureSkipVerify: true,
		VerifyConnection:   func(cs tls.ConnectionState) error { return verifyHub(cs.PeerCertificates, pins, n.now()) },
		Time:               n.Now,
	}, nil
}

var errPin = errors.New("hub CA not pinned")

func verifyHub(chain []*x509.Certificate, pins []string, now time.Time) error {
	if len(chain) < 2 {
		return errors.New("hub sent no CA certificate")
	}
	roots := x509.NewCertPool()
	found := false
	for _, c := range chain[1:] {
		if slices.Contains(pins, Pin(c)) {
			roots.AddCert(c)
			found = true
		}
	}
	if !found {
		return errPin
	}
	_, err := chain[0].Verify(x509.VerifyOptions{Roots: roots, DNSName: HubName, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}})
	return err
}

func (n *Node) Handshake(ctx context.Context, ws *websocket.Conn) (core.Channel, error) {
	cfg, err := n.ClientConfig()
	if err != nil {
		return nil, err
	}
	nc := websocket.NetConn(context.WithoutCancel(ctx), ws, websocket.MessageBinary)
	tc := tls.Client(nc, cfg)
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
	if b, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(pin, "sha256:")); err != nil || len(b) != 32 || !strings.HasPrefix(pin, "sha256:") {
		return errors.New("malformed hub pin")
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if !slices.Contains(n.pins, pin) {
		n.pins = append(n.pins, pin)
	}
	return nil
}

func (n *Node) EnrollMaterial() ([]byte, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "chadash-node"}}, n.key)
}

func (n *Node) Install(cred []byte) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.installLocked(cred, n.key)
}

func (n *Node) installLocked(cred []byte, key ed25519.PrivateKey) error {
	c, err := x509.ParseCertificate(cred)
	if err != nil {
		return err
	}
	if pub, ok := c.PublicKey.(ed25519.PublicKey); !ok || !pub.Equal(key.Public()) {
		return errors.New("issued certificate is for another key")
	}
	n.key, n.cert = key, cred
	return nil
}

func (n *Node) BeginRotate() ([]byte, error) {
	_, next, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	n.mu.Lock()
	n.next = next
	n.mu.Unlock()
	return x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "chadash-node"}}, next)
}

func (n *Node) CommitRotate(cred []byte) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.next == nil {
		return errors.New("no rotation in progress")
	}
	if err := n.installLocked(cred, n.next); err != nil {
		return err
	}
	n.next = nil
	return nil
}

func selfSigned(key ed25519.PrivateKey) ([]byte, error) {
	tmpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: "chadash-node bootstrap"},
		NotBefore:    time.Unix(0, 0),
		NotAfter:     time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	return x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
}

func serial() *big.Int {
	n, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	return n
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

// ConnectionState exposes the inner TLS state (for tests: the key exchange).
func (c *conn) ConnectionState() tls.ConnectionState { return c.tc.ConnectionState() }

// Test hooks.

// Key returns the node's current public key.
func (n *Node) Key() ed25519.PublicKey {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.key.Public().(ed25519.PublicKey)
}

// Clone copies the node's identity, as a restart that reloads it from disk would.
func (n *Node) Clone() *Node {
	n.mu.Lock()
	defer n.mu.Unlock()
	return &Node{Now: n.Now, key: n.key, cert: n.cert, pins: slices.Clone(n.pins)}
}

// Cert returns the node's certificate, which is public.
func (n *Node) Cert() []byte {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.cert
}

// UnsafeSetCert makes the node present someone else's certificate with its
// own key, as an attacker who copied a node's public certificate would.
func (n *Node) UnsafeSetCert(der []byte) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.cert = der
}
