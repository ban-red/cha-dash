package demo

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/audit"
	"dash.cha.sh/internal/store"
	"dash.cha.sh/sim"
)

var demoOwner = action.Actor{Kind: action.KindUser, ID: "u_owner", Name: "sam", Role: "owner", Via: "web", Session: "s_1"}

func newEngine(t *testing.T, d *Driver) *action.Engine {
	t.Helper()
	st, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "chadash.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	reg := action.NewRegistry()
	if err := reg.Register(ActionSource, d.Actions()...); err != nil {
		t.Fatal(err)
	}
	e, err := action.New(t.Context(), action.Options{Store: st, Audit: audit.New(st), Registry: reg, Policy: action.OwnerOnly{}, Graph: d.t.Graph})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Close)
	return e
}

func TestActionsCoverTheScript(t *testing.T) {
	d, _, _ := newDriver(t, Options{Seed: 1, At: DefaultAt})
	tiers := map[string]action.Tier{}
	for _, def := range d.Actions() {
		tiers[def.ID] = def.Tier
	}
	for id, tier := range map[string]action.Tier{
		"pve.guest.backup": action.T1, "pbs.datastore.prune": action.T2, "pve.guest.restart": action.T2,
		"integration.reconnect": action.T0, "integration.update": action.T1, "container.update": action.T2,
		"integration.settings": action.T0,
	} {
		if got, ok := tiers[id]; !ok || got != tier {
			t.Errorf("%s: tier %v (registered %v), want %v", id, got, ok, tier)
		}
	}
	if _, ok := tiers["link.open"]; ok {
		t.Error("link.open is an action; it only opens a link")
	}
}

// A demo fix runs the whole engine path: plan with the incident's blast
// radius, approve, a task that follows the scenario's phases, an audit
// record.
func TestFixThroughTheEngine(t *testing.T) {
	d, tp, clock := newDriver(t, Options{Seed: 1, At: DefaultAt})
	e := newEngine(t, d)
	in := incident(t, tp, "host-memory-pressure")
	a := in.Suggested

	p, err := e.Plan(t.Context(), demoOwner, action.PlanRequest{Action: a.ID, Target: a.Target})
	if err != nil {
		t.Fatal(err)
	}
	if p.Label != a.Label || p.Tier != action.T2 || p.Confirm != "hold" || len(p.Impact) != len(a.Impact) || len(p.Impact) == 0 || len(p.Steps) != 2 {
		t.Fatalf("plan = %+v", p)
	}
	for i, im := range a.Impact {
		if p.Impact[i] != (action.Impact{Resource: im.Resource, Via: string(im.Via), Depth: im.Depth}) {
			t.Fatalf("impact %d = %+v, want %+v", i, p.Impact[i], im)
		}
	}
	task, err := e.Approve(t.Context(), demoOwner, p.ID, action.ApproveRequest{Hash: p.Hash}, "")
	if err != nil {
		t.Fatal(err)
	}

	// The fix landed; the task waits on the incident.
	waitFor(t, func() bool {
		got, _ := e.Task(t.Context(), demoOwner, task.ID)
		return got.State == action.StateVerifying
	})
	if err := d.CanFix(in.ID); !errors.Is(err, sim.ErrFixing) {
		t.Fatalf("CanFix after the fix: %v", err)
	}
	// Pressing it again is refused before anything runs.
	_, err = e.Plan(t.Context(), demoOwner, action.PlanRequest{Action: a.ID, Target: a.Target})
	var ref *action.Refusal
	if !errors.As(err, &ref) || ref.Code != "precondition" || ref.Message != "That incident is already being fixed." {
		t.Fatalf("second plan: %v", err)
	}

	var steps []string
	for range 40 { // 20 simulated minutes
		clock.Advance(30 * time.Second)
		d.Step()
		got, _ := e.Task(t.Context(), demoOwner, task.ID)
		if got.Step != "" && !slices.Contains(steps, got.Step) {
			steps = append(steps, got.Step)
		}
		if got.Finished() {
			break
		}
	}
	var done action.Task
	waitFor(t, func() bool {
		done, _ = e.Task(t.Context(), demoOwner, task.ID)
		return done.Finished() && done.Audit != nil
	})
	if done.State != action.StateSucceeded {
		t.Fatalf("task = %+v (error %+v)", done, done.Error)
	}
	// Each phase is a step and a log line; the resolution closes the log.
	if !slices.Contains(steps, "Restarting") || len(done.Log) < 3 || !strings.HasPrefix(done.Log[1], "restarting: ") ||
		done.Log[len(done.Log)-1] != incident(t, tp, in.ID).Resolution {
		t.Fatalf("steps seen = %v, log = %q", steps, done.Log)
	}
	if got := incident(t, tp, in.ID); got.Status != sim.StatusResolved || got.ResolvedBy != "action" {
		t.Fatalf("incident after the task = %s by %s", got.Status, got.ResolvedBy)
	}
	page, _ := e.Audit().Query(t.Context(), audit.Filter{})
	if len(page.Events) != 2 || page.Events[0].Result != audit.OK || page.Events[0].Task != task.ID || page.Events[1].Result != audit.Refused {
		t.Fatalf("audit = %+v", page.Events)
	}
}

func TestFixRefusals(t *testing.T) {
	d, tp, _ := newDriver(t, Options{Seed: 1, At: DefaultAt})
	e := newEngine(t, d)
	plex := incident(t, tp, "plex-token-revoked").Suggested
	var ref *action.Refusal

	// A real resource with nothing open that this fixes.
	_, err := e.Plan(t.Context(), demoOwner, action.PlanRequest{Action: plex.ID, Target: "app:chadash"})
	if !errors.As(err, &ref) || ref.Code != "precondition" {
		t.Fatalf("nothing to fix: %v", err)
	}
	_, err = e.Plan(t.Context(), demoOwner, action.PlanRequest{Action: plex.ID, Target: "app:nope"})
	if !errors.As(err, &ref) || ref.Code != "not_found" {
		t.Fatalf("unknown target: %v", err)
	}

	// The incident resolves between plan and approve: the plan is stale.
	p, err := e.Plan(t.Context(), demoOwner, action.PlanRequest{Action: plex.ID, Target: plex.Target})
	if err != nil || p.Confirm != "none" {
		t.Fatalf("plan = %+v, %v", p, err)
	}
	if err := d.Fix("plex-token-revoked"); err != nil {
		t.Fatal(err)
	}
	_, err = e.Approve(t.Context(), demoOwner, p.ID, action.ApproveRequest{Hash: p.Hash}, "")
	if !errors.As(err, &ref) || (ref.Code != "precondition" && ref.Code != "plan_changed") {
		t.Fatalf("approve after a fix elsewhere: %v", err)
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(2 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatal("timed out")
}
