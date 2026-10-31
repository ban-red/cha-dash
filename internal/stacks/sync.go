package stacks

import (
	"context"
	crand "crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"time"

	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/audit"
	"dash.cha.sh/internal/auth"
	"dash.cha.sh/internal/livedoc"
	"dash.cha.sh/internal/stackproto"
	"dash.cha.sh/internal/vault"
)

// Git sync (ADR-0021; 09 · v0.3 → Git sync and deploy webhooks). A sync
// makes one stack on one node follow a branch and a path on a git remote.
// The node checks and fetches (stack.remote.check, stack.fetch); the hub
// gates the new tip, saves it as the stack's draft with source git:<sha>,
// and plans stack.deploy as the sync's actor (kind sync, acting for the
// admin who owns it). Deploy on push approves and runs it; draft and ask
// submits it for a person. A deploy that rolls back marks its commit
// rejected, so the sync waits for a newer one.

// problem is an error whose text is a sentence for the person.
type problem string

func (p problem) Error() string { return string(p) }

// Sync modes and states (the stack_syncs table's).
const (
	ModeAsk    = "ask"
	ModeDeploy = "deploy"

	StateActive = "active"
	StateHeld   = "held"
	StatePaused = "paused"
)

// Poll intervals, in seconds.
const (
	DefaultPollSeconds = 300
	MinPollSeconds     = 60
	MaxPollSeconds     = 86400
)

// Vault names. They sit outside stacks/ (secretPrefix), so a stack's compose
// file can never reference them and they never show in the secrets list.
func syncCredentialName(id string) string { return "stacksync/" + id + "/credential" }
func hookSecretName(id string) string     { return "stackhook/" + id }

// fetchTimeout bounds a check or a fetch from the hub's side: the node's
// own limit is 60 s.
const fetchTimeout = 75 * time.Second

// failingAfter is how many failed polls in a row open the Inbox hint.
const failingAfter = 3

// Sync is one row of stack_syncs.
type Sync struct {
	ID, Node, Stack        string
	URL, Branch, Path      string
	AuthKind, HostKey      string
	Mode                   string
	PollSeconds            int
	SecretsAllow           []string
	OwnerID                string // "" once the owner was deleted
	State, StateReason     string
	LastSeen, LastDeployed string
	// Rejected are the commits that failed their health gate, newest
	// first, at most maxRejected. The rejected_sha column holds them
	// space-separated, so its first word is still the latest one.
	Rejected             []string
	LastError            string
	CreatedAt, UpdatedAt time.Time
}

// maxRejected is how many rejected commits a sync remembers.
const maxRejected = 20

// isRejected reports whether a commit failed its health gate here.
func (y *Sync) isRejected(sha string) bool { return sha != "" && slices.Contains(y.Rejected, sha) }

// reject remembers a commit that failed its health gate, newest first.
func (y *Sync) reject(sha string) {
	rest := slices.DeleteFunc(slices.Clone(y.Rejected), func(s string) bool { return s == sha })
	y.Rejected = append([]string{sha}, rest...)
	if len(y.Rejected) > maxRejected {
		y.Rejected = y.Rejected[:maxRejected]
	}
}

// latestRejected is the newest rejected commit, or "".
func (y *Sync) latestRejected() string {
	if len(y.Rejected) == 0 {
		return ""
	}
	return y.Rejected[0]
}

// credential is what the vault holds for a sync's remote: the user and the
// token or SSH private key. It never leaves the hub except inside a
// RemoteAuth, for one call.
type credential struct {
	Username string `json:"username,omitempty"`
	Secret   string `json:"secret,omitempty"`
}

const syncColumns = `id, node, stack, url, branch, path, auth_kind, COALESCE(host_key, ''), mode, poll_seconds, secrets_allow,
	COALESCE(owner_id, ''), state, state_reason, last_seen_sha, last_deployed_sha, rejected_sha, last_error, created_at, updated_at`

func scanSync(row interface{ Scan(...any) error }) (*Sync, error) {
	var y Sync
	var allow, rejected string
	var created, updated int64
	if err := row.Scan(&y.ID, &y.Node, &y.Stack, &y.URL, &y.Branch, &y.Path, &y.AuthKind, &y.HostKey, &y.Mode, &y.PollSeconds, &allow,
		&y.OwnerID, &y.State, &y.StateReason, &y.LastSeen, &y.LastDeployed, &rejected, &y.LastError, &created, &updated); err != nil {
		return nil, err
	}
	y.Rejected = strings.Fields(rejected)
	if err := json.Unmarshal([]byte(allow), &y.SecretsAllow); err != nil || y.SecretsAllow == nil {
		y.SecretsAllow = []string{}
	}
	y.CreatedAt, y.UpdatedAt = time.UnixMilli(created).UTC(), time.UnixMilli(updated).UTC()
	return &y, nil
}

// syncByStack is the stack's sync, or nil.
func (s *Service) syncByStack(ctx context.Context, node, stack string) (*Sync, error) {
	y, err := scanSync(s.st.Reader().QueryRowContext(ctx, `SELECT `+syncColumns+` FROM stack_syncs WHERE node = ? AND stack = ?`, node, stack))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("stacks: read sync: %w", err)
	}
	return y, nil
}

// syncByID is a sync, or nil.
func (s *Service) syncByID(ctx context.Context, id string) (*Sync, error) {
	y, err := scanSync(s.st.Reader().QueryRowContext(ctx, `SELECT `+syncColumns+` FROM stack_syncs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("stacks: read sync: %w", err)
	}
	return y, nil
}

// syncs lists every sync, oldest first.
func (s *Service) syncs(ctx context.Context) ([]*Sync, error) {
	rows, err := s.st.Reader().QueryContext(ctx, `SELECT `+syncColumns+` FROM stack_syncs ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("stacks: list syncs: %w", err)
	}
	defer rows.Close()
	var out []*Sync
	for rows.Next() {
		y, err := scanSync(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, y)
	}
	return out, rows.Err()
}

// syncsFollowing lists the syncs on a remote (normalized) and branch.
func (s *Service) syncsFollowing(ctx context.Context, url, branch string) ([]*Sync, error) {
	all, err := s.syncs(ctx)
	if err != nil {
		return nil, err
	}
	var out []*Sync
	for _, y := range all {
		if y.Branch == branch && normalizeURL(y.URL) == url {
			out = append(out, y)
		}
	}
	return out, nil
}

// insertSync stores a new sync.
func (s *Service) insertSync(ctx context.Context, y *Sync) error {
	allow, _ := json.Marshal(y.SecretsAllow)
	now := s.now().UTC().Truncate(time.Millisecond)
	y.CreatedAt, y.UpdatedAt = now, now
	_, err := s.st.Writer().ExecContext(ctx, `INSERT INTO stack_syncs (id, node, stack, url, branch, path, auth_kind, host_key, mode, poll_seconds,
		secrets_allow, owner_id, state, state_reason, last_seen_sha, last_deployed_sha, rejected_sha, last_error, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		y.ID, y.Node, y.Stack, y.URL, y.Branch, y.Path, y.AuthKind, nullable(y.HostKey), y.Mode, y.PollSeconds, string(allow),
		nullable(y.OwnerID), y.State, y.StateReason, y.LastSeen, y.LastDeployed, strings.Join(y.Rejected, " "), y.LastError, now.UnixMilli(), now.UnixMilli())
	return err
}

// updateSync writes every field of y back.
func (s *Service) updateSync(ctx context.Context, y *Sync) error {
	allow, _ := json.Marshal(y.SecretsAllow)
	y.UpdatedAt = s.now().UTC().Truncate(time.Millisecond)
	_, err := s.st.Writer().ExecContext(ctx, `UPDATE stack_syncs SET url = ?, branch = ?, path = ?, auth_kind = ?, host_key = ?, mode = ?,
		poll_seconds = ?, secrets_allow = ?, owner_id = ?, state = ?, state_reason = ?, last_seen_sha = ?, last_deployed_sha = ?,
		rejected_sha = ?, last_error = ?, updated_at = ? WHERE id = ?`,
		y.URL, y.Branch, y.Path, y.AuthKind, nullable(y.HostKey), y.Mode, y.PollSeconds, string(allow), nullable(y.OwnerID),
		y.State, y.StateReason, y.LastSeen, y.LastDeployed, strings.Join(y.Rejected, " "), y.LastError, y.UpdatedAt.UnixMilli(), y.ID)
	return err
}

// patchSync re-reads a sync and applies f to it, so a long fetch doesn't
// write back fields someone changed meanwhile. Nil when it's gone.
func (s *Service) patchSync(ctx context.Context, id string, f func(y *Sync)) (*Sync, error) {
	y, err := s.syncByID(ctx, id)
	if err != nil || y == nil {
		return nil, err
	}
	f(y)
	return y, s.updateSync(context.WithoutCancel(ctx), y)
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// newID is a prefixed random ID (prefix plus 128+ random bits).
func newID(prefix string) string { return prefix + strings.ToLower(crand.Text()) }

// credentialOf reads a sync's credential from the vault (empty for none).
func (s *Service) credentialOf(ctx context.Context, id string) (credential, error) {
	var c credential
	if s.vault == nil {
		return c, nil
	}
	raw, err := s.vault.Get(ctx, syncCredentialName(id))
	if errors.Is(err, vault.ErrNotFound) {
		return c, nil
	}
	if err != nil {
		return c, fmt.Errorf("stacks: read a sync's credential: %w", err)
	}
	err = json.Unmarshal(raw, &c)
	clear(raw)
	return c, err
}

// remoteAuth is the RemoteAuth for one call to the node.
func (s *Service) remoteAuth(ctx context.Context, y *Sync) (stackproto.RemoteAuth, error) {
	a := stackproto.RemoteAuth{Kind: y.AuthKind}
	if y.AuthKind == stackproto.AuthNone {
		return a, nil
	}
	c, err := s.credentialOf(ctx, y.ID)
	if err != nil {
		return a, err
	}
	a.Username, a.Secret = c.Username, c.Secret
	return a, nil
}

// ── the Inbox ────────────────────────────────────────────────────────────

// syncItemID is a sync's one Inbox item: a rejected or blocked commit, a
// held or paused sync, or a remote that keeps failing.
func syncItemID(y *Sync) string { return "stack." + y.Node + "." + y.Stack + ".sync" }

// syncItem opens (or updates) the sync's Inbox item. severity is
// "attention" (needs attention) or "info" (a hint).
func (s *Service) syncItem(y *Sync, kind, severity, title, summary string) {
	if s.inbox == nil {
		return
	}
	id := syncItemID(y)
	target := Target(y.Node, y.Stack)
	now := s.now().UTC()
	_, _ = s.inbox.Update(func(tx *livedoc.Tx) error {
		opened := now
		if raw := tx.Item(incidentsColl, id); raw != nil {
			var old incident
			if json.Unmarshal(raw, &old) == nil && old.Status == "open" {
				opened = old.OpenedAt
			}
		}
		tx.Put(incidentsColl, id, incident{
			ID: id, Scenario: "stack-sync", Class: "problem", Severity: severity, Status: "open", Phase: "open",
			Title: title, Summary: summary, Target: target, OpenedAt: opened, UpdatedAt: now,
			Facts:  map[string]any{"node": y.Node, "stack": y.Stack, "sync": y.ID, "kind": kind, "branch": y.Branch, "remote": remoteHost(y.URL)},
			Causes: []any{}, BlastRadius: []any{},
			Suggested:    map[string]any{"id": "link.open", "label": "Open Settings → Stacks", "tier": "T0", "target": target},
			ResolvesWhen: "The sync deploys a commit, or an admin resumes it.",
		})
		return nil
	})
}

// ApprovalEnded is the action engine's OnApprovalEnded: a sync's approval
// that was denied or expired takes its "waits for approval" item with it.
// A newer commit's withdraw ends the old one before the new one asks.
func (s *Service) ApprovalEnded(apv action.Approval) {
	if apv.Originator.Kind != action.KindSync || apv.Originator.ID == "" {
		return
	}
	y, err := s.syncByID(context.Background(), apv.Originator.ID)
	if err != nil || y == nil {
		return
	}
	s.clearSyncItem(y, itemAsks)
}

// clearSyncItem resolves the sync's Inbox item, if one is open. With kinds,
// only an item of one of those kinds.
func (s *Service) clearSyncItem(y *Sync, kinds ...string) {
	if s.inbox == nil {
		return
	}
	id := syncItemID(y)
	now := s.now().UTC()
	_, _ = s.inbox.Update(func(tx *livedoc.Tx) error {
		raw := tx.Item(incidentsColl, id)
		if raw == nil {
			return nil
		}
		var in incident
		if json.Unmarshal(raw, &in) != nil || in.Status == "resolved" {
			return nil
		}
		if k, _ := in.Facts["kind"].(string); len(kinds) > 0 && !slices.Contains(kinds, k) {
			return nil
		}
		in.Status, in.Phase, in.UpdatedAt, in.ResolvedAt, in.ResolvedBy = "resolved", "resolved", now, &now, "sync"
		tx.Put(incidentsColl, id, in)
		return nil
	})
}

// Kinds of a sync's Inbox item.
const (
	itemRejected = "rejected"
	itemBlocked  = "blocked"
	itemHeld     = "held"
	itemPaused   = "paused"
	itemFailing  = "failing"
	itemWaiting  = "waiting" // a commit the gate refused (drift, an invalid file)
	itemAsks     = "asks"    // deploy on push asked a person first, and why
)

// ── results of a sync's deploy ───────────────────────────────────────────

// syncDeployed records how a deploy of a git draft ended (executeDeploy):
// deployed sets last_deployed_sha; rolled back marks the commit rejected and
// says so in the Inbox; blocked says why.
func (s *Service) syncDeployed(ctx context.Context, node, stack, sha string, res stackproto.DeployResult) {
	y, err := s.syncByStack(ctx, node, stack)
	if err != nil || y == nil {
		return
	}
	still := "the previous version"
	if y.LastDeployed != "" {
		still = short(y.LastDeployed)
	}
	switch res.Outcome {
	case stackproto.OutcomeDeployed, stackproto.OutcomeUnchanged:
		y.LastDeployed, y.LastError = sha, ""
		s.clearSyncItem(y, itemRejected, itemBlocked, itemWaiting, itemFailing, itemAsks)
	case stackproto.OutcomeRolledBack:
		y.reject(sha)
		y.LastError = "Commit " + short(sha) + " failed its health gate."
		s.syncItem(y, itemRejected, "attention", "Commit "+short(sha)+" of "+stack+" was rolled back",
			fmt.Sprintf("Commit %s failed its health gate; %s is still running. The sync waits for a newer commit on %s. %s", short(sha), still, y.Branch, res.Reason))
	case stackproto.OutcomeBlocked:
		y.LastError = "Commit " + short(sha) + " was blocked: " + res.Reason
		s.syncItem(y, itemBlocked, "attention", "Commit "+short(sha)+" of "+stack+" was blocked",
			fmt.Sprintf("Commit %s was blocked before anything stopped: %s %s is still running.", short(sha), sentence(res.Reason), still))
	default:
		return
	}
	if err := s.updateSync(context.WithoutCancel(ctx), y); err != nil {
		s.log.Error("stacks: record a sync's deploy", "sync", y.ID, "err", err)
	}
}

// holdForRollback holds a stack's sync when a person rolls it back: git
// would otherwise deploy the tip again at the next poll.
func (s *Service) holdForRollback(ctx context.Context, node, stack, commit, by string) {
	y, err := s.syncByStack(ctx, node, stack)
	if err != nil || y == nil || y.State == StateHeld {
		return
	}
	y.State = StateHeld
	y.StateReason = fmt.Sprintf("Held at %s after %s rolled %s back. Resume it to follow %s again.", short(commit), by, stack, y.Branch)
	if err := s.updateSync(context.WithoutCancel(ctx), y); err != nil {
		s.log.Error("stacks: hold a sync", "sync", y.ID, "err", err)
		return
	}
	s.syncItem(y, itemHeld, "info", stack+"'s git sync is held", y.StateReason)
	if s.syncer != nil {
		s.syncer.withdraw(ctx, y.ID, "The stack was rolled back by hand, so its git sync is held.")
	}
}

// ── the syncer ───────────────────────────────────────────────────────────

// SyncEngine is what the syncer needs of the action engine
// (*action.Engine).
type SyncEngine interface {
	Plan(ctx context.Context, a action.Actor, req action.PlanRequest) (*action.Plan, error)
	ApproveOrSubmit(ctx context.Context, a action.Actor, planID string, req action.ApproveRequest, idemKey string) (action.Task, *action.Approval, error)
	Withdraw(ctx context.Context, originatorID, reason string)
}

// SyncerOptions configure the syncer.
type SyncerOptions struct {
	Engine SyncEngine
	// Users reads a sync's owner (auth.Service.GetUser): a sync whose owner
	// is gone, disabled or no longer an admin pauses.
	Users func(ctx context.Context, id string) (auth.UserInfo, error)
	// Tick is how often the poll timer looks for syncs that are due.
	// Default 15 s.
	Tick time.Duration
	// Jitter returns a number in [0, 1) that spreads polls out. Default
	// math/rand.
	Jitter func() float64
}

// Syncer runs the syncs: one goroutine with a ticker, plus at most one run
// per sync at a time with one more queued behind it. It starts with the
// hub (Run) and stops with its context.
type Syncer struct {
	s      *Service
	eng    SyncEngine
	users  func(ctx context.Context, id string) (auth.UserInfo, error)
	tick   time.Duration
	jitter func() float64

	mu       sync.Mutex
	ctx      context.Context // nil until Run; triggers before then are dropped
	flights  map[string]*flight
	next     map[string]time.Time // when each sync polls next
	failures map[string]int       // failed polls in a row
	fetched  map[string]string    // the last tip each sync audited, so a refused tip isn't audited every poll
	wg       sync.WaitGroup
}

type flight struct{ queued bool }

// NewSyncer returns the syncer for s and attaches it, so the routes and
// webhooks can trigger it.
func NewSyncer(s *Service, o SyncerOptions) (*Syncer, error) {
	if o.Engine == nil || o.Users == nil {
		return nil, errors.New("stacks: the syncer needs Engine and Users")
	}
	y := &Syncer{s: s, eng: o.Engine, users: o.Users, tick: o.Tick, jitter: o.Jitter,
		flights: map[string]*flight{}, next: map[string]time.Time{}, failures: map[string]int{}, fetched: map[string]string{}}
	if y.tick <= 0 {
		y.tick = 15 * time.Second
	}
	if y.jitter == nil {
		y.jitter = rand.Float64
	}
	s.syncer = y
	return y, nil
}

// Run polls until ctx ends, then waits for running syncs to finish.
func (y *Syncer) Run(ctx context.Context) {
	y.mu.Lock()
	y.ctx = ctx
	y.mu.Unlock()
	t := time.NewTicker(y.tick)
	defer t.Stop()
	y.poll(ctx)
	for {
		select {
		case <-ctx.Done():
			y.mu.Lock()
			y.ctx = nil
			y.mu.Unlock()
			y.wg.Wait()
			return
		case <-t.C:
			y.poll(ctx)
		}
	}
}

// poll triggers every active sync that's due. A sync seen for the first
// time is due within its interval (at most a minute), spread by jitter, so
// a hub that restarts doesn't fetch everything at once.
func (y *Syncer) poll(ctx context.Context) {
	all, err := y.s.syncs(ctx)
	if err != nil {
		y.s.log.Error("stacks: list syncs", "err", err)
		return
	}
	now := y.s.now()
	y.mu.Lock()
	var due []string
	seen := map[string]bool{}
	for _, s := range all {
		seen[s.ID] = true
		if s.State != StateActive {
			continue
		}
		every := time.Duration(s.PollSeconds) * time.Second
		next, ok := y.next[s.ID]
		if !ok {
			y.next[s.ID] = now.Add(time.Duration(y.jitter() * float64(min(every, time.Minute))))
			continue
		}
		if !now.Before(next) {
			y.next[s.ID] = now.Add(every + time.Duration(y.jitter()*0.1*float64(every)))
			due = append(due, s.ID)
		}
	}
	for id := range y.next {
		if !seen[id] {
			delete(y.next, id)
			delete(y.failures, id)
			delete(y.fetched, id)
		}
	}
	y.mu.Unlock()
	for _, id := range due {
		y.Trigger(id)
	}
}

// Trigger runs a sync now, or queues one more run behind the one in flight.
// It reports false when the syncer isn't running.
func (y *Syncer) Trigger(id string) bool {
	if y == nil {
		return false
	}
	y.mu.Lock()
	defer y.mu.Unlock()
	if y.ctx == nil {
		return false
	}
	if f, ok := y.flights[id]; ok {
		f.queued = true
		return true
	}
	f := &flight{}
	y.flights[id] = f
	ctx := y.ctx
	y.wg.Go(func() {
		for {
			y.runOnce(ctx, id)
			y.mu.Lock()
			if !f.queued || ctx.Err() != nil {
				delete(y.flights, id)
				y.mu.Unlock()
				return
			}
			f.queued = false
			y.mu.Unlock()
		}
	})
	return true
}

// TriggerRemote triggers every active sync that follows a remote
// (normalized) and branch: what a webhook does. It returns how many.
func (y *Syncer) TriggerRemote(ctx context.Context, url, branch string) int {
	if y == nil {
		return 0
	}
	syncs, err := y.s.syncsFollowing(ctx, url, branch)
	if err != nil {
		y.s.log.Error("stacks: find syncs for a hook", "err", err)
		return 0
	}
	n := 0
	for _, s := range syncs {
		if s.State == StateActive && y.Trigger(s.ID) {
			n++
		}
	}
	return n
}

// Running reports whether a sync is checking or fetching now.
func (y *Syncer) Running(id string) bool {
	if y == nil {
		return false
	}
	y.mu.Lock()
	defer y.mu.Unlock()
	_, ok := y.flights[id]
	return ok
}

// forget drops what the syncer remembers of a sync (it was changed or
// removed), so the next poll treats it afresh.
func (y *Syncer) forget(id string) {
	if y == nil {
		return
	}
	y.mu.Lock()
	defer y.mu.Unlock()
	delete(y.next, id)
	delete(y.failures, id)
	delete(y.fetched, id)
}

func (y *Syncer) withdraw(ctx context.Context, id, reason string) {
	if y != nil {
		y.eng.Withdraw(context.WithoutCancel(ctx), id, reason)
	}
}

// ownerProblem says why a sync's owner can't act any more, or "".
func (y *Syncer) ownerProblem(ctx context.Context, s *Sync) (auth.UserInfo, string) {
	if s.OwnerID == "" {
		return auth.UserInfo{}, "The admin who set this sync up was deleted. Another admin can take it over."
	}
	u, err := y.users(ctx, s.OwnerID)
	switch {
	case errors.Is(err, auth.ErrUserNotFound):
		return u, "The admin who set this sync up was deleted. Another admin can take it over."
	case err != nil:
		return u, "" // a read that failed isn't a reason to pause; the engine refuses if it must
	case u.Disabled:
		return u, u.Username + ", who set this sync up, was disabled. Another admin can take it over."
	case u.Role != auth.RoleAdmin && u.Role != auth.RoleOwner:
		return u, u.Username + ", who set this sync up, is no longer an admin. Another admin can take it over."
	}
	return u, ""
}

// OwnerChangedAsync runs OwnerChanged in the background, tracked by the
// syncer's WaitGroup so Run waits for it at shutdown. It reports false
// when the syncer isn't running.
func (y *Syncer) OwnerChangedAsync(userID string) bool {
	if y == nil {
		return false
	}
	y.mu.Lock()
	defer y.mu.Unlock()
	if y.ctx == nil {
		return false
	}
	// The pause must land even when shutdown begins meanwhile.
	ctx := context.WithoutCancel(y.ctx)
	y.wg.Go(func() { y.OwnerChanged(ctx, userID) })
	return true
}

// OwnerChanged pauses the syncs a user owns when they can't act any more
// (auth's user.changed event: demoted or disabled).
func (y *Syncer) OwnerChanged(ctx context.Context, userID string) {
	if y == nil || userID == "" {
		return
	}
	all, err := y.s.syncs(ctx)
	if err != nil {
		y.s.log.Error("stacks: list syncs", "err", err)
		return
	}
	for _, s := range all {
		if s.OwnerID != userID || s.State == StatePaused {
			continue
		}
		if _, why := y.ownerProblem(ctx, s); why != "" {
			y.pause(ctx, s, why)
		}
	}
}

// pause pauses a sync and says why in the Inbox.
func (y *Syncer) pause(ctx context.Context, s *Sync, why string) {
	s, err := y.s.patchSync(ctx, s.ID, func(s *Sync) { s.State, s.StateReason = StatePaused, why })
	if err != nil || s == nil {
		return
	}
	y.withdraw(ctx, s.ID, "The git sync was paused: "+why)
	y.s.syncItem(s, itemPaused, "attention", s.Stack+"'s git sync is paused", why)
}

// actor is the sync as the engine sees it: kind sync, acting for its owner.
func syncActor(s *Sync, owner auth.UserInfo) action.Actor {
	return action.Actor{Kind: action.KindSync, ID: s.ID, Name: "git sync for " + owner.Username, Role: owner.Role, Via: "sync",
		User: owner.ID, UserName: owner.Username}
}

// failed records a check or fetch that didn't work: last_error, and after
// failingAfter in a row, a hint in the Inbox.
func (y *Syncer) failed(ctx context.Context, s *Sync, err error) {
	_, _, msg := statusOf(err)
	y.failedMsg(ctx, s, msg)
}

// failedMsg is failed with the sentence already made. The tip isn't marked
// seen, so the next poll tries it again.
func (y *Syncer) failedMsg(ctx context.Context, s *Sync, msg string) {
	y.mu.Lock()
	y.failures[s.ID]++
	n := y.failures[s.ID]
	y.mu.Unlock()
	s, perr := y.s.patchSync(ctx, s.ID, func(s *Sync) { s.LastError = msg })
	if perr != nil || s == nil {
		return
	}
	y.s.log.Warn("stacks: a git sync's check failed", "sync", s.ID, "host", remoteHost(s.URL), "failures", n)
	if n > failingAfter {
		y.s.syncItem(s, itemFailing, "info", s.Stack+" can't reach its git remote",
			fmt.Sprintf("The last %d checks of %s on %s failed: %s", n, s.Branch, remoteHost(s.URL), msg))
	}
}

// runOnce is one sync: check the tip, fetch it if it's new, gate it, save it
// as the draft, and plan the deploy.
func (y *Syncer) runOnce(ctx context.Context, id string) {
	s, err := y.s.syncByID(ctx, id)
	if err != nil {
		y.s.log.Error("stacks: read sync", "sync", id, "err", err)
		return
	}
	if s == nil || s.State != StateActive {
		return
	}
	owner, why := y.ownerProblem(ctx, s)
	if why != "" {
		y.pause(ctx, s, why)
		return
	}
	ra, err := y.s.remoteAuth(ctx, s)
	if err != nil {
		y.s.log.Error("stacks: read a sync's credential", "sync", id, "err", err)
		return
	}
	defer func() { ra.Secret = "" }()
	var chk stackproto.RemoteCheckResponse
	err = y.s.call(ctx, s.Node, stackproto.VerbRemoteCheck, stackproto.RemoteCheckRequest{URL: s.URL, Branch: s.Branch, Auth: ra}, &chk, fetchTimeout, nil)
	if err == nil && s.AuthKind == stackproto.AuthSSH && chk.HostKey != "" && chk.HostKey != s.HostKey {
		err = stackproto.Errorf(stackproto.CodeInvalid, "The remote's SSH host key isn't the one pinned. If the server's key really changed, test the remote again and pin the new key.")
	}
	if err != nil {
		y.failed(ctx, s, err)
		return
	}
	y.mu.Lock()
	hadFailures := y.failures[id] > 0
	delete(y.failures, id)
	y.mu.Unlock()
	y.s.clearSyncItem(s, itemFailing)
	if hadFailures {
		if s, err = y.s.patchSync(ctx, id, func(s *Sync) { s.LastError = "" }); err != nil || s == nil {
			return
		}
	}
	if chk.SHA == "" || chk.SHA == s.LastSeen {
		return
	}
	if s.isRejected(chk.SHA) {
		_, _ = y.s.patchSync(ctx, id, func(s *Sync) { s.LastSeen = chk.SHA })
		return
	}
	var got stackproto.FetchResponse
	err = y.s.call(ctx, s.Node, stackproto.VerbFetch, stackproto.FetchRequest{URL: s.URL, Branch: s.Branch, Path: s.Path, Auth: ra, HostKey: s.HostKey}, &got, fetchTimeout, nil)
	ra.Secret = ""
	if err != nil {
		y.failed(ctx, s, err)
		return
	}
	y.auditFetch(ctx, s, owner, got)
	y.process(ctx, s, owner, got)
}

// auditFetch records stack.sync.fetch once per new tip.
func (y *Syncer) auditFetch(ctx context.Context, s *Sync, owner auth.UserInfo, got stackproto.FetchResponse) {
	y.mu.Lock()
	seen := y.fetched[s.ID] == got.SHA
	y.fetched[s.ID] = got.SHA
	y.mu.Unlock()
	if seen || y.s.Audit == nil || y.s.Audit.Log == nil {
		return
	}
	params, _ := json.Marshal(map[string]any{"sync": s.ID, "sha": got.SHA, "branch": s.Branch, "host": remoteHost(s.URL)})
	a := syncActor(s, owner)
	if _, err := y.s.Audit.Log.Append(context.WithoutCancel(ctx), audit.Entry{
		Actor:  audit.Actor{Kind: a.Kind, ID: a.ID, Name: a.Name, Via: a.Via, OnBehalfOf: a.User},
		Action: "stack.sync.fetch", Target: Target(s.Node, s.Stack), Params: params, Result: audit.OK,
	}); err != nil {
		y.s.log.Error("stacks: audit a fetch", "sync", s.ID, "err", err)
	}
}

// process gates a fetched tip and hands it to the engine.
func (y *Syncer) process(ctx context.Context, s *Sync, owner auth.UserInfo, got stackproto.FetchResponse) {
	sha := got.SHA
	seen := func(lastError string) {
		_, _ = y.s.patchSync(ctx, s.ID, func(s *Sync) { s.LastSeen, s.LastError = sha, lastError })
	}
	waiting := func(msg string) {
		seen(msg)
		y.s.syncItem(s, itemWaiting, "attention", "Commit "+short(sha)+" of "+s.Stack+" can't be deployed", msg)
	}
	// refused handles the engine's refusal. Something else running on the
	// stack (locked) or a node out of reach (unavailable) passes: the tip
	// isn't marked seen, so the next poll tries it again.
	refused := func(ref *action.Refusal) {
		if ref.Code == "locked" || ref.Code == "unavailable" {
			y.failedMsg(ctx, s, ref.Message)
			return
		}
		waiting(ref.Message)
	}
	// The commit that ran (a check can race a push): a rejected one stops
	// here too.
	if s.isRejected(sha) {
		_, _ = y.s.patchSync(ctx, s.ID, func(s *Sync) { s.LastSeen = sha })
		return
	}
	// The gate: the stack as the node has it.
	var st stackproto.Stack
	err := y.s.call(ctx, s.Node, stackproto.VerbGet, stackproto.GetRequest{Stack: s.Stack}, &st, y.s.timeout, nil)
	exists := err == nil
	if e := stackproto.ErrorFrom(err); err != nil && (e == nil || e.Code != stackproto.CodeNotFound) {
		y.failed(ctx, s, err)
		return
	}
	switch {
	case st.Protected:
		y.pause(ctx, s, "Cha Dash's own stack, and any stack the node protects, can't follow git.")
		return
	case st.Drift:
		// Not seen: once the edits are accepted or undone, the next poll
		// deploys this commit.
		msg := fmt.Sprintf("%s was edited on disk outside Cha Dash, so commit %s waits. Accept the edits (Adopt) or put the file back.", s.Stack, short(sha))
		_, _ = y.s.patchSync(ctx, s.ID, func(s *Sync) { s.LastError = msg })
		y.s.syncItem(s, itemWaiting, "attention", "Commit "+short(sha)+" of "+s.Stack+" waits: the file was edited on disk", msg)
		return
	case exists && st.Managed && st.Content == got.Content:
		// The plan would be unchanged: only record the SHA as seen.
		if d, _ := y.s.draft(ctx, s.Node, s.Stack); d != nil && strings.HasPrefix(d.Source, SourceGit) {
			_ = y.s.deleteDraft(context.WithoutCancel(ctx), s.Node, s.Stack, d.Revision)
		}
		_, _ = y.s.patchSync(ctx, s.ID, func(s *Sync) { s.LastSeen, s.LastDeployed, s.LastError = sha, sha, "" })
		y.s.clearSyncItem(s, itemWaiting, itemBlocked, itemRejected)
		return
	}
	// The tip becomes the draft. Edits from the web are refused for a
	// synced stack, so whatever draft there was came from git too.
	cur, err := y.s.draft(ctx, s.Node, s.Stack)
	if err != nil {
		y.s.log.Error("stacks: read draft", "sync", s.ID, "err", err)
		return
	}
	var from int64
	if cur != nil {
		from = cur.Revision
	}
	if _, err := y.s.saveDraft(ctx, s.Node, s.Stack, got.Content, "git sync", SourceGit+sha, from); err != nil {
		y.s.log.Error("stacks: save a sync's draft", "sync", s.ID, "err", err)
		return
	}
	// A newer tip withdraws an older pending approval.
	y.withdraw(ctx, s.ID, fmt.Sprintf("A newer commit, %s, replaced it.", short(sha)))
	ask := s.Mode != ModeDeploy
	// Deploy on push still asks a person first, and says why, when the file
	// references a stack secret outside the allowlist, or does something
	// that amounts to root on the node's host (the node's plan says which;
	// stacks.deny may allow them). A plan that can't be read asks too.
	var why []string
	var outside []string
	for _, n := range secretRefs(got.Content) {
		if !slices.Contains(s.SecretsAllow, n) {
			outside = append(outside, n)
		}
	}
	if len(outside) > 0 {
		why = append(why, "it references stack secrets outside the sync's allowlist: "+strings.Join(outside, ", ")+".")
	}
	if !ask {
		switch p, _ := y.s.planDraft(ctx, s.Node, s.Stack, got.Content); {
		case p != nil && len(p.RootEquivalent) > 0:
			why = append(why, sentence("it amounts to root on "+y.s.nodeName(ctx, s.Node)+": "+strings.Join(p.RootEquivalent, "; ")))
		case p == nil:
			why = append(why, "its plan couldn't be read.")
		}
	}
	actor := syncActor(s, owner)
	plan, err := y.eng.Plan(ctx, actor, action.PlanRequest{Action: ActionDeploy, Target: Target(s.Node, s.Stack), RequireApproval: ask || len(why) > 0,
		Note: commitNote(sha, got.Subject)})
	if ref, ok := errors.AsType[*action.Refusal](err); ok {
		if ref.Code == "forbidden" {
			y.pause(ctx, s, "The sync's owner can't deploy this stack any more: "+ref.Message)
			return
		}
		refused(ref)
		return
	}
	if err != nil {
		y.s.log.Error("stacks: plan a sync's deploy", "sync", s.ID, "err", err)
		return
	}
	_, apv, err := y.eng.ApproveOrSubmit(ctx, actor, plan.ID, action.ApproveRequest{Hash: plan.Hash}, "")
	if ref, ok := errors.AsType[*action.Refusal](err); ok {
		refused(ref)
		return
	}
	if err != nil {
		y.s.log.Error("stacks: start a sync's deploy", "sync", s.ID, "err", err)
		return
	}
	seen("")
	y.s.clearSyncItem(s, itemWaiting)
	if apv != nil && !ask && len(why) > 0 {
		y.s.syncItem(s, itemAsks, "attention", "Commit "+short(sha)+" of "+s.Stack+" waits for approval",
			"Deploy on push asks first for commit "+short(sha)+": "+strings.Join(why, " ")+" Approve or deny it in the Inbox.")
	}
}

// sentence ends s with a full stop, so another sentence can follow it.
func sentence(s string) string {
	s = strings.TrimSpace(s)
	if s != "" && !strings.HasSuffix(s, ".") {
		s += "."
	}
	return s
}

// maxSubject caps a commit's subject in an approval's note, in runes.
const maxSubject = 120

// commitNote is the line an approval shows for a commit: its short SHA and
// subject, which whoever can push writes, so it's cut short.
func commitNote(sha, subject string) string {
	subject = strings.Join(strings.Fields(subject), " ")
	if r := []rune(subject); len(r) > maxSubject {
		subject = string(r[:maxSubject-1]) + "…"
	}
	if subject == "" {
		return "Commit " + short(sha)
	}
	return "Commit " + short(sha) + ": " + subject
}
