package icons

import (
	"net/netip"
	"testing"
)

// NAT64 and 6to4 addresses get the fetcher's rule for the IPv4 address
// they carry.
func TestAllowedEmbeddedIPv4(t *testing.T) {
	for _, c := range []struct {
		addr string
		ok   bool
	}{
		{"64:ff9b::7f00:1", false},         // 127.0.0.1
		{"64:ff9b::a9fe:a9fe", false},      // 169.254.169.254
		{"64:ff9b::6464:64c8", false},      // 100.100.100.200, Alibaba metadata
		{"64:ff9b::c000:c0", false},        // 192.0.0.192, Oracle metadata
		{"64:ff9b::1:203", false},          // 0.1.2.3
		{"64:ff9b:1::7f00:1", false},       // local-use, /96 layout
		{"64:ff9b:1:7f00:0:100::", false},  // local-use, /48 layout: 127.0.0.1
		{"2002:7f00:1::1", false},          // 6to4: 127.0.0.1
		{"2002:a9fe:a9fe::1", false},       // 6to4: 169.254.169.254
		{"64:ff9b::c0a8:fa0a", true},       // 192.168.250.10: the LAN is fine
		{"64:ff9b::cb00:7105", true},       // 203.0.113.5
		{"2002:cb00:7105::1", true},        // 6to4: 203.0.113.5
		{"64:ff9b:1:cb00:71:500::", false}, // local-use: the /96 reading is 0.0.0.0
		{"64:ff9b:1:cb00:71:500:cb00:7105", true},
	} {
		if got := Allowed(netip.MustParseAddr(c.addr)); got != c.ok {
			t.Errorf("Allowed(%s) = %v, want %v", c.addr, got, c.ok)
		}
	}
}
