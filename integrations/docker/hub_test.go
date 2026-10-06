package docker

import (
	"strings"
	"testing"

	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/integration/integrationtest"
)

// withHub puts chadash:hub in the harness's graph, as the hub does at boot.
func withHub(t *testing.T, h *integrationtest.Harness) {
	t.Helper()
	if _, err := h.Set.Graph.Update(func(tx *graph.Tx) error {
		return tx.Upsert(graph.Resource{ID: integration.HubResource, Kind: "app", Name: "Cha Dash"})
	}); err != nil {
		t.Fatal(err)
	}
}

// hubOn returns the container chadash:hub runs on, or "".
func hubOn(h *integrationtest.Harness) string {
	for _, r := range h.Graph().Relations {
		if string(r.From) == integration.HubResource && r.Type == "runs_on" {
			return string(r.To)
		}
	}
	return ""
}

func ownContainer(t *testing.T, id string) {
	t.Helper()
	prev := integration.HubContainerID
	t.Cleanup(func() { integration.HubContainerID = prev })
	integration.HubContainerID = func() string { return id }
}

// TestHubContainerFromTheNode: through chadash-node, the node names the
// hub's container (engine.self) and the integration relates chadash:hub to
// it, so stopping that container is the self-aware T4.
func TestHubContainerFromTheNode(t *testing.T) {
	ownContainer(t, "") // the hub can't tell by itself: only the node's answer counts
	f := newFake(t, raw)
	first := f.list[0]
	id, name := first["Id"].(string), strings.TrimPrefix(first["Names"].([]any)[0].(string), "/")
	h := integrationtest.New(t)
	withHub(t, h)
	n := newFakeNode(t, f)
	n.hubID = id
	h.Nodes = n
	inst := h.Connect(Integration{}, map[string]any{"endpoint": "node://local"}, nil)
	syncOK(t, inst)
	if got := hubOn(h); got != "docker:i_test/container/"+name {
		t.Fatalf("chadash:hub runs_on %q, want %s", got, name)
	}

	// The node no longer names one (an older node, or it can't tell): after
	// the hourly re-ask, the relation goes.
	n.mu.Lock()
	n.hubID = ""
	n.mu.Unlock()
	h.Advance(probeEvery)
	syncOK(t, inst)
	if got := hubOn(h); got != "" {
		t.Fatalf("still related to %s", got)
	}
}

// TestHubContainerFromItsOwnID: through a socket proxy, the hub's own
// container ID (its hostname or mountinfo) finds it.
func TestHubContainerFromItsOwnID(t *testing.T) {
	f := newFake(t, tecnativa)
	first := f.list[3]
	id, name := first["Id"].(string), strings.TrimPrefix(first["Names"].([]any)[0].(string), "/")
	ownContainer(t, id[:12])
	h := integrationtest.New(t)
	withHub(t, h)
	syncOK(t, h.Connect(Integration{}, map[string]any{"endpoint": f.serve()}, nil))
	if got := hubOn(h); got != "docker:i_test/container/"+name {
		t.Fatalf("chadash:hub runs_on %q, want %s", got, name)
	}

	// Another engine, or a hub outside any container: nothing.
	ownContainer(t, "")
	h2 := integrationtest.New(t)
	withHub(t, h2)
	syncOK(t, h2.Connect(Integration{}, map[string]any{"endpoint": f.serve()}, nil))
	if got := hubOn(h2); got != "" {
		t.Fatalf("related to %s", got)
	}
}
