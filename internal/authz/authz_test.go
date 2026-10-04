package authz

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"dash.cha.sh/internal/auth"
	"dash.cha.sh/internal/store"
)

// policy is the seam internal/action defines (08-v0.2 → "Go seam"). Policy
// must keep satisfying it; at the merge the real interface replaces this copy.
type policy interface {
	Authorize(ctx context.Context, a Actor, scope, target string, tier Tier) error
}

var _ policy = (*Policy)(nil)

var now = time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)

func user(role string) Actor {
	return Actor{Kind: KindUser, ID: "u_" + role, Name: role, Role: role, Via: "web", Session: "s_" + role}
}

func stepped(a Actor, ago time.Duration) Actor {
	a.StepUpUntil = now.Add(auth.StepUpTTL - ago)
	return a
}

func want(t *testing.T, name string, err, target error) {
	t.Helper()
	switch {
	case target == nil && err != nil:
		t.Errorf("%s: refused: %v", name, err)
	case target != nil && !errors.Is(err, target):
		t.Errorf("%s: got %v, want %v", name, err, target)
	case err != nil:
		var r *Refusal
		if !errors.As(err, &r) || r.Msg == "" {
			t.Errorf("%s: %v isn't a Refusal with a sentence", name, err)
		}
	}
}

func TestTierCaps(t *testing.T) {
	const scope = "docker.container.restart"
	// The role table: the highest tier each role may run (with a fresh step-up
	// where the tier asks for one, and T4 enabled for the scope).
	caps := map[string]Tier{
		auth.RoleOwner:     T4,
		auth.RoleAdmin:     T3,
		auth.RoleOperator:  T2,
		auth.RoleViewer:    T0,
		auth.RoleHousehold: T0,
	}
	env := Env{Now: now, T4: []string{scope}}
	for role, cap := range caps {
		for _, tier := range []Tier{T0, T1, T2, T3, T4} {
			a := stepped(user(role), time.Second)
			err := Decide(a, Request{Scope: scope, Tier: tier}, env)
			var target error
			if tier.rank() > cap.rank() {
				target = ErrForbidden
			}
			want(t, role+" "+string(tier), err, target)
		}
	}
}

func TestStepUp(t *testing.T) {
	const scope = "proxmox.guest.snapshot.delete"
	env := Env{Now: now, T4: []string{scope}}
	cases := []struct {
		name   string
		actor  Actor
		tier   Tier
		target error
	}{
		{"T2 needs no step-up", user(auth.RoleOperator), T2, nil},
		{"T3 without a grant", user(auth.RoleAdmin), T3, ErrStepUpRequired},
		{"T3 with a grant", stepped(user(auth.RoleAdmin), 4*time.Minute), T3, nil},
		{"T3 with an expired grant", stepped(user(auth.RoleAdmin), auth.StepUpTTL+time.Second), T3, ErrStepUpRequired},
		{"T3 grant ending now", stepped(user(auth.RoleAdmin), auth.StepUpTTL), T3, ErrStepUpRequired},
		{"T4 fresh", stepped(user(auth.RoleOwner), 10*time.Second), T4, nil},
		{"T4 with a grant that's valid but not fresh", stepped(user(auth.RoleOwner), 2*time.Minute), T4, ErrStepUpRequired},
		{"T4 without a grant", user(auth.RoleOwner), T4, ErrStepUpRequired},
		// No step-up can lift a role's cap: refused outright, not asked to confirm.
		{"operator T3 with a grant", stepped(user(auth.RoleOperator), time.Second), T3, ErrForbidden},
		{"operator T3 without a grant", user(auth.RoleOperator), T3, ErrForbidden},
		{"admin T4", stepped(user(auth.RoleAdmin), time.Second), T4, ErrForbidden},
	}
	for _, c := range cases {
		want(t, c.name, Decide(c.actor, Request{Scope: scope, Tier: c.tier}, env), c.target)
	}
}

func TestT4OffUnlessEnabled(t *testing.T) {
	owner := stepped(user(auth.RoleOwner), time.Second)
	reboot := Request{Scope: "proxmox.guest.reboot", Target: "proxmox:i_a/qemu/100", Tier: T4}
	want(t, "default empty", Decide(owner, reboot, Env{Now: now}), ErrForbidden)
	want(t, "another scope", Decide(owner, reboot, Env{Now: now, T4: []string{"docker.container.remove"}}), ErrForbidden)
	want(t, "enabled", Decide(owner, reboot, Env{Now: now, T4: []string{"proxmox.guest.reboot"}}), nil)
	want(t, "enabled by pattern", Decide(owner, reboot, Env{Now: now, T4: []string{"proxmox.guest.*"}}), nil)

	// Qualified: enabled for one host only.
	onHost := Env{Now: now, T4: []string{"proxmox.guest.reboot:host=pve-a"}}
	want(t, "unqualified request vs qualified enable", Decide(owner, reboot, onHost), ErrForbidden)
	onA := reboot
	onA.Scope = "proxmox.guest.reboot:host=pve-a"
	want(t, "that host", Decide(owner, onA, onHost), nil)
	other := reboot
	other.Scope = "proxmox.guest.reboot:host=pve-b"
	want(t, "another host", Decide(owner, other, onHost), ErrForbidden)
	// Or one target.
	byTarget := Env{Now: now, T4: []string{"proxmox.guest.reboot:target=proxmox:i_a/qemu/100"}}
	want(t, "that target", Decide(owner, reboot, byTarget), nil)
	reboot.Target = "proxmox:i_a/qemu/101"
	want(t, "another target", Decide(owner, reboot, byTarget), ErrForbidden)
}

func TestHousehold(t *testing.T) {
	h := user(auth.RoleHousehold)
	env := Env{Now: now}
	want(t, "read", Decide(h, Request{Scope: "pihole.app.status", Tier: T0}, env), nil)
	want(t, "unmarked T1", Decide(h, Request{Scope: "pihole.app.pause", Tier: T1}, env), ErrForbidden)
	want(t, "marked T1", Decide(h, Request{Scope: "pihole.app.pause", Tier: T1, Household: true}, env), nil)
	want(t, "marked T2", Decide(h, Request{Scope: "docker.container.restart", Tier: T2, Household: true}, env), ErrForbidden)
	// The mark only opens actions to household; it never lifts a viewer.
	want(t, "viewer, marked T1", Decide(user(auth.RoleViewer), Request{Scope: "pihole.app.pause", Tier: T1, Household: true}, env), ErrForbidden)
}

func TestGrantsAndTokens(t *testing.T) {
	op := user(auth.RoleOperator)
	restart := Request{Scope: "docker.container.restart:host=pve-a", Target: "docker:i_a/c/web", Tier: T2}
	want(t, "no narrowing", Decide(op, restart, Env{Now: now}), nil)
	want(t, "nothing granted", Decide(op, restart, Env{Now: now, Grants: []string{}}), ErrForbidden)
	want(t, "granted", Decide(op, restart, Env{Now: now, Grants: []string{"docker.container.*:host=pve-a"}}), nil)
	want(t, "granted elsewhere", Decide(op, restart, Env{Now: now, Grants: []string{"docker.container.restart:host=pve-b"}}), ErrForbidden)
	want(t, "a grant never lifts the role", Decide(op, Request{Scope: "docker.container.remove", Tier: T3}, Env{Now: now, Grants: []string{"*"}}), ErrForbidden)

	tok := stepped(user(auth.RoleOwner), time.Second)
	tok.Kind = KindToken
	want(t, "token T2", Decide(tok, Request{Scope: "stack.deploy", Tier: T2}, Env{Now: now}), nil)
	want(t, "token T3, even with a grant on the actor", Decide(tok, Request{Scope: "docker.container.remove", Tier: T3}, Env{Now: now}), ErrForbidden)
}

func TestBadInputsRefuse(t *testing.T) {
	env := Env{Now: now}
	owner := stepped(user(auth.RoleOwner), time.Second)
	want(t, "unknown tier", Decide(owner, Request{Scope: "a.b", Tier: "T9"}, env), ErrForbidden)
	want(t, "empty tier", Decide(owner, Request{Scope: "a.b"}, env), ErrForbidden)
	want(t, "pattern as a request", Decide(owner, Request{Scope: "docker.*", Tier: T0}, env), ErrForbidden)
	want(t, "bad scope", Decide(owner, Request{Scope: "Docker Restart", Tier: T0}, env), ErrForbidden)
	want(t, "no role", Decide(Actor{Kind: KindUser}, Request{Scope: "a.b", Tier: T0}, env), ErrForbidden)
	want(t, "made-up role", Decide(user("root"), Request{Scope: "a.b", Tier: T0}, env), ErrForbidden)
}

func TestParseScope(t *testing.T) {
	good := map[string]Scope{
		"docker.container.restart":            {Action: "docker.container.restart"},
		"proxmox.guest.reboot:host=pve-a":     {Action: "proxmox.guest.reboot", Quals: map[string]string{"host": "pve-a"}},
		"stack.deploy:host=pve-a,stack=media": {Action: "stack.deploy", Quals: map[string]string{"host": "pve-a", "stack": "media"}},
		"proxmox.*":                           {Action: "proxmox.*"},
		"*":                                   {Action: "*"},
		"x.y:target=proxmox:i_a/qemu/100":     {Action: "x.y", Quals: map[string]string{"target": "proxmox:i_a/qemu/100"}},
		"settings.security.update":            {Action: "settings.security.update"},
		"users.transfer-ownership":            {Action: "users.transfer-ownership"},
		"qbittorrent.app.pause:instance=i_qb": {Action: "qbittorrent.app.pause", Quals: map[string]string{"instance": "i_qb"}},
	}
	for s, w := range good {
		got, err := ParseScope(s)
		if err != nil || got.Action != w.Action || len(got.Quals) != len(w.Quals) {
			t.Errorf("ParseScope(%q) = %+v, %v", s, got, err)
			continue
		}
		for k, v := range w.Quals {
			if got.Quals[k] != v {
				t.Errorf("ParseScope(%q)[%s] = %q", s, k, got.Quals[k])
			}
		}
	}
	for _, s := range []string{"", "Docker", "a..b", "a.b:", "a.b:host", "a.b:host=", "a.b:host=pve-a,host=pve-a", "a.b:Host=io", "a*.b", ".a", "a.b:host=i=o"} {
		if _, err := ParseScope(s); err == nil {
			t.Errorf("ParseScope(%q) accepted", s)
		}
	}
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "chadash.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestPolicy(t *testing.T) {
	st := openStore(t)
	p := NewPolicy(st, nil, WithClock(func() time.Time { return now }))
	ctx := t.Context()
	owner := stepped(user(auth.RoleOwner), time.Second)

	want(t, "T4 before any setting", p.Authorize(ctx, owner, "proxmox.guest.reboot", "", T4), ErrForbidden)
	set := func(v string) {
		t.Helper()
		if _, err := st.Writer().ExecContext(ctx, `INSERT INTO settings (key, value, updated_at) VALUES (?, ?, 0)
			ON CONFLICT (key) DO UPDATE SET value = excluded.value`, T4SettingsKey, v); err != nil {
			t.Fatal(err)
		}
	}
	set(`["proxmox.guest.reboot:host=pve-a"]`)
	want(t, "T4 enabled for that host", p.Authorize(ctx, owner, "proxmox.guest.reboot:host=pve-a", "", T4), nil)
	want(t, "T4 elsewhere", p.Authorize(ctx, owner, "proxmox.guest.reboot:host=pve-b", "", T4), ErrForbidden)
	set(`not json`)
	want(t, "an unreadable setting refuses", p.Authorize(ctx, owner, "proxmox.guest.reboot:host=pve-a", "", T4), ErrForbidden)

	h := user(auth.RoleHousehold)
	want(t, "household, unmarked", p.Authorize(ctx, h, "pihole.app.pause", "", T1), ErrForbidden)
	want(t, "household, marked", p.Authorize(WithHouseholdAction(ctx), h, "pihole.app.pause", "", T1), nil)

	narrowed := NewPolicy(st, nil, WithClock(func() time.Time { return now }), WithGrants(func(_ context.Context, a Actor) ([]string, error) {
		if a.Kind == KindToken {
			return []string{"docker.container.restart"}, nil
		}
		return nil, nil
	}))
	tok := user(auth.RoleAdmin)
	tok.Kind = KindToken
	want(t, "token, granted", narrowed.Authorize(ctx, tok, "docker.container.restart", "", T2), nil)
	want(t, "token, not granted", narrowed.Authorize(ctx, tok, "docker.container.stop", "", T1), ErrForbidden)
	want(t, "user, not narrowed", narrowed.Authorize(ctx, user(auth.RoleAdmin), "docker.container.stop", "", T1), nil)
	failing := NewPolicy(st, nil, WithGrants(func(context.Context, Actor) ([]string, error) { return nil, errors.New("disk on fire") }))
	want(t, "grants unreadable", failing.Authorize(ctx, tok, "docker.container.restart", "", T0), ErrForbidden)
}

func TestAccess(t *testing.T) {
	roles := []string{auth.RoleHousehold, auth.RoleViewer, auth.RoleOperator, auth.RoleAdmin, auth.RoleOwner}
	for i, a := range []Access{SignedIn, Viewer, Operator, Admin, Owner} {
		for j, role := range roles {
			if got := a.Allows(role); got != (j >= i) {
				t.Errorf("%s allows %s = %v", a, role, got)
			}
		}
		if a.Allows("") || a.Allows("root") {
			t.Errorf("%s allows an unknown role", a)
		}
	}
}
