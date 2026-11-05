//go:build engine

package stacks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dash.cha.sh/internal/stackproto"
	"dash.cha.sh/internal/testengine"
	"github.com/moby/moby/client"
)

// Engine tests: the stacks module through the real Compose SDK, against a
// real Docker Engine (docs/plan/17-test-automation.md, layer 3). They replace
// the scratch-stack run on the hub's host that README.md once described.

// engineNode is the module on the Compose SDK, with real time and a short
// gate, so a run takes seconds.
func engineNode(t *testing.T, e *testengine.Engine) *testNode {
	t.Helper()
	sdk, err := NewSDK(e.Host)
	if err != nil {
		t.Fatal(err)
	}
	n := &testNode{t: t, root: t.TempDir()}
	m, err := New(Options{Root: n.root, Docker: sdk, Wait: time.Minute, Soak: 3 * time.Second, RollbackSoak: 2 * time.Second, Poll: 500 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	n.m = m
	return n
}

func appFile(image string) string {
	return "# the app stack\nservices:\n  web:\n    image: " + image + "\n"
}

// running is the image each of a project's running containers was created
// from, by service.
func running(t *testing.T, e *testengine.Engine, project string) map[string]string {
	t.Helper()
	ctx, cancel := e.Ctx()
	defer cancel()
	cs, err := e.Cli.ContainerList(ctx, client.ContainerListOptions{Filters: make(client.Filters).Add("label", "com.docker.compose.project="+project)})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, c := range cs.Items {
		out[c.Labels["com.docker.compose.service"]] = c.Image
	}
	return out
}

func projectContainers(t *testing.T, e *testengine.Engine, project string) []string {
	t.Helper()
	ctx, cancel := e.Ctx()
	defer cancel()
	cs, err := e.Cli.ContainerList(ctx, client.ContainerListOptions{Filters: make(client.Filters).Add("label", "com.docker.compose.project="+project)})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, c := range cs.Items {
		ids = append(ids, c.ID)
	}
	return ids
}

// A healthy deploy, then a bad one of each kind that rolls back on its own,
// pinned to the digest v1 ran; then a good deploy again.
func TestEngineDeployAndRollBack(t *testing.T) {
	e := testengine.New(t)
	reg := e.Registry()
	v1 := e.Push(reg, reg.Ref("app", "1"), testengine.Healthy, "1")
	_, digest, _ := strings.Cut(v1, "@")
	e.Push(reg, reg.Ref("app", "unhealthy"), testengine.NeverHealthy, "1")
	e.Push(reg, reg.Ref("app", "crash"), testengine.ExitsOne, "1")
	e.Push(reg, reg.Ref("app", "2"), testengine.Healthy, "2")

	n := engineNode(t, e)
	stack := e.Project("app")
	if res := n.deploy(stack, appFile(reg.Ref("app", "1")), nil); res.Outcome != stackproto.OutcomeDeployed {
		t.Fatalf("v1: %s: %s", res.Outcome, res.Reason)
	}
	if got := running(t, e, stack)["web"]; got != reg.Ref("app", "1") {
		t.Fatalf("v1 runs %q", got)
	}
	if res := n.deploy(stack, appFile(reg.Ref("app", "1")), nil); res.Outcome != stackproto.OutcomeUnchanged {
		t.Errorf("the same file again: %s", res.Outcome)
	}

	for _, bad := range []struct{ tag, reason string }{
		{"unhealthy", "unhealthy"},
		{"crash", ""},
	} {
		start := time.Now()
		res := n.deploy(stack, appFile(reg.Ref("app", bad.tag)), nil)
		t.Logf("%s: %s in %s: %s", bad.tag, res.Outcome, time.Since(start).Round(time.Second), res.Reason)
		if res.Outcome != stackproto.OutcomeRolledBack || !strings.Contains(res.Reason, bad.reason) {
			t.Fatalf("%s: %s: %s", bad.tag, res.Outcome, res.Reason)
		}
		if got := running(t, e, stack)["web"]; got != reg.Ref("app", "1")+"@"+digest {
			t.Errorf("after %s's rollback web runs %q, want v1 pinned", bad.tag, got)
		}
		disk, _ := os.ReadFile(filepath.Join(n.root, stack, "compose.yaml"))
		if !strings.Contains(string(disk), "@"+digest) || !strings.Contains(string(disk), "# the app stack") {
			t.Errorf("the restored file should be v1, pinned, its comment kept:\n%s", disk)
		}
		if h := n.history(stack); !strings.HasPrefix(h[0], "rollback Roll back "+stack) || !strings.HasPrefix(h[1], "deploy Deploy "+stack) {
			t.Errorf("history after %s: %q", bad.tag, h)
		}
	}

	if res := n.deploy(stack, appFile(reg.Ref("app", "2")), nil); res.Outcome != stackproto.OutcomeDeployed {
		t.Fatalf("a good deploy after the rollbacks: %s: %s", res.Outcome, res.Reason)
	}
}

// A tag the registry doesn't have is blocked by the preflight: no commit,
// and the running container isn't touched.
func TestEngineMissingTagIsBlocked(t *testing.T) {
	e := testengine.New(t)
	reg := e.Registry()
	e.Push(reg, reg.Ref("app", "1"), testengine.Healthy, "1")
	n := engineNode(t, e)
	stack := e.Project("app")
	if res := n.deploy(stack, appFile(reg.Ref("app", "1")), nil); res.Outcome != stackproto.OutcomeDeployed {
		t.Fatalf("v1: %s: %s", res.Outcome, res.Reason)
	}
	before, ids := headCommit(t, n.root, 0).Hash, projectContainers(t, e, stack)
	res := n.deploy(stack, appFile(reg.Ref("app", "9.99-nope")), nil)
	if res.Outcome != stackproto.OutcomeBlocked {
		t.Fatalf("%s: %s", res.Outcome, res.Reason)
	}
	if headCommit(t, n.root, 0).Hash != before {
		t.Error("a blocked deploy must not commit")
	}
	if after := projectContainers(t, e, stack); len(after) != 1 || after[0] != ids[0] {
		t.Errorf("a blocked deploy must not touch the container: %v → %v", ids, after)
	}
}

// A new stack whose first deploy fails is taken down again, and its file
// goes too.
func TestEngineNewStackThatFailsIsRemoved(t *testing.T) {
	e := testengine.New(t)
	reg := e.Registry()
	e.Push(reg, reg.Ref("app", "unhealthy"), testengine.NeverHealthy, "1")
	n := engineNode(t, e)
	stack := e.Project("app")
	res := n.deploy(stack, appFile(reg.Ref("app", "unhealthy")), nil)
	if res.Outcome != stackproto.OutcomeRolledBack || !strings.Contains(res.Reason, "removed it again") {
		t.Fatalf("%s: %s", res.Outcome, res.Reason)
	}
	if ids := projectContainers(t, e, stack); len(ids) != 0 {
		t.Errorf("its containers should go: %v", ids)
	}
	if _, err := os.Stat(filepath.Join(n.root, stack, "compose.yaml")); !os.IsNotExist(err) {
		t.Error("its file should go too")
	}
}
