package nodes

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"dash.cha.sh/internal/audit"
	"dash.cha.sh/internal/authz"
	"dash.cha.sh/internal/node"
	"dash.cha.sh/internal/nodeproto"
)

// hello dials the hub with a fresh key, says hello and returns its answer.
func (e *env) hello(h nodeproto.Hello) (nodeproto.Frame, string) {
	e.t.Helper()
	key, _ := nodeproto.GenerateKey()
	ident, _ := nodeproto.NewIdentity(key)
	return e.helloAs(ident, h), nodeproto.PinOf(key)
}

func (e *env) helloAs(ident nodeproto.Identity, h nodeproto.Hello) nodeproto.Frame {
	e.t.Helper()
	ch, err := nodeproto.Dial(e.t.Context(), e.url(), nodeproto.DialOptions{Identity: ident, Pins: []string{e.hub.Pin()}})
	if err != nil {
		e.t.Fatal(err)
	}
	defer ch.Close()
	h.Versions = []int{1}
	if err := nodeproto.SendFrame(e.t.Context(), ch, nodeproto.OpHello, h, nil); err != nil {
		e.t.Fatal(err)
	}
	f, err := nodeproto.RecvFrame(e.t.Context(), ch, 5*time.Second)
	if err != nil {
		e.t.Fatal(err)
	}
	return f
}

// TestKnownNodesGetThroughALockedAddress: strangers failing from a node's
// address (a shared NAT, or a proxy the hub doesn't trust) lock out only
// other strangers. Approved and pending keys still get through, and their
// connections don't clear the strangers' failures.
func TestKnownNodesGetThroughALockedAddress(t *testing.T) {
	e := newEnv(t, true)
	dir, spent := t.TempDir(), e.token()
	n := e.startNode(dir, "local", spent, node.DefaultPolicy())
	eventually(t, "the node to pair", func() bool { return e.hub.Connected("local") })
	n.stop()
	eventually(t, "the node to go", func() bool { return !e.hub.Connected("local") })

	// Five strangers' keys refused from the node's address lock it.
	for range 5 {
		if f, _ := e.hello(nodeproto.Hello{Name: "stranger"}); f.Error == nil || f.Error.Code != nodeproto.CodeUnknownNode {
			t.Fatalf("a stranger got %+v", f)
		}
	}
	if f, _ := e.hello(nodeproto.Hello{Name: "stranger"}); f.Error == nil || f.Error.Code != nodeproto.CodeRateLimited {
		t.Fatalf("the sixth stranger got %+v", f)
	}

	// The approved node reconnects all the same.
	e.startNode(dir, "local", spent, node.DefaultPolicy())
	eventually(t, "the node to reconnect", func() bool { return e.hub.Connected("local") })

	// So does a pending one: it's told to wait, not that it's limited.
	key, _ := nodeproto.GenerateKey()
	ident, _ := nodeproto.NewIdentity(key)
	now := time.Now().UnixMilli()
	if _, err := e.st.Writer().ExecContext(t.Context(), `INSERT INTO nodes (id, name, public_key, state, enrolled_via, created_at, updated_at)
		VALUES ('n_pending', 'far', ?, 'pending', 'token', ?, ?)`, nodeproto.PinOf(key), now, now); err != nil {
		t.Fatal(err)
	}
	if f := e.helloAs(ident, nodeproto.Hello{Name: "far"}); f.Error == nil || f.Error.Code != nodeproto.CodePending {
		t.Fatalf("a pending node got %+v", f)
	}
	if e.failures("127.0.0.1") < 5 {
		t.Fatalf("a known node cleared the strangers' failures: %d", e.failures("127.0.0.1"))
	}
	if f, _ := e.hello(nodeproto.Hello{Name: "stranger"}); f.Error == nil || f.Error.Code != nodeproto.CodeRateLimited {
		t.Fatalf("a stranger after the known nodes got %+v", f)
	}
}

// TestLimiterKeepsTheLockedOnes: a full limiter makes room by forgetting
// the stalest clients, not everyone, so a flood of new addresses can't
// unlock one that's being limited; and a quiet client is forgotten.
func TestLimiterKeepsTheLockedOnes(t *testing.T) {
	now := time.Unix(1000, 0)
	l := newLimiter(func() time.Time { return now })
	for i := range 4095 {
		l.failed(fmt.Sprintf("10.0.%d.%d", i/256, i%256))
	}
	now = now.Add(time.Second)
	for range 5 {
		l.failed("192.0.2.1")
	}
	now = now.Add(time.Second)
	for i := range 10 {
		l.failed(fmt.Sprintf("10.1.0.%d", i))
	}
	if l.allowed("192.0.2.1") {
		t.Fatal("filling the limiter unlocked a client")
	}
	if len(l.seen) > 4096 {
		t.Fatalf("%d entries", len(l.seen))
	}
	now = now.Add(10 * time.Minute)
	if !l.allowed("192.0.2.1") {
		t.Fatal("a client that stopped failing is never forgotten")
	}
	l.failed("192.0.2.1")
	if !l.allowed("192.0.2.1") {
		t.Fatal("a forgotten client starts over with one failure")
	}
}

// TestJoinTokenRefusalsAreAuditedSparingly: bad tokens from one client
// write one audit row per 10 minutes, as an anonymous actor, with a count
// of the refusals left out, and a name that isn't one stays out of it.
func TestJoinTokenRefusalsAreAuditedSparingly(t *testing.T) {
	j := newJoinEnv(t)
	wrong := strings.Repeat("w", 43)
	try := func() {
		t.Helper()
		j.hub.limit.succeeded("127.0.0.1") // the limiter isn't what's tested
		if f, _ := j.hello(nodeproto.Hello{Name: "bad name\nforged: line", Token: wrong}); f.Error == nil || f.Error.Code != nodeproto.CodeUnknownNode {
			t.Fatalf("a bad token got %+v", f)
		}
	}
	for range 4 {
		try()
	}
	evs := j.events("nodes.enroll")
	if len(evs) != 1 {
		t.Fatalf("%d refusals audited, want 1", len(evs))
	}
	ev := evs[0]
	if ev.Actor.Kind != authz.KindAnonymous || ev.Actor.Name != "join-token" || ev.Target != "hub" || ev.Result != audit.Refused {
		t.Fatalf("audit %+v", ev)
	}
	var p map[string]any
	if err := json.Unmarshal(ev.Params, &p); err != nil {
		t.Fatal(err)
	}
	if _, ok := p["name"]; ok {
		t.Fatalf("an invalid name reached the audit: %v", p)
	}

	j.clock.add(refusalWindow)
	try()
	evs = j.events("nodes.enroll")
	if len(evs) != 2 {
		t.Fatalf("%d refusals audited after the window, want 2", len(evs))
	}
	var p2 map[string]any
	for _, ev := range evs {
		var q map[string]any
		_ = json.Unmarshal(ev.Params, &q)
		if q["refusals_not_recorded"] != nil {
			p2 = q
		}
	}
	if p2 == nil || p2["refusals_not_recorded"] != float64(3) {
		t.Fatalf("the second row doesn't count the 3 left out: %v", evs)
	}
}

func TestRefusalAuditIsBoundedOverall(t *testing.T) {
	now := time.Unix(1000, 0)
	r := newRefusalAudit(func() time.Time { return now })
	n := 0
	for i := range refusalsAudited + 50 {
		if ok, _ := r.take(fmt.Sprintf("2001:db8:%x::/64", i)); ok {
			n++
		}
	}
	if n != refusalsAudited {
		t.Fatalf("%d recorded from rotating clients, want %d", n, refusalsAudited)
	}
	now = now.Add(refusalWindow)
	if ok, held := r.take("2001:db8:ffff::/64"); !ok || held != 50 {
		t.Fatalf("after the window: %v %d", ok, held)
	}
}

// TestJoinTokenCantBeLocal: only local pairing makes a node called local.
// A token can't name it, and a node with an unnamed token can't take it.
func TestJoinTokenCantBeLocal(t *testing.T) {
	j := newJoinEnv(t)
	for _, name := range []string{"local", "LOCAL", "Local"} {
		if _, _, err := j.hub.CreateJoinToken(t.Context(), owner, name, DefaultTokenTTL, false); err == nil {
			t.Errorf("a token for %q", name)
		}
	}
	_, tok := j.token("", false)
	secret, _, err := nodeproto.ParseJoin(tok)
	if err != nil {
		t.Fatal(err)
	}
	if f, _ := j.hello(nodeproto.Hello{Name: "Local", Token: secret}); f.Error == nil || f.Error.Code != nodeproto.CodeProtocol {
		t.Fatalf("a token node called Local got %+v", f)
	}
	if list, _ := j.hub.List(t.Context()); len(list) != 0 {
		t.Fatalf("enrolled: %+v", list)
	}
	if toks, _ := j.hub.JoinTokens().List(t.Context()); toks[0].State != TokenActive {
		t.Fatalf("the refusal spent the token: %+v", toks[0])
	}
}
