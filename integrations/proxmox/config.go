package proxmox

import (
	"fmt"
	"net"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// guestConfig is what the integration keeps from a guest's config. The rest
// (descriptions, cloud-init, raw lxc lines, SMBIOS UUIDs) is dropped at
// ingestion.
type guestConfig struct {
	onboot   bool
	ostype   string
	agent    bool     // the QEMU guest agent is enabled
	mac      string   // net0's MAC, the key OPNsense's DHCP/ARP also sees
	hostpci  []string // passed-through PCI devices, e.g. "0000:01:00"
	storages []string // storages the guest's disks live on, sorted
}

var (
	diskKeyRE = regexp.MustCompile(`^(ide|sata|scsi|virtio|efidisk|tpmstate|mp)\d+$|^rootfs$`)
	netKeyRE  = regexp.MustCompile(`^net(\d+)$`)
	pciKeyRE  = regexp.MustCompile(`^hostpci\d+$`)
	macRE     = regexp.MustCompile(`^(?i:[0-9a-f]{2}(:[0-9a-f]{2}){5})$`)
)

// parseGuestConfig reads GET /nodes/{node}/{type}/{vmid}/config.
func parseGuestConfig(raw map[string]any) *guestConfig {
	c := &guestConfig{}
	str := func(v any) string {
		switch x := v.(type) {
		case string:
			return x
		case float64:
			return fmt.Sprint(x)
		case bool:
			if x {
				return "1"
			}
			return "0"
		}
		return ""
	}
	c.onboot = str(raw["onboot"]) == "1"
	c.ostype = str(raw["ostype"])
	agent := str(raw["agent"])
	c.agent = agent == "1" || strings.HasPrefix(agent, "1,") || strings.Contains(","+agent+",", ",enabled=1,")
	stores := map[string]bool{}
	netIdx := -1
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := str(raw[k])
		switch {
		case diskKeyRE.MatchString(k):
			if s := diskStorage(v); s != "" {
				stores[s] = true
			}
		case pciKeyRE.MatchString(k):
			first, _, _ := strings.Cut(v, ",")
			first = strings.TrimPrefix(first, "host=")
			if first != "" {
				c.hostpci = append(c.hostpci, first)
			}
		case netKeyRE.MatchString(k):
			n, _ := strconv.Atoi(netKeyRE.FindStringSubmatch(k)[1]) // digits: the pattern matched
			if mac := nicMAC(v); mac != "" && (netIdx < 0 || n < netIdx) {
				c.mac, netIdx = mac, n
			}
		}
	}
	for s := range stores {
		c.storages = append(c.storages, s)
	}
	slices.Sort(c.storages)
	slices.Sort(c.hostpci)
	return c
}

// diskStorage is the storage of a disk value ("local-lvm:vm-100-disk-0,
// size=4M" → "local-lvm"), or "" for a passed-through device, a bind mount,
// an empty drive or a CD-ROM.
func diskStorage(v string) string {
	first, _, _ := strings.Cut(v, ",")
	if first == "" || first == "none" || strings.HasPrefix(first, "/") || strings.Contains(v, "media=cdrom") {
		return ""
	}
	s, _, ok := strings.Cut(first, ":")
	if !ok || s == "" {
		return ""
	}
	return s
}

// nicMAC is a NIC's MAC: "virtio=BC:24:11:…,bridge=vmbr0" (QEMU) or
// "name=eth0,hwaddr=BC:24:11:…" (LXC).
func nicMAC(v string) string {
	for part := range strings.SplitSeq(v, ",") {
		k, val, ok := strings.Cut(part, "=")
		if ok && macRE.MatchString(val) && (k == "hwaddr" || k == "macaddr" || !strings.Contains(k, "addr")) {
			return strings.ToUpper(val)
		}
	}
	return ""
}

// skipIface are interfaces whose addresses never identify a guest: loopback,
// Docker bridges and veths, VPN tunnels.
func skipIface(name string) bool {
	for _, p := range []string{"lo", "docker", "br-", "veth", "tailscale", "wg", "tun", "zt", "virbr", "cni", "flannel", "kube"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// usableIPv4 is a unicast IPv4 that isn't loopback or link-local.
func usableIPv4(s string) bool {
	ip := net.ParseIP(s)
	return ip != nil && ip.To4() != nil && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !ip.IsUnspecified()
}

// pickAddr chooses a guest's primary address: the IPv4 of the interface
// with the config's MAC, else the first usable IPv4 on an interface that
// isn't a bridge or tunnel.
func pickAddr(mac string, ifs []ifaceAddrs) (addr, bool) {
	if mac != "" {
		for _, f := range ifs {
			if strings.EqualFold(f.mac, mac) {
				for _, ip := range f.v4 {
					if usableIPv4(ip) {
						return addr{ip: ip, mac: strings.ToUpper(f.mac)}, true
					}
				}
			}
		}
	}
	for _, f := range ifs {
		if skipIface(f.name) {
			continue
		}
		for _, ip := range f.v4 {
			if usableIPv4(ip) {
				return addr{ip: ip, mac: strings.ToUpper(f.mac)}, true
			}
		}
	}
	return addr{}, false
}

type ifaceAddrs struct {
	name, mac string
	v4        []string
}

func primaryLXC(ifs []lxcIface, mac string) (addr, bool) {
	var out []ifaceAddrs
	for _, f := range ifs {
		a := ifaceAddrs{name: f.Name}
		if f.HWAddr != nil {
			a.mac = *f.HWAddr
		}
		for _, x := range f.Addrs {
			if x.Type == "inet" {
				a.v4 = append(a.v4, x.IP)
			}
		}
		out = append(out, a)
	}
	return pickAddr(mac, out)
}

func primaryAgent(ai agentIfaces, mac string) (addr, bool) {
	var out []ifaceAddrs
	for _, f := range ai.Result {
		a := ifaceAddrs{name: f.Name, mac: f.HWAddr}
		for _, x := range f.Addrs {
			if x.Type == "ipv4" {
				a.v4 = append(a.v4, x.IP)
			}
		}
		out = append(out, a)
	}
	return pickAddr(mac, out)
}
