package proxmox

import (
	"net"
	"slices"
	"strings"

	"dash.cha.sh/internal/integration"
)

// Finding the guest Cha Dash runs in, best effort, so the Home Graph can say
// so (chadash:hub runs_on the guest) and rebooting or stopping that guest,
// or the host under it, is the critical T4 (the runtime's self-aware rule):
//
//  1. The hub_vmid hint in the config, when the guest exists.
//  2. Else the hub's own addresses: a guest whose primary IPv4 (from the
//     guest agent or the LXC's interfaces) or whose MAC (from its config or
//     its interfaces) is one of this machine's. That finds a hub running
//     directly in an LXC or a VM, or in Docker with host networking. Inside
//     a Docker bridge network the container's addresses are its own, so
//     nothing matches and the hint is the way.
//
// Exactly one match counts; two or more (a cloned MAC, say) count as none.

// localAddrs lists this machine's IPv4 addresses and MACs (upper case),
// skipping loopback and the bridges and tunnels skipIface names. A
// variable, so tests can stand in for the machine.
var localAddrs = func() (ips, macs []string) {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil, nil
	}
	for _, f := range ifs {
		if f.Flags&net.FlagLoopback != 0 || skipIface(f.Name) {
			continue
		}
		if len(f.HardwareAddr) == 6 {
			macs = append(macs, strings.ToUpper(f.HardwareAddr.String()))
		}
		as, err := f.Addrs()
		if err != nil {
			continue
		}
		for _, a := range as {
			if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && usableIPv4(n.IP.String()) {
				ips = append(ips, n.IP.String())
			}
		}
	}
	return ips, macs
}

// hubGuest relates the hub to the guest it runs in, when that can be told.
func (b *builder) hubGuest() {
	var match []*guestFacts
	if b.in.hubVMID != 0 {
		if g := b.guests[b.in.hubVMID]; g != nil {
			match = append(match, g)
		}
	}
	if len(match) == 0 {
		ips, macs := localAddrs()
		for _, vmid := range sortedVMIDs(b.guests) {
			g := b.guests[vmid]
			a := b.in.addrs[g.r.guestKey()]
			mac := a.mac
			if mac == "" && g.cfg != nil {
				mac = g.cfg.mac
			}
			if (a.ip != "" && slices.Contains(ips, a.ip)) || (mac != "" && slices.Contains(macs, strings.ToUpper(mac))) {
				match = append(match, g)
			}
		}
	}
	if len(match) == 1 {
		b.rel(integration.HubResource, "runs_on", match[0].id)
	}
}

func sortedVMIDs(m map[int]*guestFacts) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
