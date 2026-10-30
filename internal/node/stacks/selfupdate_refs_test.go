package stacks

import (
	"context"
	"strings"
	"testing"

	"dash.cha.sh/internal/stackproto"
)

// TestSelfUpdateNeedsAFixedRollback: without a registry digest for the
// hub's image, the only way back would be its tag, which the pull moves to
// the image being tried. The update is refused before anything changes.
func TestSelfUpdateNeedsAFixedRollback(t *testing.T) {
	n := hubHost(t)
	n.docker.NoRepoDigests = true
	n.docker.Reset()
	err := n.call("stack.selfupdate", stackproto.SelfUpdateRequest{Image: "0.3.0-unhealthy"}, nil)
	if e := stackproto.ErrorFrom(err); e == nil || e.Code != stackproto.CodeConflict || !strings.Contains(e.Message, "no registry digest") {
		t.Fatalf("got %v", err)
	}
	if n.docker.Called("pullimage ") != 0 || n.docker.Called("recreate ") != 0 {
		t.Fatal("it pulled or recreated something")
	}
	if img := n.docker.Running("chadash")["chadash"]; img != "ghcr.io/ban-red/chadash:0.2.0" {
		t.Fatalf("the hub runs %s", img)
	}
}

// TestSelfUpdateIgnoresLeftovers: a recreate that couldn't remove the old
// container leaves it stopped, as <name>-chadash-old-<n>, in the project.
// The gate follows the new container by its ID, and the next update still
// finds the one hub.
func TestSelfUpdateIgnoresLeftovers(t *testing.T) {
	n := hubHost(t)
	n.docker.LeaveOld = true
	var res stackproto.SelfUpdateResult
	n.must("stack.selfupdate", stackproto.SelfUpdateRequest{Image: "0.3.0"}, &res)
	if res.Outcome != stackproto.OutcomeDeployed || res.Current != pinnedHub("0.3.0") {
		t.Fatalf("the first update: %+v", res)
	}
	cs, _ := n.docker.Containers(context.Background(), "chadash")
	left := 0
	for _, c := range cs {
		if strings.Contains(c.Name, "-chadash-old-") {
			left++
		}
	}
	if left != 1 {
		t.Fatalf("the fake left %d old containers: %+v", left, cs)
	}
	n.must("stack.selfupdate", stackproto.SelfUpdateRequest{Image: "0.4.0"}, &res)
	if res.Outcome != stackproto.OutcomeDeployed || res.Previous != pinnedHub("0.3.0") || res.Current != pinnedHub("0.4.0") {
		t.Fatalf("the second update: %+v", res)
	}
	// A bad image rolls back to the one running, not to a leftover.
	n.must("stack.selfupdate", stackproto.SelfUpdateRequest{Image: "0.5.0-unhealthy"}, &res)
	if res.Outcome != stackproto.OutcomeRolledBack || res.Current != pinnedHub("0.4.0") {
		t.Fatalf("the bad update: %+v", res)
	}
	var last stackproto.SelfUpdateResult
	n.must("stack.selfupdate.result", nil, &last)
	if !last.Available || last.Running != pinnedHub("0.4.0") {
		t.Fatalf("result %+v", last)
	}
}
