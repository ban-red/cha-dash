package truenas

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"math"
	"net"
	"slices"
	"strconv"
	"strings"
	"time"

	"dash.cha.sh/internal/integration"
)

// Thresholds. ZFS slows down past 80% full, which is where TrueNAS's own
// capacity alert starts.
const (
	poolWarn, poolCrit = 80, 90
	dsWarn, dsCrit     = 90, 96
	tempMaxAge         = 3 * liveInterval
	maxCovers          = 32
)

type scale struct {
	min, max   float64
	band       [2]float64
	warn, crit float64
}

var (
	hddTemp = scale{min: 15, max: 70, band: [2]float64{30, 45}, warn: 50, crit: 55}
	ssdTemp = scale{min: 15, max: 90, band: [2]float64{30, 55}, warn: 70, crit: 80}
)

// Alerts the integration already turns into health and incidents of its
// own; the others at ERROR or worse become incidents of their own.
var ownAlerts = map[string]bool{
	"VolumeStatus": true, "ReplicationFailed": true, "ReplicationSuccess": true, "SnapshotFailed": true,
}

func ownAlert(klass string) bool { return ownAlerts[klass] || strings.HasPrefix(klass, "SMART") }

func round(v float64, places int) float64 {
	if math.IsNaN(v) {
		return v
	}
	p := math.Pow(10, float64(places))
	return math.Round(v*p) / p
}

func tb(b float64) float64 { return round(b/1e12, 2) }
func gb(b float64) float64 { return round(b/1e9, 1) }
func hhmm(t time.Time) string {
	return t.Local().Format("Jan 2 15:04")
}

// slug keeps an ID token free of whitespace: dataset names may hold spaces.
func slug(s string) string {
	return strings.Map(func(r rune) rune {
		if r == ' ' || r == '\t' || r < 0x20 {
			return '_'
		}
		return r
	}, s)
}

func plural(n int, w string) string {
	if n == 1 {
		return "1 " + w
	}
	return fmt.Sprintf("%d %ss", n, w)
}

func joinAnd(s []string) string {
	switch len(s) {
	case 0:
		return ""
	case 1:
		return s[0]
	}
	return strings.Join(s[:len(s)-1], ", ") + " and " + s[len(s)-1]
}

func ptr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func (in *instance) pre() string            { return ID + ":" + in.key + "/" }
func (in *instance) hostID() string         { return in.pre() + "host" }
func (in *instance) poolID(n string) string { return in.pre() + "pool/" + slug(n) }
func (in *instance) dsID(n string) string   { return in.pre() + "dataset/" + slug(n) }
func (in *instance) replID(id int) string   { return in.pre() + "replication/" + strconv.Itoa(id) }
func (in *instance) snapID(id int) string   { return in.pre() + "snapshot_task/" + strconv.Itoa(id) }

func (in *instance) diskID(d disk) string {
	if s := strings.TrimSpace(d.Serial); s != "" {
		return in.pre() + "disk/" + slug(s)
	}
	return in.pre() + "disk/" + slug(d.Name)
}

// leafState is what a pool's topology says about one disk.
type leafState struct {
	pool, role, status string
	errors             int
}

// smartIssue is a SMART alert for a disk.
type smartIssue struct {
	message string
	down    bool
}

// publish emits everything read, raises and resolves incidents, and
// reports the version and warnings.
func (in *instance) publish(ctx context.Context, now time.Time) error {
	self := in.d.Emit.Self()
	host := in.hostID()
	var f integration.Facts
	var raise []integration.Incident
	var known []string

	// Alerts: SMART ones by disk, the rest counted on the host.
	smart := map[string]smartIssue{}
	active := 0
	for _, a := range in.alerts {
		if a.Dismissed || levelRank[a.Level] < levelRank["WARNING"] {
			continue
		}
		active++
		if strings.HasPrefix(a.Klass, "SMART") {
			k := cmp.Or(a.arg("serial"), strings.TrimPrefix(cmp.Or(a.arg("name"), a.arg("device")), "/dev/"))
			if k != "" {
				smart[k] = smartIssue{message: a.message(), down: levelRank[a.Level] >= levelRank["CRITICAL"]}
			}
		}
	}

	// The host.
	hr := integration.Resource{ID: host, Kind: "host", Name: cmp.Or(in.info.Hostname, in.name, "TrueNAS"), State: "online",
		Labels: map[string]string{"chadash.role": "nas"},
		Attrs:  map[string]any{"platform": "truenas", "version": in.version},
		Links:  []integration.Link{{Title: "TrueNAS", URL: in.web + "/"}},
		Caps:   []string{"storage.pools", "storage.disks", "storage.shares"},
		Health: integration.Health{Status: integration.HealthOK}}
	if in.info.Hostname != "" {
		hr.Attrs["hostname"] = in.info.Hostname
	}
	if in.info.Model != "" {
		hr.Attrs["cpu"] = in.info.Model
	}
	if in.info.Cores > 0 {
		hr.Attrs["threads"] = in.info.Cores
	}
	if in.info.PhysicalCores > 0 {
		hr.Attrs["cores"] = in.info.PhysicalCores
	}
	if in.info.Physmem > 0 {
		hr.Attrs["mem_gb"] = round(in.info.Physmem/(1<<30), 1)
	}
	if in.info.EccMemory != nil {
		hr.Attrs["ecc"] = *in.info.EccMemory
	}
	if !in.info.Boottime.IsZero() {
		hr.Attrs["boot_at"] = in.info.Boottime.UTC().Truncate(time.Second).Format(time.RFC3339)
	} else if in.info.UptimeSeconds > 0 {
		hr.Attrs["boot_at"] = now.Add(-time.Duration(in.info.UptimeSeconds * float64(time.Second))).UTC().Truncate(time.Minute).Format(time.RFC3339)
	}
	if ip := net.ParseIP(in.host); ip != nil {
		hr.Attrs["ip"] = ip.String()
	}
	if in.have["alerts"] {
		hr.Attrs["alerts"] = active
	}
	f.Relations = append(f.Relations, integration.Relation{From: self, Type: "monitors", To: host})
	alertCount := integration.Unknown()
	if in.have["alerts"] {
		alertCount = float64(active)
	}
	f.Metrics = append(f.Metrics, integration.Metric{Resource: host, Key: "alerts.active", Unit: "count", Min: 0, Max: 50, Value: alertCount})
	if len(in.info.Loadavg) > 0 && in.info.Loadavg[0].ok {
		f.Metrics = append(f.Metrics, integration.Metric{Resource: host, Key: "load.1m", Unit: "", Min: 0, Max: float64(max(in.info.Cores, 1)),
			Band: [2]float64{0, float64(max(in.info.Cores, 1)) * 0.7}, Value: round(in.info.Loadavg[0].v, 2)})
	}

	// Pools.
	leaves := map[string]leafState{} // disk name → its place in a pool
	poolIDs := map[string]string{}
	for _, p := range in.pools {
		id := in.poolID(p.Name)
		poolIDs[p.Name] = id
		r, inc, l := in.poolFacts(p, id, now)
		maps.Copy(leaves, l)
		used := integration.Unknown()
		if p.Size.ok && p.Size.v > 0 && p.Allocated.ok {
			used = p.Allocated.v / p.Size.v * 100
		}
		if used >= poolWarn && r.Health.Status == integration.HealthOK {
			r.Health = integration.Health{Status: integration.HealthAttention, Reason: fmt.Sprintf("%.0f%% used", used)}
		}
		f.Resources = append(f.Resources, r)
		f.Relations = append(f.Relations, integration.Relation{From: id, Type: "part_of", To: host})
		f.Metrics = append(f.Metrics, integration.Metric{Resource: id, Key: "used.pct", Unit: "%", Min: 0, Max: 100,
			Band: [2]float64{0, 70}, Warn: poolWarn, Crit: poolCrit, Value: round(used, 1)})
		known = append(known, "pool."+slug(p.Name)+".health")
		if inc != nil {
			raise = append(raise, *inc)
		}
	}

	// Datasets.
	dsIDs := map[string]string{}
	mounts := map[string]string{}
	for _, d := range in.datasets {
		if d.Mountpoint != nil && *d.Mountpoint != "" {
			mounts[d.Name] = *d.Mountpoint
		}
		if d.hidden() {
			continue
		}
		id := in.dsID(d.Name)
		dsIDs[d.Name] = id
		kind := "share"
		if d.Type == "VOLUME" {
			kind = "volume"
		}
		r := integration.Resource{ID: id, Kind: kind, Name: d.Name, State: "online",
			Attrs:  map[string]any{"type": "dataset", "dataset": d.Name, "pool": d.Pool},
			Links:  []integration.Link{{Title: "TrueNAS", URL: in.web + "/ui/datasets/" + d.Name}},
			Health: integration.Health{Status: integration.HealthOK}}
		if d.Mountpoint != nil && *d.Mountpoint != "" {
			r.Attrs["path"] = *d.Mountpoint
		}
		usedB, okU := d.Used.bytes()
		availB, okA := d.Available.bytes()
		quota, okQ := d.Quota.bytes()
		pct := integration.Unknown()
		if okU {
			r.Attrs["used_gb"] = gb(usedB)
		}
		if okA {
			r.Attrs["available_gb"] = gb(availB)
		}
		if okQ && quota > 0 {
			r.Attrs["quota_gb"] = gb(quota)
		}
		if s, ok := d.SnapUsed.bytes(); ok {
			r.Attrs["snapshots_gb"] = gb(s)
		}
		if okU && okA && usedB+availB > 0 {
			pct = usedB / (usedB + availB) * 100
		}
		if d.Encrypted {
			r.Attrs["encrypted"] = true
		}
		switch {
		case d.Locked:
			r.State, r.Attrs["locked"] = "locked", true
			r.Health = integration.Health{Status: integration.HealthUnknown, Reason: "locked: its encryption key isn't loaded"}
		case pct >= dsWarn:
			reason := fmt.Sprintf("%.0f%% used", pct)
			if okQ && quota > 0 {
				reason = fmt.Sprintf("%.0f%% of its quota used", pct)
			}
			r.Health = integration.Health{Status: integration.HealthAttention, Reason: reason}
		}
		f.Resources = append(f.Resources, r)
		parent := poolIDs[d.Pool]
		if parent == "" {
			parent = host
		}
		f.Relations = append(f.Relations, integration.Relation{From: id, Type: "part_of", To: parent})
		f.Metrics = append(f.Metrics, integration.Metric{Resource: id, Key: "used.pct", Unit: "%", Min: 0, Max: 100,
			Band: [2]float64{0, 80}, Warn: dsWarn, Crit: dsCrit, Value: round(pct, 1)})
	}

	// Disks.
	tempsFresh := in.temps != nil && now.Sub(in.tempsAt) <= tempMaxAge
	for _, d := range in.disks {
		id := in.diskID(d)
		r := integration.Resource{ID: id, Kind: "disk", Name: d.Name, State: "online",
			Attrs: map[string]any{"device": d.Name}, Health: integration.Health{Status: integration.HealthOK}}
		if d.Serial != "" {
			r.Attrs["serial"] = d.Serial
		}
		if m := strings.TrimSpace(ptr(d.Model)); m != "" {
			r.Attrs["model"] = m
		}
		if d.Size.ok && d.Size.v > 0 {
			r.Attrs["size_tb"] = tb(d.Size.v)
		}
		if t := ptr(d.Type); t != "" {
			r.Attrs["media"] = strings.ToLower(t)
		}
		rotational := d.RotationRate != nil && *d.RotationRate > 0
		r.Attrs["rotational"] = rotational
		if rotational {
			r.Attrs["rpm"] = *d.RotationRate
		}
		if d.Bus != "" {
			r.Attrs["bus"] = strings.ToLower(d.Bus)
		}
		pname := ptr(d.Pool)
		l, inPool := leaves[d.Name]
		role := "unassigned"
		switch {
		case inPool:
			role, pname = l.role, l.pool
		case pname == "boot-pool":
			role = "boot"
		}
		r.Attrs["role"] = role
		if pname != "" {
			r.Attrs["pool"] = pname
		}
		var reasons []string
		status := integration.HealthOK
		worse := func(s string) {
			if s == integration.HealthDown || status == integration.HealthOK {
				status = s
			}
		}
		if inPool {
			r.Attrs["status"] = l.status
			r.Attrs["errors"] = l.errors
			switch l.status {
			case "ONLINE":
			case "DEGRADED":
				worse(integration.HealthAttention)
				reasons = append(reasons, "degraded in "+l.pool)
			default:
				worse(integration.HealthDown)
				r.State = strings.ToLower(l.status)
				reasons = append(reasons, strings.ToLower(l.status)+" in "+l.pool)
			}
			if l.errors > 0 {
				worse(integration.HealthAttention)
				reasons = append(reasons, plural(l.errors, "ZFS error"))
			}
		}
		issue, bad := smart[d.Serial]
		if !bad {
			issue, bad = smart[d.Name]
		}
		if in.have["alerts"] {
			if bad {
				r.Attrs["smart"] = issue.message
				if issue.down {
					worse(integration.HealthDown)
				} else {
					worse(integration.HealthAttention)
				}
				reasons = append(reasons, "SMART: "+issue.message)
			} else {
				r.Attrs["smart"] = "ok"
			}
		}
		if status != integration.HealthOK {
			r.Health = integration.Health{Status: status, Reason: strings.Join(reasons, " · ")}
		}
		f.Resources = append(f.Resources, r)
		to := host
		if pid, ok := poolIDs[pname]; ok {
			to = pid
		}
		f.Relations = append(f.Relations, integration.Relation{From: id, Type: "part_of", To: to})
		if in.temps != nil {
			s := ssdTemp
			if rotational {
				s = hddTemp
			}
			v := integration.Unknown()
			if t, ok := in.temps[d.Name]; ok && tempsFresh {
				v = t
			}
			f.Metrics = append(f.Metrics, integration.Metric{Resource: id, Key: "temp.c", Unit: "°C", Min: s.min, Max: s.max,
				Band: s.band, Warn: s.warn, Crit: s.crit, Value: v})
		}
	}

	// Apps.
	for _, a := range in.apps {
		id := in.pre() + "app/" + slug(a.Name)
		r := integration.Resource{ID: id, Kind: "app", Name: cmp.Or(a.Metadata.Title, a.Name), State: strings.ToLower(a.State),
			Attrs: map[string]any{"type": "truenas_app", "app": a.Name}, Health: integration.Health{Status: integration.HealthOK}}
		if a.Metadata.Name != "" {
			r.Attrs["catalog_app"] = a.Metadata.Name
		}
		if a.Metadata.Train != "" {
			r.Attrs["train"] = a.Metadata.Train
		}
		if a.Metadata.AppVersion != "" {
			r.Attrs["version"] = a.Metadata.AppVersion
		}
		if a.Version != "" {
			r.Attrs["app_version"] = a.Version
		}
		if a.CustomApp {
			r.Attrs["custom"] = true
		}
		r.Attrs["update_available"] = a.UpgradeAvailable || a.ImageUpdates
		if a.UpgradeAvailable && a.LatestVersion != "" {
			r.Attrs["latest_version"] = a.LatestVersion
		}
		switch a.State {
		case "CRASHED":
			r.Health = integration.Health{Status: integration.HealthDown, Reason: "crashed"}
		case "STOPPED":
			r.Desired = "stopped"
		case "RUNNING":
			r.Desired = "running"
		}
		for _, name := range slices.Sorted(maps.Keys(a.Portals)) {
			r.Links = append(r.Links, integration.Link{Title: name, URL: a.Portals[name]})
		}
		r.Links = append(r.Links, integration.Link{Title: "TrueNAS", URL: in.web + "/ui/apps/installed"})
		f.Resources = append(f.Resources, r)
		f.Relations = append(f.Relations, integration.Relation{From: id, Type: "runs_on", To: host})
	}

	// Periodic snapshot tasks.
	for _, s := range in.snaps {
		id := in.snapID(s.ID)
		known = append(known, "snapshot."+strconv.Itoa(s.ID)+".failed")
		r := integration.Resource{ID: id, Kind: "snapshot_task", Name: s.Dataset + " snapshots", State: stateName(s.State.State, s.Enabled),
			Attrs: map[string]any{"dataset": s.Dataset, "recursive": s.Recursive, "enabled": s.Enabled,
				"schedule": s.Schedule.String(), "retention": lifetime(s.LifetimeValue, s.LifetimeUnit)},
			Links:  []integration.Link{{Title: "TrueNAS", URL: in.web + "/ui/data-protection/snapshot"}},
			Health: integration.Health{Status: integration.HealthOK}}
		if !s.State.Datetime.IsZero() {
			r.Attrs["last_at"] = s.State.Datetime.UTC()
		}
		if s.State.LastSnapshot != "" {
			r.Attrs["last_snapshot"] = s.State.LastSnapshot
		}
		if s.Enabled && s.State.State == "ERROR" {
			r.Attrs["error"] = s.State.Error
			r.Health = integration.Health{Status: integration.HealthAttention, Reason: "failed " + hhmm(s.State.Datetime.Time) + ": " + s.State.Error}
			raise = append(raise, integration.Incident{
				ID: "snapshot." + strconv.Itoa(s.ID) + ".failed", Scenario: "truenas-snapshot-failed", Class: integration.ClassProblem, Severity: integration.SeverityAttention,
				Title: "Snapshots of " + s.Dataset + " failed", Summary: fmt.Sprintf("TrueNAS's periodic snapshot task for %s failed at %s: %s", s.Dataset, hhmm(s.State.Datetime.Time), s.State.Error),
				Target: id, OpenedAt: s.State.Datetime.UTC(), Facts: map[string]any{"dataset": s.Dataset, "error": s.State.Error},
				Causes:       []integration.Cause{{Text: s.State.Error, Resource: id, At: s.State.Datetime.UTC()}},
				Suggested:    integration.Action{ID: "link.open", Label: "Open the snapshot tasks in TrueNAS", Tier: "T0", Target: id},
				ResolvesWhen: "Resolves when the task's next run succeeds.",
			})
		}
		f.Resources = append(f.Resources, r)
		to := dsIDs[s.Dataset]
		if to == "" {
			to = cmp.Or(poolIDs[s.Dataset], host)
		}
		f.Relations = append(f.Relations, integration.Relation{From: id, Type: "part_of", To: to})
	}
	for name, id := range dsIDs {
		if snapshotted(name, in.snaps) {
			for i := range f.Resources {
				if f.Resources[i].ID == id {
					f.Resources[i].Attrs["snapshots"] = true
				}
			}
		}
	}

	// Replication tasks: backup targets.
	stateChanged := false
	for _, t := range in.repls {
		id := in.replID(t.ID)
		known = append(known, "replication."+strconv.Itoa(t.ID)+".failed")
		r, covered := in.replicationResource(t, id, dsIDs, mounts, hr)
		k := strconv.Itoa(t.ID)
		if t.State.State == "FINISHED" && !t.State.Datetime.IsZero() {
			if ms := t.State.Datetime.UnixMilli(); in.lastOK[k] != ms {
				in.lastOK[k], stateChanged = ms, true
			}
		}
		if ms, ok := in.lastOK[k]; ok {
			r.Attrs["backup.last_ok"] = time.UnixMilli(ms).UTC().Format(time.RFC3339)
		}
		if t.Enabled && t.State.State == "ERROR" {
			raise = append(raise, in.replicationIncident(t, id, covered))
		}
		f.Resources = append(f.Resources, r)
		f.Relations = append(f.Relations, integration.Relation{From: id, Type: "part_of", To: host})
	}
	// Forget tasks that are gone.
	for k := range in.lastOK {
		if !slices.ContainsFunc(in.repls, func(t replication) bool { return strconv.Itoa(t.ID) == k }) && in.have["replication"] {
			delete(in.lastOK, k)
			stateChanged = true
		}
	}
	if stateChanged {
		in.saveLastOK(ctx)
	}

	// Other alerts at ERROR or worse.
	for _, a := range in.alerts {
		if a.Dismissed || ownAlert(a.Klass) || levelRank[a.Level] < levelRank["ERROR"] || a.UUID == "" {
			continue
		}
		msg := a.message()
		title := msg
		if i := strings.IndexAny(title, ".:"); i > 0 && i < 100 {
			title = title[:i]
		}
		if len(title) > 100 {
			title = title[:100] + "…"
		}
		raise = append(raise, integration.Incident{
			ID: "alert." + slug(a.UUID), Scenario: "truenas-alert", Class: integration.ClassProblem, Severity: integration.SeverityAttention,
			Title: "TrueNAS: " + title, Summary: msg, Target: host, OpenedAt: a.Datetime.UTC(),
			Facts:        map[string]any{"klass": a.Klass, "level": a.Level, "source": a.Source},
			Suggested:    integration.Action{ID: "link.open", Label: "Open TrueNAS's alerts", Tier: "T0", Target: host},
			ResolvesWhen: "Resolves when TrueNAS clears or dismisses the alert.",
		})
	}
	if active > 0 {
		hr.Health = integration.Health{Status: integration.HealthAttention, Reason: plural(active, "active alert")}
	}
	f.Resources = append([]integration.Resource{hr}, f.Resources...)

	if err := in.d.Emit.Replace("", f); err != nil {
		return err
	}
	raised := map[string]bool{}
	for _, inc := range raise {
		if err := in.d.Emit.Raise(inc); err != nil {
			return err
		}
		raised[inc.ID] = true
	}
	at := now.Local().Format("15:04")
	for _, id := range slices.Compact(slices.Sorted(slices.Values(append(known, mapsKeys(in.raised)...)))) {
		if raised[id] {
			continue
		}
		if err := in.d.Emit.Resolve(id, "No longer the case (seen at "+at+")."); err != nil {
			return err
		}
	}
	in.raised = raised
	in.d.Emit.SetVersion(in.version)
	in.d.Emit.SetWarnings(in.warnings())
	return nil
}

func mapsKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// snapshotted: an enabled snapshot task covers the dataset, itself or
// through a recursive task on a parent.
func snapshotted(name string, tasks []snapshotTask) bool {
	for _, s := range tasks {
		if !s.Enabled {
			continue
		}
		if s.Dataset == name || s.Recursive && strings.HasPrefix(name, s.Dataset+"/") {
			return true
		}
	}
	return false
}

func stateName(s string, enabled bool) string {
	if !enabled {
		return "disabled"
	}
	switch s {
	case "RUNNING":
		return "running"
	case "ERROR":
		return "error"
	case "HOLD":
		return "held"
	case "WAITING":
		return "waiting"
	}
	return "idle"
}

func lifetime(n int, unit string) string {
	if n <= 0 || unit == "" {
		return ""
	}
	u := strings.ToLower(unit)
	if n != 1 {
		u += "s"
	}
	return fmt.Sprintf("%d %s", n, u)
}

// poolFacts is a pool's resource, its incident when it isn't healthy, and
// where its disks sit.
func (in *instance) poolFacts(p pool, id string, now time.Time) (integration.Resource, *integration.Incident, map[string]leafState) {
	r := integration.Resource{ID: id, Kind: "pool", Name: p.Name, State: strings.ToLower(cmp.Or(p.Status, "unknown")),
		Attrs:  map[string]any{"type": "zfs", "status": p.Status},
		Links:  []integration.Link{{Title: "TrueNAS", URL: in.web + "/ui/storage"}},
		Caps:   []string{"storage.pools"},
		Health: integration.Health{Status: integration.HealthOK}}
	if p.Path != "" {
		r.Attrs["path"] = p.Path
	}
	if p.Size.ok && p.Size.v > 0 {
		r.Attrs["size_tb"] = tb(p.Size.v)
	}
	if p.Allocated.ok {
		r.Attrs["used_tb"] = tb(p.Allocated.v)
	}
	if p.Free.ok {
		r.Attrs["free_tb"] = tb(p.Free.v)
	}
	if p.Fragment.ok {
		r.Attrs["fragmentation_pct"] = p.Fragment.v
	}
	if s := p.Scan; s != nil {
		switch {
		case s.State == "SCANNING" && s.Function == "RESILVER":
			r.State = "resilvering"
			r.Attrs["resilver_pct"] = round(s.Percentage.v, 1)
		case s.State == "SCANNING":
			r.Attrs["scrub_pct"] = round(s.Percentage.v, 1)
		}
		if s.Function == "SCRUB" {
			r.Attrs["scrub_state"] = strings.ToLower(s.State)
			if !s.EndTime.IsZero() {
				r.Attrs["scrub_last"] = s.EndTime.UTC()
			}
			if s.Errors.ok {
				r.Attrs["scrub_errors"] = int(s.Errors.v)
			}
		}
	}

	leaves := map[string]leafState{}
	var bad []vdev // disks that aren't ONLINE or have errors
	var layout []string
	disks := 0
	if t := p.Topology; t != nil {
		groups := []struct {
			role string
			vs   []vdev
		}{{"data", t.Data}, {"log", t.Log}, {"cache", t.Cache}, {"spare", t.Spare}, {"special", t.Special}, {"dedup", t.Dedup}}
		for _, g := range groups {
			for _, v := range g.vs {
				if g.role == "data" {
					layout = append(layout, v.Type)
				}
				for _, l := range v.leaves() {
					disks++
					name := ptr(l.Disk)
					if name != "" {
						leaves[name] = leafState{pool: p.Name, role: g.role, status: l.Status, errors: l.errors()}
					}
					if (l.Status != "ONLINE" && !(g.role == "spare" && l.Status == "AVAIL")) || l.errors() > 0 {
						bad = append(bad, l)
					}
				}
			}
		}
	}
	if len(layout) > 0 {
		r.Attrs["layout"] = layoutText(layout)
	}
	r.Attrs["disks"] = disks

	detail := strings.TrimSpace(ptr(p.StatusDetail))
	if p.Status == "ONLINE" && p.Healthy && len(bad) == 0 {
		return r, nil, leaves
	}
	var causes []integration.Cause
	var badNames []string
	for _, l := range bad {
		name := cmp.Or(ptr(l.Disk), l.Name)
		badNames = append(badNames, name)
		text := fmt.Sprintf("%s is %s", name, l.Status)
		if l.errors() > 0 {
			text += fmt.Sprintf(": %d read, %d write and %d checksum errors", int(l.Stats.ReadErrors.v), int(l.Stats.WriteErrors.v), int(l.Stats.ChecksumErrors.v))
		}
		res := id
		for _, d := range in.disks {
			if d.Name == ptr(l.Disk) {
				res = in.diskID(d)
			}
		}
		causes = append(causes, integration.Cause{Text: text + ".", Resource: res})
	}
	sev, health := integration.SeverityAttention, integration.HealthAttention
	switch p.Status {
	case "ONLINE", "DEGRADED":
	default:
		sev, health = integration.SeverityDown, integration.HealthDown
	}
	reason := strings.ToLower(p.Status)
	if p.Status == "ONLINE" {
		reason = "unhealthy"
	}
	if len(badNames) > 0 {
		reason += ": " + joinAnd(badNames)
	}
	r.Health = integration.Health{Status: health, Reason: reason}
	if p.Status == "ONLINE" && p.Healthy {
		// Errors on a disk of a healthy pool: the disk says so, the pool
		// doesn't need an incident.
		r.Health = integration.Health{Status: integration.HealthOK}
		return r, nil, leaves
	}
	title := "Pool " + p.Name + " is " + strings.ToLower(p.Status)
	if p.Status == "ONLINE" {
		title = "Pool " + p.Name + " is unhealthy"
	}
	summary := title + "."
	if detail != "" {
		summary = title + ": " + detail
	}
	inc := &integration.Incident{
		ID: "pool." + slug(p.Name) + ".health", Scenario: "truenas-pool-degraded", Class: integration.ClassProblem, Severity: sev,
		Title: title, Summary: summary, Target: id, Causes: causes,
		Facts:        map[string]any{"pool": p.Name, "status": p.Status, "status_code": ptr(p.StatusCode), "disks": badNames},
		Suggested:    integration.Action{ID: "link.open", Label: "Open the pool in TrueNAS", Tier: "T0", Target: id},
		ResolvesWhen: "Resolves when TrueNAS reports the pool ONLINE and healthy.",
	}
	return r, inc, leaves
}

// layoutText: "RAIDZ2", "2 × MIRROR".
func layoutText(types []string) string {
	counts := map[string]int{}
	var order []string
	for _, t := range types {
		if counts[t] == 0 {
			order = append(order, t)
		}
		counts[t]++
	}
	var parts []string
	for _, t := range order {
		if counts[t] == 1 {
			parts = append(parts, t)
		} else {
			parts = append(parts, fmt.Sprintf("%d × %s", counts[t], t))
		}
	}
	return strings.Join(parts, " + ")
}

// replicationResource is a replication task as a backup target. A push
// covers its source datasets (and, recursive, the published datasets
// under them) and their paths on this host; a pull backs up another
// machine, so it covers nothing here.
func (in *instance) replicationResource(t replication, id string, dsIDs, mounts map[string]string, host integration.Resource) (integration.Resource, []string) {
	r := integration.Resource{ID: id, Kind: "backup_target", Name: cmp.Or(t.Name, "Replication "+strconv.Itoa(t.ID)),
		State: stateName(t.State.State, t.Enabled), Caps: []string{"backups.targets"},
		Attrs: map[string]any{"type": "zfs_replication", "direction": strings.ToLower(t.Direction), "transport": strings.ToLower(t.Transport),
			"source_datasets": t.SourceDatasets, "target_dataset": t.TargetDataset, "recursive": t.Recursive,
			"enabled": t.Enabled, "auto": t.Auto},
		Links:  []integration.Link{{Title: "TrueNAS", URL: in.web + "/ui/data-protection/replication"}},
		Health: integration.Health{Status: integration.HealthOK}}
	if t.SSH != nil && t.SSH.Attributes.Host != "" {
		r.Attrs["remote"] = t.SSH.Attributes.Host
	}
	if t.Retention != "" {
		r.Attrs["retention"] = strings.ToLower(t.Retention)
	}
	if s := t.schedule(); s != "" {
		r.Attrs["schedule"] = s
		r.Attrs["backup.schedule"] = s
	}
	if t.State.LastSnapshot != "" {
		r.Attrs["last_snapshot"] = t.State.LastSnapshot
	}
	switch t.State.State {
	case "FINISHED":
		r.Attrs["backup.last_at"] = t.State.Datetime.UTC().Format(time.RFC3339)
		r.Attrs["backup.last_status"] = "ok"
	case "ERROR":
		r.Attrs["backup.last_at"] = t.State.Datetime.UTC().Format(time.RFC3339)
		r.Attrs["backup.last_status"] = "failed"
		r.Attrs["backup.last_error"] = firstLine(t.State.Error)
		if t.Enabled {
			r.Health = integration.Health{Status: integration.HealthAttention, Reason: "failed " + hhmm(t.State.Datetime.Time) + ": " + firstLine(t.State.Error)}
		}
	case "HOLD":
		if t.Enabled {
			r.Health = integration.Health{Status: integration.HealthAttention, Reason: "on hold: " + firstLine(t.State.Reason)}
		}
	}

	var covered []string
	if t.Direction != "PUSH" {
		return r, covered
	}
	var covers []any
	addr := ""
	if ip, ok := host.Attrs["ip"].(string); ok {
		addr = ip
	} else if h, ok := host.Attrs["hostname"].(string); ok {
		addr = h
	}
	add := func(e map[string]any) {
		if len(covers) < maxCovers {
			covers = append(covers, e)
		}
	}
	excluded := func(name string) bool {
		for _, x := range t.Exclude {
			if name == x || strings.HasPrefix(name, x+"/") {
				return true
			}
		}
		return false
	}
	for _, src := range t.SourceDatasets {
		names := []string{src}
		if t.Recursive {
			for _, n := range slices.Sorted(slices.Values(mapsKeys(dsIDs))) {
				if strings.HasPrefix(n, src+"/") && !excluded(n) {
					names = append(names, n)
				}
			}
		}
		for _, n := range names {
			if rid, ok := dsIDs[n]; ok {
				add(map[string]any{"resource": rid})
				covered = append(covered, rid)
			}
		}
		if m := mounts[src]; m != "" && addr != "" && t.Recursive {
			// Containers on this host whose every mount is under it.
			add(map[string]any{"host": addr, "path": m})
		}
	}
	if len(covers) > 0 {
		r.Attrs["backup.covers"] = covers
	}
	return r, covered
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

// replicationIncident is the problem for a failed replication task.
func (in *instance) replicationIncident(t replication, id string, covered []string) integration.Incident {
	at := t.State.Datetime.UTC()
	where := t.TargetDataset
	if t.SSH != nil && t.SSH.Attributes.Host != "" {
		where = t.SSH.Attributes.Host + ":" + t.TargetDataset
	}
	inc := integration.Incident{
		ID: "replication." + strconv.Itoa(t.ID) + ".failed", Scenario: "truenas-replication-failed", Class: integration.ClassProblem, Severity: integration.SeverityAttention,
		Title:   "Replication " + cmp.Or(t.Name, strconv.Itoa(t.ID)) + " failed",
		Summary: fmt.Sprintf("Replicating %s to %s failed at %s: %s", joinAnd(t.SourceDatasets), where, hhmm(at), firstLine(t.State.Error)),
		Target:  id, OpenedAt: at,
		Facts: map[string]any{"task": t.ID, "sources": t.SourceDatasets, "target": t.TargetDataset, "error": firstLine(t.State.Error),
			"direction": t.Direction, "transport": t.Transport},
		Causes:       []integration.Cause{{Text: firstLine(t.State.Error), Resource: id, At: at}},
		Suggested:    integration.Action{ID: "link.open", Label: "Open the replication tasks in TrueNAS", Tier: "T0", Target: id},
		ResolvesWhen: "Resolves when the task's next run succeeds.",
	}
	if ms, ok := in.lastOK[strconv.Itoa(t.ID)]; ok {
		inc.Facts["last_ok"] = time.UnixMilli(ms).UTC().Format(time.RFC3339)
	}
	for _, c := range covered {
		inc.BlastRadius = append(inc.BlastRadius, integration.Impact{Resource: c, Via: "backed_up_by", Depth: 1})
	}
	return inc
}

// warnings are the sync-time warnings.
func (in *instance) warnings() []integration.Warning {
	var ws []integration.Warning
	for _, part := range slices.Sorted(slices.Values(mapsKeys(in.hidden))) {
		ws = append(ws, integration.Warning{Code: part + "_hidden", Severity: integration.WarnWarning,
			Message: fmt.Sprintf("%s aren't shown: TrueNAS denied them to the API key's user. Give it the Read-only Administrator privilege.", upperFirst(in.hidden[part]))})
	}
	ws = append(ws, in.notes...)
	if in.newer {
		ws = append(ws, newerWarning(in.version))
	}
	return ws
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
