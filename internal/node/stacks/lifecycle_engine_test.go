//go:build engine

package stacks

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/moby/moby/client"

	"dash.cha.sh/internal/stackproto"
	"dash.cha.sh/internal/testengine"
)

// Engine tests for a whole stack's lifecycle (plan 16, wave 32a): stop,
// start, restart, down, remove and a restore, through the Compose SDK on
// throwaway stacks in a real Docker Engine.

// volumeFile is a one-service stack with a named volume.
func volumeFile(image string) string {
	return "# the app stack\nservices:\n  web:\n    image: " + image + "\n    volumes:\n      - data:/data\nvolumes:\n  data: {}\n"
}

// allContainers is each of a project's containers, stopped ones too: ID → state.
func allContainers(t *testing.T, e *testengine.Engine, project string) map[string]string {
	t.Helper()
	ctx, cancel := e.Ctx()
	defer cancel()
	cs, err := e.Cli.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: make(client.Filters).Add("label", "com.docker.compose.project="+project)})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, c := range cs.Items {
		out[c.ID] = string(c.State)
	}
	return out
}

func projectNetworks(t *testing.T, e *testengine.Engine, project string) []string {
	t.Helper()
	ctx, cancel := e.Ctx()
	defer cancel()
	ns, err := e.Cli.NetworkList(ctx, client.NetworkListOptions{Filters: make(client.Filters).Add("label", "com.docker.compose.project="+project)})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, n := range ns.Items {
		out = append(out, n.Name)
	}
	return out
}

func projectVolumes(t *testing.T, e *testengine.Engine, project string) []string {
	t.Helper()
	ctx, cancel := e.Ctx()
	defer cancel()
	vs, err := e.Cli.VolumeList(ctx, client.VolumeListOptions{Filters: make(client.Filters).Add("label", "com.docker.compose.project="+project)})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, v := range vs.Items {
		out = append(out, v.Name)
	}
	slices.Sort(out)
	return out
}

// Stop keeps the containers, stopped, and the list says stopped without
// drift; start runs the same ones again; restart passes the gate; down
// takes the containers and the network and keeps the volume; start then
// has nothing to start, and deploying the current commit brings it back.
func TestEngineStackLifecycle(t *testing.T) {
	e := testengine.New(t)
	reg := e.Registry()
	e.Push(reg, reg.Ref("app", "1"), testengine.Healthy, "1")
	n := engineNode(t, e)
	stack := e.Project("life")
	file := volumeFile(reg.Ref("app", "1"))
	if res := n.deploy(stack, file, nil); res.Outcome != stackproto.OutcomeDeployed {
		t.Fatalf("deploy: %s: %s", res.Outcome, res.Reason)
	}
	ids := allContainers(t, e, stack)
	vol := stack + "_data"
	if len(ids) != 1 || !slices.Equal(projectVolumes(t, e, stack), []string{vol}) || len(projectNetworks(t, e, stack)) != 1 {
		t.Fatalf("after deploy: containers %v, volumes %v, networks %v", ids, projectVolumes(t, e, stack), projectNetworks(t, e, stack))
	}
	var st stackproto.Stack
	n.must("stack.get", stackproto.GetRequest{Stack: stack}, &st)
	if !slices.Equal(st.Volumes, []string{vol}) {
		t.Errorf("stack.get volumes %v", st.Volumes)
	}

	res := n.life(stackproto.VerbStop, stack)
	if res.Status != stackproto.StatusStopped {
		t.Fatalf("stop: %+v", res)
	}
	after := allContainers(t, e, stack)
	for id := range ids {
		if after[id] != "exited" {
			t.Errorf("stop keeps the container, stopped: %v", after)
		}
	}
	if s := n.summaryOf(stack); s.Status != stackproto.StatusStopped || s.Drift {
		t.Errorf("a stopped stack is stopped, not drift: %+v", s)
	}

	if res = n.life(stackproto.VerbStart, stack); res.Status != stackproto.StatusRunning {
		t.Fatalf("start: %+v", res)
	}
	for id := range allContainers(t, e, stack) {
		if _, same := ids[id]; !same {
			t.Errorf("start must start the same container, not a new one: %s", id)
		}
	}

	res = n.life(stackproto.VerbRestart, stack)
	if res.Healthy == nil || !*res.Healthy || len(res.Unhealthy) != 0 || res.Status != stackproto.StatusRunning || res.Services[0].Health != "healthy" {
		t.Fatalf("restart: %+v", res)
	}

	res = n.life(stackproto.VerbDown, stack)
	if res.Status != stackproto.StatusDown || !slices.Equal(res.VolumesKept, []string{vol}) {
		t.Fatalf("down: %+v", res)
	}
	if cs := allContainers(t, e, stack); len(cs) != 0 {
		t.Errorf("down removes the containers: %v", cs)
	}
	if ns := projectNetworks(t, e, stack); len(ns) != 0 {
		t.Errorf("down removes the project's networks: %v", ns)
	}
	if vs := projectVolumes(t, e, stack); !slices.Equal(vs, []string{vol}) {
		t.Errorf("down keeps the named volume: %v", vs)
	}
	if s := n.summaryOf(stack); s.Status != stackproto.StatusDown || s.Drift {
		t.Errorf("after down: %+v", s)
	}
	if err := n.call(stackproto.VerbStart, stackproto.LifecycleRequest{Name: stack}, nil); codeOf(err) != stackproto.CodeNotDeployed {
		t.Errorf("start after down: %v", err)
	}
	if dep := n.deploy(stack, file, nil); dep.Outcome != stackproto.OutcomeDeployed || len(allContainers(t, e, stack)) != 1 {
		t.Errorf("deploying the current commit after down: %s: %s", dep.Outcome, dep.Reason)
	}
}

// Restart's gate on a service that never turns healthy: it reports the
// service, and rolls nothing back.
func TestEngineRestartGateReportsOnly(t *testing.T) {
	e := testengine.New(t)
	reg := e.Registry()
	e.Push(reg, reg.Ref("app", "sick"), testengine.NeverHealthy, "1")
	n := engineNode(t, e)
	stack := e.Project("sick")
	content := appFile(reg.Ref("app", "sick"))
	// Started outside Cha Dash (a deploy's gate would refuse it), then
	// adopted in place.
	dir := filepath.Join(n.root, stack)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	n.must(stackproto.VerbAdopt, stackproto.AdoptRequest{Stack: stack}, nil)
	s, err := n.m.resolve(stack)
	if err != nil {
		t.Fatal(err)
	}
	p, err := load(t.Context(), s, content, loadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := e.Ctx()
	defer cancel()
	if err := n.m.docker.Up(ctx, p, 0, func(Event) {}); err != nil {
		t.Fatal(err)
	}
	ids := allContainers(t, e, stack)
	head := headCommit(t, n.root, 0).Hash

	res := n.life(stackproto.VerbRestart, stack)
	if res.Healthy == nil || *res.Healthy || !slices.Equal(res.Unhealthy, []string{"web"}) {
		t.Fatalf("restart of a never-healthy service: %+v", res)
	}
	if headCommit(t, n.root, 0).Hash != head {
		t.Error("restart must not commit anything")
	}
	after := allContainers(t, e, stack)
	if len(after) != len(ids) {
		t.Fatalf("containers %v → %v", ids, after)
	}
	for id := range after {
		if _, same := ids[id]; !same || after[id] != "running" {
			t.Errorf("nothing is rolled back or recreated: %v → %v", ids, after)
		}
	}
}

// Remove takes the stack down and deletes its folder as a commit, keeping
// the volume; a rollback restores it; removing it with its volumes takes
// those too.
func TestEngineRemoveAndRestore(t *testing.T) {
	e := testengine.New(t)
	reg := e.Registry()
	e.Push(reg, reg.Ref("app", "1"), testengine.Healthy, "1")
	n := engineNode(t, e)
	stack := e.Project("gone")
	vol := stack + "_data"
	if res := n.deploy(stack, volumeFile(reg.Ref("app", "1")), nil); res.Outcome != stackproto.OutcomeDeployed {
		t.Fatalf("deploy: %s: %s", res.Outcome, res.Reason)
	}
	deployed := headCommit(t, n.root, 0).Hash.String()

	var res stackproto.LifecycleResult
	n.must(stackproto.VerbRemove, stackproto.RemoveRequest{Name: stack, By: "sam"}, &res)
	if res.Commit == nil || res.Commit.Kind != stackproto.KindRemove || !slices.Equal(res.VolumesKept, []string{vol}) {
		t.Fatalf("remove: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(n.root, stack)); !os.IsNotExist(err) {
		t.Errorf("the folder should go: %v", err)
	}
	if cs := allContainers(t, e, stack); len(cs) != 0 {
		t.Errorf("its containers go: %v", cs)
	}
	if vs := projectVolumes(t, e, stack); !slices.Equal(vs, []string{vol}) {
		t.Errorf("the volume stays: %v", vs)
	}
	var list stackproto.ListResponse
	n.must(stackproto.VerbList, nil, &list)
	if len(list.Stacks) != 0 {
		t.Errorf("a removed stack isn't listed: %+v", list.Stacks)
	}

	// The hub reads the version to restore first.
	if err := n.call(stackproto.VerbGet, stackproto.GetRequest{Stack: stack}, nil); codeOf(err) != stackproto.CodeNotFound {
		t.Errorf("get of a removed stack without at: %v", err)
	}
	var old stackproto.Stack
	n.must(stackproto.VerbGet, stackproto.GetRequest{Stack: stack, At: deployed}, &old)
	if !old.Managed || old.Content != volumeFile(reg.Ref("app", "1")) || len(old.History) < 2 || old.History[0].Kind != stackproto.KindRemove || !slices.Equal(old.Volumes, []string{vol}) {
		t.Fatalf("get at the deployed commit: %+v", old)
	}

	var rb stackproto.DeployResult
	n.must(stackproto.VerbRollback, stackproto.RollbackRequest{Stack: stack, Commit: deployed}, &rb)
	if rb.Outcome != stackproto.OutcomeDeployed {
		t.Fatalf("restore: %s: %s", rb.Outcome, rb.Reason)
	}
	if b, err := os.ReadFile(filepath.Join(n.root, stack, "compose.yaml")); err != nil || !strings.Contains(string(b), "# the app stack") {
		t.Errorf("the file is back: %v", err)
	}
	if got := running(t, e, stack); len(got) != 1 {
		t.Errorf("restored and running: %v", got)
	}

	res = stackproto.LifecycleResult{}
	n.must(stackproto.VerbRemove, stackproto.RemoveRequest{Name: stack, Volumes: true}, &res)
	if !slices.Equal(res.VolumesRemoved, []string{vol}) || len(res.VolumesKept) != 0 {
		t.Fatalf("remove with volumes: %+v", res)
	}
	if vs := projectVolumes(t, e, stack); len(vs) != 0 {
		t.Errorf("the volume goes: %v", vs)
	}
}
