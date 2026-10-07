package docker

import (
	"strings"
	"testing"

	"dash.cha.sh/internal/node"
	"dash.cha.sh/internal/nodeproto"
)

// TestHubProtectedWhateverItsName: the container engine.self takes for the
// hub is protected even when its service and name aren't "chadash"
// (review finding 5).
func TestHubProtectedWhateverItsName(t *testing.T) {
	f, host := newFakeDocker(t)
	proj := func(svc string) map[string]string {
		return map[string]string{"com.docker.compose.project": "home", "com.docker.compose.service": svc}
	}
	f.add(&fakeContainer{ID: "abcdef123456" + strings.Repeat("0", 52), Name: "home-agent-1", Image: "ghcr.io/ban-red/chadash-node:0.2", Labels: proj("agent"),
		Mounts: []map[string]any{
			{"Type": "volume", "Name": "home_node", "Source": "/var/lib/docker/volumes/home_node/_data", "Destination": "/etc/chadash-node", "RW": true},
			{"Type": "bind", "Source": "/var/run/docker.sock", "Destination": "/var/run/docker.sock", "RW": true},
		}})
	f.add(&fakeContainer{Name: "home-dash-1", Image: "ghcr.io/ban-red/chadash:0.2", State: "running", Labels: proj("dash")})
	f.add(&fakeContainer{Name: "home-web-1", Image: "nginx:1.27", State: "running", Labels: proj("web")})
	p := node.DefaultPolicy()
	m, err := New(Options{Host: host, Policy: p, SelfID: "abcdef123456"})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	d, err := node.NewDispatcher(p, m)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := call(t, d, "container.restart", map[string]any{"id": "home-dash-1"}); nodeproto.CodeOf(err) != nodeproto.CodeForbidden {
		t.Fatalf("restarting the hub = %v, want forbidden", err)
	}
	if _, _, err := call(t, d, "container.restart", map[string]any{"id": "home-web-1"}); err != nil {
		t.Fatalf("restarting another container: %v", err)
	}
	if len(f.calls) != 1 {
		t.Fatalf("calls %v", f.calls)
	}

	// The stacks guard learns the node's mounts from here.
	mounts, err := m.SelfMounts(t.Context())
	if err != nil || len(mounts) != 2 || mounts[0].Name != "home_node" || mounts[0].Destination != "/etc/chadash-node" || mounts[1].Source != "/var/run/docker.sock" {
		t.Fatalf("mounts %+v %v", mounts, err)
	}
	other, _ := New(Options{Host: host, Policy: p, SelfID: "999999999999"})
	defer other.Close()
	if mounts, err := other.SelfMounts(t.Context()); err != nil || mounts != nil {
		t.Fatalf("unknown self: %+v %v", mounts, err)
	}
}
