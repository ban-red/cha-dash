package integration

import (
	"bufio"
	"strings"
	"testing"

	"dash.cha.sh/internal/action"
)

func TestContainerFromMountinfo(t *testing.T) {
	id := strings.Repeat("ab12", 16)
	other := strings.Repeat("cd34", 16)
	inside := `612 590 0:52 / / rw,relatime master:301 - overlay overlay rw,lowerdir=/var/lib/docker/overlay2/l/X
640 612 254:1 /var/lib/docker/containers/` + id + `/resolv.conf /etc/resolv.conf rw,relatime - ext4 /dev/vda1 rw
641 612 254:1 /var/lib/docker/containers/` + id + `/hostname /etc/hostname rw,relatime - ext4 /dev/vda1 rw
`
	if got := containerFromMountinfo(bufio.NewScanner(strings.NewReader(inside))); got != id {
		t.Fatalf("inside a container = %q", got)
	}
	// On a Docker host the containers' shm mounts show up: not ours.
	host := `29 1 254:1 / / rw,relatime shared:1 - ext4 /dev/vda1 rw
812 29 0:61 / /var/lib/docker/containers/` + other + `/mounts/shm rw,nosuid shared:410 - tmpfs shm rw,size=65536k
`
	if got := containerFromMountinfo(bufio.NewScanner(strings.NewReader(host))); got != "" {
		t.Fatalf("on the host = %q", got)
	}
}

// The engine names the hub in plans by the same ID.
func TestHubResourceIsTheEngines(t *testing.T) {
	if HubResource != action.HubResource {
		t.Fatalf("%s != %s", HubResource, action.HubResource)
	}
}

func TestIsHubContainer(t *testing.T) {
	prev := HubContainerID
	t.Cleanup(func() { HubContainerID = prev })
	full := "abcdef123456" + strings.Repeat("0", 52)
	for _, hub := range []string{full, "abcdef123456"} {
		HubContainerID = func() string { return hub }
		for id, want := range map[string]bool{full: true, "abcdef123456": true, "ABCDEF123456": true, "abcdef12345": false, "abcdef999999": false, "": false, "not-an-id-at-all": false} {
			if got := IsHubContainer(id); got != want {
				t.Errorf("hub %s: IsHubContainer(%q) = %v", hub[:12], id, got)
			}
		}
	}
	HubContainerID = func() string { return "" }
	if IsHubContainer(full) {
		t.Fatal("not in a container, yet matched")
	}
}
