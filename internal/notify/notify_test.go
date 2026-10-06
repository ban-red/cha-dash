package notify

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/audit"
	"dash.cha.sh/internal/authz"
	"dash.cha.sh/internal/livedoc"
	"dash.cha.sh/internal/store"
	"dash.cha.sh/internal/topics"
	"dash.cha.sh/internal/vault"
)

// ── harness ──────────────────────────────────────────────────────────────

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

type env struct {
	t     *testing.T
	st    *store.Store
	v     *vault.Vault
	inbox *livedoc.Doc
	clock *fakeClock
	logs  *lockedBuf
	audit *audit.Log
	s     *Service
	view  func(ctx context.Context, role string) topics.InboxView
	push  *http.Client
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "chadash.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	v, err := vault.New(st, bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range [][2]string{{"u_owner", "owner"}, {"u_admin", "admin"}, {"u_view", "viewer"}, {"u_home", "household"}} {
		if _, err := st.Writer().Exec(`INSERT INTO users (id, username, role, created_at, updated_at) VALUES (?, ?, ?, 0, 0)`, u[0], strings.TrimPrefix(u[0], "u_"), u[1]); err != nil {
			t.Fatal(err)
		}
	}
	e := &env{t: t, st: st, v: v, inbox: livedoc.New(topics.Incidents), clock: &fakeClock{t: time.Date(2026, 10, 6, 2, 0, 0, 0, time.UTC)}, logs: &lockedBuf{}, audit: audit.New(st)}
	e.start()
	return e
}

// start (re)starts the service on the same database: a restart.
func (e *env) start() {
	e.t.Helper()
	s, err := New(e.t.Context(), Options{Store: e.st, Vault: e.v, Inbox: e.inbox, InboxView: e.view, PublicURL: "https://dash.lab.example", Version: "test",
		Log: slog.New(slog.NewTextHandler(e.logs, &slog.HandlerOptions{Level: slog.LevelDebug})), Now: e.clock.Now})
	if err != nil {
		e.t.Fatal(err)
	}
	s.allowLoopbackPush = true
	if e.push != nil {
		s.pushClient = e.push
	}
	s.Audit = &authz.Recorder{Log: e.audit}
	e.s = s
}

func (e *env) step() { e.s.step(e.t.Context()) }

// tick advances the clock and runs the loop once.
func (e *env) tick(d time.Duration) {
	e.clock.Advance(d)
	e.step()
}

// settle runs the loop past the group window, so queued messages go out.
func (e *env) settle() {
	e.step()
	e.tick(DefaultGroupWindow)
}

type incOpt func(map[string]any)

func withCategory(c string) incOpt  { return func(m map[string]any) { m["category"] = c } }
func withScenario(s string) incOpt  { return func(m map[string]any) { m["scenario"] = s } }
func withTarget(tg string) incOpt   { return func(m map[string]any) { m["target"] = tg } }
func withTitle(title string) incOpt { return func(m map[string]any) { m["title"] = title } }

func inc(id, class, severity, status, phase string, opts ...incOpt) map[string]any {
	m := map[string]any{
		"id": id, "scenario": "test", "class": class, "severity": severity, "status": status, "phase": phase,
		"title": id + " is broken", "summary": "Something about " + id + ".", "target": "app:" + id,
		"opened_at": "2026-10-06T01:58:00Z", "updated_at": "2026-10-06T01:58:00Z", "causes": []any{}, "blast_radius": []any{}, "resolves_when": "",
	}
	if status == "resolved" {
		m["resolved_at"] = "2026-10-06T02:30:00Z"
	}
	for _, o := range opts {
		o(m)
	}
	return m
}

func (e *env) put(incs ...map[string]any) {
	e.t.Helper()
	if _, err := e.inbox.Update(func(tx *livedoc.Tx) error {
		for _, m := range incs {
			tx.Put(topics.Incidents, m["id"].(string), m)
		}
		return nil
	}); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) remove(ids ...string) {
	e.inbox.Update(func(tx *livedoc.Tx) error {
		for _, id := range ids {
			tx.Delete(topics.Incidents, id)
		}
		return nil
	})
}

func ptr[T any](v T) *T { return &v }

// ── fake ntfy ────────────────────────────────────────────────────────────

type ntfyMsg struct {
	Topic    string   `json:"topic"`
	Title    string   `json:"title"`
	Message  string   `json:"message"`
	Priority int      `json:"priority"`
	Tags     []string `json:"tags"`
	Click    string   `json:"click"`
	auth     string
}

type ntfyServer struct {
	*httptest.Server
	mu     sync.Mutex
	got    []ntfyMsg
	status atomic.Int32
}

func newNtfy(t *testing.T) *ntfyServer {
	n := &ntfyServer{}
	n.status.Store(200)
	n.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m ntfyMsg
		if r.Method != http.MethodPost || r.URL.Path != "/" || json.NewDecoder(r.Body).Decode(&m) != nil {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		m.auth = r.Header.Get("Authorization")
		n.mu.Lock()
		n.got = append(n.got, m)
		n.mu.Unlock()
		w.WriteHeader(int(n.status.Load()))
	}))
	t.Cleanup(n.Close)
	return n
}

func (n *ntfyServer) msgs() []ntfyMsg {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]ntfyMsg(nil), n.got...)
}

const (
	fakeNtfyToken     = "tk_fakentfytoken0123456789abcdefgh"
	fakeWebhookSecret = "whsec_fakesigningsecret0123456789ABCDEF"
)

func (e *env) addNtfy(srv *ntfyServer) Channel {
	e.t.Helper()
	c, err := e.s.CreateChannel(e.t.Context(), ChannelInput{Kind: ChannelNtfy, Name: ptr("Phones"),
		Config:  json.RawMessage(`{"server":"` + srv.URL + `","topic":"lab-alerts"}`),
		Secrets: map[string]*string{"token": ptr(fakeNtfyToken)}})
	if err != nil {
		e.t.Fatal(err)
	}
	return c
}

// ── routing ──────────────────────────────────────────────────────────────

func TestDownNotifiesAtOnceAndResolves(t *testing.T) {
	e := newEnv(t)
	n := newNtfy(t)
	e.addNtfy(n)

	e.put(inc("pbs", "problem", "down", "open", "open", withTitle("PBS can't reach its datastore")))
	e.step()
	if len(n.msgs()) != 0 {
		t.Fatal("sent before the group window")
	}
	e.tick(DefaultGroupWindow)
	got := n.msgs()
	if len(got) != 1 {
		t.Fatalf("got %d messages", len(got))
	}
	m := got[0]
	if m.Topic != "lab-alerts" || m.Title != "PBS can't reach its datastore" || m.Priority != 4 || m.auth != "Bearer "+fakeNtfyToken ||
		m.Click != "https://dash.lab.example/?incident=pbs" || !strings.Contains(m.Message, "Down since 01:58 UTC.") {
		t.Fatalf("message = %+v", m)
	}

	// Seen again (every inbox tick): nothing more.
	e.put(inc("pbs", "problem", "down", "open", "open", withTitle("PBS can't reach its datastore")))
	e.settle()
	if len(n.msgs()) != 1 {
		t.Fatalf("repeated: %d", len(n.msgs()))
	}

	// Resolved: down's rule says so by default.
	e.put(inc("pbs", "problem", "info", "resolved", "resolved", withTitle("PBS can't reach its datastore")))
	e.settle()
	got = n.msgs()
	if len(got) != 2 || got[1].Title != "Resolved: PBS can't reach its datastore" || got[1].Priority != 2 || got[1].Message != "Resolved at 02:30 UTC." {
		t.Fatalf("resolution = %+v", got)
	}
}

func TestAttentionWaitsForItsGrace(t *testing.T) {
	e := newEnv(t)
	n := newNtfy(t)
	e.addNtfy(n)

	e.put(inc("disk", "problem", "attention", "open", "open"))
	e.settle()
	e.tick(3 * time.Minute)
	if len(n.msgs()) != 0 {
		t.Fatal("attention notified inside its grace")
	}
	// A flap: it settles to info, then comes back. The grace starts again.
	e.put(inc("disk", "problem", "info", "open", "recovering"))
	e.tick(time.Minute)
	e.put(inc("disk", "problem", "attention", "open", "open"))
	e.step()
	e.tick(4 * time.Minute)
	if len(n.msgs()) != 0 {
		t.Fatal("a flap didn't restart the grace")
	}
	e.tick(time.Minute + time.Second)
	e.tick(DefaultGroupWindow)
	got := n.msgs()
	if len(got) != 1 || got[0].Priority != 3 || got[0].Title != "disk is broken" {
		t.Fatalf("got %+v", got)
	}
	// Resolving an attention problem says nothing by default.
	e.put(inc("disk", "problem", "info", "resolved", "resolved"))
	e.settle()
	if len(n.msgs()) != 1 {
		t.Fatalf("attention's resolution was sent: %+v", n.msgs())
	}
}

func TestAttentionBeingFixedWaits(t *testing.T) {
	e := newEnv(t)
	n := newNtfy(t)
	e.addNtfy(n)
	e.put(inc("ct", "problem", "attention", "open", "restarting"))
	e.settle()
	e.tick(10 * time.Minute)
	e.tick(DefaultGroupWindow)
	if len(n.msgs()) != 0 {
		t.Fatal("notified while a fix was running")
	}
}

func TestHintsAndQuietThingsNeverNotify(t *testing.T) {
	e := newEnv(t)
	n := newNtfy(t)
	e.addNtfy(n)
	e.put(
		inc("hint", "problem", "info", "open", "open"),                                         // standing advice
		inc("sec", "problem", "info", "open", "open", withCategory("security")),                // Relaxed: every finding is info
		inc("sec2", "problem", "info", "open", "recovering", withCategory("security")),         // still a hint, whatever its phase
		inc("settling", "problem", "info", "open", "recovering"),                               // settling by itself
		inc("merge", "suggestion", "info", "open", "open"),                                     // the correlator's question
		inc("old", "problem", "down", "resolved", "resolved"),                                  // already over when first seen
		inc("repairing", "repair", "attention", "open", "fixing"),                              // the user is already on it
		inc("sugg-down", "suggestion", "down", "open", "open", withTarget("app:a")),            // suggestions, whatever they say
		inc("sec-attn-later", "problem", "info", "open", "open", withCategory("security")),     // see below
		inc("info-in-fixing", "problem", "info", "open", "applying", withScenario("updating")), // settling
	)
	e.settle()
	e.tick(time.Hour)
	e.tick(DefaultGroupWindow)
	if got := n.msgs(); len(got) != 0 {
		t.Fatalf("notified: %+v", got)
	}

	// Under Standard the posture publishes a finding at attention: it's then
	// an attention problem like any other, after its grace.
	e.put(inc("sec-attn-later", "problem", "attention", "open", "open", withCategory("security")))
	e.step()
	e.tick(5 * time.Minute)
	e.tick(DefaultGroupWindow)
	if got := n.msgs(); len(got) != 1 || got[0].Title != "sec-attn-later is broken" {
		t.Fatalf("got %+v", got)
	}
}

func TestRepairsAndStuckStacks(t *testing.T) {
	e := newEnv(t)
	n := newNtfy(t)
	e.addNtfy(n)
	e.put(inc("i_plex.repair", "repair", "attention", "open", "open", withTitle("Plex rejected its token")))
	e.settle()
	e.put(inc("stack.local.media.stuck", "problem", "down", "open", "open", withScenario("stack-stuck"), withTitle("media is stuck")))
	e.settle()
	got := n.msgs()
	if len(got) != 2 || got[0].Title != "Plex rejected its token" || !strings.Contains(got[0].Message, "Reconnect it in Settings → Integrations.") ||
		got[1].Title != "media is stuck" || got[1].Priority != 4 || !slicesContain(got[1].Tags, KindStuck) {
		t.Fatalf("got %+v", got)
	}
	// The stuck stack resolves: its rule sends resolutions by default.
	e.put(inc("stack.local.media.stuck", "problem", "down", "resolved", "resolved", withScenario("stack-stuck"), withTitle("media is stuck")))
	e.settle()
	if got := n.msgs(); len(got) != 3 || got[2].Title != "Resolved: media is stuck" {
		t.Fatalf("got %+v", got)
	}
}

func slicesContain(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func TestEscalationNotifiesOnceAtDown(t *testing.T) {
	e := newEnv(t)
	n := newNtfy(t)
	e.addNtfy(n)
	e.put(inc("ups", "problem", "attention", "open", "open"))
	e.settle()
	e.put(inc("ups", "problem", "down", "open", "open"))
	e.settle()
	e.tick(10 * time.Minute)
	e.tick(DefaultGroupWindow)
	got := n.msgs()
	if len(got) != 1 || got[0].Priority != 4 {
		t.Fatalf("got %+v", got)
	}
	// Back to attention, past the grace: it was already down, so no more.
	e.put(inc("ups", "problem", "attention", "open", "open"))
	e.tick(10 * time.Minute)
	e.tick(DefaultGroupWindow)
	if len(n.msgs()) != 1 {
		t.Fatalf("got %+v", n.msgs())
	}
}

func TestRestartDoesntRepeat(t *testing.T) {
	e := newEnv(t)
	n := newNtfy(t)
	e.addNtfy(n)
	e.put(inc("pbs", "problem", "down", "open", "open"))
	e.settle()
	if len(n.msgs()) != 1 {
		t.Fatal("not sent")
	}
	e.start() // a new process: the detector forgets, the database doesn't
	e.settle()
	if len(n.msgs()) != 1 {
		t.Fatalf("repeated after a restart: %+v", n.msgs())
	}
	// It resolves and opens again: a new notification.
	e.put(inc("pbs", "problem", "down", "resolved", "resolved"))
	e.settle()
	e.put(inc("pbs", "problem", "down", "open", "open"))
	e.settle()
	if got := n.msgs(); len(got) != 3 || got[2].Title != "pbs is broken" {
		t.Fatalf("got %+v", got)
	}
	// Removed while open: nothing to say.
	e.remove("pbs")
	e.settle()
	if len(n.msgs()) != 3 {
		t.Fatal("a removal notified")
	}
}

func TestManyAtOnceAreGrouped(t *testing.T) {
	e := newEnv(t)
	n := newNtfy(t)
	e.addNtfy(n)
	var incs []map[string]any
	for _, h := range []string{"pluto", "mars", "io", "homeassistant"} {
		incs = append(incs, inc("backup-"+h, "problem", "down", "open", "open", withTitle("Backup of "+h+" failed")))
	}
	e.put(incs...)
	e.step()
	e.tick(2 * time.Second)
	e.put(inc("i_plex.repair", "repair", "attention", "open", "open"))
	e.step()
	e.tick(DefaultGroupWindow)
	got := n.msgs()
	if len(got) != 1 {
		t.Fatalf("got %d messages: %+v", len(got), got)
	}
	g := got[0]
	if g.Title != "5 new problems: 4 down, 1 integration to reconnect" || g.Priority != 4 || g.Click != "https://dash.lab.example/?inbox=1" ||
		!strings.Contains(g.Message, "· Backup of io failed") || !slicesContain(g.Tags, KindGroup) {
		t.Fatalf("group = %+v", g)
	}
	// They resolve together too.
	for _, m := range incs {
		m["status"], m["phase"], m["resolved_at"] = "resolved", "resolved", "2026-10-06T03:10:00Z"
	}
	e.put(incs...)
	e.settle()
	if got := n.msgs(); len(got) != 2 || got[1].Title != "4 problems resolved" {
		t.Fatalf("got %+v", got)
	}
}

func TestRulesTurnOffAndRoute(t *testing.T) {
	e := newEnv(t)
	n := newNtfy(t)
	other := newNtfy(t)
	e.addNtfy(n)
	c2, err := e.s.CreateChannel(t.Context(), ChannelInput{Kind: ChannelNtfy, Name: ptr("Desk"), Config: json.RawMessage(`{"server":"` + other.URL + `","topic":"desk"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.UpdateRule(t.Context(), RuleRepair, RuleUpdate{Enabled: ptr(false)}, "owner"); err != nil {
		t.Fatal(err)
	}
	only := json.RawMessage(`["` + c2.ID + `"]`)
	if _, err := e.s.UpdateRule(t.Context(), RuleDown, RuleUpdate{Channels: &only}, "owner"); err != nil {
		t.Fatal(err)
	}
	e.put(inc("i_x.repair", "repair", "attention", "open", "open"), inc("pbs", "problem", "down", "open", "open"))
	e.settle()
	if len(n.msgs()) != 0 || len(other.msgs()) != 1 || other.msgs()[0].Topic != "desk" || other.msgs()[0].auth != "" {
		t.Fatalf("phones %+v, desk %+v", n.msgs(), other.msgs())
	}
	// T4 can't be turned off.
	if _, err := e.s.UpdateRule(t.Context(), RuleAction, RuleUpdate{Enabled: ptr(false)}, "owner"); !errors.Is(err, errLocked) {
		t.Fatalf("err = %v", err)
	}
	if _, err := e.s.UpdateRule(t.Context(), RuleAttention, RuleUpdate{GraceMinutes: ptr(0)}, "owner"); err == nil {
		t.Fatal("a zero grace was accepted")
	}
	bad := json.RawMessage(`["nc_nope"]`)
	if _, err := e.s.UpdateRule(t.Context(), RuleDown, RuleUpdate{Channels: &bad}, "owner"); err == nil {
		t.Fatal("an unknown channel was accepted")
	}
	// Deleting a channel takes it out of the rules.
	if _, err := e.s.DeleteChannel(t.Context(), c2.ID); err != nil {
		t.Fatal(err)
	}
	for _, r := range e.s.Rules() {
		if r.ID == RuleDown && (r.Channels == nil || len(r.Channels) != 0) {
			t.Fatalf("down's channels = %v", r.Channels)
		}
	}
	// Settings survive a restart.
	e.start()
	for _, r := range e.s.Rules() {
		if r.ID == RuleRepair && r.Enabled {
			t.Fatal("repair is on again after a restart")
		}
	}
}

// ── critical actions ─────────────────────────────────────────────────────

func t4(state string, tier action.Tier) action.TaskDone {
	fin := time.Date(2026, 10, 6, 2, 0, 5, 0, time.UTC)
	t := action.Task{ID: "t_1", Action: "proxmox.node.shutdown", Label: "Shut down io", Target: "proxmox:i_x/node/io", State: state, FinishedAt: &fin,
		Actor: action.TaskActor{Kind: "user", Name: "sam"}, Audit: ptr("ae_1")}
	if state == action.StateFailed {
		t.Error = &action.Error{Code: "failed", Message: "io didn't answer."}
	}
	return action.TaskDone{Task: t, Tier: tier, Actor: action.Actor{Kind: "user", ID: "u_owner", Name: "sam", Role: "owner"}}
}

func TestEveryT4ReachesTheAdmins(t *testing.T) {
	e := newEnv(t)
	n := newNtfy(t)
	e.addNtfy(n)
	e.s.TaskDone(t4(action.StateSucceeded, action.T3)) // below T4: nothing
	e.s.TaskDone(t4(action.StateFailed, action.T4))
	e.settle()
	got := n.msgs()
	if len(got) != 1 || got[0].Title != "Critical action: Shut down io" || got[0].Message != "sam ran it at 02:00 UTC. It failed: io didn't answer." ||
		got[0].Click != "https://dash.lab.example/settings/audit" || got[0].Priority != 4 {
		t.Fatalf("got %+v", got)
	}
}

// ── Web Push ─────────────────────────────────────────────────────────────

// device is a browser's side of a push subscription.
type device struct {
	priv *ecdh.PrivateKey
	auth []byte
}

func newDevice() device {
	k, _ := ecdh.P256().GenerateKey(rand.Reader)
	a := make([]byte, 16)
	_, _ = rand.Read(a)
	return device{priv: k, auth: a}
}

func (d device) input(endpoint string) SubscribeInput {
	var in SubscribeInput
	in.Endpoint = endpoint
	in.Keys.P256dh = base64.RawURLEncoding.EncodeToString(d.priv.PublicKey().Bytes())
	in.Keys.Auth = base64.RawURLEncoding.EncodeToString(d.auth)
	in.Label = "Firefox on Linux"
	return in
}

// decrypt is the browser's half of RFC 8291.
func (d device) decrypt(t *testing.T, body []byte) []byte {
	t.Helper()
	if len(body) < 86 {
		t.Fatalf("body is %d bytes", len(body))
	}
	salt, rs, idlen := body[:16], binary.BigEndian.Uint32(body[16:20]), int(body[20])
	if rs != 4096 || idlen != 65 {
		t.Fatalf("rs %d idlen %d", rs, idlen)
	}
	asPub, err := ecdh.P256().NewPublicKey(body[21 : 21+idlen])
	if err != nil {
		t.Fatal(err)
	}
	shared, err := d.priv.ECDH(asPub)
	if err != nil {
		t.Fatal(err)
	}
	info := append(append([]byte("WebPush: info\x00"), d.priv.PublicKey().Bytes()...), asPub.Bytes()...)
	prkKey, _ := hkdf.Extract(sha256.New, shared, d.auth)
	ikm, _ := hkdf.Expand(sha256.New, prkKey, string(info), 32)
	prk, _ := hkdf.Extract(sha256.New, ikm, salt)
	cek, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	nonce, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	block, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(block)
	plain, err := gcm.Open(nil, nonce, body[21+idlen:], nil)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if plain[len(plain)-1] != 0x02 {
		t.Fatal("no last-record delimiter")
	}
	return plain[:len(plain)-1]
}

type pushed struct {
	path     string
	msg      pushMessage
	urgency  string
	topic    string
	vapidOK  bool
	encoding string
}

type pushService struct {
	*httptest.Server
	mu      sync.Mutex
	got     []pushed
	devices map[string]device
	status  map[string]int
	t       *testing.T
}

func newPushService(t *testing.T, e *env) *pushService {
	p := &pushService{devices: map[string]device{}, status: map[string]int{}, t: t}
	p.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		p.mu.Lock()
		d, ok := p.devices[r.URL.Path]
		st := p.status[r.URL.Path]
		p.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if st != 0 {
			w.WriteHeader(st)
			return
		}
		var m pushMessage
		if err := json.Unmarshal(d.decrypt(t, body), &m); err != nil {
			t.Errorf("payload: %v", err)
		}
		got := pushed{path: r.URL.Path, msg: m, urgency: r.Header.Get("Urgency"), topic: r.Header.Get("Topic"), encoding: r.Header.Get("Content-Encoding"),
			vapidOK: checkVAPID(r.Header.Get("Authorization"), "https://"+r.Host)}
		p.mu.Lock()
		p.got = append(p.got, got)
		p.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(p.Close)
	e.push = p.Client()
	e.s.pushClient = p.Client()
	return p
}

func (p *pushService) add(e *env, user, path string) Device {
	p.t.Helper()
	d := newDevice()
	p.mu.Lock()
	p.devices[path] = d
	p.mu.Unlock()
	dev, err := e.s.Subscribe(p.t.Context(), user, d.input(p.URL+path))
	if err != nil {
		p.t.Fatal(err)
	}
	return dev
}

func (p *pushService) msgs() []pushed {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]pushed(nil), p.got...)
}

func (p *pushService) to(path string) []pushed {
	var out []pushed
	for _, g := range p.msgs() {
		if g.path == path {
			out = append(out, g)
		}
	}
	return out
}

// checkVAPID verifies "vapid t=<jwt>, k=<key>" for aud.
func checkVAPID(h, aud string) bool {
	rest, ok := strings.CutPrefix(h, "vapid t=")
	if !ok {
		return false
	}
	tok, k, ok := strings.Cut(rest, ", k=")
	if !ok {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(k)
	if err != nil {
		return false
	}
	pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), raw)
	if err != nil {
		return false
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return false
	}
	claims, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var c struct {
		Aud string `json:"aud"`
	}
	if json.Unmarshal(claims, &c) != nil || c.Aud != aud {
		return false
	}
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	if len(sig) != 64 {
		return false
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	return ecdsa.Verify(pub, digest[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:]))
}

func TestWebPushPerViewer(t *testing.T) {
	e := newEnv(t)
	// Household sees what its boards show: app:frigate, as a title only.
	e.view = func(_ context.Context, role string) topics.InboxView {
		if authz.SeesInboxDetail(role) {
			return topics.InboxView{Detail: true}
		}
		return topics.InboxView{Relevant: func(id string) bool { return id == "app:frigate" }}
	}
	e.start()
	p := newPushService(t, e)
	p.add(e, "u_admin", "/admin")
	p.add(e, "u_view", "/viewer")
	p.add(e, "u_home", "/home")

	e.put(inc("frigate", "problem", "down", "open", "open", withTarget("app:frigate"), withTitle("Frigate is down")),
		inc("pbs", "problem", "down", "open", "open", withTitle("PBS is down")))
	e.settle()
	admin, viewer, home := p.to("/admin"), p.to("/viewer"), p.to("/home")
	if len(admin) != 2 || len(viewer) != 2 || len(home) != 1 {
		t.Fatalf("admin %d, viewer %d, home %d", len(admin), len(viewer), len(home))
	}
	for _, g := range admin {
		if !g.vapidOK || g.encoding != "aes128gcm" || g.urgency != "high" || len(g.topic) != 32 || g.msg.V != 1 || g.msg.Kind != KindDown {
			t.Fatalf("push = %+v", g)
		}
	}
	if admin[0].msg.Title != "Frigate is down" {
		admin[0], admin[1] = admin[1], admin[0] // sent in parallel
	}
	if admin[0].msg.Title != "Frigate is down" || admin[0].msg.Path != "/?incident=frigate" || admin[0].msg.Body == "" {
		t.Fatalf("admin's = %+v", admin[0].msg)
	}
	if h := home[0].msg; h.Title != "Frigate is down" || h.Body != "" || h.Path != "/" {
		t.Fatalf("household's = %+v", h)
	}

	// A T4 reaches the admin and not the viewer or household.
	e.s.TaskDone(t4(action.StateSucceeded, action.T4))
	e.settle()
	if len(p.to("/admin")) != 3 || len(p.to("/viewer")) != 2 || len(p.to("/home")) != 1 {
		t.Fatalf("after T4: admin %d, viewer %d, home %d", len(p.to("/admin")), len(p.to("/viewer")), len(p.to("/home")))
	}
	if g := p.to("/admin")[2].msg; g.Kind != KindAction || g.Path != "/settings/audit" {
		t.Fatalf("T4 push = %+v", g)
	}
	// Turning Web Push off stops it.
	if _, _, err := e.s.UpdateChannel(t.Context(), WebPushChannel, ChannelInput{Enabled: ptr(false)}); err != nil {
		t.Fatal(err)
	}
	e.put(inc("nas", "problem", "down", "open", "open"))
	e.settle()
	if len(p.msgs()) != 6 {
		t.Fatalf("sent with Web Push off: %d", len(p.msgs()))
	}
}

func TestExpiredSubscriptionsAreRemoved(t *testing.T) {
	e := newEnv(t)
	p := newPushService(t, e)
	p.add(e, "u_admin", "/gone")
	p.add(e, "u_admin", "/fine")
	p.mu.Lock()
	p.status["/gone"] = http.StatusGone
	p.mu.Unlock()
	e.put(inc("pbs", "problem", "down", "open", "open"))
	e.settle()
	devs, err := e.s.Devices(t.Context(), "u_admin")
	if err != nil {
		t.Fatal(err)
	}
	if len(devs) != 1 || devs[0].Host != "127.0.0.1" || devs[0].LastOKAt == nil {
		t.Fatalf("devices = %+v", devs)
	}
	ds, _ := e.s.Deliveries(t.Context(), "failed", 10)
	if len(ds) != 1 || ds[0].StatusCode == nil || *ds[0].StatusCode != 410 || !strings.Contains(*ds[0].Error, "expired") || ds[0].Device == nil {
		t.Fatalf("deliveries = %+v", ds)
	}
}

func TestSubscribeValidates(t *testing.T) {
	e := newEnv(t)
	e.s.allowLoopbackPush = false
	d := newDevice()
	for _, bad := range []SubscribeInput{
		d.input("http://push.example/x"),
		d.input("https://10.99.0.5/x"),
		func() SubscribeInput { in := d.input("https://push.example/x"); in.Keys.Auth = "AAAA"; return in }(),
		func() SubscribeInput {
			in := d.input("https://push.example/x")
			in.Keys.P256dh = base64.RawURLEncoding.EncodeToString(make([]byte, 65))
			return in
		}(),
	} {
		if _, err := e.s.Subscribe(t.Context(), "u_view", bad); err == nil {
			t.Errorf("accepted %+v", bad.Endpoint)
		}
	}
	dev, err := e.s.Subscribe(t.Context(), "u_view", d.input("https://push.example/x"))
	if err != nil {
		t.Fatal(err)
	}
	// The same browser, now signed in as someone else: it moves.
	if _, err := e.s.Subscribe(t.Context(), "u_home", d.input("https://push.example/x")); err != nil {
		t.Fatal(err)
	}
	if devs, _ := e.s.Devices(t.Context(), "u_view"); len(devs) != 0 {
		t.Fatalf("u_view still has %+v", devs)
	}
	if err := e.s.RemoveDevice(t.Context(), "u_view", dev.ID); !errors.Is(err, errNotFound) {
		t.Fatalf("removed someone else's device: %v", err)
	}
	if err := e.s.Unsubscribe(t.Context(), "u_home", "https://push.example/x"); err != nil {
		t.Fatal(err)
	}
}

// ── delivery ─────────────────────────────────────────────────────────────

func TestRetriesBackOff(t *testing.T) {
	e := newEnv(t)
	n := newNtfy(t)
	e.addNtfy(n)
	n.status.Store(503)
	e.put(inc("pbs", "problem", "down", "open", "open"))
	e.settle()
	pending := func() Delivery {
		t.Helper()
		ds, err := e.s.Deliveries(t.Context(), "", 10)
		if err != nil || len(ds) != 1 {
			t.Fatalf("deliveries = %+v, %v", ds, err)
		}
		return ds[0]
	}
	d := pending()
	if d.State != "pending" || d.Attempts != 1 || d.NextAt == nil || !d.NextAt.Equal(e.clock.Now().Add(30*time.Second).Truncate(time.Millisecond)) || *d.Error != "It answered 503 and asked to slow down." {
		t.Fatalf("after one failure: %+v", d)
	}
	e.tick(29 * time.Second)
	if len(n.msgs()) != 1 {
		t.Fatal("retried before the backoff")
	}
	e.tick(time.Second)
	if d := pending(); len(n.msgs()) != 2 || d.Attempts != 2 || !d.NextAt.Equal(e.clock.Now().Add(2*time.Minute)) {
		t.Fatalf("second try: %d sent, %+v", len(n.msgs()), d)
	}
	n.status.Store(200)
	e.tick(2 * time.Minute)
	if d := pending(); d.State != "sent" || d.Attempts != 3 || d.Error != nil || *d.StatusCode != 200 {
		t.Fatalf("after recovering: %+v", d)
	}

	// A 4xx is final; so is the fifth failure.
	n.status.Store(400)
	e.put(inc("nas", "problem", "down", "open", "open"))
	e.settle()
	ds, _ := e.s.Deliveries(t.Context(), "failed", 10)
	if len(ds) != 1 || ds[0].Attempts != 1 || *ds[0].Error != "It answered 400." {
		t.Fatalf("400: %+v", ds)
	}
	n.status.Store(500)
	e.put(inc("ups", "problem", "down", "open", "open"))
	e.settle()
	for _, wait := range []time.Duration{30 * time.Second, 2 * time.Minute, 10 * time.Minute, 30 * time.Minute} {
		e.tick(wait)
	}
	ds, _ = e.s.Deliveries(t.Context(), "failed", 10)
	if len(ds) != 2 || ds[0].Attempts != maxAttempts {
		t.Fatalf("gave up: %+v", ds)
	}
}

func TestRetryAfterIsHonoured(t *testing.T) {
	if d := retryAfter("120", time.Now()); d != 2*time.Minute {
		t.Fatalf("%v", d)
	}
	if d := retryAfter("999999", time.Now()); d != time.Hour {
		t.Fatalf("%v", d)
	}
	if d := retryAfter("soon", time.Now()); d != 0 {
		t.Fatalf("%v", d)
	}
}

func TestRateLimitDrops(t *testing.T) {
	e := newEnv(t)
	n := newNtfy(t)
	e.addNtfy(n)
	// One at a time (no grouping), more than the burst.
	for i := range rateBurst + 3 {
		e.put(inc("flap-"+string(rune('a'+i)), "problem", "down", "open", "open"))
		e.settle()
	}
	// 13 settles are 130 s: the burst, plus one refilled.
	sent := len(n.msgs())
	ds, _ := e.s.Deliveries(t.Context(), "dropped", 50)
	if sent != rateBurst+1 || len(ds) != 2 || !strings.Contains(*ds[0].Error, "Too many messages") {
		t.Fatalf("sent %d, dropped %d", sent, len(ds))
	}
	// The bucket refills.
	e.tick(rateEvery)
	e.put(inc("later", "problem", "down", "open", "open"))
	e.settle()
	if got := len(n.msgs()); got != sent+1 {
		t.Fatalf("after refilling: %d", got)
	}
}

func TestWebhookSignedPayload(t *testing.T) {
	e := newEnv(t)
	type req struct {
		body []byte
		h    http.Header
	}
	got := make(chan req, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- req{b, r.Header.Clone()}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	c, err := e.s.CreateChannel(t.Context(), ChannelInput{Kind: ChannelWebhook, Name: ptr("Home Assistant"),
		Config: json.RawMessage(`{"url":"` + srv.URL + `/api/webhook/chadash"}`), Secrets: map[string]*string{"secret": ptr(fakeWebhookSecret)}})
	if err != nil {
		t.Fatal(err)
	}
	e.put(inc("pbs", "problem", "down", "open", "open", withTitle("PBS is down")))
	e.settle()
	r := <-got
	ts := r.h.Get(HeaderTimestamp)
	if ts != "1791252010" { // 02:00:10 UTC, after the group window
		t.Fatalf("timestamp = %s", ts)
	}
	tsN, _ := json.Number(ts).Int64()
	if want := Sign([]byte(fakeWebhookSecret), tsN, r.body); r.h.Get(HeaderSignature) != want {
		t.Fatalf("signature %s, want %s", r.h.Get(HeaderSignature), want)
	}
	if Sign([]byte("wrong-secret-0123456789"), tsN, r.body) == r.h.Get(HeaderSignature) {
		t.Fatal("the signature doesn't depend on the secret")
	}
	if r.h.Get(HeaderEvent) != KindDown || !strings.HasPrefix(r.h.Get(HeaderDelivery), "nd_") || r.h.Get("Content-Type") != "application/json" {
		t.Fatalf("headers = %v", r.h)
	}
	var m map[string]any
	if err := json.Unmarshal(r.body, &m); err != nil {
		t.Fatal(err)
	}
	inc := m["incident"].(map[string]any)
	if m["version"] != float64(1) || m["kind"] != KindDown || m["severity"] != "down" || m["title"] != "PBS is down" || m["url"] != "https://dash.lab.example/?incident=pbs" ||
		inc["id"] != "pbs" || inc["target"] != "app:pbs" || !strings.HasPrefix(m["id"].(string), "ne_") {
		t.Fatalf("payload = %s", r.body)
	}
	// A test, with its own kind.
	res, err := e.s.Test(t.Context(), c.ID, "u_admin", "admin")
	if err != nil || !res.OK || *res.StatusCode != 204 {
		t.Fatalf("test = %+v, %v", res, err)
	}
	r = <-got
	if r.h.Get(HeaderEvent) != KindTest {
		t.Fatalf("test event = %s", r.h.Get(HeaderEvent))
	}
}

func TestNoRedirectsAndNoMetadata(t *testing.T) {
	e := newEnv(t)
	hits := atomic.Int32{}
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirect.Close()
	c, err := e.s.CreateChannel(t.Context(), ChannelInput{Kind: ChannelNtfy, Name: ptr("Redirecting"), Config: json.RawMessage(`{"server":"` + redirect.URL + `","topic":"x"}`)})
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.s.Test(t.Context(), c.ID, "u_admin", "admin")
	if err != nil || res.OK || hits.Load() != 0 || !strings.Contains(*res.Error, "redirect") {
		t.Fatalf("res = %+v, %v; target hit %d times", res, err, hits.Load())
	}
	// Link-local (cloud metadata) is refused when saving, and at dial time.
	if _, err := e.s.CreateChannel(t.Context(), ChannelInput{Kind: ChannelNtfy, Name: ptr("Meta"), Config: json.RawMessage(`{"server":"http://169.254.169.254","topic":"x"}`)}); err == nil {
		t.Fatal("a link-local server was accepted")
	}
	for _, ip := range []string{"169.254.169.254", "0.0.0.0", "224.0.0.1", "fe80::1", "::ffff:169.254.169.254"} {
		if allowed(policyLAN, mustAddr(t, ip)) {
			t.Errorf("LAN allows %s", ip)
		}
	}
	for _, ip := range []string{"172.20.99.10", "10.99.0.2", "127.0.0.1", "fd00::1"} {
		if !allowed(policyLAN, mustAddr(t, ip)) {
			t.Errorf("LAN refuses %s", ip)
		}
		if allowed(policyPublic, mustAddr(t, ip)) {
			t.Errorf("public allows %s", ip)
		}
	}
	if !allowed(policyPublic, mustAddr(t, "203.0.113.10")) || !allowed(policyPublic, mustAddr(t, "2001:db8::10")) || allowed(policyPublic, mustAddr(t, "100.100.1.1")) {
		t.Error("public policy is wrong")
	}
	// The push client refuses a loopback address even when a name points there.
	req, _ := http.NewRequest(http.MethodPost, target.URL, nil)
	if a := e.s.do(newClient(policyPublic), req); a.ok() || a.retry || !strings.Contains(a.err, "isn't one Cha Dash sends to") || hits.Load() != 0 {
		t.Fatalf("attempt = %+v", a)
	}
}

// ── secrets ──────────────────────────────────────────────────────────────

func TestSecretsNeverLeak(t *testing.T) {
	e := newEnv(t)
	mux := http.NewServeMux()
	e.s.Register(mux, func(h http.Handler) http.Handler { return h })
	e.s.actor = func(*http.Request) authz.Actor {
		return authz.Actor{Kind: "user", ID: "u_admin", Name: "admin", Role: "admin"}
	}
	call := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		r := httptest.NewRequest(method, path, rd)
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	const urlToken = "querytoken0123456789fake"
	n := newNtfy(t)
	n.status.Store(401)
	w := call("POST", "/api/notify/channels", `{"kind":"ntfy","name":"Phones","config":{"server":"`+n.URL+`","topic":"lab"},"secrets":{"token":"`+fakeNtfyToken+`"}}`)
	if w.Code != 201 {
		t.Fatalf("create = %d %s", w.Code, w.Body)
	}
	var ntfy Channel
	_ = json.Unmarshal(w.Body.Bytes(), &ntfy)
	if !ntfy.Secrets["token"].Set {
		t.Fatalf("token not set: %s", w.Body)
	}
	// A webhook at a closed port, with a token in its URL.
	closed := httptest.NewServer(http.NotFoundHandler())
	closedURL := closed.URL
	closed.Close()
	w = call("POST", "/api/notify/channels", `{"kind":"webhook","name":"Hook","config":{"url":"`+closedURL+`/hook?token=`+urlToken+`"},"secrets":{"secret":"`+fakeWebhookSecret+`"}}`)
	if w.Code != 201 {
		t.Fatalf("create webhook = %d %s", w.Code, w.Body)
	}
	var hook Channel
	_ = json.Unmarshal(w.Body.Bytes(), &hook)
	if w := call("POST", "/api/notify/channels", `{"kind":"webhook","name":"Weak","config":{"url":"https://hooks.example/x"},"secrets":{"secret":"short"}}`); w.Code != 422 {
		t.Fatalf("a short secret = %d", w.Code)
	}
	if w := call("POST", "/api/notify/channels", `{"kind":"webhook","name":"None","config":{"url":"https://hooks.example/x"}}`); w.Code != 422 {
		t.Fatalf("no secret = %d", w.Code)
	}
	// Change the token, test both, let a real message fail.
	if w := call("PUT", "/api/notify/channels/"+ntfy.ID, `{"secrets":{"token":"`+fakeNtfyToken+`x"}}`); w.Code != 200 {
		t.Fatalf("update = %d %s", w.Code, w.Body)
	}
	if w := call("POST", "/api/notify/channels/"+ntfy.ID+"/test", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "the token or secret wasn't accepted") {
		t.Fatalf("test = %d %s", w.Code, w.Body)
	}
	if w := call("POST", "/api/notify/channels/"+hook.ID+"/test", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"ok":false`) {
		t.Fatalf("hook test = %d %s", w.Code, w.Body)
	}
	e.put(inc("pbs", "problem", "down", "open", "open"))
	e.settle()

	var bodies []string
	for _, p := range []string{"/api/notify/channels", "/api/notify/deliveries", "/api/notify/rules", "/api/notify/push/key", "/api/notify/push/subscriptions"} {
		w := call("GET", p, "")
		if w.Code != 200 {
			t.Fatalf("GET %s = %d %s", p, w.Code, w.Body)
		}
		bodies = append(bodies, w.Body.String())
	}
	evs, err := e.audit.Query(t.Context(), audit.Filter{Limit: audit.MaxLimit})
	if err != nil {
		t.Fatal(err)
	}
	if len(evs.Events) < 4 {
		t.Fatalf("audited %d changes", len(evs.Events))
	}
	auditJSON, _ := json.Marshal(evs)
	var rows []string
	r, _ := e.st.Reader().Query(`SELECT payload || coalesce(last_error, '') FROM notify_deliveries`)
	for r.Next() {
		var s string
		_ = r.Scan(&s)
		rows = append(rows, s)
	}
	r.Close()
	vapid, _ := e.v.Get(t.Context(), vapidVaultName)
	vapidB64 := base64.RawURLEncoding.EncodeToString(vapid)
	for _, secret := range []string{fakeNtfyToken, fakeWebhookSecret, vapidB64} {
		for i, b := range bodies {
			if strings.Contains(b, secret) {
				t.Errorf("response %d carries a secret", i)
			}
		}
		if strings.Contains(string(auditJSON), secret) {
			t.Error("the audit log carries a secret")
		}
		if strings.Contains(e.logs.String(), secret) {
			t.Error("the log carries a secret")
		}
		for _, row := range rows {
			if strings.Contains(row, secret) {
				t.Error("a delivery row carries a secret")
			}
		}
	}
	// The webhook URL's token stays out of the log, the audit log and delivery errors.
	if strings.Contains(e.logs.String(), urlToken) || strings.Contains(string(auditJSON), urlToken) || strings.Contains(bodies[1], urlToken) {
		t.Error("the webhook URL's token leaked")
	}
	if !strings.Contains(string(auditJSON), "settings.notify.channel.create") || !strings.Contains(string(auditJSON), `"secrets_changed":["token"]`) {
		t.Errorf("audit = %s", auditJSON)
	}
}

func TestHTTPShapes(t *testing.T) {
	e := newEnv(t)
	mux := http.NewServeMux()
	e.s.Register(mux, func(h http.Handler) http.Handler { return h })
	user := "u_view"
	e.s.actor = func(*http.Request) authz.Actor {
		return authz.Actor{Kind: "user", ID: user, Name: strings.TrimPrefix(user, "u_")}
	}
	call := func(method, path, body string) *httptest.ResponseRecorder {
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		r := httptest.NewRequest(method, path, rd)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64; rv:140.0) Gecko/20100101 Firefox/140.0")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	w := call("GET", "/api/notify/push/key", "")
	var key struct {
		PublicKey string `json:"public_key"`
	}
	if json.Unmarshal(w.Body.Bytes(), &key) != nil || len(mustB64(t, key.PublicKey)) != 65 {
		t.Fatalf("key = %s", w.Body)
	}
	// The key is made once and kept.
	e.start()
	w2 := call("GET", "/api/notify/push/key", "")
	if w2.Body.String() != w.Body.String() {
		t.Fatal("the VAPID key changed after a restart")
	}

	d := newDevice()
	in := d.input("https://127.0.0.1:1/push/x")
	in.Label = ""
	b, _ := json.Marshal(in)
	w = call("POST", "/api/notify/push/subscriptions", string(b))
	if w.Code != 201 || !strings.Contains(w.Body.String(), `"label":"Firefox on Linux"`) || strings.Contains(w.Body.String(), "push/x") {
		t.Fatalf("subscribe = %d %s", w.Code, w.Body)
	}
	var dev Device
	_ = json.Unmarshal(w.Body.Bytes(), &dev)
	if dev.Fingerprint != fingerprint(in.Endpoint) {
		t.Fatalf("fingerprint = %s", dev.Fingerprint)
	}
	// Web Push test: your own devices only.
	user = "u_admin"
	if w := call("POST", "/api/notify/channels/"+WebPushChannel+"/test", ""); w.Code != 409 || !strings.Contains(w.Body.String(), "no_devices") {
		t.Fatalf("test without devices = %d %s", w.Code, w.Body)
	}
	if w := call("DELETE", "/api/notify/push/subscriptions/"+dev.ID, ""); w.Code != 404 {
		t.Fatalf("removing another user's device = %d", w.Code)
	}
	user = "u_view"
	if w := call("POST", "/api/notify/push/unsubscribe", `{"endpoint":"`+in.Endpoint+`"}`); w.Code != 204 {
		t.Fatalf("unsubscribe = %d %s", w.Code, w.Body)
	}
	if w := call("DELETE", "/api/notify/channels/"+WebPushChannel, ""); w.Code != 409 {
		t.Fatalf("deleting Web Push = %d", w.Code)
	}
	if w := call("PUT", "/api/notify/rules/action.t4", `{"enabled":false}`); w.Code != 409 {
		t.Fatalf("turning off T4 = %d %s", w.Code, w.Body)
	}
	if w := call("PUT", "/api/notify/rules/incident.attention", `{"grace_minutes":15}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"grace_minutes":15`) {
		t.Fatalf("grace = %d %s", w.Code, w.Body)
	}
	if w := call("PUT", "/api/notify/rules/nope", `{}`); w.Code != 404 {
		t.Fatalf("unknown rule = %d", w.Code)
	}
	if w := call("GET", "/api/notify/deliveries?state=lost", ""); w.Code != 422 {
		t.Fatalf("bad state = %d", w.Code)
	}
	w = call("GET", "/api/notify/rules", "")
	var rules struct {
		Rules []Rule `json:"rules"`
	}
	if json.Unmarshal(w.Body.Bytes(), &rules) != nil || len(rules.Rules) != 6 || rules.Rules[0].ID != RuleDown || !rules.Rules[5].Locked {
		t.Fatalf("rules = %s", w.Body)
	}
	w = call("GET", "/api/notify/channels", "")
	if !strings.Contains(w.Body.String(), `"id":"nc_webpush"`) || !strings.Contains(w.Body.String(), `"builtin":true`) {
		t.Fatalf("channels = %s", w.Body)
	}
}

func TestComposeAndClip(t *testing.T) {
	if got := clip("héllo wörld", 8); got != "héllo…" && got != "héll…" {
		t.Fatalf("clip = %q", got)
	}
	if clip("short", 10) != "short" {
		t.Fatal("clipped a short string")
	}
	evs := []Event{
		{ID: "ne_1", Kind: KindAction, Severity: "down", Title: "a"},
		{ID: "ne_2", Kind: KindDown, Severity: "down", Title: "b"},
		{ID: "ne_3", Kind: KindResolved, Severity: "info", Title: "c"},
	}
	ms := compose(evs, func() string { return "ng_x" })
	if len(ms) != 3 {
		t.Fatalf("composed %d", len(ms))
	}
	if labelFromUA("Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1") != "Safari on iOS" {
		t.Fatal("UA label")
	}
}

func mustAddr(t *testing.T, s string) netip.Addr {
	t.Helper()
	a, err := netip.ParseAddr(s)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// A plan an API token left waiting for a person reaches the approvers
// once: ntfy (an admin's channel) and the admins' devices, never a
// viewer's or the household's.
func TestApprovalsReachTheApprovers(t *testing.T) {
	e := newEnv(t)
	e.view = func(_ context.Context, role string) topics.InboxView {
		return topics.InboxView{Detail: authz.SeesInboxDetail(role), Approvals: authz.SeesApprovals(role)}
	}
	e.start()
	n := newNtfy(t)
	e.addNtfy(n)
	p := newPushService(t, e)
	p.add(e, "u_admin", "/admin")
	p.add(e, "u_view", "/viewer")
	p.add(e, "u_home", "/home")

	e.put(inc("approval.ap_1", topics.ApprovalClass, "attention", "open", "open", withTitle("backup script wants to reboot win-gaming")))
	e.settle()
	e.put(inc("approval.ap_1", topics.ApprovalClass, "attention", "open", "open", withTitle("backup script wants to reboot win-gaming")))
	e.settle()
	got := n.msgs()
	if len(got) != 1 || got[0].Title != "backup script wants to reboot win-gaming" || !strings.Contains(got[0].Message, "within the hour") || !slicesContain(got[0].Tags, KindApproval) {
		t.Fatalf("ntfy got %+v", got)
	}
	if len(p.to("/admin")) != 1 || len(p.to("/viewer")) != 0 || len(p.to("/home")) != 0 {
		t.Fatalf("push: admin %d, viewer %d, home %d", len(p.to("/admin")), len(p.to("/viewer")), len(p.to("/home")))
	}
}
