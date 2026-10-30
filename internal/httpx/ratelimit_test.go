package httpx

import (
	"fmt"
	"net/netip"
	"testing"
	"time"
)

func TestLimiter(t *testing.T) {
	now := time.Unix(0, 0)
	l := NewLimiter(2, time.Second)
	l.now = func() time.Time { return now }
	for i := range 2 {
		if ok, _ := l.Allow("a"); !ok {
			t.Fatalf("attempt %d denied within burst", i)
		}
	}
	ok, wait := l.Allow("a")
	if ok || wait != time.Second {
		t.Fatalf("over burst: ok=%v wait=%v", ok, wait)
	}
	if ok, _ := l.Allow("b"); !ok {
		t.Fatal("keys must be independent")
	}
	now = now.Add(time.Second)
	if ok, _ := l.Allow("a"); !ok {
		t.Fatal("token should refill")
	}
}

func TestLimiterFullMapKeepsDebt(t *testing.T) {
	now := time.Unix(0, 0)
	l := NewLimiter(1, time.Hour)
	l.now = func() time.Time { return now }
	l.Allow("attacker")
	// Fill the map with keys that are all still draining.
	for i := range maxBuckets - 1 {
		l.Allow(fmt.Sprintf("k%d", i))
	}
	if ok, _ := l.Allow("attacker"); ok {
		t.Fatal("a full map reset an existing key's budget")
	}
	// New keys share the overflow bucket: the first gets its token, the next waits.
	if ok, _ := l.Allow("new-1"); !ok {
		t.Fatal("first overflow key denied")
	}
	if ok, _ := l.Allow("new-2"); ok {
		t.Fatal("overflow keys must share one budget")
	}
	if len(l.buckets) > maxBuckets {
		t.Fatalf("map grew to %d", len(l.buckets))
	}
	if w := l.Wait("attacker"); w <= 0 {
		t.Fatalf("wait = %v for a drained key", w)
	}
	if w := l.Wait("never-seen"); w != 0 {
		t.Fatalf("wait = %v for an unknown key", w)
	}
}

func TestClientKey(t *testing.T) {
	key := func(s string) string { return ClientKey(netip.MustParseAddr(s)) }
	if key("2001:db8:1:2::1") != key("2001:db8:1:2:ffff::9") {
		t.Error("addresses in one /64 must share a key")
	}
	if key("2001:db8:1:2::1") == key("2001:db8:1:3::1") {
		t.Error("different /64s must not share a key")
	}
	if key("fe80::1%eth0") != key("fe80::2") {
		t.Error("zones must not split a /64")
	}
	if key("192.0.2.1") == key("192.0.2.2") {
		t.Error("IPv4 is keyed by address")
	}
}
