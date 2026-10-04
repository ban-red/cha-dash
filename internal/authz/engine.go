package authz

import (
	"context"
	"errors"

	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/auth"
)

// ForEngine adapts p to internal/action's Policy seam. The engine's Actor and
// Tier have their own types; this converts them, reads Household from the Def
// being authorized, maps the engine's read scopes to roles, and keeps
// Decide's sentence on refusals.
func (p *Policy) ForEngine() action.Policy { return enginePolicy{p} }

type enginePolicy struct{ p *Policy }

func (e enginePolicy) Authorize(ctx context.Context, a action.Actor, scope, target string, tier action.Tier) error {
	actor := Actor{Kind: a.Kind, ID: a.ID, Name: a.Name, Role: a.Role, Via: a.Via, Session: a.Session, IP: a.IP, StepUpUntil: a.StepUpUntil}
	switch scope {
	case action.ScopeAuditRead, action.ScopeTasksAll:
		// Not actions: the plan gives both to Admin and Owner.
		if rank(actor.Role) >= rank(auth.RoleAdmin) {
			return nil
		}
		return engineRefusal{action.ErrForbidden, "Only admins and the owner can see this."}
	}
	household := false
	if d, ok := action.DefFromContext(ctx); ok && d != nil {
		household = d.Household
	}
	err := e.p.Decide(ctx, actor, Request{Scope: scope, Target: target, Tier: Tier(tier.String()), Household: household})
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
