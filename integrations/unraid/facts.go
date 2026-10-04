package unraid

import (
	"fmt"
	"math"
	"strings"
	"time"

	"dash.cha.sh/internal/integration"
)

// What the reads return, as far as the integration uses it.

type infoData struct {
	OS struct {
		Hostname string `json:"hostname"`
		Uptime   string `json:"uptime"` // the boot time, RFC 3339
	} `json:"os"`
	CPU struct {
		Brand   string `json:"brand"`
		Cores   int    `json:"cores"`
		Threads int    `json:"threads"`
	} `json:"cpu"`
	Versions struct {
		Core struct {
			Unraid string `json:"unraid"`
			API    string `json:"api"`
		} `json:"core"`
	} `json:"versions"`
}

// version is what the instance reports as the upstream version.
func (i infoData) version() string {
	c := i.Versions.Core
	switch {
	case c.Unraid != "" && c.API != "":
		return c.Unraid + " (API " + shortVersion(c.API) + ")"
	case c.API != "":
		return "API " + shortVersion(c.API)
	}
	return c.Unraid
}

type metricsData struct {
	CPU struct {
		PercentTotal num `json:"percentTotal"`
	} `json:"cpu"`
	Memory struct {
		Total        num `json:"total"` // bytes
		PercentTotal num `json:"percentTotal"`
	} `json:"memory"`
}

type arrayData struct {
	State    string `json:"state"`
	Capacity struct {
		Kilobytes struct {
			Free  num `json:"free"`
			Used  num `json:"used"`
			Total num `json:"total"`
		} `json:"kilobytes"`
	} `json:"capacity"`
	Parity struct {
		Status  string `json:"status"`
		Errors  num    `json:"errors"`
		Running *bool  `json:"running"`
		Paused  *bool  `json:"paused"`
		Date    string `json:"date"`
	} `json:"parityCheckStatus"`
	Boot     *device  `json:"boot"`
	Parities []device `json:"parities"`
	Disks    []device `json:"disks"`
	Caches   []device `json:"caches"`
}

// device is an ArrayDisk: an array, pool or boot device as emhttp sees it.
// Sizes: size is KiB; fsSize and fsUsed are 1000-byte units.
type device struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Device     string `json:"device"`
	Size       num    `json:"size"`
	Status     string `json:"status"`
	Type       string `json:"type"`
	Temp       num    `json:"temp"` // null while spun down
	IsSpinning *bool  `json:"isSpinning"`
	NumErrors  num    `json:"numErrors"`
	FsSize     num    `json:"fsSize"`
	FsUsed     num    `json:"fsUsed"`
	FsType     string `json:"fsType"`
	Rotational *bool  `json:"rotational"`
	Transport  string `json:"transport"`
}

type share struct {
	Name    string   `json:"name"`
	Include []string `json:"include"`
	Exclude []string `json:"exclude"`
}

type container struct {
	ID             string         `json:"id"` // <server id>:<Docker container id>
	Names          []string       `json:"names"`
	Image          string         `json:"image"`
	State          string         `json:"state"`
	AutoStart      bool           `json:"autoStart"`
	AutoStartOrder *int           `json:"autoStartOrder"`
	AutoStartWait  *int           `json:"autoStartWait"`
	Labels         map[string]any `json:"labels"`
	Mounts         []struct {
		Source string `json:"Source"`
	} `json:"mounts"`
	WebUIURL string `json:"webUiUrl"`
}

// diskNames are the array's data and parity disks.
func (a *arrayData) diskNames() map[string]bool {
	out := map[string]bool{}
	for _, d := range append(append([]device(nil), a.Parities...), a.Disks...) {
		out[d.Name] = true
	}
	return out
}

// pool is a pool and its devices, the first one first.
type pool struct {
	first   device
	members []device
}

// pools groups the pool devices. Unraid has no pool object: a pool's
// devices are named <pool>, <pool>2, <pool>3, … (pool names can't end in a
// digit), and only the first carries the file system.
func (a *arrayData) pools() []*pool {
	var out []*pool
	byName := map[string]*pool{}
	for _, d := range a.Caches {
		if d.Status == "DISK_NP" {
			continue // an empty slot
		}
		if p := byName[strings.TrimRight(d.Name, "0123456789")]; p != nil && d.Name != p.first.Name {
			p.members = append(p.members, d)
			continue
		}
		p := &pool{first: d, members: []device{d}}
		out = append(out, p)
		byName[d.Name] = p
	}
	return out
}

// poolNames are the pools' names.
func (a *arrayData) poolNames() map[string]bool {
	out := map[string]bool{}
	for _, p := range a.pools() {
		out[p.first.Name] = true
	}
	return out
}

// Scales, from the Demo Home (sim/topology.go).
var (
	hddTemp = scale{min: 15, max: 70, band: [2]float64{30, 45}, warn: 50, crit: 55}
	ssdTemp = scale{min: 15, max: 90, band: [2]float64{30, 55}, warn: 70, crit: 80}
)

type scale struct {
	min, max   float64
	band       [2]float64
	warn, crit float64
}

func metric(res, key, unit string, s scale, v float64) integration.Metric {
	return integration.Metric{Resource: res, Key: key, Unit: unit, Min: s.min, Max: s.max, Band: s.band, Warn: s.warn, Crit: s.crit, Value: v}
}

func pct(res, key string, band [2]float64, warn, crit, v float64) integration.Metric {
	return metric(res, key, "%", scale{min: 0, max: 100, band: band, warn: warn, crit: crit}, round(v, 1))
}

func round(v float64, places int) float64 {
	if math.IsNaN(v) {
		return v
	}
	p := math.Pow(10, float64(places))
	return math.Round(v*p) / p
}

// ratio is 100·used/total, or unknown.
func ratio(used, total num) float64 {
	if !used.ok || !total.ok || total.v <= 0 {
		return integration.Unknown()
	}
	return 100 * used.v / total.v
}

func monitors(self, id string) integration.Relation {
	return integration.Relation{From: self, Type: "monitors", To: id}
}

func rel(from, typ, to string) integration.Relation {
	return integration.Relation{From: from, Type: typ, To: to}
}

// worse returns the more severe of two healths.
func worse(a, b integration.Health) integration.Health {
	rank := map[string]int{integration.HealthOK: 0, integration.HealthMaintenance: 1, integration.HealthAttention: 2, integration.HealthDown: 3}
	if rank[b.Status] > rank[a.Status] {
		return b
	}
	return a
}

var healthy = integration.Health{Status: integration.HealthOK}

// coreFacts are the host, the array, its disks, the pools and the boot
// device, with their metrics.
func (in *instance) coreFacts(a *arrayData, m *metricsData) integration.Facts {
	var f integration.Facts
	host := in.id("host")
	f.Resources = append(f.Resources, in.hostResource(host, m))
	f.Relations = append(f.Relations, monitors(in.d.Emit.Self(), host))
	cpu, mem := integration.Unknown(), integration.Unknown()
	if m != nil {
		cpu, mem = m.CPU.PercentTotal.or(), m.Memory.PercentTotal.or()
	}
	f.Metrics = append(f.Metrics,
		pct(host, "cpu.pct", [2]float64{0, 40}, 0, 0, cpu),
		pct(host, "mem.pct", [2]float64{0, 80}, 90, 98, mem))

	started := a.State == "STARTED"
	arrayID := in.id("array")
	ar := integration.Resource{ID: arrayID, Kind: "array", Name: "array", State: "online", Health: healthy, Attrs: map[string]any{}}
	if total := a.Capacity.Kilobytes.Total; total.ok {
		ar.Attrs["size_tb"] = round(total.v/1e9, 1)
	}
	ar.Attrs["disks"] = len(a.Disks)
	ar.Attrs["parity_disks"] = len(a.Parities)
	switch a.State {
	case "STARTED":
	case "STOPPED":
		ar.State, ar.Health = "stopped", integration.Health{Status: integration.HealthDown, Reason: "The array is stopped, so its shares are unavailable"}
	default:
		ar.State, ar.Health = "error", integration.Health{Status: integration.HealthDown, Reason: "The array can't start: " + humanize(a.State)}
	}
	f.Relations = append(f.Relations, rel(arrayID, "part_of", host))

	// Parity and data disks.
	disabled, parityBad := 0, false
	for _, d := range append(append([]device(nil), a.Parities...), a.Disks...) {
		if d.Status == "DISK_NP" {
			continue // an empty slot
		}
		r, h := in.diskResource(d)
		if h.Status == integration.HealthDown {
			disabled++
		}
		if d.Type == "PARITY" && h.Status != integration.HealthOK {
			parityBad = true
		}
		if h.Status != integration.HealthOK && started {
			ar.Health = worse(ar.Health, integration.Health{Status: integration.HealthAttention, Reason: d.Name + " " + h.Reason})
		}
		f.Resources = append(f.Resources, r)
		f.Relations = append(f.Relations, rel(r.ID, "part_of", arrayID))
		f.Metrics = append(f.Metrics, diskMetrics(r.ID, d)...)
	}
	if started && disabled > len(a.Parities) {
		ar.Health = integration.Health{Status: integration.HealthDown,
			Reason: fmt.Sprintf("%d disks are missing or disabled, more than parity can cover", disabled)}
	}

	// Parity.
	p := a.Parity
	running := p.Running != nil && *p.Running || p.Status == "RUNNING"
	paused := p.Paused != nil && *p.Paused || p.Status == "PAUSED"
	switch {
	case len(a.Parities) == 0:
		ar.Attrs["parity"] = "none"
	case parityBad:
		ar.Attrs["parity"] = "invalid"
	case running || paused:
		ar.Attrs["parity"] = "checking"
	default:
		ar.Attrs["parity"] = "valid"
	}
	if p.Status != "" {
		ar.Attrs["parity_check"] = strings.ToLower(p.Status)
	}
	if p.Status == "COMPLETED" {
		if t, err := time.Parse(time.RFC3339, p.Date); err == nil {
			ar.Attrs["parity_checked_at"] = t.UTC().Format(time.RFC3339)
		}
	}
	if p.Errors.ok && p.Errors.v > 0 {
		ar.Attrs["parity_errors"] = p.Errors.v
		if started && !running {
			ar.Health = worse(ar.Health, integration.Health{Status: integration.HealthAttention,
				Reason: fmt.Sprintf("The last parity check found %d errors", int(p.Errors.v))})
		}
	}
	if started && running && ar.Health.Status == integration.HealthOK {
		ar.Health = integration.Health{Status: integration.HealthMaintenance, Reason: "A parity check is running"}
	}
	f.Resources = append(f.Resources, ar)
	f.Metrics = append(f.Metrics, pct(arrayID, "used.pct", [2]float64{0, 85}, 85, 95, ratio(a.Capacity.Kilobytes.Used, a.Capacity.Kilobytes.Total)))

	for _, p := range a.pools() {
		id := in.id("pool/" + p.first.Name)
		pr := integration.Resource{ID: id, Kind: "pool", Name: p.first.Name, State: "online", Health: healthy,
			Attrs: map[string]any{"devices": len(p.members), "media": media(p.members)}}
		if p.first.FsType != "" {
			pr.Attrs["fs"] = p.first.FsType
		}
		if p.first.FsSize.ok {
			pr.Attrs["size_tb"] = round(p.first.FsSize.v/1e9, 2)
		}
		switch {
		case !started:
			pr.State, pr.Health = "stopped", integration.Health{Status: integration.HealthDown, Reason: "Pools are unmounted while the array is stopped"}
		case !p.first.FsSize.ok:
			pr.Health = integration.Health{Status: integration.HealthAttention, Reason: "The pool isn't mounted"}
		}
		hottest := integration.Unknown()
		for _, d := range p.members {
			r, h := in.diskResource(d)
			r.Attrs["pool"] = p.first.Name
			if h.Status != integration.HealthOK && started {
				pr.Health = worse(pr.Health, integration.Health{Status: integration.HealthAttention, Reason: d.Name + " " + h.Reason})
			}
			if d.Temp.ok && (math.IsNaN(hottest) || d.Temp.v > hottest) {
				hottest = d.Temp.v
			}
			f.Resources = append(f.Resources, r)
			f.Relations = append(f.Relations, rel(r.ID, "part_of", id))
			f.Metrics = append(f.Metrics, metric(r.ID, "temp.c", "°C", tempScale(d), d.Temp.or()))
		}
		f.Resources = append(f.Resources, pr)
		f.Relations = append(f.Relations, rel(id, "part_of", host))
		f.Metrics = append(f.Metrics,
			pct(id, "used.pct", [2]float64{0, 80}, 85, 95, ratio(p.first.FsUsed, p.first.FsSize)),
			metric(id, "temp.c", "°C", tempScale(p.members...), hottest))
	}

	// The boot flash. Its serial ties to the Unraid license, so it isn't
	// published.
	if b := a.Boot; b != nil && b.Status != "DISK_NP" {
		r, _ := in.diskResource(*b)
		delete(r.Attrs, "serial")
		f.Resources = append(f.Resources, r)
		f.Relations = append(f.Relations, rel(r.ID, "part_of", host))
		f.Metrics = append(f.Metrics, pct(r.ID, "used.pct", [2]float64{0, 85}, 90, 97, ratio(b.FsUsed, b.FsSize)))
	}
	return f
}

func (in *instance) hostResource(id string, m *metricsData) integration.Resource {
	r := integration.Resource{ID: id, Kind: "host", Name: in.name, State: "online", Health: healthy,
		Labels: map[string]string{"chadash.role": "nas"},
		Attrs:  map[string]any{"platform": "unraid"},
		Links:  []integration.Link{{Title: "Unraid", URL: in.web}},
		Caps:   []string{"host.metrics", "storage.pools", "storage.disks", "storage.shares", "containers"},
	}
	if i := in.info; i != nil {
		if i.OS.Hostname != "" {
			r.Name = i.OS.Hostname
			r.Attrs["hostname"] = i.OS.Hostname
		}
		if v := i.Versions.Core.Unraid; v != "" {
			r.Attrs["version"] = v
		}
		if v := i.Versions.Core.API; v != "" {
			r.Attrs["api_version"] = shortVersion(v)
		}
		if i.CPU.Cores > 0 {
			r.Attrs["cores"] = i.CPU.Cores
		}
		if i.CPU.Threads > 0 {
			r.Attrs["threads"] = i.CPU.Threads
		}
		if i.CPU.Brand != "" {
			r.Attrs["cpu"] = i.CPU.Brand
		}
		if t, err := time.Parse(time.RFC3339, i.OS.Uptime); err == nil {
			r.Attrs["boot_at"] = t.UTC().Truncate(time.Second).Format(time.RFC3339)
		}
	}
	if ip := cmpOr(in.urlIP, in.netIP); ip != "" {
		r.Attrs["ip"] = ip
	}
	if m != nil && m.Memory.Total.ok {
		r.Attrs["mem_gb"] = round(m.Memory.Total.v/(1<<30), 1)
	}
	return r
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// diskResource is one device, with its health from emhttp's status and
// error count.
func (in *instance) diskResource(d device) (integration.Resource, integration.Health) {
	role := map[string]string{"PARITY": "parity", "DATA": "data", "CACHE": "pool", "FLASH": "boot"}[d.Type]
	if role == "" {
		role = strings.ToLower(d.Type)
	}
	r := integration.Resource{ID: in.id("disk/" + d.Name), Kind: "disk", Name: d.Name, State: "online",
		Attrs: map[string]any{"role": role}}
	if d.IsSpinning != nil && !*d.IsSpinning {
		r.State = "idle" // spun down
	}
	model, serial := modelSerial(d.ID)
	set := func(k string, v any, keep bool) {
		if keep {
			r.Attrs[k] = v
		}
	}
	set("serial", serial, serial != "")
	set("model", model, model != "")
	set("device", d.Device, d.Device != "")
	set("fs", d.FsType, d.FsType != "")
	set("transport", d.Transport, d.Transport != "")
	set("size_tb", round(d.Size.v*1024/1e12, 2), d.Size.ok)
	if d.Rotational != nil {
		r.Attrs["rotational"] = *d.Rotational
	}
	h := diskHealth(d)
	if d.NumErrors.ok && d.NumErrors.v > 0 {
		r.Attrs["errors"] = d.NumErrors.v
	}
	r.Health = h
	return r, h
}

// diskHealth reads emhttp's device status (ArrayDiskStatus).
func diskHealth(d device) integration.Health {
	down := func(reason string) integration.Health {
		return integration.Health{Status: integration.HealthDown, Reason: reason}
	}
	attention := func(reason string) integration.Health {
		return integration.Health{Status: integration.HealthAttention, Reason: reason}
	}
	switch d.Status {
	case "DISK_OK", "":
	case "DISK_NP_MISSING":
		return down("is missing")
	case "DISK_DSBL":
		if d.Type == "DATA" {
			return down("is disabled; its contents are emulated from parity")
		}
		return down("is disabled")
	case "DISK_NP_DSBL":
		return down("is disabled and missing")
	case "DISK_INVALID":
		return attention("has invalid contents until parity is rebuilt")
	case "DISK_WRONG":
		return attention("is a different disk from the one assigned")
	case "DISK_DSBL_NEW", "DISK_NEW":
		return attention("is new and not yet part of the array")
	default:
		return attention("reports " + humanize(d.Status))
	}
	if d.NumErrors.ok && d.NumErrors.v > 0 {
		return attention(fmt.Sprintf("has %d read or write errors", int(d.NumErrors.v)))
	}
	return healthy
}

func diskMetrics(id string, d device) []integration.Metric {
	var out []integration.Metric
	if d.Type == "DATA" {
		out = append(out, pct(id, "used.pct", [2]float64{0, 85}, 90, 97, ratio(d.FsUsed, d.FsSize)))
	}
	return append(out, metric(id, "temp.c", "°C", tempScale(d), d.Temp.or()))
}

// tempScale is the HDD scale unless every device is solid state.
func tempScale(ds ...device) scale {
	for _, d := range ds {
		if d.Rotational == nil || *d.Rotational {
			return hddTemp
		}
	}
	return ssdTemp
}

// media names what a pool is made of, the Demo Home's way.
func media(ds []device) string {
	nvme, rotational := true, false
	for _, d := range ds {
		nvme = nvme && d.Transport == "nvme"
		rotational = rotational || d.Rotational == nil || *d.Rotational
	}
	switch {
	case nvme:
		return "nvme"
	case rotational:
		return "hdd"
	}
	return "ssd"
}

// modelSerial splits an array device ID, "<server id>:<model>_<serial>"
// (the boot flash: "<vendor>_<model>_<serial>-0:0", unprefixed). The model
// is spelled with spaces.
func modelSerial(id string) (model, serial string) {
	local := id
	if prefix, rest, found := strings.Cut(id, ":"); found && len(prefix) == 64 && isHex(prefix) {
		local = rest
	}
	local = strings.TrimSuffix(local, "-0:0")
	i := strings.LastIndexByte(local, '_')
	if i < 0 {
		return "", local
	}
	return strings.ReplaceAll(local[:i], "_", " "), local[i+1:]
}

func isHex(s string) bool {
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
			return false
		}
	}
	return true
}

// localID is the local part of a PrefixedID: for a container, Docker's
// full container ID.
func localID(id string) string {
	if prefix, rest, found := strings.Cut(id, ":"); found && len(prefix) == 64 && isHex(prefix) {
		return rest
	}
	return id
}

// humanize turns an enum like TOO_MANY_MISSING_DISKS into words.
func humanize(s string) string {
	return strings.ReplaceAll(strings.ToLower(s), "_", " ")
}

// shareFacts are the user shares. Unraid's API doesn't say which pool a
// share lives on, so a share is part of the array when its include or
// exclude list names array disks, and of the host otherwise.
func (in *instance) shareFacts(shares []share) integration.Facts {
	var f integration.Facts
	for _, s := range shares {
		if s.Name == "" {
			continue
		}
		id := in.id("share/" + s.Name)
		r := integration.Resource{ID: id, Kind: "share", Name: s.Name, State: "online", Health: healthy, Attrs: map[string]any{}}
		parent := in.id("host")
		for _, list := range []struct {
			key   string
			disks []string
		}{{"include", s.Include}, {"exclude", s.Exclude}} {
			if len(list.disks) == 0 {
				continue
			}
			r.Attrs[list.key] = list.disks
			for _, d := range list.disks {
				if in.arrayDisks[d] {
					parent = in.id("array")
				}
			}
		}
		f.Resources = append(f.Resources, r)
		f.Relations = append(f.Relations, rel(id, "part_of", parent))
	}
	return f
}

// containerFacts are the Docker containers Unraid knows, with the desired
// state its autostart list gives. They join the Docker integration's
// containers by attrs.container_id, Docker's full container ID.
func (in *instance) containerFacts(cs []container) integration.Facts {
	var f integration.Facts
	host := in.id("host")
	seen := map[string]bool{}
	for _, c := range cs {
		if len(c.Names) == 0 {
			continue
		}
		name := strings.TrimPrefix(c.Names[0], "/")
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		id := in.id("container/" + name)
		r := integration.Resource{ID: id, Kind: "container", Name: name, State: strings.ToLower(c.State), Health: healthy,
			Attrs: map[string]any{"container_id": localID(c.ID), "autostart": c.AutoStart}}
		if c.Image != "" {
			r.Attrs["image"] = c.Image
		}
		if m, _ := c.Labels[managedLabel].(string); m != "" {
			r.Attrs["manager"] = m
		}
		if c.AutoStart {
			// Unraid starts it at boot. Off the list, Docker's restart policy
			// (which this API doesn't report) decides, so desired stays unset.
			r.Desired = "running"
			if c.AutoStartOrder != nil {
				r.Attrs["autostart_order"] = *c.AutoStartOrder
			}
			if c.AutoStartWait != nil && *c.AutoStartWait > 0 {
				r.Attrs["autostart_wait_s"] = *c.AutoStartWait
			}
			if r.State != "running" {
				r.Health = integration.Health{Status: integration.HealthAttention, Reason: "Unraid starts it at boot, but it's " + r.State}
			}
		}
		if u := c.WebUIURL; strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") {
			r.Links = []integration.Link{{Title: name, URL: u}}
		}
		f.Resources = append(f.Resources, r)
		f.Relations = append(f.Relations, rel(id, "runs_on", host))
		stored := map[string]bool{}
		for _, m := range c.Mounts {
			if s := in.shareOf(m.Source); s != "" && !stored[s] {
				stored[s] = true
				f.Relations = append(f.Relations, rel(id, "stores_on", in.id("share/"+s)))
			}
		}
	}
	return f
}

const managedLabel = "net.unraid.docker.managed"

// shareOf names the share a host path is in: /mnt/user/<share>/…,
// /mnt/user0/<share>/… (the array-only view), or /mnt/<disk or pool>/<share>/….
func (in *instance) shareOf(path string) string {
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(parts) < 3 || parts[0] != "mnt" || !in.shares[parts[2]] {
		return ""
	}
	switch root := parts[1]; {
	case root == "user", root == "user0", in.arrayDisks[root], in.pools[root]:
		return parts[2]
	}
	return ""
}
