package stacks

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/stackproto"
)

const withVolume = `services:
  web:
    image: nginx:1.27
    environment:
      DB_PASSWORD: ${secret:db_password}
    volumes:
      - data:/data
volumes:
  data: {}
`

func refusal(err error) string {
	if r, ok := errors.AsType[*action.Refusal](err); ok {
		return r.Code
	}
	return ""
}

func hasStep(p *action.Plan, sub string) bool {
	return slices.ContainsFunc(p.Steps, func(s action.Step) bool { return strings.Contains(s.Label, sub) })
}

// runTyped plans and approves a T3 action with its typed name.
func (h *hubEnv) runTyped(id, target, params, typed string) (*action.Plan, action.Task) {
	h.t.Helper()
	p, err := h.eng.Plan(h.t.Context(), sam, action.PlanRequest{Action: id, Target: target, Params: json.RawMessage(params)})
	if err != nil {
		h.t.Fatalf("plan %s: %v", id, err)
	}
	task, err := h.eng.Approve(h.t.Context(), sam, p.ID, action.ApproveRequest{Hash: p.Hash, Typed: typed}, "")
	if err != nil {
		h.t.Fatalf("approve %s: %v", id, err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		t, err := h.eng.Task(h.t.Context(), sam, task.ID)
		if err != nil {
			h.t.Fatal(err)
		}
		if t.Finished() && t.Audit != nil {
			return p, t
		}
		time.Sleep(2 * time.Millisecond)
	}
	h.t.Fatalf("task %s didn't finish", task.ID)
	return nil, action.Task{}
}

func (h *hubEnv) status(name string) string {
	h.t.Helper()
	return decode[StackDetail](h.t, h.do("GET", "/api/stacks/local/"+name, nil)).Status
}

// A stack's whole lifecycle through the hub's actions, against the node
// module: each tier, each undo, the plan's containers and volumes, the
// refusals that make no sense in the state it's in, a start that deploys
// the last commit after a down, and a remove restored by its undo.
func TestLifecycleThroughTheHub(t *testing.T) {
	h := newHub(t)
	target := Target("local", "media")
	plan := func(id, params string) error {
		var raw json.RawMessage
		if params != "" {
			raw = json.RawMessage(params)
		}
		_, err := h.eng.Plan(t.Context(), sam, action.PlanRequest{Action: id, Target: target, Params: raw})
		return err
	}
	if err := plan(ActionStop, ""); refusal(err) != "not_found" {
		t.Errorf("no stack: %v", err)
	}
	if w := h.do("PUT", "/api/stacks/secrets/db_password", map[string]string{"value": secretValue}); w.Code != 204 {
		t.Fatalf("set secret: %d %s", w.Code, w.Body)
	}
	h.saveDraft("media", withVolume)
	if _, task := h.run(ActionDeploy, target, ""); task.State != action.StateSucceeded {
		t.Fatalf("deploy: %+v", task.Error)
	}
	head := decode[StackDetail](t, h.do("GET", "/api/stacks/local/media", nil)).History[0].Commit

	if err := plan(ActionStart, ""); refusal(err) != "precondition" || !strings.Contains(err.Error(), "already running") {
		t.Errorf("start while running: %v", err)
	}

	// Stop: T1, undo starts it; the containers stay.
	p, task := h.run(ActionStop, target, "")
	if p.Tier != action.T1 || p.Undo == nil || p.Undo.Action != ActionStart || !hasStep(p, "Stop web") {
		t.Errorf("stop plan: %+v", p)
	}
	if task.State != action.StateSucceeded || h.status("media") != stackproto.StatusStopped {
		t.Fatalf("stop: %+v, %s", task.Error, h.status("media"))
	}
	if err := plan(ActionStop, ""); refusal(err) != "precondition" || !strings.Contains(err.Error(), "isn't running") {
		t.Errorf("stop while stopped: %v", err)
	}

	// Start: T1, undo stops it.
	p, task = h.run(ActionStart, target, "")
	if p.Tier != action.T1 || p.Undo == nil || p.Undo.Action != ActionStop {
		t.Errorf("start plan: %+v", p)
	}
	if task.State != action.StateSucceeded || h.status("media") != stackproto.StatusRunning {
		t.Fatalf("start: %+v, %s", task.Error, h.status("media"))
	}

	// Restart: T1, no undo, the gate reports.
	p, task = h.run(ActionRestart, target, "")
	if p.Tier != action.T1 || p.Undo != nil || !hasStep(p, "nothing rolls back") {
		t.Errorf("restart plan: %+v", p)
	}
	if task.State != action.StateSucceeded || h.docker.Called("restart media") != 1 {
		t.Fatalf("restart: %+v", task.Error)
	}

	// Down: T2, the volume stays, undo starts it again.
	p, task = h.run(ActionDown, target, "")
	if p.Tier != action.T2 || p.Undo == nil || p.Undo.Action != ActionStart || !strings.Contains(p.Undo.Label, short(head)) || !hasStep(p, "Keep the volumes media_data") {
		t.Errorf("down plan: %+v %+v", p.Steps, p.Undo)
	}
	if task.State != action.StateSucceeded || h.status("media") != stackproto.StatusDown {
		t.Fatalf("down: %+v, %s", task.Error, h.status("media"))
	}
	if err := plan(ActionRestart, ""); refusal(err) != "precondition" || !strings.Contains(err.Error(), "Start it instead") {
		t.Errorf("restart while down: %v", err)
	}

	// Start after down deploys the last commit, with its secret.
	p, task = h.run(ActionStart, target, "")
	if !hasStep(p, "deploys its last commit ("+short(head)+")") {
		t.Errorf("start-after-down plan: %+v", p.Steps)
	}
	if task.State != action.StateSucceeded || h.status("media") != stackproto.StatusRunning {
		t.Fatalf("start after down: %+v, %s", task.Error, h.status("media"))
	}
	if got := h.docker.LastEnv()["web"]["DB_PASSWORD"]; got != secretValue {
		t.Errorf("the redeploy got %q for the secret", got)
	}

	// Remove: T3, typed; deleting the volumes needs the name again. The
	// redeploy may have made a commit; the undo restores the newest.
	head = decode[StackDetail](t, h.do("GET", "/api/stacks/local/media", nil)).History[0].Commit
	if err := plan(ActionRemove, `{"volumes":true}`); refusal(err) != "precondition" || !strings.Contains(err.Error(), "type its name") {
		t.Errorf("volumes without the name: %v", err)
	}
	if err := plan(ActionRemove, `{"other":1}`); refusal(err) != "invalid" {
		t.Errorf("unknown param: %v", err)
	}
	p, task = h.runTyped(ActionRemove, target, `{}`, "media")
	if p.Tier != action.T3 || p.ConfirmText != "media" || p.Undo == nil || p.Undo.Action != ActionRollback ||
		!strings.Contains(string(p.Undo.Params), head) || !hasStep(p, "Keep the volumes media_data") || !hasStep(p, "as a commit") {
		t.Errorf("remove plan: %s %s %+v %+v", p.Tier, p.ConfirmText, p.Steps, p.Undo)
	}
	if task.State != action.StateSucceeded {
		t.Fatalf("remove: %+v", task.Error)
	}
	if w := h.do("GET", "/api/stacks/local/media", nil); w.Code != 404 {
		t.Errorf("a removed stack: %d", w.Code)
	}

	// Its undo restores it from the commit and deploys it.
	_, task = h.run(p.Undo.Action, target, string(p.Undo.Params))
	if task.State != action.StateSucceeded || h.status("media") != stackproto.StatusRunning {
		t.Fatalf("restore: %+v", task.Error)
	}

	// Now with its volumes.
	p, task = h.runTyped(ActionRemove, target, `{"volumes":true,"confirm_volumes":"media"}`, "media")
	if !hasStep(p, "Delete the volumes media_data") || !strings.Contains(p.Label, "and its volumes") {
		t.Errorf("remove with volumes plan: %s %+v", p.Label, p.Steps)
	}
	if task.State != action.StateSucceeded || h.docker.Called("rmvolumes media_data") != 1 {
		t.Fatalf("remove with volumes: %+v", task.Error)
	}
}

// A stack that isn't managed, or that is Cha Dash's own, has no lifecycle
// actions to plan.
func TestLifecycleRefusals(t *testing.T) {
	h := newHub(t)
	h.docker.AddProject("loose", []string{"/srv/loose/compose.yaml"})
	for _, id := range []string{ActionStop, ActionStart, ActionRestart, ActionDown, ActionRemove} {
		_, err := h.eng.Plan(t.Context(), sam, action.PlanRequest{Action: id, Target: Target("local", "loose"), Params: json.RawMessage(`{}`)})
		if c := refusal(err); c != "precondition" && c != "not_found" {
			t.Errorf("%s on an unmanaged project: %v", id, err)
		}
	}
}
