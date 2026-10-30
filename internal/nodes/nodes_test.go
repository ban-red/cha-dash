package nodes

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	return newEnvWith(t, pairing, nil)
}

// newEnvWith is newEnv with a hook to adjust the hub's options.
func newEnvWith(t *testing.T, pairing bool, adjust func(*Options)) *env {
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
	// The admin the routes act as (join tokens name their maker).
	if _, err := st.Writer().ExecContext(t.Context(), `INSERT INTO users (id, username, role, created_at, updated_at) VALUES ('u_owner', 'owner', 'owner', 1, 1)`); err != nil {
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
	if adjust != nil {
		adjust(&o)
	}
	e.hub, err = New(t.Context(), o)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	pass := func(h http.Handler) http.Handler { return h }
	e.hub.Register(mux, pass, pass, func(*http.Request) audit.Actor {
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
	return e.startNodeWith(dir, name, pairing, policy, nil)
}

// startNodeWith is startNode with a hook to adjust the client's config.
func (e *env) startNodeWith(dir, name, pairing string, policy node.Policy, adjust func(*node.Config)) *nodeRun {
	e.t.Helper()
	ident, err := node.OpenIdentity(filepath.Join(dir, "identity"))
	if err != nil {
		e.t.Fatal(err)
	}
	d, err := node.NewDispatcher(policy, echoModule{})
	if err != nil {
		e.t.Fatal(err)
	}
	cfg := node.Config{HubURL: e.url(), Name: name, Build: "test", Identity: ident, Dispatcher: d, Pairing: pairing,
		Session: nodeproto.SessionOptions{KeepAlive: time.Second}}
	if adjust != nil {
		adjust(&cfg)
	}
	c, err := node.NewClient(cfg)
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
	if _, err := e.hub.Call(t.Context(), "elsewhere", "container.list", nil, nil); !errors.Is(err, ErrNotConnected) ||
		nodeproto.CodeOf(err) != nodeproto.CodeNotConnected || !strings.Contains(err.Error(), "node elsewhere isn't known") {
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
	// The error says why, for the integrations that read through the node.
	if _, err := e.hub.Call(t.Context(), "local", "container.list", nil, nil); !errors.Is(err, ErrNotConnected) ||
		!strings.Contains(err.Error(), "node local was revoked") {
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

// TestRePairAfterLosingTheIdentity: a node that lost its identity can't
// replace the enrolled one by pairing under its name (review finding 1);
// once an admin revokes the old node, the secret is regenerated, the node
// reads it from the file and pairs as a new node.
func TestRePairAfterLosingTheIdentity(t *testing.T) {
	e := newEnv(t, true)
	n := e.startNode(t.TempDir(), "local", e.token(), node.DefaultPolicy())
	eventually(t, "connect", func() bool { return e.hub.Connected("local") })
	first, _ := e.hub.ByName(t.Context(), "local")
	n.stop()
	// The identity volume is gone: a new key tries the same name, with the
	// pairing file's current (fresh, unspent) secret.
	dir := t.TempDir()
	stale := e.token()
	fresh := e.startNodeWith(dir, "local", stale, node.DefaultPolicy(), func(c *node.Config) { c.PairingFile = e.pairing })
	eventually(t, "the conflict", func() bool { return e.refusedWith(nodeproto.CodeConflict) > 0 })
	if cur, _ := e.hub.Get(t.Context(), first.ID); cur.State != StateApproved {
		t.Fatalf("pairing replaced the enrolled node: it's %s", cur.State)
	}
	if fresh.client.Connected() {
		t.Fatal("a second key paired under a live node's name")
	}
	if e.token() != stale {
		t.Fatal("a refused pairing spent the secret")
	}
	// The admin revokes the old node, which regenerates the secret. The new
	// node started with the old token, but reads the file when it pairs.
	fresh.stop()
	if _, err := e.hub.Revoke(t.Context(), first.ID, audit.Actor{Kind: "user", ID: "u_owner", Name: "owner"}); err != nil {
		t.Fatal(err)
	}
	if e.token() == stale {
		t.Fatal("revoking didn't regenerate the secret")
	}
	e.hub.limit.succeeded("127.0.0.1") // the conflicts counted as failures
	e.startNodeWith(dir, "local", stale, node.DefaultPolicy(), func(c *node.Config) { c.PairingFile = e.pairing })
	eventually(t, "the new node", func() bool {
		r, err := e.hub.ByName(t.Context(), "local")
		return err == nil && r.ID != first.ID && e.hub.Connected(r.ID)
	})
}

// refusedWith counts refused enrollments with code.
func (e *env) refusedWith(code string) int {
	n := 0
	for _, ev := range e.events("nodes.enroll") {
		if ev.Result == audit.Refused && ev.Error != nil && ev.Error.Code == code {
			n++
		}
	}
	return n
}

// TestPairingSecretIsSingleUse: the enrollment a secret admits spends it,
// and a fresh one is in the file (review finding 1).
func TestPairingSecretIsSingleUse(t *testing.T) {
	e := newEnv(t, true)
	spent := e.token()
	e.startNode(t.TempDir(), "local", spent, node.DefaultPolicy())
	eventually(t, "connect", func() bool { return e.hub.Connected("local") })
	next := e.token()
	if next == spent {
		t.Fatal("the pairing file still holds the spent secret")
	}
	if fi, err := os.Stat(e.pairing); err != nil || fi.Mode().Perm() != PairingFileMode {
		t.Fatalf("pairing file after the rewrite: %v %v", fi, err)
	}
	// Another key with the spent secret is refused, under any name.
	e.startNode(t.TempDir(), "other", spent, node.DefaultPolicy())
	eventually(t, "the refusal", func() bool { return e.refusedWith(nodeproto.CodeBadPairing) > 0 })
	if _, err := e.hub.ByName(t.Context(), "other"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the spent secret enrolled a node: %v", err)
	}
	// The fresh one works, once.
	e.startNode(t.TempDir(), "third", next, node.DefaultPolicy())
	eventually(t, "the third node", func() bool { return e.hub.Connected("third") })
	if e.token() == next {
		t.Fatal("the second enrollment didn't spend its secret either")
	}
}

// TestNodeOnAnotherHost is deploy/compose.node.yaml's flow: the hub's own
// node pairs from the shared file, then a node elsewhere pairs under its own
// name with the token the hub wrote next, passed once (CHADASH_NODE_PAIRING,
// no file). Both serve, and a restart that still carries the spent token
// reconnects on the node's identity instead of pairing again.
func TestNodeOnAnotherHost(t *testing.T) {
	e := newEnv(t, true)
	e.startNodeWith(t.TempDir(), "local", e.token(), node.DefaultPolicy(), func(c *node.Config) { c.PairingFile = e.pairing })
	eventually(t, "the local node", func() bool { return e.hub.Connected("local") })

	tok := e.token()
	dir := t.TempDir()
	remote := e.startNode(dir, "attic", tok, node.DefaultPolicy())
	eventually(t, "the remote node", func() bool { return e.hub.Connected("attic") })
	if e.token() == tok {
		t.Fatal("the remote node's pairing didn't spend the token")
	}
	for _, name := range []string{"local", "attic"} {
		if _, err := e.hub.Call(t.Context(), name, "container.list", nil, nil); err != nil {
			t.Fatalf("call %s: %v", name, err)
		}
	}
	rec, err := e.hub.ByName(t.Context(), "attic")
	if err != nil || rec.State != StateApproved {
		t.Fatalf("record %+v %v", rec, err)
	}

	remote.stop()
	eventually(t, "the hub to notice", func() bool { return !e.hub.Connected("attic") })
	e.startNode(dir, "attic", tok, node.DefaultPolicy())
	eventually(t, "the remote node to reconnect", func() bool { return e.hub.Connected(rec.ID) })
	if list, _ := e.hub.List(t.Context()); len(list) != 2 {
		t.Fatalf("nodes after the restart: %v", list)
	}
	if n := e.refusedWith(nodeproto.CodeBadPairing); n != 0 {
		t.Fatalf("the restart presented the spent token: %d refusals", n)
	}
}

// TestRevokeRegeneratesTheSecret: after a revoke, the secret anyone could
// have read before is dead; the admin route regenerates it too, audited
// (review finding 1).
func TestRevokeRegeneratesTheSecret(t *testing.T) {
	e := newEnv(t, true)
	e.startNode(t.TempDir(), "local", e.token(), node.DefaultPolicy())
	eventually(t, "connect", func() bool { return e.hub.Connected("local") })
	rec, _ := e.hub.ByName(t.Context(), "local")
	before := e.token() // what a copy of the volume would hold now
	resp, err := http.Post(e.srv.URL+"/api/nodes/"+rec.ID+"/revoke", "application/json", nil)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("revoke: %v %v", resp.Status, err)
	}
	resp.Body.Close()
	if e.token() == before {
		t.Fatal("revoking didn't regenerate the pairing secret")
	}
	// Re-pairing with what was read before the revoke is refused.
	e.startNode(t.TempDir(), "local", before, node.DefaultPolicy())
	eventually(t, "the refusal", func() bool { return e.refusedWith(nodeproto.CodeBadPairing) > 0 })
	if _, err := e.hub.ByName(t.Context(), "local"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("re-paired with the old secret: %v", err)
	}
	// An admin regenerates; the new secret pairs.
	old := e.token()
	resp, err = http.Post(e.srv.URL+"/api/nodes/pairing/regenerate", "application/json", nil)
	if err != nil || resp.StatusCode != http.StatusNoContent {
		t.Fatalf("regenerate: %v %v", resp.Status, err)
	}
	resp.Body.Close()
	if e.token() == old {
		t.Fatal("regenerate kept the secret")
	}
	evs := e.events("nodes.pairing.regenerate")
	if len(evs) != 1 || evs[0].Actor.Name != "owner" || evs[0].Result != audit.OK || strings.Contains(string(evs[0].Params), e.token()[4:20]) {
		t.Fatalf("audit %+v", evs)
	}
	e.hub.limit.succeeded("127.0.0.1")
	e.startNode(t.TempDir(), "local", e.token(), node.DefaultPolicy())
	eventually(t, "the re-paired node", func() bool {
		r, err := e.hub.ByName(t.Context(), "local")
		return err == nil && r.ID != rec.ID && e.hub.Connected(r.ID)
	})
	// Pairing off: the route says so.
	off := newEnv(t, false)
	resp, _ = http.Post(off.srv.URL+"/api/nodes/pairing/regenerate", "application/json", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("regenerate with pairing off: %s", resp.Status)
	}
}

// TestFreshInstallPairsByItself: the hub writes the file on first start,
// a restart before the node pairs keeps it, the node's first connection
// spends it, and a restart after that writes a fresh one while the node
// reconnects with its key.
func TestFreshInstallPairsByItself(t *testing.T) {
	e := newEnv(t, true)
	tok := e.token()
	v, _ := vault.New(e.st, make([]byte, 32))
	restart := func() {
		h, err := New(t.Context(), Options{Store: e.st, Vault: v, PairingFile: e.pairing})
		if err != nil {
			t.Fatal(err)
		}
		h.Close()
	}
	restart()
	if e.token() != tok {
		t.Fatal("a restart before any node paired replaced the secret the node is about to read")
	}
	dir := t.TempDir()
	n := e.startNodeWith(dir, "local", tok, node.DefaultPolicy(), func(c *node.Config) { c.PairingFile = e.pairing })
	eventually(t, "connect", func() bool { return e.hub.Connected("local") })
	n.stop()
	afterPair := e.token()
	restart()
	if e.token() == afterPair {
		t.Fatal("a restart after pairing kept the secret")
	}
	e.startNode(dir, "local", "", node.DefaultPolicy())
	eventually(t, "reconnect with the key", func() bool { return e.hub.Connected("local") })
	if list, _ := e.hub.List(t.Context()); len(list) != 1 {
		t.Fatalf("nodes %v", list)
	}
}

// TestOnConnect runs on every connection, the first and each reconnect, so
// the hub can refresh what reads through the node.
func TestOnConnect(t *testing.T) {
	e := newEnv(t, true)
	var mu sync.Mutex
	var names []string
	e.hub.OnConnect(func(name string) { mu.Lock(); names = append(names, name); mu.Unlock() })
	count := func() int { mu.Lock(); defer mu.Unlock(); return len(names) }
	dir := t.TempDir()
	n := e.startNode(dir, "local", e.token(), node.DefaultPolicy())
	eventually(t, "connect", func() bool { return count() == 1 })
	n.stop()
	e.startNode(dir, "local", "", node.DefaultPolicy())
	eventually(t, "reconnect", func() bool { return count() == 2 })
	mu.Lock()
	defer mu.Unlock()
	if names[0] != "local" || names[1] != "local" {
		t.Fatalf("names %v", names)
	}
}

// TestPairingFrom: the secret is accepted only from the allowed ranges.
func TestPairingFrom(t *testing.T) {
	e := newEnv(t, true)
	e.hub.o.PairingFrom = []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}
	e.startNode(t.TempDir(), "local", e.token(), node.DefaultPolicy())
	eventually(t, "the refusal", func() bool { return len(e.events("nodes.enroll")) > 0 })
	ev := e.events("nodes.enroll")[0]
	if ev.Result != audit.Refused || ev.Error == nil || !strings.Contains(ev.Error.Message, "address") {
		t.Fatalf("audit %+v", ev)
	}
	if !e.hub.pairing.Check(mustSecret(t, e.token())) {
		t.Fatal("a refused pairing spent the secret")
	}
	if !addrIn("10.1.2.3", DefaultPairingFrom) || !addrIn("::ffff:192.168.1.2", DefaultPairingFrom) || addrIn("203.0.113.9", DefaultPairingFrom) || addrIn("nonsense", DefaultPairingFrom) {
		t.Fatal("DefaultPairingFrom")
	}
}

func mustSecret(t *testing.T, tok string) string {
	t.Helper()
	s, _, err := nodeproto.ParsePairing(tok)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestNextKeySquatting: a node can't announce another node's key (or the
// hub's) as its next one, and a key is found by its holder first (review
// finding 3).
func TestNextKeySquatting(t *testing.T) {
	e := newEnv(t, true)
	e.startNode(t.TempDir(), "a", e.token(), node.DefaultPolicy())
	eventually(t, "a", func() bool { return e.hub.Connected("a") })
	e.startNode(t.TempDir(), "b", e.token(), node.DefaultPolicy())
	eventually(t, "b", func() bool { return e.hub.Connected("b") })
	a, _ := e.hub.ByName(t.Context(), "a")
	b, _ := e.hub.ByName(t.Context(), "b")
	rotate := e.hub.sessionOptions(b).Handler
	for what, pin := range map[string]string{"a's key": a.PublicKey, "b's own key": b.PublicKey, "the hub's key": e.hub.Pin()} {
		msg, _ := json.Marshal(nodeproto.RotateMsg{Next: pin})
		if _, err := rotate(t.Context(), nodeproto.VerbNodeRotate, msg, nil); nodeproto.CodeOf(err) != nodeproto.CodeConflict {
			t.Fatalf("announcing %s = %v, want conflict", what, err)
		}
	}
	k, _ := nodeproto.GenerateKey()
	pin := nodeproto.PinOf(k)
	msg, _ := json.Marshal(nodeproto.RotateMsg{Next: pin})
	if _, err := rotate(t.Context(), nodeproto.VerbNodeRotate, msg, nil); err != nil {
		t.Fatal(err)
	}
	// a can't take b's announced key either.
	if err := e.hub.reg.setNext(t.Context(), a.ID, pin); !errors.Is(err, errKeyInUse) {
		t.Fatalf("a squatting b's next key = %v", err)
	}
	// A row written before the rule: b announces a's key. a's key still
	// finds a.
	if _, err := e.st.Writer().ExecContext(t.Context(), `UPDATE nodes SET next_key = ? WHERE id = ?`, a.PublicKey, b.ID); err != nil {
		t.Fatal(err)
	}
	got, known, err := e.hub.reg.lookup(t.Context(), a.PublicKey)
	if err != nil || !known || got.ID != a.ID {
		t.Fatalf("lookup of a's key = %s %v %v", got.ID, known, err)
	}
}

// TestUnknownKeysAreLimited: refusing keys the hub doesn't know counts
// against the address, and an oversized hello is refused before the hub
// knows the key (review finding 6).
func TestUnknownKeysAreLimited(t *testing.T) {
	e := newEnv(t, true)
	p := node.DefaultPolicy()
	p.Hub.Pin = e.hub.Pin()
	e.startNode(t.TempDir(), "local", "", p)
	eventually(t, "the refusals to count", func() bool { return e.failures("127.0.0.1") > 0 })

	// A hello over HandshakeMaxFrame.
	e2 := newEnv(t, true)
	key, _ := nodeproto.GenerateKey()
	ident, _ := nodeproto.NewIdentity(key)
	ch, err := nodeproto.Dial(t.Context(), e2.url(), nodeproto.DialOptions{Identity: ident, Pins: []string{e2.hub.Pin()}})
	if err != nil {
		t.Fatal(err)
	}
	defer ch.Close()
	big := nodeproto.Hello{Name: strings.Repeat("n", nodeproto.HandshakeMaxFrame)}
	// The hub may hang up before the whole frame is written (a broken
	// pipe here): that's a refusal too.
	if err := nodeproto.SendFrame(t.Context(), ch, nodeproto.OpHello, big, nil); err == nil {
		f, err := nodeproto.RecvFrame(t.Context(), ch, 5*time.Second)
		if err == nil && (f.Op != nodeproto.OpRefused || f.Error == nil || f.Error.Code != nodeproto.CodeProtocol) {
			t.Fatalf("an oversized hello got %+v", f)
		}
	}
	eventually(t, "the failure to count", func() bool { return e2.failures("127.0.0.1") > 0 })
}

func (e *env) failures(addr string) int {
	e.hub.limit.mu.Lock()
	defer e.hub.limit.mu.Unlock()
	if a := e.hub.limit.seen[addr]; a != nil {
		return a.n
	}
	return 0
}

func TestHandshakeGate(t *testing.T) {
	g := newGate(2, 3)
	first, second, third := g.enter("a"), g.enter("a"), g.enter("a")
	if !first || !second || third {
		t.Fatal("per address")
	}
	if !g.enter("b") || g.enter("c") {
		t.Fatal("overall")
	}
	g.leave("a")
	if !g.enter("c") || g.enter("a") {
		t.Fatal("after leaving")
	}
	g.leave("a")
	g.leave("b")
	g.leave("c")
	if len(g.perAddr) != 0 || g.total != 0 {
		t.Fatalf("leaks: %v %d", g.perAddr, g.total)
	}
}

func TestRefusalLogSampling(t *testing.T) {
	now := time.Unix(1000, 0)
	s := &sampler{every: time.Minute, now: func() time.Time { return now }}
	if ok, _ := s.take(); !ok {
		t.Fatal("the first line")
	}
	for range 3 {
		if ok, _ := s.take(); ok {
			t.Fatal("logged within the minute")
		}
	}
	now = now.Add(time.Minute)
	if ok, held := s.take(); !ok || held != 3 {
		t.Fatalf("after a minute: %v %d", ok, held)
	}
}

// TestAllowedVerbs: integrations get the Docker verbs they use, by exact
// name, and nothing else (review finding 7).
func TestAllowedVerbs(t *testing.T) {
	var got []string
	c := Allow(callerFunc(func(verb string) { got = append(got, verb) }), IntegrationVerbs...)
	for _, v := range []string{"container.restart", "engine.self", "container.logs"} {
		if _, err := c.Call(t.Context(), "local", v, nil, nil); err != nil {
			t.Fatalf("%s: %v", v, err)
		}
	}
	for _, v := range []string{"container.exec", "stack.deploy", "engine.anything", "container.restartx", "hub.pin"} {
		if _, err := c.Call(t.Context(), "local", v, nil, nil); nodeproto.CodeOf(err) != nodeproto.CodeForbidden {
			t.Fatalf("%s = %v, want forbidden", v, err)
		}
	}
	if len(got) != 3 {
		t.Fatalf("passed through %v", got)
	}
}

type callerFunc func(verb string)

func (f callerFunc) Call(_ context.Context, _, verb string, _ any, _ func(json.RawMessage)) (json.RawMessage, error) {
	f(verb)
	return json.RawMessage("{}"), nil
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
	// Redeem: a wrong secret runs nothing; a refused enrollment keeps the
	// secret; a successful one spends it and writes a fresh one.
	ran := false
	if ok, err := p2.Redeem("nope-nope-nope-nope", func() error { ran = true; return nil }); ok || !errors.Is(err, errBadSecret) || ran {
		t.Fatalf("wrong secret: %v %v %v", ok, err, ran)
	}
	if ok, err := p2.Redeem(secret, func() error { return errNameTaken }); ok || !errors.Is(err, errNameTaken) || !p2.Check(secret) {
		t.Fatalf("refused enrollment: %v %v", ok, err)
	}
	if ok, err := p2.Redeem(secret, func() error { return nil }); !ok || err != nil || p2.Check(secret) {
		t.Fatalf("redeem: %v %v", ok, err)
	}
	b, _ = os.ReadFile(path)
	if next, _, err := nodeproto.ParsePairing(string(b)); err != nil || next == secret || !p2.Check(next) {
		t.Fatal("the file doesn't hold the fresh secret")
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

// TestReservedNodeNames: hooks and secrets, in any case, are refused at
// enrollment, since /api/stacks/hooks and /api/stacks/secrets would
// shadow them; the pairing secret stays unspent.
func TestReservedNodeNames(t *testing.T) {
	for _, name := range []string{"hooks", "Secrets"} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, true)
			e.startNode(t.TempDir(), name, e.token(), node.DefaultPolicy())
			eventually(t, "the refusal", func() bool { return len(e.events("nodes.enroll")) > 0 })
			ev := e.events("nodes.enroll")[0]
			if ev.Result != audit.Refused || ev.Error == nil || ev.Error.Code != nodeproto.CodeProtocol || !strings.Contains(ev.Error.Message, "can't be called") {
				t.Fatalf("audit %+v", ev)
			}
			if list, _ := e.hub.List(t.Context()); len(list) != 0 {
				t.Fatalf("enrolled: %v", list)
			}
			if !e.hub.pairing.Check(mustSecret(t, e.token())) {
				t.Fatal("a refused name spent the secret")
			}
		})
	}
	for _, ok := range []string{"hooks2", "my-secrets", "local"} {
		if reservedName(ok) {
			t.Errorf("%s is reserved", ok)
		}
	}
}

// TestPendingNodeIsAnsweredPending: a node waiting for approval is told
// so, with the pending code rather than unknown_node, and the refusal
// doesn't count against its address.
func TestPendingNodeIsAnsweredPending(t *testing.T) {
	e := newEnv(t, false)
	key, _ := nodeproto.GenerateKey()
	ident, _ := nodeproto.NewIdentity(key)
	now := time.Now().UnixMilli()
	if _, err := e.st.Writer().ExecContext(t.Context(), `INSERT INTO nodes (id, name, public_key, state, enrolled_via, created_at, updated_at)
		VALUES ('n_pending', 'far', ?, 'pending', 'token', ?, ?)`, nodeproto.PinOf(key), now, now); err != nil {
		t.Fatal(err)
	}
	ch, err := nodeproto.Dial(t.Context(), e.url(), nodeproto.DialOptions{Identity: ident, Pins: []string{e.hub.Pin()}})
	if err != nil {
		t.Fatal(err)
	}
	defer ch.Close()
	if err := nodeproto.SendFrame(t.Context(), ch, nodeproto.OpHello, nodeproto.Hello{Versions: []int{1}, Name: "far", Node: "n_pending"}, nil); err != nil {
		t.Fatal(err)
	}
	f, err := nodeproto.RecvFrame(t.Context(), ch, 5*time.Second)
	if err != nil || f.Op != nodeproto.OpRefused || f.Error == nil || f.Error.Code != nodeproto.CodePending || f.Error.Message != "this node is waiting for approval" {
		t.Fatalf("a pending node got %+v %v", f, err)
	}
	if e.hub.Connected("n_pending") || e.failures("127.0.0.1") != 0 {
		t.Fatalf("connected %v, failures %d", e.hub.Connected("n_pending"), e.failures("127.0.0.1"))
	}
}
