package proxmox

import (
	"cmp"
	"fmt"
	"math"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"dash.cha.sh/internal/integration"
)

// Thresholds (spike 4.1's health table and the Demo Home's series).
const (
	memWarn, memCrit         = 90, 98
	rootfsWarn, rootfsCrit   = 90, 95
	storageWarn, storageCrit = 85, 95
	lxcDiskWarn, lxcDiskCrit = 90, 95
	nodeStatusMaxAge         = 3 * slowEvery // older node status reads as unknown
)

// builder turns one sync's reads into facts and incidents.
type builder struct {
	in      *instance
	now     time.Time
	self    string
	pre     string // "proxmox:<instance id>/"
	cluster string // the cluster's name, "" when standalone

	facts  integration.Facts
	raise  []integration.Incident
	known  []string            // incident IDs evaluated this sync (resolved unless raised)
	guests map[int]*guestFacts // vmid → guest
	jobsOf map[int][]string    // vmid → backup job resource IDs
	stores map[string]string   // storage id → resource ID (shared) ; "node/id" → local
	nodes  map[string]bool     // node names
}

type guestFacts struct {
	r    resource
	id   string
	name string
	cfg  *guestConfig
}

// publish emits everything the instance knows, raises and resolves its
// incidents, and reports the version and warnings.
func (in *instance) publish(res []resource, now time.Time) error {
	b := &builder{in: in, now: now, self: in.d.Emit.Self(), pre: ID + ":" + in.key + "/",
		guests: map[int]*guestFacts{}, jobsOf: map[int][]string{}, stores: map[string]string{},
		nodes: map[string]bool{}}
	b.clusterRes()
	b.hosts(res)
	b.storages(res)
	b.guestRes(res)
	b.hubGuest()
	b.backups(res)
	b.hints(res)
	if err := in.d.Emit.Replace("", b.facts); err != nil {
		return err
	}
	raised := map[string]bool{}
	for _, inc := range b.raise {
		if err := in.d.Emit.Raise(inc); err != nil {
			return err
		}
		raised[inc.ID] = true
	}
	resolve := func(id string) error {
		if raised[id] {
			return nil
		}
		return in.d.Emit.Resolve(id, resolution(id, now))
	}
	for _, id := range b.known {
		if err := resolve(id); err != nil {
			return err
		}
	}
	for id := range in.raised {
		if err := resolve(id); err != nil {
			return err
		}
	}
	in.raised = raised
	in.d.Emit.SetVersion(in.version)
	in.d.Emit.SetWarnings(in.warnings())
	return nil
}

// resolution is the sentence a resolved incident carries.
func resolution(id string, now time.Time) string {
	at := hhmm(now)
	switch {
	case strings.HasPrefix(id, "backup."):
		return "The job's next run succeeded (seen at " + at + ")."
	case strings.HasPrefix(id, "guest."):
		return "The guest left its error state (seen at " + at + ")."
	case strings.HasPrefix(id, "node."):
		return "The node is back online (seen at " + at + ")."
	case id == "cluster.quorum":
		return "The cluster is quorate again (seen at " + at + ")."
	}
	return "No longer the case (seen at " + at + ")."
}

func hhmm(t time.Time) string { return t.Local().Format("15:04") }

func dayHHMM(t time.Time) string { return t.Local().Format("Jan 2 15:04") }

// warnings are the instance's sync-time warnings: parts the token can't
// read, reads that failed this time, write privileges, an untested version.
func (in *instance) warnings() []integration.Warning {
	var ws []integration.Warning
	hidden := map[string]struct{ code, what string }{
		"status":         {"cluster_status_hidden", "Cluster status and quorum"},
		"tasks":          {"tasks_hidden", "Recent tasks (and with them backup results)"},
		"backups":        {"backups_hidden", "Backup jobs and coverage"},
		"task-log":       {"backups_hidden", "Per-guest backup results (task logs)"},
		"node-status":    {"node_status_hidden", "Node details (memory available, swap, root disk, kernel)"},
		"storage-config": {"config_hidden", "Storage details (PBS and NFS servers)"},
		"guest-config":   {"config_hidden", "Guest configs (start at boot, disks, passthrough)"},
		"permissions":    {"permissions_hidden", "The token's permissions"},
		"updates":        {"updates_hidden", "Pending updates"},
	}
	parts := make([]string, 0, len(in.hidden))
	for part := range in.hidden {
		parts = append(parts, part)
	}
	slices.Sort(parts)
	for _, part := range parts {
		h := hidden[part]
		msg := h.what + " aren't shown: Proxmox refused the token"
		if r := in.hidden[part]; r != "" {
			msg += " (" + r + ")"
		}
		ws = append(ws, integration.Warning{Code: h.code, Severity: integration.WarnWarning, Message: msg + ". Grant the token PVEAuditor on /."})
	}
	ws = append(ws, in.notes...)
	if w := integration.WriteGrantWarning(in.perms.grants()); w != nil {
		ws = append(ws, *w)
	}
	if in.ad != nil && in.ad.newer {
		ws = append(ws, newerWarning(versionInfo{Version: in.version}))
	}
	return ws
}

func (b *builder) add(r integration.Resource) {
	b.facts.Resources = append(b.facts.Resources, r)
}

func (b *builder) rel(from, typ, to string) {
	b.facts.Relations = append(b.facts.Relations, integration.Relation{From: from, Type: typ, To: to})
}

func (b *builder) metric(m integration.Metric) {
	if !math.IsNaN(m.Value) {
		m.Value = math.Round(m.Value*10) / 10
	}
	b.facts.Metrics = append(b.facts.Metrics, m)
}

func pct(res, key string, v float64, band [2]float64, warn, crit float64) integration.Metric {
	return integration.Metric{Resource: res, Key: key, Unit: "%", Min: 0, Max: 100, Band: band, Warn: warn, Crit: crit, Value: v}
}

func ratio(a, b num) float64 {
	if b <= 0 {
		return integration.Unknown()
	}
	return math.Min(100, float64(a)/float64(b)*100)
}

func gib(b num) float64  { return math.Round(float64(b)/(1<<30)*10) / 10 }
func tb(b num) float64   { return math.Round(float64(b)/1e12*100) / 100 }
func secs(n num) float64 { return float64(n) }

// uiLink deep-links into the PVE GUI ("#v1:0:=qemu%2F103").
func (b *builder) uiLink(title, target string) []integration.Link {
	u := b.in.base + "/"
	if target != "" {
		u += "#v1:0:=" + url.PathEscape(target)
	}
	return []integration.Link{{Title: title, URL: u}}
}

func (b *builder) nodeID(n string) string { return b.pre + "node/" + n }

// ── cluster ─────────────────────────────────────────────────────────────

func (b *builder) clusterRes() {
	var c *statusEntry
	offline := []string{}
	for i, s := range b.in.status {
		switch s.Type {
		case "cluster":
			c = &b.in.status[i]
		case "node":
			if !s.Online.bool() {
				offline = append(offline, s.Name)
			}
		}
	}
	if c == nil {
		return
	}
	b.cluster = c.Name
	id := b.pre + "cluster"
	r := integration.Resource{ID: id, Kind: "cluster", Name: c.Name, State: "online",
		Attrs: map[string]any{"cluster": c.Name, "nodes": int(c.Nodes), "quorate": c.Quorate.bool(), "platform": "pve"},
		Links: b.uiLink("Proxmox", ""), Caps: []string{"compute.guests"},
		Health: integration.Health{Status: integration.HealthOK}}
	b.known = append(b.known, "cluster.quorum")
	switch {
	case !c.Quorate.bool():
		r.State, r.Health = "error", integration.Health{Status: integration.HealthDown, Reason: "quorum lost"}
		b.raise = append(b.raise, integration.Incident{
			ID: "cluster.quorum", Scenario: "cluster-quorum-lost", Class: integration.ClassProblem, Severity: integration.SeverityDown,
			Title:   "Cluster " + c.Name + " lost quorum",
			Summary: fmt.Sprintf("Cluster %s isn't quorate: %d of %d nodes answer. Guests keep running, but nothing can be started, changed or migrated until quorum returns.", c.Name, int(c.Nodes)-len(offline), int(c.Nodes)),
			Target:  id, Facts: map[string]any{"cluster": c.Name, "nodes": int(c.Nodes), "offline": offline},
			Suggested:    integration.Action{ID: "link.open", Label: "Open Proxmox", Tier: "T0", Target: id},
			ResolvesWhen: "Resolves when the cluster is quorate again.",
		})
	case len(offline) > 0:
		r.Health = integration.Health{Status: integration.HealthAttention, Reason: strings.Join(offline, ", ") + " offline"}
	}
	b.add(r)
	b.rel(b.self, "monitors", id)
}

// ── hosts ───────────────────────────────────────────────────────────────

func (b *builder) hosts(res []resource) {
	rows := map[string]resource{}
	for _, r := range res {
		if r.Type == "node" {
			rows[r.Node] = r
		}
	}
	ips := map[string]string{}
	for _, s := range b.in.status {
		if s.Type == "node" {
			ips[s.Name] = s.IP
		}
	}
	for _, n := range b.in.nodeNames(res) {
		b.nodes[n] = true
		row, haveRow := rows[n]
		id := b.nodeID(n)
		online := b.in.nodeOnline(n, res)
		r := integration.Resource{ID: id, Kind: "host", Name: n, State: "online",
			Attrs: map[string]any{"platform": "pve", "node": n},
			Links: b.uiLink("Proxmox", "node/"+n), Caps: []string{"host.metrics", "compute.guests"},
			Health: integration.Health{Status: integration.HealthOK}}
		if ip := ips[n]; ip != "" {
			r.Attrs["ip"] = ip
		}
		if b.cluster != "" {
			r.Attrs["cluster"] = b.cluster
		}
		if haveRow {
			r.Attrs["cores"] = int(row.MaxCPU)
			r.Attrs["mem_gb"] = gib(row.MaxMem)
			if row.Uptime > 0 && online {
				r.Attrs["boot_at"] = b.now.Add(-time.Duration(secs(row.Uptime)) * time.Second).UTC().Truncate(time.Minute)
			}
		}
		if u, ok := b.in.updates[n]; ok {
			r.Attrs["updates"] = u
		}
		ns, haveNS := b.in.nodeStat[n]
		fresh := haveNS && b.now.Sub(ns.at) <= nodeStatusMaxAge
		if haveNS {
			if v := strings.Split(ns.st.PVEVersion, "/"); len(v) >= 2 {
				r.Attrs["pve_version"] = v[1]
			}
			if ns.st.CurrentKernel.Release != "" {
				r.Attrs["kernel"] = ns.st.CurrentKernel.Release
			}
			if ns.st.CPUInfo.Model != "" {
				r.Attrs["cpu_model"] = ns.st.CPUInfo.Model
			}
			r.Attrs["swap_gb"] = gib(ns.st.Swap.Total)
		}

		mem := integration.Unknown()
		cpu := integration.Unknown()
		if haveRow && online {
			cpu = float64(row.CPU) * 100
			mem = ratio(row.Mem, row.MaxMem)
		}
		if online && fresh && b.in.ad != nil && b.in.ad.memAvailable && ns.st.Memory.Available != nil && ns.st.Memory.Total > 0 {
			mem = ratio(ns.st.Memory.Total-*ns.st.Memory.Available, ns.st.Memory.Total)
		}
		b.metric(pct(id, "cpu.pct", cpu, [2]float64{0, 40}, 0, 0))
		b.metric(pct(id, "mem.pct", mem, [2]float64{20, 80}, memWarn, memCrit))
		var reasons []string
		if haveNS {
			swap, rootfs := integration.Unknown(), integration.Unknown()
			if online && fresh {
				rootfs = ratio(ns.st.RootFS.Used, ns.st.RootFS.Total)
				if ns.st.Swap.Total > 0 {
					swap = ratio(ns.st.Swap.Used, ns.st.Swap.Total)
				}
			}
			if ns.st.Swap.Total > 0 {
				b.metric(pct(id, "swap.pct", swap, [2]float64{0, 25}, 50, 90))
			}
			b.metric(pct(id, "rootfs.pct", rootfs, [2]float64{0, 70}, rootfsWarn, rootfsCrit))
			if rootfs >= rootfsWarn {
				reasons = append(reasons, fmt.Sprintf("root disk %.0f%%", rootfs))
			}
		}
		if mem >= memWarn {
			m := fmt.Sprintf("memory %.0f%%", mem)
			if haveNS && ns.st.Swap.Total == 0 {
				m += " · no swap"
			}
			reasons = append([]string{m}, reasons...)
		}
		b.known = append(b.known, "node."+n)
		switch {
		case !online:
			r.State, r.Health = "offline", integration.Health{Status: integration.HealthDown, Reason: "offline"}
			if b.cluster != "" { // a standalone node that's offline can't be read at all: the sync fails instead
				b.raise = append(b.raise, integration.Incident{
					ID: "node." + n, Scenario: "host-offline", Class: integration.ClassProblem, Severity: integration.SeverityDown,
					Title:   n + " is offline",
					Summary: fmt.Sprintf("%s doesn't answer cluster %s. Its guests are unreachable unless HA moves them.", n, b.cluster),
					Target:  id, Facts: map[string]any{"node": n, "cluster": b.cluster},
					Suggested:    integration.Action{ID: "link.open", Label: "Open Proxmox", Tier: "T0", Target: id},
					ResolvesWhen: "Resolves when " + n + " is back online.",
				})
			}
		case len(reasons) > 0:
			r.Health = integration.Health{Status: integration.HealthAttention, Reason: strings.Join(reasons, " · ")}
		}
		b.add(r)
		if b.cluster != "" {
			b.rel(id, "part_of", b.pre+"cluster")
		}
		b.rel(b.self, "monitors", id)
	}
}

// ── storage ─────────────────────────────────────────────────────────────

func (b *builder) storages(res []resource) {
	type agg struct {
		row   resource
		nodes []string
		avail bool
	}
	shared := map[string]*agg{}
	var sharedOrder []string
	for _, r := range res {
		if r.Type != "storage" {
			continue
		}
		if r.Shared.bool() {
			a := shared[r.Storage]
			if a == nil {
				a = &agg{row: r}
				shared[r.Storage] = a
				sharedOrder = append(sharedOrder, r.Storage)
			}
			a.nodes = append(a.nodes, r.Node)
			if r.Status == "available" && !a.avail {
				a.row, a.avail = r, true
			}
			continue
		}
		id := b.nodeID(r.Node) + "/storage/" + r.Storage
		b.stores[r.Node+"/"+r.Storage] = id
		b.storage(id, r, []string{r.Node}, false)
		b.rel(id, "part_of", b.nodeID(r.Node))
	}
	for _, s := range sharedOrder {
		a := shared[s]
		id := b.pre + "storage/" + s
		b.stores[s] = id
		slices.Sort(a.nodes)
		b.storage(id, a.row, a.nodes, true)
		if b.cluster != "" {
			b.rel(id, "part_of", b.pre+"cluster")
		} else {
			for _, n := range a.nodes {
				b.rel(id, "part_of", b.nodeID(n))
			}
		}
	}
}

func (b *builder) storage(id string, row resource, nodes []string, shared bool) {
	r := integration.Resource{ID: id, Kind: "storage", Name: row.Storage, State: "online",
		Attrs:  map[string]any{"storage": row.Storage, "type": row.PluginType, "shared": shared, "size_tb": tb(row.MaxDisk)},
		Health: integration.Health{Status: integration.HealthOK}}
	if row.Content != "" {
		r.Attrs["content"] = strings.Split(row.Content, ",")
	}
	if shared {
		r.Attrs["nodes"] = nodes
	} else {
		r.Attrs["node"] = row.Node
	}
	cfg, haveCfg := b.in.storCfg[row.Storage]
	if haveCfg {
		switch cfg.Type {
		case "pbs":
			r.Attrs["pbs.server"] = cfg.Server
			r.Attrs["pbs.datastore"] = cfg.Datastore
			if cfg.Fingerprint != "" {
				r.Attrs["pbs.fingerprint"] = strings.ToLower(cfg.Fingerprint)
			}
		case "nfs", "cifs":
			r.Attrs[cfg.Type+".server"] = cfg.Server
			if cfg.Export != "" {
				r.Attrs["nfs.export"] = cfg.Export
			}
		}
	}
	used := integration.Unknown()
	switch {
	case haveCfg && cfg.Disable.bool():
		r.State, r.Health = "disabled", integration.Health{Status: integration.HealthOK, Reason: "disabled"}
	case row.Status != "available":
		r.State, r.Health = "offline", integration.Health{Status: integration.HealthAttention, Reason: "not active (" + cmp.Or(row.Status, "unknown") + ")"}
	default:
		used = ratio(row.Disk, row.MaxDisk)
		if used >= storageWarn {
			r.Health = integration.Health{Status: integration.HealthAttention, Reason: fmt.Sprintf("%.0f%% used", used)}
		}
	}
	b.add(r)
	b.metric(pct(id, "used.pct", used, [2]float64{0, 80}, storageWarn, storageCrit))
}

// storageID resolves a storage name as seen from a node.
func (b *builder) storageID(node, name string) string {
	if id, ok := b.stores[node+"/"+name]; ok {
		return id
	}
	return b.stores[name]
}

// ── guests ──────────────────────────────────────────────────────────────

func (b *builder) guestRes(res []resource) {
	for _, g := range res {
		if (g.Type != "qemu" && g.Type != "lxc") || g.Template.bool() {
			continue
		}
		vmid := int(g.VMID)
		key := g.guestKey()
		id := b.pre + key
		kind := "vm"
		if g.Type == "lxc" {
			kind = "lxc"
		}
		name := cmp.Or(g.Name, strconv.Itoa(vmid))
		cfg := b.in.guestCfg[key]
		b.guests[vmid] = &guestFacts{r: g, id: id, name: name, cfg: cfg}
		r := integration.Resource{ID: id, Kind: kind, Name: name,
			Attrs: map[string]any{"vmid": vmid, "node": g.Node, "type": g.Type, "cores": int(g.MaxCPU), "mem_gb": gib(g.MaxMem)},
			Links: b.uiLink("Proxmox", key)}
		if b.cluster != "" {
			r.Attrs["cluster"] = b.cluster
		}
		if g.Tags != "" {
			r.Attrs["tags"] = strings.FieldsFunc(g.Tags, func(c rune) bool { return c == ';' || c == ',' || c == ' ' })
		}
		if g.HAState != "" {
			r.Attrs["hastate"] = g.HAState
		}
		if g.Lock != "" {
			r.Attrs["lock"] = g.Lock
		}
		if cfg != nil {
			r.Attrs["onboot"] = cfg.onboot
			if cfg.ostype != "" {
				r.Attrs["os"] = cfg.ostype
			}
			if cfg.mac != "" {
				r.Attrs["mac"] = cfg.mac
			}
			if len(cfg.hostpci) > 0 {
				r.Attrs["hostpci"] = cfg.hostpci
			}
			if cfg.onboot {
				r.Desired = "running"
			}
		}
		if a, ok := b.in.addrs[key]; ok {
			r.Attrs["ip"] = a.ip
			if a.mac != "" && r.Attrs["mac"] == nil {
				r.Attrs["mac"] = a.mac
			}
		}
		alive := g.Uptime > 0 && g.Status != "stopped"
		if alive {
			r.Attrs["started_at"] = b.now.Add(-time.Duration(secs(g.Uptime)) * time.Second).UTC().Truncate(time.Minute)
		}
		b.guestState(&r, g, cfg)
		b.add(r)
		b.rel(id, "runs_on", b.nodeID(g.Node))
		if cfg != nil {
			for _, s := range cfg.storages {
				if sid := b.storageID(g.Node, s); sid != "" {
					b.rel(id, "stores_on", sid)
				}
			}
		}
		if alive {
			b.metric(pct(id, "cpu.pct", float64(g.CPU)*100, [2]float64{0, 30}, 0, 0))
			mem := ratio(g.Mem, g.MaxMem) // clamped: on 9.x mem is host-side and can exceed maxmem
			b.metric(pct(id, "mem.pct", mem, [2]float64{10, 80}, memWarn, memCrit))
			if g.Type == "lxc" && g.MaxDisk > 0 {
				b.metric(pct(id, "disk.pct", ratio(g.Disk, g.MaxDisk), [2]float64{0, 80}, lxcDiskWarn, lxcDiskCrit))
			}
		}
	}
}

// guestState maps PVE's status to the Home Graph's state and health, and
// raises the guest's incident when it's broken.
func (b *builder) guestState(r *integration.Resource, g resource, cfg *guestConfig) {
	vmid := int(g.VMID)
	incID := "guest." + strconv.Itoa(vmid)
	b.known = append(b.known, incID)
	what := "VM"
	if g.Type == "lxc" {
		what = "CT"
	}
	r.Health = integration.Health{Status: integration.HealthOK}
	switch g.Status {
	case "running":
		r.State = "running"
	case "stopped":
		r.State = "stopped"
		switch {
		case cfg != nil && cfg.onboot:
			r.Health = integration.Health{Status: integration.HealthAttention, Reason: "stopped, but set to start at boot"}
		case cfg != nil:
			r.Health.Reason = "stopped (expected)"
		default:
			r.Health.Reason = "stopped"
		}
	case "paused", "suspended":
		r.State = g.Status
		r.Health = integration.Health{Status: integration.HealthAttention, Reason: g.Status}
	case "prelaunch":
		r.State = "starting"
	case "internal-error":
		r.State = "error"
		reason := "internal-error: its QEMU process stopped responding"
		if g.Mem > 0 {
			reason += fmt.Sprintf(", still holding %.1f GB", float64(g.Mem)/1e9)
		}
		r.Health = integration.Health{Status: integration.HealthDown, Reason: reason}
		r.Attrs["pve_status"] = g.Status
	default:
		r.State = cmp.Or(g.Status, "unknown")
		r.Health = integration.Health{Status: integration.HealthUnknown, Reason: "status " + r.State}
	}
	if g.HAState == "error" || g.HAState == "fence" {
		r.Health = integration.Health{Status: integration.HealthDown, Reason: "HA state " + g.HAState}
	}
	if r.Health.Status != integration.HealthDown {
		return
	}
	facts := map[string]any{"vmid": vmid, "node": g.Node, "status": g.Status}
	summary := fmt.Sprintf("%s %d (%s) on %s is in %s.", what, vmid, r.Name, g.Node, r.Health.Reason)
	if g.Status == "internal-error" && g.Mem > 0 {
		facts["mem_gb"] = math.Round(float64(g.Mem)/1e9*10) / 10
		summary = fmt.Sprintf("%s %d (%s) on %s is in internal-error: its QEMU process stopped responding, and it still holds %.1f GB of %s's memory. Stop it and start it again.",
			what, vmid, r.Name, g.Node, float64(g.Mem)/1e9, g.Node)
	}
	inc := integration.Incident{
		ID: incID, Scenario: "guest-error", Class: integration.ClassProblem, Severity: integration.SeverityDown,
		Title: fmt.Sprintf("%s is in %s", r.Name, cmp.Or(g.Status, "an error state")), Summary: summary,
		Target: r.ID, Facts: facts,
		Suggested: integration.Action{ID: "link.open", Label: "Open " + r.Name + " in Proxmox", Tier: "T0", Target: r.ID},
		Alternatives: []integration.Action{{ID: actStop, Label: "Hard stop " + r.Name, Tier: "T2", Target: r.ID,
			Detail: "Stops it at once, like pulling the plug, and frees what it holds. Start it again afterwards."}},
		ResolvesWhen: "Resolves when " + r.Name + " leaves its error state.",
	}
	if g.HAState == "error" || g.HAState == "fence" {
		inc.Title = fmt.Sprintf("%s's HA state is %s", r.Name, g.HAState)
	}
	// The newest task on this guest is often the story (a reboot that timed
	// out, then a reset).
	var last *task
	for i, t := range b.in.tasks {
		if t.ID == strconv.Itoa(vmid) && t.Type != "vzdump" && t.Type != "vncproxy" && (last == nil || t.StartTime > last.StartTime) {
			last = &b.in.tasks[i]
		}
	}
	if last != nil {
		res := "OK"
		if !last.ok() {
			res = cmp.Or(last.Status, "running")
		}
		at := time.Unix(last.StartTime, 0).UTC()
		inc.Causes = append(inc.Causes, integration.Cause{Text: fmt.Sprintf("The last task on it was %s at %s (%s).", last.Type, dayHHMM(at), res), Resource: r.ID, At: at})
	}
	b.raise = append(b.raise, inc)
}

// ── backups ─────────────────────────────────────────────────────────────

func (b *builder) backups(res []resource) {
	refs := guestRefs(res)
	type guestBackup struct {
		lastAt, lastOK time.Time
		status, err    string
	}
	gb := map[int]*guestBackup{}
	for _, j := range b.in.jobs {
		sel := j.selection(refs)
		runs := b.in.runs(j, sel)
		id := b.pre + "backup/" + j.ID
		name := cmp.Or(j.Comment, scheduleText(j.Schedule))
		r := integration.Resource{ID: id, Kind: "backup_job", Name: name, State: "idle",
			Attrs: map[string]any{"job": j.ID, "schedule": scheduleText(j.Schedule), "storage": j.Storage, "enabled": j.enabled()},
			Caps:  []string{"backups.jobs"}, Links: b.uiLink("Proxmox", ""),
			Health: integration.Health{Status: integration.HealthOK}}
		if j.Mode != "" {
			r.Attrs["mode"] = j.Mode
		}
		if j.Node != "" {
			r.Attrs["node"] = j.Node
		}
		switch {
		case j.All.bool():
			r.Attrs["selection"] = "all"
		case j.Pool != "":
			r.Attrs["selection"] = "pool " + j.Pool
		default:
			r.Attrs["selection"] = sel
		}
		if j.NextRun > 0 && j.enabled() {
			r.Attrs["next_run"] = time.Unix(j.NextRun, 0).UTC()
		}

		var last *run
		var lastOK time.Time
		failed24 := 0
		for i := range runs {
			ru := &runs[i]
			if ru.running {
				r.State = "running"
				continue
			}
			if last == nil {
				last = ru
			}
			end := time.Unix(cmp.Or(ru.task.EndTime, ru.task.StartTime), 0).UTC()
			if !ru.failed && lastOK.IsZero() {
				lastOK = end
			}
			if ru.failed && b.now.Sub(time.Unix(ru.task.StartTime, 0)) <= 24*time.Hour {
				failed24++
			}
			// Per-guest: the newest run that says something about the guest.
			for _, vmid := range sel {
				g := gb[vmid]
				if g == nil {
					g = &guestBackup{}
					gb[vmid] = g
				}
				okHere := slices.Contains(ru.ok, vmid)
				errHere, badHere := ru.bad[vmid]
				if g.lastAt.IsZero() && (okHere || badHere || !ru.known) {
					g.lastAt = end
					switch {
					case okHere:
						g.status = "ok"
					case badHere:
						g.status, g.err = "failed", errHere
					default:
						g.status = "unknown"
					}
				}
				if okHere && g.lastOK.IsZero() {
					g.lastOK = end
				}
			}
		}
		if !lastOK.IsZero() {
			r.Attrs["last_ok"] = lastOK
		}
		b.known = append(b.known, "backup."+j.ID)
		switch {
		case !j.enabled():
			r.State, r.Health = "disabled", integration.Health{Status: integration.HealthOK, Reason: "disabled"}
		case last == nil:
			r.Health = integration.Health{Status: integration.HealthUnknown, Reason: "no run in the recent tasks"}
		}
		if last != nil {
			start, end := time.Unix(last.task.StartTime, 0).UTC(), time.Unix(cmp.Or(last.task.EndTime, last.task.StartTime), 0).UTC()
			r.Attrs["last_run.start"] = start
			r.Attrs["last_run.end"] = end
			r.Attrs["last_run.status"] = "ok"
			if last.failed {
				r.Attrs["last_run.status"] = "failed"
				r.Attrs["last_run.error"] = last.err
			}
			if last.known {
				r.Attrs["last_run.ok"] = len(last.ok)
				r.Attrs["last_run.failed"] = len(last.bad)
			}
			if last.failed && j.enabled() {
				r.Health = integration.Health{Status: integration.HealthAttention, Reason: backupReason(b, last), Since: start}
				b.raise = append(b.raise, b.backupIncident(j, id, sel, runs, last, lastOK))
			}
		}
		b.add(r)
		if sid := b.storageID(cmp.Or(j.Node, b.anyNode()), j.Storage); sid != "" {
			b.rel(id, "stores_on", sid)
		}
		for _, vmid := range sel {
			if g := b.guests[vmid]; g != nil {
				b.rel(g.id, "backed_up_by", id)
				b.jobsOf[vmid] = append(b.jobsOf[vmid], id)
			}
		}
		b.metric(integration.Metric{Resource: id, Key: "tasks.failed_24h", Unit: "count", Min: 0, Max: 100, Value: float64(failed24)})
		age := integration.Unknown()
		if !lastOK.IsZero() {
			age = b.now.Sub(lastOK).Hours()
		}
		per := j.period().Hours()
		b.metric(integration.Metric{Resource: id, Key: "last_ok.age_h", Unit: "h", Min: 0, Max: 24 * 31, Band: [2]float64{0, per + 2}, Warn: 2 * per, Value: age})
	}
	// Per-guest backup attrs, the Demo Home's backup.* keys.
	for i := range b.facts.Resources {
		r := &b.facts.Resources[i]
		if r.Kind != "vm" && r.Kind != "lxc" {
			continue
		}
		vmid, _ := r.Attrs["vmid"].(int)
		if g := gb[vmid]; g != nil {
			if !g.lastAt.IsZero() {
				r.Attrs["backup.last_at"] = g.lastAt
				r.Attrs["backup.last_status"] = g.status
				if g.err != "" {
					r.Attrs["backup.last_error"] = g.err
				}
			}
			if !g.lastOK.IsZero() {
				r.Attrs["backup.last_ok"] = g.lastOK
			}
		}
		if b.in.haveJobs {
			r.Attrs["backup.covered"] = len(b.jobsOf[vmid]) > 0
		}
	}
}

func (b *builder) anyNode() string {
	for n := range b.nodes {
		return n
	}
	return ""
}

// backupReason is the job's health reason: what failed and when.
func backupReason(b *builder, last *run) string {
	at := hhmm(time.Unix(last.task.StartTime, 0))
	switch {
	case last.unreach != nil && last.unreach.endpoint != "":
		return fmt.Sprintf("couldn't reach %s at %s (%s)", last.unreach.storage, at, cmp.Or(last.unreach.reason, "unreachable"))
	case last.unreach != nil:
		return fmt.Sprintf("couldn't activate %s at %s", last.unreach.storage, at)
	case len(last.bad) == 1 && len(last.ok) > 0:
		for vmid := range last.bad {
			return fmt.Sprintf("%s's backup failed at %s", b.guestName(vmid), at)
		}
	case len(last.bad) > 0:
		return fmt.Sprintf("%d of %d backups failed at %s", len(last.bad), len(last.bad)+len(last.ok), at)
	}
	return "the run at " + at + " failed"
}

func (b *builder) guestName(vmid int) string {
	if g := b.guests[vmid]; g != nil {
		return g.name
	}
	return "guest " + strconv.Itoa(vmid)
}

func (b *builder) guestID(vmid int) string {
	if g := b.guests[vmid]; g != nil {
		return g.id
	}
	return ""
}

// backupIncident describes a failed run: a target that couldn't be reached
// (one cause, likely shared with other instances: facts.endpoint is the key
// to correlate them), a passthrough device two guests share, or plain guest
// errors.
func (b *builder) backupIncident(j backupJob, jobID string, sel []int, runs []run, last *run, lastOK time.Time) integration.Incident {
	start := time.Unix(last.task.StartTime, 0).UTC()
	where := cmp.Or(j.Node, b.cluster, b.anyNode())
	var failedAt, failedDays []string
	fails, finished, unreachable := 0, 0, 0
	for _, ru := range runs {
		if ru.running {
			continue
		}
		finished++
		if !ru.failed {
			continue
		}
		fails++
		if ru.unreach != nil && last.unreach != nil && ru.unreach.storage == last.unreach.storage {
			unreachable++
		}
		at := time.Unix(ru.task.StartTime, 0)
		if len(failedAt) < 31 {
			failedAt = append(failedAt, at.UTC().Format(time.RFC3339))
			failedDays = append(failedDays, at.Local().Format("Jan 2"))
		}
	}
	facts := map[string]any{"job": jobID, "storage": j.Storage, "task": "vzdump", "upid": last.task.UPID,
		"error": last.err, "runs": finished, "failed_runs": fails, "failed_at": failedAt}
	if !lastOK.IsZero() {
		facts["last_ok"] = lastOK
	}
	sched := scheduleText(j.Schedule)
	run := dayHHMM(start)
	history := ""
	switch {
	case finished <= 1:
	case fails == finished:
		history = fmt.Sprintf(" It failed all of the last %d runs", finished)
	default:
		history = fmt.Sprintf(" It failed %d of the last %d runs", fails, finished)
	}
	if history != "" {
		switch {
		case last.unreach != nil && unreachable == fails && fails > 1:
			history += ", each time with its target unreachable"
		case last.unreach != nil && unreachable > 1:
			history += fmt.Sprintf(", %d of them with its target unreachable", unreachable)
		}
		history += "."
	}
	lastGood := " No run in the recent tasks succeeded completely."
	if !lastOK.IsZero() {
		lastGood = " The last good run ended " + dayHHMM(lastOK) + "."
	}
	inc := integration.Incident{
		ID: "backup." + j.ID, Scenario: "backup-failed", Class: integration.ClassProblem, Severity: integration.SeverityAttention,
		Target: jobID, OpenedAt: start, Facts: facts,
		Suggested:    integration.Action{ID: "link.open", Label: "Open the backup jobs in Proxmox", Tier: "T0", Target: jobID},
		Alternatives: []integration.Action{{ID: "pve.task.log", Label: "View the failed task's log", Tier: "T0", Target: jobID}},
		ResolvesWhen: "Resolves when a run of this job succeeds: a retry, or the next scheduled run.",
	}
	for _, vmid := range sel {
		if gid := b.guestID(vmid); gid != "" {
			inc.BlastRadius = append(inc.BlastRadius, integration.Impact{Resource: gid, Via: "backed_up_by", Depth: 1})
		}
	}
	switch {
	case last.unreach != nil:
		u := last.unreach
		inc.Scenario = "backup-target-unreachable"
		inc.Title = fmt.Sprintf("Backups on %s couldn't reach %s", where, u.storage)
		facts["cause"] = "target_unreachable"
		if u.endpoint != "" {
			facts["endpoint"] = u.endpoint
			facts["reason"] = u.reason
		}
		storID := b.storageID(cmp.Or(j.Node, b.anyNode()), u.storage)
		target := u.storage
		if u.endpoint != "" {
			target = fmt.Sprintf("%s (%s)", u.storage, u.endpoint)
		}
		inc.Summary = fmt.Sprintf("%s's %s backup couldn't reach its storage %s on its %s run (%s), so nothing was backed up.%s%s",
			where, sched, target, run, cmp.Or(u.reason, "it couldn't be activated"), history, lastGood)
		inc.Causes = append(inc.Causes, integration.Cause{
			Text:     fmt.Sprintf("%s didn't answer at %s (%s). Other hosts that back up to it fail on the same nights.", target, hhmm(start), cmp.Or(u.reason, "could not activate storage")),
			Resource: cmp.Or(storID, jobID), At: start})
		if unreachable > 1 {
			inc.Causes = append(inc.Causes, integration.Cause{
				Text:     fmt.Sprintf("The target was unreachable on %d of the last %d runs: %s.", unreachable, finished, strings.Join(failedDays, ", ")),
				Resource: jobID})
		}
	case len(last.bad) > 0:
		ids := make([]int, 0, len(last.bad))
		for id := range last.bad {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		if len(ids) == 1 {
			vmid := ids[0]
			inc.Title = "Backup of " + b.guestName(vmid) + " failed"
			if gid := b.guestID(vmid); gid != "" {
				inc.Target = gid
			}
			facts["vmid"] = vmid
		} else {
			inc.Title = fmt.Sprintf("%d backups on %s failed", len(ids), where)
		}
		var names []string
		for _, vmid := range ids {
			names = append(names, b.guestName(vmid))
			err := last.bad[vmid]
			cause := integration.Cause{Text: fmt.Sprintf("%s: %s", b.guestName(vmid), err), Resource: cmp.Or(b.guestID(vmid), jobID), At: start}
			if m := pciBusyRE.FindStringSubmatch(err); m != nil {
				other, _ := strconv.Atoi(m[2])
				inc.Scenario = "backup-passthrough-conflict"
				facts["pci_device"] = m[1]
				facts["in_use_by"] = other
				cause.Text = fmt.Sprintf("%s and %s both pass through PCI device %s. Backing up the stopped %s has to start it, and that fails while %s runs.",
					b.guestName(vmid), b.guestName(other), shortPCI(m[1]), b.guestName(vmid), b.guestName(other))
				inc.Causes = append(inc.Causes, cause)
				if oid := b.guestID(other); oid != "" {
					inc.Causes = append(inc.Causes, integration.Cause{Text: fmt.Sprintf("%s holds %s.", b.guestName(other), shortPCI(m[1])), Resource: oid})
				}
				continue
			}
			inc.Causes = append(inc.Causes, cause)
		}
		okText := ""
		switch n := len(last.ok); {
		case n > 0 && n <= 3:
			var ok []string
			for _, vmid := range last.ok {
				ok = append(ok, b.guestName(vmid))
			}
			verb := "was"
			if n > 1 {
				verb = "were"
			}
			okText = fmt.Sprintf(" %s %s backed up normally.", strings.Join(ok, ", "), verb)
		case n > 3:
			okText = fmt.Sprintf(" The other %d guests were backed up normally.", n)
		}
		inc.Summary = fmt.Sprintf("%s's %s backup failed for %s on its %s run.%s%s%s",
			where, sched, strings.Join(names, ", "), run, okText, history, lastGood)
	default:
		inc.Title = fmt.Sprintf("Backup job on %s failed", where)
		inc.Summary = fmt.Sprintf("%s's %s backup failed on its %s run: %s.%s%s", where, sched, run, last.err, history, lastGood)
		inc.Causes = append(inc.Causes, integration.Cause{Text: last.err, Resource: jobID, At: start})
	}
	return inc
}

// shortPCI drops the function from a PCI address: "0000:01:00.0" → "0000:01:00".
func shortPCI(s string) string {
	if i := strings.LastIndexByte(s, '.'); i > 0 && strings.Count(s, ":") == 2 {
		return s[:i]
	}
	return s
}

// ── hints ───────────────────────────────────────────────────────────────

// hints are quiet inbox items (severity info): backups that stay on the
// host, guests no job covers, apt-get update failing on a node.
func (b *builder) hints(res []resource) {
	domain := cmp.Or(b.cluster, b.anyNode())
	// Jobs that write to a node's own disk.
	for _, j := range b.in.jobs {
		hid := "hint.backup-local." + j.ID
		b.known = append(b.known, hid)
		if !j.enabled() {
			continue
		}
		local := false
		for _, r := range res {
			if r.Type == "storage" && r.Storage == j.Storage && !r.Shared.bool() && (j.Node == "" || r.Node == j.Node) {
				local = true
			}
		}
		if !local {
			continue
		}
		jobID := b.pre + "backup/" + j.ID
		where := cmp.Or(j.Node, domain)
		b.raise = append(b.raise, integration.Incident{
			ID: hid, Scenario: "backup-stays-local", Class: integration.ClassProblem, Severity: integration.SeverityInfo,
			Title:   "Backups on " + where + " stay on " + where,
			Summary: fmt.Sprintf("The %s job writes to %s, a disk inside %s. If %s fails, its backups go with it. Point the job at PBS or another host.", scheduleText(j.Schedule), j.Storage, where, where),
			Target:  jobID, Facts: map[string]any{"job": jobID, "storage": j.Storage},
			Suggested:    integration.Action{ID: "link.open", Label: "Open the backup jobs in Proxmox", Tier: "T0", Target: jobID},
			ResolvesWhen: "Resolves when the job writes to shared or remote storage.",
		})
	}
	// Guests in no job.
	b.known = append(b.known, "hint.not-backed-up")
	if len(b.in.notBkp) > 0 && b.in.haveJobs {
		var names []string
		var impact []integration.Impact
		var vmids []int
		for _, g := range b.in.notBkp {
			vmid := int(g.VMID)
			if gf := b.guests[vmid]; gf != nil && gf.r.Template.bool() {
				continue
			}
			vmids = append(vmids, vmid)
			names = append(names, cmp.Or(g.Name, b.guestName(vmid)))
			if gid := b.guestID(vmid); gid != "" {
				impact = append(impact, integration.Impact{Resource: gid, Via: "backed_up_by", Depth: 1})
			}
		}
		if len(vmids) > 0 {
			title := fmt.Sprintf("%d guests on %s aren't backed up", len(vmids), domain)
			if len(vmids) == 1 {
				title = names[0] + " on " + domain + " isn't backed up"
			}
			summary := fmt.Sprintf("No backup job covers %s.", strings.Join(names, ", "))
			if len(b.in.jobs) == 0 {
				summary = fmt.Sprintf("%s has no backup job, so nothing on it is backed up: %s.", domain, strings.Join(names, ", "))
			}
			target := b.self
			if b.cluster != "" {
				target = b.pre + "cluster"
			} else if n := b.anyNode(); n != "" {
				target = b.nodeID(n)
			}
			b.raise = append(b.raise, integration.Incident{
				ID: "hint.not-backed-up", Scenario: "backup-coverage-gap", Class: integration.ClassProblem, Severity: integration.SeverityInfo,
				Title: title, Summary: summary, Target: target, Facts: map[string]any{"vmids": vmids}, BlastRadius: impact,
				Suggested:    integration.Action{ID: "link.open", Label: "Open the backup jobs in Proxmox", Tier: "T0", Target: target},
				ResolvesWhen: "Resolves when a backup job covers every guest.",
			})
		}
	}
	// apt-get update failing.
	newest := map[string]task{}
	failedRun := map[string]int{}
	byNode := map[string][]task{}
	for _, t := range b.in.tasks {
		if t.Type == "aptupdate" && t.finished() {
			byNode[t.Node] = append(byNode[t.Node], t)
		}
	}
	for n, ts := range byNode {
		slices.SortFunc(ts, func(a, c task) int { return cmp.Compare(c.StartTime, a.StartTime) })
		newest[n] = ts[0]
		for _, t := range ts {
			if t.ok() {
				break
			}
			failedRun[n]++
		}
	}
	for n := range b.nodes {
		hid := "hint.aptupdate." + n
		b.known = append(b.known, hid)
		t, ok := newest[n]
		if !ok || t.ok() {
			continue
		}
		at := time.Unix(t.StartTime, 0).UTC()
		b.raise = append(b.raise, integration.Incident{
			ID: hid, Scenario: "apt-update-failing", Class: integration.ClassProblem, Severity: integration.SeverityInfo,
			Title:   "apt-get update fails on " + n,
			Summary: fmt.Sprintf("The package index refresh on %s has failed %d times in a row (%s). Until it works, %s won't learn about updates, security fixes included.", n, failedRun[n], t.Status, n),
			Target:  b.nodeID(n), OpenedAt: at, Facts: map[string]any{"node": n, "error": t.Status, "failures": failedRun[n]},
			Causes:       []integration.Cause{{Text: t.Status, Resource: b.nodeID(n), At: at}},
			Suggested:    integration.Action{ID: "link.open", Label: "Open " + n + " in Proxmox", Tier: "T0", Target: b.nodeID(n)},
			ResolvesWhen: "Resolves when apt-get update succeeds on " + n + ".",
		})
	}
}
