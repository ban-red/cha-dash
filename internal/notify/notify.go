// Package notify tells people when something needs them: Web Push to their
// browsers, ntfy and webhooks for the household's own tools.
//
// It watches the inbox topic (livedoc) for incident transitions and the
// action engine for finished T4 actions, decides what's worth a message
// (symptom-first defaults, detect.go and rules.go), and delivers each
// message to every channel its rule routes to, with retries and backoff,
// grouping, dedupe and rate limits, keeping a delivery log.
//
// Channel secrets (the ntfy token, the webhook signing secret, the VAPID
// private key) live in the vault and never appear in responses, logs,
// delivery errors or audit params. See README.md for the payloads, the
// signature scheme and the defaults.
package notify

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/authz"
	"dash.cha.sh/internal/livedoc"
	"dash.cha.sh/internal/store"
	"dash.cha.sh/internal/topics"
	"dash.cha.sh/internal/vault"
)

// sentence is an error whose text is a sentence the web can show as is.
type sentence string

func (s sentence) Error() string { return string(s) }

// errEndpointTaken refuses a push subscription whose endpoint another user
// registered.
var errEndpointTaken = sentence("This browser already gets notifications for another account. Turn them off there first.")

// Defaults.
const (
	// DefaultGroupWindow is how long the service collects messages before
	// sending, so many arriving at once (a backup night) become one.
	DefaultGroupWindow = 10 * time.Second
	// dedupeWindow is how long a notified transition holds back the same
	// one for the same incident, unless it resolved in between. It's what
	// stops a restart from repeating every open problem.
	dedupeWindow = 24 * time.Hour
	// retention of events and the delivery log.
	retention = 30 * 24 * time.Hour
	// giveUpAfter fails a delivery still pending this long after it was made.
	giveUpAfter = 48 * time.Hour
	// maxDevices is how many Web Push subscriptions one user may have.
	maxDevices = 20
)

// Options configure the service.
type Options struct {
	Store *store.Store
	Vault *vault.Vault
	// Inbox is the inbox topic's document. Nil watches nothing (tests, and
	// T4 notices still work).
	Inbox *livedoc.Doc
	// InboxView is how a role sees the inbox (server.InboxView): Web Push
	// follows it, so a household device only hears about what its boards
	// show, as a title. Nil: viewers and up see everything, household
	// nothing.
	InboxView func(ctx context.Context, role string) topics.InboxView
	// PublicURL is CHADASH_PUBLIC_URL. Without it, ntfy and webhook
	// messages carry no link.
	PublicURL string
	// Version goes into the User-Agent.
	Version     string
	GroupWindow time.Duration // 0 means DefaultGroupWindow; negative sends at once
	Log         *slog.Logger
	Now         func() time.Time
}

// Service is notifications: channels, rules, devices, delivery.
type Service struct {
	st         *store.Store
	vault      *vault.Vault
	inbox      *livedoc.Doc
	inboxView  func(ctx context.Context, role string) topics.InboxView
	publicURL  string
	userAgent  string
	window     time.Duration
	log        *slog.Logger
	now        func() time.Time
	lanClient  *http.Client
	pushClient *http.Client
	// allowLoopbackPush lets tests register push endpoints on httptest.
	allowLoopbackPush bool
	// actor stands in for authz.ActorFrom in tests.
	actor func(*http.Request) authz.Actor

	// Audit records changes to channels and rules (never a secret value).
	// Set by the server; nil records nothing.
	Audit *authz.Recorder

	mu        sync.Mutex
	rules     map[string]storedRule
	taskQueue []action.TaskDone
	limits    map[string]*bucket

	vapidMu  sync.Mutex
	vapidKey *vapidKey

	// The loop's own state.
	det          *detector
	incs         map[string]incident
	lastVersion  uint64
	pending      []Event
	pendingSince time.Time
	lastPrune    time.Time
}

// New returns the service, with the built-in Web Push channel in place.
func New(ctx context.Context, o Options) (*Service, error) {
	if o.Store == nil {
		return nil, errors.New("notify: Store is required")
	}
	s := &Service{
		st: o.Store, vault: o.Vault, inbox: o.Inbox, inboxView: o.InboxView, publicURL: strings.TrimSuffix(o.PublicURL, "/"),
		window: o.GroupWindow, log: o.Log, now: o.Now,
		lanClient: newClient(policyLAN), pushClient: newClient(policyPublic),
		det: newDetector(), incs: map[string]incident{}, limits: map[string]*bucket{},
	}
	if s.window == 0 {
		s.window = DefaultGroupWindow
	}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.inboxView == nil {
		s.inboxView = func(_ context.Context, role string) topics.InboxView {
			return topics.InboxView{Detail: authz.SeesInboxDetail(role), Approvals: authz.SeesApprovals(role)}
		}
	}
	v := o.Version
	if v == "" {
		v = "dev"
	}
	s.userAgent = "chadash/" + v + " (+https://dash.cha.sh)"
	if err := s.ensureWebPush(ctx); err != nil {
		return nil, fmt.Errorf("notify: %w", err)
	}
	if err := s.loadRules(ctx); err != nil {
		return nil, fmt.Errorf("notify: %w", err)
	}
	return s, nil
}

// Run watches the inbox and delivers until ctx ends.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		s.step(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// step is one turn of the loop: read the inbox, take finished T4 tasks,
// send what's due.
func (s *Service) step(ctx context.Context) {
	now := s.now()
	if s.inbox != nil {
		v := s.inbox.Version()
		if v != s.lastVersion || s.det.pendingGrace() {
			if v != s.lastVersion {
				_, doc := s.inbox.Snapshot()
				s.incs = parseIncidents(doc[topics.Incidents])
				s.lastVersion = v
			}
			s.handle(ctx, now, s.det.observe(now, s.incs, s.grace()))
		}
	}
	s.mu.Lock()
	done := s.taskQueue
	s.taskQueue = nil
	s.mu.Unlock()
	for _, d := range done {
		s.handleTask(ctx, now, d)
	}
	if len(s.pending) > 0 && (s.window < 0 || !now.Before(s.pendingSince.Add(s.window))) {
		s.flush(ctx, now)
	}
	s.deliverDue(ctx)
	if now.Sub(s.lastPrune) >= time.Hour {
		s.lastPrune = now
		s.prune(ctx, now)
	}
}

// ── events ───────────────────────────────────────────────────────────────

// Event is one thing worth telling someone about.
type Event struct {
	ID       string
	Rule     string
	Kind     string
	Severity string
	Title    string
	Body     string
	Path     string
	Tag      string
	At       time.Time
	Incident *IncidentRef
	Task     *TaskRef

	transition string
	raw        json.RawMessage // the incident as published, for each viewer's projection
	admins     bool            // Web Push: admins and the owner only
}

func newID(prefix string) string { return prefix + strings.ToLower(rand.Text()) }

// eventFor turns a candidate into an event.
func eventFor(c candidate, now time.Time) Event {
	in := c.inc
	e := Event{ID: newID("ne_"), Rule: c.rule, At: now.UTC(), Path: incidentPath(in.ID), Tag: in.ID, transition: c.transition, raw: in.raw,
		Incident: &IncidentRef{ID: in.ID, Target: in.Target, Scenario: in.Scenario, Class: in.Class, Severity: in.Severity, Status: in.Status, Category: in.Category}}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		title = in.Target
	}
	since := now
	if t, err := time.Parse(time.RFC3339, in.OpenedAt); err == nil {
		since = t
	}
	summary := strings.TrimSpace(in.Summary)
	join := func(parts ...string) string {
		var out []string
		for _, p := range parts {
			if p != "" {
				out = append(out, p)
			}
		}
		return strings.Join(out, " ")
	}
	switch c.transition {
	case trDown:
		e.Kind, e.Severity, e.Title = KindDown, "down", title
		e.Body = join(summary, "Down since "+hhmm(since)+".")
	case trAttention:
		e.Kind, e.Severity, e.Title = KindAttention, "attention", title
		e.Body = join(summary, "Open since "+hhmm(since)+".")
	case trRepair:
		e.Kind, e.Severity, e.Title = KindRepair, "attention", title
		e.Body = join(summary, "Reconnect it in Settings → Integrations.")
	case trStuck:
		e.Kind, e.Severity, e.Title = KindStuck, "down", title
		e.Body = summary
	case trApproval:
		e.Kind, e.Severity, e.Title = KindApproval, "attention", title
		e.Body = join(summary, "Approve or deny it in the Inbox within the hour.")
	case trResolved:
		e.Kind, e.Severity, e.Title = KindResolved, "info", "Resolved: "+title
		at := now
		var r struct {
			ResolvedAt string `json:"resolved_at"`
		}
		if json.Unmarshal(in.raw, &r) == nil {
			if t, err := time.Parse(time.RFC3339, r.ResolvedAt); err == nil {
				at = t
			}
		}
		e.Body = "Resolved at " + hhmm(at) + "."
	}
	return e
}

// dedupeTransitions are the transitions that hold one back: attention is
// moot once it was down.
func dedupeTransitions(tr string) []string {
	if tr == trAttention {
		return []string{trAttention, trDown}
	}
	return []string{tr}
}

// handle records and queues the candidates the rules let through.
func (s *Service) handle(ctx context.Context, now time.Time, cands []candidate) {
	for _, c := range cands {
		if c.transition == trResolved {
			s.resolve(ctx, now, c)
			continue
		}
		if !s.settings(c.rule).Enabled {
			continue
		}
		dup, err := s.recentlyNotified(ctx, c.inc.ID, dedupeTransitions(c.transition), now)
		if err != nil {
			s.log.Error("notify: dedupe check failed", "incident", c.inc.ID, "err", err)
			continue
		}
		if dup {
			continue
		}
		e := eventFor(c, now)
		if err := s.recordEvent(ctx, e, true); err != nil {
			s.log.Error("notify: recording an event failed", "incident", c.inc.ID, "err", err)
			continue
		}
		s.queue(e, now)
	}
}

// resolve sends a resolution for an incident whose opening was notified,
// when that rule wants resolutions; either way it's recorded, so a
// reopening notifies again.
func (s *Service) resolve(ctx context.Context, now time.Time, c candidate) {
	rule, ok, err := s.lastOpening(ctx, c.inc.ID, now)
	if err != nil {
		s.log.Error("notify: reading events failed", "incident", c.inc.ID, "err", err)
		return
	}
	if !ok {
		return
	}
	rs := s.settings(rule)
	notified := rs.Enabled && rs.Resolved
	c.rule = rule
	e := eventFor(c, now)
	if err := s.recordEvent(ctx, e, notified); err != nil {
		s.log.Error("notify: recording an event failed", "incident", c.inc.ID, "err", err)
		return
	}
	if notified {
		s.queue(e, now)
	}
}

func (s *Service) queue(e Event, now time.Time) {
	if len(s.pending) == 0 {
		s.pendingSince = now
	}
	s.pending = append(s.pending, e)
}

func (s *Service) recordEvent(ctx context.Context, e Event, notified bool) error {
	inc := ""
	if e.Incident != nil {
		inc = e.Incident.ID
	}
	_, err := s.st.Writer().ExecContext(ctx, `INSERT INTO notify_events (id, rule, incident, transition, title, notified, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		e.ID, e.Rule, inc, e.transition, clip(e.Title, maxTitle), notified, e.At.UnixMilli())
	return err
}

// recentlyNotified: one of transitions was notified for the incident within
// the dedupe window, and it hasn't resolved since.
func (s *Service) recentlyNotified(ctx context.Context, incident string, transitions []string, now time.Time) (bool, error) {
	args := []any{incident, now.Add(-dedupeWindow).UnixMilli()}
	marks := make([]string, len(transitions))
	for i, t := range transitions {
		marks[i] = "?"
		args = append(args, t)
	}
	var n int
	err := s.st.Reader().QueryRowContext(ctx, `SELECT count(*) FROM notify_events o WHERE o.incident = ? AND o.notified = 1 AND o.created_at >= ?
		AND o.transition IN (`+strings.Join(marks, ", ")+`)
		AND NOT EXISTS (SELECT 1 FROM notify_events r WHERE r.incident = o.incident AND r.transition = 'resolved' AND r.rowid > o.rowid)`, args...).Scan(&n)
	return n > 0, err
}

// lastOpening is the rule of the incident's notified opening since it last
// resolved, within the dedupe window.
func (s *Service) lastOpening(ctx context.Context, incident string, now time.Time) (string, bool, error) {
	var rule string
	err := s.st.Reader().QueryRowContext(ctx, `SELECT o.rule FROM notify_events o WHERE o.incident = ? AND o.notified = 1 AND o.transition <> 'resolved' AND o.created_at >= ?
		AND NOT EXISTS (SELECT 1 FROM notify_events r WHERE r.incident = o.incident AND r.transition = 'resolved' AND r.rowid > o.rowid)
		ORDER BY o.rowid DESC LIMIT 1`, incident, now.Add(-dedupeWindow).UnixMilli()).Scan(&rule)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return rule, err == nil, err
}

// ── critical actions ─────────────────────────────────────────────────────

// TaskDone is the action engine's OnTaskDone: every T4 task that ends
// becomes a notice to the admins and the owner. It only queues; the loop
// does the rest.
func (s *Service) TaskDone(d action.TaskDone) {
	if d.Tier != action.T4 {
		return
	}
	s.mu.Lock()
	s.taskQueue = append(s.taskQueue, d)
	s.mu.Unlock()
}

func (s *Service) handleTask(ctx context.Context, now time.Time, d action.TaskDone) {
	t := d.Task
	who := d.Actor.Name
	if who == "" {
		who = t.Actor.Name
	}
	at := now
	if t.FinishedAt != nil {
		at = *t.FinishedAt
	}
	e := Event{ID: newID("ne_"), Rule: RuleAction, Kind: KindAction, At: now.UTC(), Path: auditPath, Tag: t.ID, transition: trAction, admins: true,
		Title: "Critical action: " + t.Label,
		Task:  &TaskRef{ID: t.ID, Action: t.Action, Label: t.Label, Target: t.Target, State: t.State, Actor: who}}
	if t.Audit != nil {
		e.Task.Audit = *t.Audit
	}
	switch t.State {
	case action.StateSucceeded:
		e.Severity = "attention"
		e.Body = fmt.Sprintf("%s ran it at %s. It finished.", who, hhmm(at))
	case action.StateCancelled:
		e.Severity = "attention"
		e.Body = fmt.Sprintf("%s ran it at %s, and it was cancelled.", who, hhmm(at))
	default:
		e.Severity = "down"
		msg := "It failed."
		if t.Error != nil && t.Error.Message != "" {
			msg = "It failed: " + t.Error.Message
		}
		e.Body = fmt.Sprintf("%s ran it at %s. %s", who, hhmm(at), msg)
	}
	if err := s.recordEvent(ctx, e, true); err != nil {
		s.log.Error("notify: recording an event failed", "task", t.ID, "err", err)
		return
	}
	s.queue(e, now)
}

// ── flushing ─────────────────────────────────────────────────────────────

// flush turns the queued events into deliveries, per channel and device.
func (s *Service) flush(ctx context.Context, now time.Time) {
	evs := s.pending
	s.pending = nil
	chans, err := s.channelRows(ctx, "WHERE enabled = 1")
	if err != nil {
		s.log.Error("notify: listing channels failed", "err", err)
		return
	}
	for _, ch := range chans {
		var mine []Event
		for _, e := range evs {
			if s.routes(e.Rule, ch.id) {
				mine = append(mine, e)
			}
		}
		if len(mine) == 0 {
			continue
		}
		if ch.kind == ChannelWebPush {
			s.queuePush(ctx, ch, mine, now)
			continue
		}
		for _, m := range compose(mine, func() string { return newID("ng_") }) {
			s.addDelivery(ctx, ch, nil, withURLs(m, s.publicURL), now)
		}
	}
}

// queuePush makes a delivery per device, each with what its user may see.
func (s *Service) queuePush(ctx context.Context, ch channelRow, evs []Event, now time.Time) {
	subs, err := s.subscriptions(ctx, "")
	if err != nil {
		s.log.Error("notify: listing devices failed", "err", err)
		return
	}
	views := map[string]topics.InboxView{}
	for _, sub := range subs {
		var vis []Event
		for _, e := range evs {
			if v, ok := s.forViewer(ctx, e, sub.Role, views); ok {
				vis = append(vis, v)
			}
		}
		for _, m := range compose(vis, func() string { return newID("ng_") }) {
			s.addDelivery(ctx, ch, &sub, m, now)
		}
	}
}

// forViewer is what a user with role may hear about e, if anything: T4
// notices reach admins and the owner; incidents follow the inbox's view of
// the role (household: what its boards show, as a title only).
func (s *Service) forViewer(ctx context.Context, e Event, role string, views map[string]topics.InboxView) (Event, bool) {
	if e.admins {
		return e, authz.Admin.Allows(role)
	}
	if e.Incident == nil {
		return e, false
	}
	v, ok := views[role]
	if !ok {
		v = s.inboxView(ctx, role)
		views[role] = v
	}
	if e.Incident.Class == topics.ApprovalClass {
		// Only approvers see plans waiting for a person, as in the inbox.
		return e, v.Detail && v.Approvals
	}
	if v.Detail {
		return e, true
	}
	if v.Relevant == nil || e.raw == nil {
		return e, false
	}
	if _, ok := topics.ProjectIncident(e.raw, v.Relevant); !ok {
		return e, false
	}
	e.Body, e.Path, e.Incident, e.raw = "", "/", nil, nil
	return e, true
}

// ── devices ──────────────────────────────────────────────────────────────

// subscriptions lists devices of enabled users; userID narrows to one.
func (s *Service) subscriptions(ctx context.Context, userID string) ([]subscription, error) {
	q := `SELECT p.id, p.user_id, u.role, p.endpoint, p.p256dh, p.auth, p.label FROM notify_push_subscriptions p JOIN users u ON u.id = p.user_id
		WHERE u.disabled_at IS NULL`
	var args []any
	if userID != "" {
		q += ` AND p.user_id = ?`
		args = append(args, userID)
	}
	rows, err := s.st.Reader().QueryContext(ctx, q+` ORDER BY p.created_at`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []subscription
	for rows.Next() {
		var sub subscription
		if err := rows.Scan(&sub.ID, &sub.UserID, &sub.Role, &sub.Endpoint, &sub.P256dh, &sub.Auth, &sub.Label); err != nil {
			return nil, err
		}
		out = append(out, sub)
	}
	return out, rows.Err()
}

func (s *Service) subscription(ctx context.Context, id string) (subscription, error) {
	var sub subscription
	err := s.st.Reader().QueryRowContext(ctx, `SELECT p.id, p.user_id, u.role, p.endpoint, p.p256dh, p.auth, p.label FROM notify_push_subscriptions p
		JOIN users u ON u.id = p.user_id WHERE p.id = ? AND u.disabled_at IS NULL`, id).
		Scan(&sub.ID, &sub.UserID, &sub.Role, &sub.Endpoint, &sub.P256dh, &sub.Auth, &sub.Label)
	if errors.Is(err, sql.ErrNoRows) {
		return sub, errNotFound
	}
	return sub, err
}

// Device is one of a user's push subscriptions, as the API shows it: never
// the endpoint or its keys.
type Device struct {
	ID          string     `json:"id"`
	Label       string     `json:"label"`
	Host        string     `json:"host"`        // the push service
	Fingerprint string     `json:"fingerprint"` // hex SHA-256 of the endpoint, first 16: lets a browser find itself
	CreatedAt   time.Time  `json:"created_at"`
	LastOKAt    *time.Time `json:"last_ok_at"`
	LastError   *string    `json:"last_error"`
}

// Devices lists a user's devices.
func (s *Service) Devices(ctx context.Context, userID string) ([]Device, error) {
	rows, err := s.st.Reader().QueryContext(ctx, `SELECT id, endpoint, label, created_at, last_ok_at, last_error FROM notify_push_subscriptions WHERE user_id = ? ORDER BY created_at`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Device{}
	for rows.Next() {
		var d Device
		var endpoint string
		var created int64
		var ok sql.NullInt64
		var lastErr sql.NullString
		if err := rows.Scan(&d.ID, &endpoint, &d.Label, &created, &ok, &lastErr); err != nil {
			return nil, err
		}
		d.Host, d.Fingerprint, d.CreatedAt = hostOf(endpoint), fingerprint(endpoint), time.UnixMilli(created).UTC()
		if ok.Valid {
			t := time.UnixMilli(ok.Int64).UTC()
			d.LastOKAt = &t
		}
		if lastErr.Valid {
			d.LastError = &lastErr.String
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// SubscribeInput is a browser's PushSubscription.toJSON(), plus a label.
type SubscribeInput struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
	ExpirationTime *float64 `json:"expirationTime"`
	Label          string   `json:"label"`
}

// Subscribe stores (or refreshes) a device for a user. An endpoint another
// user registered is refused with errEndpointTaken and left as it is: the
// other account removes it first (signing out of a shared browser does).
func (s *Service) Subscribe(ctx context.Context, userID string, in SubscribeInput) (Device, error) {
	if _, err := s.checkEndpoint(in.Endpoint); err != nil {
		return Device{}, &invalidError{[]FieldError{{"/endpoint", err.Error()}}}
	}
	p256dh, err := decodeB64(in.Keys.P256dh)
	if err != nil || len(p256dh) != 65 {
		return Device{}, &invalidError{[]FieldError{{"/keys/p256dh", "The subscription's p256dh key isn't a P-256 public key."}}}
	}
	if _, err := ecdhPoint(p256dh); err != nil {
		return Device{}, &invalidError{[]FieldError{{"/keys/p256dh", "The subscription's p256dh key isn't a P-256 public key."}}}
	}
	authKey, err := decodeB64(in.Keys.Auth)
	if err != nil || len(authKey) != 16 {
		return Device{}, &invalidError{[]FieldError{{"/keys/auth", "The subscription's auth secret isn't 16 bytes."}}}
	}
	label := cleanLabel(in.Label)
	var n int
	if err := s.st.Reader().QueryRowContext(ctx, `SELECT count(*) FROM notify_push_subscriptions WHERE user_id = ? AND endpoint <> ?`, userID, in.Endpoint).Scan(&n); err != nil {
		return Device{}, err
	}
	if n >= maxDevices {
		return Device{}, sentence(fmt.Sprintf("This account already has %d devices; remove one first.", maxDevices))
	}
	id := newID("ps_")
	// A known endpoint is refreshed only for the user who registered it.
	// Someone else's is refused: rebinding it would take over their
	// device's notifications.
	res, err := s.st.Writer().ExecContext(ctx, `INSERT INTO notify_push_subscriptions (id, user_id, endpoint, p256dh, auth, label, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (endpoint) DO UPDATE SET p256dh = excluded.p256dh, auth = excluded.auth, label = excluded.label, last_error = NULL
		WHERE notify_push_subscriptions.user_id = excluded.user_id`,
		id, userID, in.Endpoint, p256dh, authKey, label, s.now().UnixMilli())
	if err != nil {
		return Device{}, err
	}
	if n, err := res.RowsAffected(); err != nil {
		return Device{}, err
	} else if n == 0 {
		return Device{}, errEndpointTaken
	}
	devs, err := s.Devices(ctx, userID)
	if err != nil {
		return Device{}, err
	}
	fp := fingerprint(in.Endpoint)
	for _, d := range devs {
		if d.Fingerprint == fp {
			return d, nil
		}
	}
	return Device{}, errNotFound
}

// Unsubscribe forgets a user's device by its endpoint.
func (s *Service) Unsubscribe(ctx context.Context, userID, endpoint string) error {
	res, err := s.st.Writer().ExecContext(ctx, `DELETE FROM notify_push_subscriptions WHERE user_id = ? AND endpoint = ?`, userID, endpoint)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errNotFound
	}
	return nil
}

// RemoveDevice forgets one of a user's devices by ID.
func (s *Service) RemoveDevice(ctx context.Context, userID, id string) error {
	res, err := s.st.Writer().ExecContext(ctx, `DELETE FROM notify_push_subscriptions WHERE user_id = ? AND id = ?`, userID, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errNotFound
	}
	return nil
}

func cleanLabel(l string) string {
	l = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, strings.TrimSpace(l))
	return clip(l, 64)
}

// ── rate limits ──────────────────────────────────────────────────────────

// A destination (a channel, or one device) gets rateBurst messages at
// once, then one every rateEvery. Grouping keeps real nights well inside
// it; past it, messages are dropped (and logged as dropped), not queued,
// so a storm can't turn into a backlog. Critical actions and tests are
// never limited.
const (
	rateBurst = 10
	rateEvery = 2 * time.Minute
)

type bucket struct {
	tokens float64
	last   time.Time
}

func (s *Service) allow(key string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.limits[key]
	if b == nil {
		b = &bucket{tokens: rateBurst, last: now}
		s.limits[key] = b
	}
	b.tokens = min(rateBurst, b.tokens+now.Sub(b.last).Seconds()/rateEvery.Seconds())
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// sortedIDs is a map's keys in order, so the detector is deterministic.
func sortedIDs[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
