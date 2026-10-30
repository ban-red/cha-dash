package nodes

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"dash.cha.sh/internal/audit"
	"dash.cha.sh/internal/livedoc"
	"dash.cha.sh/internal/nodeproto"
)

// Remote nodes (v0.3, ADR-0014's enrollment): a node on another host
// presents a join token, waits as pending, and an admin approves it in
// Settings → Nodes after comparing its fingerprint. Approval needs a
// step-up (the route's business) and can't be skipped: no setting approves
// token enrollments by itself.

// PendingExpiry is how long a node may wait for approval before the hub
// revokes it (revoked_by = 'expired').
const PendingExpiry = 7 * 24 * time.Hour

// ExpiredBy is revoked_by for a pending node nobody approved in time.
const ExpiredBy = "expired"

// sweepEvery is how often the hub expires pending nodes and prunes tokens.
const sweepEvery = time.Hour

// JoinTokens is the hub's join tokens.
func (h *Hub) JoinTokens() *JoinTokens { return h.tokens }

// CreateJoinToken makes a token for a node and audits it. by is the admin
// asking. It returns the token as listed and the whole token to give the
// node, cdj_<secret>.<hub pin>, which is shown once.
func (h *Hub) CreateJoinToken(ctx context.Context, by audit.Actor, name string, ttl time.Duration, addDocker bool) (JoinToken, string, error) {
	t, secret, err := h.tokens.Create(ctx, by.ID, name, ttl, addDocker)
	if err != nil {
		return JoinToken{}, "", err
	}
	h.record(audit.Entry{Actor: by, Action: "nodes.token.create", Target: "node-token:" + t.ID, Tier: "T1",
		Params: params(map[string]any{"name": t.Name, "add_docker": t.AddDocker, "expires_at": t.ExpiresAt.Format(time.RFC3339)}), Result: audit.OK})
	h.log.Info("made a join token", "token", t.ID, "name", t.Name, "expires", t.ExpiresAt, "by", by.Name)
	return t, nodeproto.FormatJoin(secret, h.keys.newest()), nil
}

// RevokeJoinToken stops an unspent token working, and audits it.
func (h *Hub) RevokeJoinToken(ctx context.Context, by audit.Actor, id string) (JoinToken, error) {
	t, err := h.tokens.Revoke(ctx, id)
	if err != nil {
		return JoinToken{}, err
	}
	h.record(audit.Entry{Actor: by, Action: "nodes.token.revoke", Target: "node-token:" + t.ID, Tier: "T1",
		Params: params(map[string]any{"name": t.Name}), Result: audit.OK})
	return t, nil
}

// HubURL is the CHADASH_HUB address a join token's .env block suggests:
// wss://<host>/node from the public URL (ws:// when it's plain http), else
// ws://<the host the admin is using>:3696/node. The admin can edit it.
func HubURL(publicURL, requestHost string) string {
	if u, err := url.Parse(publicURL); err == nil && u.Host != "" {
		scheme := "wss"
		if u.Scheme == "http" {
			scheme = "ws"
		}
		return scheme + "://" + u.Host + nodeproto.Path
	}
	host := requestHost
	if h, _, err := net.SplitHostPort(requestHost); err == nil {
		host = h
	}
	if host == "" {
		host = "chadash.example"
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return "ws://" + host + ":3696" + nodeproto.Path
}

// enrollToken handles an unknown key that presents a join token: it
// enrolls the node as pending, spending the token, and tells it to wait.
// Every outcome is a refusal; the session opens on a retry after approval.
func (h *Hub) enrollToken(ch *nodeproto.Channel, hello nodeproto.Hello, addr string) {
	actor := audit.Actor{Kind: "system", Name: "join-token", IP: addr}
	fp := nodeproto.Fingerprint(ch.PeerPin)
	name := hello.Name
	fail := func(e *nodeproto.Error, tokenID string) {
		h.limit.failed(addr)
		p := map[string]string{"name": name, "via": ViaToken, "fingerprint": fp, "address": addr}
		if tokenID != "" {
			p["token"] = tokenID
		}
		h.record(audit.Entry{Actor: actor, Action: "nodes.enroll", Target: "node:" + name, Params: params(p),
			Result: audit.Refused, Error: &audit.Error{Code: e.Code, Message: e.Message}})
		h.refuse(ch, "", e)
	}
	tok, err := h.tokens.check(h.ctx, hello.Token, name)
	if name == "" && tok.Name != "" {
		name = tok.Name
		tok, err = h.tokens.check(h.ctx, hello.Token, name)
	}
	switch {
	case errors.Is(err, errBadToken):
		fail(nodeproto.Errorf(nodeproto.CodeUnknownNode, "this join token isn't valid: it's wrong, expired, revoked or used already; make a new one in Settings → Nodes"), "")
		return
	case errors.Is(err, errTokenName):
		fail(nodeproto.Errorf(nodeproto.CodeConflict, "this join token is for a node called %q; set CHADASH_NODE_NAME=%s", tok.Name, tok.Name), tok.ID)
		return
	case err != nil:
		h.log.Error("reading join tokens failed", "err", err)
		h.refuse(ch, "", nodeproto.Errorf(nodeproto.CodeFailed, "the hub couldn't read its join tokens"))
		return
	case !nameRE.MatchString(name):
		fail(nodeproto.Errorf(nodeproto.CodeProtocol, "a node name is 1–63 letters, digits, dots, dashes or underscores"), tok.ID)
		return
	case reservedName(name):
		fail(nodeproto.Errorf(nodeproto.CodeProtocol, "a node can't be called %q: the hub uses that name; pick another CHADASH_NODE_NAME", name), tok.ID)
		return
	}
	rec, err := h.reg.enrollPending(h.ctx, name, ch.PeerPin, func(tx *sql.Tx, id string) error {
		return h.tokens.redeem(h.ctx, tx, hello.Token, id, name)
	})
	switch {
	case errors.Is(err, errNameTaken):
		fail(nodeproto.Errorf(nodeproto.CodeConflict, "a node called %q is already enrolled; pick another CHADASH_NODE_NAME, or revoke that node first", name), tok.ID)
		return
	case errors.Is(err, errBadToken):
		// Another node spent it between the check and the transaction.
		fail(nodeproto.Errorf(nodeproto.CodeUnknownNode, "this join token isn't valid: it's wrong, expired, revoked or used already; make a new one in Settings → Nodes"), tok.ID)
		return
	case err != nil:
		h.log.Error("enrolling a node failed", "err", err)
		h.refuse(ch, "", nodeproto.Errorf(nodeproto.CodeFailed, "the hub couldn't record the node"))
		return
	}
	h.record(audit.Entry{Actor: actor, Action: "nodes.enroll", Target: "node:" + rec.ID,
		Params: params(map[string]string{"name": rec.Name, "via": ViaToken, "token": tok.ID, "address": addr, "fingerprint": rec.Fingerprint}), Result: audit.OK})
	h.log.Info("a node enrolled with a join token and waits for approval", "node", rec.ID, "name", rec.Name, "fingerprint", rec.Fingerprint, "client", addr)
	h.pendingSeen(ch, rec, hello, addr)
}

// pendingSeen records what a pending node said about itself (its address,
// version and verbs, for the card an admin approves from), keeps its Inbox
// item current, and tells it to wait. It doesn't count against the
// address's limit: the node proved a key the registry holds.
func (h *Hub) pendingSeen(ch *nodeproto.Channel, rec Record, hello nodeproto.Hello, addr string) {
	if _, err := h.reg.seen(h.ctx, rec.ID, ch.PeerPin, addr, hello.Build, hello.HubPins, hello.Verbs); err != nil {
		h.log.Error("recording a pending node failed", "err", err)
	} else if cur, err := h.reg.get(h.ctx, rec.ID); err == nil {
		rec = cur
	}
	h.raisePending(rec)
	h.refuse(ch, rec.ID, nodeproto.Errorf(nodeproto.CodePending, "this node is waiting for approval"))
}

// ErrNotPending is Approve on a node that isn't waiting for approval.
var ErrNotPending = errNotPending

// Approve lets a pending node in: its next retry opens a session. Only
// pending → approved; anything else is ErrNotPending (or ErrNotFound).
// When its token asked for one, Options.OnApproved adds its Docker
// integration.
func (h *Hub) Approve(ctx context.Context, id string, by audit.Actor) (Record, error) {
	rec, err := h.reg.approve(ctx, id, by.ID)
	if err != nil {
		return Record{}, err
	}
	addDocker := false
	if t, ok, err := h.tokens.forNode(ctx, id); err != nil {
		h.log.Error("reading a node's join token failed", "err", err)
	} else if ok {
		addDocker = t.AddDocker
	}
	h.record(audit.Entry{Actor: by, Action: "nodes.approve", Target: "node:" + id, Tier: "T3",
		Params: params(map[string]any{"name": rec.Name, "fingerprint": rec.Fingerprint, "add_docker": addDocker}), Result: audit.OK})
	h.log.Info("approved a node", "node", id, "name", rec.Name, "by", by.Name)
	h.resolvePending(id, "action", "Approved by "+by.Name+".")
	h.publish(rec, h.Connected(id))
	if h.o.OnApproved != nil {
		h.o.OnApproved(context.WithoutCancel(ctx), rec, addDocker, by)
	}
	return rec, nil
}

// sweepLoop expires pending nodes and prunes tokens hourly (New sweeps
// once first).
func (h *Hub) sweepLoop() {
	defer h.wg.Done()
	t := time.NewTicker(sweepEvery)
	defer t.Stop()
	for {
		select {
		case <-h.ctx.Done():
			return
		case <-t.C:
			h.sweep(h.ctx)
		}
	}
}

// sweep revokes the pending nodes nobody approved within PendingExpiry,
// deletes old tokens, and brings the Inbox in line with the registry.
func (h *Hub) sweep(ctx context.Context) {
	old, err := h.reg.pendingBefore(ctx, h.o.Now().Add(-PendingExpiry))
	if err != nil {
		h.log.Error("finding expired pending nodes failed", "err", err)
	}
	for _, r := range old {
		rec, err := h.reg.revoke(ctx, r.ID, ExpiredBy)
		if err != nil {
			continue // approved or revoked meanwhile
		}
		h.record(audit.Entry{Actor: audit.Actor{Kind: "system", Name: "hub"}, Action: "nodes.expire", Target: "node:" + rec.ID,
			Params: params(map[string]string{"name": rec.Name, "fingerprint": rec.Fingerprint}), Result: audit.OK})
		h.log.Info("a pending node expired unapproved", "node", rec.ID, "name", rec.Name)
		h.resolvePending(rec.ID, "auto", "Nobody approved it within 7 days, so the hub revoked it.")
	}
	if err := h.tokens.prune(ctx); err != nil && ctx.Err() == nil {
		h.log.Error("pruning join tokens failed", "err", err)
	}
	h.syncInbox(ctx)
}

// ── Inbox ────────────────────────────────────────────────────────────────

// incidentsColl is the inbox topic's collection (topics.Incidents).
const incidentsColl = "incidents"

// PendingItem is a pending node's Inbox item ID.
func PendingItem(id string) string { return "node." + id + ".pending" }

// nodeItem is the Inbox item's shape (sim.Incident's JSON).
type nodeItem struct {
	ID           string         `json:"id"`
	Scenario     string         `json:"scenario"`
	Class        string         `json:"class"`
	Severity     string         `json:"severity"`
	Status       string         `json:"status"`
	Phase        string         `json:"phase"`
	Title        string         `json:"title"`
	Summary      string         `json:"summary"`
	Target       string         `json:"target"`
	OpenedAt     time.Time      `json:"opened_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
	ResolvedAt   *time.Time     `json:"resolved_at,omitempty"`
	Facts        map[string]any `json:"facts,omitempty"`
	Causes       []any          `json:"causes"`
	BlastRadius  []any          `json:"blast_radius"`
	Suggested    map[string]any `json:"suggested"`
	ResolvesWhen string         `json:"resolves_when"`
	ResolvedBy   string         `json:"resolved_by,omitempty"`
	Resolution   string         `json:"resolution,omitempty"`
}

// ReviewAction is the pending item's suggested action: the web opens
// Settings → Nodes on it, where Approve (with a step-up) and Reject are.
const ReviewAction = "node.review"

// raisePending opens (or refreshes) a pending node's Inbox item.
func (h *Hub) raisePending(rec Record) {
	if h.o.Inbox == nil {
		return
	}
	// Under inboxMu, and from the registry as it is now: an approve or a
	// revoke that commits meanwhile resolves after this, or this sees it.
	h.inboxMu.Lock()
	defer h.inboxMu.Unlock()
	rec, err := h.reg.get(context.WithoutCancel(h.ctx), rec.ID)
	if err != nil || rec.State != StatePending {
		return
	}
	id := PendingItem(rec.ID)
	now := h.o.Now().UTC()
	facts := map[string]any{"node": rec.ID, "name": rec.Name, "fingerprint": rec.Fingerprint}
	where := ""
	if rec.Address != "" {
		facts["address"] = rec.Address
		where = " from " + rec.Address
	}
	if rec.Version != "" {
		facts["version"] = rec.Version
	}
	if t, ok, err := h.tokens.forNode(h.ctx, rec.ID); err == nil && ok {
		facts["token"] = t.ID
		if t.CreatedByName != "" {
			facts["token_by"] = t.CreatedByName
		}
	}
	item := nodeItem{
		ID: id, Scenario: "node-pending", Class: "node", Severity: "attention", Status: "open", Phase: "open",
		Title: rec.Name + " wants to join as a node",
		Summary: fmt.Sprintf("A node called %s enrolled with a join token%s. Compare its fingerprint, %s, with the node's log before you approve it: "+
			"an approved node gets stack secrets when Cha Dash deploys there.", rec.Name, where, rec.Fingerprint),
		Target: string(ResourceID(rec.ID)), OpenedAt: rec.CreatedAt, UpdatedAt: now, Facts: facts,
		Causes: []any{}, BlastRadius: []any{},
		Suggested:    map[string]any{"id": ReviewAction, "label": "Review in Settings → Nodes", "tier": "T0", "target": string(ResourceID(rec.ID))},
		ResolvesWhen: "Resolves when an admin approves or rejects the node, or after 7 days.",
	}
	_, _ = h.o.Inbox.Update(func(tx *livedoc.Tx) error {
		var cur nodeItem
		if raw := tx.Item(incidentsColl, id); raw != nil && jsonInto(raw, &cur) && cur.Status == "open" {
			// A pending node retries every 30 s: write only what changed.
			if cur.Summary == item.Summary && fmt.Sprint(cur.Facts) == fmt.Sprint(item.Facts) {
				return nil
			}
			item.OpenedAt = cur.OpenedAt
		}
		tx.Put(incidentsColl, id, item)
		return nil
	})
}

func jsonInto(raw json.RawMessage, v any) bool { return json.Unmarshal(raw, v) == nil }

// resolvePending resolves a node's pending item, if it's open.
func (h *Hub) resolvePending(id, by, resolution string) {
	if h.o.Inbox == nil {
		return
	}
	h.inboxMu.Lock()
	defer h.inboxMu.Unlock()
	now := h.o.Now().UTC()
	_, _ = h.o.Inbox.Update(func(tx *livedoc.Tx) error {
		raw := tx.Item(incidentsColl, PendingItem(id))
		var in nodeItem
		if raw == nil || !jsonInto(raw, &in) || in.Status != "open" {
			return nil
		}
		in.Status, in.Phase, in.UpdatedAt, in.ResolvedAt, in.ResolvedBy, in.Resolution = "resolved", "resolved", now, &now, by, resolution
		tx.Put(incidentsColl, in.ID, in)
		return nil
	})
}

// syncInbox opens an item per pending node and resolves the open items of
// nodes that aren't pending any more (approved or revoked while the hub was
// down, or before it had an Inbox).
func (h *Hub) syncInbox(ctx context.Context) {
	if h.o.Inbox == nil {
		return
	}
	recs, err := h.reg.list(ctx)
	if err != nil {
		return
	}
	for _, r := range recs {
		switch r.State {
		case StatePending:
			h.raisePending(r)
		case StateRevoked:
			if r.RevokedBy == ExpiredBy {
				h.resolvePending(r.ID, "auto", "Nobody approved it within 7 days, so the hub revoked it.")
			} else {
				h.resolvePending(r.ID, "action", "Rejected.")
			}
		default:
			h.resolvePending(r.ID, "action", "Approved.")
		}
	}
}
