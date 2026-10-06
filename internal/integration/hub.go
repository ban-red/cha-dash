package integration

import (
	"bufio"
	"os"
	"regexp"
	"strings"
	"sync"
)

// The hub's own container, for integrations that can tell where the hub
// runs (docs/plan/03-architecture.md, "hub self-awareness"): the Docker
// integration relates HubResource runs_on the container whose ID this is,
// and Unraid its container of that ID. Proxmox matches the hub's addresses
// instead, and chadash-node can name the hub's container too (its
// engine.self verb).

// containerIDRE is a Docker container ID, or the 12-character prefix a
// container's default hostname is.
var containerIDRE = regexp.MustCompile(`^[0-9a-f]{12,64}$`)

// mountinfoIDRE finds the container ID in the paths Docker bind-mounts into
// every container (/etc/hostname, /etc/hosts, /etc/resolv.conf), which name
// /var/lib/docker/containers/<id>/ on the host.
var mountinfoIDRE = regexp.MustCompile(`/containers/([0-9a-f]{64})/`)

// HubContainerID is the Docker container ID the hub runs in, full or as its
// 12-character prefix, or "" when the hub isn't in a container (or can't
// tell). It reads /proc/self/mountinfo first, which works whatever the
// container's hostname, then the hostname, when it looks like a container
// ID. A variable, so tests can stand in for the machine.
var HubContainerID = sync.OnceValue(func() string {
	if f, err := os.Open("/proc/self/mountinfo"); err == nil {
		defer f.Close()
		if id := containerFromMountinfo(bufio.NewScanner(f)); id != "" {
			return id
		}
	}
	if h, err := os.Hostname(); err == nil && containerIDRE.MatchString(h) {
		return h
	}
	return ""
})

// containerFromMountinfo reads the container ID from the mount of
// /etc/hostname, /etc/hosts or /etc/resolv.conf: inside a container their
// source (the fourth field) is under /var/lib/docker/containers/<id>/. Only
// those mount points count, so a process on a Docker host, which sees the
// containers' own mounts, never takes one of them for itself.
func containerFromMountinfo(sc *bufio.Scanner) string {
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 5 {
			continue
		}
		switch f[4] {
		case "/etc/hostname", "/etc/hosts", "/etc/resolv.conf":
			if m := mountinfoIDRE.FindStringSubmatch(f[3]); m != nil {
				return m[1]
			}
		}
	}
	return ""
}

// IsHubContainer reports whether a Docker container ID (full or short) is
// the hub's own container: one is a prefix of the other, at least 12
// characters long.
func IsHubContainer(id string) bool {
	hub := HubContainerID()
	id = strings.ToLower(strings.TrimPrefix(id, "sha256:"))
	if len(hub) < 12 || len(id) < 12 || !containerIDRE.MatchString(id) {
		return false
	}
	return strings.HasPrefix(id, hub) || strings.HasPrefix(hub, id)
}
