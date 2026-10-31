package docker

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/integration/integrationtest"
)

// Through a node, each running container carries what image.check said
// about it, asked at most every updatesEvery or on Check now; a node that
// refuses says why.
func TestNodeImageUpdates(t *testing.T) {
	f := newFake(t, raw)
	h := integrationtest.New(t)
	n := newFakeNode(t, f)
	h.Nodes = n
	// The node answers for every running container: sonarr has an update,
	// the rest are current.
	answer := func(sonarr string) {
		b, _, err := n.get(t.Context(), "/v1.54/containers/json")
		if err != nil {
			t.Fatal(err)
		}
		var list []summary
		_ = json.Unmarshal(b, &list)
		var out []map[string]any
		for _, s := range list {
			u := map[string]any{"id": s.ID, "image": s.Image, "state": "current", "current": "sha256:aa", "latest": "sha256:aa"}
			if s.name() == "sonarr" {
				u["state"], u["latest"] = sonarr, "sha256:bb"
			}
			out = append(out, u)
		}
		b, _ = json.Marshal(map[string]any{"images": out})
		n.mu.Lock()
		n.images = string(b)
		n.mu.Unlock()
	}
	answer("available")
	inst := h.Connect(Integration{}, map[string]any{"endpoint": "node://local"}, nil)
	syncOK(t, inst)

	update := func(name string) map[string]any {
		t.Helper()
		r, ok := h.Resource("docker:i_test/container/" + name)
		if !ok {
			t.Fatalf("no %s", name)
		}
		u, _ := r.Attrs["update"].(map[string]any)
		return u
	}
	if u := update("sonarr"); u["state"] != "available" || u["latest"] != "sha256:bb" || u["current"] != "sha256:aa" {
		t.Fatalf("sonarr's update: %v", u)
	}
	if u := update("immich_server"); u["state"] != "current" {
		t.Fatalf("immich_server's update: %v", u)
	}
	asked := func() int {
		return len(slices.DeleteFunc(n.verbs(), func(v string) bool { return v != "image.check" }))
	}
	if asked() != 1 {
		t.Fatalf("image.check asked %d times", asked())
	}

	// Within updatesEvery the node isn't asked again.
	h.Advance(updatesEvery / 2)
	syncOK(t, inst)
	if asked() != 1 {
		t.Fatalf("asked %d times within %s", asked(), updatesEvery)
	}
	// Check now asks at once, and asks the node to ask the registries.
	answer("current")
	if err := inst.Sync(t.Context(), integration.Interest{Watched: true, Metrics: true, Refresh: true}); err != nil {
		t.Fatal(err)
	}
	if asked() != 2 || n.fresh != 1 || update("sonarr")["state"] != "current" {
		t.Fatalf("Check now: asked %d, fresh %d, sonarr %v", asked(), n.fresh, update("sonarr"))
	}
	// After updatesEvery, the node is asked again, not fresh.
	h.Advance(updatesEvery)
	syncOK(t, inst)
	if asked() != 3 || n.fresh != 1 {
		t.Fatalf("after %s: asked %d, fresh %d", updatesEvery, asked(), n.fresh)
	}

	// A node whose policy refuses images.check: every running container says
	// why, and stopped ones carry nothing.
	n.mu.Lock()
	n.deny["images.check"] = "forbidden"
	n.mu.Unlock()
	h.Advance(updatesEvery)
	syncOK(t, inst)
	if u := update("sonarr"); u["state"] != "cant_check" || u["reason"] != "The node's policy doesn't allow image checks (images.check)." {
		t.Fatalf("refused: %v", u)
	}
	if u := update("pingvin-share"); u != nil {
		t.Fatalf("a stopped container carries %v", u)
	}
}

// docker.container.update runs the node's container.update: T2, and a
// rollback fails the task with the node's sentence.
func TestDockerUpdateAction(t *testing.T) {
	e := newActionEnv(t)
	sonarr := "docker:i_test/container/sonarr"
	p, err := e.plan("docker.container.update", sonarr, "")
	if err != nil {
		t.Fatal(err)
	}
	if p.Tier != action.T2 || p.Confirm != "hold" || len(p.Steps) != 4 || !strings.Contains(p.Steps[3].Label, "previous image") {
		t.Fatalf("plan %+v", p)
	}
	task := e.run(p, "")
	if task.State != action.StateSucceeded || !slices.ContainsFunc(task.Log, func(l string) bool {
		return strings.Contains(l, "runs lscr.io/linuxserver/sonarr:latest@sha256:bb now")
	}) {
		t.Fatalf("update: %+v", task)
	}
	if !slices.Contains(e.node.verbs(), "container.update") {
		t.Fatalf("verbs %v", e.node.verbs())
	}

	e.node.mu.Lock()
	e.node.outcome = "rolled_back"
	e.node.mu.Unlock()
	p, _ = e.plan("docker.container.update", sonarr, "")
	task = e.run(p, "")
	if task.State != action.StateFailed || task.Error == nil || task.Error.Code != "rolled_back" || !strings.Contains(task.Error.Message, "Rolled back to") {
		t.Fatalf("rolled back: %+v", task)
	}

	// The node's policy without docker.update makes it unavailable.
	e.node.mu.Lock()
	e.node.deny["docker.update"] = "this node's policy doesn't allow docker.update"
	e.node.mu.Unlock()
	if _, err := e.plan("docker.container.update", sonarr, ""); !isRefusal(err, "unavailable") {
		t.Fatalf("update without docker.update = %v", err)
	}
}
