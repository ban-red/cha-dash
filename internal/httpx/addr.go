package httpx

import "net/netip"

// IPv6 ranges that carry an IPv4 address, for outbound address checks.
var (
	nat64WellKnown = netip.MustParsePrefix("64:ff9b::/96")   // RFC 6052
	nat64Local     = netip.MustParsePrefix("64:ff9b:1::/48") // RFC 8215, local use
	sixToFour      = netip.MustParsePrefix("2002::/16")      // RFC 3056
)

// EmbeddedIPv4 returns the IPv4 addresses an IPv6 address stands for when
// a translator or relay would carry the connection on over IPv4: NAT64
// (64:ff9b::/96, and the local-use 64:ff9b:1::/48) and 6to4 (2002::/16).
// An outbound address check applies its IPv4 rule to each of them too, so
// 64:ff9b::7f00:1 isn't a way to reach 127.0.0.1 through a NAT64 gateway.
//
// A local-use NAT64 prefix may be any length inside the /48, so both the
// /96 layout (the last 32 bits) and the /48 layout of RFC 6052 (bits 48–63
// and 72–87) are returned. Nil for any other address.
func EmbeddedIPv4(a netip.Addr) []netip.Addr {
	if !a.Is6() || a.Is4In6() {
		return nil
	}
	b := a.As16()
	switch {
	case nat64WellKnown.Contains(a):
		return []netip.Addr{netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]})}
	case nat64Local.Contains(a):
		return []netip.Addr{
			netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]}),
			netip.AddrFrom4([4]byte{b[6], b[7], b[9], b[10]}),
		}
	case sixToFour.Contains(a):
		return []netip.Addr{netip.AddrFrom4([4]byte{b[2], b[3], b[4], b[5]})}
	}
	return nil
}
