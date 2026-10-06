package authz

import (
	"context"
	"errors"
	"testing"
	"time"

	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/auth"
)

func tokenActor(role string) Actor {
	return Actor{Kind: KindToken, ID: "tok_" + role, Name: "bot (" + role + ")", Role: role, Via: "api"}
}

func TestTokenRoute(t *testing.T) {
	for _, c := range []struct {
		name   string
		route  Route
		role   string
		scopes []string
		want   error
	}{
		{"public", Route{Access: Public}, auth.RoleViewer, nil, nil},
		{"session only", Route{Access: SignedIn}, auth.RoleOwner, []string{"*"}, ErrSessionOnly},
		{"node key", Route{Access: NodeKey, Token: TokenAny}, auth.RoleOwner, []string{"*"}, ErrSessionOnly},
		{"any token", Route{Access: SignedIn, Token: TokenAny}, auth.RoleHousehold, []string{"pihole.app.pause"}, nil},
		{"scoped, held", Route{Access: Viewer, Token: ScopeGraphRead}, auth.RoleViewer, []string{"graph.read"}, nil},
		{"scoped, by star", Route{Access: Viewer, Token: ScopeGraphRead}, auth.RoleViewer, []string{"*"}, nil},
		{"scoped, by pattern", Route{Access: Viewer, Token: ScopeGraphRead}, auth.RoleViewer, []string{"graph.*"}, nil},
		{"scoped, missing", Route{Access: Viewer, Token: ScopeGraphRead}, auth.RoleViewer, []string{"inbox.read", "docker.*"}, ErrForbidden},
		{"role ceiling", Route{Access: Admin, Token: ScopeAuditRead}, auth.RoleOperator, []string{"*"}, ErrForbidden},
	} {
		err := TokenRoute(c.route, c.role, c.scopes)
		want(t, c.name, err, c.want)
	}
}

func TestValidTokenScope(t *testing.T) {
	for _, c := range []struct {
		scope, role string
		ok          bool
	}{
		{"graph.read", auth.RoleHousehold, true},
		{"audit.read", auth.RoleViewer, false},
		{"audit.read", auth.RoleAdmin, true},
		{"tasks.all", auth.RoleOperator, false},
		{"*", auth.RoleViewer, true},
		{"proxmox.guest.*", auth.RoleOperator, true},
		{"proxmox.guest.reboot:host=pve-a", auth.RoleOperator, true},
		{"approvals.decide", auth.RoleOwner, false},
		{"Not a scope", auth.RoleOwner, false},
		{"", auth.RoleOwner, false},
	} {
		if err := ValidTokenScope(c.scope, c.role); (err == nil) != c.ok {
			t.Errorf("ValidTokenScope(%q, %s) = %v", c.scope, c.role, err)
		}
	}
}

func TestNeedsApprovalAndDecideApproval(t *testing.T) {
	for _, c := range []struct {
		kind string
		tier Tier
		want bool
	}{
		{KindUser, T4, false}, {KindToken, T0, false}, {KindToken, T1, false}, {KindToken, T2, true}, {KindAgent, T0, true},
	} {
		if got := NeedsApproval(c.kind, c.tier); got != c.want {
			t.Errorf("NeedsApproval(%s, %s) = %v", c.kind, c.tier, got)
		}
	}
	want(t, "admin, T2, stepped up", DecideApproval(stepped(user(auth.RoleAdmin), time.Second), T2, now), nil)
	want(t, "admin, deny (T0), no step-up", DecideApproval(user(auth.RoleAdmin), T0, now), nil)
	want(t, "admin, T2, no step-up", DecideApproval(user(auth.RoleAdmin), T2, now), ErrStepUpRequired)
	want(t, "operator", DecideApproval(stepped(user(auth.RoleOperator), time.Second), T2, now), ErrForbidden)
	tok := tokenActor(auth.RoleOwner)
	tok.StepUpUntil = now.Add(time.Minute)
	want(t, "a token", DecideApproval(tok, T0, now), ErrForbidden)
	noSession := user(auth.RoleOwner)
	noSession.Session = ""
	want(t, "no session", DecideApproval(noSession, T0, now), ErrForbidden)
}

func TestPolicyWithTokens(t *testing.T) {
	st := openStore(t)
	access := map[string]auth.TokenAccess{
		"tok_live":    {OK: true, Role: auth.RoleOperator, Scopes: []string{"docker.*", "audit.read"}},
		"tok_demoted": {OK: true, Role: auth.RoleViewer, Scopes: []string{"*"}},
		"tok_dead":    {OK: false, Role: auth.RoleOwner, Scopes: []string{"*"}},
		"tok_admin":   {OK: true, Role: auth.RoleAdmin, Scopes: []string{"*"}},
		"tok_narrow":  {OK: true, Role: auth.RoleAdmin, Scopes: []string{"graph.read"}},
	}
	p := NewPolicy(st, nil, WithClock(func() time.Time { return now }), WithTokens(func(_ context.Context, id string) (auth.TokenAccess, error) {
		if id == "tok_broken" {
			return auth.TokenAccess{}, errors.New("disk on fire")
		}
		return access[id], nil
	}))
	ctx := t.Context()
	act := func(id, role string) Actor { a := tokenActor(role); a.ID = id; return a }

	want(t, "live, in scope, T2", p.Authorize(ctx, act("tok_live", auth.RoleOperator), "docker.container.restart", "", T2), nil)
	want(t, "live, out of scope", p.Authorize(ctx, act("tok_live", auth.RoleOperator), "proxmox.guest.start", "", T1), ErrForbidden)
	// The user's current role counts, not the one recorded on the actor.
	want(t, "demoted since", p.Authorize(ctx, act("tok_demoted", auth.RoleOwner), "docker.container.start", "", T1), ErrForbidden)
	want(t, "revoked or expired", p.Authorize(ctx, act("tok_dead", auth.RoleOwner), "docker.container.start", "", T0), ErrForbidden)
	want(t, "unknown token", p.Authorize(ctx, act("tok_nope", auth.RoleOwner), "docker.container.start", "", T0), ErrForbidden)
	want(t, "unreadable", p.Authorize(ctx, act("tok_broken", auth.RoleOwner), "docker.container.start", "", T0), ErrForbidden)
	// T3 is never a token's, whatever its role and step-up.
	adm := act("tok_admin", auth.RoleAdmin)
	adm.StepUpUntil = now.Add(time.Minute)
	want(t, "token at T3", p.Authorize(ctx, adm, "docker.container.remove", "", T3), ErrForbidden)

	// Read scopes through the engine's seam: role and scope both.
	eng := p.ForEngine()
	engine := func(a Actor) action.Actor {
		return action.Actor{Kind: a.Kind, ID: a.ID, Name: a.Name, Role: a.Role, Via: a.Via}
	}
	if err := eng.Authorize(ctx, engine(act("tok_admin", auth.RoleAdmin)), action.ScopeAuditRead, "", action.T0); err != nil {
		t.Errorf("admin token with *: %v", err)
	}
	if err := eng.Authorize(ctx, engine(act("tok_narrow", auth.RoleAdmin)), action.ScopeAuditRead, "", action.T0); !errors.Is(err, action.ErrForbidden) {
		t.Errorf("admin token without audit.read: %v", err)
	}
	if err := eng.Authorize(ctx, engine(act("tok_live", auth.RoleOperator)), action.ScopeAuditRead, "", action.T0); !errors.Is(err, action.ErrForbidden) {
		t.Errorf("operator token with audit.read: %v", err)
	}
	if err := eng.Authorize(ctx, engine(act("tok_admin", auth.RoleAdmin)), action.ScopeApprovals, "", action.T0); !errors.Is(err, action.ErrForbidden) {
		t.Errorf("a token deciding an approval: %v", err)
	}
	ap, ok := eng.(action.ApprovalPolicy)
	if !ok || !ap.NeedsApproval(engine(adm), action.T2) || ap.NeedsApproval(engine(adm), action.T1) || ap.NeedsApproval(action.Actor{Kind: KindUser}, action.T4) {
		t.Error("NeedsApproval through the engine's seam")
	}
}
