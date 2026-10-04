package action

import (
	"context"
	"time"
)

// AllowAll lets everyone do everything, step-up included. It's for tests
// only: never wire it into the hub.
type AllowAll struct{}

// Authorize implements Policy.
func (AllowAll) Authorize(context.Context, Actor, string, string, Tier) error { return nil }

// OwnerOnly is a simple policy for the engine's tests: only the owner may act
// or read the audit log, T3 needs a step-up grant that hasn't ended, and T4
// is refused unless AllowT4 is set. The hub runs internal/authz's.
type OwnerOnly struct {
	AllowT4 bool
	Now     func() time.Time // nil means time.Now
}

// Authorize implements Policy.
func (p OwnerOnly) Authorize(_ context.Context, a Actor, _, _ string, tier Tier) error {
	if a.Kind != KindUser || a.Role != "owner" {
		return ErrForbidden
	}
	if tier == T4 && !p.AllowT4 {
		return ErrForbidden
	}
	now := time.Now
	if p.Now != nil {
		now = p.Now
	}
	if tier >= T3 && !a.StepUpUntil.After(now()) {
		return ErrStepUpRequired
	}
	return nil
}
