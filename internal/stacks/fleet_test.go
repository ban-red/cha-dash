package stacks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/audit"
	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/livedoc"
	"dash.cha.sh/internal/node"
	nodestacks "dash.cha.sh/internal/node/stacks"
	"dash.cha.sh/internal/node/stacks/dockerapi"
	"dash.cha.sh/internal/node/stacks/stackstest"
	"dash.cha.sh/internal/nodeproto"
	"dash.cha.sh/internal/stackproto"
	"dash.cha.sh/internal/store"
	"dash.cha.sh/internal/vault"
)

// fleetCaller dispatches to in-process modules as localCaller does, but as
// a real transport would in two more ways: a call returns when its context
// ends (the node goes on with the work), and the node runtime's own
// refusals keep their nodeproto code. A stack verb's errors are flattened
// to text.
type fleetCaller struct {
	mu      sync.Mutex
	modules map[string]node.Module
}

func (c *fleetCaller) Call(ctx context.Context, nodeID, verb string, req any, progress func(json.RawMessage)) (json.RawMessage, error) {
	c.mu.Lock()
	m := c.modules[nodeID]
	c.mu.Unlock()
	if m == nil {
		return nil, errors.New("no such node")
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	type answer struct {
		out json.RawMessage
		err error
	}
	done := make(chan answer, 1)
	go func() {
		for _, v := range m.Verbs() {
			if v.Name == verb {
				out, err := v.Handle(ctx, raw, func(p json.RawMessage) {
					if ctx.Err() == nil {
						progress(p)
					}
				})
				if _, ok := errors.AsType[*nodeproto.Error](err); ok {
					done <- answer{nil, fmt.Errorf("node %s: %w", nodeID, err)}
				} else if err != nil {
					done <- answer{nil, errors.New("node " + nodeID + ": " + err.Error())}
				} else {
					done <- answer{out, nil}
				}
				return
			}
		}
		done <- answer{nil, errors.New("unknown verb " + verb)}
	}()
	select {
	case a := <-done:
		return a.out, a.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// policed is a module behind the node's policy, as the node runtime serves
// it: a verb the policy doesn't grant is refused before it runs.
type policed struct {
	m node.Module
	d *node.Dispatcher
}

func (p policed) Name() string { return p.m.Name() }
func (p policed) Verbs() []node.Verb {
	vs := p.m.Verbs()
	for i := range vs {
		name := vs[i].Name
		vs[i].Handle = func(ctx context.Context, raw json.RawMessage, progress func(json.RawMessage)) (json.RawMessage, error) {
			return p.d.Call(ctx, name, raw, progress)
		}
	}
	return vs
}

type fleetHub struct {
	*hubEnv
	reg    *action.Registry
	fc     *fleetCaller
	module *nodestacks.Module
}

// newFleetHub is newHub with the fleet actions, a node beside the hub (in
// the compose project chadash, which runs the hub and the node), and an
// engine that can be restarted.
func newFleetHub(t *testing.T, selfupdate bool) *fleetHub {
	t.Helper()
	st, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "chadash.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	v, err := vault.New(st, bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	h := &fleetHub{hubEnv: &hubEnv{t: t, st: st, vault: v, docker: stackstest.New(), online: true, root: t.TempDir(), inbox: livedoc.New(incidentsColl), g: graph.New()}}
	var mu sync.Mutex
	clock := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	h.module, err = nodestacks.New(nodestacks.Options{
		Root: h.root, Docker: h.docker, Soak: 20 * time.Second, Poll: 2 * time.Second,
		SelfProject: func(context.Context) (string, error) { return "chadash", nil },
		SelfUpdate:  h.docker,
		Now:         func() time.Time { mu.Lock(); defer mu.Unlock(); return clock },
		Sleep: func(_ context.Context, d time.Duration) error {
			mu.Lock()
			defer mu.Unlock()
			clock = clock.Add(d)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	policy := node.DefaultPolicy()
	if selfupdate {
		policy.Stacks.Allow = append(policy.Stacks.Allow, "selfupdate")
	}
	d, err := node.NewDispatcher(policy, h.module)
	if err != nil {
		t.Fatal(err)
	}
	h.fc = &fleetCaller{modules: map[string]node.Module{"local": policed{h.module, d}}}
	h.svc, err = New(Options{
		Store: st, Vault: v, Caller: h.fc, Graph: h.g, Inbox: h.inbox,
		Nodes: func(context.Context) ([]Node, error) {
			return []Node{{ID: "local", Name: "jupiter", Online: h.online}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	h.reg = action.NewRegistry()
	if err := h.reg.Register(ActionSource, h.svc.Actions()...); err != nil {
		t.Fatal(err)
	}
	if err := h.reg.Register(FleetSource, h.svc.FleetActions()...); err != nil {
		t.Fatal(err)
	}
	h.start()
	// The hub and the node, as deploy/compose.yaml runs them.
	h.docker.AddContainer("chadash", dockerapi.Container{Name: "chadash-chadash-1", Service: "chadash", Image: "ghcr.io/ban-red/chadash:0.2.0", State: "running", Health: "healthy"})
	h.docker.AddContainer("chadash", dockerapi.Container{Name: "chadash-chadash-node-1", Service: "chadash-node", Image: "ghcr.io/ban-red/chadash-node:0.2.0", State: "running"})
	return h
}

// start starts an engine on the hub's database: at boot, or after a restart.
func (h *fleetHub) start() {
	h.t.Helper()
	eng, err := action.New(h.t.Context(), action.Options{Store: h.st, Audit: audit.New(h.st), Registry: h.reg, Policy: action.AllowAll{}, Graph: h.g})
	if err != nil {
		h.t.Fatal(err)
	}
	h.eng = eng
	h.t.Cleanup(eng.Close)
}

func (h *fleetHub) wait(id string) action.Task {
	h.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		t, err := h.eng.Task(h.t.Context(), sam, id)
		if err != nil {
			h.t.Fatal(err)
		}
		if t.Finished() && t.Audit != nil {
			return t
		}
		time.Sleep(2 * time.Millisecond)
	}
	h.t.Fatalf("task %s didn't finish", id)
	return action.Task{}
}

// A project another tool runs moves into the root through the preview and
// the action, nothing restarts, the stack says where the old copy is, and
// un-adopting (the move's undo) keeps both copies.
func TestMoveThroughTheHub(t *testing.T) {
	h := newFleetHub(t, false)
	dir := filepath.Join(t.TempDir(), "srv", "app")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "compose.yaml")
	content := "services:\n  app:\n    image: nginx:1.28\n    volumes:\n      - ./data:/data\n"
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	h.docker.AddProject("app", []string{file})

	// The list says it can move.
	list := decode[struct {
		Nodes []NodeView `json:"nodes"`
	}](t, h.do("GET", "/api/stacks", nil))
	var found bool
	for _, d := range list.Nodes[0].Detected {
		if d.Name == "app" {
			found = d.Moveable && d.MoveRefusal == ""
		}
	}
	if !found {
		t.Fatalf("detected %+v", list.Nodes[0].Detected)
	}

	// The preview: the rewrite and the plan, and nothing saved.
	w := h.do("POST", "/api/stacks/local/app/move/preview", map[string]string{"from_path": file})
	pv := decode[MovePreview](t, w)
	if w.Code != http.StatusOK || !pv.OK || len(pv.Rewrites) != 1 || pv.Rewrites[0].Line != 5 || pv.Rewrites[0].To != dir+"/data:/data" ||
		pv.Plan == nil || pv.Plan.Services[0].Action != stackproto.ActionCreate || pv.Path != filepath.Join(h.root, "app") {
		t.Fatalf("preview %d %+v", w.Code, pv)
	}
	if _, err := os.Stat(filepath.Join(h.root, "app")); !os.IsNotExist(err) {
		t.Fatal("the preview saved something")
	}
	// A refusal is part of the review, not an error.
	w = h.do("POST", "/api/stacks/local/web/move/preview", map[string]string{"content": "services:\n  web:\n    image: nginx:1.28\n    volumes:\n      - ./html:/html\n"})
	if pv := decode[MovePreview](t, w); w.Code != http.StatusOK || pv.OK || pv.Refusal == nil || pv.Refusal.Code != stackproto.CodeInvalid || !strings.Contains(pv.Refusal.Message, "line 5") {
		t.Fatalf("refusal %d %+v", w.Code, pv)
	}
	if w := h.do("POST", "/api/stacks/local/app/move/preview", map[string]string{}); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("neither: %d", w.Code)
	}

	// The move: T1, undo un-adopts.
	p, task := h.run(ActionMove, Target("local", "app"), fmt.Sprintf(`{"from_path":%q}`, file))
	if task.State != action.StateSucceeded || p.Undo == nil || p.Undo.Action != ActionUnadopt || p.Tier != action.T1 {
		t.Fatalf("move: %+v %+v", p, task)
	}
	if h.docker.Called("up ") != 0 || h.docker.Called("pull") != 0 {
		t.Fatal("moving ran something")
	}
	d := decode[StackDetail](t, h.do("GET", "/api/stacks/local/app", nil))
	if !d.Managed || d.Location != stackproto.LocationRoot || d.MovedFrom == nil || *d.MovedFrom != file || d.History[0].Kind != stackproto.KindAdopt {
		t.Fatalf("detail %+v", d)
	}
	// Moving it again: it's in the root already.
	if _, err := h.eng.Plan(t.Context(), sam, action.PlanRequest{Action: ActionMove, Target: Target("local", "app"), Params: json.RawMessage(fmt.Sprintf(`{"from_path":%q}`, file))}); err == nil || !strings.Contains(err.Error(), "already in the stacks root") {
		t.Fatalf("again: %v", err)
	}
	// A relative from_path is refused by the schema.
	if _, err := h.eng.Plan(t.Context(), sam, action.PlanRequest{Action: ActionMove, Target: Target("local", "app"), Params: json.RawMessage(`{"from_path":"srv/app"}`)}); err == nil {
		t.Fatal("a relative from_path was planned")
	}

	_, undo := h.run(ActionUnadopt, Target("local", "app"), "")
	if undo.State != action.StateSucceeded {
		t.Fatalf("undo %+v", undo)
	}
	for _, f := range []string{file, filepath.Join(h.root, "app", "compose.yaml")} {
		if _, err := os.Stat(f); err != nil {
			t.Errorf("un-adopting removed %s", f)
		}
	}
}

// update plans and approves chadash.update, typing "Cha Dash" as the T3
// ladder asks, and waits for its task.
func (h *fleetHub) update(params string) (*action.Plan, action.Task) {
	h.t.Helper()
	p, err := h.eng.Plan(h.t.Context(), sam, action.PlanRequest{Action: ActionSelfUpdate, Target: "node:local", Params: json.RawMessage(params)})
	if err != nil {
		h.t.Fatalf("plan: %v", err)
	}
	if p.ConfirmText != "Cha Dash" {
		h.t.Fatalf("confirm text %q", p.ConfirmText)
	}
	task, err := h.eng.Approve(h.t.Context(), sam, p.ID, action.ApproveRequest{Hash: p.Hash, Typed: "Cha Dash"}, "")
	if err != nil {
		h.t.Fatalf("approve: %v", err)
	}
	return p, task
}

func selfUpdateStatusOf(t *testing.T, h *fleetHub) SelfUpdateStatus {
	t.Helper()
	w := h.do("GET", "/api/nodes/local/self-update", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("self-update status: %d %s", w.Code, w.Body)
	}
	return decode[SelfUpdateStatus](t, w)
}

func TestSelfUpdateThroughTheHub(t *testing.T) {
	h := newFleetHub(t, true)
	if st := selfUpdateStatusOf(t, h); !st.Allowed || !st.Available || st.Running != "ghcr.io/ban-red/chadash:0.2.0" || st.Last != nil {
		t.Fatalf("before: %+v", st)
	}
	p, started := h.update(`{"image":"0.3.0"}`)
	task := h.wait(started.ID)
	if task.State != action.StateSucceeded || p.Tier != action.T3 || p.Undo != nil || p.Label != "Update Cha Dash to 0.3.0" {
		t.Fatalf("update: %+v %+v", p, task)
	}
	if len(p.Impact) == 0 || p.Impact[0].Resource != action.HubResource {
		t.Fatalf("impact %+v", p.Impact)
	}
	if !strings.HasPrefix(h.docker.Running("chadash")["chadash"], "ghcr.io/ban-red/chadash:0.3.0@sha256:") || h.docker.Running("chadash")["chadash-node"] != "ghcr.io/ban-red/chadash-node:0.2.0" {
		t.Fatalf("running %v", h.docker.Running("chadash"))
	}
	st := selfUpdateStatusOf(t, h)
	if st.Last == nil || st.Last.Outcome != stackproto.OutcomeDeployed || st.Last.Ref != task.ID {
		t.Fatalf("after: %+v", st)
	}

	// A bad image rolls back on its own, and the task says so.
	_, badTask := h.update(`{"image":"0.3.1-unhealthy"}`)
	bad := h.wait(badTask.ID)
	if bad.State != action.StateFailed || bad.Error == nil || bad.Error.Code != "rolled_back" {
		t.Fatalf("bad: %+v", bad)
	}
	if !strings.HasPrefix(h.docker.Running("chadash")["chadash"], "ghcr.io/ban-red/chadash:0.3.0@sha256:") {
		t.Fatalf("after the rollback: %v", h.docker.Running("chadash"))
	}

	// The node's own image, or another, isn't a param.
	for _, params := range []string{`{"image":"ghcr.io/ban-red/chadash-node:0.3.0"}`, `{"image":"nginx:1"}`, `{}`, `{"image":"0.3.0","node":"x"}`} {
		if _, err := h.eng.Plan(t.Context(), sam, action.PlanRequest{Action: ActionSelfUpdate, Target: "node:local", Params: json.RawMessage(params)}); err == nil {
			t.Errorf("%s was planned", params)
		}
	}
	if _, err := h.eng.Plan(t.Context(), sam, action.PlanRequest{Action: ActionSelfUpdate, Target: "node:nowhere", Params: json.RawMessage(`{"image":"0.3.0"}`)}); err == nil {
		t.Error("an unknown node was planned")
	}
}

func TestSelfUpdateOffByPolicy(t *testing.T) {
	h := newFleetHub(t, false)
	if st := selfUpdateStatusOf(t, h); st.Allowed || st.Available || !strings.Contains(st.Reason, "stacks.selfupdate") {
		t.Fatalf("status %+v", st)
	}
	_, err := h.eng.Plan(t.Context(), sam, action.PlanRequest{Action: ActionSelfUpdate, Target: "node:local", Params: json.RawMessage(`{"image":"0.3.0"}`)})
	var ref *action.Refusal
	if !errors.As(err, &ref) || ref.Code != "unavailable" || !strings.Contains(ref.Message, "stacks.selfupdate") {
		t.Fatalf("plan: %v", err)
	}
	if h.docker.Called("recreate") != 0 {
		t.Fatal("recreated something")
	}
}

// The hub restarts mid-update (the node recreates it): the task isn't
// interrupted; the next start asks the node how it ended and finishes the
// task, audit record included.
func TestSelfUpdateSurvivesTheHubRestarting(t *testing.T) {
	h := newFleetHub(t, true)
	old := resumePoll
	resumePoll = 5 * time.Millisecond
	defer func() { resumePoll = old }()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	h.docker.OnRecreate = func(string) {
		once.Do(func() {
			close(entered)
			<-release
		})
	}
	_, task := h.update(`{"image":"0.3.0"}`)
	<-entered
	// The node stops the hub to recreate it: the hub shuts down.
	h.eng.Close()
	var state string
	if err := h.st.Reader().QueryRow(`SELECT state FROM tasks WHERE id = ?`, task.ID).Scan(&state); err != nil || state != action.StateRunning {
		t.Fatalf("after the hub stopped: %s %v", state, err)
	}
	// The hub is back before the node has finished: Resume waits for it.
	h.start()
	time.Sleep(20 * time.Millisecond)
	if got, _ := h.eng.Task(t.Context(), sam, task.ID); got.Finished() {
		t.Fatalf("finished before the node did: %+v", got)
	}
	close(release)
	done := h.wait(task.ID)
	if done.State != action.StateSucceeded || done.Error != nil || done.Audit == nil || done.Step != "Ask the node how the update ended" {
		t.Fatalf("resumed: %+v", done)
	}
	var last stackproto.SelfUpdateResult
	if st := selfUpdateStatusOf(t, h); st.Last != nil {
		last = *st.Last
	}
	if last.Outcome != stackproto.OutcomeDeployed || last.Ref != task.ID {
		t.Fatalf("last %+v", last)
	}

	// A task the node never started (the hub stopped before the call got
	// there) ends interrupted, with nothing changed.
	h.docker.OnRecreate = nil
	if _, err := h.st.Writer().Exec(`UPDATE tasks SET state = ?, finished_at = NULL, document = json_remove(json_set(document, '$.state', ?), '$.finished_at', '$.audit', '$.error') WHERE id = ?`, action.StateRunning, action.StateRunning, task.ID); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(stackproto.SelfUpdateResult{Outcome: stackproto.OutcomeDeployed, Ref: "t_another"})
	if err := os.WriteFile(filepath.Join(h.root, ".chadash", "selfupdate.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	h.eng.Close()
	h.start()
	if again := h.wait(task.ID); again.State != action.StateFailed || again.Error == nil || again.Error.Code != "interrupted" {
		t.Fatalf("never started: %+v", again)
	}
}
