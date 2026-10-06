package main

import (
	"os/exec"
	"strings"
	"testing"
)

// heavyForTheHub are module paths only chadash-node may link: the Docker
// client, the Compose SDK and go-git cost the node about 30 MB (ADR-0020),
// and the hub never talks to Docker for writes (ADR-0006). The node's
// runtime packages stay out too; the hub shares only internal/nodeproto.
var heavyForTheHub = []string{
	"github.com/moby/",
	"github.com/docker/",
	"github.com/compose-spec/",
	"github.com/go-git/",
	"github.com/containerd/",
	"go.opentelemetry.io/",
	"dash.cha.sh/internal/node/",
	"dash.cha.sh/internal/node\n",
}

// TestHubFootprint checks the hub's dependency graph (go list -deps
// ./cmd/chadash) holds none of them, so the hub stays small.
func TestHubFootprint(t *testing.T) {
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go command on PATH")
	}
	out, err := exec.Command(gobin, "list", "-deps", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps: %v\n%s", err, out)
	}
	deps := string(out) + "\n"
	if !strings.Contains(deps, "dash.cha.sh/internal/nodes\n") || !strings.Contains(deps, "dash.cha.sh/internal/nodeproto\n") {
		t.Fatal("the scan is broken: the hub should list internal/nodes and internal/nodeproto")
	}
	for _, p := range heavyForTheHub {
		for line := range strings.SplitSeq(deps, "\n") {
			if strings.HasPrefix(line+"\n", p) {
				t.Errorf("the hub links %s: only chadash-node may (see heavyForTheHub)", line)
			}
		}
	}
}
