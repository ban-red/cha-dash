package node

import (
	"bytes"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dash.cha.sh/internal/nodeproto"
)

func testClient(t *testing.T, cfg Config) *Client {
	t.Helper()
	if cfg.Identity == nil {
		ident, err := OpenIdentity(filepath.Join(t.TempDir(), "identity"))
		if err != nil {
			t.Fatal(err)
		}
		cfg.Identity = ident
	}
	d, err := NewDispatcher(DefaultPolicy(), fakeModule{})
	if err != nil {
		t.Fatal(err)
	}
	cfg.HubURL, cfg.Dispatcher = "ws://hub.lab.example:3696/node", d
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TestPendingRefusalWaits: a node the hub holds for approval says so once,
// with its fingerprint, then retries every 30 s for as long as it takes,
// and never tries to pair again, even with a pairing secret it could send.
// unknown_node keeps today's handling.
func TestPendingRefusalWaits(t *testing.T) {
	k, _ := nodeproto.GenerateKey()
	pairing := nodeproto.FormatPairing(strings.Repeat("s", 43), nodeproto.PinOf(k))
	var log bytes.Buffer
	c := testClient(t, Config{Pairing: pairing, Log: slog.New(slog.NewTextHandler(&log, nil))})
	for range 5 {
		wait, err := c.refused(false, nodeproto.Errorf(nodeproto.CodePending, "this node is waiting for approval"))
		if wait != 30*time.Second || err != nil {
			t.Fatalf("pending: %v %v", wait, err)
		}
	}
	fp := nodeproto.Fingerprint(c.cfg.Identity.Current().Pin)
	if n := strings.Count(log.String(), "waiting for approval in Cha Dash; fingerprint "+fp); n != 1 {
		t.Fatalf("logged the wait %d times:\n%s", n, log.String())
	}
	if c.repair {
		t.Fatal("pending set up a re-pair")
	}
	// While rotating, too: only unknown_node aborts a rotation.
	if wait, _ := c.refused(true, nodeproto.Errorf(nodeproto.CodePending, "this node is waiting for approval")); wait != 30*time.Second || c.repair {
		t.Fatalf("pending while rotating: %v %v", wait, c.repair)
	}

	c = testClient(t, Config{Pairing: pairing})
	if wait, _ := c.refused(false, nodeproto.Errorf(nodeproto.CodeUnknownNode, "this hub doesn't know the node's key")); wait != 0 || !c.repair {
		t.Fatalf("unknown_node: %v %v", wait, c.repair)
	}
	if wait, _ := c.refused(false, nodeproto.Errorf(nodeproto.CodeUnknownNode, "this hub doesn't know the node's key")); wait != 5*time.Minute {
		t.Fatalf("unknown_node again: %v", wait)
	}
}

// TestJoinToken: a node without an identity trusts the token's pin and
// holds its secret; once it has joined, the token is ignored and the log
// says it can go. A malformed token (or a pairing token) stops the node.
func TestJoinToken(t *testing.T) {
	k, _ := nodeproto.GenerateKey()
	pin := nodeproto.PinOf(k)
	tok := nodeproto.FormatJoin(strings.Repeat("j", 43), pin)

	c := testClient(t, Config{Token: tok})
	if c.token != strings.Repeat("j", 43) || c.tokenPin != pin || len(c.pins()) != 1 || c.pins()[0] != pin {
		t.Fatalf("token %q pin %q pins %v", c.token, c.tokenPin, c.pins())
	}

	ident, err := OpenIdentity(filepath.Join(t.TempDir(), "identity"))
	if err != nil {
		t.Fatal(err)
	}
	other, _ := nodeproto.GenerateKey()
	if err := ident.Update(func(s *State) { s.Node, s.HubPins = "n_joined", []string{nodeproto.PinOf(other)} }); err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	c = testClient(t, Config{Token: tok, Identity: ident, Log: slog.New(slog.NewTextHandler(&log, nil))})
	if c.token != "" || len(c.pins()) != 1 || c.pins()[0] != nodeproto.PinOf(other) {
		t.Fatalf("an enrolled node took the token: %q %v", c.token, c.pins())
	}
	if !strings.Contains(log.String(), "the token is ignored and can be removed") {
		t.Fatalf("log: %s", log.String())
	}

	for _, bad := range []string{"cdj_short." + pin, nodeproto.FormatPairing(strings.Repeat("s", 43), pin)} {
		d, _ := NewDispatcher(DefaultPolicy(), fakeModule{})
		ident, _ := OpenIdentity(filepath.Join(t.TempDir(), "identity"))
		if _, err := NewClient(Config{HubURL: "ws://hub.lab.example:3696/node", Identity: ident, Dispatcher: d, Token: bad}); err == nil {
			t.Errorf("token %q was accepted", bad)
		}
	}
}
