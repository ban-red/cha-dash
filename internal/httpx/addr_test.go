package httpx

import (
	"net/netip"
	"slices"
	"testing"
)

func TestEmbeddedIPv4(t *testing.T) {
	for _, c := range []struct {
		ip   string
		want []string
	}{
		{"64:ff9b::7f00:1", []string{"127.0.0.1"}},
		{"64:ff9b::a9fe:a9fe", []string{"169.254.169.254"}},
		{"64:ff9b:1::cb00:710a", []string{"203.0.113.10", "0.0.0.0"}},
		{"64:ff9b:1:a9fe:a9:fe00::", []string{"0.0.0.0", "169.254.169.254"}},
		{"2002:a9fe:a9fe::1", []string{"169.254.169.254"}},
		{"2002:cb00:710a:1::", []string{"203.0.113.10"}},
		{"2001:db8::1", nil},
		{"::ffff:127.0.0.1", nil}, // mapped: the caller unmaps it
		{"127.0.0.1", nil},
		{"64:ff9a::7f00:1", nil},
	} {
		var got []string
		for _, a := range EmbeddedIPv4(netip.MustParseAddr(c.ip)) {
			got = append(got, a.String())
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("EmbeddedIPv4(%s) = %v, want %v", c.ip, got, c.want)
		}
	}
}
