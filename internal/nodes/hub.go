// Package nodes is the hub's side of chadash-node: the /node endpoint, the
// registry of node identities with their pinned keys, local pairing, the
// hub's own keys, node status in the Home Graph, and the Caller other
// packages use to run node verbs.
//
// The transport is internal/nodeproto (ADR-0014). A node is known by its
// Ed25519 key; every connection looks it up in the registry, so revoking a
// node cuts it off for good, including a session that's live. People's
// permissions are internal/authz's business; what a node may do on its
// host is its own policy file's, enforced on the node.
package nodes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"dash.cha.sh/internal/audit"
	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/httpx"
	"dash.cha.sh/internal/nodeproto"
	"dash.cha.sh/internal/store"
	"dash.cha.sh/internal/vault"
)

// Caller runs a verb on a connected node. node is the node's ID (n_…) or
// its name ("local"); progress receives stream items (log lines, deploy
// steps) and may be nil. Errors are ErrNotConnected, a *nodeproto.Error
// from the node (forbidden by its policy, not_found, conflict, …), or the
// context's.
type Caller interface {
	Call(ctx context.Context, node, verb string, req any, progress func(json.RawMessage)) (json.RawMessage, error)
}

// ErrNotConnected is a call to a node without a live session. Its code is
// nodeproto.CodeNotConnected.
var ErrNotConnected error = nodeproto.Errorf(nodeproto.CodeNotConnected, "the node isn't connected to the hub")

// Allow wraps c so only the named verbs get through, matched exactly: the
// integrations' Deps get the Docker verbs they use (IntegrationVerbs),
// never stacks, exec or anything a later module adds.
func Allow(c Caller, verbs ...string) Caller {
	return allowed{c, slices.Clone(verbs)}
}

// IntegrationVerbs are the node verbs integrations may call: the Docker
// integration's reads (its node:// endpoint) and its actions. A verb the
// node gains later stays out until it's added here.
var IntegrationVerbs = []string{
	"engine.version", "engine.info", "engine.events", "engine.self",
	"container.list", "container.inspect", "container.stats", "container.can", "container.logs",
	"container.start", "container.stop", "container.restart",
	"container.pull", "container.recreate", "container.remove",
}

type allowed struct {
	c     Caller
	verbs []string
}

func (a allowed) Call(ctx context.Context, node, verb string, req any, progress func(json.RawMessage)) (json.RawMessage, error) {
	if !slices.Contains(a.verbs, verb) {
		return nil, nodeproto.Errorf(nodeproto.CodeForbidden, "the hub doesn't let this caller use %s", verb)
	}
	return a.c.Call(ctx, node, verb, req, progress)
}

// Options configure a Hub.
type Options struct {
	Store *store.Store
	// Vault holds the hub's private node keys.
	Vault *vault.Vault
	// Graph gets a node resource per approved node (node:<id>). Nil: none.
	Graph *graph.Graph
	// Audit records enrollments, rotations and revocations. Nil: none.
	Audit *audit.Log
	// PairingFile turns local pairing on: the file is created on first run
	// (see Pairing). Empty: off, and unknown nodes are always refused.
	PairingFile string
	// PairingFrom are the client addresses a node may pair from (nil:
	// DefaultPairingFrom). A known node connects from anywhere; this only
	// bounds where the pairing secret is accepted.
	PairingFrom []netip.Prefix
	// Proxies resolve the client address recorded for each node.
	Proxies httpx.Proxies
	// Session tunes keepalive and limits (nodeproto's defaults when zero).
	Session nodeproto.SessionOptions
	Log     *slog.Logger
	Now     func() time.Time
}

// Hub serves nodes.
type Hub struct {
	o       Options
	log     *slog.Logger
	reg     *registry
	keys    *hubKeys
	pairing *Pairing
	limit   *limiter
	gate    *gate
	quiet   *sampler // refusals of unknown keys: one Info line a minute

	ctx    context.Context // ends with Close; sessions run under it
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu        sync.Mutex
	live      map[string]*conn // by node ID
	closed    bool
	onConnect func(name string)
}

type conn struct {
	sess *nodeproto.Session
	name string
}

// New loads the hub's keys (making the first on a new hub), opens the
// pairing file when configured, and publishes every approved node as
// disconnected.
func New(ctx context.Context, o Options) (*Hub, error) {
	if o.Store == nil || o.Vault == nil {
		return nil, errors.New("nodes: a hub needs the store and the vault")
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	h := &Hub{
		o: o, log: o.Log, reg: &registry{st: o.Store, now: o.Now},
		keys:  &hubKeys{st: o.Store, vault: o.Vault, now: o.Now},
		limit: newLimiter(o.Now), gate: newGate(maxHandshakesPerAddr, maxHandshakes),
		quiet: &sampler{every: time.Minute, now: o.Now}, live: map[string]*conn{},
	}
	if h.o.PairingFrom == nil {
		h.o.PairingFrom = DefaultPairingFrom
	}
	if err := h.keys.load(ctx); err != nil {
		return nil, err
	}
	recs, err := h.reg.list(ctx)
	if err != nil {
		return nil, err
	}
	if o.PairingFile != "" {
		p, err := OpenPairing(o.PairingFile, h.keys.newest())
		if err != nil {
			return nil, err
		}
		// A node paired before: the secret in the file may be one it spent
		// (or one copied out since). A fresh one costs nothing, since the
		// enrolled node never needs it again.
		if slices.ContainsFunc(recs, func(r Record) bool { return r.EnrolledVia == ViaLocal }) {
			if err := p.Regenerate(); err != nil {
				return nil, err
			}
		}
		h.pairing = p
		h.log.Info("local node pairing is on", "file", p.Path())
	}
	h.ctx, h.cancel = context.WithCancel(context.WithoutCancel(ctx))
	for _, r := range recs {
		h.publish(r, false)
	}
	return h, nil
}

// Pin is the hub's newest node key.
func (h *Hub) Pin() string { return h.keys.newest() }

// Close ends every session and waits for them.
func (h *Hub) Close() {
	h.mu.Lock()
	h.closed = true
	h.mu.Unlock()
	h.cancel()
	h.mu.Lock()
	for _, c := range h.live {
		c.sess.Close()
	}
	h.mu.Unlock()
	h.wg.Wait()
}

// nameRE is what a node may call itself.
var nameRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,62}$`)

// reservedNames are names a node can't have, compared without case: the
// stacks API's literal segments (/api/stacks/hooks, /api/stacks/secrets)
// win over {node}, so a node called one of them would be unreachable.
var reservedNames = []string{"hooks", "secrets"}

// reservedName reports whether name is in reservedNames.
func reservedName(name string) bool {
	return slices.ContainsFunc(reservedNames, func(r string) bool { return strings.EqualFold(r, name) })
}

// ServeHTTP serves /node: the upgrade, the TLS handshake, the hello, then
// the node's session until it ends.
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	addr := h.o.Proxies.ClientIP(r).String()
	if !h.limit.allowed(addr) {
		httpx.Error(w, http.StatusTooManyRequests, "rate_limited", "Too many failed node handshakes from this address; wait a minute.")
		return
	}
	// Until the node is admitted it holds a handshake slot: a few per
	// address, a few dozen in all, each for at most HandshakeTimeout twice.
	if !h.gate.enter(addr) {
		httpx.Error(w, http.StatusTooManyRequests, "rate_limited", "Too many node handshakes in progress; try again shortly.")
		return
	}
	inGate := true
	release := func() {
		if inGate {
			inGate = false
			h.gate.leave(addr)
		}
	}
	defer release()
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		httpx.Error(w, http.StatusServiceUnavailable, "shutting_down", "The hub is shutting down.")
		return
	}
	h.wg.Add(1)
	h.mu.Unlock()
	defer h.wg.Done()
	// The hello is read under a small frame limit: a stranger can't make the
	// hub buffer a session-sized frame before it knows the key.
	ch, accepted, err := nodeproto.Accept(w, r, nodeproto.AcceptOptions{Identity: h.keys.pick, MaxFrame: nodeproto.HandshakeMaxFrame})
	if err != nil {
		h.log.Debug("node handshake failed", "client", addr, "err", err)
		h.limit.failed(addr)
		return
	}
	defer ch.Close()
	f, err := nodeproto.RecvFrame(h.ctx, ch, nodeproto.HandshakeTimeout)
	if err != nil || f.Op != nodeproto.OpHello {
		h.limit.failed(addr)
		h.refuse(ch, "", nodeproto.Errorf(nodeproto.CodeProtocol, "the node must say hello first"))
		return
	}
	var hello nodeproto.Hello
	if err := json.Unmarshal(f.Data, &hello); err != nil {
		h.limit.failed(addr)
		h.refuse(ch, "", nodeproto.Errorf(nodeproto.CodeProtocol, "a malformed hello"))
		return
	}
	rec, known, err := h.reg.lookup(h.ctx, ch.PeerPin)
	if err != nil {
		h.log.Error("node registry lookup failed", "err", err)
		h.refuse(ch, "", nodeproto.Errorf(nodeproto.CodeFailed, "the hub couldn't read its registry"))
		return
	}
	switch {
	case !known && hello.Pairing != "":
		rec, err = h.enroll(ch, hello, addr)
		if err != nil {
			return // enroll refused it
		}
	case !known && hello.Token != "":
		h.limit.failed(addr)
		h.refuse(ch, "", nodeproto.Errorf(nodeproto.CodeUnknownNode, "remote enrollment with join tokens arrives in v0.3; pair this node on the hub's compose network"))
		return
	case !known:
		h.limit.failed(addr)
		h.refuse(ch, "", nodeproto.Errorf(nodeproto.CodeUnknownNode, "this hub doesn't know the node's key"))
		return
	case rec.State == StateRevoked:
		h.limit.failed(addr)
		h.refuse(ch, rec.ID, nodeproto.Errorf(nodeproto.CodeRevoked, "this node was revoked"))
		return
	case rec.State == StatePending:
		h.refuse(ch, rec.ID, nodeproto.Errorf(nodeproto.CodePending, "this node is waiting for approval"))
		return
	}
	h.limit.succeeded(addr)
	release()
	ch.SetMaxFrame(nodeproto.DefaultMaxFrame)
	h.serve(ch, rec, hello, accepted, addr)
}

// refuse tells a node no. Refusals of keys the hub doesn't know are anyone
// on the network knocking, so they log at Info at most once a minute (with
// a count of the rest) and otherwise at Debug.
func (h *Hub) refuse(ch *nodeproto.Channel, id string, e *nodeproto.Error) {
	_ = nodeproto.SendFrame(h.ctx, ch, nodeproto.OpRefused, nil, e)
	attrs := []any{"node", id, "code", e.Code, "reason", e.Message, "fingerprint", nodeproto.Fingerprint(ch.PeerPin)}
	if id == "" {
		ok, held := h.quiet.take()
		if !ok {
			h.log.Debug("refused a node", attrs...)
			return
		}
		if held > 0 {
			attrs = append(attrs, "similar_refusals_not_logged", held)
		}
	}
	h.log.Info("refused a node", attrs...)
}

// enroll handles an unknown key that presents the pairing secret.
func (h *Hub) enroll(ch *nodeproto.Channel, hello nodeproto.Hello, addr string) (Record, error) {
	actor := audit.Actor{Kind: "system", Name: ApprovedByPairing, IP: addr}
	fail := func(e *nodeproto.Error) (Record, error) {
		h.limit.failed(addr)
		h.record(audit.Entry{Actor: actor, Action: "nodes.enroll", Target: "node:" + hello.Name,
			Params: params(map[string]string{"name": hello.Name, "fingerprint": nodeproto.Fingerprint(ch.PeerPin)}),
			Result: audit.Refused, Error: &audit.Error{Code: e.Code, Message: e.Message}})
		h.refuse(ch, "", e)
		return Record{}, e
	}
	name := hello.Name
	if name == "" {
		name = "local"
	}
	switch {
	case h.pairing == nil:
		return fail(nodeproto.Errorf(nodeproto.CodeUnknownNode, "local pairing is off on this hub"))
	case !addrIn(addr, h.o.PairingFrom):
		return fail(nodeproto.Errorf(nodeproto.CodeUnknownNode, "this hub doesn't accept pairing from the node's address (CHADASH_NODE_PAIRING_FROM)"))
	case !nameRE.MatchString(name):
		return fail(nodeproto.Errorf(nodeproto.CodeProtocol, "a node name is 1–63 letters, digits, dots, dashes or underscores"))
	case reservedName(name):
		return fail(nodeproto.Errorf(nodeproto.CodeProtocol, "a node can't be called %q: the hub uses that name; pick another CHADASH_NODE_NAME", name))
	}
	// The secret is spent by the enrollment it admits, and only then: a
	// refusal (a name in use) leaves it for the node that should have it.
	var rec Record
	redeemed, err := h.pairing.Redeem(hello.Pairing, func() error {
		var err error
		rec, err = h.reg.enrollApproved(h.ctx, name, ch.PeerPin, ViaLocal, ApprovedByPairing)
		return err
	})
	switch {
	case errors.Is(err, errBadSecret):
		return fail(nodeproto.Errorf(nodeproto.CodeBadPairing, "the pairing secret doesn't match this hub's (it's single-use: a node that paired already spent it)"))
	case errors.Is(err, errNameTaken):
		// Pairing never replaces a node: that would let anyone with the
		// secret take over node://local. An admin revokes the old one first.
		return fail(nodeproto.Errorf(nodeproto.CodeConflict, "a node called %q is already enrolled; revoke it on the hub before pairing another under that name", name))
	case !redeemed:
		h.log.Error("enrolling a node failed", "err", err)
		h.refuse(ch, "", nodeproto.Errorf(nodeproto.CodeFailed, "the hub couldn't record the node"))
		return Record{}, err
	case err != nil:
		h.log.Error("writing a fresh pairing secret failed; no other node can pair until it's regenerated", "err", err)
	}
	h.record(audit.Entry{Actor: actor, Action: "nodes.enroll", Target: "node:" + rec.ID,
		Params: params(map[string]string{"name": rec.Name, "via": ViaLocal, "fingerprint": rec.Fingerprint}), Result: audit.OK})
	h.log.Info("paired a node", "node", rec.ID, "name", rec.Name, "fingerprint", rec.Fingerprint, "client", addr)
	return rec, nil
}

func params(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func (h *Hub) record(e audit.Entry) {
	if h.o.Audit == nil {
		return
	}
	if _, err := h.o.Audit.Append(context.WithoutCancel(h.ctx), e); err != nil {
		h.log.Error("audit: recording a node event failed", "err", err, "action", e.Action)
	}
}

// serve runs an approved node's session.
func (h *Hub) serve(ch *nodeproto.Channel, rec Record, hello nodeproto.Hello, accepted []string, addr string) {
	pins := slices.Concat(accepted, hello.HubPins)
	slices.Sort(pins)
	pins = slices.Compact(pins)
	rotated, err := h.reg.seen(h.ctx, rec.ID, ch.PeerPin, addr, hello.Build, pins, hello.Verbs)
	if err != nil {
		h.log.Error("recording a node's connection failed", "err", err)
		h.refuse(ch, rec.ID, nodeproto.Errorf(nodeproto.CodeFailed, "the hub couldn't record the connection"))
		return
	}
	if rotated {
		h.record(audit.Entry{Actor: audit.Actor{Kind: "node", ID: rec.ID, Name: rec.Name, IP: addr}, Action: "nodes.rotate",
			Target: "node:" + rec.ID, Params: params(map[string]string{"fingerprint": nodeproto.Fingerprint(ch.PeerPin)}), Result: audit.OK})
		h.log.Info("a node finished rotating its key", "node", rec.ID, "fingerprint", nodeproto.Fingerprint(ch.PeerPin))
	}
	// Welcome first: the session is registered (and callable) only after,
	// so no request can reach the node before its welcome.
	newest := h.keys.newest()
	if err := nodeproto.SendFrame(h.ctx, ch, nodeproto.OpWelcome, nodeproto.Welcome{Node: rec.ID, HubPin: newest}, nil); err != nil {
		return
	}
	sess := nodeproto.NewSession(ch, h.sessionOptions(rec))
	c := &conn{sess: sess, name: rec.Name}
	h.mu.Lock()
	if old := h.live[rec.ID]; old != nil {
		old.sess.Close() // one session per node: the newest wins
	}
	h.live[rec.ID] = c
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		if h.live[rec.ID] == c {
			delete(h.live, rec.ID)
		}
		h.mu.Unlock()
		if cur, err := h.reg.get(context.WithoutCancel(h.ctx), rec.ID); err == nil {
			h.publish(cur, h.Connected(rec.ID))
		}
	}()
	// A revoke may have run between the lookup and registering the session;
	// after registering, Revoke's cut finds it.
	if cur, err := h.reg.get(h.ctx, rec.ID); err != nil || cur.State != StateApproved {
		h.refuse(ch, rec.ID, nodeproto.Errorf(nodeproto.CodeRevoked, "this node was revoked"))
		return
	}
	if cur, err := h.reg.get(h.ctx, rec.ID); err == nil {
		h.publish(cur, true)
	}
	h.log.Info("node connected", "node", rec.ID, "name", rec.Name, "version", hello.Build, "client", addr, "protocol", ch.Version)
	h.mu.Lock()
	onConnect := h.onConnect
	h.mu.Unlock()
	if onConnect != nil {
		onConnect(rec.Name)
	}
	if !slices.Contains(pins, newest) {
		go h.pushPin(sess, rec.ID, newest)
	}
	err = sess.Run(h.ctx)
	h.log.Info("node disconnected", "node", rec.ID, "name", rec.Name, "err", err)
}

func (h *Hub) sessionOptions(rec Record) nodeproto.SessionOptions {
	o := h.o.Session
	id, name := rec.ID, rec.Name
	o.Handler = func(ctx context.Context, method string, p json.RawMessage, _ func(json.RawMessage) error) (json.RawMessage, error) {
		if method != nodeproto.VerbNodeRotate {
			return nil, nodeproto.Errorf(nodeproto.CodeNotFound, "the hub has no verb %q", method)
		}
		var m nodeproto.RotateMsg
		if err := json.Unmarshal(p, &m); err != nil || !nodeproto.ValidPin(m.Next) {
			return nil, nodeproto.Errorf(nodeproto.CodeInvalid, "node.rotate needs the next key's pin")
		}
		// A next key must be new: never a key some node already holds or
		// announced (that would let one node squat on another's key), nor
		// one of the hub's own.
		if h.keys.has(m.Next) {
			return nil, nodeproto.Errorf(nodeproto.CodeConflict, "the hub couldn't record the next key: it's one of the hub's keys")
		}
		if err := h.reg.setNext(ctx, id, m.Next); err != nil {
			return nil, nodeproto.Errorf(nodeproto.CodeConflict, "the hub couldn't record the next key: %v", err)
		}
		h.log.Info("a node announced its next key", "node", id, "name", name, "fingerprint", nodeproto.Fingerprint(m.Next))
		return json.RawMessage("{}"), nil
	}
	return o
}

// pushPin sends a node the hub's newest key and records that it took it.
func (h *Hub) pushPin(sess *nodeproto.Session, id, pin string) bool {
	ctx, cancel := context.WithTimeout(h.ctx, 30*time.Second)
	defer cancel()
	if _, err := sess.Call(ctx, nodeproto.VerbHubPin, nodeproto.PinMsg{Pin: pin}, nil); err != nil {
		h.log.Warn("a node didn't take the hub's new key", "node", id, "err", err)
		return false
	}
	if err := h.reg.addHubPin(ctx, id, pin); err != nil {
		h.log.Error("recording a node's hub key failed", "err", err)
	}
	return true
}

// session finds a live session by node ID or name.
func (h *Hub) session(node string) *conn {
	h.mu.Lock()
	defer h.mu.Unlock()
	if c := h.live[node]; c != nil {
		return c
	}
	for _, c := range h.live {
		if c.name == node {
			return c
		}
	}
	return nil
}

// Connected reports whether a node (by ID or name) has a live session.
func (h *Hub) Connected(node string) bool { return h.session(node) != nil }

// OnConnect sets what runs each time a node connects, with its name: the
// hub refreshes the instances that read through it, which would otherwise
// wait for their next poll (or backoff) after a hub or node restart. f must
// not block.
func (h *Hub) OnConnect(f func(name string)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.onConnect = f
}

// Call implements Caller.
func (h *Hub) Call(ctx context.Context, node, verb string, req any, progress func(json.RawMessage)) (json.RawMessage, error) {
	c := h.session(node)
	if c == nil {
		return nil, fmt.Errorf("%w (%s)", ErrNotConnected, node)
	}
	return c.sess.Call(ctx, verb, req, progress)
}

var _ Caller = (*Hub)(nil)

// cut ends a node's live session with a revocation.
func (h *Hub) cut(id, why string) {
	h.mu.Lock()
	c := h.live[id]
	h.mu.Unlock()
	if c == nil {
		return
	}
	_ = nodeproto.SendFrame(h.ctx, c.sess.Channel(), nodeproto.OpRefused, nil, nodeproto.Errorf(nodeproto.CodeRevoked, "this node was revoked: %s", why))
	c.sess.Close()
}

// Revoke cuts a node off now and for good: its live session ends and every
// later handshake is refused. Re-pairing makes a new node.
func (h *Hub) Revoke(ctx context.Context, id string, by audit.Actor) (Record, error) {
	rec, err := h.reg.revoke(ctx, id, by.ID)
	if err != nil {
		return Record{}, err
	}
	h.cut(id, "revoked by "+by.Name)
	h.graphRemove(id)
	h.record(audit.Entry{Actor: by, Action: "nodes.revoke", Target: "node:" + id,
		Params: params(map[string]string{"name": rec.Name, "fingerprint": rec.Fingerprint}), Result: audit.OK})
	h.log.Info("revoked a node", "node", id, "name", rec.Name, "by", by.Name)
	// Whatever the revoked node (or whoever had its volume) read of the
	// pairing file is dead now too.
	if h.pairing != nil {
		if err := h.pairing.Regenerate(); err != nil {
			h.log.Error("regenerating the pairing secret after a revoke failed", "err", err)
		}
	}
	return rec, nil
}

// ErrPairingOff is RegeneratePairing on a hub without local pairing.
var ErrPairingOff = errors.New("nodes: local pairing is off")

// RegeneratePairing replaces the pairing secret: the old one stops working
// at once, and the pairing file holds the new one for the next node to
// pair. The secret itself never leaves the file.
func (h *Hub) RegeneratePairing(_ context.Context, by audit.Actor) error {
	if h.pairing == nil {
		return ErrPairingOff
	}
	err := h.pairing.Regenerate()
	e := audit.Entry{Actor: by, Action: "nodes.pairing.regenerate", Target: "hub", Params: params(map[string]string{}), Result: audit.OK}
	if err != nil {
		e.Result, e.Error = audit.Failed, &audit.Error{Code: "failed", Message: "The pairing file couldn't be written."}
	}
	h.record(e)
	if err != nil {
		return err
	}
	h.log.Info("regenerated the pairing secret", "by", by.Name)
	return nil
}

// RotateKey makes a new hub key and pushes its pin to every connected node
// over the channel the current key authenticates. Nodes that miss it get
// it on their next connection, which the hub still answers with the old key
// until RetireOldKeys. It returns how many nodes took the new pin. The
// pairing file is rewritten with the new pin.
//
// Rotation is hygiene, not compromise recovery: whoever holds the old key
// could push a pin of their own. After a compromise, re-pair every node.
func (h *Hub) RotateKey(ctx context.Context) (int, error) {
	pin, err := h.keys.add(ctx)
	if err != nil {
		return 0, err
	}
	if h.pairing != nil {
		if err := h.pairing.setPin(pin); err != nil {
			return 0, err
		}
	}
	h.mu.Lock()
	type target struct {
		id   string
		sess *nodeproto.Session
	}
	var live []target
	for id, c := range h.live {
		live = append(live, target{id, c.sess})
	}
	h.mu.Unlock()
	took := 0
	for _, t := range live {
		if h.pushPin(t.sess, t.id, pin) {
			took++
		}
	}
	h.record(audit.Entry{Actor: audit.Actor{Kind: "system", Name: "hub"}, Action: "nodes.hub-key.rotate", Target: "hub",
		Params: params(map[string]any{"fingerprint": nodeproto.Fingerprint(pin), "pushed": took}), Result: audit.OK})
	return took, nil
}

// Stranded lists the approved nodes that haven't acknowledged the newest
// hub key: retiring the old keys now would cut them off.
func (h *Hub) Stranded(ctx context.Context) ([]Record, error) {
	recs, err := h.reg.list(ctx)
	if err != nil {
		return nil, err
	}
	newest := h.keys.newest()
	var out []Record
	for _, r := range recs {
		if r.State == StateApproved && !slices.Contains(r.HubPins, newest) {
			out = append(out, r)
		}
	}
	return out, nil
}

// RetireOldKeys drops every hub key but the newest.
func (h *Hub) RetireOldKeys(ctx context.Context) (int, error) { return h.keys.retireOld(ctx) }

// Status is a node as the API lists it.
type Status struct {
	Record
	Connected bool `json:"connected"`
	// HubKeyCurrent says the node has acknowledged the hub's newest key.
	HubKeyCurrent bool `json:"hub_key_current"`
}

// List returns every node, live ones first.
func (h *Hub) List(ctx context.Context) ([]Status, error) {
	recs, err := h.reg.list(ctx)
	if err != nil {
		return nil, err
	}
	newest := h.keys.newest()
	out := make([]Status, len(recs))
	for i, r := range recs {
		out[i] = Status{Record: r, Connected: h.Connected(r.ID), HubKeyCurrent: slices.Contains(r.HubPins, newest)}
	}
	return out, nil
}

// Get returns one node.
func (h *Hub) Get(ctx context.Context, id string) (Record, error) { return h.reg.get(ctx, id) }

// ByName returns the live node with a name.
func (h *Hub) ByName(ctx context.Context, name string) (Record, error) {
	return h.reg.byName(ctx, name)
}
