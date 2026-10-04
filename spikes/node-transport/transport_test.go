package transport_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"dash.cha.sh/spikes/node-transport/core"
	"dash.cha.sh/spikes/node-transport/internal/testnet"
)

// Enrollment, request/response and streaming, on the compose network (plain
// ws://) and behind a reverse proxy that terminates TLS (wss://).
func TestEnrollAndCall(t *testing.T) {
	each(t, func(t *testing.T, d design) {
		for _, path := range []string{"direct", "tls-terminating-proxy"} {
			t.Run(path, func(t *testing.T) {
				e := newEnv(t, d)
				url, client := e.url, (*http.Client)(nil)
				if path != "direct" {
					url, client = testnet.TLSProxy(t, strings.Replace(strings.TrimSuffix(e.url, "/node"), "ws", "http", 1))
				}
				n, nc, c := e.enroll(url, client)
				defer c.close()

				rec, ok := e.hub.Reg.Get(n.ID())
				if !ok || rec.State != core.Approved || !bytes.Equal(rec.Key, d.key(nc)) {
					t.Fatalf("registry: %+v", rec)
				}
				if err := e.ping(n.ID()); err != nil {
					t.Fatal(err)
				}

				var items int
				res, err := e.hub.Call(e.ctx, n.ID(), "logs.tail", map[string]int{"lines": 500}, func(json.RawMessage) { items++ })
				if err != nil || items != 500 || string(res) != `{"lines":500}` {
					t.Fatalf("stream: %d items, %s, %v", items, res, err)
				}

				// 300 KiB: bigger than one Noise message, so it's chunked.
				big := strings.Repeat("x", 300<<10)
				res, err = e.hub.Call(e.ctx, n.ID(), "echo", big, nil)
				var back string
				if err != nil || json.Unmarshal(res, &back) != nil || back != big {
					t.Fatalf("echo: %d bytes, %v", len(back), err)
				}
			})
		}
	})
}

// A node reconnects with its pinned identity, including after a restart that
// reloads it from disk, without a token.
func TestReconnect(t *testing.T) {
	each(t, func(t *testing.T, d design) {
		e := newEnv(t, d)
		n, nc, c := e.enroll(e.url, nil)
		c.close()
		e.ev.waitN(t, "session_ended", 1)

		c, reconnects := e.dial(n)
		if reconnects != 0 {
			t.Fatalf("reconnects = %d", reconnects)
		}
		if err := e.ping(n.ID()); err != nil {
			t.Fatal(err)
		}
		c.close()

		restarted := e.newNode(d.clone(nc), e.url, "", nil)
		restarted.Resume(n.ID())
		c, _ = e.dial(restarted)
		defer c.close()
		if err := e.ping(n.ID()); err != nil {
			t.Fatal(err)
		}
		if got := len(e.hub.Reg.List(core.Approved)); got != 1 {
			t.Fatalf("%d approved nodes, want 1", got)
		}
	})
}

// A node that restarts while waiting for approval comes back as the same
// pending node, without its (spent) token.
func TestPendingSurvivesRestart(t *testing.T) {
	each(t, func(t *testing.T, d design) {
		e := newEnv(t, d)
		nc, _ := d.node()
		n := e.newNode(nc, e.url, e.hub.JoinToken(time.Minute), nil)
		ctx, cancel := context.WithCancel(e.ctx)
		done := make(chan struct{})
		go func() { n.Dial(ctx); close(done) }()
		id := e.ev.waitN(t, "pending", 1).Node
		cancel()
		<-done

		restarted := e.newNode(d.clone(nc), e.url, "", nil)
		restarted.Resume(id)
		errc := make(chan error, 1)
		go func() { _, err := restarted.Dial(e.ctx); errc <- err }()
		e.ev.waitN(t, "pending", 2)
		e.hub.Approve(id)
		if err := <-errc; !errors.Is(err, core.ErrReconnect) {
			t.Fatal(err)
		}
		c, _ := e.dial(restarted)
		defer c.close()
		if err := e.ping(id); err != nil {
			t.Fatal(err)
		}
	})
}

// A join token works once, expires, and can't be guessed.
func TestJoinTokenRules(t *testing.T) {
	each(t, func(t *testing.T, d design) {
		e := newEnv(t, d)
		tok := e.hub.JoinToken(15 * time.Minute)
		first, _ := d.node()
		n := e.newNode(first, e.url, tok, nil)
		go n.Dial(e.ctx)
		e.ev.waitN(t, "pending", 1)

		second, _ := d.node()
		if _, err := e.newNode(second, e.url, tok, nil).Dial(e.ctx); !refusedWith(err, core.ErrBadToken.Error()) {
			t.Fatalf("reused token: %v", err)
		}

		expiring := e.hub.JoinToken(15 * time.Minute)
		e.hub.Reg.Now = func() time.Time { return time.Now().Add(16 * time.Minute) }
		third, _ := d.node()
		if _, err := e.newNode(third, e.url, expiring, nil).Dial(e.ctx); !refusedWith(err, core.ErrBadToken.Error()) {
			t.Fatalf("expired token: %v", err)
		}
		e.hub.Reg.Now = time.Now

		_, pin, _ := core.ParseToken(tok)
		forged := "cdj_" + strings.Repeat("A", 26) + "." + pin
		fourth, _ := d.node()
		if _, err := e.newNode(fourth, e.url, forged, nil).Dial(e.ctx); !refusedWith(err, core.ErrBadToken.Error()) {
			t.Fatalf("forged token: %v", err)
		}
		if got := len(e.hub.Reg.List(core.Pending)); got != 1 {
			t.Fatalf("%d pending nodes, want 1", got)
		}
	})
}

// A node the hub has never enrolled gets nothing, even with a valid handshake.
func TestUnenrolledNodeRejected(t *testing.T) {
	each(t, func(t *testing.T, d design) {
		e := newEnv(t, d)
		nc, _ := d.node()
		nc.AddPin(e.hc.Pin()) // it knows the hub's pin; it just isn't enrolled
		n := e.newNode(nc, e.url, "", nil)
		n.Resume("n_madeup")
		if _, err := n.Dial(e.ctx); !refusedWith(err, "unknown node") {
			t.Fatalf("unenrolled node: %v", err)
		}
		if e.ev.count("session") != 0 {
			t.Fatal("hub opened a session")
		}
	})
}

// An impostor hub (an active MITM with its own keys) never sees the token,
// and the token still works at the real hub afterwards.
func TestImpostorHubRejected(t *testing.T) {
	each(t, func(t *testing.T, d design) {
		real, impostor := newEnv(t, d), newEnv(t, d)
		tok := real.hub.JoinToken(15 * time.Minute)
		nc, _ := d.node()
		n := real.newNode(nc, impostor.url, tok, nil)
		_, err := n.Dial(real.ctx)
		var refused *core.RefusedError
		if err == nil || errors.As(err, &refused) {
			t.Fatalf("dial impostor: %v", err)
		}
		t.Logf("node refused the impostor: %v", err)
		impostor.ev.waitN(t, "handshake_failed", 1)
		if impostor.ev.count("pending")+impostor.ev.count("refused") != 0 {
			t.Fatal("impostor got past the handshake")
		}

		n.URL = real.url
		go n.Dial(real.ctx)
		real.ev.waitN(t, "pending", 1)
	})
}

// A proxy sees WebSocket frames in the clear. It must not see the token, the
// node ID or any RPC content.
func TestProxySeesOnlyCiphertext(t *testing.T) {
	each(t, func(t *testing.T, d design) {
		e := newEnv(t, d)
		relay := testnet.NewRelay(t, e.url, core.PinsHeader)
		tok := e.hub.JoinToken(15 * time.Minute)
		secret, _, _ := core.ParseToken(tok)

		nc, _ := d.node()
		n := e.newNode(nc, relay.URL, tok, nil)
		errc := make(chan error, 1)
		go func() { _, err := n.Dial(e.ctx); errc <- err }()
		id := e.ev.waitN(t, "pending", 1).Node
		e.hub.Approve(id)
		<-errc
		c, _ := e.dial(n)
		defer c.close()
		if err := e.ping(id); err != nil {
			t.Fatal(err)
		}
		seen := bytes.Join(relay.Seen(), nil)
		for _, s := range []string{secret, id, `"op"`, "pong", "hello"} {
			if bytes.Contains(seen, []byte(s)) {
				t.Errorf("relay saw %q in the clear", s)
			}
		}
		t.Logf("relay carried %d messages, %d bytes, all opaque", len(relay.Seen()), len(seen))
	})
}

// Flipping one bit in either direction kills the session instead of
// delivering altered data.
func TestTamperingDetected(t *testing.T) {
	each(t, func(t *testing.T, d design) {
		for _, dir := range []string{"hub→node", "node→hub"} {
			t.Run(dir, func(t *testing.T) {
				e := newEnv(t, d)
				relay := testnet.NewRelay(t, e.url, core.PinsHeader)
				n, _, c := e.enroll(relay.URL, nil)
				defer c.close()
				var on atomic.Bool
				relay.Tap = func(toHub bool, msg []byte) []byte {
					if on.Load() && toHub == (dir == "node→hub") {
						msg = bytes.Clone(msg)
						msg[len(msg)-1] ^= 1
					}
					return msg
				}
				on.Store(true)
				if err := e.ping(n.ID()); err == nil {
					t.Fatal("tampered call succeeded")
				}
				select {
				case <-c.s.Done():
				case <-time.After(5 * time.Second):
					t.Fatal("node session survived tampering")
				}
			})
		}
	})
}

// Replaying a recorded handshake opens nothing.
func TestReplayRejected(t *testing.T) {
	each(t, func(t *testing.T, d design) {
		e := newEnv(t, d)
		n, nc, c := e.enroll(e.url, nil)
		c.close()

		relay := testnet.NewRelay(t, e.url, core.PinsHeader)
		n.URL = relay.URL
		c, _ = e.dial(n)
		recorded := relay.ToHub()
		c.close()

		sessions, failed := e.ev.count("session"), e.ev.count("handshake_failed")
		ctx, cancel := context.WithTimeout(e.ctx, 3*time.Second)
		defer cancel()
		testnet.Replay(ctx, e.url, http.Header{core.PinsHeader: {strings.Join(nc.Pins(), ",")}}, recorded)
		e.ev.waitN(t, "handshake_failed", failed+1)
		if e.ev.count("session") != sessions {
			t.Fatal("replay opened a session")
		}
		t.Logf("replayed %d recorded messages: handshake failed", len(recorded))
	})
}

// Knowing a node's public identity (its certificate, or its public key) isn't
// enough to connect as it.
func TestStolenPublicIdentityRejected(t *testing.T) {
	each(t, func(t *testing.T, d design) {
		e := newEnv(t, d)
		victim, nc, c := e.enroll(e.url, nil)
		c.close()
		sessions, failed := e.ev.count("session"), e.ev.count("handshake_failed")

		attacker := e.newNode(d.stolen(nc), e.url, "", nil)
		attacker.Resume(victim.ID())
		if _, err := attacker.Dial(e.ctx); err == nil {
			t.Fatal("attacker connected")
		}
		e.ev.waitN(t, "handshake_failed", failed+1)
		if e.ev.count("session") != sessions {
			t.Fatal("attacker got a session")
		}
	})
}

// Revoking cuts a live session and refuses every later attempt, pending or not.
func TestRevocation(t *testing.T) {
	each(t, func(t *testing.T, d design) {
		e := newEnv(t, d)
		n, _, c := e.enroll(e.url, nil)
		if err := e.hub.Revoke(e.ctx, n.ID()); err != nil {
			t.Fatal(err)
		}
		select {
		case <-c.s.Done():
		case <-time.After(5 * time.Second):
			t.Fatal("live session survived revocation")
		}
		if _, err := n.Dial(e.ctx); !refusedWith(err, "revoked") {
			t.Fatalf("revoked node: %v", err)
		}

		nc, _ := d.node()
		p := e.newNode(nc, e.url, e.hub.JoinToken(time.Minute), nil)
		errc := make(chan error, 1)
		go func() { _, err := p.Dial(e.ctx); errc <- err }()
		id := e.ev.waitN(t, "pending", 2).Node
		e.hub.Revoke(e.ctx, id)
		if err := <-errc; !refusedWith(err, "revoked") {
			t.Fatalf("revoked while pending: %v", err)
		}
	})
}

// The node rotates its key over the live session; afterwards only the new key
// works.
func TestNodeKeyRotation(t *testing.T) {
	each(t, func(t *testing.T, d design) {
		e := newEnv(t, d)
		n, nc, c := e.enroll(e.url, nil)
		old := d.clone(nc)
		oldKey := d.key(nc)
		if err := n.RotateKey(e.ctx); err != nil {
			t.Fatal(err)
		}
		if rec, _ := e.hub.Reg.Get(n.ID()); !bytes.Equal(rec.Next, d.key(nc)) || !bytes.Equal(rec.Key, oldKey) {
			t.Fatal("hub should hold old and next key until it sees the new one")
		}
		if err := e.ping(n.ID()); err != nil { // the current session carries on
			t.Fatal(err)
		}
		c.close()

		c, reconnects := e.dial(n)
		defer c.close()
		if rec, _ := e.hub.Reg.Get(n.ID()); !bytes.Equal(rec.Key, d.key(nc)) || rec.Next != nil {
			t.Fatal("rotation not completed")
		}
		t.Logf("reconnected with the new key (%d extra round trips)", reconnects)

		stale := e.newNode(old, e.url, "", nil)
		stale.Resume(n.ID())
		if _, err := stale.Dial(e.ctx); !refusedWith(err, "unknown node") {
			t.Fatalf("old key after rotation: %v", err)
		}
	})
}

// The hub rotates its own identity without re-enrolling anyone: connected
// nodes get the new pin pushed, offline ones get it on their next connection,
// and only nodes that stay away past RetireOldKeys must re-enroll.
func TestHubKeyRotation(t *testing.T) {
	each(t, func(t *testing.T, d design) {
		e := newEnv(t, d)
		online, onlineNC, c1 := e.enroll(e.url, nil)
		straggler, stragglerNC, c2 := e.enroll(e.url, nil)
		gone, _, c3 := e.enroll(e.url, nil)
		c2.close()
		c3.close()

		if _, err := e.hub.RotateKey(e.ctx); err != nil {
			t.Fatal(err)
		}
		if got := len(onlineNC.Pins()); got != 2 {
			t.Fatalf("online node holds %d pins, want 2", got)
		}
		c1.close()
		c1, _ = e.dial(online)

		// The straggler only pins the old identity; the hub still answers with
		// it and pushes the new pin.
		c2, _ = e.dial(straggler)
		deadline := time.Now().Add(5 * time.Second)
		for len(stragglerNC.Pins()) != 2 {
			if time.Now().After(deadline) {
				t.Fatal("straggler never got the new pin")
			}
			time.Sleep(10 * time.Millisecond)
		}
		c1.close()
		c2.close()

		e.hub.RetireOldKeys()
		for _, n := range []*core.Node{online, straggler} {
			c, reconnects := e.dial(n)
			if err := e.ping(n.ID()); err != nil {
				t.Fatal(err)
			}
			c.close()
			t.Logf("%s after retiring the old identity: %d reconnects", n.ID(), reconnects)
		}

		_, err := gone.Dial(e.ctx)
		var refused *core.RefusedError
		if err == nil || errors.As(err, &refused) {
			t.Fatalf("node that missed the rotation: %v", err)
		}
		t.Logf("node that missed the whole window must re-enroll: %v", err)
	})
}
