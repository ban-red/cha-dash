package authz

import (
	"errors"
	"testing"
	"time"

	"dash.cha.sh/internal/auth"
)

// TestChangeSecuritySettings: importing security settings follows PUT
// /api/settings/security's rule: the owner only, and the step-up rule
// needs a step-up.
func TestChangeSecuritySettings(t *testing.T) {
	for _, role := range []string{auth.RoleAdmin, auth.RoleOperator, auth.RoleViewer, auth.RoleHousehold, ""} {
		if err := ChangeSecuritySettings(stepped(user(role), time.Second), false, now); !errors.Is(err, ErrForbidden) {
			t.Errorf("%q: %v, want forbidden", role, err)
		}
	}
	owner := user(auth.RoleOwner)
	if err := ChangeSecuritySettings(owner, false, now); err != nil {
		t.Fatalf("owner, posture only: %v", err)
	}
	if err := ChangeSecuritySettings(owner, true, now); !errors.Is(err, ErrStepUpRequired) {
		t.Fatalf("owner, step-up rule, no grant: %v", err)
	}
	if err := ChangeSecuritySettings(stepped(owner, time.Minute), true, now); err != nil {
		t.Fatalf("owner with a grant: %v", err)
	}
}
