// Package authz decides who may do what. It is the only place permission
// decisions are made (docs/plan/08-v0.2.md → Shared contracts):
//
//   - routes.go: every HTTP route with the role it needs, enforced by Guard in
//     front of the server's mux. A route without a row is refused.
//   - this file: actions. Decide (and Policy.Authorize, the action engine's
//     seam) checks an actor's role against an action's tier and scope, and
//     asks for a step-up where the tier needs one.
//   - users.go: the users API, whose rules (an admin manages everyone but the
//     owner; handing over ownership is T3) are decisions too.
//   - tokens.go: the API tokens API, and which scopes a role may hand a token.
//
// API tokens act as their user, never above the user's current role, and
// narrowed by their scopes (read scopes such as graph.read, and action IDs
// or patterns). They can't step up, so T3 and T4 are out of reach; their
// plans from T2 up wait for a person (NeedsApproval), who approves them in a
// session (DecideApproval).
//
// A git sync (KindSync) acts for its owner the same way: the owner's current
// role, refused once the owner is disabled or gone, and no step-up, so T3
// and T4 are out of reach. Its stack.* plans need the owner to be an admin
// still (the settings rule). Its plans wait for a person only when the
// syncer asks (action.Plan.RequireApproval), never by kind.
//
// Roles and their tier caps:
//
//	owner      T0–T4; T4 only for scopes enabled in the actions.t4 setting
//	admin      T0–T3
//	operator   T0–T2
//	viewer     T0
//	household  only actions marked household: true, up to T1
//
// An action that reads data back (Request.ReadsData: a container's logs)
// needs Operator or above whatever its tier, since what it reads can hold
// secrets.
//
// T3 needs a step-up grant on the session (auth.StepUpTTL, five minutes). T4
// needs a fresh one, granted within FreshStepUp, and the engine clears it
// after use (auth.Service.ClearStepUp), so T4 always asks again.
//
// Scopes are action IDs, optionally qualified by resource:
// "proxmox.guest.reboot:host=pve-a". A grant or an actions.t4 entry covers a
// request when the action matches ("proxmox.guest.*" and "*" work too) and
// each of its qualifiers is on the request; the "target" qualifier matches
// the request's target. The UI doesn't offer qualified scopes yet; the
// engine supports them from day one.
package authz

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"dash.cha.sh/internal/auth"
	"dash.cha.sh/internal/store"
)

// ── The seam with internal/action ────────────────────────────────────────────
//
// internal/action defines Actor, Tier, Policy and the two errors, and this
// package implements Policy (08-v0.2 → "Go seam"). Until both tracks merge,
// these are local copies with the agreed shapes. At the merge, replace them
// with aliases (type Actor = action.Actor, and so on) or move them here and
// have action alias them; nothing else changes.

// Tier is an action's risk tier, "T0" to "T4".
type Tier string

// The tiers.
const (
	T0 Tier = "T0" // read-only
	T1 Tier = "T1" // reversible: undo
	T2 Tier = "T2" // disruptive: hold to confirm, blast radius
	T3 Tier = "T3" // destructive: typed name and step-up
	T4 Tier = "T4" // catastrophic: off unless enabled per scope, fresh step-up
)

func (t Tier) rank() int {
	switch t {
	case T0:
		return 0
	case T1:
		return 1
	case T2:
		return 2
	case T3:
		return 3
	case T4:
		return 4
	}
	return -1
}

// Actor is who is asking, as the engine and the audit log record them.
type Actor struct {
	Kind, ID, Name, Role, Via, Session, IP string
	StepUpUntil                            time.Time
	// User and UserName are the user an API token acts for (kind token), or
	// a sync's owner (kind sync).
	User, UserName string
}

// Actor kinds.
const (
	KindUser  = "user"
	KindToken = "token" // an API token (wave 10); it can't step up
	KindAgent = "agent" // an agent's token (ADR-0017, later); every plan waits for a person
	// KindSync is a git sync (v0.3): ID is the sync's, User its owner. It
	// acts with the owner's current role and can't step up.
	KindSync = "sync"
)

// Errors. A refusal unwraps to one of these and reads as a sentence the web
// can show as-is.
var (
	ErrForbidden      = errors.New("forbidden")
	ErrStepUpRequired = errors.New("step-up required")
	// ErrSessionOnly: an API token called a route only a signed-in person
	// may (TokenRoute).
	ErrSessionOnly = errors.New("session only")
)

// Refusal is a decision against the actor.
type Refusal struct {
	Err error  // ErrForbidden or ErrStepUpRequired
	Msg string // a sentence for the person
}

func (r *Refusal) Error() string { return r.Msg }
func (r *Refusal) Unwrap() error { return r.Err }

func forbid(format string, args ...any) error {
	return &Refusal{Err: ErrForbidden, Msg: fmt.Sprintf(format, args...)}
}

// ── Roles ────────────────────────────────────────────────────────────────────

// rank orders the roles for route access: household < viewer < operator <
// admin < owner. -1 for anything else, which nothing admits.
func rank(role string) int {
	switch role {
	case auth.RoleHousehold:
		return 0
	case auth.RoleViewer:
		return 1
	case auth.RoleOperator:
		return 2
	case auth.RoleAdmin:
		return 3
	case auth.RoleOwner:
		return 4
	}
	return -1
}

// tierCap is the highest tier a role may run (household: see Decide).
func tierCap(role string) Tier {
	switch role {
	case auth.RoleOwner:
		return T4
	case auth.RoleAdmin:
		return T3
	case auth.RoleOperator:
		return T2
	case auth.RoleViewer, auth.RoleHousehold:
		return T0
	}
	return ""
}

// SeesInboxDetail reports whether role sees the Inbox in full: every
// incident with its facts, causes, blast radius and actions, on the inbox
// topic and in the first-paint snapshot. Household doesn't ("Boards only,
// no Inbox detail"): it gets topics.ProjectIncident's projection, limited to
// incidents about what its boards show. An unknown role sees nothing.
func SeesInboxDetail(role string) bool { return rank(role) >= rank(auth.RoleViewer) }

// SeesWholeGraph reports whether role sees the whole Home Graph, on the
// graph topic, GET /api/graph and the first paint. Household doesn't: it
// gets topics.HouseholdGraph (no chadash-node resources, no addresses or
// key fingerprints), which still renders every board.
func SeesWholeGraph(role string) bool { return rank(role) >= rank(auth.RoleViewer) }

// SeesApprovals reports whether role sees plans waiting for a person (an
// API token's T2 plans) in the Inbox: the approvers, admin and owner.
func SeesApprovals(role string) bool { return rank(role) >= rank(auth.RoleAdmin) }

// ChangeSecuritySettings decides whether a may change the security settings
// (the posture and the step-up rule) other than through Settings → Security:
// importing a Home file that carries them. The same rule as PUT
// /api/settings/security: the owner only, and changing the step-up rule
// (stepUp) is T3, so it needs a step-up grant.
func ChangeSecuritySettings(a Actor, stepUp bool, now time.Time) error {
	if a.Role != auth.RoleOwner {
		return forbid("Only the owner can change the security settings, so this file's security settings weren't imported.")
	}
	if !stepUp {
		return nil
	}
	return Decide(a, Request{Scope: "settings.security.update", Tier: T3}, Env{Now: now})
}

// HouseholdCap is the highest tier an action marked household: true may have
// and still be open to the household role.
const HouseholdCap = T1

// FreshStepUp is how recent a step-up must be for T4: granted within the
// last minute.
const FreshStepUp = time.Minute

// ── Scopes ───────────────────────────────────────────────────────────────────

// Read scopes: what an API token may read, beside the action scopes (action
// IDs and patterns) that say what it may plan. A token's user must be
// allowed the read too; audit.read and tasks.all are Admin and Owner only.
// "*" grants every read and every action the user's role allows.
const (
	// ScopeGraphRead: the Home Graph and its metrics history, boards and
	// their layouts, correlation links and suggestions, the stacks list, the
	// demo clock.
	ScopeGraphRead = "graph.read"
	// ScopeInboxRead: the Inbox, as the user's role sees it.
	ScopeInboxRead = "inbox.read"
	// ScopeAuditRead: the audit log, its export and its verification.
	ScopeAuditRead = "audit.read"
	// ScopeTasksAll: everyone's tasks, not only the token's own.
	ScopeTasksAll = "tasks.all"
	// ScopeApprovals is the engine's scope for deciding a plan that waits
	// for a person (action.ScopeApprovals); never a token's.
	ScopeApprovals = "approvals.decide"
)

// ReadScope describes a read scope for Settings → API tokens.
type ReadScope struct {
	Scope       string `json:"scope"`
	Label       string `json:"label"`
	Description string `json:"description"`
	// MinRole is the least role whose tokens may hold it.
	MinRole string `json:"min_role"`
}

// ReadScopes is the read vocabulary, in the order the page shows it.
var ReadScopes = []ReadScope{
	{ScopeGraphRead, "Read the Home Graph", "Resources and relations, metrics history, boards and their layouts, correlation links, the stacks list.", auth.RoleHousehold},
	{ScopeInboxRead, "Read the Inbox", "Open and recent incidents, repairs and suggestions, as your role sees them.", auth.RoleHousehold},
	{ScopeAuditRead, "Read the audit log", "Every recorded change, its export and the chain check.", auth.RoleAdmin},
	{ScopeTasksAll, "See everyone's tasks", "Tasks other people and tokens started, not only this token's own.", auth.RoleAdmin},
}

// ValidTokenScope checks a scope a token of a user with role may hold:
// a read scope the role may use, an action ID or pattern, optionally
// qualified, or "*". It returns a sentence for the person when it can't.
func ValidTokenScope(scope, role string) error {
	sc, err := ParseScope(scope)
	if err != nil {
		return forbid("%q isn't a scope. Use an action ID such as docker.container.restart, a pattern such as proxmox.guest.*, or a read scope.", scope)
	}
	for _, r := range ReadScopes {
		if sc.Action == r.Scope && rank(role) < rank(r.MinRole) {
			return forbid("Your role (%s) can't use %s, so a token of yours can't either.", role, r.Scope)
		}
	}
	if sc.Action == ScopeApprovals {
		return forbid("Tokens can't approve plans: a person approves them, in a session.")
	}
	return nil
}

// Scope is a parsed scope: an action ID (or pattern) and its qualifiers.
type Scope struct {
	Action string
	Quals  map[string]string
}

var (
	actionRE = regexp.MustCompile(`^(\*|[a-z0-9][a-z0-9_-]*(\.[a-z0-9][a-z0-9_-]*)*(\.\*)?)$`)
	qualKey  = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
)

// ParseScope parses "action" or "action:key=value,key=value".
func ParseScope(s string) (Scope, error) {
	action, quals, hasQuals := strings.Cut(s, ":")
	if !actionRE.MatchString(action) {
		return Scope{}, fmt.Errorf("authz: scope %q: bad action", s)
	}
	sc := Scope{Action: action}
	if !hasQuals {
		return sc, nil
	}
	sc.Quals = map[string]string{}
	for q := range strings.SplitSeq(quals, ",") {
		k, v, ok := strings.Cut(q, "=")
		if !ok || !qualKey.MatchString(k) || v == "" || strings.ContainsAny(v, "=,") {
			return Scope{}, fmt.Errorf("authz: scope %q: bad qualifier %q", s, q)
		}
		if _, dup := sc.Quals[k]; dup {
			return Scope{}, fmt.Errorf("authz: scope %q: %s twice", s, k)
		}
		sc.Quals[k] = v
	}
	return sc, nil
}

// Covers reports whether grant g allows the requested scope on target. A
// request's own action can't be a pattern.
func (g Scope) Covers(req Scope, target string) bool {
	if strings.Contains(req.Action, "*") {
		return false
	}
	switch {
	case g.Action == "*":
	case strings.HasSuffix(g.Action, ".*"):
		if !strings.HasPrefix(req.Action, strings.TrimSuffix(g.Action, "*")) {
			return false
		}
	case g.Action != req.Action:
		return false
	}
	for k, v := range g.Quals {
		got, ok := req.Quals[k]
		if k == "target" && !ok {
			got, ok = target, target != ""
		}
		if !ok || got != v {
			return false
		}
	}
	return true
}

// covered reports whether any of grants covers req; unparsable grants cover nothing.
func covered(grants []string, req Scope, target string) bool {
	for _, g := range grants {
		if gs, err := ParseScope(g); err == nil && gs.Covers(req, target) {
			return true
		}
	}
	return false
}

// ── Decisions ────────────────────────────────────────────────────────────────

// Request is one action an actor wants to run.
type Request struct {
	Scope  string // the action ID, optionally qualified
	Target string // the resource, e.g. "proxmox:i_x/qemu/201"
	Tier   Tier
	// Household: the action is marked household: true, so the household
	// role may run it (up to HouseholdCap). Household runs nothing else, T0
	// included.
	Household bool
	// ReadsData: the action reads data back (a container's logs), which can
	// hold secrets, so it needs Operator or above whatever its tier, unless
	// it's also marked Household.
	ReadsData bool
}

// Env is what a decision depends on beyond the actor and the request.
type Env struct {
	Now time.Time
	// T4 lists the scopes T4 is enabled for (the actions.t4 setting).
	T4 []string
	// Grants narrows the actor to these scopes (an API token's). Nil: no
	// narrowing, the role alone decides; empty: nothing is granted.
	Grants []string
}

// Decide returns nil when a may run req, or a *Refusal unwrapping to
// ErrForbidden or ErrStepUpRequired. Refusals that no step-up could change
// come first, so nobody is asked to confirm something they can't do anyway.
func Decide(a Actor, req Request, env Env) error {
	tr := req.Tier.rank()
	if tr < 0 {
		return forbid("%q isn't a tier.", req.Tier)
	}
	sc, err := ParseScope(req.Scope)
	if err != nil || strings.Contains(sc.Action, "*") {
		return forbid("%q isn't an action.", req.Scope)
	}
	if rank(a.Role) < 0 {
		return forbid("Your account has no role that allows this.")
	}
	if settingsAction(sc.Action) && rank(a.Role) < rank(auth.RoleAdmin) {
		return forbid("Only an admin or the owner can change stacks: they're settings, whatever the tier.")
	}
	if a.Role == auth.RoleHousehold {
		// Only actions meant for the household, at every tier: a T0 can
		// still read what household shouldn't see.
		if !req.Household || tr > HouseholdCap.rank() {
			return forbid("Household accounts can only run actions meant for the household.")
		}
	} else if tr > tierCap(a.Role).rank() {
		return forbid("Your role (%s) can't run %s actions.", a.Role, req.Tier)
	}
	if req.ReadsData && !req.Household && rank(a.Role) < rank(auth.RoleOperator) {
		return forbid("Only operators, admins and the owner can run %s: what it reads back can hold secrets.", sc.Action)
	}
	if env.Grants != nil && !covered(env.Grants, sc, req.Target) {
		return forbid("This token isn't allowed to run %s.", sc.Action)
	}
	if tr >= T3.rank() && a.Kind == KindToken {
		return forbid("API tokens can't run %s actions; they need a person to confirm.", req.Tier)
	}
	if tr >= T3.rank() && a.Kind == KindSync {
		return forbid("A git sync can't run %s actions; they need a person to confirm.", req.Tier)
	}
	if tr >= T4.rank() && a.Kind == KindAgent {
		return forbid("Agents can never propose %s actions.", req.Tier)
	}
	switch req.Tier {
	case T4:
		if !covered(env.T4, sc, req.Target) {
			return forbid("T4 actions are off for %s. The owner can enable them in Settings.", sc.Action)
		}
		if a.StepUpUntil.Sub(env.Now) <= auth.StepUpTTL-FreshStepUp {
			return &Refusal{Err: ErrStepUpRequired, Msg: "Confirm it's you again: this action always asks."}
		}
	case T3:
		if !a.StepUpUntil.After(env.Now) {
			return &Refusal{Err: ErrStepUpRequired, Msg: "Confirm it's you first."}
		}
	}
	return nil
}

// NeedsApproval reports whether a plan an actor of kind makes at tier
// waits for a person instead of running when its maker approves it: an API
// token's plans from T2 up ("API-originated actions above T1 always require
// human approval", 02 · Experience), and every plan of an agent's (ADR-0017).
// A sync's never do by kind: the syncer sets action.Plan.RequireApproval for
// "draft and ask", and the engine makes those wait.
func NeedsApproval(kind string, tier Tier) bool {
	switch kind {
	case KindToken:
		return tier.rank() >= T2.rank()
	case KindAgent:
		return true
	}
	return false
}

// DecideApproval decides whether a may approve or deny a plan that waits for
// a person, at the plan's tier (T0 to deny): only a person signed in to a
// session, never a token; an admin or the owner; and from T2 up, with a
// step-up grant (passkey, or the password where the step-up rule allows it).
// The engine also wants that grant to be newer than the request, so it's
// bound to this approval; and it asks Decide whether a may run the action
// itself.
func DecideApproval(a Actor, tier Tier, now time.Time) error {
	switch {
	case a.Kind != KindUser || a.Session == "":
		return forbid("Only a person signed in to Cha Dash can approve or deny this, never a token.")
	case rank(a.Role) < rank(auth.RoleAdmin):
		return forbid("Only an admin or the owner can approve or deny plans that wait for a person.")
	case tier.rank() >= T2.rank() && !a.StepUpUntil.After(now):
		return &Refusal{Err: ErrStepUpRequired, Msg: "Confirm it's you first: approving this needs a fresh check."}
	}
	return nil
}

// ── Policy ───────────────────────────────────────────────────────────────────

// T4SettingsKey is the settings row listing the scopes T4 is enabled for, as
// a JSON array of scopes. Missing means none: T4 is off by default.
const T4SettingsKey = "actions.t4"

// Policy implements the action engine's Policy: Decide with the hub's
// settings.
type Policy struct {
	st     *store.Store
	log    *slog.Logger
	now    func() time.Time
	grants func(context.Context, Actor) ([]string, error)
	tokens func(context.Context, string) (auth.TokenAccess, error)
	users  func(context.Context, string) (auth.UserInfo, error)
}

// PolicyOption adjusts NewPolicy.
type PolicyOption func(*Policy)

// WithGrants narrows actors to scopes: f returns an actor's grants, or nil
// for no narrowing (wave 10's API tokens).
func WithGrants(f func(context.Context, Actor) ([]string, error)) PolicyOption {
	return func(p *Policy) { p.grants = f }
}

// WithTokens resolves API token actors (kind token): f returns what the
// token may do now (auth.Service.TokenAccess). The token's current scopes
// narrow the actor, its user's current role replaces the one recorded, and
// a revoked or expired token is refused. Without it every token is refused.
func WithTokens(f func(ctx context.Context, id string) (auth.TokenAccess, error)) PolicyOption {
	return func(p *Policy) { p.tokens = f }
}

// WithUsers resolves sync actors (kind sync) to their owner: f returns the
// user (auth.Service.GetUser), or auth.ErrUserNotFound. The owner's current
// role replaces the one recorded, and a disabled or missing owner is
// refused. Without it every sync is refused.
func WithUsers(f func(ctx context.Context, id string) (auth.UserInfo, error)) PolicyOption {
	return func(p *Policy) { p.users = f }
}

// WithClock replaces time.Now, for tests.
func WithClock(now func() time.Time) PolicyOption { return func(p *Policy) { p.now = now } }

// NewPolicy returns the policy over st's settings.
func NewPolicy(st *store.Store, log *slog.Logger, opts ...PolicyOption) *Policy {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	p := &Policy{st: st, log: log, now: time.Now}
	for _, o := range opts {
		o(p)
	}
	return p
}

type householdKey struct{}

// WithHouseholdAction marks ctx as carrying an action declared household: true,
// for Authorize, whose signature has no room for it. Decide takes it directly.
func WithHouseholdAction(ctx context.Context) context.Context {
	return context.WithValue(ctx, householdKey{}, true)
}

// Authorize is the engine's seam: nil, or a refusal unwrapping to ErrForbidden
// or ErrStepUpRequired.
func (p *Policy) Authorize(ctx context.Context, a Actor, scope, target string, tier Tier) error {
	household, _ := ctx.Value(householdKey{}).(bool)
	return p.Decide(ctx, a, Request{Scope: scope, Target: target, Tier: tier, Household: household})
}

// Decide is Decide with the hub's settings. A setting it can't read refuses
// rather than allows.
func (p *Policy) Decide(ctx context.Context, a Actor, req Request) error {
	env := Env{Now: p.now()}
	switch a.Kind {
	case KindToken:
		var err error
		if a, env.Grants, err = p.token(ctx, a); err != nil {
			return err
		}
	case KindSync:
		var err error
		if a, err = p.syncOwner(ctx, a); err != nil {
			return err
		}
	}
	if req.Tier == T4 {
		t4, err := p.T4Enabled(ctx)
		if err != nil {
			p.log.Error("authz: reading the T4 settings failed; refusing", "err", err)
			return forbid("T4 actions are unavailable: the hub couldn't read their settings.")
		}
		env.T4 = t4
	}
	if p.grants != nil && a.Kind != KindToken {
		g, err := p.grants(ctx, a)
		if err != nil {
			p.log.Error("authz: reading the actor's grants failed; refusing", "err", err, "actor", a.ID)
			return forbid("The hub couldn't check what you're allowed to do.")
		}
		env.Grants = g
	}
	return Decide(a, req, env)
}

// token resolves a token actor: its user's current role and the token's
// scopes, or a refusal when it's revoked, expired or can't be read.
func (p *Policy) token(ctx context.Context, a Actor) (Actor, []string, error) {
	if p.tokens == nil {
		if p.grants == nil {
			return a, nil, forbid("API tokens aren't accepted here.")
		}
		// Grants alone (tests, or a caller that resolves tokens itself).
		g, err := p.grants(ctx, a)
		if err != nil {
			p.log.Error("authz: reading the actor's grants failed; refusing", "err", err, "actor", a.ID)
			return a, nil, forbid("The hub couldn't check what you're allowed to do.")
		}
		if g == nil {
			g = []string{}
		}
		return a, g, nil
	}
	acc, err := p.tokens(ctx, a.ID)
	if err != nil {
		p.log.Error("authz: reading an API token failed; refusing", "err", err, "token", a.ID)
		return a, nil, forbid("The hub couldn't check what this token is allowed to do.")
	}
	if !acc.OK {
		return a, nil, forbid("This API token has been revoked or has expired.")
	}
	a.Role, a.StepUpUntil = acc.Role, time.Time{}
	return a, acc.Scopes, nil
}

// syncOwner resolves a sync actor to its owner's current role, or a refusal
// when the owner is disabled, gone or can't be read. A sync never has a
// step-up grant.
func (p *Policy) syncOwner(ctx context.Context, a Actor) (Actor, error) {
	if p.users == nil || a.User == "" {
		return a, forbid("Git syncs aren't accepted here.")
	}
	u, err := p.users(ctx, a.User)
	switch {
	case errors.Is(err, auth.ErrUserNotFound):
		return a, forbid("The user this sync acts for no longer exists.")
	case err != nil:
		p.log.Error("authz: reading a sync's owner failed; refusing", "err", err, "sync", a.ID)
		return a, forbid("The hub couldn't check what this sync is allowed to do.")
	case u.Disabled:
		return a, forbid("The user this sync acts for has been disabled.")
	}
	a.Role, a.UserName, a.StepUpUntil = u.Role, u.Username, time.Time{}
	return a, nil
}

// ReadScope decides a read that isn't an action (audit.read, tasks.all):
// Admin and Owner, and for a token, one whose scopes include it. A sync
// reads only its own tasks, so never these.
func (p *Policy) ReadScope(ctx context.Context, a Actor, scope string) error {
	if a.Kind == KindSync {
		return forbid("A git sync can't read %s.", scope)
	}
	var grants []string
	if a.Kind == KindToken {
		var err error
		if a, grants, err = p.token(ctx, a); err != nil {
			return err
		}
	}
	if rank(a.Role) < rank(auth.RoleAdmin) {
		return forbid("Only admins and the owner can see this.")
	}
	if a.Kind == KindToken {
		sc, _ := ParseScope(scope)
		if !covered(grants, sc, "") {
			return forbid("This token doesn't have the %s scope.", scope)
		}
	}
	return nil
}

// T4Enabled returns the scopes T4 is enabled for.
func (p *Policy) T4Enabled(ctx context.Context) ([]string, error) {
	var raw string
	err := p.st.Reader().QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, T4SettingsKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var scopes []string
	if err := json.Unmarshal([]byte(raw), &scopes); err != nil {
		return nil, fmt.Errorf("authz: %s: %w", T4SettingsKey, err)
	}
	return scopes, nil
}

// settingsAction reports whether an action changes settings rather than
// operating the lab: owned stacks (stack.deploy, stack.rollback, stack.adopt,
// stack.unadopt) are Settings in the Roles table, so admin+ whatever their
// tier, although an operator may run other T2 actions.
func settingsAction(id string) bool { return strings.HasPrefix(id, "stack.") }
