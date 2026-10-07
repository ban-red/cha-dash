package notify

import (
	"net/netip"
	"testing"
)

// NAT64 and 6to4 addresses carry an IPv4 address; each policy applies its
// IPv4 rule to it.
func TestAllowedEmbeddedIPv4(t *testing.T) {
	for _, c := range []struct {
		ip          string
		lan, public bool
	}{
		// NAT64, well-known prefix: the last 32 bits.
		{"64:ff9b::7f00:1", true, false},     // 127.0.0.1
		{"64:ff9b::a63:2", true, false},      // 10.99.0.2
		{"64:ff9b::ac14:630a", true, false},  // 172.20.99.10
		{"64:ff9b::c0a8:101", true, false},   // 192.168.1.1
		{"64:ff9b::a9fe:a9fe", false, false}, // 169.254.169.254, metadata
		{"64:ff9b::0:0", false, false},       // 0.0.0.0
		{"64:ff9b::e000:1", false, false},    // 224.0.0.1, multicast
		{"64:ff9b::6464:101", true, false},   // 100.100.1.1, CGNAT
		{"64:ff9b::cb00:710a", true, true},   // 203.0.113.10
		{"64:ff9b::808:808", true, true},     // 8.8.8.8
		// NAT64, local use: never public; on the LAN, by what it carries in
		// either layout.
		{"64:ff9b:1::cb00:710a", false, false}, // the /48 reading is 0.0.0.0: both readings must pass
		{"64:ff9b:1:cb00:71:a00:cb00:710a", true, false},
		{"64:ff9b:1::a9fe:a9fe", false, false},
		{"64:ff9b:1:a9fe:a9:fe00::", false, false}, // 169.254.169.254 in the /48 layout
		// 6to4: bits 16–48.
		{"2002:7f00:1::1", true, false},     // 127.0.0.1
		{"2002:a63:5::1", true, false},      // 10.99.0.5
		{"2002:a9fe:a9fe::1", false, false}, // 169.254.169.254
		{"2002:cb00:710a::1", true, true},   // 203.0.113.10
		// Plain addresses are unchanged.
		{"2001:db8::10", true, true},
		{"203.0.113.10", true, true},
	} {
		ip := netip.MustParseAddr(c.ip)
		if got := allowed(policyLAN, ip); got != c.lan {
			t.Errorf("LAN %s: %v, want %v", c.ip, got, c.lan)
		}
		if got := allowed(policyPublic, ip); got != c.public {
			t.Errorf("public %s: %v, want %v", c.ip, got, c.public)
		}
	}
}
