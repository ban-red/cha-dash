package authz

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/audit"
	"dash.cha.sh/internal/auth"
	"dash.cha.sh/internal/graph"
)

// syncUsers stands in for auth.Service.GetUser.
func syncUsers(users map[string]auth.UserInfo) func(context.Context, string) (auth.UserInfo, error) {
	return func(_ context.Context, id string) (auth.UserInfo, error) {
		if id == "u_broken" {
			return auth.UserInfo{}, errors.New("disk on fire")
		}
		u, ok := users[id]
		if !ok {
			return auth.UserInfo{}, auth.ErrUserNotFound
		}
		return u, nil
	}
}

func syncActor(owner string) Actor {
	// The role recorded on the actor is stale on purpose: the owner's
	// current role is what counts.
	return Actor{Kind: KindSync, ID: "sync_media", Name: "media (git)", Role: auth.RoleOwner, Via: "sync", User: owner}
}

// A sync acts for its owner as a token does: the owner's current role,
// refused once the owner is demoted below admin for stacks, disabled or
// gone, and never with a step-up.
func TestSyncActor(t *testing.T) {
	users := map[string]auth.UserInfo{
		"u_admin":    {ID: "u_admin", Username: "sam", Role: auth.RoleAdmin},
		"u_owner":    {ID: "u_owner", Username: "alex", Role: auth.RoleOwner},
		"u_demoted":  {ID: "u_demoted", Username: "kim", Role: auth.RoleOperator},
		"u_disabled": {ID: "u_disabled", Username: "lee", Role: auth.RoleAdmin, Disabled: true},
	}
	p := NewPolicy(openStore(t), nil, WithClock(func() time.Time { return now }), WithUsers(syncUsers(users)))
	ctx := t.Context()
	deploy := func(owner string) error {
		return p.Authorize(ctx, syncActor(owner), "stack.deploy", "stack:pve-a/media", T2)
	}

	want(t, "admin owner deploys", deploy("u_admin"), nil)
	want(t, "owner's sync deploys", deploy("u_owner"), nil)
	want(t, "owner demoted", deploy("u_demoted"), ErrForbidden)
	want(t, "owner disabled", deploy("u_disabled"), ErrForbidden)
	want(t, "owner deleted", deploy("u_gone"), ErrForbidden)
	want(t, "owner unreadable", deploy("u_broken"), ErrForbidden)
	want(t, "no owner", deploy(""), ErrForbidden)
	// The demoted owner may still run what an operator may, outside stacks.
	want(t, "demoted, not a stack", p.Authorize(ctx, syncActor("u_demoted"), "docker.container.restart", "", T2), nil)

	// No step-up, ever: T3 and T4 are refused outright, not asked for.
	stepped := syncActor("u_owner")
	stepped.StepUpUntil = now.Add(time.Minute)
	if err := p.Authorize(ctx, stepped, "stack.unadopt", "stack:pve-a/media", T3); !errors.Is(err, ErrForbidden) || errors.Is(err, ErrStepUpRequired) {
		t.Errorf("sync at T3: %v", err)
	}
	want(t, "sync at T3, Decide alone", Decide(Actor{Kind: KindSync, Role: auth.RoleOwner, StepUpUntil: now.Add(time.Minute)}, Request{Scope: "stack.unadopt", Tier: T3}, Env{Now: now}), ErrForbidden)
	want(t, "sync at T4", p.Authorize(ctx, stepped, "chadash.update", "", T4), ErrForbidden)

	// Without a way to read users, no sync is accepted.
	bare := NewPolicy(openStore(t), nil, WithClock(func() time.Time { return now }))
	want(t, "no WithUsers", bare.Authorize(ctx, syncActor("u_admin"), "stack.deploy", "", T2), ErrForbidden)

	// Reads that aren't actions, and approving, are never a sync's.
	want(t, "audit.read", p.ReadScope(ctx, syncActor("u_owner"), ScopeAuditRead), ErrForbidden)
	want(t, "approving", DecideApproval(syncActor("u_owner"), T0, now), ErrForbidden)

	// A sync's plans don't wait by kind: the syncer asks (RequireApproval).
	for _, tier := range []Tier{T0, T1, T2, T3, T4} {
		if NeedsApproval(KindSync, tier) {
			t.Errorf("NeedsApproval(sync, %s)", tier)
		}
	}
	// Audited as the sync, for its owner.
	if a := AuditActor(syncActor("u_admin")); a.Kind != KindSync || a.ID != "sync_media" || a.OnBehalfOf != "u_admin" {
		t.Errorf("audit actor %+v", a)
	}
	if action.KindSync != KindSync {
		t.Errorf("kinds differ: %q, %q", action.KindSync, KindSync)
	}
}

// Through the engine: a sync whose owner is an admin plans and runs
// stack.deploy; RequireApproval makes its plan, or an admin's, wait for a
// person; demoting the owner refuses the next plan.
func TestSyncThroughTheEngine(t *testing.T) {
	st := openStore(t)
	users := map[string]auth.UserInfo{"u_admin": {ID: "u_admin", Username: "sam", Role: auth.RoleAdmin}}
	clock := time.Now().UTC().Truncate(time.Millisecond)
	pol := NewPolicy(st, nil, WithClock(func() time.Time { return clock }), WithUsers(syncUsers(users)))
	reg := action.NewRegistry()
	ran := make(chan string, 4)
	deploy := action.Def{ID: "stack.deploy", Title: "Deploy", Tier: action.T2,
		Resolve: func(_ context.Context, target string) (action.Target, error) {
			return action.Target{ID: target, Kind: "stack", Name: "media"}, nil
		},
		BlastRadius: func(context.Context, action.Request) []action.Impact { return nil },
		Execute: func(_ context.Context, r action.Request, _ *action.TaskRun) error {
			ran <- r.Actor.Kind
			return nil
		}}
	if err := reg.Register("stacks", deploy); err != nil {
		t.Fatal(err)
	}
	log := audit.New(st)
	eng, err := action.New(t.Context(), action.Options{Store: st, Audit: log, Registry: reg, Policy: pol.ForEngine(), Graph: graph.New(),
		Log: slog.New(slog.DiscardHandler), Now: func() time.Time { return clock }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(eng.Close)
	ctx := t.Context()
	sync := action.Actor{Kind: action.KindSync, ID: "sync_media", Name: "media (git)", Via: "sync", User: "u_admin", UserName: "sam"}
	admin := action.Actor{Kind: action.KindUser, ID: "u_admin", Name: "sam", Role: auth.RoleAdmin, Via: "web", Session: "s_1"}
	req := action.PlanRequest{Action: "stack.deploy", Target: "stack:pve-a/media"}

	// Deploy on push: planned and run by the sync alone.
	p, err := eng.Plan(ctx, sync, req)
	if err != nil {
		t.Fatalf("sync plans stack.deploy: %v", err)
	}
	task, err := eng.Approve(ctx, sync, p.ID, action.ApproveRequest{Hash: p.Hash}, "")
	if err != nil || task.ID == "" {
		t.Fatalf("sync runs it: %+v %v", task, err)
	}
	if kind := <-ran; kind != action.KindSync {
		t.Fatalf("ran as %q", kind)
	}

	// Draft and ask: the same plan with RequireApproval waits, by a sync or
	// by an admin in a session.
	req.RequireApproval = true
	for _, a := range []action.Actor{sync, admin} {
		p, err := eng.Plan(ctx, a, req)
		if err != nil {
			t.Fatalf("%s plans: %v", a.Kind, err)
		}
		var aw *action.AwaitingApproval
		if _, err := eng.Approve(ctx, a, p.ID, action.ApproveRequest{Hash: p.Hash}, ""); !errors.As(err, &aw) || aw.Approval.State != action.ApprovalAwaiting {
			t.Fatalf("%s: RequireApproval didn't wait: %v", a.Kind, err)
		}
	}

	// The owner loses admin: the sync can't plan a deploy any more.
	users["u_admin"] = auth.UserInfo{ID: "u_admin", Username: "sam", Role: auth.RoleOperator}
	req.RequireApproval = false
	var ref *action.Refusal
	if _, err := eng.Plan(ctx, sync, req); !errors.As(err, &ref) || ref.Code != "forbidden" {
		t.Fatalf("demoted owner's sync plans: %v", err)
	}
	select {
	case kind := <-ran:
		t.Fatalf("something else ran, as %q", kind)
	default:
	}
}
