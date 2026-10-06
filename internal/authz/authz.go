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
//
// Roles and their tier caps:
//
//	owner      T0–T4; T4 only for scopes enabled in the actions.t4 setting
//	admin      T0–T3
//	operator   T0–T2
//	viewer     T0
//	household  T0, plus actions marked household: true up to T1
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
}

// Actor kinds.
const (
	KindUser  = "user"
	KindToken = "token" // an API token (wave 10); it can't step up
)

// Errors. A refusal unwraps to one of these and reads as a sentence the web
// can show as-is.
var (
	ErrForbidden      = errors.New("forbidden")
	ErrStepUpRequired = errors.New("step-up required")
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

// HouseholdCap is the highest tier an action marked household: true may have
// and still be open to the household role.
const HouseholdCap = T1

// FreshStepUp is how recent a step-up must be for T4: granted within the
// last minute.
const FreshStepUp = time.Minute

// ── Scopes ───────────────────────────────────────────────────────────────────

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
	// role may run it (up to HouseholdCap).
	Household bool
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
		if tr > 0 && !(req.Household && tr <= HouseholdCap.rank()) {
			return forbid("Household accounts can only run actions meant for the household.")
		}
	} else if tr > tierCap(a.Role).rank() {
		return forbid("Your role (%s) can't run %s actions.", a.Role, req.Tier)
	}
	if env.Grants != nil && !covered(env.Grants, sc, req.Target) {
		return forbid("This token isn't allowed to run %s.", sc.Action)
	}
	if tr >= T3.rank() && a.Kind == KindToken {
		return forbid("API tokens can't run %s actions; they need a person to confirm.", req.Tier)
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
}

// PolicyOption adjusts NewPolicy.
type PolicyOption func(*Policy)

// WithGrants narrows actors to scopes: f returns an actor's grants, or nil
// for no narrowing (wave 10's API tokens).
func WithGrants(f func(context.Context, Actor) ([]string, error)) PolicyOption {
	return func(p *Policy) { p.grants = f }
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
	if req.Tier == T4 {
		t4, err := p.T4Enabled(ctx)
		if err != nil {
			p.log.Error("authz: reading the T4 settings failed; refusing", "err", err)
			return forbid("T4 actions are unavailable: the hub couldn't read their settings.")
		}
		env.T4 = t4
	}
	if p.grants != nil {
		g, err := p.grants(ctx, a)
		if err != nil {
			p.log.Error("authz: reading the actor's grants failed; refusing", "err", err, "actor", a.ID)
			return forbid("The hub couldn't check what you're allowed to do.")
		}
		env.Grants = g
	}
	return Decide(a, req, env)
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
