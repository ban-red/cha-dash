package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// The Mapper holds every real value the spike saw and the fake that replaces
// it. It's built in memory from the env file and the raw captures, never
// written to disk, and shared by the sanitizer and the leak check.
//
// Scheme (matches testdata/importers/homepage/dogfood/):
//   - host nicknames stay (pluto, minig5, mars, mars-1, mars-2, io, jupiter-nas);
//   - IPv4 → 10.0.11.x for the LAN, 10.0.12.x, 10.0.13.x … for other subnets,
//     last octet kept; IPv6 → fd00:11::n / fe80::n;
//   - domains → *.lab.example;
//   - MACs → BC:24:11:00:00:nn (Proxmox OUI, zeroed); fingerprints → 00:…:nn;
//     UUIDs → 00000000-0000-4000-8000-…nn; serials → FAKESERIALnnnn; e-mail → admin@lab.example;
//   - names (guests, storage, pools, tags, groups, users) stay when every word
//     in them is on the allowlist below, and become neutral names otherwise;
//   - the token ID becomes <user>@<realm>!chadash; secrets never appear in responses.

// allowWords are words that can't identify a person or a home. A name made only
// of these (and digits) is kept; anything else is mapped.
var allowWords = strings.Fields(`
pluto minig5 mini g5 mars io jupiter nas titan
pve proxmox pbs backup backups bak vzdump dump local lvm thin thinpool zfs rpool tank data pool pools store storage
datastore nfs cifs smb iscsi ceph cephfs rbd dir directory iso isos images image templates template tmpl vztmpl snippets import
ssd nvme hdd disk disks sata usb ext ext4 xfs btrfs fast slow cold hot scratch archive media share shares
games game gaming vm vms ct cts lxc qemu kvm guest guests host hosts node nodes cluster
opnsense pfsense router firewall fw wan lan homeassistant home assistant haos ha frigate nvr cctv
docker pvedocker dockerproxy compose podman portainer k3s k8s kube kubernetes swarm
ubuntu debian windows win win10 win11 win2022 linux iolinux fedora alpine arch alma rocky centos nixos
truenas unraid omv openmediavault cockpit tailscale wireguard vpn plex jellyfin emby media
dev devbox test testing sandbox lab server srv app apps web db sql postgres mysql redis mail dns pihole adguard
nginx traefik caddy proxy monitoring grafana prometheus influx uptime kuma
chadash cha dash main primary secondary default localnetwork vmbr0 vmbr1 bond0 eth0
root admin api ro rw users user group groups auditor readonly monitor homepage
var lib vz mnt opt srv etc usr tmp private rootdir subvol dump
`)

var (
	reFP        = regexp.MustCompile(`(?i)\b(?:[0-9a-f]{2}:){31}[0-9a-f]{2}\b`)
	reMAC       = regexp.MustCompile(`(?i)\b(?:[0-9a-f]{2}[:-]){5}[0-9a-f]{2}\b`)
	reUUID      = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)
	reEmail     = regexp.MustCompile(`(?i)\b[a-z0-9._%+-]+@[a-z0-9-]+(?:\.[a-z0-9-]+)*\.[a-z]{2,}\b`)
	reIP4       = regexp.MustCompile(`(?:^|[^0-9.])((?:\d{1,3}\.){3}\d{1,3})(?:$|[^0-9])`)
	reIP6       = regexp.MustCompile(`(?i)[0-9a-f:]*:[0-9a-f:]*[0-9a-f](?:%[a-z0-9]+)?`)
	reLANSuffix = regexp.MustCompile(`(?i)([a-z0-9-])\.(?:lan|local|localdomain|home|internal|home\.arpa)\b`)
)

type rep struct {
	real, fake, class string
	bounded           bool // match only between non-alphanumerics
}

// Needle is one real value the leak check looks for.
type Needle struct {
	Value   string
	Class   string
	Bounded bool
}

type Mapper struct {
	allow  map[string]bool
	reps   map[string]rep // lower(real) → rep
	ip4    map[string]string
	subnet map[string]string
	ip6    map[string]string
	pat    map[string]map[string]string // class → canonical real → fake (mac, fp, uuid, email)
	count  map[string]int               // per-class counters for neutral names
	extra  []Needle                     // needles that aren't replacements (secrets, subnets, domains)
	ins    []Instance
	cur    Instance        // the instance being sanitized
	lan16  map[string]bool // "a.b" of every node IP: these are never treated as Docker bridges
	fakes  map[string]bool // every fake handed out (lowercase), so a fake is never taken for a real value
}

func newMapper(ins []Instance) *Mapper {
	m := &Mapper{allow: map[string]bool{}, reps: map[string]rep{}, ip4: map[string]string{}, subnet: map[string]string{},
		ip6: map[string]string{}, pat: map[string]map[string]string{}, count: map[string]int{}, ins: ins, fakes: map[string]bool{}, lan16: map[string]bool{}}
	for _, w := range allowWords {
		m.allow[w] = true
	}
	return m
}

func words(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	})
}

// allowed reports whether every word of s is on the allowlist or numeric.
func (m *Mapper) allowed(s string) bool {
	ws := words(s)
	if len(ws) == 0 {
		return true
	}
	for _, w := range ws {
		if m.allow[w] || strings.Trim(w, "0123456789") == "" {
			continue
		}
		// a known word followed by digits (pve1, disk2, win11)
		if t := strings.TrimRight(w, "0123456789"); t != w && m.allow[t] {
			continue
		}
		return false
	}
	return true
}

func (m *Mapper) add(real, fake, class string, bounded bool) {
	if real == "" || strings.EqualFold(real, fake) {
		return
	}
	m.fakes[strings.ToLower(fake)] = true
	k := strings.ToLower(real)
	if _, ok := m.reps[k]; !ok {
		m.reps[k] = rep{real: real, fake: fake, class: class, bounded: bounded}
	}
}

// name keeps an allowlisted name, or maps it to prefix-N (stable per value).
func (m *Mapper) name(real, class, prefix string) string {
	if real == "" {
		return real
	}
	if r, ok := m.reps[strings.ToLower(real)]; ok {
		return r.fake
	}
	if m.allowed(real) {
		return real
	}
	m.count[class]++
	fake := fmt.Sprintf("%s-%d", prefix, m.count[class])
	m.add(real, fake, class, true)
	return fake
}

// guestName keeps an allowlisted guest name or maps it to vm-<vmid> / ct-<vmid>.
func (m *Mapper) guestName(real, fake string) string {
	if r, ok := m.reps[strings.ToLower(real)]; ok {
		return r.fake
	}
	if real == "" || m.allowed(real) {
		return real
	}
	m.add(real, fake, "guest", true)
	return fake
}

// force maps real to fake even when real is allowlisted (node, cluster, token).
func (m *Mapper) force(real, fake, class string) {
	if _, ok := m.reps[strings.ToLower(real)]; ok {
		return
	}
	m.add(real, fake, class, true)
}

// ── addresses and patterns ────────────────────────────────────────────────

var keepIP4 = map[string]bool{"0.0.0.0": true, "1.1.1.1": true, "1.0.0.1": true, "8.8.8.8": true, "8.8.4.4": true, "9.9.9.9": true}

func (m *Mapper) mapIP4(ip string) string {
	if m.fakes[ip] {
		return ip
	}
	p := net.ParseIP(ip).To4()
	if p == nil || keepIP4[ip] || p[0] == 127 || p[0] == 255 || p[0] >= 224 || (p[0] == 169 && p[1] == 254) {
		return ip
	}
	if f, ok := m.ip4[ip]; ok {
		return f
	}
	// Docker's default bridge pools (172.17–31.x) are the same on every Docker
	// host and identify nothing, so they stay, unless the LAN itself is there.
	if p[0] == 172 && p[1] >= 17 && p[1] <= 31 && !m.lan16[fmt.Sprintf("%d.%d", p[0], p[1])] {
		return ip
	}
	sub := fmt.Sprintf("%d.%d.%d", p[0], p[1], p[2])
	fs, ok := m.subnet[sub]
	if !ok {
		// The first subnet (the node IPs, mapped first) is the LAN: 10.0.11.x.
		// Others get 10.0.20.x, 10.0.24.x … so a prefix up to /22 never overlaps another.
		fs = "10.0.11"
		if len(m.subnet) > 0 {
			fs = fmt.Sprintf("10.0.%d", 16+4*len(m.subnet))
		}
		m.subnet[sub] = fs
		if fs != sub {
			m.extra = append(m.extra, Needle{Value: sub + ".", Class: "subnet", Bounded: true})
		}
	}
	f := fmt.Sprintf("%s.%d", fs, p[3])
	m.ip4[ip] = f
	m.add(ip, f, "ip4", true)
	return f
}

func (m *Mapper) mapIP6(s string) string {
	if m.fakes[strings.ToLower(s)] {
		return s
	}
	core, zone, _ := strings.Cut(s, "%")
	ip := net.ParseIP(core)
	if ip == nil || ip.To4() != nil || ip.IsLoopback() || ip.IsUnspecified() {
		return s
	}
	// A hand-set link-local such as fe80::1 (a router's) identifies nothing; keep it.
	if b := ip.To16(); ip.IsLinkLocalUnicast() && b[8]|b[9]|b[10]|b[11]|b[12]|b[13] == 0 {
		return s
	}
	canon := ip.String()
	if f, ok := m.ip6[canon]; ok {
		return f + suffixZone(zone)
	}
	n := len(m.ip6) + 1
	f := fmt.Sprintf("fd00:11::%x", n)
	if ip.IsLinkLocalUnicast() {
		f = fmt.Sprintf("fe80::be24:11ff:fe00:%x", n) // EUI-64 of a fake BC:24:11:00:00:xx MAC
	}
	m.ip6[canon] = f
	m.add(canon, f, "ip6", true)
	if !ip.IsLinkLocalUnicast() {
		// The site's /48 (an ISP-delegated or ULA prefix) must not survive either.
		b := ip.To16()
		m.extra = append(m.extra, Needle{Value: fmt.Sprintf("%x:%x:%x:", uint16(b[0])<<8|uint16(b[1]),
			uint16(b[2])<<8|uint16(b[3]), uint16(b[4])<<8|uint16(b[5])), Class: "ip6-prefix"})
	}
	if core != canon {
		m.add(core, f, "ip6", true)
	}
	return f + suffixZone(zone)
}

func suffixZone(z string) string {
	if z == "" {
		return ""
	}
	return "%" + z
}

func (m *Mapper) mapPat(class, real string) string {
	if m.fakes[strings.ToLower(real)] || m.fakes[strings.ToLower(strings.ReplaceAll(real, "-", ":"))] {
		return real
	}
	canon := strings.ToUpper(strings.ReplaceAll(real, "-", ":"))
	if class == "email" || class == "uuid" {
		canon = strings.ToLower(real)
	}
	if m.pat[class] == nil {
		m.pat[class] = map[string]string{}
	}
	if f, ok := m.pat[class][canon]; ok {
		return f
	}
	n := len(m.pat[class]) + 1
	var f string
	switch class {
	case "mac":
		if strings.Trim(canon, "0:") == "" {
			return real // 00:00:00:00:00:00 (loopback)
		}
		f = fmt.Sprintf("BC:24:11:00:00:%02X", n)
	case "fp":
		f = strings.Repeat("00:", 30) + fmt.Sprintf("%02X:%02X", n/256, n%256)
	case "uuid":
		f = fmt.Sprintf("00000000-0000-4000-8000-%012d", n)
	case "email":
		f = "admin@lab.example"
		if n > 1 {
			f = fmt.Sprintf("admin%d@lab.example", n)
		}
		if _, dom, ok := strings.Cut(canon, "@"); ok && !genericDomain(dom) {
			m.extra = append(m.extra, Needle{Value: dom, Class: "domain"})
		}
	}
	m.pat[class][canon] = f
	m.add(real, f, class, false)
	if class == "mac" || class == "fp" {
		plain := strings.ReplaceAll(canon, ":", "")
		m.extra = append(m.extra, Needle{Value: plain, Class: class}, Needle{Value: strings.ReplaceAll(canon, ":", "-"), Class: class})
		m.add(canon, f, class, false)
	}
	return f
}

// genericDomain is a suffix that identifies nobody (.lan, .home.arpa …).
func genericDomain(d string) bool {
	switch strings.ToLower(d) {
	case "", "lan", "local", "localdomain", "home", "internal", "home.arpa", "intranet", "corp", "example", "lab.example", "proxmox.com":
		return true
	}
	return false
}

// mapHost maps a DNS name (or IP) the spike saw to *.lab.example.
func (m *Mapper) mapHost(h string) string {
	h = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h)), ".")
	if h == "" || h == "localhost" || strings.HasSuffix(h, ".lab.example") {
		return h
	}
	if net.ParseIP(h) != nil {
		if strings.Contains(h, ":") {
			return m.mapIP6(h)
		}
		return m.mapIP4(h)
	}
	if r, ok := m.reps[h]; ok {
		return r.fake
	}
	label, dom, _ := strings.Cut(h, ".")
	fl := label
	if !m.allowed(label) {
		m.count["host"]++
		fl = fmt.Sprintf("host-%d", m.count["host"])
		m.add(label, fl, "hostname", true)
	}
	if dom == "" {
		return fl
	}
	f := fl + ".lab.example"
	m.add(h, f, "hostname", false)
	if !genericDomain(dom) {
		m.add("."+dom, ".lab.example", "domain", false)
		m.extra = append(m.extra, Needle{Value: dom, Class: "domain"})
	}
	return f
}

// str applies every pattern and replacement to s.
func (m *Mapper) str(s string) string {
	if s == "" {
		return s
	}
	s = reFP.ReplaceAllStringFunc(s, func(x string) string { return m.mapPat("fp", x) })
	s = reMAC.ReplaceAllStringFunc(s, func(x string) string { return m.mapPat("mac", x) })
	s = reUUID.ReplaceAllStringFunc(s, func(x string) string { return m.mapPat("uuid", x) })
	s = reEmail.ReplaceAllStringFunc(s, func(x string) string { return m.mapPat("email", x) })
	s = replaceSubmatch(reIP4, s, m.mapIP4)
	s = reIP6.ReplaceAllStringFunc(s, func(x string) string {
		core, _, _ := strings.Cut(x, "%")
		if strings.Count(core, ":") < 2 || (!strings.Contains(core, "::") && strings.Count(core, ":") < 7) {
			return x
		}
		if ip := net.ParseIP(core); ip == nil || ip.To4() != nil {
			return x
		}
		return m.mapIP6(x)
	})
	// Anything still ending in a LAN-only suffix (a name that looks like a host) gets *.lab.example.
	return reLANSuffix.ReplaceAllString(m.applyReps(s), "$1.lab.example")
}

// replaceSubmatch replaces capture group 1 of every match.
func replaceSubmatch(re *regexp.Regexp, s string, f func(string) string) string {
	idx := re.FindAllStringSubmatchIndex(s, -1)
	if idx == nil {
		return s
	}
	var b strings.Builder
	last := 0
	for _, ix := range idx {
		b.WriteString(s[last:ix[2]])
		b.WriteString(f(s[ix[2]:ix[3]]))
		last = ix[3]
	}
	b.WriteString(s[last:])
	return b.String()
}

func (m *Mapper) sortedReps() []rep {
	rs := make([]rep, 0, len(m.reps))
	for _, r := range m.reps {
		rs = append(rs, r)
	}
	sort.Slice(rs, func(i, j int) bool {
		if len(rs[i].real) != len(rs[j].real) {
			return len(rs[i].real) > len(rs[j].real)
		}
		return rs[i].real < rs[j].real
	})
	return rs
}

func isAlnum(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
}

// findFold returns the index of needle in s (case-insensitive) at or after
// from, honoring word boundaries when bounded. -1 if none.
func findFold(lo, lneedle string, from int, bounded bool) int {
	for from <= len(lo) {
		i := strings.Index(lo[from:], lneedle)
		if i < 0 {
			return -1
		}
		i += from
		end := i + len(lneedle)
		if !bounded || ((i == 0 || !isAlnum(lo[i-1])) && (end == len(lo) || !isAlnum(lo[end]))) {
			return i
		}
		from = i + 1
	}
	return -1
}

func (m *Mapper) applyReps(s string) string {
	for _, r := range m.sortedReps() {
		lr := strings.ToLower(r.real)
		lo := strings.ToLower(s)
		if !strings.Contains(lo, lr) {
			continue
		}
		var b strings.Builder
		pos := 0
		for {
			i := findFold(lo, lr, pos, r.bounded)
			if i < 0 {
				break
			}
			b.WriteString(s[pos:i])
			b.WriteString(r.fake)
			pos = i + len(r.real)
		}
		b.WriteString(s[pos:])
		s = b.String()
	}
	return s
}

// Needles is every real value the leak check must not find.
func (m *Mapper) Needles() []Needle {
	var out []Needle
	seen := map[string]bool{}
	push := func(n Needle) {
		k := strings.ToLower(n.Value)
		if len(n.Value) < 2 || seen[k] {
			return
		}
		seen[k] = true
		out = append(out, n)
	}
	for _, in := range m.ins {
		push(Needle{Value: in.Secret, Class: "token-secret"})
		push(Needle{Value: in.TokenID, Class: "token-id"})
		push(Needle{Value: in.URL.String(), Class: "url"})
		push(Needle{Value: in.URL.Host, Class: "url-host"})
		if h := strings.ToLower(in.URL.Hostname()); m.mapHost(h) != h {
			push(Needle{Value: h, Class: "hostname", Bounded: true})
		}
	}
	for _, r := range m.sortedReps() {
		if r.class == "domain" { // ".example.net" → covered by the bare-domain needle
			continue
		}
		push(Needle{Value: r.real, Class: r.class, Bounded: r.bounded})
	}
	for _, n := range m.extra {
		push(n)
	}
	return out
}

// ── collecting real values from the raw captures ─────────────────────────

func rawFiles(rawDir, inst string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(filepath.Join(rawDir, inst), func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(p, ".json") && !strings.HasPrefix(d.Name(), "_") {
			out = append(out, p)
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}

func loadData(path string) any {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var e map[string]any
	dec := json.NewDecoder(bytesReader(b))
	dec.UseNumber()
	if dec.Decode(&e) != nil {
		return nil
	}
	return e["data"]
}

func list(v any) []map[string]any {
	a, _ := v.([]any)
	var out []map[string]any
	for _, x := range a {
		if mm, ok := x.(map[string]any); ok {
			out = append(out, mm)
		}
	}
	return out
}

// buildMapper collects every real value from env + raw in a fixed order, so
// the same captures always give the same fakes.
func buildMapper(ins []Instance, rawDir string) (*Mapper, error) {
	m := newMapper(ins)
	// 1. Cluster shape first: node IPs claim 10.0.11.x, names get nicknames.
	for _, in := range ins {
		dir := filepath.Join(rawDir, in.Name)
		cs := list(loadData(filepath.Join(dir, "cluster/status.json")))
		var nodes []map[string]any
		for _, e := range cs {
			switch e["type"] {
			case "cluster":
				m.force(str(e["name"]), in.Name, "cluster")
			case "node":
				nodes = append(nodes, e)
			}
		}
		sort.Slice(nodes, func(i, j int) bool { return num(nodes[i]["nodeid"]) < num(nodes[j]["nodeid"]) })
		for _, n := range nodes {
			if p := net.ParseIP(str(n["ip"])).To4(); p != nil {
				m.lan16[fmt.Sprintf("%d.%d", p[0], p[1])] = true
			}
		}
		for i, n := range nodes {
			real := str(n["name"])
			fake := in.Name
			if len(nodes) > 1 {
				fake = fmt.Sprintf("%s-%d", in.Name, i+1)
			}
			// Node names are hostnames: always the nickname in fixtures.
			if !strings.EqualFold(real, fake) {
				m.force(real, fake, "node")
			}
			m.mapIP4(str(n["ip"]))
		}
		// The URL host after the node IPs, so a LAN host keeps 10.0.11.x.
		m.mapHost(in.URL.Hostname())
		// Token: keep a generic owner name, always replace the token name.
		user, realm := in.User(), in.Realm()
		uname, _, _ := strings.Cut(user, "@")
		fu := uname
		if !m.allowed(uname) {
			fu = "api"
		}
		ftok := "chadash"
		if strings.EqualFold(in.Token(), ftok) {
			ftok = "spike"
		}
		m.add(in.TokenID, fu+"@"+realm+"!"+ftok, "token-id", false)
		// The same ID as an API path (/access/users/<user>/token/<name>) and URL-escaped.
		m.add(user+"/token/"+in.Token(), fu+"@"+realm+"/token/"+ftok, "token-id", false)
		m.add(url.PathEscape(user)+"/token/"+url.PathEscape(in.Token()), fu+"%40"+realm+"/token/"+ftok, "token-id", false)
		m.add(url.QueryEscape(in.TokenID), url.QueryEscape(fu+"@"+realm+"!"+ftok), "token-id", false)
		if fu != uname {
			m.add(user, fu+"@"+realm, "user", true)
		}
	}
	// 2. Names and addresses from every response.
	for _, in := range ins {
		files, err := rawFiles(rawDir, in.Name)
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			rel, _ := filepath.Rel(filepath.Join(rawDir, in.Name), f)
			m.collect(in, filepath.ToSlash(rel), loadData(f))
		}
	}
	// 3. Patterns anywhere (IPs, MACs, fingerprints, UUIDs, e-mail, IPv6):
	// running str over every string assigns their fakes.
	for _, in := range ins {
		files, _ := rawFiles(rawDir, in.Name)
		for _, f := range files {
			walkStrings(loadData(f), func(s string) { m.str(s) })
		}
	}
	// A real subnet that equals its fake would pass through unchanged.
	for sub, fs := range m.subnet {
		if sub == fs {
			return nil, fmt.Errorf("a real subnet collides with the fake scheme (10.0.x); change the scheme before sanitizing")
		}
	}
	return m, nil
}

func walkStrings(v any, f func(string)) {
	switch x := v.(type) {
	case string:
		f(x)
	case map[string]any:
		for _, k := range sortedKeys(x) { // sorted, so fakes are numbered the same on every run
			f(k)
			walkStrings(x[k], f)
		}
	case []any:
		for _, y := range x {
			walkStrings(y, f)
		}
	}
}

func (m *Mapper) user(u string) string {
	if u == "" {
		return u
	}
	id, tok, hasTok := strings.Cut(u, "!")
	name, realm, _ := strings.Cut(id, "@")
	fn := m.name(name, "user", "user")
	out := fn + "@" + realm
	if fn != name {
		m.add(id, out, "user", true)
	}
	if hasTok {
		// Every token ID is replaced, generic or not.
		if r, ok := m.reps[strings.ToLower(u)]; ok {
			return r.fake
		}
		m.count["token"]++
		f := fmt.Sprintf("%s!token-%d", out, m.count["token"])
		m.add(u, f, "token-id", false)
		_ = tok
		return f
	}
	return out
}

// collect registers the names in one response.
func (m *Mapper) collect(in Instance, rel string, data any) {
	switch {
	case rel == "cluster/resources.json":
		for _, e := range list(data) {
			switch e["type"] {
			case "qemu":
				m.guestName(str(e["name"]), "vm-"+str(e["vmid"]))
			case "lxc":
				m.guestName(str(e["name"]), "ct-"+str(e["vmid"]))
			case "storage":
				m.name(str(e["storage"]), "storage", "store")
			case "pool":
				m.name(str(e["pool"]), "pool", "pool")
			case "sdn":
				m.name(str(e["sdn"]), "sdn", "zone")
			case "network":
				m.name(str(e["network"]), "sdn", "zone")
			}
			for _, t := range strings.FieldsFunc(str(e["tags"]), func(r rune) bool { return r == ';' || r == ',' || r == ' ' }) {
				m.name(t, "tag", "tag")
			}
		}
	case rel == "storage.json":
		for _, e := range list(data) {
			m.name(str(e["storage"]), "storage", "store")
			if s := str(e["server"]); s != "" {
				m.mapHost(s)
			}
			if u := str(e["username"]); u != "" {
				m.user(u)
			}
			for _, k := range []string{"datastore", "pool", "vgname", "thinpool", "fs-name", "namespace"} {
				m.name(str(e[k]), "storage", "store")
			}
			for _, k := range []string{"path", "export", "mountpoint", "share"} {
				for _, seg := range strings.Split(str(e[k]), "/") {
					m.name(seg, "path", "dir")
				}
			}
		}
	case rel == "pools.json":
		for _, e := range list(data) {
			m.name(str(e["poolid"]), "pool", "pool")
		}
	case rel == "access/acl.json":
		for _, e := range list(data) {
			switch e["type"] {
			case "group":
				m.name(str(e["ugid"]), "group", "group")
			default:
				m.user(str(e["ugid"]))
			}
		}
	case rel == "access/user.json":
		if u, ok := data.(map[string]any); ok {
			if gs, ok := u["groups"].([]any); ok {
				for _, g := range gs {
					m.name(str(g), "group", "group")
				}
			}
		}
	case strings.HasSuffix(rel, "tasks.json") || strings.HasSuffix(rel, ".status.json"):
		items := list(data)
		if mm, ok := data.(map[string]any); ok {
			items = []map[string]any{mm}
		}
		for _, e := range items {
			m.user(str(e["user"]))
		}
	case rel == "cluster/config/join.json":
		if d, ok := data.(map[string]any); ok {
			for _, n := range list(d["nodelist"]) {
				m.mapHost(str(n["pve_addr"]))
				m.mapHost(str(n["ring0_addr"]))
			}
		}
	case strings.HasSuffix(rel, "certificates/info.json"):
		for _, e := range list(data) {
			if sans, ok := e["san"].([]any); ok {
				for _, s := range sans {
					m.mapHost(str(s))
				}
			}
			for _, k := range []string{"subject", "issuer"} {
				for _, part := range strings.FieldsFunc(str(e[k]), func(r rune) bool { return r == '/' || r == ',' }) {
					if v, ok := strings.CutPrefix(strings.TrimSpace(part), "CN="); ok && strings.Contains(v, ".") {
						m.mapHost(v)
					}
				}
			}
		}
	case strings.HasSuffix(rel, "disks/list.json"):
		for _, e := range list(data) {
			if s := str(e["serial"]); len(s) > 2 {
				m.count["serial"]++
				m.add(s, fmt.Sprintf("FAKESERIAL%04d", m.count["serial"]), "serial", false)
			}
			if w := str(e["wwn"]); len(w) > 4 && w != "unknown" {
				m.count["wwn"]++
				m.add(w, fmt.Sprintf("0x5000000000%06d", m.count["wwn"]), "wwn", false)
			}
		}
	case strings.HasSuffix(rel, "/config.json"):
		if c, ok := data.(map[string]any); ok {
			if h := str(c["hostname"]); h != "" {
				m.name(h, "guest", "ct")
			}
			if u := str(c["ciuser"]); u != "" {
				m.name(u, "user", "user")
			}
			for _, t := range strings.FieldsFunc(str(c["tags"]), func(r rune) bool { return r == ';' || r == ',' || r == ' ' }) {
				m.name(t, "tag", "tag")
			}
		}
	case strings.HasSuffix(rel, "backup.json"):
		for _, e := range list(data) {
			m.name(str(e["storage"]), "storage", "store")
		}
	}
}
