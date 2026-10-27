package httpx

import (
	"context"
	"net"
	"net/netip"
	"syscall"
)

// GuardDial returns a DialContext for an http.Transport that refuses every
// address check rejects, in two places:
//
//   - an IP literal ("[::1]:80", "169.254.169.254:80") is checked before any
//     socket is made, so the refusal is the same whether or not the host
//     has that address family;
//   - a name is checked on each address actually dialled, after DNS
//     (net.Dialer.Control), so a rebinding answer can't slip one past.
//
// check gets the zero netip.Addr when an address can't be parsed, and must
// refuse it. Its error comes back from the dial, wrapped, for errors.As and
// errors.Is. d is copied, never changed; its Control is replaced.
func GuardDial(d *net.Dialer, check func(netip.Addr) error) func(ctx context.Context, network, address string) (net.Conn, error) {
	guarded := *d
	guarded.Control = func(_, address string, _ syscall.RawConn) error {
		ap, err := netip.ParseAddrPort(address)
		if err != nil {
			return check(netip.Addr{})
		}
		return check(ap.Addr())
	}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		if host, _, err := net.SplitHostPort(address); err == nil {
			if ip, err := netip.ParseAddr(host); err == nil {
				if err := check(ip); err != nil {
					return nil, &net.OpError{Op: "dial", Net: network, Err: err}
				}
			}
		}
		return guarded.DialContext(ctx, network, address)
	}
}
