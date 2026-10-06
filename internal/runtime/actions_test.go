package runtime

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/audit"
	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/store"
	"dash.cha.sh/internal/topics"
	"dash.cha.sh/internal/vault"
)

// lever is a fake integration with actions and a control credential: a
// thing it publishes can be flipped (T1) or smashed (T2, Disrupts). It
// records which key each part used.
type lever struct{ rec *leverLog }

type leverLog struct {
	mu   sync.Mutex
	sync []string // "inst:key" Sync read with, plus "!control" when it saw the control group
	exec []string // "inst:key:action" Execute ran with
	hook []string // "hook:inst" for TierFor and Verify
}

func (l *leverLog) add(to *[]string, s string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	*to = append(*to, s)
}

func (l *leverLog) get(from *[]string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(*from)
}

func (lever) Manifest() integration.Manifest {
	return integration.Manifest{
		ID: "lever", Name: "Lever", Icon: "lever", Version: "0.1.0", Tier: integration.Bronze,
		ConfigSchema: integration.Object(integration.URLProp(),
			integration.Prop("user", integration.String("User")),
			integration.Prop("api_key", integration.Secret("API key")),
			integration.Prop("control_user", integration.String("Control user").InGroup(integration.GroupControl)),
			integration.Prop("control_key", integration.Secret("Control key").InGroup(integration.GroupControl)),
		).Require("url", "api_key"),
		Control: &integration.Control{Fields: []integration.ControlField{
			{Field: "control_user", Replaces: "user"}, {Field: "control_key", Replaces: "api_key"},
		}},
		Poll: integration.Poll{Interval: 5 * time.Second, Idle: 5 * time.Second},
	}
}

func (l lever) Connect(_ context.Context, cfg integration.Config, d integration.Deps) (integration.Instance, error) {
	_, sawControl := cfg.Values["control_key"]
	return &leverInst{rec: l.rec, prefix: "lever", id: cfg.InstanceID, key: cfg.String("api_key"), sawControl: sawControl, d: d}, nil
}

type leverInst struct {
	rec        *leverLog
	prefix     string
	id, key    string
	sawControl bool
	d          integration.Deps
}

func (i *leverInst) thing() string { return i.prefix + ":" + i.id + "/thing" }

func (i *leverInst) Test(context.Context) integration.TestResult {
	return integration.TestResult{OK: true}
}
func (i *leverInst) Close() error { return nil }

func (i *leverInst) Sync(context.Context, integration.Interest) error {
	s := i.id + ":" + i.key
	if i.sawControl {
		s += "!control"
	}
	i.rec.add(&i.rec.sync, s)
	if err := i.d.Emit.Replace("", integration.Facts{Resources: []integration.Resource{{ID: i.thing(), Kind: "thing", Name: "Thing " + i.id}}}); err != nil {
		return err
	}
	return i.d.Emit.Raise(integration.Incident{ID: "stuck", Class: integration.ClassProblem, Severity: integration.SeverityAttention,
		Title: "The thing is stuck", Target: i.thing(),
		Suggested:    integration.Action{ID: "lever.thing.flip", Label: "Flip it", Tier: "T1", Target: i.thing()},
		Alternatives: []integration.Action{{ID: "link.open", Label: "Open it", Tier: "T0", Target: i.thing()}}})
}

func (i *leverInst) Actions() []action.Def {
	return []action.Def{
		{ID: "lever.thing.flip", Title: "Flip", Targets: []string{"thing"}, Tier: action.T1,
			TierFor: func(action.Target) action.Tier { i.rec.add(&i.rec.hook, "tier:"+i.id); return action.T0 },
			Undo: func(action.Request) *action.Undo {
				return &action.Undo{Action: "lever.thing.flip", Params: json.RawMessage("{}"), Label: "Unflip on " + i.id}
			},
			Verify: func(context.Context, action.Request, *action.TaskRun) error {
				i.rec.add(&i.rec.hook, "verify:"+i.id)
				return nil
			}},
		{ID: "lever.thing.smash", Title: "Smash", Targets: []string{"thing"}, Tier: action.T2, Disrupts: true},
	}
}

func (i *leverInst) Execute(_ context.Context, req action.Request, _ *action.TaskRun) error {
	i.rec.add(&i.rec.exec, i.id+":"+i.key+":"+req.Action)
	return nil
}

// dial is lever's one-credential cousin: a mode field says whether it may
// act (Unraid's shape).
type dial struct{ rec *leverLog }

func (dial) Manifest() integration.Manifest {
	return integration.Manifest{
		ID: "dial", Name: "Dial", Icon: "dial", Version: "0.1.0", Tier: integration.Bronze,
		ConfigSchema: integration.Object(integration.URLProp(),
			integration.Prop("api_key", integration.Secret("API key")),
			integration.Prop("control", integration.Boolean("Let Cha Dash control it")),
		).Require("url", "api_key"),
		Control: &integration.Control{Mode: "control", Grants: []string{"docker:update_any"}},
		Poll:    integration.Poll{Interval: 5 * time.Second, Idle: 5 * time.Second},
	}
}

func (d dial) Connect(ctx context.Context, cfg integration.Config, deps integration.Deps) (integration.Instance, error) {
	in, err := lever(d).Connect(ctx, cfg, deps)
	li := in.(*leverInst)
	li.prefix = "dial"
	return &dialInst{li}, err
}

type dialInst struct{ *leverInst }

func (i *dialInst) Test(context.Context) integration.TestResult {
	return integration.TestResult{OK: true, Grants: []integration.Grant{{Name: "docker:update_any", Write: true}, {Name: "docker:read_any"}}}
}

func (i *dialInst) Actions() []action.Def {
	return []action.Def{{ID: "dial.thing.turn", Title: "Turn", Targets: []string{"thing"}, Tier: action.T1}}
}

type actEnv struct {
	m   *Manager
	set *topics.Set
	reg *action.Registry
	eng *action.Engine
	rec *leverLog
}

func newActEnv(t *testing.T) *actEnv {
	t.Helper()
	st, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "chadash.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	rand.Read(key)
	v, err := vault.New(st, key)
	if err != nil {
		t.Fatal(err)
	}
	rec := &leverLog{}
	reg := integration.NewRegistry()
	for _, i := range []integration.Integration{lever{rec}, dial{rec}} {
		if err := reg.Register(i); err != nil {
			t.Fatal(err)
		}
	}
	e := &actEnv{set: topics.New(graph.New(), time.Now()), reg: action.NewRegistry(), rec: rec}
	e.m, err = New(Options{Store: st, Vault: v, Registry: reg, Topics: e.set, Actions: e.reg, Log: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.m.Close)
	e.eng, err = action.New(t.Context(), action.Options{Store: st, Audit: audit.New(st), Registry: e.reg, Policy: action.AllowAll{}, Graph: e.set.Graph})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.eng.Close)
	return e
}

var owner = action.Actor{Kind: action.KindUser, ID: "u_1", Name: "sam", Role: "owner", Session: "s_1"}

// run plans and approves an action and waits for it.
func (e *actEnv) run(t *testing.T, act, target string) (action.Task, error) {
	t.Helper()
	p, err := e.eng.Plan(t.Context(), owner, action.PlanRequest{Action: act, Target: target})
	if err != nil {
		return action.Task{}, err
	}
	task, err := e.eng.Approve(t.Context(), owner, p.ID, action.ApproveRequest{Hash: p.Hash}, "")
	if err != nil {
		return action.Task{}, err
	}
	var done action.Task
	waitFor(t, "the task", func() bool {
		done, _ = e.eng.Task(t.Context(), owner, task.ID)
		return done.Finished()
	})
	return done, nil
}

func (e *actEnv) incident(id string) (integration.Incident, bool) {
	b := e.set.Inbox.Item(topics.Incidents, id)
	if b == nil {
		return integration.Incident{}, false
	}
	var in integration.Incident
	_ = json.Unmarshal(b, &in)
	return in, true
}

func (e *actEnv) create(t *testing.T, integ string, cfg map[string]any, secrets map[string]any) string {
	t.Helper()
	id, err := e.m.Create(t.Context(), CreateRequest{Integration: integ, Name: integ + "-" + strings.ToLower(rand.Text()[:4]), Config: cfg, Secrets: secrets})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the first sync", func() bool { _, ok := e.set.Graph.Get(graph.RID(integ + ":" + id + "/thing")); return ok })
	return id
}

func refusal(err error) *action.Refusal {
	var r *action.Refusal
	errors.As(err, &r)
	return r
}

func TestActionsFollowTheInstance(t *testing.T) {
	e := newActEnv(t)
	a := e.create(t, "lever", map[string]any{"url": "http://lever.example", "user": "reader", "control_user": "boss"},
		map[string]any{"api_key": "read-key", "control_key": "control-key"})
	if got := e.reg.IDs(); !slices.Equal(got, []string{"lever.thing.flip", "lever.thing.smash"}) {
		t.Fatalf("registered = %v", got)
	}
	thing := "lever:" + a + "/thing"
	task, err := e.run(t, "lever.thing.flip", thing)
	if err != nil || task.State != action.StateSucceeded {
		t.Fatalf("flip: %+v, %v", task, err)
	}
	// Execute acted with the control key; Sync read with the read key and
	// never saw the control group.
	if got := e.rec.get(&e.rec.exec); !slices.Equal(got, []string{a + ":control-key:lever.thing.flip"}) {
		t.Fatalf("exec = %v", got)
	}
	for _, s := range e.rec.get(&e.rec.sync) {
		if s != a+":read-key" {
			t.Fatalf("sync saw %q", s)
		}
	}

	// The incident's suggested action is plan_ready; the link isn't the
	// engine's, so it carries nothing.
	in, ok := e.incident(a + ".stuck")
	if !ok || in.Suggested.PlanReady == nil || !*in.Suggested.PlanReady || in.Suggested.Unavailable != "" ||
		in.Alternatives[0].PlanReady != nil || in.Alternatives[0].Unavailable != "" {
		t.Fatalf("incident = %+v", in)
	}

	// Take the control credential away: the actions stay, unavailable,
	// and the incident says so at once.
	if err := e.m.Update(t.Context(), a, UpdateRequest{Config: map[string]any{"url": "http://lever.example", "user": "reader"},
		Secrets: map[string]any{"control_key": nil}}); err != nil {
		t.Fatal(err)
	}
	_, err = e.run(t, "lever.thing.flip", thing)
	if r := refusal(err); r == nil || r.Code != "unavailable" || r.Recipe != a || !strings.Contains(r.Message, "Add a control credential") {
		t.Fatalf("without control: %v", err)
	}
	waitFor(t, "the incident to say so", func() bool {
		in, _ := e.incident(a + ".stuck")
		return in.Suggested.PlanReady != nil && !*in.Suggested.PlanReady && in.Suggested.Recipe == a && in.Suggested.Unavailable != ""
	})

	// Disabled: gone from the registry. Enabled again: back.
	off, on := false, true
	if err := e.m.Update(t.Context(), a, UpdateRequest{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	if got := e.reg.IDs(); len(got) != 0 {
		t.Fatalf("disabled, registered = %v", got)
	}
	if _, err := e.run(t, "lever.thing.flip", thing); refusal(err) == nil || refusal(err).Code != "not_found" {
		t.Fatalf("disabled: %v", err)
	}
	if err := e.m.Update(t.Context(), a, UpdateRequest{Enabled: &on}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "registration", func() bool { return len(e.reg.IDs()) == 2 })

	// Deleted: gone.
	if err := e.m.Delete(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	if got := e.reg.IDs(); len(got) != 0 || len(e.m.Actions().Attached()) != 0 {
		t.Fatalf("deleted, registered = %v", got)
	}
}

func TestActionsRouteToTheOwningInstance(t *testing.T) {
	e := newActEnv(t)
	sec := func(k string) map[string]any {
		return map[string]any{"api_key": "read-" + k, "control_key": "control-" + k}
	}
	a := e.create(t, "lever", map[string]any{"url": "http://a.example", "control_user": "boss"}, sec("a"))
	b := e.create(t, "lever", map[string]any{"url": "http://b.example", "control_user": "boss"}, sec("b"))
	if got := e.m.Actions().Attached(); len(got) != 2 {
		t.Fatalf("attached = %v", got)
	}
	// Plan, TierFor, Undo, Execute and Verify all reach the owning instance.
	p, err := e.eng.Plan(t.Context(), owner, action.PlanRequest{Action: "lever.thing.flip", Target: "lever:" + b + "/thing"})
	if err != nil || p.Undo == nil || p.Undo.Label != "Unflip on "+b {
		t.Fatalf("plan on b = %+v, %v", p, err)
	}
	if _, err := e.run(t, "lever.thing.flip", "lever:"+b+"/thing"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.run(t, "lever.thing.flip", "lever:"+a+"/thing"); err != nil {
		t.Fatal(err)
	}
	for _, h := range e.rec.get(&e.rec.hook) {
		if h != "tier:"+b && h != "verify:"+b && h != "tier:"+a && h != "verify:"+a {
			t.Fatalf("hook = %q", h)
		}
	}
	if got := e.rec.get(&e.rec.hook); !slices.Contains(got, "verify:"+b) || !slices.Contains(got, "verify:"+a) ||
		slices.Index(got, "verify:"+b) > slices.Index(got, "verify:"+a) {
		t.Fatalf("hooks = %v", got)
	}
	if got := e.rec.get(&e.rec.exec); !slices.Equal(got, []string{b + ":control-b:lever.thing.flip", a + ":control-a:lever.thing.flip"}) {
		t.Fatalf("exec = %v", got)
	}
	// One instance going leaves the other's actions.
	if err := e.m.Delete(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	if _, err := e.run(t, "lever.thing.flip", "lever:"+b+"/thing"); err != nil {
		t.Fatalf("b after a went: %v", err)
	}
	if _, err := e.run(t, "lever.thing.flip", "lever:"+a+"/thing"); refusal(err) == nil || refusal(err).Code != "not_found" {
		t.Fatalf("a's target after a went: %v", err)
	}
}

func TestActionsModeIntegration(t *testing.T) {
	e := newActEnv(t)
	ro := e.create(t, "dial", map[string]any{"url": "http://dial.example"}, map[string]any{"api_key": "only-key"})
	_, err := e.run(t, "dial.thing.turn", "dial:"+ro+"/thing")
	if r := refusal(err); r == nil || r.Code != "unavailable" || r.Recipe != ro || !strings.Contains(r.Message, "is set up read-only") {
		t.Fatalf("read-only mode: %v", err)
	}
	rw := e.create(t, "dial", map[string]any{"url": "http://dial.example", "control": true}, map[string]any{"api_key": "only-key"})
	if task, err := e.run(t, "dial.thing.turn", "dial:"+rw+"/thing"); err != nil || task.State != action.StateSucceeded {
		t.Fatalf("control mode: %+v, %v", task, err)
	}
	if got := e.rec.get(&e.rec.exec); !slices.Equal(got, []string{rw + ":only-key:dial.thing.turn"}) {
		t.Fatalf("exec = %v", got)
	}

	// The write grants a controllable key needs aren't "more than Cha Dash
	// uses"; on a read-only key they are.
	writeWarning := func(control bool) bool {
		res, err := e.m.Test(t.Context(), TestRequest{Integration: "dial", Config: map[string]any{"url": "http://dial.example", "control": control},
			Secrets: map[string]any{"api_key": "only-key"}})
		if err != nil {
			t.Fatal(err)
		}
		return slices.ContainsFunc(res.Warnings, func(w integration.Warning) bool { return w.Code == "write_grant" })
	}
	if writeWarning(true) || !writeWarning(false) {
		t.Fatalf("write_grant: controllable %v, read-only %v", writeWarning(true), writeWarning(false))
	}
}

func TestControlGroupAllOrNothing(t *testing.T) {
	e := newActEnv(t)
	_, err := e.m.Create(t.Context(), CreateRequest{Integration: "lever", Name: "half",
		Config: map[string]any{"url": "http://lever.example", "control_user": "boss"}, Secrets: map[string]any{"api_key": "k"}})
	var ve *ValidationError
	if !errors.As(err, &ve) || len(ve.Fields) != 1 || ve.Fields[0].Path != "/control_key" {
		t.Fatalf("err = %v", err)
	}
	// A read-only connection's test hints at the control credential.
	res, err := e.m.Test(t.Context(), TestRequest{Integration: "lever", Config: map[string]any{"url": "http://lever.example"}, Secrets: map[string]any{"api_key": "k"}})
	if err != nil || !slices.ContainsFunc(res.Warnings, func(w integration.Warning) bool { return w.Code == "control_available" }) {
		t.Fatalf("test = %+v, %v", res, err)
	}
	res, _ = e.m.Test(t.Context(), TestRequest{Integration: "lever", Config: map[string]any{"url": "http://lever.example", "control_user": "boss"},
		Secrets: map[string]any{"api_key": "k", "control_key": "c"}})
	if slices.ContainsFunc(res.Warnings, func(w integration.Warning) bool { return w.Code == "control_available" }) {
		t.Fatalf("with control: %+v", res.Warnings)
	}
}

func TestSelfAwareRule(t *testing.T) {
	g := graph.New()
	if _, err := g.Update(func(tx *graph.Tx) error {
		for _, r := range []graph.Resource{
			{ID: "pve:host", Kind: "host"}, {ID: "pve:host/vm/1", Kind: "vm"}, {ID: "pve:host/vm/2", Kind: "vm"},
			{ID: "net:router", Kind: "router"}, {ID: "docker:vm2/engine", Kind: "engine"}, {ID: "app:chadash", Kind: "app", Attrs: map[string]any{"self": true}},
			{ID: "pve:host/vm/3", Kind: "vm"}, {ID: "app:plex", Kind: "app"},
		} {
			if err := tx.Upsert(r); err != nil {
				return err
			}
		}
		for _, rel := range [][2]string{{"pve:host/vm/1", "pve:host"}, {"pve:host/vm/2", "pve:host"}, {"pve:host/vm/3", "pve:host"},
			{"net:router", "pve:host/vm/1"}, {"docker:vm2/engine", "pve:host/vm/2"}, {"app:chadash", "docker:vm2/engine"}, {"app:plex", "pve:host/vm/3"}} {
			if err := tx.Relate(graph.Relation{From: graph.RID(rel[0]), Type: "runs_on", To: graph.RID(rel[1])}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	a := NewActions(action.NewRegistry(), g, nil)
	for id, want := range map[string]bool{
		"pve:host": true, "pve:host/vm/1": true, "pve:host/vm/2": true, "net:router": true,
		"pve:host/vm/3": false, "app:plex": false, "unknown": false,
	} {
		if got := a.Critical(id); got != want {
			t.Errorf("Critical(%s) = %v, want %v", id, got, want)
		}
	}
}
