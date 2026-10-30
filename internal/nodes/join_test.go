package nodes

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"dash.cha.sh/internal/audit"
	"dash.cha.sh/internal/livedoc"
	"dash.cha.sh/internal/node"
	"dash.cha.sh/internal/nodeproto"
)

// clock is a settable time for the hub.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type approval struct {
	rec       Record
	addDocker bool
	by        audit.Actor
}

// joinEnv is a hub with an Inbox, a settable clock and a record of
// OnApproved calls.
type joinEnv struct {
	*env
	inbox    *livedoc.Doc
	clock    *clock
	mu       sync.Mutex
	approved []approval
}

func newJoinEnv(t *testing.T) *joinEnv {
	t.Helper()
	j := &joinEnv{inbox: livedoc.New(incidentsColl), clock: &clock{t: time.Now()}}
	j.env = newEnvWith(t, false, func(o *Options) {
		o.Inbox, o.Now = j.inbox, j.clock.now
		o.OnApproved = func(_ context.Context, rec Record, addDocker bool, by audit.Actor) {
			j.mu.Lock()
			j.approved = append(j.approved, approval{rec, addDocker, by})
			j.mu.Unlock()
		}
	})
	return j
}

func (j *joinEnv) approvals() []approval {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]approval(nil), j.approved...)
}

var owner = audit.Actor{Kind: "user", ID: "u_owner", Name: "owner"}

// token makes a join token on the hub.
func (j *joinEnv) token(name string, addDocker bool) (JoinToken, string) {
	j.t.Helper()
	t, tok, err := j.hub.CreateJoinToken(j.t.Context(), owner, name, DefaultTokenTTL, addDocker)
	if err != nil {
		j.t.Fatal(err)
	}
	return t, tok
}

// startJoining runs a node with a join token that retries quickly while
// pending.
func (j *joinEnv) startJoining(dir, name, token string) *nodeRun {
	j.t.Helper()
	return j.startNodeWith(dir, name, "", node.DefaultPolicy(), func(c *node.Config) {
		c.Token, c.PendingRetry = token, 50*time.Millisecond
	})
}

// item is a node's Inbox item, if there is one.
func (j *joinEnv) item(id string) (nodeItem, bool) {
	var in nodeItem
	raw := j.inbox.Item(incidentsColl, PendingItem(id))
	return in, raw != nil && json.Unmarshal(raw, &in) == nil
}

func slogTo(w io.Writer) *slog.Logger { return slog.New(slog.NewTextHandler(w, nil)) }

// syncBuffer is a bytes.Buffer a node's goroutine writes while the test reads.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func (j *joinEnv) enrolled(name string) Record {
	j.t.Helper()
	var rec Record
	eventually(j.t, name+" to enroll", func() bool {
		r, err := j.hub.ByName(j.t.Context(), name)
		rec = r
		return err == nil
	})
	return rec
}

func (j *joinEnv) post(path string) (int, []byte) {
	j.t.Helper()
	resp, err := http.Post(j.srv.URL+path, "application/json", strings.NewReader("{}"))
	if err != nil {
		j.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// TestJoinTokenEnrollsPending is the whole flow: a token enrolls a node as
// pending once, the node waits without counting against its address, the
// Inbox says so, an approve opens its next session, and the item resolves.
func TestJoinTokenEnrollsPending(t *testing.T) {
	j := newJoinEnv(t)
	tok, secret := j.token("alpha", true)
	if !strings.HasPrefix(secret, "cdj_") || !strings.HasSuffix(secret, "."+j.hub.Pin()) {
		t.Fatalf("token %q", secret)
	}
	dir := t.TempDir()
	n := j.startJoining(dir, "alpha", secret)
	rec := j.enrolled("alpha")
	if rec.State != StatePending || rec.EnrolledVia != ViaToken || rec.PublicKey != n.ident.Current().Pin {
		t.Fatalf("record %+v", rec)
	}
	// It keeps asking, quietly: not connected, not limited, not in the graph.
	time.Sleep(300 * time.Millisecond)
	if j.hub.Connected("alpha") || n.client.Connected() || j.failures("127.0.0.1") != 0 {
		t.Fatalf("connected %v, failures %d", j.hub.Connected("alpha"), j.failures("127.0.0.1"))
	}
	if _, ok := j.g.Get(ResourceID(rec.ID)); ok {
		t.Fatal("a pending node is in the graph")
	}
	list, _ := j.hub.List(t.Context())
	if len(list) != 1 || list[0].JoinToken == nil || list[0].JoinToken.ID != tok.ID || list[0].JoinToken.CreatedByName != "owner" ||
		list[0].Address != "127.0.0.1" || list[0].Version != "test" || len(list[0].Verbs) != 2 {
		t.Fatalf("list %+v", list)
	}
	toks, _ := j.hub.JoinTokens().List(t.Context())
	if len(toks) != 1 || toks[0].State != TokenUsed || toks[0].NodeID != rec.ID || toks[0].NodeName != "alpha" {
		t.Fatalf("tokens %+v", toks)
	}
	enrolls := j.events("nodes.enroll")
	if len(enrolls) != 1 || enrolls[0].Result != audit.OK || !strings.Contains(string(enrolls[0].Params), `"via":"token"`) ||
		!strings.Contains(string(enrolls[0].Params), `"token":"`+tok.ID+`"`) || !strings.Contains(string(enrolls[0].Params), `"address":"127.0.0.1"`) {
		t.Fatalf("audit %+v", enrolls)
	}
	in, ok := j.item(rec.ID)
	if !ok || in.Status != "open" || in.Class != "node" || in.Severity != "attention" || !strings.Contains(in.Summary, rec.Fingerprint) ||
		in.Facts["token_by"] != "owner" || in.Suggested["id"] != ReviewAction {
		t.Fatalf("inbox %+v", in)
	}

	// A restart while pending stays pending: the same key, no new enrollment.
	n.stop()
	n = j.startJoining(dir, "alpha", secret)
	time.Sleep(200 * time.Millisecond)
	if l, _ := j.hub.List(t.Context()); len(l) != 1 || l[0].State != StatePending || len(j.events("nodes.enroll")) != 1 {
		t.Fatalf("after a restart: %+v", l)
	}

	// Approve, over HTTP: the next retry opens the session.
	status, b := j.post("/api/nodes/" + rec.ID + "/approve")
	if status != http.StatusOK || !strings.Contains(string(b), `"state":"approved"`) {
		t.Fatalf("approve: %d %s", status, b)
	}
	eventually(t, "the session", func() bool { return j.hub.Connected(rec.ID) && n.client.Connected() })
	if st := n.ident.State(); st.Node != rec.ID {
		t.Fatalf("the node saved %q", st.Node)
	}
	if got := j.approvals(); len(got) != 1 || got[0].rec.ID != rec.ID || !got[0].addDocker || got[0].by.ID != "u_owner" {
		t.Fatalf("OnApproved %+v", got)
	}
	if in, _ := j.item(rec.ID); in.Status != "resolved" || in.Resolution != "Approved by owner." {
		t.Fatalf("inbox after approve %+v", in)
	}
	if ev := j.events("nodes.approve"); len(ev) != 1 || ev[0].Actor.ID != "u_owner" || ev[0].Tier != "T3" {
		t.Fatalf("audit %+v", ev)
	}
	if _, ok := j.g.Get(ResourceID(rec.ID)); !ok {
		t.Fatal("the approved node isn't in the graph")
	}
	if status, _ := j.post("/api/nodes/" + rec.ID + "/approve"); status != http.StatusConflict {
		t.Fatalf("approving an approved node: %d", status)
	}
	if status, _ := j.post("/api/nodes/n_nobody/approve"); status != http.StatusNotFound {
		t.Fatalf("approving nobody: %d", status)
	}
	if len(j.approvals()) != 1 {
		t.Fatal("OnApproved ran twice")
	}
}

// TestJoinTokenRefusals: a token enrolls one node; a second node with it,
// and expired, revoked and wrong tokens are refused (and counted against
// the address); a token for alpha refuses a node named beta, and stays
// usable for alpha.
func TestJoinTokenRefusals(t *testing.T) {
	j := newJoinEnv(t)
	refused := func(what, token, name, code string) {
		t.Helper()
		before := j.refusedWith(code)
		nodes, _ := j.hub.List(t.Context())
		j.startJoining(t.TempDir(), name, token)
		eventually(t, what, func() bool { return j.refusedWith(code) > before })
		if after, _ := j.hub.List(t.Context()); len(after) != len(nodes) {
			t.Fatalf("%s: %s enrolled", what, name)
		}
	}

	_, alpha := j.token("alpha", false)
	refused("a node named beta with alpha's token", alpha, "beta", nodeproto.CodeConflict)
	j.startJoining(t.TempDir(), "alpha", alpha)
	j.enrolled("alpha")
	refused("a second node with a spent token", alpha, "alpha", nodeproto.CodeUnknownNode)

	_, any := j.token("", false)
	j.hub.limit.succeeded("127.0.0.1")
	t2, revoked := j.token("", false)
	if _, err := j.hub.RevokeJoinToken(t.Context(), owner, t2.ID); err != nil {
		t.Fatal(err)
	}
	refused("a revoked token", revoked, "gamma", nodeproto.CodeUnknownNode)
	j.hub.limit.succeeded("127.0.0.1")
	_, expired := j.token("", false)
	j.clock.add(DefaultTokenTTL + time.Second)
	refused("an expired token", expired, "delta", nodeproto.CodeUnknownNode)
	j.hub.limit.succeeded("127.0.0.1")
	wrong := nodeproto.FormatJoin(strings.Repeat("w", 43), j.hub.Pin())
	refused("a token the hub never made", wrong, "epsilon", nodeproto.CodeUnknownNode)
	if j.failures("127.0.0.1") == 0 {
		t.Fatal("bad tokens don't count against the address")
	}
	// The unnamed token made before the clock moved has expired too.
	j.hub.limit.succeeded("127.0.0.1")
	refused("another expired token", any, "zeta", nodeproto.CodeUnknownNode)
}

// TestJoinTokenOnlyToItsHub: a node holding a token never sends it to a
// hub that doesn't prove the token's key, even one its policy trusts.
func TestJoinTokenOnlyToItsHub(t *testing.T) {
	real := newJoinEnv(t)
	impostor := newJoinEnv(t)
	_, tok := real.token("alpha", false)

	// It dials the impostor: the TLS handshake fails before any frame.
	ident, _ := node.OpenIdentity(filepath.Join(t.TempDir(), "identity"))
	d, _ := node.NewDispatcher(node.DefaultPolicy(), echoModule{})
	c, err := node.NewClient(node.Config{HubURL: impostor.url(), Name: "alpha", Identity: ident, Dispatcher: d, Token: tok})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	c.Run(ctx)
	cancel()

	// Its policy pins the impostor: the channel opens, but the token's
	// secret isn't sent, since that isn't the key the token names.
	p := node.DefaultPolicy()
	p.Hub.Pin = impostor.hub.Pin()
	log := &syncBuffer{}
	impostor.startNodeWith(t.TempDir(), "alpha", "", p, func(c *node.Config) {
		c.Token, c.Log = tok, slogTo(log)
	})
	eventually(t, "the node to hold the token back", func() bool { return strings.Contains(log.String(), "the token wasn't sent") })
	if len(impostor.events("nodes.enroll")) != 0 || len(real.events("nodes.enroll")) != 0 {
		t.Fatal("a hub got the token")
	}
	if toks, _ := real.hub.JoinTokens().List(t.Context()); toks[0].State != TokenActive {
		t.Fatalf("the token was spent: %+v", toks[0])
	}
}

// TestRejectPending: revoking a pending node is Reject: it's refused for
// good and its Inbox item resolves.
func TestRejectPending(t *testing.T) {
	j := newJoinEnv(t)
	_, tok := j.token("", true)
	j.startJoining(t.TempDir(), "alpha", tok)
	rec := j.enrolled("alpha")
	eventually(t, "the Inbox item", func() bool { in, ok := j.item(rec.ID); return ok && in.Status == "open" })
	if status, _ := j.post("/api/nodes/" + rec.ID + "/revoke"); status != http.StatusOK {
		t.Fatalf("reject: %d", status)
	}
	if in, _ := j.item(rec.ID); in.Status != "resolved" || in.Resolution != "Rejected by owner." {
		t.Fatalf("inbox %+v", in)
	}
	eventually(t, "the revoked refusal", func() bool {
		r, _ := j.hub.Get(t.Context(), rec.ID)
		return r.State == StateRevoked
	})
	if status, _ := j.post("/api/nodes/" + rec.ID + "/approve"); status != http.StatusConflict {
		t.Fatalf("approving a rejected node: %d", status)
	}
	if len(j.approvals()) != 0 {
		t.Fatal("OnApproved ran for a rejected node")
	}
}

// TestPendingExpiry: a node nobody approves within 7 days is revoked by
// the hub (revoked_by expired), audited, and its item resolves.
func TestPendingExpiry(t *testing.T) {
	j := newJoinEnv(t)
	_, tok := j.token("", false)
	n := j.startJoining(t.TempDir(), "alpha", tok)
	rec := j.enrolled("alpha")
	eventually(t, "the Inbox item", func() bool { _, ok := j.item(rec.ID); return ok })
	n.stop()
	j.clock.add(PendingExpiry - time.Minute)
	j.hub.sweep(t.Context())
	if r, _ := j.hub.Get(t.Context(), rec.ID); r.State != StatePending {
		t.Fatalf("expired early: %+v", r)
	}
	// Spent tokens stay listed for 7 days, then go.
	if toks, _ := j.hub.JoinTokens().List(t.Context()); len(toks) != 1 {
		t.Fatalf("tokens %+v", toks)
	}
	j.clock.add(2 * time.Minute)
	j.hub.sweep(t.Context())
	r, _ := j.hub.Get(t.Context(), rec.ID)
	if r.State != StateRevoked || r.RevokedBy != ExpiredBy {
		t.Fatalf("after 7 days: %+v", r)
	}
	if ev := j.events("nodes.expire"); len(ev) != 1 || ev[0].Target != "node:"+rec.ID {
		t.Fatalf("audit %+v", ev)
	}
	if in, _ := j.item(rec.ID); in.Status != "resolved" || in.ResolvedBy != "auto" {
		t.Fatalf("inbox %+v", in)
	}
	if toks, _ := j.hub.JoinTokens().List(t.Context()); len(toks) != 0 {
		t.Fatalf("a week-old spent token is still listed: %+v", toks)
	}
}

// TestJoinTokenRoutes: the token API, the one-time answer, and the literal
// tokens segment beside /api/nodes/{id}.
func TestJoinTokenRoutes(t *testing.T) {
	j := newJoinEnv(t)
	do := func(method, path, body string) (int, http.Header, []byte) {
		t.Helper()
		req, _ := http.NewRequest(method, j.srv.URL+path, strings.NewReader(body))
		req.Host = "dash.lab.example:3000"
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, resp.Header, b
	}
	for _, bad := range []string{`{"ttl_minutes": 1441}`, `{"ttl_minutes": -1}`, `{"name": "bad name"}`, `{"name": "hooks"}`} {
		if status, _, b := do("POST", "/api/nodes/tokens", bad); status != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d %s", bad, status, b)
		}
	}
	status, hdr, b := do("POST", "/api/nodes/tokens", `{"name": "alpha", "ttl_minutes": 60}`)
	var made struct {
		Token   JoinToken `json:"token"`
		Secret  string    `json:"secret"`
		HubURL  string    `json:"hub_url"`
		Env     string    `json:"env"`
		Command string    `json:"command"`
	}
	if status != http.StatusCreated || json.Unmarshal(b, &made) != nil || hdr.Get("Cache-Control") != "no-store" {
		t.Fatalf("create: %d %s", status, b)
	}
	if !made.Token.AddDocker || made.Token.Name != "alpha" || made.Token.State != TokenActive || made.Token.CreatedByName != "owner" ||
		made.Token.ExpiresAt.Sub(made.Token.CreatedAt) != time.Hour {
		t.Fatalf("token %+v", made.Token)
	}
	if made.HubURL != "ws://dash.lab.example:3696/node" || made.Command != NodeCommand ||
		made.Env != "CHADASH_HUB=ws://dash.lab.example:3696/node\nCHADASH_NODE_NAME=alpha\nCHADASH_NODE_TOKEN="+made.Secret+"\n" {
		t.Fatalf("answer %+v", made)
	}
	if _, _, err := nodeproto.ParseJoin(made.Secret); err != nil {
		t.Fatal(err)
	}
	if ev := j.events("nodes.token.create"); len(ev) != 1 || ev[0].Target != "node-token:"+made.Token.ID || strings.Contains(string(ev[0].Params), made.Secret[4:20]) {
		t.Fatalf("audit %+v", ev)
	}
	// The list never carries the secret, nor its hash.
	status, _, b = do("GET", "/api/nodes/tokens", "")
	secret, _, _ := nodeproto.ParseJoin(made.Secret)
	if status != http.StatusOK || !strings.Contains(string(b), made.Token.ID) || strings.Contains(string(b), secret) || strings.Contains(string(b), hashSecret(secret)) {
		t.Fatalf("list: %d %s", status, b)
	}
	// Revoke: once.
	if status, _, b := do("DELETE", "/api/nodes/tokens/"+made.Token.ID, ""); status != http.StatusOK || !strings.Contains(string(b), `"state":"revoked"`) {
		t.Fatalf("revoke: %d %s", status, b)
	}
	if status, _, _ := do("DELETE", "/api/nodes/tokens/"+made.Token.ID, ""); status != http.StatusConflict {
		t.Fatalf("revoke again: %d", status)
	}
	if status, _, _ := do("DELETE", "/api/nodes/tokens/njt_nope", ""); status != http.StatusNotFound {
		t.Fatalf("revoke nothing: %d", status)
	}
	if ev := j.events("nodes.token.revoke"); len(ev) != 1 {
		t.Fatalf("audit %+v", ev)
	}
	// add_docker off when asked.
	_, _, b = do("POST", "/api/nodes/tokens", `{"add_docker": false}`)
	made.Token, made.Env = JoinToken{}, ""
	if json.Unmarshal(b, &made) != nil || made.Token.AddDocker || !strings.Contains(made.Env, "CHADASH_NODE_NAME=<this host>\n") {
		t.Fatalf("add_docker off: %s", b)
	}
	// "tokens" is a literal segment: never a node ID.
	if status, _, b := do("POST", "/api/nodes/tokens/approve", ""); status == http.StatusOK {
		t.Fatalf("POST /api/nodes/tokens/approve: %d %s", status, b)
	}
	if status, _, b := do("POST", "/api/nodes/tokens/revoke", ""); status != http.StatusNotFound || strings.Contains(string(b), "already") {
		t.Fatalf("POST /api/nodes/tokens/revoke: %d %s", status, b)
	}
}

func TestHubURL(t *testing.T) {
	for _, c := range []struct{ public, host, want string }{
		{"https://dash.lab.example", "192.0.2.7:3696", "wss://dash.lab.example/node"},
		{"https://dash.lab.example:8443", "", "wss://dash.lab.example:8443/node"},
		{"http://dash.lab.example:3000", "", "ws://dash.lab.example:3000/node"},
		{"", "192.0.2.7:3696", "ws://192.0.2.7:3696/node"},
		{"", "dash.lab.example", "ws://dash.lab.example:3696/node"},
		{"", "[fd00::5]:3000", "ws://[fd00::5]:3696/node"},
	} {
		if got := HubURL(c.public, c.host); got != c.want {
			t.Errorf("HubURL(%q, %q) = %q, want %q", c.public, c.host, got, c.want)
		}
	}
}

func TestTTL(t *testing.T) {
	if d, err := TTL(0); err != nil || d != 15*time.Minute {
		t.Fatal(d, err)
	}
	if d, err := TTL(24 * 60); err != nil || d != 24*time.Hour {
		t.Fatal(d, err)
	}
	if _, err := TTL(24*60 + 1); err == nil {
		t.Fatal("over a day")
	}
}
