package stacks

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"dash.cha.sh/internal/node"
	"dash.cha.sh/internal/stackproto"
)

// hubCompose is deploy/compose.yaml in short: the hub and the node, in one
// project.
const hubCompose = `services:
  chadash:
    image: ghcr.io/ban-red/chadash:0.2.0
    healthcheck:
      test: ["CMD", "/chadash", "healthcheck"]
  chadash-node:
    image: ghcr.io/ban-red/chadash-node:0.2.0
`

// hubHost is a node beside the hub: in its compose project, able to
// recreate containers.
func hubHost(t *testing.T) *testNode {
	t.Helper()
	n := newTestNode(t, func(o *Options) {
		o.SelfProject = func(context.Context) (string, error) { return "chadash", nil }
	})
	// The fake Docker is made with the node, so it's the updater too.
	n.m.selfUpdater = n.docker
	startOutside(t, n.docker, "chadash", filepath.Join(t.TempDir(), "chadash", "compose.yaml"), hubCompose)
	return n
}

// pinnedHub is the hub's image at tag, pinned to the fake registry's
// digest: "ghcr.io/ban-red/chadash:0.3.0@sha256:…".
func pinnedHub(tag string) string {
	ref := "ghcr.io/ban-red/chadash:" + tag
	_, sum, _ := strings.Cut(digestOf(ref), "@")
	return ref + "@" + sum
}

func containerIDs(n *testNode, project string) map[string]string {
	cs, _ := n.docker.Containers(context.Background(), project)
	out := map[string]string{}
	for _, c := range cs {
		out[c.Service] = c.ID
	}
	return out
}

func TestSelfUpdateGoodImage(t *testing.T) {
	n := hubHost(t)
	before := containerIDs(n, "chadash")
	var res stackproto.SelfUpdateResult
	n.must("stack.selfupdate", stackproto.SelfUpdateRequest{Image: "0.3.0", By: "sam", Via: "web", Ref: "t_update"}, &res)
	want := pinnedHub("0.3.0")
	if res.Outcome != stackproto.OutcomeDeployed || res.Current != want || res.Previous != pinnedHub("0.2.0") || res.Ref != "t_update" || res.Commit == "" {
		t.Fatalf("result %+v", res)
	}
	if img := n.docker.Running("chadash"); img["chadash"] != want || img["chadash-node"] != "ghcr.io/ban-red/chadash-node:0.2.0" {
		t.Fatalf("running %v", img)
	}
	after := containerIDs(n, "chadash")
	if after["chadash-node"] != before["chadash-node"] || after["chadash"] == before["chadash"] {
		t.Fatalf("containers before %v after %v: only the hub's changes", before, after)
	}
	if n.docker.Called("recreate ") != 1 || n.docker.Called("recreate chadash-node") != 0 || n.docker.Called("up ") != 0 {
		t.Fatal("only the hub is recreated, once, and no compose up runs")
	}
	// The history of Cha Dash's own stack has it, and .result answers it.
	var st stackproto.Stack
	n.must("stack.get", stackproto.GetRequest{Stack: "chadash"}, &st)
	if !st.Protected || len(st.History) != 1 || st.History[0].Kind != stackproto.KindSelfUpdate || !strings.HasPrefix(st.History[0].Subject, "Update Cha Dash to ghcr.io/ban-red/chadash:0.3.0: deployed") {
		t.Fatalf("stack %+v", st)
	}
	var last stackproto.SelfUpdateResult
	n.must("stack.selfupdate.result", nil, &last)
	if last.Outcome != stackproto.OutcomeDeployed || last.Ref != "t_update" || last.Commit != res.Commit || !last.Available || last.Running != want || last.FinishedAt == nil {
		t.Fatalf("last %+v", last)
	}
	// The same tag again changes nothing.
	n.docker.Reset()
	n.must("stack.selfupdate", stackproto.SelfUpdateRequest{Image: "ghcr.io/ban-red/chadash:0.3.0"}, &res)
	if res.Outcome != stackproto.OutcomeUnchanged || n.docker.Called("recreate ") != 0 {
		t.Fatalf("again: %+v", res)
	}
}

func TestSelfUpdateBadImageRollsBack(t *testing.T) {
	n := hubHost(t)
	prev := pinnedHub("0.2.0")
	var res stackproto.SelfUpdateResult
	n.must("stack.selfupdate", stackproto.SelfUpdateRequest{Image: "0.3.0-unhealthy", Ref: "t_bad"}, &res)
	if res.Outcome != stackproto.OutcomeRolledBack || res.Current != prev || !strings.Contains(res.Reason, "unhealthy") || !strings.Contains(res.Reason, "Rolled back") {
		t.Fatalf("result %+v", res)
	}
	if img := n.docker.Running("chadash")["chadash"]; img != prev {
		t.Fatalf("the hub runs %s, want the previous digest %s", img, prev)
	}
	if n.docker.Called("recreate chadash ") != 2 || n.docker.Called("recreate chadash-node") != 0 {
		t.Fatal("the hub is recreated twice (update, rollback), the node never")
	}
	var last stackproto.SelfUpdateResult
	n.must("stack.selfupdate.result", nil, &last)
	if last.Outcome != stackproto.OutcomeRolledBack || last.Ref != "t_bad" {
		t.Fatalf("last %+v", last)
	}
	// A tag that isn't there changes nothing.
	n.docker.Reset()
	n.must("stack.selfupdate", stackproto.SelfUpdateRequest{Image: "9.9.9-missing"}, &res)
	if res.Outcome != stackproto.OutcomeBlocked || n.docker.Called("recreate ") != 0 {
		t.Fatalf("missing: %+v", res)
	}
}

func TestSelfUpdateRefusals(t *testing.T) {
	n := hubHost(t)
	for _, img := range []string{
		"ghcr.io/ban-red/chadash-node:0.3.0", // the node's own image
		"nginx:1.28",
		"ghcr.io/ban-red/chadash@sha256:" + strings.Repeat("a", 64),
		"ghcr.io/ban-red/chadash",
		"",
		"0.3.0; rm -rf /",
	} {
		err := n.call("stack.selfupdate", stackproto.SelfUpdateRequest{Image: img}, nil)
		if e := stackproto.ErrorFrom(err); e == nil || e.Code != stackproto.CodeInvalid {
			t.Errorf("%q: %v", img, err)
		}
	}
	if n.docker.Called("recreate ") != 0 || n.docker.Called("pullimage ") != 0 {
		t.Fatal("a refused request touched Docker")
	}

	// The policy's grant: off by default.
	for _, allow := range [][]string{node.DefaultPolicy().Stacks.Allow, append(node.DefaultPolicy().Stacks.Allow, "selfupdate")} {
		p := node.DefaultPolicy()
		p.Stacks.Allow = allow
		d, err := node.NewDispatcher(p, n.m)
		if err != nil {
			t.Fatal(err)
		}
		_, err = d.Call(t.Context(), stackproto.VerbSelfUpdateResult, json.RawMessage(`{}`), nil)
		on := len(allow) > len(node.DefaultPolicy().Stacks.Allow)
		if on != (err == nil) || !on && !strings.Contains(err.Error(), "stacks.selfupdate") {
			t.Errorf("allow %v: %v", allow, err)
		}
		_, err = d.Call(t.Context(), stackproto.VerbSelfUpdate, json.RawMessage(`{"image":"0.3.0-unhealthy"}`), nil)
		if !on && (err == nil || !strings.Contains(err.Error(), "doesn't allow stacks.selfupdate")) {
			t.Errorf("selfupdate with it off: %v", err)
		}
	}

	// A node that isn't beside the hub can't.
	other := newTestNode(t, func(o *Options) { o.SelfUpdate = nil })
	other.m.selfUpdater = other.docker
	startOutside(t, other.docker, "chadash", filepath.Join(t.TempDir(), "compose.yaml"), hubCompose)
	var last stackproto.SelfUpdateResult
	other.must("stack.selfupdate.result", nil, &last)
	if last.Available || !strings.Contains(last.Unavailable, "beside the hub") {
		t.Fatalf("not beside the hub: %+v", last)
	}
	if e := stackproto.ErrorFrom(other.call("stack.selfupdate", stackproto.SelfUpdateRequest{Image: "0.3.0"}, nil)); e == nil || e.Code != stackproto.CodeForbidden {
		t.Fatalf("not beside the hub: %v", e)
	}
}

// While the hub is being recreated (the hub that asked is gone), the
// result says running; a node that stopped mid-update says so too.
func TestSelfUpdateResultWhileRunning(t *testing.T) {
	n := hubHost(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	n.docker.OnRecreate = func(string) {
		once.Do(func() {
			entered <- struct{}{}
			<-release
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan stackproto.SelfUpdateResult)
	go func() {
		i := 0
		for j, v := range n.m.Verbs() {
			if v.Name == stackproto.VerbSelfUpdate {
				i = j
			}
		}
		out, err := n.m.Verbs()[i].Handle(ctx, json.RawMessage(`{"image":"0.3.0","ref":"t_mid"}`), func(json.RawMessage) {})
		var res stackproto.SelfUpdateResult
		if err == nil {
			_ = json.Unmarshal(out, &res)
		}
		done <- res
	}()
	<-entered
	cancel() // the hub goes away
	var mid stackproto.SelfUpdateResult
	n.must("stack.selfupdate.result", nil, &mid)
	if mid.Outcome != stackproto.OutcomeRunning || mid.Ref != "t_mid" || mid.StartedAt == nil {
		t.Fatalf("mid-update: %+v", mid)
	}
	if e := stackproto.ErrorFrom(n.call("stack.selfupdate", stackproto.SelfUpdateRequest{Image: "0.3.0"}, nil)); e == nil || e.Code != stackproto.CodeBusy {
		t.Fatalf("a second update: %v", e)
	}
	close(release)
	select {
	case res := <-done:
		if res.Outcome != stackproto.OutcomeDeployed {
			t.Fatalf("the update didn't finish after the caller went: %+v", res)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the update never finished")
	}

	// The node stopped while the record said running.
	b, _ := json.Marshal(stackproto.SelfUpdateResult{Outcome: stackproto.OutcomeRunning, Ref: "t_lost"})
	if err := os.WriteFile(filepath.Join(n.root, filepath.FromSlash(selfUpdateFile)), b, 0o644); err != nil {
		t.Fatal(err)
	}
	var lost stackproto.SelfUpdateResult
	n.must("stack.selfupdate.result", nil, &lost)
	if lost.Outcome != stackproto.OutcomeStuck || lost.Ref != "t_lost" || !strings.Contains(lost.Reason, "stopped") {
		t.Fatalf("lost: %+v", lost)
	}
}
