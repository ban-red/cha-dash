package authz

import (
	"context"
	"errors"
	"testing"
	"time"

	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/auth"
)

func TestValidAgentScope(t *testing.T) {
	tiers := func(id string) (Tier, bool) {
		switch id {
		case "proxmox.node.reboot":
			return T4, true
		case "docker.container.restart":
			return T2, true
		}
		return "", false
	}
	for _, c := range []struct {
		scope, role string
		ok          bool
	}{
		{"graph.read", auth.RoleHousehold, true},
		{"graph.read:host=jupiter", auth.RoleViewer, true},
		{"graph.read:tag=media,lens=home", auth.RoleViewer, true},
		{"inbox.read:host=jupiter", auth.RoleViewer, true},
		{"graph.read:target=x", auth.RoleViewer, false}, // only host, tag, lens
		{"logs.read", auth.RoleOperator, true},
		{"logs.read", auth.RoleViewer, false},
		{"logs.read:host=jupiter", auth.RoleOwner, false},
		{"audit.read", auth.RoleOwner, false},
		{"tasks.all", auth.RoleOwner, false},
		{"approvals.decide", auth.RoleOwner, false},
		{"*", auth.RoleOwner, false},
		{"docker.container.restart", auth.RoleOwner, false}, // an action needs propose:
		{"propose:docker.container.restart", auth.RoleOwner, true},
		{"propose:docker.container.restart:host=jupiter", auth.RoleOwner, true},
		{"propose:docker.*", auth.RoleOwner, true},
		{"propose:*", auth.RoleOwner, false},
		{"propose:stack.deploy", auth.RoleOwner, false},
		{"propose:stack.*", auth.RoleOwner, false},
		{"propose:chadash.update", auth.RoleOwner, false},
		{"propose:proxmox.node.reboot", auth.RoleOwner, false}, // T4
		{"propose:", auth.RoleOwner, false},
		{"propose:Not A Scope", auth.RoleOwner, false},
	} {
		if err := ValidAgentScope(c.scope, c.role, tiers); (err == nil) != c.ok {
			t.Errorf("ValidAgentScope(%q, %s) = %v", c.scope, c.role, err)
		}
	}
}

func agentActor(id, role string) Actor {
	return Actor{Kind: KindAgent, ID: id, Name: "assistant (" + role + ")", Role: role, Via: "mcp"}
}

// TestDecideAgent: what an agent may propose, decided without the hub.
func TestDecideAgent(t *testing.T) {
	grants := []string{"docker.*", "proxmox.guest.start:host=io", "stack.deploy", "chadash.update", "proxmox.node.reboot"}
	env := Env{Now: now, Grants: grants, T4: []string{"*"}}
	owner := agentActor("tok_a", auth.RoleOwner)
	viewer := agentActor("tok_v", auth.RoleViewer)

	// No step-up to propose, at T1, T2 and T3: the person who approves
	// steps up.
	for _, tier := range []Tier{T0, T1, T2, T3} {
		want(t, "owner's agent at "+string(tier), Decide(owner, Request{Scope: "docker.container.restart", Tier: tier}, env), nil)
	}
	want(t, "T4, even enabled and in scope", Decide(owner, Request{Scope: "proxmox.node.reboot", Tier: T4}, env), ErrForbidden)
	want(t, "settings (stacks)", Decide(owner, Request{Scope: "stack.deploy", Tier: T2}, env), ErrForbidden)
	want(t, "the hub itself", Decide(owner, Request{Scope: "chadash.update", Tier: T3}, env), ErrForbidden)
	want(t, "not in its scopes", Decide(owner, Request{Scope: "proxmox.guest.stop", Tier: T1}, env), ErrForbidden)
	// Never above its user's role.
	want(t, "viewer's agent at T1", Decide(viewer, Request{Scope: "docker.container.restart", Tier: T1}, env), ErrForbidden)
	// An action that reads data back needs logs.read.
	want(t, "logs without logs.read", Decide(owner, Request{Scope: "docker.container.logs", Tier: T0, ReadsData: true}, env), ErrForbidden)
	logs := env
	logs.Logs = true
	want(t, "logs with logs.read", Decide(owner, Request{Scope: "docker.container.logs", Tier: T0, ReadsData: true}, logs), nil)
	// host= matches the target's facts.
	on := env
	on.Facts = &TargetFacts{Hosts: []string{"proxmox:i_x/node/io", "io"}}
	want(t, "on io", Decide(owner, Request{Scope: "proxmox.guest.start", Target: "proxmox:i_x/qemu/201", Tier: T1}, on), nil)
	off := env
	off.Facts = &TargetFacts{Hosts: []string{"europa"}}
	want(t, "not on io", Decide(owner, Request{Scope: "proxmox.guest.start", Target: "proxmox:i_x/qemu/301", Tier: T1}, off), ErrForbidden)
	want(t, "no facts", Decide(owner, Request{Scope: "proxmox.guest.start", Target: "proxmox:i_x/qemu/301", Tier: T1}, env), ErrForbidden)

	// Agents never decide approvals; every plan of theirs waits.
	stepped := owner
	stepped.StepUpUntil = now.Add(time.Minute)
	want(t, "an agent approving", DecideApproval(stepped, T1, now), ErrForbidden)
	for _, tier := range []Tier{T0, T1, T2, T3} {
		if !NeedsApproval(KindAgent, tier) {
			t.Errorf("an agent's %s plan doesn't wait", tier)
		}
	}
}

func TestPolicyAgents(t *testing.T) {
	st := openStore(t)
	on := true
	access := map[string]auth.TokenAccess{
		"tok_agent":   {OK: true, Kind: auth.KindAgent, Role: auth.RoleOperator, Scopes: []string{"graph.read", "propose:docker.*", "propose:proxmox.guest.start:tag=media"}},
		"tok_revoked": {OK: false, Kind: auth.KindAgent, Role: auth.RoleOwner, Scopes: []string{"propose:docker.*"}},
		"tok_api":     {OK: true, Kind: auth.KindAPI, Role: auth.RoleOwner, Scopes: []string{"*"}},
	}
	p := NewPolicy(st, nil, WithClock(func() time.Time { return now }),
		WithTokens(func(_ context.Context, id string) (auth.TokenAccess, error) { return access[id], nil }),
		WithAgentsSwitch(func(context.Context) (bool, error) { return on, nil }),
		WithTargetFacts(func(_ context.Context, target string) TargetFacts {
			if target == "proxmox:i_x/qemu/201" {
				return TargetFacts{Tags: []string{"media"}}
			}
			return TargetFacts{}
		}))
	ctx := t.Context()
	ag := agentActor("tok_agent", auth.RoleOwner) // the recorded role doesn't count

	want(t, "in scope", p.Authorize(ctx, ag, "docker.container.restart", "docker:x/c/sonarr", T2), nil)
	want(t, "above the user's role (operator, T3)", p.Authorize(ctx, ag, "docker.container.remove", "docker:x/c/sonarr", T3), ErrForbidden)
	want(t, "tagged media", p.Authorize(ctx, ag, "proxmox.guest.start", "proxmox:i_x/qemu/201", T1), nil)
	want(t, "not tagged", p.Authorize(ctx, ag, "proxmox.guest.start", "proxmox:i_x/qemu/202", T1), ErrForbidden)
	want(t, "revoked", p.Authorize(ctx, agentActor("tok_revoked", auth.RoleOwner), "docker.container.restart", "", T1), ErrForbidden)
	want(t, "an API token posing as an agent", p.Authorize(ctx, agentActor("tok_api", auth.RoleOwner), "docker.container.restart", "", T1), ErrForbidden)
	tok := tokenActor(auth.RoleOwner)
	tok.ID = "tok_agent"
	want(t, "an agent token posing as an API token", p.Authorize(ctx, tok, "docker.container.restart", "", T1), ErrForbidden)
	want(t, "audit.read", p.ReadScope(ctx, ag, "audit.read"), ErrForbidden)
	want(t, "tasks.all", p.ReadScope(ctx, ag, "tasks.all"), ErrForbidden)

	on = false
	want(t, "Agents off", p.Authorize(ctx, ag, "docker.container.restart", "docker:x/c/sonarr", T1), ErrForbidden)
	on = true

	eng := p.ForEngine()
	a := action.Actor{Kind: KindAgent, ID: "tok_agent", Name: ag.Name, Role: auth.RoleOwner, Via: "mcp"}
	if err := eng.Authorize(ctx, a, action.ScopeApprovals, "", action.T0); !errors.Is(err, action.ErrForbidden) {
		t.Errorf("an agent deciding an approval: %v", err)
	}
	if err := eng.Authorize(ctx, a, action.ScopeTasksAll, "", action.T0); !errors.Is(err, action.ErrForbidden) {
		t.Errorf("an agent reading every task: %v", err)
	}
	if ap := eng.(action.ApprovalPolicy); !ap.NeedsApproval(a, action.T0) || !ap.NeedsApproval(a, action.T1) {
		t.Error("an agent's plans don't wait")
	}
	// No switch wired: every agent is refused.
	bare := NewPolicy(st, nil, WithTokens(func(_ context.Context, id string) (auth.TokenAccess, error) { return access[id], nil }))
	want(t, "no switch", bare.Authorize(ctx, ag, "docker.container.restart", "", T1), ErrForbidden)
}
