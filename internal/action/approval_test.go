package action

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"dash.cha.sh/internal/audit"
	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/livedoc"
)

// tokenPolicy stands in for internal/authz in these tests: tokens may plan
// up to T2, and their T2 plans wait for a person; approvals are the
// owner's, in a session, with a step-up from T2. revoked tokens can't do
// anything.
type tokenPolicy struct {
	now     func() time.Time
	revoked map[string]bool
}

func (p tokenPolicy) Authorize(_ context.Context, a Actor, scope, _ string, tier Tier) error {
	switch scope {
	case ScopeApprovals:
		if a.Kind != KindUser || a.Session == "" || a.Role != "owner" {
			return ErrForbidden
		}
		if tier >= T2 && !a.StepUpUntil.After(p.now()) {
			return ErrStepUpRequired
		}
		return nil
	case ScopeTasksAll:
		if a.Kind == KindUser && a.Role == "owner" {
			return nil
		}
		return ErrForbidden
	}
	switch a.Kind {
	case KindToken, KindSync:
		if p.revoked[a.ID] || tier >= T3 {
			return ErrForbidden
		}
		return nil
	case KindUser:
		if tier >= T3 && !a.StepUpUntil.After(p.now()) {
			return ErrStepUpRequired
		}
		return nil
	}
	return ErrForbidden
}

func (p tokenPolicy) NeedsApproval(a Actor, tier Tier) bool { return a.Kind == KindToken && tier >= T2 }

var (
	token  = Actor{Kind: KindToken, ID: "tok_backup", Name: "backup (sam)", Role: "owner", Via: "api", User: "u_owner", UserName: "sam"}
	token2 = Actor{Kind: KindToken, ID: "tok_other", Name: "other (sam)", Role: "owner", Via: "api", User: "u_owner", UserName: "sam"}
)

func newTokenEnv(t *testing.T, defs ...Def) (*env, tokenPolicy) {
	t.Helper()
	pol := tokenPolicy{revoked: map[string]bool{}}
	c := &clock{t: time.Date(2026, 10, 4, 14, 5, 11, 0, time.UTC)}
	pol.now = c.Now
	en := newEnvWithInbox(t, pol, c, defs...)
	return en, pol
}

// newEnvWithInbox is newEnv with an Inbox and a given clock.
func newEnvWithInbox(t *testing.T, pol Policy, c *clock, defs ...Def) *env {
	t.Helper()
	e := newEnv(t, pol, defs...)
	e.e.Close()
	e.clock = c
	e.audit.SetClock(c.Now)
	e.inbox = livedoc.New(incidentsColl)
	e.start()
	return e
}

func stepUpAt(a Actor, at time.Time) Actor {
	a.StepUpUntil = at.Add(StepUpTTL)
	return a
}

func (e *env) item(id string) map[string]any {
	e.t.Helper()
	raw := e.inbox.Item(incidentsColl, ApprovalItemID(id))
	if raw == nil {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		e.t.Fatal(err)
	}
	return m
}

func (e *env) actions() []string {
	var out []string
	for _, ev := range e.events() {
		out = append(out, ev.Actor.Kind+" "+ev.Action+" "+ev.Result)
	}
	return out
}

func TestTokenSelfApprovesT0AndT1(t *testing.T) {
	var calls []string
	start := Def{ID: "proxmox.guest.start", Title: "Start", Targets: []string{"vm"}, Tier: T1,
		Execute: func(context.Context, Request, *TaskRun) error { calls = append(calls, "start"); return nil }}
	e, _ := newTokenEnv(t, start)
	p := e.mustPlan(token, "proxmox.guest.start", "proxmox:i_x/qemu/201")
	task, err := e.approve(token, p, "", "")
	if err != nil {
		t.Fatal(err)
	}
	done := e.wait(task.ID)
	if done.State != StateSucceeded || done.Actor.Kind != KindToken || done.ApprovedBy != nil {
		t.Fatalf("task %+v", done)
	}
	// Another token, or a session, can't approve a token's plan.
	p = e.mustPlan(token, "proxmox.guest.start", "proxmox:i_x/qemu/201")
	refusal(t, func() error { _, err := e.approve(token2, p, "", ""); return err }(), "forbidden")
	refusal(t, func() error { _, err := e.approve(owner, p, "", ""); return err }(), "forbidden")
	// The token can read its own task; another can't.
	if _, err := e.e.Task(t.Context(), token, task.ID); err != nil {
		t.Fatalf("own task: %v", err)
	}
	if _, err := e.e.Task(t.Context(), token2, task.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another token's task: %v", err)
	}
	var last audit.Event
	for _, ev := range e.events() {
		if ev.Action == "proxmox.guest.start" && ev.Result == audit.OK {
			last = ev
		}
	}
	if last.Actor.Kind != KindToken || last.Actor.ID != "tok_backup" || last.Actor.Name != "backup (sam)" || last.Actor.Session != "" {
		t.Fatalf("audit actor %+v", last.Actor)
	}
}

func TestTokenT2WaitsForAPerson(t *testing.T) {
	var calls []string
	e, _ := newTokenEnv(t, reboot(&calls))
	p := e.mustPlan(token, "proxmox.guest.reboot", "proxmox:i_x/qemu/201")
	task, apv, err := e.e.ApproveOrSubmit(t.Context(), token, p.ID, ApproveRequest{Hash: p.Hash}, "")
	if err != nil || apv == nil || task.ID != "" {
		t.Fatalf("submit: %+v %+v %v", task, apv, err)
	}
	if apv.State != ApprovalAwaiting || apv.Plan.Hash != p.Hash || apv.Originator.ID != token.ID || apv.Originator.UserName != "sam" ||
		!apv.ExpiresAt.Equal(e.clock.Now().Add(ApprovalTTL)) {
		t.Fatalf("approval %+v", apv)
	}
	// Approve (the error form) says it's waiting.
	var aw *AwaitingApproval
	if _, err := e.approve(token, p, "", ""); !errors.As(err, &aw) || aw.Approval.ID != apv.ID {
		t.Fatalf("asking again: %v", err)
	}
	if len(calls) != 0 {
		t.Fatal("a T2 token plan ran without a person")
	}
	it := e.item(apv.ID)
	if it == nil || it["class"] != ApprovalClass || it["status"] != "open" || it["target"] != "proxmox:i_x/qemu/201" {
		t.Fatalf("inbox item %v", it)
	}
	if br, _ := it["blast_radius"].([]any); len(br) != 1 {
		t.Fatalf("blast radius %v", it["blast_radius"])
	}

	// Who may read it: the token that asked, and approvers.
	if _, err := e.e.Approval(t.Context(), token, apv.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.e.Approval(t.Context(), token2, apv.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another token read it: %v", err)
	}
	if _, err := e.e.Approval(t.Context(), other, apv.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an operator read it: %v", err)
	}
	if list, err := e.e.Approvals(t.Context(), owner); err != nil || len(list) != 1 {
		t.Fatalf("list: %v %v", list, err)
	}
	refusal(t, func() error { _, err := e.e.Approvals(t.Context(), token); return err }(), "forbidden")

	// Approving: never a token; a person needs a step-up made after the request.
	refusal(t, func() error {
		_, err := e.e.Grant(t.Context(), token, apv.ID, ApproveRequest{Hash: p.Hash})
		return err
	}(), "forbidden")
	refusal(t, func() error {
		_, err := e.e.Grant(t.Context(), owner, apv.ID, ApproveRequest{Hash: p.Hash})
		return err
	}(), "step_up_required")
	early := stepUpAt(owner, e.clock.Now().Add(-time.Minute))
	refusal(t, func() error {
		_, err := e.e.Grant(t.Context(), early, apv.ID, ApproveRequest{Hash: p.Hash})
		return err
	}(), "step_up_required")
	e.clock.Advance(10 * time.Minute) // well past the plan's own two minutes
	fresh := stepUpAt(owner, e.clock.Now())
	refusal(t, func() error {
		_, err := e.e.Grant(t.Context(), fresh, apv.ID, ApproveRequest{Hash: "sha256:0"})
		return err
	}(), "plan_changed")
	task, err = e.e.Grant(t.Context(), fresh, apv.ID, ApproveRequest{Hash: p.Hash})
	if err != nil {
		t.Fatal(err)
	}
	done := e.wait(task.ID)
	if done.State != StateSucceeded || done.Actor.Kind != KindToken || done.ApprovedBy == nil || done.ApprovedBy.Name != "sam" {
		t.Fatalf("task %+v", done)
	}
	if !slices.Equal(calls, []string{"execute:proxmox:i_x/qemu/201", "verify"}) {
		t.Fatalf("calls %v", calls)
	}
	// The token sees its task, and the approval says who approved it.
	if _, err := e.e.Task(t.Context(), token, task.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := e.e.Approval(t.Context(), token, apv.ID)
	if got.State != ApprovalApproved || got.Task == nil || *got.Task != task.ID || got.DecidedBy == nil || got.DecidedBy.Name != "sam" {
		t.Fatalf("after: %+v", got)
	}
	if it := e.item(apv.ID); it["status"] != "resolved" || it["resolved_by"] != ApprovalApproved {
		t.Fatalf("inbox item after: %v", it)
	}
	// Once only.
	refusal(t, func() error {
		_, err := e.e.Grant(t.Context(), fresh, apv.ID, ApproveRequest{Hash: p.Hash})
		return err
	}(), "already_decided")

	want := []string{
		"token approval.request ok",
		"user approval.approve refused", // no step-up
		"user approval.approve refused", // too early a step-up
		"user approval.approve refused", // wrong hash
		"user approval.approve ok",
		"token proxmox.guest.reboot ok",
		"user approval.approve refused", // twice
	}
	// The token's own attempt to grant is refused and audited too, first.
	got2 := e.actions()
	if len(got2) == 0 || got2[0] != want[0] {
		t.Fatalf("audit %v", got2)
	}
	filtered := []string{}
	for _, a := range got2 {
		if a != "token approval.approve refused" {
			filtered = append(filtered, a)
		}
	}
	if !slices.Equal(filtered, want) {
		t.Fatalf("audit %v, want %v", filtered, want)
	}
}

func TestApprovalDenyExpireWithdraw(t *testing.T) {
	var calls []string
	e, pol := newTokenEnv(t, reboot(&calls))
	submit := func(a Actor) Approval {
		t.Helper()
		p := e.mustPlan(a, "proxmox.guest.reboot", "proxmox:i_x/qemu/201")
		_, apv, err := e.e.ApproveOrSubmit(t.Context(), a, p.ID, ApproveRequest{Hash: p.Hash}, "")
		if err != nil || apv == nil {
			t.Fatalf("submit: %v %v", apv, err)
		}
		return *apv
	}

	// Deny needs no step-up, but an approver.
	a := submit(token)
	refusal(t, func() error { _, err := e.e.Deny(t.Context(), other, a.ID, DenyRequest{}); return err }(), "forbidden")
	d, err := e.e.Deny(t.Context(), owner, a.ID, DenyRequest{Reason: "Not during the film."})
	if err != nil || d.State != ApprovalDenied || d.Reason != "Not during the film." {
		t.Fatalf("deny: %+v %v", d, err)
	}
	refusal(t, func() error {
		_, err := e.e.Grant(t.Context(), stepUpAt(owner, e.clock.Now()), a.ID, ApproveRequest{Hash: a.Plan.Hash})
		return err
	}(), "already_decided")
	if it := e.item(a.ID); it["status"] != "resolved" || it["resolved_by"] != ApprovalDenied {
		t.Fatalf("denied item %v", it)
	}

	// Expiry: an hour, then it's gone.
	b := submit(token)
	e.clock.Advance(ApprovalTTL - time.Second)
	e.e.ExpireApprovals(t.Context())
	if got, _ := e.e.Approval(t.Context(), token, b.ID); got.State != ApprovalAwaiting {
		t.Fatalf("expired early: %+v", got)
	}
	e.clock.Advance(time.Second)
	e.e.ExpireApprovals(t.Context())
	got, _ := e.e.Approval(t.Context(), token, b.ID)
	if got.State != ApprovalExpired || got.Reason == "" {
		t.Fatalf("not expired: %+v", got)
	}
	refusal(t, func() error {
		_, err := e.e.Grant(t.Context(), stepUpAt(owner, e.clock.Now()), b.ID, ApproveRequest{Hash: b.Plan.Hash})
		return err
	}(), "approval_expired")

	// Revoking the token withdraws what it asked for; a revoked token
	// can't be approved for either.
	c := submit(token)
	pol.revoked[token.ID] = true
	refusal(t, func() error {
		_, err := e.e.Grant(t.Context(), stepUpAt(owner, e.clock.Now()), c.ID, ApproveRequest{Hash: c.Plan.Hash})
		return err
	}(), "forbidden")
	if got, _ := e.e.Approval(t.Context(), owner, c.ID); got.State != ApprovalExpired {
		t.Fatalf("a revoked token's approval: %+v", got)
	}
	pol.revoked[token.ID] = false
	d2 := submit(token)
	e.e.Withdraw(t.Context(), token.ID, "The token that asked was revoked.")
	if got, _ := e.e.Approval(t.Context(), owner, d2.ID); got.State != ApprovalExpired || got.Reason != "The token that asked was revoked." {
		t.Fatalf("withdrawn: %+v", got)
	}

	// A restart: an awaiting approval returns to the Inbox; one whose time
	// ran out while the hub was down expires.
	f := submit(token)
	g := submit(token2)
	e.e.Close()
	e.inbox = livedoc.New(incidentsColl)
	e.clock.Advance(30 * time.Minute)
	e.start()
	if e.item(f.ID) == nil || e.item(f.ID)["status"] != "open" {
		t.Fatal("an awaiting approval didn't return to the Inbox")
	}
	e.e.Close()
	e.inbox = livedoc.New(incidentsColl)
	e.clock.Advance(31 * time.Minute)
	e.start()
	if got, _ := e.e.Approval(t.Context(), owner, g.ID); got.State != ApprovalExpired {
		t.Fatalf("after a long restart: %+v", got)
	}
	if len(calls) != 0 {
		t.Fatalf("something ran: %v", calls)
	}

	var exp []string
	for _, a := range e.actions() {
		if a == "system approval.expire ok" {
			exp = append(exp, a)
		}
	}
	if len(exp) != 5 { // b, c (voided), d2 (withdrawn), f and g
		t.Fatalf("expiries audited %d: %v", len(exp), e.actions())
	}
}

func TestApprovalVoidedWhenTheTargetChanges(t *testing.T) {
	var calls []string
	e, _ := newTokenEnv(t, reboot(&calls))
	p := e.mustPlan(token, "proxmox.guest.reboot", "proxmox:i_x/qemu/201")
	_, apv, err := e.e.ApproveOrSubmit(t.Context(), token, p.ID, ApproveRequest{Hash: p.Hash}, "")
	if err != nil {
		t.Fatal(err)
	}
	renameVM(t, e)
	refusal(t, func() error {
		_, err := e.e.Grant(t.Context(), stepUpAt(owner, e.clock.Now()), apv.ID, ApproveRequest{Hash: p.Hash})
		return err
	}(), "plan_changed")
	if got, _ := e.e.Approval(t.Context(), owner, apv.ID); got.State != ApprovalExpired {
		t.Fatalf("approval after the target changed: %+v", got)
	}
}

// renameVM changes win-gaming's state, so a plan on it no longer matches.
func renameVM(t *testing.T, e *env) {
	t.Helper()
	if _, err := e.g.Update(func(tx *graph.Tx) error {
		return tx.Upsert(graph.Resource{ID: "proxmox:i_x/qemu/201", Kind: "vm", Name: "win-gaming", State: "stopped"})
	}); err != nil {
		t.Fatal(err)
	}
}
