package node

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dash.cha.sh/internal/nodeproto"
)

// TestPendingRefusalWaits: a node the hub holds for approval retries in
// 30 s and never tries to pair again, even with a pairing secret it could
// send. unknown_node keeps today's handling.
func TestPendingRefusalWaits(t *testing.T) {
	newClient := func(t *testing.T) *Client {
		t.Helper()
		k, _ := nodeproto.GenerateKey()
		ident, err := OpenIdentity(filepath.Join(t.TempDir(), "identity"))
		if err != nil {
			t.Fatal(err)
		}
		d, err := NewDispatcher(DefaultPolicy(), fakeModule{})
		if err != nil {
			t.Fatal(err)
		}
		c, err := NewClient(Config{HubURL: "ws://hub.lab.example:3696/node", Identity: ident, Dispatcher: d,
			Pairing: nodeproto.FormatPairing(strings.Repeat("s", 43), nodeproto.PinOf(k))})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}

	c := newClient(t)
	for range 3 {
		wait, err := c.refused(false, nodeproto.Errorf(nodeproto.CodePending, "this node is waiting for approval"))
		if wait != 30*time.Second || err == nil || !strings.Contains(err.Error(), "waiting for approval") {
			t.Fatalf("pending: %v %v", wait, err)
		}
	}
	if c.repair {
		t.Fatal("pending set up a re-pair")
	}
	// While rotating, too: only unknown_node aborts a rotation.
	if wait, _ := c.refused(true, nodeproto.Errorf(nodeproto.CodePending, "this node is waiting for approval")); wait != 30*time.Second || c.repair {
		t.Fatalf("pending while rotating: %v %v", wait, c.repair)
	}

	c = newClient(t)
	if wait, _ := c.refused(false, nodeproto.Errorf(nodeproto.CodeUnknownNode, "this hub doesn't know the node's key")); wait != 0 || !c.repair {
		t.Fatalf("unknown_node: %v %v", wait, c.repair)
	}
	if wait, _ := c.refused(false, nodeproto.Errorf(nodeproto.CodeUnknownNode, "this hub doesn't know the node's key")); wait != 5*time.Minute {
		t.Fatalf("unknown_node again: %v", wait)
	}
}
