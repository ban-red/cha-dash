// Package httpx holds small HTTP helpers shared by the hub's handlers:
// client identity behind reverse proxies, JSON I/O and common middleware.
package httpx

import (
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
)

// Proxies decides which X-Forwarded-* headers to believe. Headers are only
// honoured when the TCP peer is inside one of the configured prefixes
// (CHADASH_TRUSTED_PROXIES); otherwise any client could spoof them.
type Proxies struct {
	trusted []netip.Prefix
}

// NewProxies returns a Proxies trusting the given prefixes.
func NewProxies(trusted []netip.Prefix) Proxies { return Proxies{trusted: trusted} }

func (p Proxies) isTrusted(a netip.Addr) bool {
	a = a.Unmap()
	for _, pre := range p.trusted {
		if pre.Contains(a) {
			return true
		}
	}
	return false
}

// ViaTrusted reports whether the request's TCP peer is a trusted proxy.
func (p Proxies) ViaTrusted(r *http.Request) bool {
	peer := PeerAddr(r)
	return peer.IsValid() && p.isTrusted(peer)
}

// PeerAddr is the TCP peer of the request.
func PeerAddr(r *http.Request) netip.Addr {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}
	}
	return a.Unmap()
}

// ClientIP returns the client's address: the TCP peer, or, when the peer is a
// trusted proxy, the rightmost X-Forwarded-For entry that isn't one.
func (p Proxies) ClientIP(r *http.Request) netip.Addr {
	peer := PeerAddr(r)
	if !peer.IsValid() || !p.isTrusted(peer) {
		return peer
	}
	hops := forwardedFor(r)
	client := peer
	for _, hop := range slices.Backward(hops) {
		a, err := netip.ParseAddr(hop)
		if err != nil {
			return client // malformed chain: stop at the last hop we could vouch for
		}
		client = a.Unmap()
		if !p.isTrusted(client) {
			return client
		}
	}
	return client
}

func forwardedFor(r *http.Request) []string {
	var hops []string
	for _, h := range r.Header.Values("X-Forwarded-For") {
		for part := range strings.SplitSeq(h, ",") {
			if part = strings.TrimSpace(part); part != "" {
				hops = append(hops, part)
			}
		}
	}
	return hops
}

// IsHTTPS reports whether the client reached the hub over TLS: directly, or
// through a trusted proxy that says so in X-Forwarded-Proto.
func (p Proxies) IsHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if !p.isTrusted(PeerAddr(r)) {
		return false
	}
	proto, _, _ := strings.Cut(r.Header.Get("X-Forwarded-Proto"), ",")
	return strings.EqualFold(strings.TrimSpace(proto), "https")
}
