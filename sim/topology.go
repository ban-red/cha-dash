package sim

import (
	"cmp"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"
	"time"
)

// The Demo Home is a fictional family home, shaped like what the v0.1
// integrations publish, not like any real lab. Its hosts are named after
// birds: wren (the network core: OPNsense, AdGuard Home, Tailscale), the
// 3-node cluster aviary (heron runs Home Assistant and Frigate, egret the
// Docker LXC rook, stork the file server), the workstation owl, and the
// Unraid box magpie with PBS in a container. Addresses are 10.20.30.x and
// *.home.example only.

// topology is the static part of the world: resources at rest, relations and
// metric definitions. Routines and incidents are applied on top of it.
type topology struct {
	res     map[string]*Resource
	rels    []Relation
	specs   []*metricSpec
	specIdx map[string]*metricSpec
}

func (tp *topology) spec(res, key string) *metricSpec { return tp.specIdx[res+"|"+key] }

// metricSpec defines one series.
type metricSpec struct {
	res, key, unit string
	min, max       float64
	band           [2]float64
	warn, crit     float64
	dec            int
	source         string // integration that reports it; "" = a built-in check
	awake          bool   // reported unknown (null) while its resource is spun down (state idle)
	fn             valueFn
	hk             uint64      // noise stream key
	adj            []*instance // incidents that shape this series
}

type valueFn func(e *eval, m *metricSpec, t time.Time) float64

// wave is the common shape of a metric: a base level, a diurnal swing, slow
// wandering noise and a little fast jitter, plus optional extras.
type wave struct {
	base, amp, peak float64 // amp: diurnal swing above base, peaking at hour peak
	slow, fast      float64 // noise amplitudes
	extra           func(t time.Time) float64
}

func (w wave) fn() valueFn {
	return func(e *eval, m *metricSpec, t time.Time) float64 {
		v := w.base + w.amp*diurnal(t, w.peak)
		if w.slow != 0 {
			v += w.slow * e.s.rng.smooth(m.hk, t, 20*time.Minute)
		}
		if w.fast != 0 {
			v += w.fast * e.s.rng.jitter(m.hk, t)
		}
		if w.extra != nil {
			v += w.extra(t)
		}
		return v
	}
}

// nightlyBackups is 1 during the nightly backup window (02:00–03:05) and
// ramps at its edges.
func nightlyBackups(t time.Time) float64 { return bump(t, 2.55, 0.65) }

// eveningMedia is the household's evening streaming bump.
func eveningMedia(t time.Time) float64 { return bump(t, 21.25, 2.75) }

// The home's addresses: one LAN, 10.20.30.0/24.
const (
	ipRouter    = "10.20.30.1"
	ipAdGuard   = "10.20.30.3"
	ipMagpie    = "10.20.30.20"
	ipHA        = "10.20.30.40"
	ipFrigate   = "10.20.30.41"
	magpieProxy = "tcp://" + ipMagpie + ":2375"
)

type builder struct {
	start time.Time
	rng   rng
	tp    *topology
}

func buildTopology(start time.Time, r rng) *topology {
	b := &builder{start: start, rng: r, tp: &topology{res: map[string]*Resource{}, specIdx: map[string]*metricSpec{}}}
	b.integrations()
	b.network()
	b.wren()
	b.aviary()
	b.heron()
	b.egret()
	b.stork()
	b.owl()
	b.magpie()
	b.rook()
	b.backups()
	b.routes()
	b.checks()
	b.weather()
	b.calendars()
	b.finish()
	return b.tp
}

// ── helpers ──────────────────────────────────────────────────────────────

// ago is a seeded moment between minH and maxH hours before start.
func (b *builder) ago(id string, minH, maxH float64) time.Time {
	h := minH + (maxH-minH)*b.rng.u(hashKey("ago:"+id), 0)
	return b.start.Add(-time.Duration(h * float64(time.Hour))).Truncate(time.Second)
}

func (b *builder) add(r *Resource) *Resource {
	if _, dup := b.tp.res[r.ID]; dup {
		panic("sim: duplicate resource " + r.ID)
	}
	if r.Health.Status == "" {
		since := b.start.Add(-72 * time.Hour)
		if v, ok := r.Attrs["started_at"].(time.Time); ok {
			since = v
		} else if v, ok := r.Attrs["boot_at"].(time.Time); ok {
			since = v
		}
		r.Health = Health{Status: HealthOK, Since: since}
	}
	b.tp.res[r.ID] = r
	return r
}

func (b *builder) rel(from string, t RelType, to string) {
	b.tp.rels = append(b.tp.rels, Relation{From: from, Type: t, To: to})
}

func (b *builder) metric(m *metricSpec) {
	m.hk = hashKey(m.res + "|" + m.key)
	k := m.res + "|" + m.key
	if _, dup := b.tp.specIdx[k]; dup {
		panic("sim: duplicate metric " + k)
	}
	b.tp.specIdx[k] = m
	b.tp.specs = append(b.tp.specs, m)
}

func pct(res, key, source string, band [2]float64, warn, crit float64, w wave) *metricSpec {
	return &metricSpec{res: res, key: key, unit: "%", min: 0, max: 100, band: band, warn: warn, crit: crit, dec: 1, source: source, fn: w.fn()}
}

// cpuMem adds cpu.pct and mem.pct for a host or guest.
func (b *builder) cpuMem(res, source string, cpu, mem wave, memBand [2]float64) {
	b.metric(pct(res, "cpu.pct", source, [2]float64{0, max(10, cpu.base*2.5)}, 0, 0, cpu))
	b.metric(pct(res, "mem.pct", source, memBand, 90, 98, mem))
}

// Hosts have no temp.c: neither the PVE API nor the Unraid API reports a CPU
// temperature (spike 4.1, integrations/unraid). It needs the node agent.

// check adds a latency series. Without a source it's the hub's own HTTP
// check of an app; with one, the series of a built-in check resource.
func (b *builder) check(res string, ms float64, source ...string) {
	m := &metricSpec{res: res, key: "http.ms", unit: "ms", min: 1, max: 5000, band: [2]float64{1, max(150, ms*2.5)}, warn: 800, crit: 2000, dec: 0,
		fn: wave{base: ms, amp: ms * 0.25, peak: 21, slow: ms * 0.12, fast: ms * 0.08}.fn()}
	if len(source) > 0 {
		m.source = source[0]
	}
	b.metric(m)
}

func (b *builder) count(res, key, source string, lo, hi float64, band [2]float64, fn valueFn) {
	b.metric(&metricSpec{res: res, key: key, unit: "count", min: lo, max: hi, band: band, dec: 0, source: source, fn: fn})
}

// constant is a series that doesn't move during the day: a library's size.
func constant(v float64) valueFn {
	return func(*eval, *metricSpec, time.Time) float64 { return v }
}

// growing is a running total that grows by perHour from v at the start.
func (b *builder) growing(v, perHour float64) valueFn {
	return func(e *eval, m *metricSpec, t time.Time) float64 {
		return float64(int64(v + perHour*hours(t.Sub(b.start))))
	}
}

func pveUI(host string) string { return "https://" + host + ".home.example:8006/" }

func guestLink(host, typ string, vmid int) Link {
	return Link{Title: "Proxmox", URL: fmt.Sprintf("%s#v1:0:=%s%%2F%d", pveUI(host), typ, vmid)}
}

func containerID(r rng, id string, gen int) string {
	return fmt.Sprintf("%012x", splitmix64(r.seed^hashKey("cid:"+id)^uint64(gen))>>16)
}

// ── integrations ─────────────────────────────────────────────────────────

type integ struct {
	id, typ, tier, auth, transport string
	readOnly                       bool
	version                        string
	monitors                       string
	group                          string
	name                           string // the instance's name, when it isn't its ID
}

// integrationTable lists the hub's integration instances. The Proxmox
// versions are mixed 9.x, as a fleet updated one host at a time is.
var integrationTable = []integ{
	{"proxmox-wren", "proxmox", "go", "token", "https", true, "9.1.4", "pve:wren", "Servers", ""},
	{"proxmox-aviary", "proxmox", "go", "token", "https", true, "9.0.11", "pve:aviary", "Servers", ""},
	{"proxmox-owl", "proxmox", "go", "token", "https", true, "9.2.3", "pve:owl", "Servers", ""},
	{"unraid-magpie", "unraid", "go", "key", "https", true, "7.2.4", "unraid:magpie", "Servers", ""},
	{"docker-rook", "docker", "go", "socket-proxy", "tcp", true, "", "docker:rook", "Servers", ""},
	{"docker-magpie", "docker", "go", "socket-proxy", "tcp", true, "", "docker:magpie", "Servers", ""},
	{"opnsense", "opnsense", "go", "key", "https", true, "", "net:opnsense", "Servers", ""},
	{"adguard", "adguard", "go", "basic", "http", true, "0.107.64", "app:adguard", "Services", ""},
	{"pbs", "pbs", "go", "token", "https", true, "4.0", "app:pbs", "Backup", ""},
	{"homeassistant", "homeassistant", "go", "token", "https", false, "", "app:homeassistant", "Home", ""},
	{"frigate", "frigate", "go", "none", "http", true, "0.16.1", "app:frigate", "Home", ""},
	{"traefik", "traefik", "go", "basic", "http", true, "3.5.3", "app:traefik", "Services", ""},
	{"uptimekuma", "uptimekuma", "go", "key", "https", true, "", "app:uptimekuma", "Services", ""},
	{"plex", "plex", "go", "token", "https", true, "", "app:plex", "Media", ""},
	{"jellyfin", "jellyfin", "go", "key", "https", true, "10.10.7", "app:jellyfin", "Media", ""},
	{"tautulli", "tautulli", "spec", "key", "https", true, "", "app:tautulli", "Media", ""},
	{"sonarr", "sonarr", "go", "key", "https", true, "", "app:sonarr", "Media", ""},
	{"radarr", "radarr", "go", "key", "https", true, "", "app:radarr", "Media", ""},
	{"bazarr", "bazarr", "spec", "key", "https", true, "", "app:bazarr", "Media", ""},
	{"seerr", "seerr", "spec", "key", "https", true, "2.7.3", "app:seerr", "Media", ""},
	{"audiobookshelf", "audiobookshelf", "go", "key", "https", true, "2.30.0", "app:audiobookshelf", "Media", ""},
	{"prowlarr", "prowlarr", "go", "key", "https", true, "", "app:prowlarr", "Downloads", ""},
	{"qbittorrent", "qbittorrent", "go", "basic", "https", true, "", "app:qbittorrent", "Downloads", ""},
	{"sabnzbd", "sabnzbd", "spec", "key", "https", true, "4.5.3", "app:sabnzbd", "Downloads", ""},
	{"karakeep", "karakeep", "spec", "key", "https", true, "0.30.2", "app:karakeep", "Archive", ""},
	{"linkding", "linkding", "spec", "token", "https", true, "1.42.0", "app:linkding", "Archive", ""},
	{"paperlessngx", "paperlessngx", "spec", "token", "https", true, "2.18.4", "app:paperlessngx", "Archive", ""},
	{"immich", "immich", "go", "key", "https", true, "", "app:immich", "Apps", ""},
	{"mealie", "mealie", "spec", "key", "https", true, "", "app:mealie", "Apps", ""},
	{"nextcloud", "nextcloud", "go", "token", "https", true, "31.0.9", "app:nextcloud", "Apps", ""},
	{"speedtesttracker", "speedtesttracker", "spec", "token", "https", true, "", "app:speedtesttracker", "Services", ""},
	{"checks", "checks", "go", "none", "http", true, "", "", "Services", ""},           // monitors each check
	{"demo", "weather", "go", "none", "https", true, "", weatherID, "Home", "Weather"}, // Open-Meteo, keyless
}

func (b *builder) integrations() {
	for _, it := range integrationTable {
		id := "integration:" + it.id
		attrs := map[string]any{"type": it.typ, "tier": it.tier, "auth": it.auth, "transport": it.transport, "read_only": it.readOnly}
		if it.version != "" {
			attrs["upstream_version"] = it.version
		}
		if it.typ == "karakeep" {
			attrs["adapter"] = "≤ 0.30"
		}
		name := it.id
		if it.name != "" {
			name = it.name
		}
		b.add(&Resource{ID: id, Kind: KindIntegration, Name: name, Source: "hub", State: StateConnected,
			Labels: map[string]string{"chadash.group": it.group}, Attrs: attrs,
			Health: Health{Status: HealthOK, Since: b.start.Add(-9 * 24 * time.Hour)}})
		if it.monitors != "" {
			b.rel(id, RelMonitors, it.monitors)
		}
	}
	b.rel("integration:docker-rook", RelDependsOn, "docker:rook/dockerproxy")
	b.rel("integration:docker-magpie", RelDependsOn, "docker:magpie/socket-proxy")
}

// ── network ──────────────────────────────────────────────────────────────

// The line is 1 Gb/s down and about 110 Mb/s up; speed tests measure ~930.
const (
	lineDown = 930.0
	lineUp   = 110.0
)

func (b *builder) network() {
	b.add(&Resource{ID: "net:opnsense", Kind: KindRouter, Name: "OPNsense", Source: "integration:opnsense", State: StateRunning, Desired: StateRunning,
		Labels: map[string]string{"chadash.role": "network-core", "chadash.group": "Servers"},
		Attrs:  map[string]any{"lan": "10.20.30.0/24", "ip": ipRouter, "boot_at": b.ago("net:opnsense", 24*20, 24*45)},
		Links:  []Link{{Title: "OPNsense", URL: "https://opnsense.home.example"}},
		Caps:   []string{"net.router", "net.wan"}})
	b.add(&Resource{ID: "net:opnsense/wan", Kind: KindWAN, Name: "WAN", Source: "integration:opnsense", State: StateOnline,
		Attrs: map[string]any{"interface": "wan"}})
	b.rel("net:opnsense", RelRunsOn, "pve:wren/qemu/100")
	b.rel("net:opnsense/wan", RelPartOf, "net:opnsense")

	src := "integration:opnsense"
	b.metric(pct("net:opnsense", "cpu.pct", src, [2]float64{5, 40}, 0, 0, wave{base: 15, amp: 7, peak: 21, slow: 3, fast: 1.5}))
	b.metric(&metricSpec{res: "net:opnsense", key: "mem.mib", unit: "MiB", min: 0, max: 4096, band: [2]float64{200, 520}, dec: 0, source: src,
		fn: wave{base: 344, amp: 16, peak: 21, slow: 10, fast: 2}.fn()})
	wan := "net:opnsense/wan"
	b.metric(&metricSpec{res: wan, key: "rx.mbps", unit: "Mb/s", min: 0, max: 1000, band: [2]float64{2, 80}, dec: 1, source: src,
		fn: func(e *eval, m *metricSpec, t time.Time) float64 {
			v := 9 + 52*eveningMedia(t) + 12*bump(t, 9, 3) + 5*e.s.rng.smooth(m.hk, t, 15*time.Minute) + 2*e.s.rng.jitter(m.hk, t)
			// Short bursts: a download or a game update, a few per day,
			// some close to the line's speed.
			if u := e.s.rng.u(m.hk^0xb5, t.Unix()/600); u > 0.93 {
				v += 820 * (u - 0.93) / 0.07
			}
			return min(v, lineDown+10*e.s.rng.jitter(m.hk^1, t))
		}})
	b.metric(&metricSpec{res: wan, key: "tx.mbps", unit: "Mb/s", min: 0, max: 1000, band: [2]float64{1, 25}, dec: 1, source: src,
		fn: wave{base: 3.1, amp: 8, peak: 21.5, slow: 1.4, fast: 0.6}.fn()})
	b.metric(&metricSpec{res: wan, key: "rx.gb_30d", unit: "GB", min: 0, max: 100000, band: [2]float64{800, 1500}, dec: 0, source: src,
		fn: wave{base: 1164, slow: 3}.fn()})
	b.metric(&metricSpec{res: wan, key: "tx.gb_30d", unit: "GB", min: 0, max: 100000, band: [2]float64{300, 700}, dec: 0, source: src,
		fn: wave{base: 497, slow: 2}.fn()})
}

// ── PVE hosts and guests ─────────────────────────────────────────────────

// pveHost adds a PVE node. version is its pve-manager version, published as
// the Proxmox integration does.
func (b *builder) pveHost(id, name, src, ip, role, version string, cores int, memGB float64) *Resource {
	r := b.add(&Resource{ID: id, Kind: KindHost, Name: name, Source: src, State: StateOnline,
		Labels: map[string]string{"chadash.role": role, "chadash.group": "Servers"},
		Attrs: map[string]any{"platform": "pve", "node": name, "pve_version": version, "ip": ip, "cores": cores, "mem_gb": memGB,
			"boot_at": b.ago(id, 24*6, 24*60)},
		Links: []Link{{Title: "Proxmox", URL: pveUI(name)}},
		Caps:  []string{"host.metrics", "compute.guests", "power"}})
	b.rel("net:opnsense", RelProvidesNetworkFor, id)
	return r
}

func (b *builder) pveStorage(host, name, typ string, sizeTB float64, src string, used float64) string {
	id := host + "/storage/" + name
	b.add(&Resource{ID: id, Kind: KindStorage, Name: name, Source: src, State: StateOnline,
		Attrs: map[string]any{"type": typ, "size_tb": sizeTB}})
	b.rel(id, RelPartOf, host)
	b.metric(pct(id, "used.pct", src, [2]float64{0, 80}, 85, 95, wave{base: used, slow: 0.05}))
	return id
}

type guestSpec struct {
	host, typ string // typ: qemu or lxc
	vmid      int
	name, os  string
	ip        string
	memGB     float64
	running   bool
	storage   string
	byHand    time.Time // running, started by hand at this time rather than at boot (no onboot)
	hostpci   []string  // passed-through PCI devices
}

func (b *builder) guest(g guestSpec, src string) string {
	hostName := g.host[len("pve:"):]
	id := fmt.Sprintf("%s/%s/%d", g.host, g.typ, g.vmid)
	kind := KindVM
	if g.typ == "lxc" {
		kind = KindLXC
	}
	state, desired := StateStopped, StateStopped
	attrs := map[string]any{"vmid": g.vmid, "os": g.os, "mem_gb": g.memGB, "onboot": g.running && g.byHand.IsZero()}
	if g.ip != "" {
		attrs["ip"] = g.ip
	}
	if len(g.hostpci) > 0 {
		attrs["hostpci"] = g.hostpci
	}
	health := Health{Status: HealthOK, Since: b.ago("stopped:"+id, 24*2, 24*30)}
	if g.running {
		state, desired = StateRunning, StateRunning
		boot := b.tp.res[g.host].Attrs["boot_at"].(time.Time)
		started := boot.Add(time.Duration(2+b.rng.u(hashKey("onboot:"+id), 0)*6) * time.Minute).Truncate(time.Second)
		if !g.byHand.IsZero() {
			started, desired = g.byHand, "" // no onboot: running it by hand is fine, stopping it too
		}
		attrs["started_at"] = started
		health = Health{Status: HealthOK, Since: started}
	} else {
		health.Reason = "stopped (expected)"
	}
	b.add(&Resource{ID: id, Kind: kind, Name: g.name, Source: src, State: state, Desired: desired, Health: health, Attrs: attrs,
		Links: []Link{guestLink(hostName, g.typ, g.vmid)},
		Caps:  []string{"power", "snapshot", "backup", "console"}})
	b.rel(id, RelRunsOn, g.host)
	b.rel(id, RelStoresOn, g.storage)
	return id
}

// app adds a logical service. attrs.type is the ID of the integration that
// reads it ("app:sonarr" is a Sonarr), how widgets find a service on a real
// hub; the apps no integration reads (Cockpit, Cha Dash itself) carry their
// own name. extra attrs are merged in.
func (b *builder) app(id, name, group, url string, extra map[string]any, caps ...string) *Resource {
	attrs := map[string]any{"type": strings.TrimPrefix(id, "app:")}
	maps.Copy(attrs, extra)
	return b.add(&Resource{ID: id, Kind: KindApp, Name: name, Source: "", State: StateRunning, Desired: StateRunning,
		Labels: map[string]string{"chadash.group": group},
		Attrs:  attrs,
		Links:  []Link{{Title: name, URL: url}},
		Caps:   caps})
}

// ── wren: the network core ───────────────────────────────────────────────

func (b *builder) wren() {
	src := "integration:proxmox-wren"
	b.pveHost("pve:wren", "wren", src, "10.20.30.2", "network-core", "9.1.4", 4, 16)
	b.cpuMem("pve:wren", src, wave{base: 6, amp: 3, peak: 21, slow: 1.5, fast: 0.8}, wave{base: 61, amp: 1, peak: 21, slow: 0.8, fast: 0.2}, [2]float64{45, 80})
	st := b.pveStorage("pve:wren", "local-zfs", "zfspool", 0.5, src, 27)

	vm := b.guest(guestSpec{host: "pve:wren", typ: "qemu", vmid: 100, name: "opnsense", os: "freebsd", ip: ipRouter, memGB: 4, running: true, storage: st}, src)
	b.cpuMem(vm, src, wave{base: 11, amp: 6, peak: 21, slow: 2, fast: 1}, wave{base: 42, slow: 1, fast: 0.3}, [2]float64{30, 60})
	ag := b.guest(guestSpec{host: "pve:wren", typ: "lxc", vmid: 101, name: "adguard", os: "debian", ip: ipAdGuard, memGB: 1, running: true, storage: st}, src)
	b.cpuMem(ag, src, wave{base: 2, amp: 1.2, peak: 20, slow: 0.4, fast: 0.3}, wave{base: 34, slow: 1.2, fast: 0.3}, [2]float64{20, 55})
	ct := b.guest(guestSpec{host: "pve:wren", typ: "lxc", vmid: 102, name: "tailscale", os: "debian", ip: "10.20.30.4", memGB: 0.5, running: true, storage: st}, src)
	b.cpuMem(ct, src, wave{base: 1, amp: 0.4, peak: 21, slow: 0.3, fast: 0.2}, wave{base: 28, slow: 1.5, fast: 0.3}, [2]float64{15, 50})

	// AdGuard Home answers the home's DNS: OPNsense hands it out and
	// forwards to it, so without it nothing resolves.
	b.app("app:adguard", "AdGuard Home", "Services", "https://adguard.home.example",
		map[string]any{"version": "0.107.64", "running": true, "protection_enabled": true, "filtering_enabled": true, "filters": 5, "stats_hours": 24}, "dns.filter")
	b.rel("app:adguard", RelRunsOn, ag)
	b.rel("net:opnsense", RelDependsOn, "app:adguard")
	b.check("app:adguard", 14)
	as := "integration:adguard"
	b.count("app:adguard", "queries.24h", as, 0, 1e7, [2]float64{0, 50000}, wave{base: 41800, amp: 1600, peak: 22, slow: 420}.fn())
	b.count("app:adguard", "blocked.24h", as, 0, 1e7, [2]float64{0, 15000}, func(e *eval, m *metricSpec, t time.Time) float64 {
		return e.raw("app:adguard", "queries.24h", t) * (0.152 + 0.004*e.s.rng.smooth(m.hk, t, 2*time.Hour))
	})
	b.metric(&metricSpec{res: "app:adguard", key: "blocked.pct", unit: "%", min: 0, max: 100, band: [2]float64{5, 35}, dec: 1, source: as,
		fn: func(e *eval, m *metricSpec, t time.Time) float64 {
			q := math.Round(e.raw("app:adguard", "queries.24h", t))
			return 100 * math.Round(e.raw("app:adguard", "blocked.24h", t)) / max(q, 1)
		}})
	b.count("app:adguard", "blocklist", as, 0, 1e7, [2]float64{50000, 5e6}, constant(486212))
}

// ── aviary: a 3-node cluster ─────────────────────────────────────────────

const aviarySrc = "integration:proxmox-aviary"

func (b *builder) aviary() {
	b.add(&Resource{ID: "pve:aviary", Kind: KindCluster, Name: "aviary", Source: aviarySrc, State: StateOnline,
		Labels: map[string]string{"chadash.group": "Servers"},
		Attrs:  map[string]any{"platform": "pve", "cluster": "aviary", "nodes": 3, "quorate": true},
		Links:  []Link{{Title: "Proxmox", URL: pveUI("heron")}},
		Health: Health{Status: HealthOK, Since: b.start.Add(-120 * 24 * time.Hour)}})
}

// node adds one of aviary's nodes.
func (b *builder) node(id, name, ip, role, version string, cores int, memGB float64) *Resource {
	n := b.pveHost(id, name, aviarySrc, ip, role, version, cores, memGB)
	n.Attrs["cluster"] = "aviary"
	b.rel(id, RelPartOf, "pve:aviary")
	return n
}

// heron runs the house: Home Assistant and Frigate.
func (b *builder) heron() {
	src := aviarySrc
	b.node("pve:heron", "heron", "10.20.30.21", "home-automation", "9.0.11", 12, 32)
	b.cpuMem("pve:heron", src, wave{base: 27, amp: 8, peak: 14, slow: 3, fast: 1.5}, wave{base: 54, amp: 1.5, peak: 14, slow: 1, fast: 0.3}, [2]float64{40, 75})
	st := b.pveStorage("pve:heron", "local-lvm", "lvmthin", 1, src, 46)

	vm := b.guest(guestSpec{host: "pve:heron", typ: "qemu", vmid: 200, name: "homeassistant", os: "haos", ip: ipHA, memGB: 6, running: true, storage: st}, src)
	b.cpuMem(vm, src, wave{base: 7, amp: 2.5, peak: 19, slow: 1.2, fast: 0.6}, wave{base: 55, amp: 2, peak: 19, slow: 1, fast: 0.3}, [2]float64{40, 75})
	ct := b.guest(guestSpec{host: "pve:heron", typ: "lxc", vmid: 201, name: "frigate", os: "debian", ip: ipFrigate, memGB: 8, running: true, storage: st}, src)
	b.cpuMem(ct, src, wave{base: 44, amp: 16, peak: 14, slow: 5, fast: 2}, wave{base: 50, amp: 3, peak: 14, slow: 1.5, fast: 0.4}, [2]float64{35, 70})

	b.app("app:homeassistant", "Home Assistant", "Home", "https://ha.home.example",
		map[string]any{
			"people_total": 4, "lights_total": 52, "switches_total": 360,
			"temperature_unit": "°C",
			"climate_items": []map[string]any{
				{"name": "Living room", "current": 21.4, "target": 21.5, "action": "idle", "humidity": 47.0},
				{"name": "Bedroom", "current": 19.2, "target": 18.0, "action": "idle"},
			},
			"safety_total": 6, "safety_items": []map[string]any{},
			"media_total": 4,
		}, "home.entities")
	b.rel("app:homeassistant", RelRunsOn, vm)
	b.rel("app:homeassistant", RelDependsOn, "app:frigate")
	// Cha Dash reads Frigate on 55000, the internal port published as is: it
	// answers anyone as an admin (the hint in hintFrigate).
	b.app("app:frigate", "Frigate", "Home", "http://"+ipFrigate+":55000",
		map[string]any{"version": "0.16.1", "cameras": len(cameraTable), "detector": "openvino"}, "cameras", "cameras.events")
	b.rel("app:frigate", RelRunsOn, ct)
	b.check("app:homeassistant", 35)
	b.check("app:frigate", 22)

	hs := "integration:homeassistant"
	b.metric(&metricSpec{res: "app:homeassistant", key: "power.kw", unit: "kW", min: 0, max: 15, band: [2]float64{0.7, 2.6}, dec: 2, source: hs, fn: housePower})
	b.metric(&metricSpec{res: "app:homeassistant", key: "energy.kwh", unit: "kWh", min: 0, max: 200, band: [2]float64{0, 45}, dec: 1, source: hs,
		fn: func(e *eval, m *metricSpec, t time.Time) float64 {
			// Energy today: power integrated from local midnight in 5-minute steps.
			sum := 0.0
			for at := dayStart(t); at.Before(t); at = at.Add(5 * time.Minute) {
				step := min(5*time.Minute, t.Sub(at))
				sum += housePower(e, m, at) * hours(step)
			}
			return sum
		}})
	b.count("app:homeassistant", "people_home", hs, 0, 4, [2]float64{0, 4}, func(e *eval, m *metricSpec, t time.Time) float64 {
		return float64(peopleHome(t))
	})
	b.count("app:homeassistant", "lights_on", hs, 0, 52, [2]float64{0, 16}, func(e *eval, m *metricSpec, t time.Time) float64 {
		h := hourOf(t)
		base := 2.0
		switch {
		case h >= 6.5 && h < 8.5:
			base = 6
		case h >= 18.5 && h < 23.25:
			base = 12
		case h >= 23.25:
			base = 4
		}
		return base + 1.5*e.s.rng.smooth(m.hk, t, 30*time.Minute)
	})
	b.count("app:homeassistant", "safety_tripped", hs, 0, 6, [2]float64{0, 0}, constant(0))
	b.count("app:homeassistant", "media_playing", hs, 0, 4, [2]float64{0, 4}, func(e *eval, m *metricSpec, t time.Time) float64 {
		if h := hourOf(t); h >= 18.5 && h < 23 {
			return 1
		}
		return 0
	})
	b.count("app:homeassistant", "switches_on", hs, 0, 360, [2]float64{140, 190}, wave{base: 166, amp: 5, peak: 20, slow: 1.8}.fn())

	b.metric(&metricSpec{res: "app:frigate", key: "inference.ms", unit: "ms", min: 1, max: 500, band: [2]float64{5, 20}, dec: 1, source: "integration:frigate",
		fn: wave{base: 10.2, amp: 1.4, peak: 14, slow: 0.6, fast: 0.3}.fn()})

	for i, c := range cameraTable {
		id := "frigate:camera/" + c.id
		b.add(&Resource{ID: id, Kind: KindCamera, Name: c.name, Source: "integration:frigate", State: StateOnline,
			Labels: map[string]string{"chadash.group": "Home"},
			Attrs:  map[string]any{"ip": fmt.Sprintf("10.20.30.%d", 61+i), "fps": 5, "detect": true, "record": true},
			Health: Health{Status: HealthOK, Since: b.ago(id, 24*10, 24*40)}})
		b.rel("app:frigate", RelExposes, id)
		b.rel("net:opnsense", RelProvidesNetworkFor, id)
		b.count(id, "detections.24h", "integration:frigate", 0, 500, [2]float64{0, 40}, func(e *eval, m *metricSpec, t time.Time) float64 {
			return float64(e.s.detectionsIn(c, t.Add(-24*time.Hour), t))
		})
	}
}

// peopleHome is how many of the four are home. On the default day, a
// Saturday, the kids are at football 09:30–12:00, and one of the adults is
// out for groceries 10:30–12:45.
func peopleHome(t time.Time) int {
	h, n := hourOf(t), 4
	if h >= 9.5 && h < 12 {
		n -= 2
	}
	if h >= 10.5 && h < 12.75 {
		n--
	}
	return n
}

// housePower is the house's total power draw: a base load, morning and
// evening bumps, and noise.
func housePower(e *eval, m *metricSpec, t time.Time) float64 {
	return 0.95 + 0.7*bump(t, 7.5, 1.5) + 1.35*bump(t, 19.75, 3) + 0.14*e.s.rng.smooth(m.hk, t, 20*time.Minute)
}

// egret runs rook, the LXC that is the second Docker host.
func (b *builder) egret() {
	src := aviarySrc
	b.node("pve:egret", "egret", "10.20.30.22", "docker", "9.1.4", 8, 32)
	b.cpuMem("pve:egret", src, wave{base: 7, amp: 2, peak: 21, slow: 1.2, fast: 0.5}, wave{base: 46, amp: 1, peak: 21, slow: 0.6, fast: 0.2}, [2]float64{30, 65})
	st := b.pveStorage("pve:egret", "local-lvm", "lvmthin", 1, src, 38)

	ct := b.guest(guestSpec{host: "pve:egret", typ: "lxc", vmid: 210, name: "rook", os: "debian", ip: "10.20.30.30", memGB: 12, running: true, storage: st}, src)
	b.cpuMem(ct, src, wave{base: 9, amp: 3, peak: 21, slow: 1.5, fast: 0.7}, wave{base: 43, amp: 1.5, peak: 21, slow: 0.8, fast: 0.2}, [2]float64{25, 65})
}

// stork is the small file server: 8 GB and no swap.
func (b *builder) stork() {
	src := aviarySrc
	h := b.node("pve:stork", "stork", "10.20.30.23", "nas", "9.1.4", 4, 8)
	h.Attrs["swap_gb"] = 0
	b.cpuMem("pve:stork", src, wave{base: 4, amp: 1.5, peak: 20, slow: 1, fast: 0.5}, wave{base: 62, amp: 1, peak: 20, slow: 0.8, fast: 0.2}, [2]float64{45, 85})
	st := b.pveStorage("pve:stork", "tank", "zfspool", 8, src, 54)
	ct := b.guest(guestSpec{host: "pve:stork", typ: "lxc", vmid: 220, name: "files", os: "debian", ip: "10.20.30.50", memGB: 4, running: true, storage: st}, src)
	b.cpuMem(ct, src, wave{base: 3, amp: 1, peak: 20, slow: 0.8, fast: 0.4}, wave{base: 12.5, amp: 0.5, peak: 20, slow: 0.4, fast: 0.1}, [2]float64{5, 40})
	b.guest(guestSpec{host: "pve:stork", typ: "qemu", vmid: 221, name: "sandbox", os: "debian", memGB: 2, storage: st}, src)
	// files serves the Samba shares; Cockpit is how they're managed.
	b.app("app:cockpit", "Cockpit", "Servers", "https://files.home.example:9090/", nil)
	b.rel("app:cockpit", RelRunsOn, ct)
	b.check("app:cockpit", 23)
}

// ── owl: the workstation ─────────────────────────────────────────────────

func (b *builder) owl() {
	src := "integration:proxmox-owl"
	b.pveHost("pve:owl", "owl", src, "10.20.30.25", "workstation", "9.2.3", 16, 64)
	b.cpuMem("pve:owl", src, wave{base: 2, amp: 1, peak: 20, slow: 0.6, fast: 0.3}, wave{base: 38, slow: 0.6, fast: 0.2}, [2]float64{20, 60})
	st := b.pveStorage("pve:owl", "local-lvm", "lvmthin", 2, src, 64)
	// win-games and studio pass through the same GPU. studio is started by
	// hand when it's wanted: since yesterday evening.
	gpu := []string{strings.TrimSuffix(ptDevice, ".0")}
	b.guest(guestSpec{host: "pve:owl", typ: "qemu", vmid: 301, name: "win-games", os: "windows", memGB: 16, storage: st, hostpci: gpu}, src)
	vm := b.guest(guestSpec{host: "pve:owl", typ: "qemu", vmid: 302, name: "studio", os: "ubuntu", ip: "10.20.30.70", memGB: 16, running: true, storage: st,
		byHand: b.start.Add(-2*time.Hour - 45*time.Minute), hostpci: gpu}, src)
	b.cpuMem(vm, src, wave{base: 3, amp: 1.5, peak: 20, slow: 0.8, fast: 0.4}, wave{base: 31, slow: 1, fast: 0.3}, [2]float64{20, 60})
	b.guest(guestSpec{host: "pve:owl", typ: "qemu", vmid: 303, name: "homework", os: "ubuntu", memGB: 8, storage: st}, src)
}

// ── magpie (Unraid) ──────────────────────────────────────────────────────

type diskSpec struct {
	name   string
	sizeTB float64
	used   float64 // 0 for parity
	temp   float64
}

// arrayDisks: 84 TB usable, about 71% used.
var arrayDisks = []diskSpec{
	{"parity", 20, 0, 37},
	{"disk1", 18, 82, 40},
	{"disk2", 18, 79, 39},
	{"disk3", 14, 74, 38},
	{"disk4", 14, 68, 36},
	{"disk5", 10, 61, 35},
	{"disk6", 10, 45, 35},
}

const unraidSrc = "integration:unraid-magpie"

func (b *builder) magpie() {
	src := unraidSrc
	b.add(&Resource{ID: "unraid:magpie", Kind: KindHost, Name: "magpie", Source: src, State: StateOnline,
		Labels: map[string]string{"chadash.role": "nas", "chadash.group": "Servers"},
		Attrs: map[string]any{"platform": "unraid", "version": "7.2.4", "hostname": "magpie", "ip": ipMagpie, "cores": 12, "mem_gb": 64,
			"boot_at": b.ago("unraid:magpie", 24*12, 24*50)},
		Links: []Link{{Title: "Unraid", URL: "https://magpie.home.example/"}},
		Caps:  []string{"host.metrics", "storage.pools", "storage.disks", "storage.shares", "containers", "power"}})
	b.rel("net:opnsense", RelProvidesNetworkFor, "unraid:magpie")
	b.cpuMem("unraid:magpie", src,
		wave{base: 16, amp: 9, peak: 21, slow: 2.5, fast: 1.2, extra: func(t time.Time) float64 { return 9 * nightlyBackups(t) }},
		wave{base: 46, amp: 2.5, peak: 21, slow: 0.8, fast: 0.2}, [2]float64{30, 65})

	// Array and disks.
	usable := 0.0
	for _, d := range arrayDisks[1:] {
		usable += d.sizeTB
	}
	b.add(&Resource{ID: "unraid:magpie/array", Kind: KindArray, Name: "array", Source: src, State: StateOnline,
		Attrs:  map[string]any{"size_tb": usable, "parity": "valid", "parity_checked_at": b.start.Add(-6*24*time.Hour - 7*time.Hour)},
		Health: Health{Status: HealthOK, Since: b.start.Add(-6*24*time.Hour - 7*time.Hour)}})
	b.rel("unraid:magpie/array", RelPartOf, "unraid:magpie")
	for _, d := range arrayDisks {
		id := "unraid:magpie/disk/" + d.name
		role := "data"
		if d.used == 0 {
			role = "parity"
		}
		b.add(&Resource{ID: id, Kind: KindDisk, Name: d.name, Source: src, State: StateOnline,
			Attrs: map[string]any{"size_tb": d.sizeTB, "role": role, "fs": "xfs"}})
		b.rel(id, RelPartOf, "unraid:magpie/array")
		if d.used > 0 {
			b.metric(pct(id, "used.pct", src, [2]float64{0, 85}, 90, 97, wave{base: d.used, slow: 0.02}))
		}
		// Unknown while the disk is spun down (routines.go, diskSpin): Unraid
		// reports no temperature then, and reading one would wake the disk.
		b.metric(&metricSpec{res: id, key: "temp.c", unit: "°C", min: 15, max: 70, band: [2]float64{30, 45}, warn: 50, crit: 55, dec: 0, source: src, awake: true,
			fn: wave{base: d.temp, amp: 1.5, peak: 16, slow: 0.6, extra: func(t time.Time) float64 { return 1.5 * moverRuns(t) }}.fn()})
	}
	b.metric(pct("unraid:magpie/array", "used.pct", src, [2]float64{0, 85}, 85, 95, wave{}))
	b.tp.specIdx["unraid:magpie/array|used.pct"].fn = func(e *eval, m *metricSpec, t time.Time) float64 {
		sum := 0.0
		for _, d := range arrayDisks[1:] {
			sum += e.raw("unraid:magpie/disk/"+d.name, "used.pct", t) * d.sizeTB
		}
		return sum / usable
	}

	// Pools and shares.
	b.add(&Resource{ID: "unraid:magpie/pool/cache", Kind: KindPool, Name: "cache", Source: src, State: StateOnline,
		Attrs: map[string]any{"size_tb": 2, "fs": "btrfs", "devices": 2, "media": "nvme"}})
	b.add(&Resource{ID: "unraid:magpie/pool/backups", Kind: KindPool, Name: "backups", Source: src, State: StateOnline,
		Attrs: map[string]any{"size_tb": 2, "fs": "xfs", "devices": 1, "media": "hdd"}})
	b.rel("unraid:magpie/pool/cache", RelPartOf, "unraid:magpie")
	b.rel("unraid:magpie/pool/backups", RelPartOf, "unraid:magpie")
	b.metric(pct("unraid:magpie/pool/cache", "used.pct", src, [2]float64{0, 80}, 85, 95, wave{base: 48, slow: 0.1}))
	b.metric(&metricSpec{res: "unraid:magpie/pool/cache", key: "temp.c", unit: "°C", min: 15, max: 90, band: [2]float64{30, 55}, warn: 70, crit: 80, dec: 0, source: src,
		fn: wave{base: 41, amp: 3, peak: 21, slow: 1}.fn()})
	b.metric(pct("unraid:magpie/pool/backups", "used.pct", src, [2]float64{0, 88}, 90, 96, wave{}))
	b.tp.specIdx["unraid:magpie/pool/backups|used.pct"].fn = func(e *eval, m *metricSpec, t time.Time) float64 {
		return e.raw("pbs:main", "used.pct", t) + 0.4
	}
	b.metric(&metricSpec{res: "unraid:magpie/pool/backups", key: "temp.c", unit: "°C", min: 15, max: 70, band: [2]float64{30, 45}, warn: 50, crit: 55, dec: 0, source: src,
		fn: wave{base: 36, amp: 1, peak: 16, slow: 0.5, extra: func(t time.Time) float64 { return 4 * nightlyBackups(t) }}.fn()})
	for _, sh := range []struct{ name, on string }{
		{"media", "unraid:magpie/array"}, {"photos", "unraid:magpie/array"}, {"books", "unraid:magpie/array"}, {"appdata", "unraid:magpie/pool/cache"},
	} {
		id := "unraid:magpie/share/" + sh.name
		b.add(&Resource{ID: id, Kind: KindShare, Name: sh.name, Source: src, State: StateOnline})
		b.rel(id, RelPartOf, sh.on)
	}

	// Docker engine, reached through a socket-proxy container on :2375.
	eng := "docker:magpie"
	boot := b.tp.res["unraid:magpie"].Attrs["boot_at"].(time.Time)
	engStart := boot.Add(4 * time.Minute)
	// The proxy is a coarse one (spike 4.4): every GET under /containers/
	// gets through, so the Docker integration's probe says broad-proxy and
	// raises a hint (hintDocker).
	b.add(&Resource{ID: eng, Kind: KindEngine, Name: "magpie", Source: "integration:docker-magpie", State: StateRunning,
		Attrs: map[string]any{"endpoint": magpieProxy, "via": "socket-proxy", "access": "broad-proxy", "host": ipMagpie, "ip": ipMagpie,
			"manager": "unraid", "started_at": engStart},
		Caps: []string{"containers"}})
	b.rel(eng, RelRunsOn, "unraid:magpie")
	b.rel(eng, RelDependsOn, "unraid:magpie/array")
	b.rel(eng, RelStoresOn, "unraid:magpie/share/appdata") // container appdata; docker.img sits beside it on the cache pool

	// Homepage builds Mealie, Immich and PBS from these containers' labels;
	// the Docker integration copies the homepage.* labels that aren't
	// secrets, so label discovery offers them as tiles.
	homepage := map[string]map[string]string{
		"mealie": {"homepage.group": "Apps", "homepage.name": "Mealie", "homepage.href": "https://food.home.example", "homepage.icon": "mealie.png",
			"homepage.description": "Recipes", "homepage.widget.type": "mealie", "homepage.widget.url": "http://" + ipMagpie + ":9925"},
		"immich": {"homepage.group": "Apps", "homepage.name": "Immich", "homepage.href": "https://photos.home.example", "homepage.icon": "immich.png",
			"homepage.description": "Photos", "homepage.widget.type": "immich", "homepage.widget.url": "http://" + ipMagpie + ":2283", "homepage.widget.version": "2"},
		"pbs": {"homepage.group": "Backup", "homepage.name": "Proxmox Backup Server", "homepage.href": "https://" + ipMagpie + ":8007",
			"homepage.icon": "proxmox-backup-server.png", "homepage.widget.type": "proxmoxbackupserver", "homepage.widget.url": "https://" + ipMagpie + ":8007"},
	}
	b.containers(eng, "integration:docker-magpie", engStart.Add(time.Minute), 24, 24*21, 16384, []ct{
		{"plex", "plexinc/pms-docker:latest", "", "app:plex",
			wave{base: 2.5, slow: 1, fast: 0.5, extra: func(t time.Time) float64 { return 12 * eveningMedia(t) }},
			wave{base: 900, slow: 30, fast: 5, extra: func(t time.Time) float64 { return 320 * eveningMedia(t) }}, 0, []string{"32400/tcp"}},
		{"jellyfin", "jellyfin/jellyfin:10.10.7", "", "app:jellyfin",
			wave{base: 1.2, slow: 0.6, fast: 0.3, extra: func(t time.Time) float64 { return 7 * eveningMedia(t) }},
			wave{base: 640, slow: 25, fast: 4, extra: func(t time.Time) float64 { return 220 * eveningMedia(t) }}, 0, []string{"8096/tcp"}},
		{"tautulli", "lscr.io/linuxserver/tautulli:latest", "", "app:tautulli", wave{base: 0.3, slow: 0.1, fast: 0.1}, wave{base: 130, slow: 5, fast: 1}, 0, []string{"8181/tcp"}},
		{"sonarr", "lscr.io/linuxserver/sonarr:latest", "", "app:sonarr", wave{base: 0.9, slow: 0.3, fast: 0.2}, wave{base: 360, slow: 14, fast: 2}, 0, []string{"8989/tcp"}},
		{"radarr", "lscr.io/linuxserver/radarr:latest", "", "app:radarr", wave{base: 0.8, slow: 0.3, fast: 0.2}, wave{base: 310, slow: 12, fast: 2}, 0, []string{"7878/tcp"}},
		{"bazarr", "lscr.io/linuxserver/bazarr:latest", "", "app:bazarr", wave{base: 0.5, slow: 0.2, fast: 0.1}, wave{base: 240, slow: 10, fast: 2}, 0, []string{"6767/tcp"}},
		{"prowlarr", "lscr.io/linuxserver/prowlarr:latest", "", "app:prowlarr", wave{base: 0.4, slow: 0.2, fast: 0.1}, wave{base: 210, slow: 8, fast: 2}, 0, []string{"9696/tcp"}},
		{"qbittorrent", "lscr.io/linuxserver/qbittorrent:latest", "", "app:qbittorrent",
			wave{base: 5, amp: 3, peak: 1, slow: 1.5, fast: 0.8}, wave{base: 1100, amp: 60, peak: 1, slow: 35, fast: 6}, 4096, []string{"8080/tcp", "6881/tcp"}},
		{"sabnzbd", "lscr.io/linuxserver/sabnzbd:latest", "", "app:sabnzbd",
			wave{base: 0.6, slow: 0.3, fast: 0.2, extra: func(t time.Time) float64 { return 9 * bump(t, 3, 2) }}, wave{base: 190, slow: 8, fast: 2}, 0, []string{"8080/tcp"}},
		{"mealie", "ghcr.io/mealie-recipes/mealie:latest", "", "app:mealie", wave{base: 0.2, slow: 0.1, fast: 0.05}, wave{base: 390, slow: 10, fast: 2}, 0, []string{"9925/tcp"}},
		{"immich", "ghcr.io/imagegenius/immich:latest", "", "app:immich",
			wave{base: 3, amp: 4, peak: 20, slow: 2.5, fast: 0.8}, wave{base: 2300, amp: 300, peak: 20, slow: 80, fast: 10}, 0, []string{"8080/tcp"}},
		{"audiobookshelf", "ghcr.io/advplyr/audiobookshelf:2", "", "app:audiobookshelf", wave{base: 0.3, slow: 0.1, fast: 0.1}, wave{base: 160, slow: 6, fast: 1}, 0, []string{"80/tcp"}},
		{"pbs", "ayufan/proxmox-backup-server:latest", "", "app:pbs",
			wave{base: 0.6, slow: 0.2, fast: 0.1, extra: func(t time.Time) float64 { return 26 * nightlyBackups(t) }},
			wave{base: 620, slow: 15, fast: 3, extra: func(t time.Time) float64 { return 260 * nightlyBackups(t) }}, 0, []string{"8007/tcp"}},
		{"socket-proxy", "lscr.io/linuxserver/socket-proxy:latest", "", "", wave{base: 0.1, fast: 0.05}, wave{base: 9, fast: 0.5}, 0, []string{"2375/tcp"}},
	}, func(c ct, r *Resource) {
		r.Labels = map[string]string{"net.unraid.docker.managed": "dockerman"}
		if hp := homepage[c.name]; hp != nil {
			maps.Copy(r.Labels, hp)
			r.Links = []Link{{Title: hp["homepage.name"], URL: hp["homepage.href"]}}
		}
	})
	b.tp.res[eng+"/qbittorrent"].Attrs["vpn"] = "wireguard"

	// Apps on magpie.
	b.app("app:plex", "Plex", "Media", "https://plex.home.example", map[string]any{"movies": 1876, "shows": 264}, "media.sessions", "media.library")
	// Jellyfin serves the kids' tablets: its own, smaller libraries on the same share.
	b.app("app:jellyfin", "Jellyfin", "Media", "https://jellyfin.home.example", map[string]any{"version": "10.10.7", "movies": 342, "shows": 57},
		"media.sessions", "media.library")
	b.app("app:tautulli", "Tautulli", "Media", "https://plex.home.example/tautulli", nil, "media.sessions")
	b.app("app:sonarr", "Sonarr", "Media", "https://arr.home.example/sonarr", map[string]any{"calendar": []CalendarItem{
		{At: b.start.Add(28 * time.Hour), Title: "Starling Point", Detail: "new episode"},
		{At: b.start.Add(34 * time.Hour), Title: "The Copper Kettle", Detail: "new episode"},
		{At: b.start.Add(3*24*time.Hour + 20*time.Hour), Title: "5 more episodes", Detail: "this week"},
	}}, "media.calendar", "media.library")
	b.app("app:radarr", "Radarr", "Media", "https://arr.home.example/radarr", map[string]any{"calendar": []CalendarItem{
		{At: b.start.Add(6*24*time.Hour + 8*time.Hour), Title: "2 movie releases", Detail: "digital release"},
	}}, "media.calendar", "media.library")
	b.app("app:bazarr", "Bazarr", "Media", "https://arr.home.example/bazarr", nil, "media.library")
	b.app("app:audiobookshelf", "Audiobookshelf", "Media", "https://books.home.example", map[string]any{"version": "2.30.0", "libraries": 2},
		"media.library", "media.sessions")
	b.app("app:prowlarr", "Prowlarr", "Downloads", "https://arr.home.example/prowlarr", map[string]any{"indexers": 4}, "downloads.indexers")
	b.app("app:qbittorrent", "qBittorrent", "Downloads", "https://torrents.home.example", nil, "downloads.queue")
	b.app("app:sabnzbd", "SABnzbd", "Downloads", "https://arr.home.example/sabnzbd", map[string]any{"version": "4.5.3", "paused": false}, "downloads.queue")
	b.app("app:mealie", "Mealie", "Apps", "https://food.home.example", map[string]any{"recipes": 143, "categories": 11, "tags": 186})
	b.app("app:immich", "Immich", "Apps", "https://photos.home.example", map[string]any{"users": 4}, "photos.library")
	b.app("app:pbs", "Proxmox Backup Server", "Backup", "https://"+ipMagpie+":8007", map[string]any{"version": "4.0.18", "datastores": 1},
		"backups.jobs", "backups.snapshots", "backups.targets")

	for _, d := range []struct{ from, to string }{
		{"app:sonarr", "app:qbittorrent"}, {"app:sonarr", "app:sabnzbd"}, {"app:sonarr", "app:prowlarr"},
		{"app:radarr", "app:qbittorrent"}, {"app:radarr", "app:sabnzbd"}, {"app:radarr", "app:prowlarr"},
		{"app:bazarr", "app:sonarr"}, {"app:bazarr", "app:radarr"},
		{"app:tautulli", "app:plex"},
	} {
		b.rel(d.from, RelDependsOn, d.to)
	}
	for _, s := range []struct{ app, share string }{
		{"app:plex", "media"}, {"app:jellyfin", "media"}, {"app:sonarr", "media"}, {"app:radarr", "media"}, {"app:bazarr", "media"},
		{"app:qbittorrent", "media"}, {"app:sabnzbd", "media"}, {"app:immich", "photos"}, {"app:audiobookshelf", "books"},
	} {
		b.rel(s.app, RelStoresOn, "unraid:magpie/share/"+s.share)
	}

	for app, ms := range map[string]float64{"app:plex": 45, "app:jellyfin": 38, "app:tautulli": 60, "app:sonarr": 55, "app:radarr": 52, "app:bazarr": 70,
		"app:prowlarr": 48, "app:qbittorrent": 40, "app:sabnzbd": 44, "app:mealie": 65, "app:immich": 80, "app:audiobookshelf": 42, "app:pbs": 90} {
		b.check(app, ms)
	}

	// App statistics, read through each app's integration.
	b.count("app:plex", "streams", "integration:plex", 0, 10, [2]float64{0, 3}, func(e *eval, m *metricSpec, t time.Time) float64 {
		return 2.2*eveningMedia(t) + 0.6*e.s.rng.smooth(m.hk, t, 25*time.Minute) - 0.3
	})
	// The kids' shows are on Jellyfin, mostly late afternoon.
	b.count("app:jellyfin", "streams", "integration:jellyfin", 0, 10, [2]float64{0, 3}, func(e *eval, m *metricSpec, t time.Time) float64 {
		return 1.6*bump(t, 17.5, 1.75) + 0.8*eveningMedia(t) + 0.5*e.s.rng.smooth(m.hk, t, 25*time.Minute) - 0.3
	})
	b.count("app:jellyfin", "streams.transcode", "integration:jellyfin", 0, 10, [2]float64{0, 2}, func(e *eval, m *metricSpec, t time.Time) float64 {
		return math.Floor(clamp(math.Round(e.raw("app:jellyfin", "streams", t)), 0, 10) / 2) // the tablet's streams
	})
	b.count("app:sonarr", "queue", "integration:sonarr", 0, 200, [2]float64{0, 15}, wave{base: 9, amp: 4, peak: 14, slow: 0.8}.fn())
	b.count("app:sonarr", "wanted", "integration:sonarr", 0, 5000, [2]float64{0, 40}, wave{base: 31, slow: 0.7}.fn())
	b.count("app:radarr", "wanted", "integration:radarr", 0, 5000, [2]float64{0, 20}, wave{base: 12, slow: 0.6}.fn())
	b.count("app:radarr", "queue", "integration:radarr", 0, 200, [2]float64{0, 5}, wave{base: 0.2, slow: 0.6}.fn()) // Sonarr's key, as the integration has it
	b.count("app:bazarr", "missing.episodes", "integration:bazarr", 0, 100000, [2]float64{0, 1000}, func(e *eval, m *metricSpec, t time.Time) float64 {
		return 612.4 - 0.238*hours(t.Sub(b.start)) + 0.4*e.s.rng.smooth(m.hk, t, 2*time.Hour) // down ~40 a week
	})
	b.count("app:bazarr", "missing.movies", "integration:bazarr", 0, 10000, [2]float64{0, 20}, wave{base: 8, slow: 0.3}.fn())
	// wanted is the sum the Bazarr spec publishes for the *arr tile.
	b.count("app:bazarr", "wanted", "integration:bazarr", 0, 100000, [2]float64{0, 1000}, func(e *eval, m *metricSpec, t time.Time) float64 {
		return math.Round(e.raw("app:bazarr", "missing.episodes", t)) + math.Round(e.raw("app:bazarr", "missing.movies", t))
	})
	b.count("app:prowlarr", "grabs", "integration:prowlarr", 0, 1e7, [2]float64{0, 2000}, b.growing(1430, 1.3))
	b.count("app:prowlarr", "queries.24h", "integration:prowlarr", 0, 1e5, [2]float64{20, 90}, wave{base: 54, amp: 6, peak: 2, slow: 2}.fn())
	b.count("app:prowlarr", "failures.24h", "integration:prowlarr", 0, 1e4, [2]float64{0, 2}, wave{base: 0.1, slow: 0.3}.fn())
	b.metric(&metricSpec{res: "app:qbittorrent", key: "dl.mbps", unit: "Mb/s", min: 0, max: 1000, band: [2]float64{0, 60}, dec: 1, source: "integration:qbittorrent",
		fn: func(e *eval, m *metricSpec, t time.Time) float64 {
			return 22*bump(t, 1.5, 4.5) + 6*e.s.rng.smooth(m.hk, t, 15*time.Minute) - 2
		}})
	b.metric(&metricSpec{res: "app:qbittorrent", key: "ul.mbps", unit: "Mb/s", min: 0, max: 1000, band: [2]float64{0, 20}, dec: 1, source: "integration:qbittorrent",
		fn: wave{base: 3, amp: 4, peak: 22, slow: 1.5, fast: 0.4}.fn()})
	b.count("app:qbittorrent", "leeching", "integration:qbittorrent", 0, 1000, [2]float64{0, 30}, wave{base: 20, slow: 1.2}.fn())
	b.count("app:qbittorrent", "seeding", "integration:qbittorrent", 0, 10000, [2]float64{50, 100}, wave{base: 74, slow: 0.8}.fn())
	b.count("app:qbittorrent", "queued", "integration:qbittorrent", 0, 1000, [2]float64{0, 10}, wave{base: 3, slow: 1}.fn())
	// SABnzbd works through its queue at night, and trickles through the
	// afternoon's grabs.
	sab := "integration:sabnzbd"
	b.metric(&metricSpec{res: "app:sabnzbd", key: "dl.mbps", unit: "Mb/s", min: 0, max: 1000, band: [2]float64{0, 60}, dec: 1, source: sab,
		fn: func(e *eval, m *metricSpec, t time.Time) float64 {
			return 140*bump(t, 3, 2) + 26*bump(t, 14.5, 2.5) + 3*e.s.rng.smooth(m.hk, t, 15*time.Minute) - 1
		}})
	b.count("app:sabnzbd", "queue", sab, 0, 1000, [2]float64{0, 15}, wave{base: 2.5, amp: 3, peak: 1, slow: 1}.fn())
	b.metric(&metricSpec{res: "app:sabnzbd", key: "left.gib", unit: "GiB", min: 0, max: 10000, band: [2]float64{0, 50}, dec: 1, source: sab,
		fn: wave{base: 6, amp: 14, peak: 1, slow: 2}.fn()})
	b.count("app:immich", "photos", "integration:immich", 0, 1e7, [2]float64{0, 1e6}, b.growing(61208, 4.6))
	b.count("app:immich", "videos", "integration:immich", 0, 1e6, [2]float64{0, 1e5}, b.growing(3115, 0.3))
	b.metric(&metricSpec{res: "app:immich", key: "storage.gib", unit: "GiB", min: 0, max: 1e6, band: [2]float64{0, 1000}, dec: 1, source: "integration:immich",
		fn: func(e *eval, m *metricSpec, t time.Time) float64 { return 384.6 + 0.03*hours(t.Sub(b.start)) }})
	// Audiobookshelf: bedtime stories, and a podcast on the way home.
	abs := "integration:audiobookshelf"
	b.count("app:audiobookshelf", "streams", abs, 0, 10, [2]float64{0, 3}, func(e *eval, m *metricSpec, t time.Time) float64 {
		return 1.7*bump(t, 19.75, 0.75) + 0.6*bump(t, 17.25, 0.75) + 0.3*e.s.rng.smooth(m.hk, t, 25*time.Minute) - 0.2
	})
	b.count("app:audiobookshelf", "books", abs, 0, 100000, [2]float64{0, 100000}, constant(412))
	b.count("app:audiobookshelf", "podcasts", abs, 0, 10000, [2]float64{0, 10000}, constant(23))
	b.metric(&metricSpec{res: "app:audiobookshelf", key: "books.hours", unit: "h", min: 0, max: 1e6, band: [2]float64{0, 1e6}, dec: 1, source: abs, fn: constant(4127.5)})
	b.metric(&metricSpec{res: "app:audiobookshelf", key: "podcasts.hours", unit: "h", min: 0, max: 1e6, band: [2]float64{0, 1e6}, dec: 1, source: abs, fn: constant(611.3)})
	b.count("app:pbs", "tasks.failed_24h", "integration:pbs", 0, 1000, [2]float64{0, 0}, func(e *eval, m *metricSpec, t time.Time) float64 {
		return float64(e.s.failedBackupsIn(t.Add(-24*time.Hour), t))
	})
}

// CalendarItem is an upcoming release on a media calendar.
type CalendarItem struct {
	At     time.Time `json:"at"`
	Title  string    `json:"title"`
	Detail string    `json:"detail,omitempty"`
}

// ct is a container at rest. project is its compose project ("" on Unraid).
// cpu and mem shape its stats, which only the containers in containerStats
// publish.
type ct struct {
	name, image, project, app string
	cpu, mem                  wave
	memLimit                  int
	ports                     []string
}

// containerStats are the containers with cpu.pct and mem.mib series: the
// ones a story reads (qBittorrent's OOM kill, PBS's nightly load and its
// prune). The Docker integration reads container stats only while a sheet
// watches a container (integrations/docker), so the rest have none, as on a
// real hub with no sheet open.
var containerStats = map[string]bool{"docker:magpie/qbittorrent": true, "docker:magpie/pbs": true}

// containers adds an engine's containers, started between minH and maxH
// hours before the start (never before since). each finishes a container's
// resource: labels, links, relations.
func (b *builder) containers(eng, src string, since time.Time, minH, maxH, maxMem float64, cts []ct, each func(ct, *Resource)) {
	for _, c := range cts {
		id := eng + "/" + c.name
		started := maxTime(since, b.ago(id, minH, maxH))
		attrs := map[string]any{"image": c.image, "container_id": containerID(b.rng, id, 0), "started_at": started, "restart_count": 0}
		if c.ports != nil {
			attrs["ports"] = c.ports
		}
		limit := maxMem
		if c.memLimit > 0 {
			attrs["mem_limit_mib"] = c.memLimit
			limit = 6144 // room for the raised limit
		}
		caps := []string{"lifecycle", "logs"}
		if containerStats[id] {
			caps = append(caps, "metrics")
		}
		r := b.add(&Resource{ID: id, Kind: KindContainer, Name: c.name, Source: src, State: StateRunning, Desired: StateRunning,
			Attrs: attrs, Caps: caps})
		each(c, r)
		b.rel(id, RelRunsOn, eng)
		if c.app != "" {
			b.rel(c.app, RelRunsOn, id)
		}
		if !containerStats[id] {
			continue
		}
		b.metric(pct(id, "cpu.pct", src, [2]float64{0, max(5, c.cpu.base*3)}, 0, 0, c.cpu))
		b.metric(&metricSpec{res: id, key: "mem.mib", unit: "MiB", min: 0, max: limit, band: [2]float64{c.mem.base * 0.7, c.mem.base * 1.4}, dec: 0, source: src, fn: c.mem.fn()})
	}
}

// ── rook (Docker in an LXC on egret) ─────────────────────────────────────

func (b *builder) rook() {
	eng := "docker:rook"
	lxc := "pve:egret/lxc/210"
	engStart := b.tp.res[lxc].Attrs["started_at"].(time.Time).Add(40 * time.Second)
	b.add(&Resource{ID: eng, Kind: KindEngine, Name: "rook", Source: "integration:docker-rook", State: StateRunning,
		Attrs: map[string]any{"endpoint": "tcp://dockerproxy:2375", "via": "socket-proxy", "access": "filtered-proxy", "host": "dockerproxy",
			"manager": "compose", "started_at": engStart},
		Caps: []string{"containers", "stacks"}})
	b.rel(eng, RelRunsOn, lxc)

	b.containers(eng, "integration:docker-rook", engStart.Add(30*time.Second), 24*2, 24*30, 8192, []ct{
		{"chadash", "ghcr.io/ban-red/chadash:0.1", "chadash", "app:chadash", wave{base: 0.8, slow: 0.3, fast: 0.2}, wave{base: 64, slow: 3, fast: 0.5}, 0, []string{"3696/tcp"}},
		{"homepage", "ghcr.io/gethomepage/homepage:v1", "homepage", "", wave{base: 0.4, slow: 0.2, fast: 0.1}, wave{base: 120, slow: 4, fast: 1}, 0, []string{"3000/tcp"}}, // the dashboard the Homepage importer reads
		{"dockerproxy", "wollomatic/socket-proxy:1", "proxy", "", wave{base: 0.1, fast: 0.05}, wave{base: 11, fast: 0.5}, 0, []string{"2375/tcp"}},
		{"traefik", "traefik:v3", "proxy", "app:traefik", wave{base: 1, amp: 0.6, peak: 21, slow: 0.3, fast: 0.2}, wave{base: 92, slow: 4, fast: 1}, 0, []string{"80/tcp", "443/tcp"}},
		{"uptimekuma", "louislam/uptime-kuma:2", "uptimekuma", "app:uptimekuma", wave{base: 1.8, slow: 0.4, fast: 0.3}, wave{base: 170, slow: 6, fast: 1}, 0, []string{"3001/tcp"}},
		{"linkding", "sissbruecker/linkding:latest", "linkding", "app:linkding", wave{base: 0.3, slow: 0.1, fast: 0.05}, wave{base: 95, slow: 3, fast: 0.5}, 0, []string{"9090/tcp"}},
		{"karakeep-web-1", "ghcr.io/karakeep-app/karakeep:0.30.2", "karakeep", "app:karakeep", wave{base: 0.8, slow: 0.3, fast: 0.2}, wave{base: 340, slow: 12, fast: 2}, 0, []string{"3000/tcp"}},
		{"karakeep-chrome-1", "gcr.io/zenika-hub/alpine-chrome:124", "karakeep", "", wave{base: 1.2, slow: 0.5, fast: 0.3}, wave{base: 410, slow: 20, fast: 3}, 0, nil},
		{"karakeep-meilisearch-1", "getmeili/meilisearch:v1", "karakeep", "", wave{base: 0.4, slow: 0.2, fast: 0.1}, wave{base: 260, slow: 8, fast: 1}, 0, nil},
		{"paperless-ngx", "ghcr.io/paperless-ngx/paperless-ngx:2.18", "paperless", "app:paperlessngx", wave{base: 0.7, slow: 0.3, fast: 0.2}, wave{base: 540, slow: 15, fast: 2}, 0, []string{"8000/tcp"}},
		{"paperless-redis", "redis:7", "paperless", "", wave{base: 0.2, fast: 0.05}, wave{base: 14, fast: 0.5}, 0, nil},
		{"seerr", "ghcr.io/seerr-team/seerr:v2", "seerr", "app:seerr", wave{base: 0.4, slow: 0.2, fast: 0.1}, wave{base: 270, slow: 8, fast: 1}, 0, []string{"5055/tcp"}},
		{"speedtest-tracker", "lscr.io/linuxserver/speedtest-tracker:latest", "speedtest", "app:speedtesttracker", wave{base: 0.3, slow: 0.1, fast: 0.1}, wave{base: 150, slow: 5, fast: 1}, 0, []string{"80/tcp"}},
		{"nextcloud", "nextcloud:" + ncFrom, "nextcloud", "app:nextcloud", wave{base: 1.2, amp: 0.8, peak: 20, slow: 0.4, fast: 0.2}, wave{base: 430, slow: 14, fast: 2}, 0, []string{"80/tcp"}},
		{"nextcloud-db", "postgres:16", "nextcloud", "", wave{base: 0.6, slow: 0.2, fast: 0.1}, wave{base: 310, slow: 8, fast: 1}, 0, nil},
	}, func(c ct, r *Resource) {
		r.Labels = map[string]string{"com.docker.compose.project": c.project}
	})
	for _, d := range [][2]string{
		{"karakeep-web-1", "karakeep-chrome-1"}, {"karakeep-web-1", "karakeep-meilisearch-1"},
		{"paperless-ngx", "paperless-redis"}, {"nextcloud", "nextcloud-db"},
	} {
		b.rel(eng+"/"+d[0], RelDependsOn, eng+"/"+d[1])
	}

	b.app("app:chadash", "Cha Dash", "Services", "https://dash.home.example", map[string]any{"self": true, "version": "0.1.0-dev"})
	// The counts the Traefik integration reads from /api/overview.
	b.app("app:traefik", "Traefik", "Services", "https://traefik.home.example",
		map[string]any{"routers": 48, "services": 44, "middlewares": 6, "version": "3.5.3", "providers": []string{"docker", "file"}}, "proxy.routes")
	b.app("app:uptimekuma", "Uptime Kuma", "Services", "https://uptime.home.example",
		map[string]any{"status_page": "home", "monitors": len(kumaMonitors)}, "checks.uptime")
	b.app("app:speedtesttracker", "Speedtest Tracker", "Services", "https://speed.home.example", map[string]any{"last_status": "completed"}, "net.speedtest")
	b.app("app:linkding", "Linkding", "Archive", "https://links.home.example", map[string]any{"version": "1.42.0", "archived": 96, "tags": 58}, "bookmarks.library")
	b.app("app:karakeep", "Karakeep", "Archive", "https://karakeep.home.example",
		map[string]any{"version": "0.30.2", "description": "Saved articles and pages"})
	b.app("app:paperlessngx", "Paperless-ngx", "Archive", "https://docs.home.example",
		map[string]any{"version": "2.18.4", "tags": 64, "correspondents": 112, "document_types": 18}, "documents.library")
	b.app("app:seerr", "Seerr", "Media", "https://requests.home.example", map[string]any{"version": "2.7.3"}, "media.requests")
	b.app("app:nextcloud", "Nextcloud", "Apps", "https://cloud.home.example",
		map[string]any{"version": ncFrom, "php_version": "8.3", "database": "pgsql", "users": 4}, "files.storage")
	for _, to := range []string{"app:sonarr", "app:radarr", "app:plex"} {
		b.rel("app:seerr", RelDependsOn, to)
	}

	for app, ms := range map[string]float64{"app:chadash": 8, "app:traefik": 12, "app:uptimekuma": 30, "app:linkding": 38, "app:karakeep": 120,
		"app:paperlessngx": 95, "app:seerr": 50, "app:speedtesttracker": 33, "app:nextcloud": 140} {
		b.check(app, ms)
	}
	b.count("app:karakeep", "bookmarks", "integration:karakeep", 0, 1e6, [2]float64{0, 1e5}, b.growing(1487, 0.2))
	b.count("app:linkding", "bookmarks", "integration:linkding", 0, 1e6, [2]float64{0, 1e5}, b.growing(2264, 0.1))
	b.count("app:uptimekuma", "monitors.up", "integration:uptimekuma", 0, float64(len(kumaMonitors)), [2]float64{float64(len(kumaMonitors)), float64(len(kumaMonitors))},
		func(e *eval, m *metricSpec, t time.Time) float64 { return float64(len(kumaMonitors) - kumaDown(e.w)) })
	b.count("app:uptimekuma", "monitors.down", "integration:uptimekuma", 0, float64(len(kumaMonitors)), [2]float64{0, 0},
		func(e *eval, m *metricSpec, t time.Time) float64 { return float64(kumaDown(e.w)) })

	pl := "integration:paperlessngx"
	b.count("app:paperlessngx", "documents", pl, 0, 1e6, [2]float64{0, 1e6}, b.growing(3184, 0.12))
	b.count("app:paperlessngx", "inbox", pl, 0, 1000, [2]float64{0, 20}, wave{base: 6, amp: 3, peak: 11, slow: 1.2}.fn())
	se := "integration:seerr"
	b.count("app:seerr", "requests.pending", se, 0, 100, [2]float64{0, 5}, wave{base: 2.6, amp: 1.5, peak: 19, slow: 0.7}.fn())
	b.count("app:seerr", "requests.processing", se, 0, 100, [2]float64{0, 10}, wave{base: 3, slow: 1}.fn())
	b.count("app:seerr", "requests.available", se, 0, 10000, [2]float64{0, 10000}, b.growing(418, 0.05))
	nc := "integration:nextcloud"
	b.count("app:nextcloud", "users", nc, 0, 10000, [2]float64{0, 50}, constant(4))
	b.count("app:nextcloud", "users.active", nc, 0, 10000, [2]float64{0, 10}, func(e *eval, m *metricSpec, t time.Time) float64 {
		return 1.6*bump(t, 20, 3) + 0.8*bump(t, 8, 1.5) + 0.5*e.s.rng.smooth(m.hk, t, 20*time.Minute)
	})
	b.count("app:nextcloud", "users.24h", nc, 0, 10000, [2]float64{0, 20}, constant(4))
	b.count("app:nextcloud", "files", nc, 0, 1e9, [2]float64{0, 1e7}, b.growing(182311, 3))
	b.count("app:nextcloud", "shares", nc, 0, 1e6, [2]float64{0, 1000}, constant(137))
	b.metric(&metricSpec{res: "app:nextcloud", key: "free.gib", unit: "GiB", min: 0, max: 1e5, band: [2]float64{50, 1e5}, dec: 1, source: nc,
		fn: func(e *eval, m *metricSpec, t time.Time) float64 { return 1843.2 - 0.02*hours(t.Sub(b.start)) }})

	// Speedtest Tracker's latest result: a test every 30 minutes, at :10
	// and :40. Each series holds the last run's value until the next.
	st := "integration:speedtesttracker"
	run := func(base, spread float64) valueFn {
		return func(e *eval, m *metricSpec, t time.Time) float64 {
			return base + spread*e.s.rng.s(m.hk, speedRun(t))
		}
	}
	b.metric(&metricSpec{res: "app:speedtesttracker", key: "dl.mbps", unit: "Mb/s", min: 0, max: 10000, band: [2]float64{100, 1000}, dec: 1, source: st, fn: run(lineDown, 9)})
	b.metric(&metricSpec{res: "app:speedtesttracker", key: "ul.mbps", unit: "Mb/s", min: 0, max: 10000, band: [2]float64{20, 1000}, dec: 1, source: st, fn: run(lineUp, 2)})
	b.metric(&metricSpec{res: "app:speedtesttracker", key: "ping.ms", unit: "ms", min: 0, max: 1000, band: [2]float64{0, 30}, dec: 1, source: st, fn: run(9.2, 1.1)})
	b.metric(&metricSpec{res: "app:speedtesttracker", key: "jitter.ms", unit: "ms", min: 0, max: 1000, band: [2]float64{0, 10}, dec: 1, source: st, fn: run(1.4, 0.4)})
	b.metric(&metricSpec{res: "app:speedtesttracker", key: "packet_loss.pct", unit: "%", min: 0, max: 100, band: [2]float64{0, 1}, dec: 1, source: st, fn: run(0.1, 0.1)})
}

// speedRun is the index of the latest speed test at or before t: one every
// 30 minutes, at :10 and :40.
func speedRun(t time.Time) int64 { return floorDiv(t.Unix()-600, 1800) }

// speedRunAt is when run k started.
func speedRunAt(k int64) time.Time { return time.Unix(k*1800+600, 0).UTC() }

// kumaMonitors are the sites Uptime Kuma watches.
var kumaMonitors = []string{"app:homeassistant", "net:opnsense", "app:adguard", "app:plex", "app:jellyfin", "app:immich", "app:mealie",
	"app:karakeep", "app:linkding", "app:qbittorrent", "app:cockpit", "app:nextcloud", "app:paperlessngx", "app:audiobookshelf"}

func kumaDown(w *world) int {
	n := 0
	for _, id := range kumaMonitors {
		r, ok := w.res[id]
		// Down is what Kuma sees: not running, or answering with an error.
		if !ok || (r.State != StateRunning && r.State != StateOnline) || r.Health.Status == HealthDown || r.Health.Status == HealthMaintenance {
			n++
		}
	}
	return n
}

// ── backups ──────────────────────────────────────────────────────────────

func (b *builder) backups() {
	b.add(&Resource{ID: "pbs:main", Kind: KindBackupTarget, Name: "main", Source: "integration:pbs", State: StateOnline,
		Labels: map[string]string{"chadash.group": "Backup"},
		Attrs: map[string]any{"store": "main", "capacity_tb": 2.0, "retention": "keep-daily 7 · weekly 4 · monthly 6", "prune_job": "disabled",
			"prune_job_disabled_at": pruneDisabledAt, "snapshots_beyond_policy": 38, "gc_last": b.start.Add(-19*24*time.Hour + 5*time.Hour)},
		Links:  []Link{{Title: "PBS", URL: "https://" + ipMagpie + ":8007/#DataStore-main"}},
		Caps:   []string{"backups.targets"},
		Health: Health{Status: HealthOK, Since: b.start.Add(-30 * 24 * time.Hour)}})
	b.rel("pbs:main", RelPartOf, "app:pbs")
	b.rel("pbs:main", RelStoresOn, "unraid:magpie/pool/backups")
	b.metric(pct("pbs:main", "used.pct", "integration:pbs", [2]float64{60, 88}, 90, 96, wave{base: 86.6, slow: 0.02, fast: 0.01}))

	for _, j := range backupJobs {
		b.add(&Resource{ID: j.id, Kind: KindBackupJob, Name: "daily", Source: j.source, State: StateIdle,
			Attrs: map[string]any{"job": "daily", "schedule": jobSchedule(j.id), "mode": "snapshot",
				"storage": pbsStorage, "enabled": true},
			Links:  []Link{{Title: "Proxmox", URL: pveUI(j.ui)}},
			Caps:   []string{"backups.jobs"},
			Health: Health{Status: HealthOK, Since: b.start.Add(-14 * 24 * time.Hour)}})
		b.rel(j.id, RelStoresOn, "pbs:main")
	}
	for _, s := range backupSlots {
		b.rel(s.guest, RelBackedUpBy, s.job)
	}
}

// pruneDisabledAt is when the PBS prune job was switched off (a cause hint).
var pruneDisabledAt = time.Date(2026, 9, 14, 9, 12, 0, 0, time.UTC)

type backupJob struct {
	id, source string
	offset     time.Duration // after 02:00
	where      string        // the node, or the cluster, as the Proxmox integration names it
	ui         string        // the node whose web UI the job links to
}

var backupJobs = []backupJob{
	{"pve:wren/backup/daily", "integration:proxmox-wren", 0, "wren", "wren"},
	{"pve:aviary/backup/daily", aviarySrc, 15 * time.Minute, "aviary", "heron"},
	{"pve:owl/backup/daily", "integration:proxmox-owl", 30 * time.Minute, "owl", "owl"},
}

func jobOf(id string) backupJob {
	for _, j := range backupJobs {
		if j.id == id {
			return j
		}
	}
	panic("sim: no backup job " + id)
}

// backupSlot is one guest's place in the nightly run, relative to 02:00 UTC.
type backupSlot struct {
	job, guest string
	start, dur time.Duration
}

// backupSlots: each PVE endpoint has its own job; guests run one after
// another, the cluster's across its nodes. win-games is stopped, so it has
// no dirty bitmap and every backup reads its whole disk, which is why it
// takes 22 minutes. stork's sandbox isn't in any job.
var backupSlots = []backupSlot{
	{"pve:wren/backup/daily", "pve:wren/qemu/100", 0, 3 * time.Minute},
	{"pve:wren/backup/daily", "pve:wren/lxc/101", 3 * time.Minute, time.Minute},
	{"pve:wren/backup/daily", "pve:wren/lxc/102", 4 * time.Minute, time.Minute},
	{"pve:aviary/backup/daily", "pve:heron/qemu/200", 15 * time.Minute, 4 * time.Minute},
	{"pve:aviary/backup/daily", "pve:heron/lxc/201", 19 * time.Minute, 3 * time.Minute},
	{"pve:aviary/backup/daily", "pve:egret/lxc/210", 22 * time.Minute, 5 * time.Minute},
	{"pve:aviary/backup/daily", "pve:stork/lxc/220", 27 * time.Minute, 2 * time.Minute},
	{"pve:owl/backup/daily", "pve:owl/qemu/301", 30 * time.Minute, 22 * time.Minute},
	{"pve:owl/backup/daily", "pve:owl/qemu/302", 52 * time.Minute, 6 * time.Minute},
	{"pve:owl/backup/daily", "pve:owl/qemu/303", 58 * time.Minute, 3 * time.Minute},
}

// ── Traefik routes ───────────────────────────────────────────────────────

func (b *builder) routes() {
	for _, r := range []struct{ name, rule, upstream, to string }{
		{"ha", "Host(`ha.home.example`)", "http://" + ipHA + ":8123", "app:homeassistant"},
		{"traefik", "Host(`traefik.home.example`)", "api@internal", "app:traefik"},
		{"uptime", "Host(`uptime.home.example`)", "http://uptimekuma:3001", "app:uptimekuma"},
		{"dash", "Host(`dash.home.example`)", "http://chadash:3696", "app:chadash"},
		{"links", "Host(`links.home.example`)", "http://linkding:9090", "app:linkding"},
		{"karakeep", "Host(`karakeep.home.example`)", "http://karakeep-web-1:3000", "app:karakeep"},
		{"docs", "Host(`docs.home.example`)", "http://paperless-ngx:8000", "app:paperlessngx"},
		{"cloud", "Host(`cloud.home.example`)", "http://nextcloud:80", "app:nextcloud"},
		{"plex", "Host(`plex.home.example`)", "http://" + ipMagpie + ":32400", "app:plex"},
		{"jellyfin", "Host(`jellyfin.home.example`)", "http://" + ipMagpie + ":8096", "app:jellyfin"},
		{"sonarr", "Host(`arr.home.example`) && PathPrefix(`/sonarr`)", "http://" + ipMagpie + ":8989", "app:sonarr"},
		{"radarr", "Host(`arr.home.example`) && PathPrefix(`/radarr`)", "http://" + ipMagpie + ":7878", "app:radarr"},
		{"torrents", "Host(`torrents.home.example`)", "http://" + ipMagpie + ":8080", "app:qbittorrent"},
		{"food", "Host(`food.home.example`)", "http://" + ipMagpie + ":9925", "app:mealie"},
		{"photos", "Host(`photos.home.example`)", "http://" + ipMagpie + ":2283", "app:immich"},
		{"adguard", "Host(`adguard.home.example`)", "http://" + ipAdGuard + ":80", "app:adguard"},
		{"tautulli", "Host(`plex.home.example`) && PathPrefix(`/tautulli`)", "http://" + ipMagpie + ":8181", "app:tautulli"},
		{"bazarr", "Host(`arr.home.example`) && PathPrefix(`/bazarr`)", "http://" + ipMagpie + ":6767", "app:bazarr"},
		{"prowlarr", "Host(`arr.home.example`) && PathPrefix(`/prowlarr`)", "http://" + ipMagpie + ":9696", "app:prowlarr"},
		{"sabnzbd", "Host(`arr.home.example`) && PathPrefix(`/sabnzbd`)", "http://" + ipMagpie + ":8085", "app:sabnzbd"},
		{"books", "Host(`books.home.example`)", "http://" + ipMagpie + ":13378", "app:audiobookshelf"},
		{"requests", "Host(`requests.home.example`)", "http://seerr:5055", "app:seerr"},
		{"speed", "Host(`speed.home.example`)", "http://speedtest-tracker:80", "app:speedtesttracker"},
	} {
		id := "traefik:route/" + r.name
		b.add(&Resource{ID: id, Kind: KindRoute, Name: r.name, Source: "integration:traefik", State: StateEnabled,
			Attrs:  map[string]any{"rule": r.rule, "upstream": r.upstream, "entrypoint": "websecure", "tls": true},
			Health: Health{Status: HealthOK, Since: b.start.Add(-9 * 24 * time.Hour)}})
		b.rel(id, RelPartOf, "app:traefik")
		b.rel(id, RelRoutesTo, r.to)
	}
}

// ── built-in checks ──────────────────────────────────────────────────────

// checks adds two checks of the kind the Homepage importer creates from a
// siteMonitor: the Unraid web UI, which answers while its API integration
// can't read it, and Linkding, which a check watches beside its integration.
func (b *builder) checks() {
	src := "integration:checks"
	for _, c := range []struct {
		slug, name, url string
		status          int
		ms              float64
		monitors        string
	}{
		{"magpie-unraid", "Magpie Unraid", "http://" + ipMagpie, 302, 34, ""}, // a redirect to the sign-in page means up
		{"linkding", "Linkding", "https://links.home.example", 200, 41, "app:linkding"},
	} {
		id := "check:checks/" + c.slug
		b.add(&Resource{ID: id, Kind: KindCheck, Name: c.name, Source: src, State: StateOnline,
			Attrs:  map[string]any{"check": "http", "target": c.url, "status_code": c.status},
			Links:  []Link{{Title: c.name, URL: c.url}},
			Caps:   []string{"checks.uptime"},
			Health: Health{Status: HealthOK, Since: b.start.Add(-9*24*time.Hour + 2*time.Minute)}})
		b.rel(src, RelMonitors, id)
		if c.monitors != "" {
			b.rel(id, RelMonitors, c.monitors) // the correlator's: the check's target is the app's URL
		}
		b.check(id, c.ms, src)
	}
}

// ── finish ───────────────────────────────────────────────────────────────

func (b *builder) finish() {
	// Apps take their source from the integration that monitors them, so the
	// UI knows which instance reports each one.
	for _, it := range integrationTable {
		if r, ok := b.tp.res[it.monitors]; ok && r.Kind == KindApp {
			r.Source = "integration:" + it.id
		}
	}
	for _, r := range b.tp.res {
		if r.Source == "" {
			r.Source = "check" // apps seen only by built-in checks: Cockpit, Cha Dash
		}
	}
	// Apps are healthy since their runtime started.
	for _, rel := range b.tp.rels {
		if rel.Type != RelRunsOn {
			continue
		}
		from, to := b.tp.res[rel.From], b.tp.res[rel.To]
		if from == nil || to == nil || from.Kind != KindApp {
			continue
		}
		if s, ok := to.Attrs["started_at"].(time.Time); ok {
			from.Health.Since = s
		}
	}
	slices.SortFunc(b.tp.rels, cmpRel)
	b.tp.rels = slices.Compact(b.tp.rels)
	slices.SortFunc(b.tp.specs, func(a, c *metricSpec) int {
		return cmp.Or(cmp.Compare(a.res, c.res), cmp.Compare(a.key, c.key))
	})
}

func cmpRel(a, c Relation) int {
	return cmp.Or(cmp.Compare(a.From, c.From), cmp.Compare(a.Type, c.Type), cmp.Compare(a.To, c.To))
}
