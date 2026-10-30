// Package action is the action engine: every change Cha Dash makes to the
// lab goes through it, from a tile button, ⌘K, the REST API or an agent.
//
// An action is a Def in the Registry, contributed by a source (an
// integration's Actor, the Demo Home, the hub itself). Each attempt runs the
// same lifecycle:
//
//  1. Resolve: the action, its target (through the Def or the Home Graph)
//     and its params, validated against the Def's schema.
//  2. Authorize: the Policy decides, given the actor, the scope, the target
//     and the tier. This package never decides permissions itself.
//  3. Plan: steps, blast radius, undo and the confirmation the tier needs,
//     stored with a hash over everything shown. Plans expire after
//     PlanTTL.
//  4. Approve: the same session echoes the hash (and the typed name for T3
//     and T4); the target must not have changed, and the policy is asked
//     again, now with any step-up grant.
//  5. Execute: under a per-resource lock, with an optional idempotency key,
//     as a Task with steps, progress and a log tail, a timeout and
//     cancellation.
//  6. Verify: the Def's post-condition, read back through the source. A
//     failed verify is a failed action.
//  7. Audit: every refusal, failure and success goes to internal/audit.
//
// Tasks are published on the "tasks" realtime topic (TasksSource) and kept
// in the tasks table; plans in plans. See docs/plan/08-v0.2.md for the
// contract and the HTTP shapes, which http.go implements.
package action

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Tier is an action's safety tier (02 · Experience): T0 instant, T1
// reversible, T2 disruptive, T3 destructive, T4 critical.
type Tier uint8

// Tiers.
const (
	T0 Tier = iota
	T1
	T2
	T3
	T4
)

func (t Tier) String() string { return fmt.Sprintf("T%d", uint8(t)) }

// MarshalText implements encoding.TextMarshaler: "T0" … "T4".
func (t Tier) MarshalText() ([]byte, error) {
	if t > T4 {
		return nil, fmt.Errorf("action: tier %d", uint8(t))
	}
	return []byte(t.String()), nil
}

// UnmarshalText implements encoding.TextUnmarshaler.
func (t *Tier) UnmarshalText(b []byte) error {
	v, err := ParseTier(string(b))
	if err != nil {
		return err
	}
	*t = v
	return nil
}

// ParseTier parses "T0" … "T4".
func ParseTier(s string) (Tier, error) {
	if len(s) == 2 && s[0] == 'T' && s[1] >= '0' && s[1] <= '4' {
		return Tier(s[1] - '0'), nil
	}
	return 0, fmt.Errorf("action: unknown tier %q", s)
}

// Actor is who asks: a signed-in user, an API token, a git sync, or (later)
// an agent. internal/authz decides what an Actor may do. A plan's actor is
// its originator: the plans table keeps it, and an approval names it.
type Actor struct {
	Kind    string `json:"kind"` // user, token, sync, agent, system
	ID      string `json:"id,omitempty"`
	Name    string `json:"name"`
	Role    string `json:"role,omitempty"`
	Via     string `json:"via,omitempty"` // web, api, mcp
	Session string `json:"session,omitempty"`
	IP      string `json:"ip,omitempty"`
	// User and UserName are the user a token, a sync (its owner) or an
	// agent acts for.
	User     string `json:"user,omitempty"`
	UserName string `json:"user_name,omitempty"`
	// StepUpUntil is when the session's step-up grant ends; zero for none.
	StepUpUntil time.Time `json:"-"`
}

// Actor kinds.
const (
	KindUser   = "user"
	KindToken  = "token" // an API token, acting for its user
	KindAgent  = "agent" // an agent's token (ADR-0017; not issued yet)
	KindSync   = "sync"  // a git sync (ID), acting for its owner (User); no session, no step-up
	KindSystem = "system"
)

// binding is what may approve a plan the actor made: its session, or for a
// token, an agent or a sync, the actor itself ("sync:<id>"). "" binds to
// nothing, so nothing can approve.
func (a Actor) binding() string {
	switch {
	case a.Session != "":
		return a.Session
	case (a.Kind == KindToken || a.Kind == KindAgent || a.Kind == KindSync) && a.ID != "":
		return a.Kind + ":" + a.ID
	}
	return ""
}

// Policy decides whether an actor may act. internal/authz implements it;
// The hub runs authz's through authz.Policy.ForEngine; OwnerOnly is for tests.
type Policy interface {
	// Authorize returns nil, ErrForbidden or ErrStepUpRequired. scope is
	// the action's scope (its ID unless the Def says otherwise) or a read
	// scope such as ScopeAuditRead; target is the resource ID, or "". The
	// Def being authorized, if any, is DefFromContext(ctx).
	Authorize(ctx context.Context, a Actor, scope, target string, tier Tier) error
}

// ApprovalPolicy is an optional extension of Policy: plans by the actors it
// names wait for a person (an approval in a session) instead of running when
// their maker approves them. internal/authz implements it: an API token's
// plans from T2 up, every agent's plan. Without it, only plans made with
// RequireApproval wait.
type ApprovalPolicy interface {
	NeedsApproval(a Actor, tier Tier) bool
}

// Policy answers.
var (
	ErrForbidden      = errors.New("action: forbidden")
	ErrStepUpRequired = errors.New("action: step-up required")
)

// Scopes that aren't actions: reading the audit log, and seeing (or
// cancelling) other people's tasks. The policy maps them to roles; the plan
// gives both to Admin and Owner. Cancelling is audited as ActionCancel.
const (
	ScopeAuditRead = "audit.read"
	ScopeTasksAll  = "tasks.all"
	// ActionCancel is the audit log's name for cancelling a task.
	ActionCancel = "task.cancel"
	// ScopeApprovals is deciding (approving or denying) a plan that waits
	// for a person, asked at the plan's tier to approve and at T0 to deny or
	// to read others' approvals. The policy gives it to people in a session,
	// admin and up, with a step-up from T2.
	ScopeApprovals = "approvals.decide"
)

type defKey struct{}

// WithDef attaches the Def being authorized to ctx.
func WithDef(ctx context.Context, d *Def) context.Context { return context.WithValue(ctx, defKey{}, d) }

// DefFromContext returns the Def being authorized, if any: a Policy reads
// Household and Tier from it.
func DefFromContext(ctx context.Context) (*Def, bool) {
	d, ok := ctx.Value(defKey{}).(*Def)
	return d, ok
}

// Target is a resolved target.
type Target struct {
	ID   string // the resource ID, as the request named it
	Kind string // resource kind, when known
	Name string // what the UI calls it: confirm_text for T3 and T4, and labels
	// State fingerprints what an approval must find unchanged: for a graph
	// resource its kind, name and state. It goes into the plan's hash.
	State string
	// Ref is the source's own handle on the target (an incident ID, a
	// Proxmox node and VMID). Approval resolves the target again, so
	// Execute and Verify get it as of then.
	Ref string
}

// Impact is one resource in a blast radius, the web's Impact shape.
type Impact struct {
	Resource string `json:"resource"`
	Via      string `json:"via"`
	Depth    int    `json:"depth"`
}

// Step is one step a plan will take.
type Step struct {
	Label string `json:"label"`
}

// Undo is the inverse a plan offers: an action ID and its params. It's
// never run automatically.
type Undo struct {
	Action string          `json:"action"`
	Params json.RawMessage `json:"params"`
	Label  string          `json:"label"`
}

// Request is one action as the engine hands it to a Def's hooks.
type Request struct {
	Action string
	Target Target
	// Params are the validated params, a JSON object ({} for none).
	Params json.RawMessage
	Actor  Actor
	Plan   string // the plan's ID; "" while planning
	Task   string // the task's ID; set for Execute and Verify
}

// Decode unmarshals the params into v.
func (r Request) Decode(v any) error { return json.Unmarshal(r.Params, v) }

// Schema describes and checks an action's params. integration.Params
// adapts the integration framework's JSON Schema subset; a Def without one
// takes no params.
type Schema interface {
	// MarshalJSON is the JSON Schema, for clients.
	MarshalJSON() ([]byte, error)
	// Validate checks decoded params (a map from encoding/json).
	Validate(params map[string]any) []FieldError
	// Secrets are the top-level params that hold secrets: redacted in the
	// audit log and never echoed in a plan.
	Secrets() []string
}

// FieldError is one invalid param, by JSON Pointer.
type FieldError struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// Def is one action. ID, Tier and Execute are required; the rest have
// defaults.
type Def struct {
	// ID is <integration>.<kind>.<verb>: docker.container.restart.
	ID string
	// Title names the action in a label: "Reboot" becomes "Reboot
	// win-gaming". Label overrides it.
	Title string
	// Targets are the resource kinds it applies to; empty means any.
	Targets []string
	Params  Schema
	// Choices are params the listing offers one by one on the resolved
	// target, each checked with Preconditions and listed with its params,
	// so a page can run it as is (a Home Assistant script per allowed one).
	// Never secrets: offers carry them. Nil, or none: one offer, with no
	// params.
	Choices func(t Target) []json.RawMessage
	Tier    Tier
	// Scope is the authorization scope; empty means ID. The policy may
	// qualify it by target (proxmox.guest.reboot:host=io).
	Scope string
	// Household marks an action the Household role may run (pausing
	// Pi-hole, say). Household runs nothing else, T0 included.
	Household bool
	// ReadsData marks an action that reads data back to whoever runs it (a
	// container's logs), which can hold secrets: the policy wants Operator
	// or above whatever its tier, unless it's also Household.
	ReadsData bool
	// Async marks a long-running action (a PVE task, a deploy): its default
	// timeout is AsyncTimeout rather than DefaultTimeout.
	Async bool
	// Disrupts marks an action that takes its target down for a while
	// (reboot, stop). The engine doesn't read it: the integration runtime
	// raises such an action to T4 when the target runs the hub or the
	// router (the self-aware T4; see runtime.Actions).
	Disrupts bool
	// Cancellable lets the person who started a task (or anyone with
	// ScopeTasksAll) stop it while it runs: POST /api/tasks/{id}/cancel
	// ends its context. Leave it false unless Execute and Verify stop
	// cleanly when their context ends; instant T0 and T1 actions don't need
	// it, and an action whose upstream can't be stopped mustn't claim it.
	Cancellable bool
	// Timeout bounds Execute; VerifyTimeout bounds Verify.
	Timeout       time.Duration
	VerifyTimeout time.Duration

	// Resolve finds the target. Nil resolves it in the Home Graph: the
	// resource must exist and be of one of Targets' kinds. Return
	// ErrNotFound for an unknown target.
	Resolve func(ctx context.Context, target string) (Target, error)
	// TierFor raises the tier for a particular target (the host running
	// the hub is T4). Nil keeps Tier.
	TierFor func(t Target) Tier
	// Preconditions refuse early with a sentence: return Precondition(…)
	// or Unavailable(…). Checked at plan and again at approve.
	Preconditions func(ctx context.Context, req Request) error
	// Permitted refuses, as Preconditions do, when the credential that acts
	// may not do this to this target (an API token's ACL). The engine
	// doesn't read it: the integration runtime runs it on the instance
	// connected with the control credential, after Preconditions, so a
	// target out of that credential's reach is unavailable when planned
	// instead of failing when it runs. Nil permits.
	Permitted func(ctx context.Context, req Request) error
	// Label is what the plan and the task are called. Nil is Title plus
	// the target's name.
	Label func(req Request) string
	// Steps are what the plan says will happen. Nil is the label alone,
	// plus "Check the result" when there's a Verify.
	Steps func(req Request) []Step
	// BlastRadius is what the action affects. Nil walks the Home Graph
	// from the target (GraphImpact).
	BlastRadius func(ctx context.Context, req Request) []Impact
	// Undo is the inverse to offer once it has run, if any.
	Undo func(req Request) *Undo
	// Execute does it, reporting progress on run. Return a *Error for a
	// failure the UI can show as is.
	Execute func(ctx context.Context, req Request, run *TaskRun) error
	// Verify is the post-condition, read back through the source's normal
	// reads ("the guest reports running"). It may wait (until its context
	// ends) and report progress. Nil skips verification.
	Verify func(ctx context.Context, req Request, run *TaskRun) error
	// Resumes marks an action whose task can outlive the hub (Cha Dash
	// updating itself restarts the hub mid-task). At start, a task the last
	// run left running is handed to Resume instead of being marked
	// interrupted. Without Resumes it's interrupted; Register refuses
	// Resumes without a Resume. A task that never started is interrupted
	// either way.
	Resumes bool
	// Resume finds out how a task the hub lost track of ended (asking the
	// node for the last result) and reports it as Execute does: nil
	// succeeded, a *Error failed with that code. The engine finishes the
	// task with it, audit record, tasks topic and OnTaskDone included, as
	// any task ends; Verify isn't run. It runs in the background once the
	// engine has started, bounded by Timeout, holding the target's lock,
	// with t as stored (its state running). If the hub stops again before
	// it returns, the task stays running and is resumed at the next start.
	Resume func(ctx context.Context, t Task, run *TaskRun) error
}

// Timeouts used when a Def doesn't set its own.
const (
	DefaultTimeout       = 2 * time.Minute
	AsyncTimeout         = 30 * time.Minute
	DefaultVerifyTimeout = time.Minute
)

func (d *Def) scope() string {
	if d.Scope != "" {
		return d.Scope
	}
	return d.ID
}

func (d *Def) timeout() time.Duration {
	switch {
	case d.Timeout > 0:
		return d.Timeout
	case d.Async:
		return AsyncTimeout
	}
	return DefaultTimeout
}

func (d *Def) verifyTimeout() time.Duration {
	if d.VerifyTimeout > 0 {
		return d.VerifyTimeout
	}
	return DefaultVerifyTimeout
}

// Refusal is an attempt refused with a stable code and a sentence the web
// shows as is.
type Refusal struct {
	Status  int    // HTTP status
	Code    string // forbidden, not_found, precondition, unavailable, invalid, …
	Message string
	// Recipe names the instance whose recipe grants what's missing
	// (unavailable only).
	Recipe string
	// Instead is a page to open in place of the action, such as the
	// upstream's own list when there's too much to do at once
	// (unavailable only).
	Instead *Link
	// Fields are the invalid params (invalid only).
	Fields []FieldError
}

func (r *Refusal) Error() string { return r.Code + ": " + r.Message }

// Precondition refuses because the target isn't in a state the action
// makes sense for: "pluto is already stopped".
func Precondition(msg string) *Refusal {
	return &Refusal{Status: 409, Code: "precondition", Message: msg}
}

// Unavailable refuses because Cha Dash can't do it here yet: no control
// credential, a scope missing from a key. recipe names the instance.
func Unavailable(msg, recipe string) *Refusal {
	return &Refusal{Status: 409, Code: "unavailable", Message: msg, Recipe: recipe}
}

// Link is a page someone can open: a title and an http(s) URL.
type Link struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

// UnavailableInstead refuses an action and offers a page to open in its
// place; listings show it as that link.
func UnavailableInstead(msg string, instead Link) *Refusal {
	return &Refusal{Status: 409, Code: "unavailable", Message: msg, Instead: &instead}
}

// ErrNotFound is what a Def's Resolve returns for an unknown target.
var ErrNotFound = errors.New("action: target not found")

// Error is a failure Execute or Verify reports with a code and a sentence.
// Any other error fails the task with code "failed" and its text.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Message }

// Fail returns an *Error.
func Fail(code, msg string) *Error { return &Error{Code: code, Message: msg} }
