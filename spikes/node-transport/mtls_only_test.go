package transport_test

import (
	"slices"
	"testing"
	"time"

	"dash.cha.sh/spikes/node-transport/mtlsws"
)

// What a CA buys: the hub replaces its serving key and nodes don't notice.
func TestMTLSLeafRotationIsInvisible(t *testing.T) {
	e := newEnv(t, designs[0])
	n, nc, c := e.enroll(e.url, nil)
	c.close()
	pins := nc.Pins()
	if err := e.hc.(*mtlsws.Hub).RotateLeaf(); err != nil {
		t.Fatal(err)
	}
	c, reconnects := e.dial(n)
	defer c.close()
	if reconnects != 0 || !slices.Equal(nc.Pins(), pins) {
		t.Fatalf("reconnects %d, pins %v → %v", reconnects, pins, nc.Pins())
	}
}

// What a CA costs: certificates have dates. A node whose clock is wrong (a
// board without a real-time clock before NTP syncs) can't connect, and an
// expired hub leaf takes every node offline until it's replaced.
func TestMTLSClockFailures(t *testing.T) {
	e := newEnv(t, designs[0])
	n, nc, c := e.enroll(e.url, nil)
	c.close()

	node := nc.(*mtlsws.Node)
	node.Now = func() time.Time { return time.Date(1970, 1, 2, 0, 0, 0, 0, time.UTC) }
	_, err := n.Dial(e.ctx)
	if err == nil {
		t.Fatal("node with a 1970 clock connected")
	}
	t.Logf("node clock at 1970: %v", err)
	node.Now = nil

	hub := e.hc.(*mtlsws.Hub)
	hub.Now = func() time.Time { return time.Now().Add(-2 * 365 * 24 * time.Hour) }
	hub.RotateLeaf() // issued two years ago with a one-year lifetime
	hub.Now = nil
	if _, err := n.Dial(e.ctx); err == nil {
		t.Fatal("node accepted an expired hub certificate")
	} else {
		t.Logf("expired hub leaf: %v", err)
	}
	hub.RotateLeaf()
	c, _ = e.dial(n)
	c.close()
}
