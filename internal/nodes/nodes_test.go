package nodes

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dash.cha.sh/internal/audit"
	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/node"
	"dash.cha.sh/internal/nodeproto"
	"dash.cha.sh/internal/store"
	"dash.cha.sh/internal/vault"
)

type env struct {
	t       *testing.T
	hub     *Hub
	st      *store.Store
	g       *graph.Graph
	audit   *audit.Log
	srv     *httptest.Server
	pairing string // the pairing file's path
	dir     string
}

func newEnv(t *testing.T, pairing bool) *env {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(t.Context(), filepath.Join(dir, "chadash.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	v, err := vault.New(st, make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, st: st, g: graph.New(), audit: audit.New(st), dir: dir}
	o := Options{Store: st, Vault: v, Graph: e.g, Audit: e.audit, Session: nodeproto.SessionOptions{KeepAlive: time.Second}}
	if pairing {
		e.pairing = filepath.Join(dir, "pairing", "token")
		os.MkdirAll(filepath.Dir(e.pairing), 0o750)
		o.PairingFile = e.pairing
	}
	e.hub, err = New(t.Context(), o)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	e.hub.Register(mux, func(h http.Handler) http.Handler { return h }, func(*http.Request) audit.Actor {
		return audit.Actor{Kind: "user", ID: "u_owner", Name: "owner"}
	})
	e.srv = httptest.NewServer(mux)
	t.Cleanup(e.srv.Close)
	t.Cleanup(e.hub.Close)
	return e
}

func (e *env) url() string { return "ws" + strings.TrimPrefix(e.srv.URL, "http") + nodeproto.Path }

func (e *env) token() string {
	b, err := os.ReadFile(e.pairing)
	if err != nil {
		e.t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

// echoModule serves a read verb and a lifecycle verb.
type echoModule struct{}

func (echoModule) Name() string { return "echo" }
func (echoModule) Verbs() []node.Verb {
	return []node.Verb{
		{Name: "container.list", Policy: "docker.read", Handle: func(_ context.Context, req json.RawMessage, progress func(json.RawMessage)) (json.RawMessage, error) {
			progress(json.RawMessage(`"listing"`))
			return req, nil
		}},
		{Name: "container.remove", Policy: "docker.remove", Handle: func(context.Context, json.RawMessage, func(json.RawMessage)) (json.RawMessage, error) {
			return json.RawMessage(`"removed"`), nil
		}},
	}
}

type nodeRun struct {
	client *node.Client
	ident  *node.IdentityStore
	cancel context.CancelFunc
	done   chan struct{}
}

// startNode runs a node with its identity in dir.
func (e *env) startNode(dir, name, pairing string, policy node.Policy) *nodeRun {
	e.t.Helper()
	ident, err := node.OpenIdentity(filepath.Join(dir, "identity"))
	if err != nil {
		e.t.Fatal(err)
	}
	d, err := node.NewDispatcher(policy, echoModule{})
	if err != nil {
		e.t.Fatal(err)
	}
	c, err := node.NewClient(node.Config{HubURL: e.url(), Name: name, Build: "test", Identity: ident, Dispatcher: d, Pairing: pairing,
		Session: nodeproto.SessionOptions{KeepAlive: time.Second}})
	if err != nil {
		e.t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &nodeRun{client: c, ident: ident, cancel: cancel, done: make(chan struct{})}
	go func() { c.Run(ctx); close(r.done) }()
	e.t.Cleanup(r.stop)
	return r
}

func (r *nodeRun) stop() {
	r.cancel()
	<-r.done
}

func eventually(t *testing.T, what string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !f() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (e *env) events(action string) []audit.Event {
	e.t.Helper()
	page, err := e.audit.Query(e.t.Context(), audit.Filter{Action: action})
	if err != nil {
		e.t.Fatal(err)
	}
	return page.Events
}

func TestPairingWithTheRightSecret(t *testing.T) {
	e := newEnv(t, true)
	fi, err := os.Stat(e.pairing)
	if err != nil || fi.Mode().Perm() != PairingFileMode {
		t.Fatalf("pairing file: %v %v", fi, err)
	}
	if !strings.HasSuffix(e.token(), "."+e.hub.Pin()) {
		t.Fatal("the pairing token doesn't carry the hub's pin")
	}
	dir := t.TempDir()
	n := e.startNode(dir, "local", e.token(), node.DefaultPolicy())
	eventually(t, "the node to connect", func() bool { return e.hub.Connected("local") && n.client.Connected() })

	list, err := e.hub.List(t.Context())
	if err != nil || len(list) != 1 {
		t.Fatalf("list %v %v", list, err)
	}
	rec := list[0]
	if rec.State != StateApproved || rec.EnrolledVia != ViaLocal || rec.ApprovedBy != ApprovedByPairing || rec.PublicKey != n.ident.Current().Pin || !rec.HubKeyCurrent {
		t.Fatalf("record %+v", rec)
	}
	if len(rec.Verbs) != 2 || !rec.Verbs[0].Allowed || rec.Verbs[1].Allowed {
		t.Fatalf("verbs %+v", rec.Verbs)
	}
	if st := n.ident.State(); st.Node != rec.ID {
		t.Fatalf("the node saved %q", st.Node)
	}
	// Status in the graph.
	r, ok := e.g.Get(ResourceID(rec.ID))
	if !ok || r.Kind != "node" || r.State != "connected" || r.Health != graph.HealthOK || r.Name != "local" {
		t.Fatalf("graph %+v", r)
	}
	// The caller, by name and by ID; the node's policy refuses remove.
	var items []string
	res, err := e.hub.Call(t.Context(), "local", "container.list", map[string]bool{"all": true}, func(b json.RawMessage) { items = append(items, string(b)) })
	if err != nil || string(res) != `{"all":true}` || len(items) != 1 {
		t.Fatalf("call: %s %v %v", res, err, items)
	}
	if _, err := e.hub.Call(t.Context(), rec.ID, "container.list", nil, nil); err != nil {
		t.Fatal(err)
	}
	_, err = e.hub.Call(t.Context(), "local", "container.remove", nil, nil)
	if nodeproto.CodeOf(err) != nodeproto.CodeForbidden {
		t.Fatalf("remove = %v, want the node's refusal", err)
	}
	if _, err := e.hub.Call(t.Context(), "elsewhere", "container.list", nil, nil); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("unknown node = %v", err)
	}
	if evs := e.events("nodes.enroll"); len(evs) != 1 || evs[0].Result != audit.OK || strings.Contains(string(evs[0].Params), e.token()[4:20]) {
		t.Fatalf("audit %+v", evs)
	}

	// A restart reconnects with the saved identity, without the pairing token.
	n.stop()
	eventually(t, "the hub to notice", func() bool {
		r, _ := e.g.Get(ResourceID(rec.ID))
		return !e.hub.Connected("local") && r.State == "disconnected" && r.Health == graph.HealthDown
	})
	e.startNode(dir, "local", "", node.DefaultPolicy())
	eventually(t, "the node to reconnect", func() bool { return e.hub.Connected(rec.ID) })
	if list, _ := e.hub.List(t.Context()); len(list) != 1 {
		t.Fatalf("a reconnect enrolled again: %v", list)
	}
}

func TestPairingWithTheWrongSecret(t *testing.T) {
	e := newEnv(t, true)
	_, pin, _ := nodeproto.ParsePairing(e.token())
	n := e.startNode(t.TempDir(), "local", nodeproto.FormatPairing(strings.Repeat("x", 43), pin), node.DefaultPolicy())
	eventually(t, "the refusal", func() bool { return len(e.events("nodes.enroll")) > 0 })
	time.Sleep(100 * time.Millisecond)
	if e.hub.Connected("local") || n.client.Connected() {
		t.Fatal("a wrong secret connected")
	}
	if list, _ := e.hub.List(t.Context()); len(list) != 0 {
		t.Fatalf("a wrong secret enrolled: %v", list)
	}
	ev := e.events("nodes.enroll")[0]
	if ev.Result != audit.Refused || ev.Error == nil || ev.Error.Code != nodeproto.CodeBadPairing || strings.Contains(string(ev.Params), "xxxx") {
		t.Fatalf("audit %+v", ev)
	}
}

func TestNoSecret(t *testing.T) {
	e := newEnv(t, true)
	// The node pins the hub (so it trusts it) but has no pairing secret.
	p := node.DefaultPolicy()
	p.Hub.Pin = e.hub.Pin()
	n := e.startNode(t.TempDir(), "local", "", p)
	time.Sleep(300 * time.Millisecond)
	if e.hub.Connected("local") || n.client.Connected() {
		t.Fatal("a node without a secret connected")
	}
	if list, _ := e.hub.List(t.Context()); len(list) != 0 {
		t.Fatalf("enrolled: %v", list)
	}
}

func TestPairingOff(t *testing.T) {
	e := newEnv(t, false)
	// Even with the hub's pin and a well-formed secret, a hub without
	// pairing refuses unknown keys.
	n := e.startNode(t.TempDir(), "local", nodeproto.FormatPairing(strings.Repeat("y", 43), e.hub.Pin()), node.DefaultPolicy())
	eventually(t, "the refusal", func() bool { return len(e.events("nodes.enroll")) > 0 })
	if n.client.Connected() {
		t.Fatal("connected")
	}
}

func TestImpostorHubNeverSeesTheSecret(t *testing.T) {
	real := newEnv(t, true)
	impostor := newEnv(t, true)
	// The node holds the real hub's token but dials the impostor.
	ident, _ := node.OpenIdentity(filepath.Join(t.TempDir(), "identity"))
	d, _ := node.NewDispatcher(node.DefaultPolicy(), echoModule{})
	c, err := node.NewClient(node.Config{HubURL: impostor.url(), Name: "local", Identity: ident, Dispatcher: d, Pairing: real.token()})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	c.Run(ctx)
	if len(impostor.events("nodes.enroll")) != 0 || len(real.events("nodes.enroll")) != 0 {
		t.Fatal("the impostor got a hello")
	}
}

func TestRevoke(t *testing.T) {
	e := newEnv(t, true)
	n := e.startNode(t.TempDir(), "local", e.token(), node.DefaultPolicy())
	eventually(t, "connect", func() bool { return e.hub.Connected("local") })
	rec, _ := e.hub.ByName(t.Context(), "local")

	// Over HTTP, as an admin would.
	resp, err := http.Post(e.srv.URL+"/api/nodes/"+rec.ID+"/revoke", "application/json", nil)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("revoke: %v %v", resp.Status, err)
	}
	resp.Body.Close()
	eventually(t, "the session to end", func() bool { return !n.client.Connected() && !e.hub.Connected(rec.ID) })
	if _, err := e.hub.Call(t.Context(), "local", "container.list", nil, nil); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("call after revoke = %v", err)
	}
	if _, ok := e.g.Get(ResourceID(rec.ID)); ok {
		t.Fatal("a revoked node is still in the graph")
	}
	got, _ := e.hub.Get(t.Context(), rec.ID)
	if got.State != StateRevoked || got.RevokedBy != "u_owner" {
		t.Fatalf("record %+v", got)
	}
	// It stays out.
	time.Sleep(300 * time.Millisecond)
	if e.hub.Connected(rec.ID) {
		t.Fatal("a revoked node reconnected")
	}
	resp, _ = http.Post(e.srv.URL+"/api/nodes/"+rec.ID+"/revoke", "application/json", nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("revoke twice: %s", resp.Status)
	}
	resp, _ = http.Post(e.srv.URL+"/api/nodes/n_nope/revoke", "application/json", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("revoke unknown: %s", resp.Status)
	}
	if evs := e.events("nodes.revoke"); len(evs) != 1 || evs[0].Actor.Name != "owner" {
		t.Fatalf("audit %+v", evs)
	}
	resp, _ = http.Get(e.srv.URL + "/api/nodes")
	var body struct{ Nodes []Status }
	json.NewDecoder(resp.Body).Decode(&body)
	resp.Body.Close()
	if len(body.Nodes) != 1 || body.Nodes[0].State != StateRevoked || body.Nodes[0].Connected {
		t.Fatalf("list %+v", body)
	}
}

func TestRePairAfterLosingTheIdentity(t *testing.T) {
	e := newEnv(t, true)
	n := e.startNode(t.TempDir(), "local", e.token(), node.DefaultPolicy())
	eventually(t, "connect", func() bool { return e.hub.Connected("local") })
	first, _ := e.hub.ByName(t.Context(), "local")
	n.stop()
	// The identity volume is gone: a new key pairs under the same name.
	e.startNode(t.TempDir(), "local", e.token(), node.DefaultPolicy())
	eventually(t, "the new node", func() bool {
		r, err := e.hub.ByName(t.Context(), "local")
		return err == nil && r.ID != first.ID && e.hub.Connected(r.ID)
	})
	old, _ := e.hub.Get(t.Context(), first.ID)
	if old.State != StateRevoked {
		t.Fatalf("the old key is %s", old.State)
	}
}

func TestNodeKeyRotation(t *testing.T) {
	e := newEnv(t, true)
	dir := t.TempDir()
	n := e.startNode(dir, "local", e.token(), node.DefaultPolicy())
	eventually(t, "connect", func() bool { return n.client.Connected() })
	rec, _ := e.hub.ByName(t.Context(), "local")
	oldPin := n.ident.Current().Pin
	if err := n.client.RotateKey(t.Context()); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the rotation", func() bool {
		r, _ := e.hub.Get(t.Context(), rec.ID)
		return r.PublicKey != oldPin && r.NextKey == "" && e.hub.Connected(rec.ID) && n.ident.Current().Pin != oldPin
	})
	cur, _ := e.hub.Get(t.Context(), rec.ID)
	if cur.PublicKey != n.ident.Current().Pin {
		t.Fatal("the hub and the node disagree on the key")
	}
	if _, ok := n.ident.Next(); ok {
		t.Fatal("the node is still rotating")
	}
	if _, known, _ := e.hub.reg.lookup(t.Context(), oldPin); known {
		t.Fatal("the old key is still accepted")
	}
	if evs := e.events("nodes.rotate"); len(evs) != 1 {
		t.Fatalf("audit %v", evs)
	}
}

func TestHubKeyRotation(t *testing.T) {
	e := newEnv(t, true)
	n := e.startNode(t.TempDir(), "local", e.token(), node.DefaultPolicy())
	eventually(t, "connect", func() bool { return n.client.Connected() })
	old := e.hub.Pin()
	took, err := e.hub.RotateKey(t.Context())
	if err != nil || took != 1 {
		t.Fatalf("rotate: %d %v", took, err)
	}
	newPin := e.hub.Pin()
	if newPin == old || !strings.HasSuffix(e.token(), "."+newPin) {
		t.Fatal("the pairing file wasn't rewritten")
	}
	pins := n.ident.State().HubPins
	if len(pins) < 2 || pins[len(pins)-1] != newPin {
		t.Fatalf("node pins %v", pins)
	}
	if s, _ := e.hub.Stranded(t.Context()); len(s) != 0 {
		t.Fatalf("stranded %v", s)
	}
	if n, err := e.hub.RetireOldKeys(t.Context()); err != nil || n != 1 {
		t.Fatalf("retire: %d %v", n, err)
	}
	// The node reconnects against the new key only.
	live := e.hub.session("local")
	live.sess.Close()
	eventually(t, "reconnect on the new key", func() bool {
		c := e.hub.session("local")
		return c != nil && c != live && c.sess.Channel().Version == 1 && n.client.Connected()
	})
	if _, err := e.hub.Call(t.Context(), "local", "container.list", nil, nil); err != nil {
		t.Fatalf("call after the rollover: %v", err)
	}
	_ = must(e.hub.ByName(t.Context(), "local"))

	// A node whose policy fixes the hub's pin refuses a push.
	p := node.DefaultPolicy()
	p.Hub.Pin = newPin
	ident, _ := node.OpenIdentity(filepath.Join(t.TempDir(), "id"))
	if err := ident.AddHubPin(newPin); err != nil {
		t.Fatal(err)
	}
	if p.PinRotation() {
		t.Fatal("a fixed pin allows rotation")
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func TestHubKeysPersist(t *testing.T) {
	e := newEnv(t, false)
	pin := e.hub.Pin()
	v, _ := vault.New(e.st, make([]byte, 32))
	h2, err := New(t.Context(), Options{Store: e.st, Vault: v})
	if err != nil {
		t.Fatal(err)
	}
	defer h2.Close()
	if h2.Pin() != pin {
		t.Fatal("a restart made a new hub key")
	}
	// A wrong vault key can't open the hub's key.
	bad, _ := vault.New(e.st, append(make([]byte, 31), 1))
	if _, err := New(t.Context(), Options{Store: e.st, Vault: bad}); err == nil {
		t.Fatal("opened the hub key with the wrong vault key")
	}
}

func TestPairingFile(t *testing.T) {
	dir := t.TempDir()
	k1, _ := nodeproto.GenerateKey()
	k2, _ := nodeproto.GenerateKey()
	path := filepath.Join(dir, "token")
	p, err := OpenPairing(path, nodeproto.PinOf(k1))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	secret, _, err := nodeproto.ParsePairing(string(b))
	if err != nil || !p.Check(secret) || p.Check("") || p.Check(secret+"x") {
		t.Fatalf("check: %v", err)
	}
	// Reopening keeps the secret and takes the new pin.
	p2, err := OpenPairing(path, nodeproto.PinOf(k2))
	if err != nil || !p2.Check(secret) {
		t.Fatal("the secret changed")
	}
	b, _ = os.ReadFile(path)
	if !strings.HasSuffix(strings.TrimSpace(string(b)), nodeproto.PinOf(k2)) {
		t.Fatal("the pin wasn't updated")
	}
	os.WriteFile(path, []byte("garbage"), 0o640)
	if _, err := OpenPairing(path, nodeproto.PinOf(k1)); err == nil {
		t.Fatal("a garbled pairing file was replaced silently")
	}
}

func TestEnrollmentRateLimit(t *testing.T) {
	now := time.Unix(1000, 0)
	l := newLimiter(func() time.Time { return now })
	for range 5 {
		if !l.allowed("192.0.2.1") {
			t.Fatal("limited too early")
		}
		l.failed("192.0.2.1")
	}
	if l.allowed("192.0.2.1") || !l.allowed("192.0.2.2") {
		t.Fatal("limit")
	}
	now = now.Add(time.Minute)
	if !l.allowed("192.0.2.1") {
		t.Fatal("never recovers")
	}
}
