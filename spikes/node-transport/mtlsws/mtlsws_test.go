package mtlsws

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"dash.cha.sh/spikes/node-transport/core"
)

// The pin check is the custom verification code a reviewer has to trust, so
// throw forged chains at it.
func TestVerifyHubRejectsForgedChains(t *testing.T) {
	real, _ := NewHub()
	fake, _ := NewHub()
	leaf := func(h *Hub) *x509.Certificate {
		c, _ := x509.ParseCertificate(h.ids[0].leaf.Certificate[0])
		return c
	}
	rca, fca := real.ids[0].ca, fake.ids[0].ca
	pins := []string{real.Pin()}
	now := time.Now()
	for name, tc := range map[string]struct {
		chain []*x509.Certificate
		ok    bool
	}{
		"genuine":                             {[]*x509.Certificate{leaf(real), rca}, true},
		"impostor with its own CA":            {[]*x509.Certificate{leaf(fake), fca}, false},
		"impostor leaf with the real CA":      {[]*x509.Certificate{leaf(fake), rca}, false},
		"impostor leaf with both CAs":         {[]*x509.Certificate{leaf(fake), fca, rca}, false},
		"real CA presented as the leaf":       {[]*x509.Certificate{rca, rca}, false},
		"genuine leaf without its CA":         {[]*x509.Certificate{leaf(real)}, false},
		"genuine leaf with the impostor's CA": {[]*x509.Certificate{leaf(real), fca}, false},
	} {
		err := verifyHub(tc.chain, pins, now)
		if (err == nil) != tc.ok {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := verifyHub([]*x509.Certificate{leaf(fake), fca}, pins, now); !errors.Is(err, errPin) {
		t.Errorf("unpinned CA should fail the pin check, got %v", err)
	}
}

// The inner session negotiates what Go's TLS 1.3 defaults to, including the
// hybrid post-quantum key exchange, with no configuration.
func TestInnerSessionParameters(t *testing.T) {
	hub, _ := NewHub()
	node, _ := NewNode()
	node.AddPin(hub.Pin())
	hubCh, nodeCh := pair(t, hub, node)
	for side, ch := range map[string]core.Channel{"hub": hubCh, "node": nodeCh} {
		cs := ch.(*conn).ConnectionState()
		t.Logf("%s: %s, %s, key exchange %s, ALPN %q", side, tls.VersionName(cs.Version), tls.CipherSuiteName(cs.CipherSuite), cs.CurveID, cs.NegotiatedProtocol)
		if cs.Version != tls.VersionTLS13 || cs.NegotiatedProtocol != alpn {
			t.Errorf("%s: unexpected parameters", side)
		}
		if cs.CurveID != tls.X25519MLKEM768 {
			t.Errorf("%s: key exchange %s, want X25519MLKEM768", side, cs.CurveID)
		}
	}
}

func pair(t *testing.T, hub *Hub, node *Node) (core.Channel, core.Channel) {
	got := make(chan core.Channel, 1)
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		ch, _, err := hub.Handshake(r.Context(), ws, nil)
		if err != nil {
			t.Error(err)
		}
		got <- ch
		<-done
	}))
	t.Cleanup(func() { close(done); srv.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	nodeCh, err := node.Handshake(ctx, ws)
	if err != nil {
		t.Fatal(err)
	}
	return <-got, nodeCh
}

// Handshake bytes on the wire, for the results table.
func TestHandshakeSize(t *testing.T) {
	hub, _ := NewHub()
	node, _ := NewNode()
	node.AddPin(hub.Pin())
	cert, _ := hub.Issue("n_size", node.Key())
	node.Install(cert)
	ccfg, _ := node.ClientConfig()
	c1, c2 := net.Pipe()
	cc := &counting{Conn: c1}
	go tls.Server(c2, hub.ServerConfig(nil)).Handshake()
	if err := tls.Client(cc, ccfg).Handshake(); err != nil {
		t.Fatal(err)
	}
	t.Logf("TLS 1.3 with client certificate: %d bytes from the node, %d from the hub, %d total", cc.w, cc.r, cc.w+cc.r)
}

type counting struct {
	net.Conn
	r, w int
}

func (c *counting) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	c.r += n
	return n, err
}

func (c *counting) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	c.w += n
	return n, err
}

// The handshake's cryptography alone, over an in-memory pipe: an approved node
// with its certificate, the hub verifying it against its CA.
func BenchmarkHandshakeCrypto(b *testing.B) {
	hub, _ := NewHub()
	node, _ := NewNode()
	node.AddPin(hub.Pin())
	cert, _ := hub.Issue("n_bench", node.Key())
	node.Install(cert)
	scfg := hub.ServerConfig([]string{hub.Pin()})
	for b.Loop() {
		ccfg, _ := node.ClientConfig()
		c1, c2 := net.Pipe()
		errc := make(chan error, 1)
		go func() {
			s := tls.Server(c2, scfg)
			err := s.Handshake()
			if err == nil && !hub.issued(s.ConnectionState().PeerCertificates[0]) {
				err = errors.New("not issued")
			}
			errc <- err
		}()
		if err := tls.Client(c1, ccfg).Handshake(); err != nil {
			b.Fatal(err)
		}
		if err := <-errc; err != nil {
			b.Fatal(err)
		}
		c1.Close()
		c2.Close()
	}
}
