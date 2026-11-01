package updates

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/audit"
	"dash.cha.sh/internal/authz"
	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/integration"
)

// routes mounts the Update Center's API with an audit log.
func (e *env) routes() (func(method, path, body string) *httptest.ResponseRecorder, *audit.Log) {
	log := audit.New(e.st)
	e.s.Audit = &authz.Recorder{Log: log}
	mux := http.NewServeMux()
	e.s.Register(mux, func(h http.Handler) http.Handler { return h })
	return func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		mux.ServeHTTP(w, r)
		return w
	}, log
}

func auditActions(t *testing.T, log *audit.Log) []string {
	t.Helper()
	page, err := log.Query(t.Context(), audit.Filter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, ev := range slices.Backward(page.Events) {
		got = append(got, ev.Action)
	}
	return got
}

// A running rollout cancelled: the step in progress finishes, the rest
// don't start, the rollout and its task read cancelled, and it's audited.
func TestCancelRunningRollout(t *testing.T) {
	e := newEnv(t)
	for _, n := range []string{"bazarr", "lidarr", "radarr"} {
		e.container(n, integration.UpdateAvailable, "sha256:"+n)
	}
	started, release := make(chan string, 4), make(chan struct{})
	reg := action.NewRegistry()
	if err := reg.Register("fake", action.Def{ID: "docker.container.update", Targets: []string{"container"}, Tier: action.T2,
		Execute: func(ctx context.Context, r action.Request, _ *action.TaskRun) error {
			started <- r.Target.Name
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(ActionSource, e.s.Actions()...); err != nil {
		t.Fatal(err)
	}
	e.s.reg = reg
	alog := audit.New(e.st)
	eng, err := action.New(t.Context(), action.Options{Store: e.st, Audit: alog, Registry: reg, Policy: action.AllowAll{}, Graph: e.g})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(eng.Close)
	do, log := e.routes()

	p, err := eng.Plan(t.Context(), operator, action.PlanRequest{Action: ActRollout, Target: RolloutTarget, Params: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	task, err := eng.Approve(t.Context(), operator, p.ID, action.ApproveRequest{Hash: p.Hash}, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := <-started; got != "bazarr" {
		t.Fatalf("first step = %s", got)
	}
	ros, _ := e.s.Rollouts(t.Context())
	if len(ros) != 1 || ros[0].State != RolloutRunning {
		t.Fatalf("rollouts = %+v", ros)
	}
	id := ros[0].ID
	ro, err := e.s.CancelRollout(t.Context(), id, "op")
	if err != nil || !ro.Cancelling || ro.State != RolloutRunning {
		t.Fatalf("cancel = %+v %v", ro, err)
	}
	// Asked twice: it's already stopping.
	if w := do("POST", "/api/updates/rollouts/"+id+"/cancel", ""); w.Code != 409 || !strings.Contains(w.Body.String(), `"stopping"`) {
		t.Fatalf("cancel twice %d %s", w.Code, w.Body)
	}
	if got, _ := e.s.Rollout(t.Context(), id); !got.Cancelling {
		t.Fatalf("read back = %+v", got)
	}
	close(release) // bazarr's update finishes on its own
	done := wait(t, eng, operator, task.ID)
	if done.State != action.StateCancelled || done.Error == nil || done.Error.Code != action.CodeCancelled ||
		done.Error.Message != "Cancelled by op. Updated: bazarr. Didn't start: lidarr and radarr." {
		t.Fatalf("task = %+v %+v", done, done.Error)
	}
	if len(started) != 0 {
		t.Fatalf("a step started after the cancel: %s", <-started)
	}
	ro, _ = e.s.Rollout(t.Context(), id)
	if ro.State != RolloutCancelled || ro.Cancelling || ro.FinishedAt.IsZero() ||
		!slices.Equal(names(ro.Steps), []string{"bazarr:succeeded", "lidarr:not_started", "radarr:not_started"}) ||
		ro.Steps[1].Reason != "Cancelled by op before this step." {
		t.Fatalf("rollout = %+v", ro)
	}
	if raw := e.inbox.Item(incidentsColl, failedItemID(id)); raw != nil {
		t.Fatalf("a cancelled rollout told the Inbox it failed: %s", raw)
	}
	// Ended: nothing to cancel.
	if w := do("POST", "/api/updates/rollouts/"+id+"/cancel", ""); w.Code != 409 || !strings.Contains(w.Body.String(), `"not_pending"`) {
		t.Fatalf("cancel ended %d %s", w.Code, w.Body)
	}

	// Through the route, a running one answers 202 and is audited.
	e.container("bazarr", integration.UpdateAvailable, "sha256:bazarr2")
	release = make(chan struct{})
	p, err = eng.Plan(t.Context(), operator, action.PlanRequest{Action: ActRollout, Target: RolloutTarget, Params: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	task, err = eng.Approve(t.Context(), operator, p.ID, action.ApproveRequest{Hash: p.Hash}, "")
	if err != nil {
		t.Fatal(err)
	}
	<-started
	ros, _ = e.s.Rollouts(t.Context())
	i := slices.IndexFunc(ros, func(r Rollout) bool { return r.State == RolloutRunning })
	if i < 0 {
		t.Fatalf("rollouts = %+v", ros)
	}
	w := do("POST", "/api/updates/rollouts/"+ros[i].ID+"/cancel", "")
	if w.Code != 202 || !strings.Contains(w.Body.String(), `"cancelling":true`) {
		t.Fatalf("cancel running %d %s", w.Code, w.Body)
	}
	close(release)
	if done := wait(t, eng, operator, task.ID); done.State != action.StateCancelled {
		t.Fatalf("second task = %+v", done)
	}
	page, _ := log.Query(t.Context(), audit.Filter{Limit: 100})
	var cancels []string
	for _, ev := range page.Events {
		if ev.Action == "update.rollout.cancel" {
			cancels = append(cancels, string(ev.Params))
		}
	}
	if len(cancels) != 1 || !strings.Contains(cancels[0], `"running":true`) {
		t.Fatalf("audit = %v", cancels)
	}
}

// A rollout whose update rolls back remembers the digest: the row reads
// rolled back with the reason, Update all and a selection leave it out,
// it survives a restart, and a newer digest or Try again ends it.
func TestRejectedDigests(t *testing.T) {
	e := newEnv(t)
	e.container("sonarr", integration.UpdateAvailable, "sha256:s1")
	canary := e.container("canary", integration.UpdateAvailable, "sha256:broken0000000000")
	f := &fakeUpdates{fail: "canary"}
	eng, _ := e.engine(f)
	_, task := rollout(t, eng, operator, `{}`)
	if task.State != action.StateFailed {
		t.Fatalf("task = %+v", task)
	}
	ros, _ := e.s.Rollouts(t.Context())
	row := e.list()["canary"]
	if row.State != StateRolledBack || row.Rejected == nil || row.Rejected.Digest != "sha256:broken0000000000" || row.Rejected.Rollout != ros[0].ID ||
		!strings.Contains(row.Rejected.Reason, "didn't come up healthy") || row.Update == nil {
		t.Fatalf("row = %+v %+v", row, row.Rejected)
	}
	// Not counted as available in the Inbox.
	e.s.Recount(t.Context())
	if it, ok := e.item(); ok && it.Status == "open" && slices.Contains(it.Facts["containers"].([]any), any("canary")) {
		t.Fatalf("inbox = %+v", it)
	}

	// Update all, and a selection naming it, leave it out with the reason.
	e.container("sonarr", integration.UpdateAvailable, "sha256:s2")
	for _, p := range []RolloutPlan{e.plan(), e.plan(canary, "docker:i_a/container/sonarr")} {
		if got := names(p.Steps); !slices.Equal(got, []string{"sonarr:pending", "canary:skipped"}) ||
			!strings.HasPrefix(p.Steps[1].Reason, "canary's update to broken000000 rolled back: canary didn't come up healthy") {
			t.Fatalf("plan = %v %q", got, p.Steps[1].Reason)
		}
	}
	if _, err := eng.Plan(t.Context(), operator, action.PlanRequest{Action: ActRollout, Target: RolloutTarget,
		Params: json.RawMessage(`{"targets":["` + canary + `"]}`)}); err == nil || !strings.Contains(err.Error(), "nothing to update") {
		t.Fatalf("only the rejected one = %v", err)
	}

	// A new hub reads it back.
	s2, err := New(Options{Store: e.st, Graph: e.g, Now: func() time.Time { return e.now }})
	if err != nil {
		t.Fatal(err)
	}
	if r, _ := s2.row(t.Context(), canary); r.State != StateRolledBack {
		t.Fatalf("after a restart = %+v", r)
	}

	// Try again, through the route: offered again, and audited.
	do, log := e.routes()
	path := "/api/updates/" + url.PathEscape(canary) + "/rejected"
	if w := do("DELETE", path, ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"available"`) || strings.Contains(w.Body.String(), `"rejected"`) {
		t.Fatalf("try again %d %s", w.Code, w.Body)
	}
	if w := do("DELETE", path, ""); w.Code != 404 || !strings.Contains(w.Body.String(), "not_rejected") {
		t.Fatalf("try again twice %d %s", w.Code, w.Body)
	}
	if got := auditActions(t, log); got[len(got)-1] != "update.retry" || slices.Index(got, "update.retry") != len(got)-1 {
		t.Fatalf("audit = %v", got)
	}

	// A single update that rolls back (the engine's OnTaskDone) remembers
	// it too; a newer digest from the registry ends it.
	e.s.TaskDone(action.TaskDone{Task: action.Task{Action: "docker.container.update", Target: canary, State: action.StateFailed,
		Error: &action.Error{Code: "rolled_back", Message: "It didn't come up healthy."}}})
	deadline := time.Now().Add(5 * time.Second)
	for e.list()["canary"].State != StateRolledBack {
		if time.Now().After(deadline) {
			t.Fatal("a rolled-back single update wasn't remembered")
		}
		time.Sleep(5 * time.Millisecond)
	}
	e.container("canary", integration.UpdateAvailable, "sha256:fixed")
	if row := e.list()["canary"]; row.State != integration.UpdateAvailable {
		t.Fatalf("newer digest = %+v", row)
	}
	e.s.Recount(t.Context())
	var n int
	if err := e.st.Reader().QueryRow(`SELECT COUNT(*) FROM update_rejections`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("rejections kept = %d %v", n, err)
	}
	// Other failures aren't remembered.
	e.s.TaskDone(action.TaskDone{Task: action.Task{Action: "docker.container.update", Target: canary, State: action.StateFailed,
		Error: &action.Error{Code: "node_unavailable", Message: "x"}}})
	if err := e.s.Reject(t.Context(), "docker:i_a/container/ghost", "", "", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown = %v", err)
	}
}

// A window's hosts must be ones the graph has; one it already had may be
// missing for a while without blocking an edit.
func TestWindowHosts(t *testing.T) {
	e := newEnv(t)
	start := e.now.Add(time.Hour)
	in := WindowInput{Name: "Sunday", Hosts: []string{"docker:i_a/engine", "proxmox:i_p/qemu/999"}, StartsAt: start, EndsAt: start.Add(time.Hour), Weekly: true}
	var inv *InvalidError
	if _, err := e.s.CreateWindow(t.Context(), in, ""); !errors.As(err, &inv) || inv.M.ID != "updates.window_unknown_host" ||
		!strings.Contains(inv.Error(), "proxmox:i_p/qemu/999") {
		t.Fatalf("unknown host = %v", err)
	}
	e.upsert(graph.Resource{ID: "proxmox:i_p/qemu/999", Kind: "vm", Name: "gone-soon"})
	w, err := e.s.CreateWindow(t.Context(), in, "")
	if err != nil {
		t.Fatal(err)
	}
	// The guest leaves the graph; renaming the window still works.
	if _, err := e.g.Update(func(tx *graph.Tx) error { tx.Remove("proxmox:i_p/qemu/999"); return nil }); err != nil {
		t.Fatal(err)
	}
	in.Name = "Sundays"
	if w, err = e.s.UpdateWindow(t.Context(), w.ID, in); err != nil || w.Name != "Sundays" {
		t.Fatalf("edit with a missing host = %+v %v", w, err)
	}
	// But a new unknown host is refused.
	in.Hosts = append(in.Hosts, "docker:i_z/engine")
	if _, err := e.s.UpdateWindow(t.Context(), w.ID, in); !errors.As(err, &inv) || inv.M.ID != "updates.window_unknown_host" {
		t.Fatalf("new unknown host = %v", err)
	}
	if _, err := e.s.UpdateWindow(t.Context(), "mw_nope", in); !errors.Is(err, ErrNoWindow) {
		t.Fatalf("no window = %v", err)
	}
}
