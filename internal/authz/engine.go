package authz

import (
	"context"
	"errors"

	"dash.cha.sh/internal/action"
)

// ForEngine adapts p to internal/action's Policy seam. The engine's Actor and
// Tier have their own types; this converts them, reads Household from the Def
// being authorized, maps the engine's read scopes to roles (and, for API
// tokens, to their scopes), decides approvals, and keeps Decide's sentence
// on refusals. It also implements action.ApprovalPolicy: which plans wait
// for a person.
func (p *Policy) ForEngine() action.Policy { return enginePolicy{p} }

type enginePolicy struct{ p *Policy }

var _ action.ApprovalPolicy = enginePolicy{}

func actorOf(a action.Actor) Actor {
	return Actor{Kind: a.Kind, ID: a.ID, Name: a.Name, Role: a.Role, Via: a.Via, Session: a.Session, IP: a.IP, StepUpUntil: a.StepUpUntil, User: a.User, UserName: a.UserName}
}

func (e enginePolicy) Authorize(ctx context.Context, a action.Actor, scope, target string, tier action.Tier) error {
	actor := actorOf(a)
	switch scope {
	case action.ScopeAuditRead, action.ScopeTasksAll:
		// Not actions: the plan gives both to Admin and Owner (and a token
		// needs the scope as well).
		return engineErr(e.p.ReadScope(ctx, actor, scope))
	case action.ScopeApprovals:
		// Deciding a plan that waits for a person, at its tier (T0 to deny).
		return engineErr(DecideApproval(actor, Tier(tier.String()), e.p.now()))
	}
	household := false
	if d, ok := action.DefFromContext(ctx); ok && d != nil {
		household = d.Household
	}
	return engineErr(e.p.Decide(ctx, actor, Request{Scope: scope, Target: target, Tier: Tier(tier.String()), Household: household}))
}

// NeedsApproval implements action.ApprovalPolicy (NeedsApproval).
func (e enginePolicy) NeedsApproval(a action.Actor, tier action.Tier) bool {
	return NeedsApproval(a.Kind, Tier(tier.String()))
}

// engineErr converts a decision to the engine's sentinels, keeping the
// sentence.
func engineErr(err error) error {
	var r *Refusal
	switch {
	case err == nil:
		return nil
	case errors.As(err, &r) && errors.Is(r.Err, ErrStepUpRequired):
		return engineRefusal{action.ErrStepUpRequired, r.Msg}
	case errors.As(err, &r):
		return engineRefusal{action.ErrForbidden, r.Msg}
	}
	return action.ErrForbidden
}

// engineRefusal unwraps to the engine's sentinel and reads as Decide's
// sentence.
type engineRefusal struct {
	err error
	msg string
}

func (r engineRefusal) Error() string { return r.msg }
func (r engineRefusal) Unwrap() error { return r.err }
