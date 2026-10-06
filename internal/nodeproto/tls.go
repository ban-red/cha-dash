package nodeproto

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Protocol versions, negotiated with ALPN inside the WebSocket: the node
// offers every version it speaks, the hub answers with the newest it shares,
// and a handshake with nothing in common fails before any frame is sent.
// Versions lists the ones this build speaks, newest first.
var Versions = []int{1}

const alpnPrefix = "chadash-node/"

func alpn(versions []int) []string {
	out := make([]string, len(versions))
	for i, v := range versions {
		out[i] = alpnPrefix + strconv.Itoa(v)
	}
	return out
}

func versionOf(proto string) (int, error) {
	v, err := strconv.Atoi(strings.TrimPrefix(proto, alpnPrefix))
	if err != nil || !strings.HasPrefix(proto, alpnPrefix) || !slices.Contains(Versions, v) {
		return 0, fmt.Errorf("nodeproto: no protocol version in common (got %q)", proto)
	}
	return v, nil
}

// Identity is a key and the self-signed certificate that carries it. The
// certificate is only the envelope TLS needs: its names, dates and
// signature are never checked by either side, so no clock is consulted and
// nothing expires (ADR-0014).
type Identity struct {
	Key  ed25519.PrivateKey
	Pin  string
	cert tls.Certificate
}

// NewIdentity wraps key in a certificate.
func NewIdentity(key ed25519.PrivateKey) (Identity, error) {
	if len(key) != ed25519.PrivateKeySize {
		return Identity{}, errors.New("nodeproto: an identity needs an Ed25519 key")
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "chadash"},
		NotBefore:    time.Unix(0, 0),
		NotAfter:     time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		return Identity{}, err
	}
	return Identity{Key: key, Pin: PinOf(key), cert: tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}}, nil
}

// peerPin returns the pin of the Ed25519 key the TLS peer proved it holds
// (TLS 1.3's CertificateVerify signs the handshake with it).
func peerPin(cs tls.ConnectionState) (string, error) {
	if len(cs.PeerCertificates) == 0 {
		return "", errors.New("nodeproto: the peer sent no certificate")
	}
	k, ok := cs.PeerCertificates[0].PublicKey.(ed25519.PublicKey)
	if !ok {
		return "", errors.New("nodeproto: the peer's key must be Ed25519")
	}
	return Pin(k), nil
}

// serverConfig is the hub's side: it presents id, requires a client
// certificate (which proves the node's key; the registry decides whether
// that key is welcome), and speaks only TLS 1.3, without session tickets so
// every connection is a full handshake.
func serverConfig(id Identity) *tls.Config {
	return &tls.Config{
		MinVersion:             tls.VersionTLS13,
		Certificates:           []tls.Certificate{id.cert},
		ClientAuth:             tls.RequireAnyClientCert,
		NextProtos:             alpn(Versions),
		SessionTicketsDisabled: true,
	}
}

// clientConfig is the node's side: it presents id and accepts the hub only
// if the hub's key is one of pins.
func clientConfig(id Identity, pins []string) *tls.Config {
	pins = slices.Clone(pins)
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{id.cert},
		NextProtos:   alpn(Versions),
		// There is no CA, no name and no date to check: the hub is known by
		// its key alone. Go's verifier is switched off and VerifyConnection
		// compares the hub's key with the pins instead, which runs before
		// the handshake completes, so a hub we don't pin never sees a frame.
		// The impostor-hub tests in this package guard it (ADR-0014).
		InsecureSkipVerify: true, //nolint:gosec // the pin check below replaces chain verification
		VerifyConnection: func(cs tls.ConnectionState) error {
			pin, err := peerPin(cs)
			if err != nil {
				return err
			}
			if !slices.Contains(pins, pin) {
				return ErrHubNotPinned
			}
			return nil
		},
	}
}

// ErrHubNotPinned is the node refusing a hub whose key it doesn't pin.
var ErrHubNotPinned = errors.New("nodeproto: the hub's key isn't pinned")
