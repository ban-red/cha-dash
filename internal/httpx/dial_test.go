package httpx

import (
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"
)

var errRefused = errors.New("refused")

// refuseLoopback is a rule like the icon fetcher's: loopback and anything
// unparseable are refused.
func refuseLoopback(seen *[]netip.Addr) func(netip.Addr) error {
	return func(a netip.Addr) error {
		*seen = append(*seen, a)
		if !a.IsValid() || a.Unmap().IsLoopback() {
			return errRefused
		}
		return nil
	}
}

func listen(t *testing.T) (net.Listener, string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	return ln, port
}

// TestGuardDialRefusesLiteralsBeforeASocket: an IP literal is checked
// before dialling, so the refusal doesn't depend on the host having IPv6.
func TestGuardDialRefusesLiteralsBeforeASocket(t *testing.T) {
	var seen []netip.Addr
	dial := GuardDial(&net.Dialer{Timeout: time.Second}, refuseLoopback(&seen))
	for _, addr := range []string{"[::1]:1", "127.0.0.1:1", "[::ffff:127.0.0.1]:1"} {
		c, err := dial(t.Context(), "tcp", addr)
		if c != nil {
			_ = c.Close()
		}
		if !errors.Is(err, errRefused) {
			t.Errorf("dial(%s) = %v, want the rule's refusal", addr, err)
		}
		if _, ok := errors.AsType[*net.OpError](err); !ok {
			t.Errorf("dial(%s): %T isn't a *net.OpError", addr, err)
		}
	}
	if len(seen) != 3 {
		t.Errorf("the rule saw %v, want each literal once", seen)
	}
}

// TestGuardDialChecksResolvedNames: a name is checked on the address it
// resolved to, not on the name.
func TestGuardDialChecksResolvedNames(t *testing.T) {
	_, port := listen(t)
	var seen []netip.Addr
	dial := GuardDial(&net.Dialer{Timeout: time.Second}, refuseLoopback(&seen))
	c, err := dial(t.Context(), "tcp4", "localhost:"+port)
	if c != nil {
		_ = c.Close()
	}
	if !errors.Is(err, errRefused) {
		t.Fatalf("dial(localhost) = %v, want the rule's refusal", err)
	}
	if len(seen) == 0 || !seen[0].IsLoopback() {
		t.Errorf("the rule saw %v, want the resolved loopback address", seen)
	}
}

func TestGuardDialAllows(t *testing.T) {
	_, port := listen(t)
	d := &net.Dialer{Timeout: time.Second}
	var seen []netip.Addr
	dial := GuardDial(d, func(a netip.Addr) error { seen = append(seen, a); return nil })
	c, err := dial(t.Context(), "tcp", "127.0.0.1:"+port)
	if err != nil {
		t.Fatalf("dial = %v, want a connection", err)
	}
	_ = c.Close()
	// Once as a literal, once on the socket.
	if len(seen) != 2 {
		t.Errorf("the rule saw %v, want the address twice", seen)
	}
	if d.Control != nil {
		t.Error("GuardDial changed the caller's dialer")
	}
}
