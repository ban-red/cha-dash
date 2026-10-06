package action

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"dash.cha.sh/internal/audit"
	"dash.cha.sh/internal/auth"
	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/realtime"
	"dash.cha.sh/internal/store"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type identityKey struct{}

// withIdentity is what auth.Require does, for tests: Sessions.Lookup reads it.
func withIdentity(ctx context.Context, id auth.Identity) context.Context {
	return context.WithValue(ctx, identityKey{}, id)
}

func lookup(ctx context.Context) (auth.Identity, bool) {
	id, ok := ctx.Value(identityKey{}).(auth.Identity)
	return id, ok
}

type env struct {
	t      *testing.T
	st     *store.Store
	g      *graph.Graph
	reg    *Registry
	audit  *audit.Log
	clock  *clock
	policy Policy
	e      *Engine
	// consumed lists the sessions whose step-up grant a T4 spent.
	consumed []string
}

var (
	owner = Actor{Kind: KindUser, ID: "u_owner", Name: "sam", Role: "owner", Via: "web", Session: "s_1", IP: "192.0.2.10"}
	other = Actor{Kind: KindUser, ID: "u_kim", Name: "kim", Role: "operator", Via: "web", Session: "s_2"}
)

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

// newEnv builds an engine over a small graph: io (a host) runs win-gaming
// (a VM), which runs plex.
func newEnv(t *testing.T, policy Policy, defs ...Def) *env {
	t.Helper()
	e := &env{t: t, st: openStore(t), g: graph.New(), reg: NewRegistry(), clock: &clock{t: time.Date(2026, 10, 4, 14, 5, 11, 0, time.UTC)}, policy: policy}
	if p, ok := policy.(OwnerOnly); ok && p.Now == nil {
		p.Now = e.clock.Now
		e.policy = p
	}
	e.audit = audit.New(e.st)
	e.audit.SetClock(e.clock.Now)
	if _, err := e.g.Update(func(tx *graph.Tx) error {
		for _, r := range []graph.Resource{
			{ID: "proxmox:i_x/node/io", Kind: "host", Name: "io", State: "running"},
			{ID: "proxmox:i_x/qemu/201", Kind: "vm", Name: "win-gaming", State: "running"},
			{ID: "app:plex", Kind: "app", Name: "Plex", State: "running"},
		} {
			if err := tx.Upsert(r); err != nil {
				return err
			}
		}
		if err := tx.Relate(graph.Relation{From: "proxmox:i_x/qemu/201", To: "proxmox:i_x/node/io", Type: "runs_on"}); err != nil {
			return err
		}
		return tx.Relate(graph.Relation{From: "app:plex", To: "proxmox:i_x/qemu/201", Type: "runs_on"})
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.reg.Register("test", defs...); err != nil {
		t.Fatal(err)
	}
	e.start()
	return e
}

func (e *env) start() {
	e.t.Helper()
	eng, err := New(e.t.Context(), Options{Store: e.st, Audit: e.audit, Registry: e.reg, Policy: e.policy, Graph: e.g,
		Sessions: Sessions{Lookup: lookup}, Now: e.clock.Now,
		ConsumeStepUp: func(_ context.Context, session string) error { e.consumed = append(e.consumed, session); return nil }})
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(eng.Close)
	e.e = eng
}

func (e *env) plan(a Actor, action, target string, params string) (*Plan, error) {
	e.t.Helper()
	var raw json.RawMessage
	if params != "" {
		raw = json.RawMessage(params)
	}
	return e.e.Plan(e.t.Context(), a, PlanRequest{Action: action, Target: target, Params: raw})
}

func (e *env) mustPlan(a Actor, action, target string) *Plan {
	e.t.Helper()
	p, err := e.plan(a, action, target, "")
	if err != nil {
		e.t.Fatalf("plan %s on %s: %v", action, target, err)
	}
	return p
}

func (e *env) approve(a Actor, p *Plan, typed, key string) (Task, error) {
	e.t.Helper()
	return e.e.Approve(e.t.Context(), a, p.ID, ApproveRequest{Hash: p.Hash, Typed: typed}, key)
}

// wait waits for a task to finish.
func (e *env) wait(id string) Task {
	e.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		t, err := e.e.task(e.t.Context(), id)
		if err != nil {
			e.t.Fatal(err)
		}
		if t.Finished() && t.Audit != nil {
			return t
		}
		time.Sleep(2 * time.Millisecond)
	}
	e.t.Fatalf("task %s didn't finish", id)
	return Task{}
}

// events returns the audit log, oldest first.
func (e *env) events() []audit.Event {
	e.t.Helper()
	p, err := e.audit.Query(e.t.Context(), audit.Filter{Limit: audit.MaxLimit})
	if err != nil {
		e.t.Fatal(err)
	}
	out := make([]audit.Event, len(p.Events))
	for i, ev := range p.Events {
		out[len(out)-1-i] = ev
	}
	return out
}

func (e *env) lastEvent() audit.Event {
	e.t.Helper()
	evs := e.events()
	if len(evs) == 0 {
		e.t.Fatal("no audit events")
	}
	return evs[len(evs)-1]
}

func refusal(t *testing.T, err error, code string) *Refusal {
	t.Helper()
	var r *Refusal
	if !errors.As(err, &r) || r.Code != code {
		t.Fatalf("got %v, want a %s refusal", err, code)
	}
	return r
}

// reboot is a Proxmox-like action: T2, verified by the guest's state.
func reboot(calls *[]string) Def {
	return Def{
		ID: "proxmox.guest.reboot", Title: "Reboot", Targets: []string{"vm"}, Tier: T2,
		Steps: func(r Request) []Step {
			return []Step{{Label: "Ask io to reboot " + r.Target.Name}, {Label: "Wait for it to report running"}}
		},
		Execute: func(_ context.Context, r Request, run *TaskRun) error {
			run.Step("Ask io to reboot " + r.Target.Name)
			run.Progress(0.5)
			run.Logf("rebooting %s", r.Target.Name)
			*calls = append(*calls, "execute:"+r.Target.ID)
			return nil
		},
		Verify: func(_ context.Context, r Request, run *TaskRun) error {
			run.Step("Wait for it to report running")
			*calls = append(*calls, "verify")
			return nil
		},
		Undo: func(Request) *Undo { return nil },
	}
}

func TestTier(t *testing.T) {
	for i, s := range []string{"T0", "T1", "T2", "T3", "T4"} {
		tier, err := ParseTier(s)
		if err != nil || tier != Tier(i) || tier.String() != s {
			t.Fatalf("%s: %v %v", s, tier, err)
		}
		b, _ := json.Marshal(tier)
		if string(b) != `"`+s+`"` {
			t.Fatalf("marshal %s = %s", s, b)
		}
	}
	for _, s := range []string{"", "T5", "t1", "T", "T10"} {
		if _, err := ParseTier(s); err == nil {
			t.Errorf("ParseTier(%q) accepted", s)
		}
	}
}

func TestRegistry(t *testing.T) {
	exec := func(context.Context, Request, *TaskRun) error { return nil }
	r := NewRegistry()
	for _, bad := range [][]Def{
		{{ID: "reboot", Tier: T1, Execute: exec}},
		{{ID: "Proxmox.Guest.Reboot", Tier: T1, Execute: exec}},
		{{ID: "a.b", Tier: T1}},
		{{ID: "a.b", Tier: 9, Execute: exec}},
		{{ID: "a.b", Tier: T1, Execute: exec}, {ID: "a.b", Tier: T1, Execute: exec}},
	} {
		if err := r.Register("x", bad...); err == nil {
			t.Errorf("Register(%+v) accepted", bad)
		}
	}
	if err := r.Register("one", Def{ID: "a.b", Execute: exec}, Def{ID: "a.c", Execute: exec}); err != nil {
		t.Fatal(err)
	}
	if err := r.Register("two", Def{ID: "a.b", Execute: exec}); err == nil {
		t.Fatal("an ID was claimed by a second source")
	}
	// Registering a source again replaces its actions.
	if err := r.Register("one", Def{ID: "a.d", Execute: exec}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(r.IDs(), ","); got != "a.d" {
		t.Fatalf("IDs = %s", got)
	}
	r.Unregister("one")
	if _, ok := r.Get("a.d"); ok || len(r.IDs()) != 0 {
		t.Fatal("Unregister left actions behind")
	}
}

func TestOwnerOnly(t *testing.T) {
	now := time.Date(2026, 10, 4, 14, 0, 0, 0, time.UTC)
	granted := owner
	granted.StepUpUntil = now.Add(time.Minute)
	lapsed := owner
	lapsed.StepUpUntil = now.Add(-time.Second)
	viewer := owner
	viewer.Role = "viewer"
	token := owner
	token.Kind = "token"
	ctx := t.Context()
	for _, c := range []struct {
		p    OwnerOnly
		a    Actor
		tier Tier
		want error
	}{
		{OwnerOnly{}, owner, T0, nil},
		{OwnerOnly{}, owner, T2, nil},
		{OwnerOnly{}, owner, T3, ErrStepUpRequired},
		{OwnerOnly{}, lapsed, T3, ErrStepUpRequired},
		{OwnerOnly{}, granted, T3, nil},
		{OwnerOnly{}, granted, T4, ErrForbidden},
		{OwnerOnly{AllowT4: true}, granted, T4, nil},
		{OwnerOnly{AllowT4: true}, owner, T4, ErrStepUpRequired},
		{OwnerOnly{}, viewer, T0, ErrForbidden},
		{OwnerOnly{}, token, T0, ErrForbidden},
	} {
		c.p.Now = func() time.Time { return now }
		if got := c.p.Authorize(ctx, c.a, "x.y", "", c.tier); !errors.Is(got, c.want) && got != c.want {
			t.Errorf("%+v %s %s: %v, want %v", c.p, c.a.Role, c.tier, got, c.want)
		}
	}
}

func TestPlanApproveRun(t *testing.T) {
	var calls []string
	e := newEnv(t, OwnerOnly{}, reboot(&calls))
	p := e.mustPlan(owner, "proxmox.guest.reboot", "proxmox:i_x/qemu/201")

	if !strings.HasPrefix(p.ID, "p_") || !strings.HasPrefix(p.Hash, "sha256:") || p.Label != "Reboot win-gaming" || p.Tier != T2 ||
		p.Confirm != "hold" || p.ConfirmText != "" || p.StepUp != "none" || p.Undo != nil || string(p.Params) != "{}" ||
		!p.ExpiresAt.Equal(e.clock.Now().Add(PlanTTL)) {
		t.Fatalf("plan = %+v", p)
	}
	if len(p.Steps) != 2 || p.Steps[1].Label != "Wait for it to report running" {
		t.Fatalf("steps = %+v", p.Steps)
	}
	if len(p.Impact) != 1 || p.Impact[0] != (Impact{Resource: "app:plex", Via: "runs_on", Depth: 1}) {
		t.Fatalf("impact = %+v", p.Impact)
	}
	// The JSON is the API's shape: confirm_text left out below T3, undo null.
	b, _ := json.Marshal(p)
	for _, want := range []string{`"tier":"T2"`, `"confirm":"hold"`, `"step_up":"none"`, `"undo":null`, `"expires_at":"2026-10-04T14:07:11Z"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("plan JSON lacks %s: %s", want, b)
		}
	}
	if strings.Contains(string(b), "confirm_text") {
		t.Errorf("confirm_text below T3: %s", b)
	}

	// Approval needs the same session and the hash that was shown.
	stranger := owner
	stranger.Session = "s_9"
	if _, err := e.approve(stranger, p, "", ""); err != nil {
		refusal(t, err, "forbidden")
	} else {
		t.Fatal("another session approved the plan")
	}
	if _, err := e.e.Approve(t.Context(), owner, p.ID, ApproveRequest{Hash: "sha256:0"}, ""); err == nil {
		t.Fatal("a wrong hash was accepted")
	} else {
		refusal(t, err, "plan_changed")
	}

	task, err := e.approve(owner, p, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(task.ID, "t_") || task.Plan != p.ID || task.State != StateQueued || task.Label != "Reboot win-gaming" ||
		task.Actor != (TaskActor{Kind: "user", Name: "sam"}) || task.Audit != nil {
		t.Fatalf("task = %+v", task)
	}
	done := e.wait(task.ID)
	if done.State != StateSucceeded || done.Step != "Wait for it to report running" || done.Progress == nil || *done.Progress != 0.5 ||
		len(done.Log) != 1 || done.StartedAt == nil || done.FinishedAt == nil || done.Error != nil {
		t.Fatalf("finished task = %+v", done)
	}
	if strings.Join(calls, ",") != "execute:proxmox:i_x/qemu/201,verify" {
		t.Fatalf("calls = %v", calls)
	}
	ev := e.lastEvent()
	if ev.ID != *done.Audit || ev.Result != audit.OK || ev.Task != task.ID || ev.Action != "proxmox.guest.reboot" || ev.Tier != "T2" ||
		ev.Actor.Session != "s_1" || ev.Actor.IP != "192.0.2.10" {
		t.Fatalf("audit = %+v", ev)
	}

	// A plan runs once.
	if _, err := e.approve(owner, p, "", ""); err == nil {
		t.Fatal("a plan ran twice")
	} else {
		refusal(t, err, "plan_expired")
	}
	// The task survives in the table: a fresh engine reads it back.
	got, err := e.e.Task(t.Context(), owner, task.ID)
	if err != nil || got.State != StateSucceeded {
		t.Fatalf("Task = %+v, %v", got, err)
	}
	if _, err := e.e.Task(t.Context(), owner, "t_nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown task: %v", err)
	}
}

func TestApproveRefusals(t *testing.T) {
	var calls []string
	e := newEnv(t, OwnerOnly{}, reboot(&calls))
	const vm = "proxmox:i_x/qemu/201"

	// Expired.
	p := e.mustPlan(owner, "proxmox.guest.reboot", vm)
	e.clock.Advance(PlanTTL)
	_, err := e.approve(owner, p, "", "")
	refusal(t, err, "plan_expired")

	// The target changed since planning.
	p = e.mustPlan(owner, "proxmox.guest.reboot", vm)
	e.g.Update(func(tx *graph.Tx) error {
		return tx.Upsert(graph.Resource{ID: vm, Kind: "vm", Name: "win-gaming", State: "stopped"})
	})
	_, err = e.approve(owner, p, "", "")
	refusal(t, err, "plan_changed")
	// Health moving doesn't count as a change.
	p = e.mustPlan(owner, "proxmox.guest.reboot", vm)
	e.g.Update(func(tx *graph.Tx) error {
		return tx.Upsert(graph.Resource{ID: vm, Kind: "vm", Name: "win-gaming", State: "stopped", Health: graph.HealthAttention})
	})
	if _, err := e.approve(owner, p, "", ""); err != nil {
		t.Fatalf("a health change refused the plan: %v", err)
	}

	// Unknown plan.
	_, err = e.e.Approve(t.Context(), owner, "p_nope", ApproveRequest{Hash: "x"}, "")
	refusal(t, err, "not_found")

	// Every refusal is audited.
	refused := 0
	for _, ev := range e.events() {
		if ev.Result == audit.Refused {
			refused++
		}
	}
	if refused != 3 {
		t.Fatalf("%d refusals audited, want 3", refused)
	}
}

func TestPlanRefusals(t *testing.T) {
	var calls []string
	pre := Def{ID: "proxmox.guest.start", Title: "Start", Targets: []string{"vm"}, Tier: T1,
		Preconditions: func(_ context.Context, r Request) error {
			return Precondition(r.Target.Name + " is already running.")
		},
		Execute: func(context.Context, Request, *TaskRun) error { return nil }}
	unavail := Def{ID: "proxmox.guest.snapshot.create", Targets: []string{"vm"}, Tier: T1,
		Preconditions: func(context.Context, Request) error {
			return Unavailable("Add a control token to let Cha Dash snapshot guests on io.", "i_x")
		},
		Execute: func(context.Context, Request, *TaskRun) error { return nil }}
	withParams := Def{ID: "users.create", Tier: T1, Params: fakeSchema{}, Resolve: func(_ context.Context, id string) (Target, error) {
		return Target{ID: id, Name: id}, nil
	}, Execute: func(context.Context, Request, *TaskRun) error { return nil }}
	e := newEnv(t, OwnerOnly{}, reboot(&calls), pre, unavail, withParams)
	const vm = "proxmox:i_x/qemu/201"

	_, err := e.plan(owner, "nope.nope", vm, "")
	refusal(t, err, "not_found")
	if ev := e.lastEvent(); ev.Result != audit.Refused || ev.Action != "nope.nope" || ev.Error.Code != "not_found" {
		t.Fatalf("audit = %+v", ev)
	}
	_, err = e.plan(owner, "proxmox.guest.reboot", "proxmox:i_x/qemu/999", "")
	refusal(t, err, "not_found")
	// The wrong kind of target is as unknown as a missing one.
	_, err = e.plan(owner, "proxmox.guest.reboot", "app:plex", "")
	refusal(t, err, "not_found")
	_, err = e.plan(other, "proxmox.guest.reboot", vm, "")
	refusal(t, err, "forbidden")
	if ev := e.lastEvent(); ev.Actor.Name != "kim" || ev.Tier != "T2" || ev.Error.Code != "forbidden" {
		t.Fatalf("audit = %+v", ev)
	}
	r := refusal(t, func() error { _, err := e.plan(owner, "proxmox.guest.start", vm, ""); return err }(), "precondition")
	if r.Status != 409 || r.Message != "win-gaming is already running." {
		t.Fatalf("precondition = %+v", r)
	}
	r = refusal(t, func() error { _, err := e.plan(owner, "proxmox.guest.snapshot.create", vm, ""); return err }(), "unavailable")
	if r.Status != 409 || r.Recipe != "i_x" {
		t.Fatalf("unavailable = %+v", r)
	}
	_, err = e.plan(owner, "proxmox.guest.reboot", vm, `{"force":true}`)
	refusal(t, err, "invalid")
	r = refusal(t, func() error { _, err := e.plan(owner, "users.create", "users", `{"username":""}`); return err }(), "invalid")
	if r.Status != 422 || len(r.Fields) != 1 || r.Fields[0].Path != "/username" {
		t.Fatalf("invalid = %+v", r)
	}
	_, err = e.plan(owner, "users.create", "users", `[1,2]`)
	refusal(t, err, "invalid")

	// Secret params: redacted in the plan and the audit log, kept for the run.
	p, err := e.plan(owner, "users.create", "users", `{"username":"kim","password":"FAKE-secret-value"}`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(p.Params), "FAKE-secret") || !strings.Contains(string(p.Params), `"password":"[redacted]"`) {
		t.Fatalf("plan params = %s", p.Params)
	}
	_, err = e.plan(owner, "users.create", "users", `{"username":"","password":"FAKE-secret-value"}`)
	refusal(t, err, "invalid")
	for _, ev := range e.events() {
		if strings.Contains(string(ev.Params), "FAKE-secret") {
			t.Fatalf("a secret reached the audit log: %+v", ev)
		}
	}
}

// fakeSchema takes {"username" (non-empty), "password" (secret)}.
type fakeSchema struct{}

func (fakeSchema) MarshalJSON() ([]byte, error) { return []byte(`{"type":"object"}`), nil }
func (fakeSchema) Secrets() []string            { return []string{"password"} }
func (fakeSchema) Validate(p map[string]any) []FieldError {
	var errs []FieldError
	for k := range p {
		if k != "username" && k != "password" {
			errs = append(errs, FieldError{Path: "/" + k, Message: "Unknown param " + k + "."})
		}
	}
	if u, _ := p["username"].(string); u == "" {
		errs = append(errs, FieldError{Path: "/username", Message: "A username is required."})
	}
	return errs
}

func TestTypeToConfirmAndStepUp(t *testing.T) {
	remove := Def{ID: "proxmox.guest.delete", Title: "Delete", Targets: []string{"vm"}, Tier: T3,
		Execute: func(context.Context, Request, *TaskRun) error { return nil }}
	rebootHost := Def{ID: "proxmox.host.reboot", Title: "Reboot", Targets: []string{"host"}, Tier: T2,
		// The host running the hub is T4.
		TierFor: func(t Target) Tier {
			if t.Name == "io" {
				return T4
			}
			return T2
		},
		Execute: func(context.Context, Request, *TaskRun) error { return nil }}
	e := newEnv(t, OwnerOnly{AllowT4: true}, remove, rebootHost)
	const vm = "proxmox:i_x/qemu/201"

	p := e.mustPlan(owner, "proxmox.guest.delete", vm)
	if p.Confirm != "type" || p.ConfirmText != "win-gaming" || p.StepUp != "required" {
		t.Fatalf("T3 plan = %+v", p)
	}
	_, err := e.approve(owner, p, "win-gaming", "")
	refusal(t, err, "step_up_required")

	stepped := owner
	stepped.StepUpUntil = e.clock.Now().Add(StepUpTTL)
	p = e.mustPlan(stepped, "proxmox.guest.delete", vm)
	if p.StepUp != "none" {
		t.Fatalf("a valid grant still asked for step-up: %+v", p)
	}
	_, err = e.approve(stepped, p, "win-gamin", "")
	refusal(t, err, "confirm_mismatch")
	task, err := e.approve(stepped, p, " win-gaming ", "")
	if err != nil {
		t.Fatal(err)
	}
	e.wait(task.ID)

	// T4 always asks again: a grant from before the plan doesn't do.
	p = e.mustPlan(stepped, "proxmox.host.reboot", "proxmox:i_x/node/io")
	if p.Tier != T4 || p.StepUp != "fresh" || p.Confirm != "type" || p.ConfirmText != "io" {
		t.Fatalf("T4 plan = %+v", p)
	}
	e.clock.Advance(time.Second)
	_, err = e.approve(stepped, p, "io", "")
	refusal(t, err, "step_up_required")
	fresh := owner
	fresh.StepUpUntil = e.clock.Now().Add(StepUpTTL)
	task, err = e.approve(fresh, p, "io", "")
	if err != nil {
		t.Fatal(err)
	}
	e.wait(task.ID)
	if got := e.lastEvent(); got.Tier != "T4" {
		t.Fatalf("audited at %s, want the raised tier", got.Tier)
	}
	// The T4 spent the grant; the T3 before it didn't.
	if !slices.Equal(e.consumed, []string{owner.Session}) {
		t.Fatalf("consumed step-up grants = %v", e.consumed)
	}

	// With T4 off, it's forbidden outright.
	e2 := newEnv(t, OwnerOnly{}, rebootHost)
	_, err = e2.plan(fresh, "proxmox.host.reboot", "proxmox:i_x/node/io", "")
	refusal(t, err, "forbidden")
}

func TestLocksAndIdempotency(t *testing.T) {
	release := make(chan struct{})
	slow := Def{ID: "proxmox.guest.backup", Title: "Back up", Targets: []string{"vm"}, Tier: T1,
		Execute: func(ctx context.Context, _ Request, _ *TaskRun) error {
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}}
	e := newEnv(t, OwnerOnly{}, slow)
	const vm = "proxmox:i_x/qemu/201"

	p1 := e.mustPlan(owner, "proxmox.guest.backup", vm)
	t1, err := e.approve(owner, p1, "", "key-1")
	if err != nil {
		t.Fatal(err)
	}
	// Retrying with the same key gives the same task, not a second run.
	again, err := e.approve(owner, p1, "", "key-1")
	if err != nil || again.ID != t1.ID {
		t.Fatalf("retry = %+v, %v", again, err)
	}
	p2 := e.mustPlan(owner, "proxmox.guest.backup", vm)
	_, err = e.approve(owner, p2, "", "key-1")
	refusal(t, err, "invalid")
	_, err = e.approve(owner, p2, "", "")
	refusal(t, err, "locked")
	close(release)
	if done := e.wait(t1.ID); done.State != StateSucceeded {
		t.Fatalf("task = %+v", done)
	}
	// The lock is gone with the task; p2's plan is still good.
	t2, err := e.approve(owner, p2, "", "")
	if err != nil {
		t.Fatal(err)
	}
	e.wait(t2.ID)
}

func TestFailures(t *testing.T) {
	block := make(chan struct{})
	defs := []Def{
		{ID: "x.fail", Tier: T1, Execute: func(context.Context, Request, *TaskRun) error {
			return Fail("upstream", "io said no.")
		}},
		{ID: "x.plain", Tier: T1, Execute: func(context.Context, Request, *TaskRun) error { return errors.New("connection refused") }},
		{ID: "x.verify", Tier: T1, Execute: func(context.Context, Request, *TaskRun) error { return nil },
			Verify: func(context.Context, Request, *TaskRun) error { return errors.New("the guest reports stopped") }},
		{ID: "x.timeout", Tier: T1, Timeout: 20 * time.Millisecond, Execute: func(ctx context.Context, _ Request, _ *TaskRun) error {
			<-ctx.Done()
			return ctx.Err()
		}},
		{ID: "x.panic", Tier: T1, Execute: func(context.Context, Request, *TaskRun) error { panic("boom") }},
		{ID: "x.block", Tier: T1, Cancellable: true, Execute: func(ctx context.Context, _ Request, _ *TaskRun) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-block:
				return nil
			}
		}},
	}
	for i := range defs {
		defs[i].Targets = []string{"app"}
	}
	e := newEnv(t, OwnerOnly{}, defs...)
	run := func(id string) Task {
		t.Helper()
		task, err := e.approve(owner, e.mustPlan(owner, id, "app:plex"), "", "")
		if err != nil {
			t.Fatal(err)
		}
		return e.wait(task.ID)
	}
	for _, c := range []struct{ id, state, code, msg string }{
		{"x.fail", StateFailed, "upstream", "io said no."},
		{"x.plain", StateFailed, "failed", "connection refused"},
		{"x.verify", StateFailed, "verify_failed", "It ran, but the check afterwards failed: the guest reports stopped"},
		{"x.timeout", StateFailed, "timeout", "It didn't finish within 20ms."},
		{"x.panic", StateFailed, "internal", "Cha Dash hit an internal error running this."},
	} {
		got := run(c.id)
		if got.State != c.state || got.Error == nil || got.Error.Code != c.code || got.Error.Message != c.msg || got.Undo != nil {
			t.Errorf("%s: %+v (error %+v)", c.id, got, got.Error)
		}
		if ev := e.lastEvent(); ev.Result != audit.Failed || ev.Error == nil || ev.Error.Code != c.code || ev.Task != got.ID {
			t.Errorf("%s: audit %+v", c.id, ev)
		}
	}

	// Cancellation: only someone who may see the task can cancel it, and
	// only when its action allows it. Every attempt is audited.
	task, err := e.approve(owner, e.mustPlan(owner, "x.block", "app:plex"), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !task.Cancellable {
		t.Fatalf("task from a cancellable action = %+v", task)
	}
	if err := e.e.Cancel(t.Context(), other, task.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("someone else cancelled it: %v", err)
	}
	if ev := e.lastEvent(); ev.Action != ActionCancel || ev.Result != audit.Refused || ev.Error == nil || ev.Error.Code != "not_found" || ev.Target != "" {
		t.Fatalf("refused cancel audited as %+v", ev)
	}
	if err := e.e.Cancel(t.Context(), owner, task.ID); err != nil {
		t.Fatal(err)
	}
	if ev := e.lastEvent(); ev.Action != ActionCancel || ev.Result != audit.OK || ev.Target != "app:plex" || ev.Task != task.ID || ev.Actor.ID != owner.ID {
		t.Fatalf("cancel audited as %+v", ev)
	}
	if got := e.wait(task.ID); got.State != StateCancelled || got.Error.Code != "cancelled" {
		t.Fatalf("cancelled task = %+v", got)
	}
	refusal(t, e.e.Cancel(t.Context(), owner, task.ID), "finished")
	// An action that doesn't allow it is refused, and audited.
	fails, err := e.approve(owner, e.mustPlan(owner, "x.fail", "app:plex"), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if fails.Cancellable {
		t.Fatalf("task from an action that isn't cancellable = %+v", fails)
	}
	refusal(t, e.e.Cancel(t.Context(), owner, fails.ID), "not_cancellable")
	e.wait(fails.ID)
	if rep, _ := e.audit.Verify(t.Context()); !rep.OK {
		t.Fatalf("audit chain: %+v", rep)
	}
}

func TestRestartFailsUnfinishedTasks(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	slow := Def{ID: "x.slow", Tier: T1, Targets: []string{"app"}, Execute: func(ctx context.Context, _ Request, _ *TaskRun) error {
		select {
		case <-block:
		case <-ctx.Done():
		}
		return nil
	}}
	e := newEnv(t, OwnerOnly{}, slow)
	task, err := e.approve(owner, e.mustPlan(owner, "x.slow", "app:plex"), "", "")
	if err != nil {
		t.Fatal(err)
	}
	// Mark the row running, as if the hub died mid-task, and start a second
	// engine on the same database.
	deadline := time.Now().Add(5 * time.Second)
	for {
		var state string
		e.st.Reader().QueryRow(`SELECT state FROM tasks WHERE id = ?`, task.ID).Scan(&state)
		if state == StateRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("task never ran")
		}
		time.Sleep(time.Millisecond)
	}
	e.start()
	got, err := e.e.Task(t.Context(), owner, task.ID)
	if err != nil || got.State != StateFailed || got.Error.Code != "interrupted" || got.Audit == nil {
		t.Fatalf("after restart: %+v, %v", got, err)
	}
	if ev := e.lastEvent(); ev.Task != task.ID || ev.Error.Code != "interrupted" {
		t.Fatalf("audit = %+v", ev)
	}
}

func TestTasksTopicPerViewer(t *testing.T) {
	ok := Def{ID: "x.ok", Tier: T1, Targets: []string{"app", "vm"}, Execute: func(context.Context, Request, *TaskRun) error { return nil }}
	pol := rolePolicy{}
	e := newEnv(t, pol, ok)
	src := e.e.TasksSource()
	ownerCtx := withIdentity(t.Context(), auth.Identity{User: auth.User{ID: owner.ID, Username: owner.Name, Role: "owner"}, Session: auth.Session{ID: owner.Session}})
	kimCtx := withIdentity(t.Context(), auth.Identity{User: auth.User{ID: other.ID, Username: other.Name, Role: "operator"}, Session: auth.Session{ID: other.Session}})

	type feed interface {
		Snapshot() (uint64, any)
	}
	open := func(ctx context.Context) (feed, func()) {
		f, err := openCtx(src, ctx)
		if err != nil {
			t.Fatal(err)
		}
		return f, f.Close
	}
	ownerFeed, c1 := open(ownerCtx)
	defer c1()
	kimFeed, c2 := open(kimCtx)
	defer c2()
	anonFeed, c3 := open(t.Context())
	defer c3()

	t1, err := e.approve(owner, e.mustPlan(owner, "x.ok", "app:plex"), "", "")
	if err != nil {
		t.Fatal(err)
	}
	t2, err := e.approve(other, e.mustPlan(other, "x.ok", "proxmox:i_x/qemu/201"), "", "")
	if err != nil {
		t.Fatal(err)
	}
	e.wait(t1.ID)
	e.wait(t2.ID)
	ids := func(f feed) string {
		_, doc := f.Snapshot()
		m := doc.(map[string]any)["tasks"].(map[string]json.RawMessage)
		var out []string
		for _, id := range []string{t1.ID, t2.ID} {
			if _, ok := m[id]; ok {
				out = append(out, id)
			}
		}
		return fmt.Sprint(out)
	}
	if got := ids(ownerFeed); got != fmt.Sprint([]string{t1.ID, t2.ID}) {
		t.Errorf("owner sees %s", got)
	}
	if got := ids(kimFeed); got != fmt.Sprint([]string{t2.ID}) {
		t.Errorf("an operator sees %s", got)
	}
	if got := ids(anonFeed); got != "[]" {
		t.Errorf("no session sees %s", got)
	}
	// GET /api/tasks/{id} follows the same rule.
	if _, err := e.e.Task(t.Context(), other, t1.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("an operator read the owner's task: %v", err)
	}
	if _, err := e.e.Task(t.Context(), owner, t2.ID); err != nil {
		t.Errorf("the owner couldn't read an operator's task: %v", err)
	}
}

// rolePolicy lets owners and operators act; only owners see every task.
type rolePolicy struct{}

func (rolePolicy) Authorize(_ context.Context, a Actor, scope, _ string, _ Tier) error {
	switch {
	case a.Role == "owner":
		return nil
	case scope == ScopeTasksAll || scope == ScopeAuditRead:
		return ErrForbidden
	case a.Role == "operator":
		return nil
	}
	return ErrForbidden
}

func TestTopicKeepsRecentTasks(t *testing.T) {
	ok := Def{ID: "x.ok", Tier: T1, Targets: []string{"app"}, Execute: func(context.Context, Request, *TaskRun) error { return nil }}
	e := newEnv(t, OwnerOnly{}, ok)
	var first string
	for i := range MaxTasks + 3 {
		task, err := e.approve(owner, e.mustPlan(owner, "x.ok", "app:plex"), "", "")
		if err != nil {
			t.Fatal(err)
		}
		e.wait(task.ID)
		if i == 0 {
			first = task.ID
		}
		e.clock.Advance(time.Millisecond)
	}
	// The last task's topic update lands just after it shows as finished.
	var doc map[string]any
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		_, doc = e.e.tasks.Snapshot()
		n := len(doc["tasks"].(map[string]json.RawMessage))
		if n == MaxTasks {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("topic holds %d tasks, want %d", n, MaxTasks)
		}
	}
	if _, ok := doc["tasks"].(map[string]json.RawMessage)[first]; ok {
		t.Fatal("the oldest task stayed")
	}
	// A day later they've all gone from the topic, not from the table.
	e.clock.Advance(RecentTasks + time.Minute)
	e.e.prune(t.Context())
	_, doc = e.e.tasks.Snapshot()
	if n := len(doc["tasks"].(map[string]json.RawMessage)); n != 0 {
		t.Fatalf("topic holds %d day-old tasks", n)
	}
	if _, err := e.e.Task(t.Context(), owner, first); err != nil {
		t.Fatalf("the table lost a task: %v", err)
	}
}

func openCtx(src realtime.Source, ctx context.Context) (realtime.Feed, error) {
	return realtime.OpenWith(ctx, src, TasksTopic, make(chan struct{}, 1))
}
