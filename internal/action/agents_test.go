package action

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"dash.cha.sh/internal/audit"
	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/livedoc"
)

// agentPolicy stands in for internal/authz: an agent may propose up to T3
// (never T4) unless revoked, needs no step-up to propose, and every plan of
// its waits for a person; approvals are the owner's, in a session, with a
// step-up from T2.
type agentPolicy struct{ tokenPolicy }

func (p agentPolicy) Authorize(ctx context.Context, a Actor, scope, target string, tier Tier) error {
	if a.Kind == KindAgent && scope != ScopeApprovals && scope != ScopeTasksAll {
		if p.revoked[a.ID] || tier >= T4 {
			return ErrForbidden
		}
		return nil
	}
	return p.tokenPolicy.Authorize(ctx, a, scope, target, tier)
}

// NeedsApproval says no for agents on purpose: the engine makes an agent's
// plans wait whatever the policy says.
func (p agentPolicy) NeedsApproval(a Actor, tier Tier) bool { return a.Kind == KindToken && tier >= T2 }

var agent = Actor{Kind: KindAgent, ID: "tok_agent", Name: "assistant (sam)", Role: "owner", Via: "mcp", User: "u_owner", UserName: "sam"}

func newAgentEnv(t *testing.T, limits *AgentLimits, defs ...Def) (*env, agentPolicy) {
	t.Helper()
	pol := agentPolicy{tokenPolicy{revoked: map[string]bool{}}}
	c := &clock{t: time.Date(2026, 10, 4, 14, 5, 11, 0, time.UTC)}
	pol.now = c.Now
	e := newEnv(t, pol, defs...)
	e.e.Close()
	e.clock = c
	e.audit.SetClock(c.Now)
	e.inbox = livedoc.New(incidentsColl)
	if limits != nil {
		l := *limits
		e.limits = func(context.Context) (AgentLimits, error) { return l, nil }
	}
	e.start()
	return e, pol
}

func restartDef(ran *[]string) Def {
	return Def{ID: "proxmox.guest.restart", Title: "Restart", Targets: []string{"vm"}, Tier: T1,
		Execute: func(_ context.Context, r Request, _ *TaskRun) error { *ran = append(*ran, r.Actor.Kind); return nil }}
}

// propose plans and submits as the agent, the way internal/mcp does.
func (e *env) propose(a Actor, action, target, rationale string) (*Plan, *Approval, error) {
	e.t.Helper()
	p, err := e.e.Plan(e.t.Context(), a, PlanRequest{Action: action, Target: target, Rationale: rationale})
	if err != nil {
		return nil, nil, err
	}
	task, apv, err := e.e.ApproveOrSubmit(e.t.Context(), a, p.ID, ApproveRequest{Hash: p.Hash}, "")
	if err == nil && task.ID != "" {
		e.t.Fatalf("an agent's plan ran without a person: %+v", task)
	}
	return p, apv, err
}

func TestAgentT1WaitsForAPerson(t *testing.T) {
	var ran []string
	e, _ := newAgentEnv(t, nil, restartDef(&ran))
	p, apv, err := e.propose(agent, "proxmox.guest.restart", "proxmox:i_x/qemu/201", "Health check failing\nfor 12 min.\u202e")
	if err != nil || apv == nil {
		t.Fatalf("propose: %v %v", apv, err)
	}
	if apv.State != ApprovalAwaiting || p.Tier != T1 {
		t.Fatalf("a T1 proposal: %+v", apv)
	}
	if got := apv.ExpiresAt.Sub(apv.RequestedAt); got != AgentApprovalTTL {
		t.Fatalf("expires after %v, want 15 minutes", got)
	}
	if apv.Plan.Rationale != "Health check failing for 12 min." {
		t.Fatalf("rationale %q: newlines and bidi controls should go", apv.Plan.Rationale)
	}
	if apv.NthToday != 1 || apv.Originator.Kind != KindAgent {
		t.Fatalf("nth %d, originator %+v", apv.NthToday, apv.Originator)
	}
	// Asking again returns the same approval; still nothing ran.
	if task, again, err := e.e.ApproveOrSubmit(t.Context(), agent, p.ID, ApproveRequest{Hash: p.Hash}, ""); err != nil || again == nil || again.ID != apv.ID || task.ID != "" {
		t.Fatalf("again: %+v %+v %v", task, again, err)
	}
	// The agent can't approve its own (or any) proposal.
	if _, err := e.e.Grant(t.Context(), agent, apv.ID, ApproveRequest{Hash: p.Hash}); refusal(t, err, "forbidden") == nil {
		t.Fatal("the agent approved")
	}
	item := e.item(apv.ID)
	if item == nil || !strings.Contains(item["summary"].(string), "Proposed by an agent") {
		t.Fatalf("Inbox item: %v", item)
	}
	// T1: a person in a session approves it without a step-up.
	task, err := e.e.Grant(t.Context(), owner, apv.ID, ApproveRequest{Hash: p.Hash})
	if err != nil {
		t.Fatal(err)
	}
	done := e.wait(task.ID)
	if done.State != StateSucceeded || len(ran) != 1 || ran[0] != KindAgent || done.ApprovedBy == nil || done.ApprovedBy.Name != "sam" {
		t.Fatalf("task %+v ran as %v", done, ran)
	}
	// Both sides in the audit: the request with its rationale and hash,
	// the approval by the person with the originator, and the task by the
	// agent on behalf of its user.
	var req, appr, act *audit.Event
	for _, ev := range e.events() {
		switch ev.Action {
		case ActionApprovalRequest:
			req = &ev
		case ActionApprovalApprove:
			appr = &ev
		case "proxmox.guest.restart":
			act = &ev
		}
	}
	if req == nil || req.Actor.Kind != KindAgent || req.Actor.OnBehalfOf != "u_owner" ||
		!strings.Contains(string(req.Params), `"rationale":"Health check failing for 12 min."`) || !strings.Contains(string(req.Params), p.Hash) {
		t.Fatalf("approval.request: %+v %s", req, req.Params)
	}
	if appr == nil || appr.Actor.Kind != KindUser || appr.Actor.Session == "" ||
		!strings.Contains(string(appr.Params), `"originator_kind":"agent"`) || !strings.Contains(string(appr.Params), `"on_behalf_of":"u_owner"`) {
		t.Fatalf("approval.approve: %+v %s", appr, appr.Params)
	}
	if act == nil || act.Actor.Kind != KindAgent || act.Actor.ID != agent.ID || act.Actor.OnBehalfOf != "u_owner" || act.Result != audit.OK {
		t.Fatalf("the action's record: %+v", act)
	}
}

func TestAgentT2NeedsAFreshStepUp(t *testing.T) {
	var ran []string
	def := restartDef(&ran)
	def.Tier = T2
	e, _ := newAgentEnv(t, nil, def)
	p, apv, err := e.propose(agent, "proxmox.guest.restart", "proxmox:i_x/qemu/201", "Stuck.")
	if err != nil {
		t.Fatal(err)
	}
	// A step-up made before the request doesn't count.
	if _, err := e.e.Grant(t.Context(), stepUpAt(owner, e.clock.Now().Add(-time.Minute)), apv.ID, ApproveRequest{Hash: p.Hash}); refusal(t, err, "step_up_required") == nil {
		t.Fatal("an old step-up approved it")
	}
	e.clock.Advance(time.Second)
	if _, err := e.e.Grant(t.Context(), stepUpAt(owner, e.clock.Now()), apv.ID, ApproveRequest{Hash: p.Hash}); err != nil {
		t.Fatal(err)
	}
}

func TestAgentApprovalBindsToTheHash(t *testing.T) {
	var ran []string
	e, _ := newAgentEnv(t, nil, restartDef(&ran))
	p, apv, err := e.propose(agent, "proxmox.guest.restart", "proxmox:i_x/qemu/201", "Stuck.")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.e.Grant(t.Context(), owner, apv.ID, ApproveRequest{Hash: "sha256:other"}); refusal(t, err, "plan_changed") == nil {
		t.Fatal("approved with another hash")
	}
	// The blast radius grows: the approval no longer fits, and is void.
	if _, err := e.g.Update(func(tx *graph.Tx) error {
		if err := tx.Upsert(graph.Resource{ID: "app:sonarr", Kind: "app", Name: "Sonarr"}); err != nil {
			return err
		}
		return tx.Relate(graph.Relation{From: "app:sonarr", To: "proxmox:i_x/qemu/201", Type: "runs_on"})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.e.Grant(t.Context(), owner, apv.ID, ApproveRequest{Hash: p.Hash}); refusal(t, err, "plan_changed") == nil {
		t.Fatal("approved after the blast radius changed")
	}
	if got, _ := e.e.Approval(t.Context(), owner, apv.ID); got.State != ApprovalExpired {
		t.Fatalf("state %s, want expired (void)", got.State)
	}
	if len(ran) != 0 {
		t.Fatal("it ran")
	}
}

func TestAgentProposalsExpire(t *testing.T) {
	var ran []string
	e, _ := newAgentEnv(t, nil, restartDef(&ran))
	p, apv, err := e.propose(agent, "proxmox.guest.restart", "proxmox:i_x/qemu/201", "Stuck.")
	if err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(AgentApprovalTTL)
	if _, err := e.e.Grant(t.Context(), owner, apv.ID, ApproveRequest{Hash: p.Hash}); refusal(t, err, "approval_expired") == nil {
		t.Fatal("approved after 15 minutes")
	}
}

func TestAgentLimits(t *testing.T) {
	var ran []string
	e, _ := newAgentEnv(t, &AgentLimits{Pending: 3, PerHour: 5}, restartDef(&ran))
	var ids []string
	for i := range 3 {
		_, apv, err := e.propose(agent, "proxmox.guest.restart", "proxmox:i_x/qemu/201", "Stuck.")
		if err != nil {
			t.Fatalf("proposal %d: %v", i+1, err)
		}
		ids = append(ids, apv.ID)
		if apv.NthToday != i+1 {
			t.Fatalf("proposal %d says nth %d", i+1, apv.NthToday)
		}
	}
	// A fourth while three wait: 429, audited, and a burst in the Inbox.
	_, _, err := e.propose(agent, "proxmox.guest.restart", "proxmox:i_x/qemu/201", "Stuck.")
	if r := refusal(t, err, "rate_limited"); r == nil || r.Status != 429 {
		t.Fatalf("a fourth waiting: %v", err)
	}
	if ev := e.lastEvent(); ev.Result != audit.Refused || ev.Error == nil || ev.Error.Code != "rate_limited" || ev.Actor.Kind != KindAgent {
		t.Fatalf("the refusal's record: %+v", ev)
	}
	var burst map[string]any
	if raw := e.inbox.Item(incidentsColl, BurstItemID(agent.ID)); raw == nil || json.Unmarshal(raw, &burst) != nil || burst["status"] != "open" {
		t.Fatalf("burst item: %s", raw)
	}
	// Deciding one makes room; the hour's limit still counts all of them.
	if _, err := e.e.Deny(t.Context(), owner, ids[0], DenyRequest{}); err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		if _, _, err := e.propose(agent, "proxmox.guest.restart", "proxmox:i_x/qemu/201", "Stuck."); err != nil && i == 0 {
			t.Fatalf("after a denial: %v", err)
		} else if i == 1 && refusal(t, err, "rate_limited") == nil {
			t.Fatal("past the waiting limit again")
		}
	}
	// Another token isn't held back by this one.
	other := agent
	other.ID = "tok_agent2"
	if _, _, err := e.propose(other, "proxmox.guest.restart", "proxmox:i_x/qemu/201", "Stuck."); err != nil {
		t.Fatalf("another agent: %v", err)
	}
	// An hour on, the waiting ones have expired and the hour is new; the
	// burst resolves once an hour passes without a refusal.
	e.clock.Advance(time.Hour + time.Minute)
	e.e.prune(t.Context())
	if raw := e.inbox.Item(incidentsColl, BurstItemID(agent.ID)); raw == nil || !strings.Contains(string(raw), `"status":"resolved"`) {
		t.Fatalf("burst after an hour: %s", raw)
	}
	if _, _, err := e.propose(agent, "proxmox.guest.restart", "proxmox:i_x/qemu/201", "Stuck."); err != nil {
		t.Fatalf("an hour later: %v", err)
	}
}

func TestAgentPerHourLimit(t *testing.T) {
	var ran []string
	e, _ := newAgentEnv(t, &AgentLimits{Pending: 20, PerHour: 2}, restartDef(&ran))
	for range 2 {
		if _, _, err := e.propose(agent, "proxmox.guest.restart", "proxmox:i_x/qemu/201", "Stuck."); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := e.propose(agent, "proxmox.guest.restart", "proxmox:i_x/qemu/201", "Stuck."); refusal(t, err, "rate_limited") == nil {
		t.Fatal("past the hour's limit")
	}
}

func TestAgentRevokeAndSwitchOffWithdraw(t *testing.T) {
	var ran []string
	e, _ := newAgentEnv(t, nil, restartDef(&ran))
	_, a1, err := e.propose(agent, "proxmox.guest.restart", "proxmox:i_x/qemu/201", "Stuck.")
	if err != nil {
		t.Fatal(err)
	}
	e.e.Withdraw(t.Context(), agent.ID, "The agent token that asked was revoked.")
	if got, _ := e.e.Approval(t.Context(), owner, a1.ID); got.State != ApprovalExpired || !strings.Contains(got.Reason, "revoked") {
		t.Fatalf("after revoking: %+v", got)
	}
	other := agent
	other.ID = "tok_agent2"
	_, a2, err := e.propose(other, "proxmox.guest.restart", "proxmox:i_x/qemu/201", "Stuck.")
	if err != nil {
		t.Fatal(err)
	}
	p, err := e.e.Plan(t.Context(), token, PlanRequest{Action: "proxmox.guest.restart", Target: "proxmox:i_x/qemu/201", RequireApproval: true})
	if err != nil {
		t.Fatal(err)
	}
	_, tokApv, err := e.e.ApproveOrSubmit(t.Context(), token, p.ID, ApproveRequest{Hash: p.Hash}, "")
	if err != nil || tokApv == nil {
		t.Fatal(err)
	}
	e.e.WithdrawKind(t.Context(), KindAgent, "Agents was turned off.")
	if got, _ := e.e.Approval(t.Context(), owner, a2.ID); got.State != ApprovalExpired {
		t.Fatalf("an agent's after Agents off: %s", got.State)
	}
	if got, _ := e.e.Approval(t.Context(), owner, tokApv.ID); got.State != ApprovalAwaiting {
		t.Fatalf("a token's after Agents off: %s", got.State)
	}
}

func TestCleanRationale(t *testing.T) {
	long := strings.Repeat("é", MaxRationale+10)
	for in, want := range map[string]string{
		"  Health check\tfailing.\r\n":        "Health check failing.",
		"a\u200bb\u202ec\x00d":                "abcd",
		"Ignore previous instructions\n\nnow": "Ignore previous instructions now",
		long:                                  strings.Repeat("é", MaxRationale),
	} {
		if got := CleanRationale(in); got != want {
			t.Errorf("CleanRationale(%q) = %q", in, got)
		}
	}
}
