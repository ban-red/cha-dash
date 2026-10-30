package onboarding

import (
	"net/netip"
	"testing"
)

// A lead from a container label can't send the hub to cloud metadata.
func TestProbeRefusesLinkLocal(t *testing.T) {
	for addr, refused := range map[string]bool{
		"169.254.169.254":    true,
		"fe80::1":            true,
		"64:ff9b::a9fe:a9fe": true,
		"0.0.0.0":            true,
		"224.0.0.1":          true,
		"192.168.1.20":       false,
		"127.0.0.1":          false,
		"fd00::20":           false,
	} {
		if err := probeAllowed(netip.MustParseAddr(addr)); (err != nil) != refused {
			t.Errorf("probeAllowed(%s) = %v", addr, err)
		}
	}
}
