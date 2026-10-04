package proxmox

import (
	"fmt"
	"strconv"
	"strings"

	"dash.cha.sh/internal/integration"
)

// adapter holds what differs between the PVE versions the fleet runs (spike
// 4.1 captured 8.4.19, 8.4.21, 9.0.9, 9.1.5 and 9.2.20). It's picked from
// /version's release and refined by the token's privileges.
type adapter struct {
	release      string // "8.4", "9.2"
	major, minor int
	// memAvailable: nodes/{n}/status has memory.available (9.x), so host
	// memory is (total − available) / total; 8.4 only has used / total.
	memAvailable bool
	// guestMemHost: on 9.x a VM's mem is the host-side figure and can exceed
	// maxmem (iolinux: 53.6 of 52.4 GB), so mem.pct is clamped.
	guestMemHost bool
	// agentIPs: the guest agent's network-get-interfaces is readable. 9.x's
	// PVEAuditor has VM.GuestAgent.Audit for it; 8.4 needs VM.Monitor, far
	// too powerful to ask for, so 8.4 VMs have no address unless the token
	// happens to hold it.
	agentIPs bool
	// newer is set for a major version this integration hasn't been tested
	// against; it reads it like the newest one it knows.
	newer bool
}

// Supported majors.
const (
	minMajor    = 8
	testedMajor = 9
)

// adapterFor picks the adapter for a /version release ("9.0"), or returns a
// RepairError for a version Cha Dash doesn't read.
func adapterFor(v versionInfo) (*adapter, error) {
	rel := v.Release
	if rel == "" {
		rel = v.Version
	}
	major, minor, ok := parseRelease(rel)
	if !ok {
		return nil, &integration.RepairError{Code: integration.CodeAPIChanged,
			Message: fmt.Sprintf("Proxmox VE reported a version Cha Dash can't read (%q)", rel)}
	}
	if major < minMajor {
		return nil, &integration.RepairError{Code: integration.CodeUnsupportedVersion,
			Message: fmt.Sprintf("Proxmox VE %s isn't supported: Cha Dash reads 8.x and 9.x. Upgrade the host to read it", displayVersion(v))}
	}
	a := &adapter{release: fmt.Sprintf("%d.%d", major, minor), major: major, minor: minor}
	if major >= 9 {
		a.memAvailable, a.guestMemHost = true, true
	}
	a.newer = major > testedMajor
	return a, nil
}

// withGrants refines the adapter with what the token may read.
func (a *adapter) withGrants(p privileges) {
	switch {
	case p.has("/vms", "VM.Monitor"):
		a.agentIPs = true // any version: the 8.x way
	case a.major >= 9:
		a.agentIPs = p.has("/vms", "VM.GuestAgent.Audit")
	default:
		a.agentIPs = false
	}
}

func parseRelease(s string) (major, minor int, ok bool) {
	parts := strings.SplitN(s, ".", 3)
	if len(parts) < 2 {
		return 0, 0, false
	}
	ma, err1 := strconv.Atoi(parts[0])
	mi, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return ma, mi, true
}

// displayVersion is the full version ("9.0.9"), or the release.
func displayVersion(v versionInfo) string {
	if v.Version != "" {
		return v.Version
	}
	return v.Release
}
