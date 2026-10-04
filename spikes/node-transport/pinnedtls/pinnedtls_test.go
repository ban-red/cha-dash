package pinnedtls

import (
	"crypto/tls"
	"net"
	"testing"
	"time"
)

// handshake runs both ends over an in-memory pipe, closed when the test ends
// (closing the tls.Conns instead would block on an unread close_notify).
func handshake(t testing.TB, scfg, ccfg *tls.Config) (*tls.Conn, *tls.Conn, int) {
	c1, c2 := net.Pipe()
	t.Cleanup(func() { c1.Close(); c2.Close() })
	cc := &counting{Conn: c1}
	s := tls.Server(c2, scfg)
	errc := make(chan error, 1)
	go func() { errc <- s.Handshake() }()
	c := tls.Client(cc, ccfg)
	if err := c.Handshake(); err != nil {
		t.Fatal(err)
	}
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	return s, c, cc.r + cc.w
}

// No dates are checked anywhere, so a node or hub with a wrong clock (a board
// without a real-time clock, before NTP) still connects.
func TestClockIsIrrelevant(t *testing.T) {
	hub, _ := NewHub()
	node, _ := NewNode()
	node.AddPin(hub.Pin())
	for _, at := range []time.Time{time.Date(1970, 1, 2, 0, 0, 0, 0, time.UTC), time.Date(2200, 1, 1, 0, 0, 0, 0, time.UTC)} {
		scfg, ccfg := hub.ServerConfig(nil), node.ClientConfig()
		scfg.Time = func() time.Time { return at }
		ccfg.Time = func() time.Time { return at }
		s, _, _ := handshake(t, scfg, ccfg)
		if k, _ := peerKey(s.ConnectionState()); !k.Equal(node.Key()) {
			t.Fatalf("%v: hub saw the wrong key", at)
		}
	}
}

// Handshake bytes on the wire and the negotiated parameters, for the results table.
func TestHandshakeSize(t *testing.T) {
	hub, _ := NewHub()
	node, _ := NewNode()
	node.AddPin(hub.Pin())
	_, c, n := handshake(t, hub.ServerConfig(nil), node.ClientConfig())
	cs := c.ConnectionState()
	t.Logf("TLS 1.3, pinned self-signed keys: %d bytes, %s, %s", n, tls.CipherSuiteName(cs.CipherSuite), cs.CurveID)
	if cs.CurveID != tls.X25519MLKEM768 {
		t.Errorf("key exchange %s, want X25519MLKEM768", cs.CurveID)
	}
}

// The handshake's cryptography alone, over an in-memory pipe.
func BenchmarkHandshakeCrypto(b *testing.B) {
	hub, _ := NewHub()
	node, _ := NewNode()
	node.AddPin(hub.Pin())
	scfg := hub.ServerConfig(nil)
	for b.Loop() {
		c1, c2 := net.Pipe()
		errc := make(chan error, 1)
		go func() { errc <- tls.Server(c2, scfg).Handshake() }()
		if err := tls.Client(c1, node.ClientConfig()).Handshake(); err != nil {
			b.Fatal(err)
		}
		if err := <-errc; err != nil {
			b.Fatal(err)
		}
		c1.Close()
		c2.Close()
	}
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
