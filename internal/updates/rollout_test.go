package updates

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/audit"
	"dash.cha.sh/internal/auth"
	"dash.cha.sh/internal/authz"
	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/integration"
)

func (e *env) relate(from, typ, to string) {
	e.t.Helper()
	if _, err := e.g.Update(func(tx *graph.Tx) error {
		return tx.Relate(graph.Relation{From: graph.RID(from), Type: graph.RelType(typ), To: graph.RID(to)})
	}); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) upsert(r graph.Resource) {
	e.t.Helper()
	if _, err := e.g.Update(func(tx *graph.Tx) error { return tx.Upsert(r) }); err != nil {
		e.t.Fatal(err)
	}
}

func names(steps []RolloutStep) []string {
	var out []string
	for _, s := range steps {
		out = append(out, s.Name+":"+string(s.State))
	}
	return out
}

func (e *env) plan(targets ...string) RolloutPlan {
	e.t.Helper()
	p, err := e.s.Plan(e.t.Context(), targets)
	if err != nil {
		e.t.Fatal(err)
	}
	return p
}

// Dependencies first, the list's order otherwise; a cycle or an unknown
// dependency keeps the list's order for the lot.
func TestPlanOrder(t *testing.T) {
	e := newEnv(t)
	for _, n := range []string{"bazarr", "db", "prowlarr", "sonarr"} {
		e.container(n, integration.UpdateAvailable, "sha256:"+n)
	}
	// The list is by name: bazarr, db, prowlarr, sonarr.
	e.relate("docker:i_a/container/sonarr", "depends_on", "docker:i_a/container/prowlarr")
	e.relate("docker:i_a/container/bazarr", "depends_on", "docker:i_a/container/sonarr")
	e.relate("docker:i_a/container/prowlarr", "depends_on", "docker:i_a/container/db")
	p := e.plan()
	if got := names(p.Steps); !slices.Equal(got, []string{"db:pending", "prowlarr:pending", "sonarr:pending", "bazarr:pending"}) || !p.Ordered {
		t.Fatalf("order = %v (ordered %v)", got, p.Ordered)
	}
	// A dependency outside the selection doesn't hold anything up.
	p = e.plan("docker:i_a/container/bazarr", "docker:i_a/container/sonarr")
	if got := names(p.Steps); !slices.Equal(got, []string{"sonarr:pending", "bazarr:pending"}) {
		t.Fatalf("selection = %v", got)
	}

	// A cycle: the list's order.
	e.relate("docker:i_a/container/db", "depends_on", "docker:i_a/container/bazarr")
	p = e.plan()
	if got := names(p.Steps); !slices.Equal(got, []string{"bazarr:pending", "db:pending", "prowlarr:pending", "sonarr:pending"}) || p.Ordered {
		t.Fatalf("cycle = %v (ordered %v)", got, p.Ordered)
	}

	// An unknown dependency (the graph keeps none, but a snapshot taken
	// mid-change could): the list's order too.
	doc := graph.Doc{Resources: map[graph.RID]*graph.Resource{"a": {ID: "a"}, "b": {ID: "b"}}, Relations: map[string]*graph.Relation{
		"1": {From: "a", Type: "depends_on", To: "b"}, "2": {From: "b", Type: "depends_on", To: "ghost"}}}
	if got, ordered := orderSteps(doc, []RolloutStep{{Target: "a", Name: "a"}, {Target: "b", Name: "b"}}); got[0].Name != "a" || ordered {
		t.Fatalf("unknown = %v", got)
	}
	delete(doc.Relations, "2")
	if got, ordered := orderSteps(doc, []RolloutStep{{Target: "a", Name: "a"}, {Target: "b", Name: "b"}}); got[0].Name != "b" || !ordered {
		t.Fatalf("known = %v", got)
	}
}

// Held and snoozed containers are left out and listed, and so is a host's
// row; the tier is the highest step's, and a newer image changes the hash.
func TestPlanLeavesOutHeldAndTier(t *testing.T) {
	e := newEnv(t)
	sonarr := e.container("sonarr", integration.UpdateAvailable, "sha256:s1")
	radarr := e.container("radarr", integration.UpdateAvailable, "sha256:r1")
	lidarr := e.container("lidarr", integration.UpdateAvailable, "sha256:l1")
	e.container("plex", integration.UpdateCurrent, "sha256:aa")
	if _, err := e.s.Hold(t.Context(), radarr, KindHold, "u_op"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.Hold(t.Context(), lidarr, KindSnooze, ""); err != nil {
		t.Fatal(err)
	}
	p := e.plan()
	if got := names(p.Steps); !slices.Equal(got, []string{"sonarr:pending", "lidarr:skipped", "radarr:skipped"}) {
		t.Fatalf("steps = %v", got)
	}
	if !strings.Contains(p.Steps[1].Reason, "snoozed") || !strings.Contains(p.Steps[2].Reason, "held") || p.Tier != action.T2 {
		t.Fatalf("plan = %+v", p)
	}
	// Selected, a current one and an unknown one are listed too.
	p = e.plan(sonarr, "docker:i_a/container/plex", "docker:i_a/container/ghost")
	if got := names(p.Steps); !slices.Equal(got, []string{"sonarr:pending", "plex:skipped", "docker:i_a/container/ghost:skipped"}) {
		t.Fatalf("selected = %v", got)
	}

	// A step whose Def makes it T4 (it runs the hub) is left out; one the
	// Def raises to T3 raises the plan.
	reg := action.NewRegistry()
	noop := func(context.Context, action.Request, *action.TaskRun) error { return nil }
	if err := reg.Register("fake", action.Def{ID: "docker.container.update", Tier: action.T2, Execute: noop,
		TierFor: func(t action.Target) action.Tier {
			switch t.Name {
			case "sonarr":
				return action.T4
			case "lidarr":
				return action.T3
			}
			return action.T2
		}}); err != nil {
		t.Fatal(err)
	}
	e.s.reg = reg
	if _, _, err := e.s.Release(t.Context(), lidarr); err != nil {
		t.Fatal(err)
	}
	p = e.plan()
	if got := names(p.Steps); !slices.Equal(got, []string{"lidarr:pending", "sonarr:skipped", "radarr:skipped"}) || p.Tier != action.T3 ||
		!strings.Contains(p.Steps[1].Reason, "runs Cha Dash itself") {
		t.Fatalf("tiers = %v %s", got, p.Tier)
	}

	before := p.Hash
	e.container("lidarr", integration.UpdateAvailable, "sha256:l2")
	if p := e.plan(); p.Hash == before {
		t.Fatal("a newer image kept the plan's hash")
	}
}

// Snapshots: an owned stack's commit, a PVE guest's snapshot once per
// guest, or none.
func TestPlanSnapshots(t *testing.T) {
	e := newEnv(t)
	e.container("sonarr", integration.UpdateAvailable, "sha256:s1")
	e.container("radarr", integration.UpdateAvailable, "sha256:r1")
	p := e.plan()
	for _, st := range p.Steps {
		if st.Snapshot != SnapshotNone {
			t.Fatalf("no way to snapshot, but %+v", st)
		}
	}
	// The engine runs on a VM the correlator matched to Proxmox's guest.
	e.upsert(graph.Resource{ID: "docker:i_a/host", Kind: "vm", Name: "pvedocker"})
	e.upsert(graph.Resource{ID: "proxmox:i_p/qemu/101", Kind: "vm", Name: "pvedocker"})
	e.relate("docker:i_a/engine", "runs_on", "docker:i_a/host")
	e.relate("docker:i_a/host", "same_as", "proxmox:i_p/qemu/101")
	asked := ""
	e.s.canAct = func(id, target string) bool { asked = id + " " + target; return true }
	p = e.plan()
	if p.Steps[0].Snapshot != SnapshotPVE || p.Steps[0].Guest != "proxmox:i_p/qemu/101" || asked != "proxmox.guest.snapshot.create proxmox:i_p/qemu/101" {
		t.Fatalf("pve = %+v (asked %q)", p.Steps[0], asked)
	}
	// The containers are in the media project; owned, its commit comes first.
	e.upsert(graph.Resource{ID: "docker:i_a/engine", Kind: "engine", Name: "pvedocker", Attrs: map[string]any{"node": "pvedocker"}})
	e.upsert(graph.Resource{ID: "docker:i_a/stack/media", Kind: "stack", Name: "media"})
	e.s.ownedStack = func(_ context.Context, node, project string) bool { return node == "pvedocker" && project == "media" }
	p = e.plan()
	if p.Steps[0].Snapshot != SnapshotGit || p.Steps[1].Snapshot != SnapshotGit {
		t.Fatalf("git = %+v", p.Steps)
	}
}

// fakeUpdates is the updates and snapshots a rollout runs, recorded.
type fakeUpdates struct {
	mu    sync.Mutex
	calls []string
	fail  string // the container whose update rolls back
}

func (f *fakeUpdates) record(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, s)
}

func (f *fakeUpdates) got() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func (f *fakeUpdates) defs() []action.Def {
	snapParams := integration.Params(integration.Object(
		integration.Prop("name", integration.String("Name")),
		integration.Prop("description", integration.String("Description")),
	))
	return []action.Def{
		{ID: "docker.container.update", Targets: []string{"container"}, Tier: action.T2,
			Execute: func(_ context.Context, r action.Request, _ *action.TaskRun) error {
				f.record("update " + r.Target.Name)
				if r.Target.Name == f.fail {
					return action.Fail("rolled_back", r.Target.Name+" didn't come up healthy, so its previous image is back.")
				}
				return nil
			}},
		{ID: "proxmox.guest.snapshot.create", Targets: []string{"vm"}, Tier: action.T1, Params: snapParams,
			Execute: func(_ context.Context, r action.Request, _ *action.TaskRun) error {
				var p struct{ Name string }
				_ = json.Unmarshal(r.Params, &p)
				f.record("snapshot " + r.Target.Name + " " + p.Name)
				return nil
			}},
	}
}

var operator = action.Actor{Kind: action.KindUser, ID: "u_op", Name: "op", Role: "operator", Via: "web", Session: "s_1"}

// engine starts an action engine with the fake updates and the rollout.
func (e *env) engine(f *fakeUpdates) (*action.Engine, *audit.Log) {
	e.t.Helper()
	reg := action.NewRegistry()
	if err := reg.Register("fake", f.defs()...); err != nil {
		e.t.Fatal(err)
	}
	if err := reg.Register(ActionSource, e.s.Actions()...); err != nil {
		e.t.Fatal(err)
	}
	e.s.reg = reg
	log := audit.New(e.st)
	eng, err := action.New(e.t.Context(), action.Options{Store: e.st, Audit: log, Registry: reg, Policy: action.AllowAll{}, Graph: e.g, Guard: e.s.Guard})
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(eng.Close)
	e.s.Engine = eng
	return eng, log
}

func wait(t *testing.T, eng *action.Engine, a action.Actor, id string) action.Task {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		task, err := eng.Task(t.Context(), a, id)
		if err != nil {
			t.Fatal(err)
		}
		if task.Finished() {
			return task
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("task %s didn't finish", id)
	return action.Task{}
}

func rollout(t *testing.T, eng *action.Engine, a action.Actor, params string) (*action.Plan, action.Task) {
	t.Helper()
	p, err := eng.Plan(t.Context(), a, action.PlanRequest{Action: ActRollout, Target: RolloutTarget, Params: json.RawMessage(params)})
	if err != nil {
		t.Fatal(err)
	}
	task, err := eng.Approve(t.Context(), a, p.ID, action.ApproveRequest{Hash: p.Hash}, "")
	if err != nil {
		t.Fatal(err)
	}
	return p, wait(t, eng, a, task.ID)
}

// One task runs every step in order, snapshot first; the first update that
// fails stops it: it rolled back, the rest don't start, the Inbox says so.
func TestRolloutStopsAtTheFirstFailure(t *testing.T) {
	e := newEnv(t)
	for _, n := range []string{"bazarr", "lidarr", "radarr", "sonarr"} {
		e.container(n, integration.UpdateAvailable, "sha256:"+n)
	}
	e.upsert(graph.Resource{ID: "proxmox:i_p/qemu/101", Kind: "vm", Name: "pvedocker"})
	e.relate("docker:i_a/engine", "runs_on", "proxmox:i_p/qemu/101")
	e.s.canAct = func(string, string) bool { return true }
	f := &fakeUpdates{fail: "radarr"}
	eng, log := e.engine(f)

	preview := e.plan()
	p, task := rollout(t, eng, operator, `{"hash":"`+preview.Hash+`"}`)
	if p.Tier != action.T2 || p.Label != "Update everything available" || len(p.Steps) != 5 ||
		p.Steps[0].Label != "Snapshot pvedocker in Proxmox, then update bazarr on pvedocker." ||
		p.Steps[1].Label != "Update lidarr on pvedocker. The snapshot of pvedocker covers it." {
		t.Fatalf("plan = %+v", p)
	}
	if task.State != action.StateFailed || task.Error == nil || task.Error.Code != "rolled_back" ||
		task.Error.Message != "Updated: bazarr and lidarr. Rolled back: radarr. Didn't start: sonarr." {
		t.Fatalf("task = %+v %+v", task, task.Error)
	}
	calls := f.got()
	if len(calls) != 4 || !strings.HasPrefix(calls[0], "snapshot pvedocker cd-") || !slices.Equal(calls[1:], []string{"update bazarr", "update lidarr", "update radarr"}) {
		t.Fatalf("calls = %v", calls)
	}
	ros, err := e.s.Rollouts(t.Context())
	if err != nil || len(ros) != 1 {
		t.Fatalf("rollouts = %+v %v", ros, err)
	}
	ro := ros[0]
	if ro.State != RolloutFailed || ro.CreatedBy != "op" || ro.PlanHash != preview.Hash || ro.FinishedAt.IsZero() ||
		!slices.Equal(names(ro.Steps), []string{"bazarr:succeeded", "lidarr:succeeded", "radarr:rolled_back", "sonarr:not_started"}) {
		t.Fatalf("rollout = %+v", ro)
	}
	raw := e.inbox.Item(incidentsColl, failedItemID(ro.ID))
	var it item
	if raw == nil || json.Unmarshal(raw, &it) != nil || it.Title != "A rollout stopped at radarr" || it.Summary != task.Error.Message {
		t.Fatalf("inbox = %s", raw)
	}
	// Each step is audited on its own, under the rollout's task.
	page, err := log.Query(t.Context(), audit.Filter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	var steps []string
	for _, ev := range slices.Backward(page.Events) {
		if ev.Task == task.ID && ev.Action != ActRollout {
			steps = append(steps, ev.Action+" "+ev.Result)
		}
	}
	if !slices.Equal(steps, []string{"proxmox.guest.snapshot.create ok", "docker.container.update ok", "docker.container.update ok", "docker.container.update failed"}) {
		t.Fatalf("audit = %v", steps)
	}

	// A changed plan since the preview (a newer image) is refused.
	e.container("sonarr", integration.UpdateAvailable, "sha256:sonarr2")
	_, err = eng.Plan(t.Context(), operator, action.PlanRequest{Action: ActRollout, Target: RolloutTarget, Params: json.RawMessage(`{"hash":"` + preview.Hash + `"}`)})
	var ref *action.Refusal
	if !errors.As(err, &ref) || ref.Code != "precondition" || !strings.Contains(ref.Message, "changed since you looked") {
		t.Fatalf("stale hash = %v", err)
	}
}

// A held container isn't updated, even when a rollout names it.
func TestRolloutLeavesOutHeld(t *testing.T) {
	e := newEnv(t)
	sonarr := e.container("sonarr", integration.UpdateAvailable, "sha256:s1")
	radarr := e.container("radarr", integration.UpdateAvailable, "sha256:r1")
	if _, err := e.s.Hold(t.Context(), radarr, KindHold, "u_op"); err != nil {
		t.Fatal(err)
	}
	f := &fakeUpdates{}
	eng, _ := e.engine(f)
	_, task := rollout(t, eng, operator, `{"targets":["`+sonarr+`","`+radarr+`"]}`)
	if task.State != action.StateSucceeded || !slices.Equal(f.got(), []string{"update sonarr"}) {
		t.Fatalf("task = %+v, calls %v", task, f.got())
	}
	ros, _ := e.s.Rollouts(t.Context())
	if len(ros) != 1 || ros[0].State != RolloutSucceeded || !slices.Equal(names(ros[0].Steps), []string{"sonarr:succeeded", "radarr:skipped"}) {
		t.Fatalf("rollout = %+v", ros)
	}
	// Only held ones: nothing to do.
	_, err := eng.Plan(t.Context(), operator, action.PlanRequest{Action: ActRollout, Target: RolloutTarget, Params: json.RawMessage(`{"targets":["` + radarr + `"]}`)})
	var ref *action.Refusal
	if !errors.As(err, &ref) || !strings.Contains(ref.Message, "nothing to update") {
		t.Fatalf("held only = %v", err)
	}
}

// Scheduled into a window, a rollout runs when it opens, as the person who
// scheduled it, if the plan's hash still matches; a changed plan waits for
// a person.
func TestScheduledRollout(t *testing.T) {
	e := newEnv(t)
	e.container("sonarr", integration.UpdateAvailable, "sha256:s1")
	f := &fakeUpdates{}
	eng, _ := e.engine(f)
	e.s.users = func(_ context.Context, id string) (auth.UserInfo, error) {
		return auth.UserInfo{ID: id, Username: "op", Role: "operator"}, nil
	}
	w, err := e.s.CreateWindow(t.Context(), WindowInput{Name: "Sunday", Hosts: []string{"docker:i_a/engine"},
		StartsAt: e.now.Add(time.Hour), EndsAt: e.now.Add(3 * time.Hour), Weekly: true}, "u_op")
	if err != nil {
		t.Fatal(err)
	}
	p, task := rollout(t, eng, operator, `{"window":"`+w.ID+`"}`)
	if task.State != action.StateSucceeded || len(f.got()) != 0 || !strings.Contains(p.Steps[0].Label, "runs in Sunday") {
		t.Fatalf("scheduling = %+v, %v, %+v", task, f.got(), p.Steps)
	}
	ros, _ := e.s.Rollouts(t.Context())
	if len(ros) != 1 || ros[0].State != RolloutScheduled || !ros[0].ScheduledFor.Equal(w.StartsAt) || ros[0].WindowID != w.ID {
		t.Fatalf("scheduled = %+v", ros)
	}
	id := ros[0].ID

	// Before the window: nothing.
	e.s.RunDue(t.Context())
	if len(f.got()) != 0 {
		t.Fatal("ran before its window")
	}
	// In the window, unchanged: it runs.
	e.now = e.now.Add(90 * time.Minute)
	e.s.RunDue(t.Context())
	deadline := time.Now().Add(5 * time.Second)
	for {
		ro, _ := e.s.Rollout(t.Context(), id)
		if ro.State == RolloutSucceeded {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("scheduled rollout = %+v", ro)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !slices.Equal(f.got(), []string{"update sonarr"}) {
		t.Fatalf("calls = %v", f.got())
	}

	// Another, whose plan changes before the window: it waits.
	e.now = e.now.Add(3 * time.Hour) // past this week's window
	e.container("sonarr", integration.UpdateAvailable, "sha256:s2")
	_, task = rollout(t, eng, operator, `{"window":"`+w.ID+`"}`)
	if task.State != action.StateSucceeded {
		t.Fatalf("second scheduling = %+v", task)
	}
	ros, _ = e.s.Rollouts(t.Context())
	second := ros[0]
	if !second.ScheduledFor.Equal(w.StartsAt.Add(week)) {
		t.Fatalf("next opening = %v", second.ScheduledFor)
	}
	e.container("sonarr", integration.UpdateAvailable, "sha256:s3")
	e.now = w.StartsAt.Add(week + time.Minute)
	e.s.RunDue(t.Context())
	ro, _ := e.s.Rollout(t.Context(), second.ID)
	var it item
	raw := e.inbox.Item(incidentsColl, waitingItemID(second.ID))
	if ro.State != RolloutWaiting || raw == nil || json.Unmarshal(raw, &it) != nil || !strings.Contains(it.Summary, "changed since it was scheduled") {
		t.Fatalf("changed = %+v, item %s", ro, raw)
	}
	if len(f.got()) != 1 {
		t.Fatalf("a changed plan ran: %v", f.got())
	}
	// A person runs it, with the new plan.
	_, task = rollout(t, eng, operator, `{"rollout":"`+second.ID+`"}`)
	ro, _ = e.s.Rollout(t.Context(), second.ID)
	if task.State != action.StateSucceeded || ro.State != RolloutSucceeded || len(f.got()) != 2 {
		t.Fatalf("run by a person = %+v %+v", task, ro)
	}
	e.s.settleItems(t.Context())
	if err := json.Unmarshal(e.inbox.Item(incidentsColl, waitingItemID(second.ID)), &it); err != nil || it.Status != "resolved" {
		t.Fatalf("waiting item = %+v", it)
	}
}

// A window is checked, opens weekly, and makes incidents on its hosts
// expected while it's open.
func TestWindows(t *testing.T) {
	e := newEnv(t)
	at := func(s string) time.Time { v, _ := time.Parse(time.RFC3339, s); return v }
	w := MaintenanceWindow{StartsAt: at("2026-11-01T02:00:00Z"), EndsAt: at("2026-11-01T04:00:00Z"), Weekly: true}
	for _, c := range []struct {
		t          string
		open       bool
		start, end string
	}{
		{"2026-10-31T12:00:00Z", false, "2026-11-01T02:00:00Z", "2026-11-01T04:00:00Z"},
		{"2026-11-01T03:00:00Z", true, "2026-11-01T02:00:00Z", "2026-11-01T04:00:00Z"},
		{"2026-11-01T04:00:00Z", false, "2026-11-08T02:00:00Z", "2026-11-08T04:00:00Z"},
		{"2026-11-15T02:30:00Z", true, "2026-11-15T02:00:00Z", "2026-11-15T04:00:00Z"},
	} {
		start, end, ok := w.Occurrence(at(c.t))
		if w.Open(at(c.t)) != c.open || !ok || !start.Equal(at(c.start)) || !end.Equal(at(c.end)) {
			t.Errorf("%s: open %v, %v–%v", c.t, w.Open(at(c.t)), start, end)
		}
	}
	once := MaintenanceWindow{StartsAt: w.StartsAt, EndsAt: w.EndsAt}
	if _, _, ok := once.Occurrence(at("2026-11-02T00:00:00Z")); ok || once.Open(at("2026-11-08T03:00:00Z")) {
		t.Fatal("a one-off window came back")
	}

	for _, in := range []WindowInput{
		{Name: "", Hosts: []string{"h"}, StartsAt: w.StartsAt, EndsAt: w.EndsAt},
		{Name: "x", StartsAt: w.StartsAt, EndsAt: w.EndsAt},
		{Name: "x", Hosts: []string{"h"}, StartsAt: w.EndsAt, EndsAt: w.StartsAt},
		{Name: "x", Hosts: []string{"h"}, StartsAt: w.StartsAt, EndsAt: w.StartsAt.Add(8 * 24 * time.Hour), Weekly: true},
	} {
		var inv *InvalidError
		if _, err := e.s.CreateWindow(t.Context(), in, ""); !errors.As(err, &inv) {
			t.Errorf("%+v: %v", in, err)
		}
	}

	// sonarr runs on the engine, which runs on pvedocker.
	sonarr := e.container("sonarr", integration.UpdateAvailable, "sha256:s1")
	e.upsert(graph.Resource{ID: "proxmox:i_p/qemu/101", Kind: "vm", Name: "pvedocker"})
	e.relate("docker:i_a/engine", "runs_on", "proxmox:i_p/qemu/101")
	e.now = at("2026-10-31T12:00:00Z")
	mw, err := e.s.CreateWindow(t.Context(), WindowInput{Name: " Sunday night ", Hosts: []string{"proxmox:i_p/qemu/101", "proxmox:i_p/qemu/101"},
		StartsAt: w.StartsAt, EndsAt: w.EndsAt, Weekly: true}, "u_op")
	if err != nil || mw.Name != "Sunday night" || !slices.Equal(mw.Hosts, []string{"proxmox:i_p/qemu/101"}) || mw.CreatedBy != "op" {
		t.Fatalf("window = %+v %v", mw, err)
	}
	if _, ok := e.s.Expected(sonarr, at("2026-11-01T01:59:00Z")); ok {
		t.Fatal("expected before the window")
	}
	if id, ok := e.s.Expected(sonarr, at("2026-11-08T03:00:00Z")); !ok || id != mw.ID {
		t.Fatal("not expected in the window")
	}
	if e.s.ExpectedNow("docker:i_b/container/other", at("2026-11-08T03:00:00Z")) {
		t.Fatal("expected on another host")
	}
	if err := e.s.DeleteWindow(t.Context(), mw.ID); err != nil {
		t.Fatal(err)
	}
	if e.s.ExpectedNow(sonarr, at("2026-11-08T03:00:00Z")) {
		t.Fatal("a deleted window still counts")
	}
	if err := e.s.DeleteWindow(t.Context(), mw.ID); !errors.Is(err, ErrNoWindow) {
		t.Fatalf("delete twice = %v", err)
	}
}

// The routes beyond the list, and that each change is audited
// (server.TestAuditedRoutesRecord names this test).
func TestRolloutRoutes(t *testing.T) {
	e := newEnv(t)
	sonarr := e.container("sonarr", integration.UpdateAvailable, "sha256:s1")
	log := audit.New(e.st)
	e.s.Audit = &authz.Recorder{Log: log}
	mux := http.NewServeMux()
	e.s.Register(mux, func(h http.Handler) http.Handler { return h })
	do := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		mux.ServeHTTP(w, r)
		return w
	}
	if w := do("POST", "/api/updates/rollouts/preview", `{"targets":[]}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"name":"sonarr"`) {
		t.Fatalf("preview %d %s", w.Code, w.Body)
	}
	if w := do("GET", "/api/updates/settings", ""); w.Code != 200 || w.Body.String() != "{\"release_notes\":false}\n" {
		t.Fatalf("settings %d %s", w.Code, w.Body)
	}
	if w := do("GET", "/api/updates/"+escape(sonarr)+"/notes", ""); w.Code != 409 || !strings.Contains(w.Body.String(), "release_notes_off") {
		t.Fatalf("notes off %d %s", w.Code, w.Body)
	}
	if w := do("PUT", "/api/updates/settings", `{"release_notes":true}`); w.Code != 200 {
		t.Fatalf("settings put %d %s", w.Code, w.Body)
	}
	if w := do("GET", "/api/updates/"+escape(sonarr)+"/notes", ""); w.Code != 404 || !strings.Contains(w.Body.String(), "no_source") {
		t.Fatalf("no source %d %s", w.Code, w.Body)
	}
	if w := do("POST", "/api/windows", `{"name":"x","hosts":[],"starts_at":"2026-11-01T02:00:00Z","ends_at":"2026-11-01T04:00:00Z"}`); w.Code != 422 ||
		!strings.Contains(w.Body.String(), `"id":"updates.window_hosts"`) {
		t.Fatalf("bad window %d %s", w.Code, w.Body)
	}
	w := do("POST", "/api/windows", `{"name":"Sunday","hosts":["docker:i_a/engine"],"starts_at":"2026-11-01T02:00:00Z","ends_at":"2026-11-01T04:00:00Z","weekly":true}`)
	var win WindowView
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &win) != nil || win.Open || !win.NextOpen.Equal(win.StartsAt) {
		t.Fatalf("create window %d %s", w.Code, w.Body)
	}
	if w := do("PUT", "/api/windows/"+win.ID, `{"name":"Sundays","hosts":["docker:i_a/engine"],"starts_at":"2026-11-01T03:00:00Z","ends_at":"2026-11-01T04:00:00Z","weekly":true}`); w.Code != 200 ||
		!strings.Contains(w.Body.String(), `"name":"Sundays"`) {
		t.Fatalf("update window %d %s", w.Code, w.Body)
	}
	if w := do("GET", "/api/windows", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"name":"Sundays"`) {
		t.Fatalf("windows %d %s", w.Code, w.Body)
	}
	// A scheduled rollout, cancelled.
	ro := Rollout{ID: newRolloutID(), CreatedBy: "u_op", PlanHash: "sha256:x", State: RolloutScheduled, WindowID: win.ID, ScheduledFor: win.StartsAt,
		Steps: []RolloutStep{{Target: sonarr, Name: "sonarr", Action: "docker.container.update", State: StepPending}}, CreatedAt: e.now}
	if err := e.s.insertRollout(t.Context(), ro); err != nil {
		t.Fatal(err)
	}
	if w := do("GET", "/api/updates/rollouts", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"scheduled"`) || !strings.Contains(w.Body.String(), `"created_by":"op"`) {
		t.Fatalf("rollouts %d %s", w.Code, w.Body)
	}
	if w := do("POST", "/api/updates/rollouts/"+ro.ID+"/cancel", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"cancelled"`) {
		t.Fatalf("cancel %d %s", w.Code, w.Body)
	}
	if w := do("POST", "/api/updates/rollouts/"+ro.ID+"/cancel", ""); w.Code != 409 {
		t.Fatalf("cancel twice %d %s", w.Code, w.Body)
	}
	if w := do("DELETE", "/api/windows/"+win.ID, ""); w.Code != 204 {
		t.Fatalf("delete window %d %s", w.Code, w.Body)
	}
	if w := do("DELETE", "/api/windows/"+win.ID, ""); w.Code != 404 {
		t.Fatalf("delete twice %d", w.Code)
	}
	page, err := log.Query(t.Context(), audit.Filter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, ev := range slices.Backward(page.Events) {
		got = append(got, ev.Action)
	}
	want := []string{"settings.updates.update", "settings.updates.window.create", "settings.updates.window.update", "update.rollout.cancel", "settings.updates.window.delete"}
	if !slices.Equal(got, want) {
		t.Fatalf("audit = %v", got)
	}
}

func escape(id string) string { return strings.ReplaceAll(id, "/", "%2F") }
