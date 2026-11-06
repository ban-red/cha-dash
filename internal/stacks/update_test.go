package stacks

import (
	"strings"
	"testing"

	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/stackproto"
)

// stack.update through the hub, against the node module: nothing moved is
// refused at planning; a pushed tag plans with its old and new digest,
// updates only that service with the file unchanged, and its undo is a
// rollback to the commit before; a broken push rolls back on its own.
func TestUpdateThroughTheHub(t *testing.T) {
	h := newHub(t)
	target := Target("local", "media")
	if w := h.do("PUT", "/api/stacks/secrets/db_password", map[string]string{"value": secretValue}); w.Code != 204 {
		t.Fatalf("set secret: %d %s", w.Code, w.Body)
	}
	two := good + "  db:\n    image: postgres:17\n"
	two = strings.Replace(two, "x-chadash:\n  soak: 20s\n", "", 1) + "x-chadash:\n  soak: 20s\n"
	h.saveDraft("media", two)
	if _, task := h.run(ActionDeploy, target, ""); task.State != action.StateSucceeded {
		t.Fatalf("deploy: %+v", task.Error)
	}
	before := decode[StackDetail](t, h.do("GET", "/api/stacks/local/media", nil))

	_, err := h.eng.Plan(t.Context(), sam, action.PlanRequest{Action: ActionUpdate, Target: target})
	if refusal(err) != "precondition" || !strings.Contains(err.Error(), "current") {
		t.Fatalf("nothing moved: %v", err)
	}

	h.docker.Push("nginx:1.27")
	p, task := h.run(ActionUpdate, target, "")
	if p.Tier != action.T2 || p.Undo == nil || p.Undo.Action != ActionRollback || !strings.Contains(string(p.Undo.Params), before.History[0].Commit) {
		t.Errorf("update plan: %+v %+v", p, p.Undo)
	}
	if !hasStep(p, "recreate web: sha256:") || !hasStep(p, "Leave db as they are") {
		t.Errorf("update steps: %+v", p.Steps)
	}
	if task.State != action.StateSucceeded {
		t.Fatalf("update: %+v", task.Error)
	}
	after := decode[StackDetail](t, h.do("GET", "/api/stacks/local/media", nil))
	if after.History[0].Kind != stackproto.KindUpdate || after.Content != before.Content {
		t.Errorf("an update is its own commit with the file unchanged: %+v", after.History[0])
	}
	if got := h.docker.LastEnv()["web"]["DB_PASSWORD"]; got != secretValue {
		t.Errorf("the update got %q for the secret", got)
	}

	// A broken image on the tag: the gate fails and it rolls back on its own.
	h.docker.Push("nginx:1.27", "unhealthy")
	_, task = h.run(ActionUpdate, target, "")
	if task.State != action.StateFailed || task.Error == nil || task.Error.Code != "rolled_back" {
		t.Fatalf("a broken update: %+v %+v", task.State, task.Error)
	}

	// Services must be the stack's.
	if _, err := h.eng.Plan(t.Context(), sam, action.PlanRequest{Action: ActionUpdate, Target: target, Params: servicesParams("nope")}); refusal(err) != "precondition" {
		t.Errorf("an unknown service: %v", err)
	}
}
