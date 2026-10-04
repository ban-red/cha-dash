package authz

import (
	"errors"
	"testing"
	"time"

	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/auth"
)

func TestForEngine(t *testing.T) {
	now := time.Now()
	p := NewPolicy(openStore(t), nil, WithClock(func() time.Time { return now })).ForEngine()
	ctx := t.Context()
	actor := func(role string) action.Actor {
		return action.Actor{Kind: action.KindUser, ID: "u_1", Name: "sam", Role: role}
	}

	// Tier caps come through with Decide's sentence, unwrapping to the engine's sentinel.
	err := p.Authorize(ctx, actor(auth.RoleViewer), "proxmox.guest.reboot", "proxmox:i_x/qemu/201", action.T2)
	if !errors.Is(err, action.ErrForbidden) || err.Error() != "Your role (viewer) can't run T2 actions." {
		t.Fatalf("viewer T2: %v", err)
	}
	if err := p.Authorize(ctx, actor(auth.RoleOperator), "proxmox.guest.reboot", "", action.T2); err != nil {
		t.Fatalf("operator T2: %v", err)
	}
	if err := p.Authorize(ctx, actor(auth.RoleAdmin), "proxmox.guest.snapshot.delete", "", action.T3); !errors.Is(err, action.ErrStepUpRequired) {
		t.Fatalf("admin T3 without a grant: %v", err)
	}
	stepped := actor(auth.RoleAdmin)
	stepped.StepUpUntil = now.Add(time.Minute)
	if err := p.Authorize(ctx, stepped, "proxmox.guest.snapshot.delete", "", action.T3); err != nil {
		t.Fatalf("admin T3 with a grant: %v", err)
	}

	// Household runs only what the Def marks for the household.
	pause := &action.Def{ID: "pihole.app.pause", Household: true}
	if err := p.Authorize(action.WithDef(ctx, pause), actor(auth.RoleHousehold), pause.ID, "", action.T1); err != nil {
		t.Fatalf("household, household action: %v", err)
	}
	if err := p.Authorize(ctx, actor(auth.RoleHousehold), "qbittorrent.app.pause", "", action.T1); !errors.Is(err, action.ErrForbidden) {
		t.Fatalf("household, other action: %v", err)
	}

	// The engine's read scopes belong to Admin and Owner.
	for _, scope := range []string{action.ScopeAuditRead, action.ScopeTasksAll} {
		if err := p.Authorize(ctx, actor(auth.RoleAdmin), scope, "", action.T0); err != nil {
			t.Fatalf("admin %s: %v", scope, err)
		}
		if err := p.Authorize(ctx, actor(auth.RoleOperator), scope, "", action.T0); !errors.Is(err, action.ErrForbidden) {
			t.Fatalf("operator %s: %v", scope, err)
		}
	}
}
