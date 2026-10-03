package sim

import (
	"cmp"
	"fmt"
	"slices"
	"time"
)

// The Demo Home topology follows docs/plan/05-dogfood-home.md: five Proxmox VE
// endpoints (mars is a 2-host cluster), the Unraid box jupiter with PBS in a
// container, the pvedocker LXC as the second Docker host, OPNsense as a VM on
// pluto, Home Assistant and Frigate on minig5. Addresses are sanitized:
// 10.0.11.x and *.lab.example / *.family.example only.

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

type builder struct {
	start time.Time
	rng   rng
	tp    *topology
}

func buildTopology(start time.Time, r rng) *topology {
	b := &builder{start: start, rng: r, tp: &topology{res: map[string]*Resource{}, specIdx: map[string]*metricSpec{}}}
	b.integrations()
	b.network()
	b.pluto()
	b.minig5()
	b.mars()
	b.io()
	b.jupiterNAS()
	b.jupiter()
	b.pvedocker()
	b.backups()
	b.routes()
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

// temp adds a CPU temperature that follows the resource's CPU load.
func (b *builder) temp(res, source string, base, cpuBase float64) {
	b.metric(&metricSpec{res: res, key: "temp.c", unit: "°C", min: 20, max: 105, band: [2]float64{base - 8, base + 12}, warn: 80, crit: 90, dec: 0, source: source,
		fn: func(e *eval, m *metricSpec, t time.Time) float64 {
			cpu := e.raw(res, "cpu.pct", t)
			return base + 0.3*(cpu-cpuBase) + 1.5*diurnal(t, 16) + 0.8*e.s.rng.smooth(m.hk, t, 30*time.Minute)
		}})
}

func (b *builder) check(app string, ms float64) {
	b.metric(&metricSpec{res: app, key: "http.ms", unit: "ms", min: 1, max: 5000, band: [2]float64{1, max(150, ms*2.5)}, warn: 800, crit: 2000, dec: 0,
		fn: wave{base: ms, amp: ms * 0.25, peak: 21, slow: ms * 0.12, fast: ms * 0.08}.fn()})
}

func (b *builder) count(res, key, source string, lo, hi float64, band [2]float64, fn valueFn) {
	b.metric(&metricSpec{res: res, key: key, unit: "count", min: lo, max: hi, band: band, dec: 0, source: source, fn: fn})
}

func pveUI(host string) string { return "https://" + host + ".lab.example:8006/" }

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
}

var integrationTable = []integ{
	{"proxmox-pluto", "proxmox", "go", "token", "https", true, "9.0", "pve:pluto", "Servers"},
	{"proxmox-minig5", "proxmox", "go", "token", "https", true, "9.0", "pve:minig5", "Servers"},
	{"proxmox-mars", "proxmox", "go", "token", "https", true, "9.0", "pve:mars", "Servers"},
	{"proxmox-io", "proxmox", "go", "token", "https", true, "9.0", "pve:io", "Servers"},
	{"proxmox-jupiter-nas", "proxmox", "go", "token", "https", true, "9.0", "pve:jupiter-nas", "Servers"},
	{"unraid-jupiter", "unraid", "go", "key", "https", true, "7.3", "unraid:jupiter", "Servers"},
	{"docker-pvedocker", "docker", "go", "socket-proxy", "tcp", true, "", "docker:pvedocker", "Servers"},
	{"docker-jupiter", "docker", "go", "socket-proxy", "tcp", true, "", "docker:jupiter", "Servers"},
	{"opnsense", "opnsense", "go", "key", "http", true, "", "net:opnsense", "Servers"},
	{"pbs", "pbs", "go", "token", "https", true, "4.0", "app:pbs", "Backup"},
	{"homeassistant", "homeassistant", "go", "token", "https", false, "", "app:homeassistant", "Home"},
	{"frigate", "frigate", "go", "none", "http", true, "0.18.0", "app:frigate", "Home"},
	{"traefik", "traefik", "go", "basic", "http", true, "", "app:traefik", "Services"},
	{"uptimekuma", "uptimekuma", "go", "key", "https", true, "", "app:uptimekuma", "Services"},
	{"plex", "plex", "go", "token", "https", true, "", "app:plex", "Media"},
	{"tautulli", "tautulli", "spec", "key", "https", true, "", "app:tautulli", "Media"},
	{"sonarr", "sonarr", "go", "key", "https", true, "", "app:sonarr", "Media"},
	{"radarr", "radarr", "go", "key", "https", true, "", "app:radarr", "Media"},
	{"bazarr", "bazarr", "spec", "key", "https", true, "", "app:bazarr", "Media"},
	{"prowlarr", "prowlarr", "go", "key", "https", true, "", "app:prowlarr", "Downloads"},
	{"qbittorrent", "qbittorrent", "go", "basic", "https", true, "", "app:qbittorrent", "Downloads"},
	{"karakeep", "karakeep", "spec", "key", "https", true, "0.30.2", "app:karakeep", "Archive"},
	{"immich", "immich", "go", "key", "https", true, "", "app:immich", "Apps"},
	{"mealie", "mealie", "spec", "key", "https", true, "", "app:mealie", "Apps"},
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
		b.add(&Resource{ID: id, Kind: KindIntegration, Name: it.id, Source: "hub", State: StateConnected,
			Labels: map[string]string{"chadash.group": it.group}, Attrs: attrs,
			Health: Health{Status: HealthOK, Since: b.start.Add(-9 * 24 * time.Hour)}})
		b.rel(id, RelMonitors, it.monitors)
	}
	b.rel("integration:docker-pvedocker", RelDependsOn, "docker:pvedocker/dockerproxy")
	b.rel("integration:docker-jupiter", RelDependsOn, "docker:jupiter/socket-proxy")
}

// ── network ──────────────────────────────────────────────────────────────

func (b *builder) network() {
	b.add(&Resource{ID: "net:opnsense", Kind: KindRouter, Name: "OPNsense", Source: "integration:opnsense", State: StateRunning, Desired: StateRunning,
		Labels: map[string]string{"chadash.role": "network-core", "chadash.group": "Servers"},
		Attrs:  map[string]any{"lan": "10.0.11.0/24", "ip": "10.0.11.1", "boot_at": b.ago("net:opnsense", 24*20, 24*45)},
		Links:  []Link{{Title: "OPNsense", URL: "https://opnsense.lab.example"}},
		Caps:   []string{"net.router", "net.wan"}})
	b.add(&Resource{ID: "net:opnsense/wan", Kind: KindWAN, Name: "WAN", Source: "integration:opnsense", State: StateOnline,
		Attrs: map[string]any{"interface": "wan"}})
	b.rel("net:opnsense", RelRunsOn, "pve:pluto/qemu/100")
	b.rel("net:opnsense/wan", RelPartOf, "net:opnsense")

	src := "integration:opnsense"
	b.metric(pct("net:opnsense", "cpu.pct", src, [2]float64{5, 40}, 0, 0, wave{base: 17, amp: 7, peak: 21, slow: 3, fast: 1.5}))
	b.metric(&metricSpec{res: "net:opnsense", key: "mem.mib", unit: "MiB", min: 0, max: 8192, band: [2]float64{200, 420}, dec: 0, source: src,
		fn: wave{base: 288, amp: 14, peak: 21, slow: 9, fast: 2}.fn()})
	wan := "net:opnsense/wan"
	b.metric(&metricSpec{res: wan, key: "rx.mbps", unit: "Mb/s", min: 0, max: 1000, band: [2]float64{2, 60}, dec: 1, source: src,
		fn: func(e *eval, m *metricSpec, t time.Time) float64 {
			v := 7 + 38*eveningMedia(t) + 9*bump(t, 9, 3) + 4*e.s.rng.smooth(m.hk, t, 15*time.Minute) + 2*e.s.rng.jitter(m.hk, t)
			// Short bursts: a download or a game update, a few per day.
			if u := e.s.rng.u(m.hk^0xb5, t.Unix()/600); u > 0.93 {
				v += 140 * (u - 0.93) / 0.07
			}
			return v
		}})
	b.metric(&metricSpec{res: wan, key: "tx.mbps", unit: "Mb/s", min: 0, max: 1000, band: [2]float64{1, 20}, dec: 1, source: src,
		fn: wave{base: 2.6, amp: 7, peak: 21.5, slow: 1.2, fast: 0.6}.fn()})
	b.metric(&metricSpec{res: wan, key: "rx.gb_30d", unit: "GB", min: 0, max: 100000, band: [2]float64{600, 1100}, dec: 0, source: src,
		fn: wave{base: 852, slow: 3}.fn()})
	b.metric(&metricSpec{res: wan, key: "tx.gb_30d", unit: "GB", min: 0, max: 100000, band: [2]float64{300, 600}, dec: 0, source: src,
		fn: wave{base: 418, slow: 2}.fn()})
}

// ── PVE hosts and guests ─────────────────────────────────────────────────

func (b *builder) pveHost(id, name, src, ip, role string, cores int, memGB float64) *Resource {
	r := b.add(&Resource{ID: id, Kind: KindHost, Name: name, Source: src, State: StateOnline,
		Labels: map[string]string{"chadash.role": role, "chadash.group": "Servers"},
		Attrs:  map[string]any{"platform": "pve", "ip": ip, "cores": cores, "mem_gb": memGB, "boot_at": b.ago(id, 24*6, 24*60)},
		Links:  []Link{{Title: "Proxmox", URL: pveUI(name)}},
		Caps:   []string{"host.metrics", "compute.guests", "power"}})
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
}

func (b *builder) guest(g guestSpec, src string) string {
	hostName := g.host[len("pve:"):]
	id := fmt.Sprintf("%s/%s/%d", g.host, g.typ, g.vmid)
	kind := KindVM
	if g.typ == "lxc" {
		kind = KindLXC
	}
	state, desired := StateStopped, StateStopped
	attrs := map[string]any{"vmid": g.vmid, "os": g.os, "mem_gb": g.memGB, "onboot": g.running}
	if g.ip != "" {
		attrs["ip"] = g.ip
	}
	health := Health{Status: HealthOK, Since: b.ago("stopped:"+id, 24*2, 24*30)}
	if g.running {
		state, desired = StateRunning, StateRunning
		boot := b.tp.res[g.host].Attrs["boot_at"].(time.Time)
		started := boot.Add(time.Duration(2+b.rng.u(hashKey("onboot:"+id), 0)*6) * time.Minute).Truncate(time.Second)
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

func (b *builder) app(id, name, group, url string, caps ...string) *Resource {
	return b.add(&Resource{ID: id, Kind: KindApp, Name: name, Source: "", State: StateRunning, Desired: StateRunning,
		Labels: map[string]string{"chadash.group": group},
		Links:  []Link{{Title: name, URL: url}},
		Caps:   caps})
}

func (b *builder) pluto() {
	src := "integration:proxmox-pluto"
	b.pveHost("pve:pluto", "pluto", src, "10.0.11.10", "network-core", 4, 16)
	b.cpuMem("pve:pluto", src, wave{base: 7, amp: 3, peak: 21, slow: 1.5, fast: 0.8}, wave{base: 80, amp: 1, peak: 21, slow: 0.8, fast: 0.2}, [2]float64{55, 85})
	b.temp("pve:pluto", src, 47, 7)
	st := b.pveStorage("pve:pluto", "local-zfs", "zfspool", 0.5, src, 31)

	vm := b.guest(guestSpec{host: "pve:pluto", typ: "qemu", vmid: 100, name: "opnsense", os: "freebsd", ip: "10.0.11.1", memGB: 8, running: true, storage: st}, src)
	b.cpuMem(vm, src, wave{base: 12, amp: 6, peak: 21, slow: 2, fast: 1}, wave{base: 45, slow: 1, fast: 0.3}, [2]float64{30, 60})
	ct := b.guest(guestSpec{host: "pve:pluto", typ: "lxc", vmid: 101, name: "tailscale", os: "debian", ip: "10.0.11.5", memGB: 0.5, running: true, storage: st}, src)
	b.cpuMem(ct, src, wave{base: 1, amp: 0.4, peak: 21, slow: 0.3, fast: 0.2}, wave{base: 30, slow: 1.5, fast: 0.3}, [2]float64{15, 50})
}

func (b *builder) minig5() {
	src := "integration:proxmox-minig5"
	b.pveHost("pve:minig5", "minig5", src, "10.0.11.11", "home-automation", 6, 16)
	b.cpuMem("pve:minig5", src, wave{base: 34, amp: 9, peak: 14, slow: 3, fast: 1.5}, wave{base: 66, amp: 1.5, peak: 14, slow: 1, fast: 0.3}, [2]float64{50, 80})
	b.temp("pve:minig5", src, 58, 38)
	st := b.pveStorage("pve:minig5", "local-lvm", "lvmthin", 0.5, src, 48)

	vm := b.guest(guestSpec{host: "pve:minig5", typ: "qemu", vmid: 100, name: "homeassistant", os: "haos", ip: "10.0.11.203", memGB: 4, running: true, storage: st}, src)
	b.cpuMem(vm, src, wave{base: 6, amp: 2, peak: 19, slow: 1.2, fast: 0.6}, wave{base: 58, amp: 2, peak: 19, slow: 1, fast: 0.3}, [2]float64{40, 75})
	ct := b.guest(guestSpec{host: "pve:minig5", typ: "lxc", vmid: 101, name: "frigate", os: "debian", ip: "10.0.11.202", memGB: 4, running: true, storage: st}, src)
	b.cpuMem(ct, src, wave{base: 42, amp: 15, peak: 14, slow: 5, fast: 2}, wave{base: 52, amp: 3, peak: 14, slow: 1.5, fast: 0.4}, [2]float64{35, 70})

	ha := b.app("app:homeassistant", "Home Assistant", "Home", "https://home.lab.example", "home.entities")
	ha.Attrs = map[string]any{"people_total": 2, "lights_total": 39, "switches_total": 305}
	b.rel("app:homeassistant", RelRunsOn, vm)
	b.rel("app:homeassistant", RelDependsOn, "app:frigate")
	fr := b.app("app:frigate", "Frigate", "Home", "http://10.0.11.202:55000", "cameras", "cameras.events")
	fr.Attrs = map[string]any{"version": "0.18.0", "cameras": 3, "detector": "openvino"}
	b.rel("app:frigate", RelRunsOn, ct)
	b.check("app:homeassistant", 35)
	b.check("app:frigate", 22)

	hs := "integration:homeassistant"
	b.metric(&metricSpec{res: "app:homeassistant", key: "power.kw", unit: "kW", min: 0, max: 12, band: [2]float64{0.6, 2.2}, dec: 2, source: hs, fn: housePower})
	b.metric(&metricSpec{res: "app:homeassistant", key: "energy.kwh", unit: "kWh", min: 0, max: 200, band: [2]float64{0, 40}, dec: 1, source: hs,
		fn: func(e *eval, m *metricSpec, t time.Time) float64 {
			// Energy today: power integrated from local midnight in 5-minute steps.
			sum := 0.0
			for at := dayStart(t); at.Before(t); at = at.Add(5 * time.Minute) {
				step := min(5*time.Minute, t.Sub(at))
				sum += housePower(e, m, at) * hours(step)
			}
			return sum
		}})
	b.count("app:homeassistant", "people_home", hs, 0, 2, [2]float64{0, 2}, func(e *eval, m *metricSpec, t time.Time) float64 {
		h := hourOf(t)
		if h >= 10.5 && h < 12.75 { // one of the two is out for groceries
			return 1
		}
		return 2
	})
	b.count("app:homeassistant", "lights_on", hs, 0, 39, [2]float64{0, 12}, func(e *eval, m *metricSpec, t time.Time) float64 {
		h := hourOf(t)
		base := 1.0
		switch {
		case h >= 6.5 && h < 8.5:
			base = 4
		case h >= 18.5 && h < 23.25:
			base = 9
		case h >= 23.25:
			base = 3
		}
		return base + 1.2*e.s.rng.smooth(m.hk, t, 30*time.Minute)
	})
	b.count("app:homeassistant", "switches_on", hs, 0, 305, [2]float64{120, 160}, wave{base: 141, amp: 4, peak: 20, slow: 1.5}.fn())

	b.metric(&metricSpec{res: "app:frigate", key: "inference.ms", unit: "ms", min: 1, max: 500, band: [2]float64{5, 20}, dec: 1, source: "integration:frigate",
		fn: wave{base: 9.4, amp: 1.2, peak: 14, slow: 0.6, fast: 0.3}.fn()})

	for i, c := range cameraTable {
		id := "frigate:camera/" + c.id
		b.add(&Resource{ID: id, Kind: KindCamera, Name: c.name, Source: "integration:frigate", State: StateOnline,
			Labels: map[string]string{"chadash.group": "Home"},
			Attrs:  map[string]any{"ip": fmt.Sprintf("10.0.11.%d", 231+i), "fps": 5, "detect": true, "record": true},
			Health: Health{Status: HealthOK, Since: b.ago(id, 24*10, 24*40)}})
		b.rel("app:frigate", RelExposes, id)
		b.rel("net:opnsense", RelProvidesNetworkFor, id)
		b.count(id, "detections.24h", "integration:frigate", 0, 500, [2]float64{0, 40}, func(e *eval, m *metricSpec, t time.Time) float64 {
			return float64(e.s.detectionsIn(c, t.Add(-24*time.Hour), t))
		})
	}
}

// housePower is the house's total power draw: a base load, morning and
// evening bumps, and noise.
func housePower(e *eval, m *metricSpec, t time.Time) float64 {
	return 0.85 + 0.6*bump(t, 7.5, 1.5) + 1.15*bump(t, 19.75, 3) + 0.12*e.s.rng.smooth(m.hk, t, 20*time.Minute)
}

func (b *builder) mars() {
	src := "integration:proxmox-mars"
	b.add(&Resource{ID: "pve:mars", Kind: KindCluster, Name: "mars", Source: src, State: StateOnline,
		Labels: map[string]string{"chadash.group": "Servers"},
		Attrs:  map[string]any{"nodes": 2, "quorate": true},
		Links:  []Link{{Title: "Proxmox", URL: pveUI("mars-1")}},
		Health: Health{Status: HealthOK, Since: b.start.Add(-90 * 24 * time.Hour)}})
	b.pveHost("pve:mars-1", "mars-1", src, "10.0.11.12", "docker", 8, 32)
	b.pveHost("pve:mars-2", "mars-2", src, "10.0.11.13", "cluster", 8, 32)
	b.rel("pve:mars-1", RelPartOf, "pve:mars")
	b.rel("pve:mars-2", RelPartOf, "pve:mars")
	b.cpuMem("pve:mars-1", src, wave{base: 4, amp: 1.5, peak: 21, slow: 1, fast: 0.5}, wave{base: 32, amp: 1, peak: 21, slow: 0.6, fast: 0.2}, [2]float64{20, 60})
	b.cpuMem("pve:mars-2", src, wave{base: 1, amp: 0.3, peak: 21, slow: 0.3, fast: 0.2}, wave{base: 28, slow: 0.4, fast: 0.1}, [2]float64{15, 60})
	b.temp("pve:mars-1", src, 41, 4)
	b.temp("pve:mars-2", src, 38, 1)
	st := b.pveStorage("pve:mars-1", "local-lvm", "lvmthin", 1, src, 40)
	b.pveStorage("pve:mars-2", "local-lvm", "lvmthin", 1, src, 12)

	ct := b.guest(guestSpec{host: "pve:mars-1", typ: "lxc", vmid: 110, name: "pvedocker", os: "debian", ip: "10.0.11.201", memGB: 8, running: true, storage: st}, src)
	b.cpuMem(ct, src, wave{base: 6, amp: 2, peak: 21, slow: 1.2, fast: 0.6}, wave{base: 40, amp: 1, peak: 21, slow: 0.8, fast: 0.2}, [2]float64{25, 65})
}

func (b *builder) io() {
	src := "integration:proxmox-io"
	b.pveHost("pve:io", "io", src, "10.0.11.14", "workstation", 16, 64)
	b.cpuMem("pve:io", src, wave{base: 1.5, amp: 0.8, peak: 20, slow: 0.5, fast: 0.3}, wave{base: 55, slow: 0.6, fast: 0.2}, [2]float64{30, 70})
	b.temp("pve:io", src, 36, 1.5)
	st := b.pveStorage("pve:io", "local-lvm", "lvmthin", 2, src, 67)
	b.guest(guestSpec{host: "pve:io", typ: "qemu", vmid: 201, name: "win-gaming", os: "windows", memGB: 16, storage: st}, src)
	vm := b.guest(guestSpec{host: "pve:io", typ: "qemu", vmid: 202, name: "iolinux", os: "ubuntu", ip: "10.0.11.204", memGB: 8, running: true, storage: st}, src)
	b.cpuMem(vm, src, wave{base: 2, amp: 1, peak: 20, slow: 0.8, fast: 0.4}, wave{base: 35, slow: 1, fast: 0.3}, [2]float64{20, 60})
	b.guest(guestSpec{host: "pve:io", typ: "qemu", vmid: 203, name: "devbox", os: "ubuntu", memGB: 8, storage: st}, src)
}

func (b *builder) jupiterNAS() {
	src := "integration:proxmox-jupiter-nas"
	h := b.pveHost("pve:jupiter-nas", "jupiter-nas", src, "10.0.11.15", "nas", 4, 8)
	h.Attrs["swap_gb"] = 0
	b.cpuMem("pve:jupiter-nas", src, wave{base: 4, amp: 1.5, peak: 20, slow: 1, fast: 0.5}, wave{base: 62, amp: 1, peak: 20, slow: 0.8, fast: 0.2}, [2]float64{45, 85})
	b.temp("pve:jupiter-nas", src, 44, 4)
	st := b.pveStorage("pve:jupiter-nas", "tank", "zfspool", 8, src, 58)
	ct := b.guest(guestSpec{host: "pve:jupiter-nas", typ: "lxc", vmid: 100, name: "debian-nas", os: "debian", ip: "10.0.11.121", memGB: 4, running: true, storage: st}, src)
	b.cpuMem(ct, src, wave{base: 3, amp: 1, peak: 20, slow: 0.8, fast: 0.4}, wave{base: 12.5, amp: 0.5, peak: 20, slow: 0.4, fast: 0.1}, [2]float64{5, 40})
	b.guest(guestSpec{host: "pve:jupiter-nas", typ: "qemu", vmid: 101, name: "scratch", os: "debian", memGB: 2, storage: st}, src)
	b.app("app:cockpit", "Cockpit", "Servers", "https://jupiter-nas-debian.lab.example:9090/")
	b.rel("app:cockpit", RelRunsOn, ct)
	b.check("app:cockpit", 23)
}

// ── jupiter (Unraid) ─────────────────────────────────────────────────────

type diskSpec struct {
	name   string
	sizeTB float64
	used   float64 // 0 for parity
	temp   float64
}

var arrayDisks = []diskSpec{
	{"parity", 16, 0, 38},
	{"disk1", 14, 89, 41},
	{"disk2", 12, 84, 39},
	{"disk3", 12, 77, 37},
	{"disk4", 8, 63, 36},
	{"disk5", 8, 52, 35},
	{"disk6", 8, 40, 35},
}

func (b *builder) jupiter() {
	src := "integration:unraid-jupiter"
	b.add(&Resource{ID: "unraid:jupiter", Kind: KindHost, Name: "jupiter", Source: src, State: StateOnline,
		Labels: map[string]string{"chadash.role": "nas", "chadash.group": "Servers"},
		Attrs:  map[string]any{"platform": "unraid", "version": "7.3", "ip": "10.0.11.120", "cores": 12, "mem_gb": 64, "boot_at": b.ago("unraid:jupiter", 24*12, 24*50)},
		Links:  []Link{{Title: "Unraid", URL: "https://jupiter.lab.example/"}},
		Caps:   []string{"host.metrics", "storage.pools", "storage.disks", "storage.shares", "containers", "power"}})
	b.rel("net:opnsense", RelProvidesNetworkFor, "unraid:jupiter")
	b.cpuMem("unraid:jupiter", src,
		wave{base: 14, amp: 8, peak: 21, slow: 2.5, fast: 1.2, extra: func(t time.Time) float64 { return 9 * nightlyBackups(t) }},
		wave{base: 41, amp: 2, peak: 21, slow: 0.8, fast: 0.2}, [2]float64{30, 60})
	b.temp("unraid:jupiter", src, 52, 18)

	// Array and disks.
	usable := 0.0
	for _, d := range arrayDisks[1:] {
		usable += d.sizeTB
	}
	b.add(&Resource{ID: "unraid:jupiter/array", Kind: KindArray, Name: "array", Source: src, State: StateOnline,
		Attrs:  map[string]any{"size_tb": usable, "parity": "valid", "parity_checked_at": b.start.Add(-6*24*time.Hour - 7*time.Hour)},
		Health: Health{Status: HealthOK, Since: b.start.Add(-6*24*time.Hour - 7*time.Hour)}})
	b.rel("unraid:jupiter/array", RelPartOf, "unraid:jupiter")
	for _, d := range arrayDisks {
		id := "unraid:jupiter/disk/" + d.name
		role := "data"
		if d.used == 0 {
			role = "parity"
		}
		b.add(&Resource{ID: id, Kind: KindDisk, Name: d.name, Source: src, State: StateOnline,
			Attrs: map[string]any{"size_tb": d.sizeTB, "role": role, "fs": "xfs"}})
		b.rel(id, RelPartOf, "unraid:jupiter/array")
		if d.used > 0 {
			b.metric(pct(id, "used.pct", src, [2]float64{0, 85}, 90, 97, wave{base: d.used, slow: 0.02}))
		}
		b.metric(&metricSpec{res: id, key: "temp.c", unit: "°C", min: 15, max: 70, band: [2]float64{30, 45}, warn: 50, crit: 55, dec: 0, source: src,
			fn: wave{base: d.temp, amp: 1.5, peak: 16, slow: 0.6, extra: func(t time.Time) float64 { return 1.5 * nightlyBackups(t) }}.fn()})
	}
	b.metric(pct("unraid:jupiter/array", "used.pct", src, [2]float64{0, 85}, 85, 95, wave{}))
	b.tp.specIdx["unraid:jupiter/array|used.pct"].fn = func(e *eval, m *metricSpec, t time.Time) float64 {
		sum := 0.0
		for _, d := range arrayDisks[1:] {
			sum += e.raw("unraid:jupiter/disk/"+d.name, "used.pct", t) * d.sizeTB
		}
		return sum / usable
	}

	// Pools and shares.
	b.add(&Resource{ID: "unraid:jupiter/pool/cache", Kind: KindPool, Name: "cache", Source: src, State: StateOnline,
		Attrs: map[string]any{"size_tb": 2, "fs": "btrfs", "devices": 1, "media": "nvme"}})
	b.add(&Resource{ID: "unraid:jupiter/pool/backups", Kind: KindPool, Name: "backups", Source: src, State: StateOnline,
		Attrs: map[string]any{"size_tb": 2, "fs": "xfs", "devices": 1, "media": "hdd"}})
	b.rel("unraid:jupiter/pool/cache", RelPartOf, "unraid:jupiter")
	b.rel("unraid:jupiter/pool/backups", RelPartOf, "unraid:jupiter")
	b.metric(pct("unraid:jupiter/pool/cache", "used.pct", src, [2]float64{0, 80}, 85, 95, wave{base: 44, slow: 0.1}))
	b.metric(&metricSpec{res: "unraid:jupiter/pool/cache", key: "temp.c", unit: "°C", min: 15, max: 90, band: [2]float64{30, 55}, warn: 70, crit: 80, dec: 0, source: src,
		fn: wave{base: 40, amp: 3, peak: 21, slow: 1}.fn()})
	b.metric(pct("unraid:jupiter/pool/backups", "used.pct", src, [2]float64{0, 88}, 90, 96, wave{}))
	b.tp.specIdx["unraid:jupiter/pool/backups|used.pct"].fn = func(e *eval, m *metricSpec, t time.Time) float64 {
		return e.raw("pbs:main", "used.pct", t) + 0.4
	}
	b.metric(&metricSpec{res: "unraid:jupiter/pool/backups", key: "temp.c", unit: "°C", min: 15, max: 70, band: [2]float64{30, 45}, warn: 50, crit: 55, dec: 0, source: src,
		fn: wave{base: 36, amp: 1, peak: 16, slow: 0.5, extra: func(t time.Time) float64 { return 4 * nightlyBackups(t) }}.fn()})
	for _, sh := range []struct{ name, on string }{{"media", "unraid:jupiter/array"}, {"photos", "unraid:jupiter/array"}, {"appdata", "unraid:jupiter/pool/cache"}} {
		id := "unraid:jupiter/share/" + sh.name
		b.add(&Resource{ID: id, Kind: KindShare, Name: sh.name, Source: src, State: StateOnline})
		b.rel(id, RelPartOf, sh.on)
	}

	// Docker engine, reached through a socket-proxy container on :2375.
	eng := "docker:jupiter"
	boot := b.tp.res["unraid:jupiter"].Attrs["boot_at"].(time.Time)
	engStart := boot.Add(4 * time.Minute)
	b.add(&Resource{ID: eng, Kind: KindEngine, Name: "jupiter", Source: "integration:docker-jupiter", State: StateRunning,
		Attrs: map[string]any{"endpoint": "tcp://10.0.11.120:2375", "via": "socket-proxy", "manager": "unraid", "started_at": engStart},
		Caps:  []string{"containers"}})
	b.rel(eng, RelRunsOn, "unraid:jupiter")
	b.rel(eng, RelDependsOn, "unraid:jupiter/array")
	b.rel(eng, RelStoresOn, "unraid:jupiter/pool/cache")

	type ct struct {
		name, image, app string
		cpu, mem         wave
		memLimit         int
		ports            []string
	}
	cts := []ct{
		{"Plex-Current", "plexinc/pms-docker:latest", "app:plex",
			wave{base: 2.5, slow: 1, fast: 0.5, extra: func(t time.Time) float64 { return 12 * eveningMedia(t) }},
			wave{base: 900, slow: 30, fast: 5, extra: func(t time.Time) float64 { return 320 * eveningMedia(t) }}, 0, []string{"32400/tcp"}},
		{"radarr", "lscr.io/linuxserver/radarr:latest", "app:radarr", wave{base: 0.8, slow: 0.3, fast: 0.2}, wave{base: 310, slow: 12, fast: 2}, 0, []string{"7878/tcp"}},
		{"sonarr", "lscr.io/linuxserver/sonarr:latest", "app:sonarr", wave{base: 0.9, slow: 0.3, fast: 0.2}, wave{base: 360, slow: 14, fast: 2}, 0, []string{"8989/tcp"}},
		{"bazarr", "lscr.io/linuxserver/bazarr:latest", "app:bazarr", wave{base: 0.5, slow: 0.2, fast: 0.1}, wave{base: 240, slow: 10, fast: 2}, 0, []string{"6767/tcp"}},
		{"tautulli", "lscr.io/linuxserver/tautulli:latest", "app:tautulli", wave{base: 0.3, slow: 0.1, fast: 0.1}, wave{base: 130, slow: 5, fast: 1}, 0, []string{"8181/tcp"}},
		{"binhex-qbittorrentvpn", "binhex/arch-qbittorrentvpn:latest", "app:qbittorrent",
			wave{base: 5, amp: 3, peak: 1, slow: 1.5, fast: 0.8}, wave{base: 1100, amp: 60, peak: 1, slow: 35, fast: 6}, 4096, []string{"8080/tcp", "6881/tcp"}},
		{"binhex-prowlarr", "binhex/arch-prowlarr:latest", "app:prowlarr", wave{base: 0.4, slow: 0.2, fast: 0.1}, wave{base: 210, slow: 8, fast: 2}, 0, []string{"9696/tcp"}},
		{"mealie", "ghcr.io/mealie-recipes/mealie:latest", "app:mealie", wave{base: 0.2, slow: 0.1, fast: 0.05}, wave{base: 390, slow: 10, fast: 2}, 0, []string{"9925/tcp"}},
		{"immich", "ghcr.io/imagegenius/immich:latest", "app:immich",
			wave{base: 3, amp: 4, peak: 20, slow: 2.5, fast: 0.8}, wave{base: 2300, amp: 300, peak: 20, slow: 80, fast: 10}, 0, []string{"8080/tcp"}},
		{"pbs", "ayufan/proxmox-backup-server:latest", "app:pbs",
			wave{base: 0.6, slow: 0.2, fast: 0.1, extra: func(t time.Time) float64 { return 26 * nightlyBackups(t) }},
			wave{base: 620, slow: 15, fast: 3, extra: func(t time.Time) float64 { return 260 * nightlyBackups(t) }}, 0, []string{"8007/tcp"}},
		{"socket-proxy", "lscr.io/linuxserver/socket-proxy:latest", "", wave{base: 0.1, fast: 0.05}, wave{base: 9, fast: 0.5}, 0, []string{"2375/tcp"}},
	}
	for _, c := range cts {
		id := eng + "/" + c.name
		started := maxTime(engStart.Add(time.Minute), b.ago(id, 24, 24*21))
		attrs := map[string]any{"image": c.image, "container_id": containerID(b.rng, id, 0), "started_at": started,
			"restart_policy": "unless-stopped", "restart_count": 0, "ports": c.ports}
		if c.memLimit > 0 {
			attrs["mem_limit_mib"] = c.memLimit
		}
		b.add(&Resource{ID: id, Kind: KindContainer, Name: c.name, Source: "integration:docker-jupiter", State: StateRunning, Desired: StateRunning,
			Labels: map[string]string{"net.unraid.docker.managed": "dockerman"},
			Attrs:  attrs, Caps: []string{"lifecycle", "logs", "metrics"}})
		b.rel(id, RelRunsOn, eng)
		b.rel(id, RelStoresOn, "unraid:jupiter/share/appdata")
		if c.app != "" {
			b.rel(c.app, RelRunsOn, id)
		}
		src := "integration:docker-jupiter"
		b.metric(pct(id, "cpu.pct", src, [2]float64{0, max(5, c.cpu.base*3)}, 0, 0, c.cpu))
		maxMem := 16384.0
		if c.memLimit > 0 {
			maxMem = 6144 // room for the raised limit
		}
		b.metric(&metricSpec{res: id, key: "mem.mib", unit: "MiB", min: 0, max: maxMem, band: [2]float64{c.mem.base * 0.7, c.mem.base * 1.4}, dec: 0, source: src, fn: c.mem.fn()})
	}
	b.tp.res[eng+"/binhex-qbittorrentvpn"].Attrs["vpn"] = "wireguard"

	// Apps on jupiter.
	b.app("app:plex", "Plex", "Media", "https://plex.lab.example", "media.sessions", "media.library").Attrs = map[string]any{"movies": 1240, "shows": 318}
	b.app("app:tautulli", "Tautulli", "Media", "https://jupiter.lab.example/tautulli", "media.sessions")
	sonarr := b.app("app:sonarr", "Sonarr", "Media", "https://jupiter.lab.example/sonarr", "media.calendar", "media.library")
	sonarr.Attrs = map[string]any{"calendar": []CalendarItem{
		{At: b.start.Add(28 * time.Hour), Title: "Black Clover", Detail: "new episode"},
		{At: b.start.Add(34 * time.Hour), Title: "Ranma ½ (2024)", Detail: "new episode"},
		{At: b.start.Add(3*24*time.Hour + 20*time.Hour), Title: "4 more episodes", Detail: "this week"},
	}}
	radarr := b.app("app:radarr", "Radarr", "Media", "https://jupiter.lab.example/radarr", "media.calendar", "media.library")
	radarr.Attrs = map[string]any{"calendar": []CalendarItem{{At: b.start.Add(6*24*time.Hour + 8*time.Hour), Title: "1 movie release", Detail: "digital release"}}}
	b.app("app:bazarr", "Bazarr", "Media", "https://jupiter.lab.example/bazarr", "media.library")
	b.app("app:prowlarr", "Prowlarr", "Downloads", "https://jupiter.lab.example/prowlarr", "downloads.indexers")
	b.app("app:qbittorrent", "qBittorrent", "Downloads", "https://torrents.lab.example", "downloads.queue")
	b.app("app:mealie", "Mealie", "Apps", "https://food.family.example").Attrs = map[string]any{"recipes": 87, "categories": 8, "tags": 240}
	b.app("app:immich", "Immich", "Apps", "https://photos.family.example").Attrs = map[string]any{"users": 2}
	b.app("app:pbs", "Proxmox Backup Server", "Backup", "https://10.0.11.120:8007", "backups.jobs", "backups.snapshots", "backups.targets")

	for _, d := range []struct{ from, to string }{
		{"app:sonarr", "app:qbittorrent"}, {"app:sonarr", "app:prowlarr"},
		{"app:radarr", "app:qbittorrent"}, {"app:radarr", "app:prowlarr"},
		{"app:bazarr", "app:sonarr"}, {"app:bazarr", "app:radarr"},
		{"app:tautulli", "app:plex"},
	} {
		b.rel(d.from, RelDependsOn, d.to)
	}
	for _, s := range []struct{ app, share string }{
		{"app:plex", "media"}, {"app:sonarr", "media"}, {"app:radarr", "media"}, {"app:bazarr", "media"},
		{"app:qbittorrent", "media"}, {"app:immich", "photos"},
	} {
		b.rel(s.app, RelStoresOn, "unraid:jupiter/share/"+s.share)
	}

	for app, ms := range map[string]float64{"app:plex": 45, "app:tautulli": 60, "app:sonarr": 55, "app:radarr": 52, "app:bazarr": 70,
		"app:prowlarr": 48, "app:qbittorrent": 40, "app:mealie": 65, "app:immich": 80, "app:pbs": 90} {
		b.check(app, ms)
	}

	// App statistics, read through each app's integration.
	b.count("app:plex", "streams", "integration:plex", 0, 10, [2]float64{0, 3}, func(e *eval, m *metricSpec, t time.Time) float64 {
		return 2.2*eveningMedia(t) + 0.6*e.s.rng.smooth(m.hk, t, 25*time.Minute) - 0.3
	})
	b.count("app:sonarr", "queue", "integration:sonarr", 0, 200, [2]float64{0, 15}, wave{base: 9, amp: 4, peak: 14, slow: 0.8}.fn())
	b.count("app:sonarr", "wanted", "integration:sonarr", 0, 5000, [2]float64{0, 40}, wave{base: 25, slow: 0.7}.fn())
	b.count("app:radarr", "wanted", "integration:radarr", 0, 5000, [2]float64{0, 20}, wave{base: 10, slow: 0.6}.fn())
	b.count("app:radarr", "queued", "integration:radarr", 0, 200, [2]float64{0, 5}, wave{base: 0.2, slow: 0.6}.fn())
	b.count("app:bazarr", "missing.episodes", "integration:bazarr", 0, 100000, [2]float64{0, 1000}, func(e *eval, m *metricSpec, t time.Time) float64 {
		return 798.4 - 0.238*hours(t.Sub(b.start)) + 0.4*e.s.rng.smooth(m.hk, t, 2*time.Hour) // down ~40 a week
	})
	b.count("app:bazarr", "missing.movies", "integration:bazarr", 0, 10000, [2]float64{0, 20}, wave{base: 6, slow: 0.3}.fn())
	b.count("app:prowlarr", "grabs", "integration:prowlarr", 0, 1e7, [2]float64{0, 2000}, func(e *eval, m *metricSpec, t time.Time) float64 {
		return float64(int64(1100 + 1.14*hours(t.Sub(b.start)))) // a running total
	})
	b.count("app:prowlarr", "queries.24h", "integration:prowlarr", 0, 1e5, [2]float64{20, 80}, wave{base: 47, amp: 6, peak: 2, slow: 2}.fn())
	b.count("app:prowlarr", "failures.24h", "integration:prowlarr", 0, 1e4, [2]float64{0, 2}, wave{base: 0.1, slow: 0.3}.fn())
	b.metric(&metricSpec{res: "app:qbittorrent", key: "dl.mbps", unit: "Mb/s", min: 0, max: 1000, band: [2]float64{0, 60}, dec: 1, source: "integration:qbittorrent",
		fn: func(e *eval, m *metricSpec, t time.Time) float64 {
			return 22*bump(t, 1.5, 4.5) + 6*e.s.rng.smooth(m.hk, t, 15*time.Minute) - 2
		}})
	b.metric(&metricSpec{res: "app:qbittorrent", key: "ul.mbps", unit: "Mb/s", min: 0, max: 1000, band: [2]float64{0, 20}, dec: 1, source: "integration:qbittorrent",
		fn: wave{base: 3, amp: 4, peak: 22, slow: 1.5, fast: 0.4}.fn()})
	b.count("app:qbittorrent", "leeching", "integration:qbittorrent", 0, 1000, [2]float64{0, 30}, wave{base: 20, slow: 1.2}.fn())
	b.count("app:qbittorrent", "seeding", "integration:qbittorrent", 0, 10000, [2]float64{50, 100}, wave{base: 78, slow: 0.8}.fn())
	b.count("app:immich", "photos", "integration:immich", 0, 1e7, [2]float64{0, 1e6}, func(e *eval, m *metricSpec, t time.Time) float64 {
		return float64(int64(44040 + 4.26*hours(t.Sub(b.start))))
	})
	b.count("app:immich", "videos", "integration:immich", 0, 1e6, [2]float64{0, 1e5}, func(e *eval, m *metricSpec, t time.Time) float64 {
		return float64(int64(2401 + 0.3*hours(t.Sub(b.start))))
	})
	b.metric(&metricSpec{res: "app:immich", key: "storage.gib", unit: "GiB", min: 0, max: 1e6, band: [2]float64{0, 1000}, dec: 1, source: "integration:immich",
		fn: func(e *eval, m *metricSpec, t time.Time) float64 { return 272.3 + 0.03*hours(t.Sub(b.start)) }})
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

// ── pvedocker (Docker in an LXC on mars-1) ───────────────────────────────

func (b *builder) pvedocker() {
	eng := "docker:pvedocker"
	lxc := "pve:mars-1/lxc/110"
	engStart := b.tp.res[lxc].Attrs["started_at"].(time.Time).Add(40 * time.Second)
	b.add(&Resource{ID: eng, Kind: KindEngine, Name: "pvedocker", Source: "integration:docker-pvedocker", State: StateRunning,
		Attrs: map[string]any{"endpoint": "tcp://dockerproxy:2375", "via": "socket-proxy", "manager": "compose", "started_at": engStart},
		Caps:  []string{"containers", "stacks"}})
	b.rel(eng, RelRunsOn, lxc)

	type ct struct {
		name, image, project, app string
		cpu, mem                  wave
		ports                     []string
	}
	cts := []ct{
		{"chadash", "ghcr.io/ban-red/chadash:0.1", "chadash", "app:chadash", wave{base: 0.8, slow: 0.3, fast: 0.2}, wave{base: 62, slow: 3, fast: 0.5}, []string{"3696/tcp"}},
		{"dockerproxy", "wollomatic/socket-proxy:1", "proxy", "", wave{base: 0.1, fast: 0.05}, wave{base: 11, fast: 0.5}, []string{"2375/tcp"}},
		{"traefik", "traefik:v3", "proxy", "app:traefik", wave{base: 0.9, amp: 0.6, peak: 21, slow: 0.3, fast: 0.2}, wave{base: 85, slow: 4, fast: 1}, []string{"80/tcp", "443/tcp"}},
		{"uptimekuma", "louislam/uptime-kuma:2", "uptimekuma", "app:uptimekuma", wave{base: 1.6, slow: 0.4, fast: 0.3}, wave{base: 160, slow: 6, fast: 1}, []string{"3001/tcp"}},
		{"linkding", "sissbruecker/linkding:latest", "linkding", "app:linkding", wave{base: 0.3, slow: 0.1, fast: 0.05}, wave{base: 95, slow: 3, fast: 0.5}, []string{"9090/tcp"}},
		{"karakeep-web-1", "ghcr.io/karakeep-app/karakeep:0.30.2", "karakeep", "app:karakeep", wave{base: 0.8, slow: 0.3, fast: 0.2}, wave{base: 340, slow: 12, fast: 2}, []string{"3000/tcp"}},
		{"karakeep-chrome-1", "gcr.io/zenika-hub/alpine-chrome:124", "karakeep", "", wave{base: 1.2, slow: 0.5, fast: 0.3}, wave{base: 410, slow: 20, fast: 3}, nil},
		{"karakeep-meilisearch-1", "getmeili/meilisearch:v1", "karakeep", "", wave{base: 0.4, slow: 0.2, fast: 0.1}, wave{base: 260, slow: 8, fast: 1}, nil},
	}
	for _, c := range cts {
		id := eng + "/" + c.name
		started := maxTime(engStart.Add(30*time.Second), b.ago(id, 24*2, 24*30))
		attrs := map[string]any{"image": c.image, "container_id": containerID(b.rng, id, 0), "started_at": started,
			"restart_policy": "unless-stopped", "restart_count": 0}
		if c.ports != nil {
			attrs["ports"] = c.ports
		}
		b.add(&Resource{ID: id, Kind: KindContainer, Name: c.name, Source: "integration:docker-pvedocker", State: StateRunning, Desired: StateRunning,
			Labels: map[string]string{"com.docker.compose.project": c.project},
			Attrs:  attrs, Caps: []string{"lifecycle", "logs", "metrics"}})
		b.rel(id, RelRunsOn, eng)
		if c.app != "" {
			b.rel(c.app, RelRunsOn, id)
		}
		src := "integration:docker-pvedocker"
		b.metric(pct(id, "cpu.pct", src, [2]float64{0, max(5, c.cpu.base*3)}, 0, 0, c.cpu))
		b.metric(&metricSpec{res: id, key: "mem.mib", unit: "MiB", min: 0, max: 8192, band: [2]float64{c.mem.base * 0.7, c.mem.base * 1.4}, dec: 0, source: src, fn: c.mem.fn()})
	}
	b.rel(eng+"/karakeep-web-1", RelDependsOn, eng+"/karakeep-chrome-1")
	b.rel(eng+"/karakeep-web-1", RelDependsOn, eng+"/karakeep-meilisearch-1")

	self := b.app("app:chadash", "Cha Dash", "Services", "https://dash.lab.example")
	self.Attrs = map[string]any{"self": true, "version": "0.1.0-dev"}
	tr := b.app("app:traefik", "Traefik", "Services", "https://traefik.lab.example", "proxy.routes")
	tr.Attrs = map[string]any{"routers": 61, "services": 56, "middlewares": 7}
	uk := b.app("app:uptimekuma", "Uptime Kuma", "Services", "https://uptime.lab.example", "checks.uptime")
	uk.Attrs = map[string]any{"status_page": "current", "monitors": len(kumaMonitors)}
	ld := b.app("app:linkding", "Linkding", "Archive", "https://links.lab.example")
	ld.Attrs = map[string]any{"description": "Shared bookmarks"}
	kk := b.app("app:karakeep", "Karakeep", "Archive", "https://hoarder.lab.example")
	kk.Attrs = map[string]any{"version": "0.30.2", "description": "Long term archived bookmarks/websites"}

	for app, ms := range map[string]float64{"app:chadash": 8, "app:traefik": 12, "app:uptimekuma": 30, "app:linkding": 38, "app:karakeep": 120} {
		b.check(app, ms)
	}
	b.count("app:karakeep", "bookmarks", "integration:karakeep", 0, 1e6, [2]float64{0, 1e5}, func(e *eval, m *metricSpec, t time.Time) float64 {
		return float64(int64(1201 + 0.2*hours(t.Sub(b.start))))
	})
	b.count("app:uptimekuma", "monitors.up", "integration:uptimekuma", 0, float64(len(kumaMonitors)), [2]float64{float64(len(kumaMonitors)), float64(len(kumaMonitors))},
		func(e *eval, m *metricSpec, t time.Time) float64 { return float64(len(kumaMonitors) - kumaDown(e.w)) })
	b.count("app:uptimekuma", "monitors.down", "integration:uptimekuma", 0, float64(len(kumaMonitors)), [2]float64{0, 0},
		func(e *eval, m *metricSpec, t time.Time) float64 { return float64(kumaDown(e.w)) })
}

// kumaMonitors are the sites Uptime Kuma watches.
var kumaMonitors = []string{"app:homeassistant", "net:opnsense", "app:plex", "app:immich", "app:mealie", "app:karakeep", "app:linkding", "app:qbittorrent", "app:cockpit"}

func kumaDown(w *world) int {
	n := 0
	for _, id := range kumaMonitors {
		r, ok := w.res[id]
		if !ok || (r.State != StateRunning && r.State != StateOnline) {
			n++
		}
	}
	return n
}

// ── backups ──────────────────────────────────────────────────────────────

func (b *builder) backups() {
	b.add(&Resource{ID: "pbs:main", Kind: KindBackupTarget, Name: "main", Source: "integration:pbs", State: StateOnline,
		Labels: map[string]string{"chadash.group": "Backup"},
		Attrs: map[string]any{"capacity_tb": 2.0, "retention": "keep-daily 7 · weekly 4 · monthly 6", "prune_job": "disabled",
			"prune_job_disabled_at": pruneDisabledAt, "snapshots_beyond_policy": 38, "gc_last": b.start.Add(-19*24*time.Hour + 5*time.Hour)},
		Links:  []Link{{Title: "PBS", URL: "https://10.0.11.120:8007/#DataStore-main"}},
		Caps:   []string{"backups.targets"},
		Health: Health{Status: HealthOK, Since: b.start.Add(-30 * 24 * time.Hour)}})
	b.rel("pbs:main", RelPartOf, "app:pbs")
	b.rel("pbs:main", RelStoresOn, "unraid:jupiter/pool/backups")
	b.metric(pct("pbs:main", "used.pct", "integration:pbs", [2]float64{60, 88}, 90, 96, wave{base: 86.6, slow: 0.02, fast: 0.01}))

	for _, j := range backupJobs {
		b.add(&Resource{ID: j.id, Kind: KindBackupJob, Name: "daily", Source: j.source, State: StateIdle,
			Attrs:  map[string]any{"schedule": fmt.Sprintf("daily %02d:%02d", 2, int(j.offset/time.Minute)), "mode": "snapshot", "storage": "pbs-jupiter"},
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
}

var backupJobs = []backupJob{
	{"pve:pluto/backup/daily", "integration:proxmox-pluto", 0},
	{"pve:minig5/backup/daily", "integration:proxmox-minig5", 10 * time.Minute},
	{"pve:mars/backup/daily", "integration:proxmox-mars", 20 * time.Minute},
	{"pve:io/backup/daily", "integration:proxmox-io", 30 * time.Minute},
	{"pve:jupiter-nas/backup/daily", "integration:proxmox-jupiter-nas", 40 * time.Minute},
}

// backupSlot is one guest's place in the nightly run, relative to 02:00 UTC.
type backupSlot struct {
	job, guest string
	start, dur time.Duration
}

// backupSlots: each PVE instance has its own job; guests run one after
// another. win-gaming is stopped, so it has no dirty bitmap and every backup
// reads its whole disk, which is why it takes 22 minutes.
var backupSlots = []backupSlot{
	{"pve:pluto/backup/daily", "pve:pluto/qemu/100", 0, 3 * time.Minute},
	{"pve:pluto/backup/daily", "pve:pluto/lxc/101", 3 * time.Minute, time.Minute},
	{"pve:minig5/backup/daily", "pve:minig5/qemu/100", 10 * time.Minute, 4 * time.Minute},
	{"pve:minig5/backup/daily", "pve:minig5/lxc/101", 14 * time.Minute, 3 * time.Minute},
	{"pve:mars/backup/daily", "pve:mars-1/lxc/110", 20 * time.Minute, 7 * time.Minute},
	{"pve:io/backup/daily", "pve:io/qemu/201", 30 * time.Minute, 22 * time.Minute},
	{"pve:io/backup/daily", "pve:io/qemu/202", 52 * time.Minute, 6 * time.Minute},
	{"pve:io/backup/daily", "pve:io/qemu/203", 58 * time.Minute, 3 * time.Minute},
	{"pve:jupiter-nas/backup/daily", "pve:jupiter-nas/lxc/100", 40 * time.Minute, 4 * time.Minute},
}

// ── Traefik routes ───────────────────────────────────────────────────────

func (b *builder) routes() {
	for _, r := range []struct{ name, rule, upstream, to string }{
		{"home", "Host(`home.lab.example`)", "http://10.0.11.203:8123", "app:homeassistant"},
		{"opnsense", "Host(`opnsense.lab.example`)", "https://10.0.11.1", "net:opnsense"},
		{"traefik", "Host(`traefik.lab.example`)", "api@internal", "app:traefik"},
		{"uptime", "Host(`uptime.lab.example`)", "http://uptimekuma:3001", "app:uptimekuma"},
		{"dash", "Host(`dash.lab.example`)", "http://chadash:3696", "app:chadash"},
		{"links", "Host(`links.lab.example`)", "http://linkding:9090", "app:linkding"},
		{"hoarder", "Host(`hoarder.lab.example`)", "http://karakeep-web-1:3000", "app:karakeep"},
		{"plex", "Host(`plex.lab.example`)", "http://10.0.11.120:32400", "app:plex"},
		{"jupiter", "Host(`jupiter.lab.example`)", "http://10.0.11.120", "unraid:jupiter"},
		{"radarr", "Host(`jupiter.lab.example`) && PathPrefix(`/radarr`)", "http://10.0.11.120:7878", "app:radarr"},
		{"sonarr", "Host(`jupiter.lab.example`) && PathPrefix(`/sonarr`)", "http://10.0.11.120:8989", "app:sonarr"},
		{"bazarr", "Host(`jupiter.lab.example`) && PathPrefix(`/bazarr`)", "http://10.0.11.120:6767", "app:bazarr"},
		{"tautulli", "Host(`jupiter.lab.example`) && PathPrefix(`/tautulli`)", "http://10.0.11.120:8181", "app:tautulli"},
		{"prowlarr", "Host(`jupiter.lab.example`) && PathPrefix(`/prowlarr`)", "http://10.0.11.120:9696", "app:prowlarr"},
		{"torrents", "Host(`torrents.lab.example`)", "http://10.0.11.120:8080", "app:qbittorrent"},
		{"food", "Host(`food.family.example`)", "http://10.0.11.120:9925", "app:mealie"},
		{"photos", "Host(`photos.family.example`)", "http://10.0.11.120:2283", "app:immich"},
	} {
		id := "traefik:route/" + r.name
		b.add(&Resource{ID: id, Kind: KindRoute, Name: r.name, Source: "integration:traefik", State: StateEnabled,
			Attrs:  map[string]any{"rule": r.rule, "upstream": r.upstream, "entrypoint": "websecure", "tls": true},
			Health: Health{Status: HealthOK, Since: b.start.Add(-9 * 24 * time.Hour)}})
		b.rel(id, RelPartOf, "app:traefik")
		b.rel(id, RelRoutesTo, r.to)
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
			r.Source = "check" // apps seen only by built-in checks: Linkding, Cockpit, Cha Dash
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
