package pbs

import (
	"cmp"
	"fmt"
	"math"
	"net/url"
	"slices"
	"strings"
	"time"

	"dash.cha.sh/internal/integration"
)

// Thresholds: the Demo Home's pbs:main series (warn 90, crit 96), and the
// estimate PBS itself shows on its dashboard.
const (
	usedWarn, usedCrit = 90, 96
	fullSoon           = 30 * 24 * time.Hour // "full in" under this is worth saying
	gcStale            = 7 * 24 * time.Hour  // GC older than this is a cause when a store fills up
	nodeMaxAge         = 3 * jobsEvery
)

func hhmm(t time.Time) string         { return t.Local().Format("Jan 2 15:04") }
func day(t time.Time) string          { return t.Local().Format("Jan 2") }
func unix(n *num) time.Time           { return time.Unix(int64(*n), 0).UTC() }
func tb(n num) float64                { return math.Round(float64(n)/1e12*100) / 100 }
func round1(v float64) float64        { return math.Round(v*10) / 10 }
func okState(s string) bool           { return s == "" || s == "OK" || strings.HasPrefix(s, "WARNINGS") }
func set(n *num) bool                 { return n != nil && *n > 0 }
func pctOf(a, b num) float64          { return math.Min(100, float64(a)/float64(b)*100) }
func storeLink(base, s string) string { return base + "/#DataStore-" + url.PathEscape(s) }

// inAbout says a duration the way the Demo Home does: "about 3 weeks".
func inAbout(d time.Duration) string {
	days := d.Hours() / 24
	switch {
	case days < 1.5:
		return "about a day"
	case days < 14:
		return fmt.Sprintf("about %.0f days", days)
	case days < 60:
		return fmt.Sprintf("about %.0f weeks", days/7)
	}
	return fmt.Sprintf("about %.0f months", days/30)
}

// retention is a prune job's keep settings: "keep-daily 7 · weekly 4 ·
// monthly 6".
func retention(k keepOptions) string {
	var parts []string
	for _, x := range []struct {
		name string
		n    num
	}{{"last", k.Last}, {"hourly", k.Hourly}, {"daily", k.Daily}, {"weekly", k.Weekly}, {"monthly", k.Monthly}, {"yearly", k.Yearly}} {
		if x.n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", x.name, int(x.n)))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "keep-" + strings.Join(parts, " · ")
}

// taskWhat names a task for people: "Verification of store-2".
func taskWhat(t task) string {
	kind := map[string]string{
		"verificationjob": "Verification", "verify": "Verification", "verify_group": "Verification", "verify_snapshot": "Verification",
		"garbage_collection": "Garbage collection", "prune": "Pruning", "prunejob": "Pruning", "syncjob": "Sync", "sync": "Sync",
		"backup": "Backup", "reader": "Restore", "tape-backup": "Tape backup",
	}[t.WorkerType]
	if kind == "" {
		kind = "Task " + t.WorkerType
	}
	if s := t.store(); s != "" {
		return kind + " of " + s
	}
	return kind
}

type storeFacts struct {
	u        datastoreUsage
	id       string
	used     float64 // NaN when unknown
	full     time.Time
	prune    string // enabled, disabled, none
	pruneJob *pruneJob
	gcLast   time.Time
	gcState  string
}

// publish emits the app, its datastores and their series, raises and
// resolves incidents, and reports the version and warnings.
func (in *instance) publish(now time.Time) error {
	self := in.d.Emit.Self()
	pre := ID + ":" + in.key + "/"
	appID := pre + "app"
	var f integration.Facts
	var raise []integration.Incident
	known := []string{"tasks.failed"}

	var failed []task
	for _, t := range in.tasks {
		if t.failed() && now.Sub(time.Unix(t.StartTime, 0)) <= taskWindow {
			failed = append(failed, t)
		}
	}
	slices.SortFunc(failed, func(a, b task) int { return cmp.Compare(b.StartTime, a.StartTime) })

	app := integration.Resource{ID: appID, Kind: "app", Name: cmp.Or(in.name, "Proxmox Backup Server"), State: "running", Desired: "running",
		Attrs: map[string]any{"type": ID, "version": in.version, "datastores": len(in.usage)},
		Links: []integration.Link{{Title: "Proxmox Backup Server", URL: in.base + "/"}},
		Caps:  []string{"backups.targets", "backups.jobs"}, Health: integration.Health{Status: integration.HealthOK}}
	cpu, mem := integration.Unknown(), integration.Unknown()
	if in.node != nil {
		if in.node.CPUInfo.Model != "" {
			app.Attrs["cpu_model"] = in.node.CPUInfo.Model
		}
		if in.node.Uptime > 0 {
			app.Attrs["boot_at"] = in.nodeAt.Add(-time.Duration(in.node.Uptime) * time.Second).UTC().Truncate(time.Minute)
		}
		if now.Sub(in.nodeAt) <= nodeMaxAge {
			cpu = float64(in.node.CPU) * 100
			if in.node.Memory.Total > 0 {
				mem = pctOf(in.node.Memory.Used, in.node.Memory.Total)
			}
		}
	}
	failedCount := integration.Unknown()
	if in.haveTasks {
		failedCount = float64(len(failed))
	}
	if len(failed) > 0 {
		app.Health = integration.Health{Status: integration.HealthAttention, Reason: plural(len(failed), "task") + " failed in the last 24 h"}
	}
	f.Resources = append(f.Resources, app)
	f.Relations = append(f.Relations, integration.Relation{From: self, Type: "monitors", To: appID})
	f.Metrics = append(f.Metrics,
		integration.Metric{Resource: appID, Key: "tasks.failed_24h", Unit: "count", Min: 0, Max: 1000, Value: failedCount},
	)
	if in.node != nil {
		f.Metrics = append(f.Metrics,
			integration.Metric{Resource: appID, Key: "cpu.pct", Unit: "%", Min: 0, Max: 100, Band: [2]float64{0, 30}, Value: round1(cpu)},
			integration.Metric{Resource: appID, Key: "mem.pct", Unit: "%", Min: 0, Max: 100, Band: [2]float64{20, 80}, Warn: 90, Crit: 98, Value: round1(mem)})
	}

	// Datastores.
	var stores []*storeFacts
	for _, u := range in.usage {
		s := &storeFacts{u: u, id: pre + "datastore/" + u.Store, used: integration.Unknown(), prune: "none"}
		r := integration.Resource{ID: s.id, Kind: "backup_target", Name: u.Store, State: "online",
			Attrs: map[string]any{"store": u.Store}, Caps: []string{"backups.targets"},
			Links:  []integration.Link{{Title: "PBS", URL: storeLink(in.base, u.Store)}},
			Health: integration.Health{Status: integration.HealthOK}}
		if u.Backend != "" {
			r.Attrs["backend"] = u.Backend
		}
		if u.Total != nil && *u.Total > 0 && u.Used != nil {
			s.used = pctOf(*u.Used, *u.Total)
			r.Attrs["capacity_tb"] = tb(*u.Total)
			r.Attrs["used_tb"] = tb(*u.Used)
		}
		if set(u.FullDate) && unix(u.FullDate).After(now) {
			s.full = unix(u.FullDate)
			r.Attrs["full_at"] = s.full
			r.Attrs["full_in_days"] = int(s.full.Sub(now).Hours() / 24)
		}
		// Garbage collection: the job status, else the last run's UPID.
		if j, ok := in.gc[u.Store]; ok {
			if j.Schedule != "" {
				r.Attrs["gc_schedule"] = j.Schedule
			} else {
				r.Attrs["gc_schedule"] = "none"
			}
			if set(j.LastEndtime) {
				s.gcLast = unix(j.LastEndtime)
			}
			s.gcState = j.LastState
		} else if u.GC != nil {
			if at, ok := upidStart(u.GC.UPID); ok {
				s.gcLast = at
			}
		}
		if !s.gcLast.IsZero() {
			r.Attrs["gc_last"] = s.gcLast
		}
		if s.gcState != "" {
			r.Attrs["gc_state"] = s.gcState
		}
		// Prune jobs: an enabled one wins; a disabled one is worth saying.
		for i := range in.prune {
			j := &in.prune[i]
			if j.Store != u.Store {
				continue
			}
			if j.Disable == 0 {
				s.prune, s.pruneJob = "enabled", j
				break
			}
			if s.pruneJob == nil {
				s.prune, s.pruneJob = "disabled", j
			}
		}
		if in.havePrune {
			r.Attrs["prune_job"] = s.prune
		}
		if j := s.pruneJob; j != nil {
			if ret := retention(j.keepOptions); ret != "" {
				r.Attrs["retention"] = ret
			}
			if set(j.LastEndtime) {
				r.Attrs["prune_last"] = unix(j.LastEndtime)
			}
			if j.LastState != "" {
				r.Attrs["prune_state"] = j.LastState
			}
		}
		var verifyFail *verifyJob
		for i := range in.verify {
			j := &in.verify[i]
			if j.Store != u.Store {
				continue
			}
			if set(j.LastEndtime) {
				r.Attrs["verify_last"] = unix(j.LastEndtime)
			}
			if j.LastState != "" {
				r.Attrs["verify_state"] = j.LastState
			}
			if !okState(j.LastState) {
				verifyFail = j
			}
		}
		maint := ""
		if it, ok := in.stores[u.Store]; ok && it.Maintenance != "" {
			maint = it.Maintenance
			if t, ok := strings.CutPrefix(maint, "type="); ok {
				maint, _, _ = strings.Cut(t, ",")
			}
			r.Attrs["maintenance"] = maint
		}

		// Health, worst first.
		var reasons []string
		if s.used >= usedWarn {
			reasons = append(reasons, fmt.Sprintf("%.0f%% used", s.used))
		}
		if !s.full.IsZero() && s.full.Sub(now) < fullSoon {
			reasons = append(reasons, "full in "+inAbout(s.full.Sub(now)))
		}
		if !okState(s.gcState) {
			reasons = append(reasons, "garbage collection failed")
		}
		if j := s.pruneJob; j != nil && !okState(j.LastState) {
			reasons = append(reasons, "pruning failed")
		}
		if verifyFail != nil {
			reasons = append(reasons, "verification failed "+day(unix(verifyFail.LastEndtime)))
		}
		known = append(known, "datastore."+u.Store+".error", "filling."+u.Store)
		switch {
		case u.Error != "":
			r.State, r.Health = "error", integration.Health{Status: integration.HealthDown, Reason: u.Error}
			raise = append(raise, integration.Incident{
				ID: "datastore." + u.Store + ".error", Scenario: "pbs-datastore-error", Class: integration.ClassProblem, Severity: integration.SeverityDown,
				Title: "PBS datastore " + u.Store + " can't be read", Summary: fmt.Sprintf("Proxmox Backup Server reports an error for datastore %s: %s. Backups to it fail until it's fixed.", u.Store, u.Error),
				Target: s.id, Facts: map[string]any{"store": u.Store, "error": u.Error},
				Suggested:    integration.Action{ID: "link.open", Label: "Open the datastore in PBS", Tier: "T0", Target: s.id},
				ResolvesWhen: "Resolves when PBS reads the datastore again.",
			})
		case u.MountStatus == "notmounted":
			r.State, r.Health = "unmounted", integration.Health{Status: integration.HealthUnknown, Reason: "removable datastore not mounted"}
		case maint != "":
			r.State, r.Health = "maintenance", integration.Health{Status: integration.HealthMaintenance, Reason: "maintenance mode: " + maint}
		case len(reasons) > 0:
			r.Health = integration.Health{Status: integration.HealthAttention, Reason: strings.Join(reasons, " · ")}
		}
		f.Resources = append(f.Resources, r)
		f.Relations = append(f.Relations, integration.Relation{From: s.id, Type: "part_of", To: appID})
		f.Metrics = append(f.Metrics, integration.Metric{Resource: s.id, Key: "used.pct", Unit: "%", Min: 0, Max: 100,
			Band: [2]float64{0, 80}, Warn: usedWarn, Crit: usedCrit, Value: round1(s.used)})
		stores = append(stores, s)
	}

	raise = append(raise, in.fillingIncidents(stores, now)...)
	if len(failed) > 0 {
		raise = append(raise, tasksIncident(failed, appID, pre, stores))
	}

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
	for _, id := range append(known, mapKeys(in.raised)...) {
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

func mapKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func plural(n int, w string) string {
	if n == 1 {
		return "1 " + w
	}
	return fmt.Sprintf("%d %ss", n, w)
}

// fillingIncidents raise one problem per filesystem that's filling up.
// Datastores on one filesystem report identical totals (spike 4.1: five
// datastores, one disk, 91.9% each), so they're one problem, not five.
func (in *instance) fillingIncidents(stores []*storeFacts, now time.Time) []integration.Incident {
	type fsKey struct{ total, used, avail num }
	groups := map[fsKey][]*storeFacts{}
	var order []fsKey
	for _, s := range stores {
		if s.u.Error != "" || s.u.Total == nil || s.u.Used == nil {
			continue
		}
		soon := !s.full.IsZero() && s.full.Sub(now) < fullSoon
		if !(s.used >= usedWarn || soon) {
			continue
		}
		k := fsKey{*s.u.Total, *s.u.Used, 0}
		if s.u.Avail != nil {
			k.avail = *s.u.Avail
		}
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], s)
	}
	var out []integration.Incident
	for _, k := range order {
		g := groups[k]
		slices.SortFunc(g, func(a, b *storeFacts) int { return cmp.Compare(a.u.Store, b.u.Store) })
		first := g[0]
		names := make([]string, len(g))
		for i, s := range g {
			names[i] = s.u.Store
		}
		facts := map[string]any{"used_pct": math.Round(first.used), "capacity_tb": tb(k.total), "used_tb": tb(k.used), "stores": names}
		size := fmt.Sprintf("%.0f%% full (%.2f of %.2f TB)", first.used, tb(k.used), tb(k.total))
		var summary, title string
		if len(g) == 1 {
			title = "PBS datastore " + first.u.Store + " is filling up"
			summary = fmt.Sprintf("Datastore %s is %s.", first.u.Store, size)
		} else {
			title = "PBS datastores are filling up"
			summary = fmt.Sprintf("Datastores %s share one filesystem, which is %s.", joinAnd(names), size)
		}
		if !first.full.IsZero() {
			facts["full_at"] = first.full
			facts["full_in_days"] = int(first.full.Sub(now).Hours() / 24)
			summary += " At this rate it fills up in " + inAbout(first.full.Sub(now)) + "."
		}
		inc := integration.Incident{
			ID: "filling." + first.u.Store, Scenario: "pbs-datastore-filling", Class: integration.ClassProblem, Severity: integration.SeverityAttention,
			Title: title, Summary: summary, Target: first.id, Facts: facts,
			Suggested:    integration.Action{ID: "link.open", Label: "Open the datastore in PBS", Tier: "T0", Target: first.id},
			ResolvesWhen: fmt.Sprintf("Resolves when usage drops below %d%% and it's no longer due to fill up within 30 days.", usedWarn),
		}
		var noPrune []string
		for _, s := range g {
			if s.prune == "disabled" {
				text := fmt.Sprintf("The prune job %s on %s is disabled, so snapshots outside its retention policy stay.", s.pruneJob.ID, s.u.Store)
				if set(s.pruneJob.LastEndtime) {
					text = fmt.Sprintf("The prune job %s on %s is disabled; it last ran %s, so snapshots outside its retention policy have piled up since.", s.pruneJob.ID, s.u.Store, day(unix(s.pruneJob.LastEndtime)))
				}
				inc.Causes = append(inc.Causes, integration.Cause{Text: text, Resource: s.id})
			}
			if s.prune == "none" && in.havePrune {
				noPrune = append(noPrune, s.u.Store)
			}
			if !s.gcLast.IsZero() && now.Sub(s.gcLast) > gcStale {
				inc.Causes = append(inc.Causes, integration.Cause{Text: fmt.Sprintf("Garbage collection on %s last ran %s; space from pruned snapshots isn't freed until it runs.", s.u.Store, day(s.gcLast)), Resource: s.id, At: s.gcLast})
			}
		}
		if len(noPrune) > 0 {
			verb := "has"
			if len(noPrune) > 1 {
				verb = "have"
			}
			inc.Causes = append(inc.Causes, integration.Cause{Text: fmt.Sprintf("%s %s no prune job.", joinAnd(noPrune), verb), Resource: storeID(stores, noPrune[0])})
		}
		out = append(out, inc)
	}
	return out
}

func storeID(stores []*storeFacts, name string) string {
	for _, s := range stores {
		if s.u.Store == name {
			return s.id
		}
	}
	return ""
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

// tasksIncident is the problem for tasks that failed in the last day.
func tasksIncident(failed []task, appID, pre string, stores []*storeFacts) integration.Incident {
	var list []any
	var lines []string
	for i, t := range failed {
		at := time.Unix(t.StartTime, 0).UTC()
		list = append(list, map[string]any{"type": t.WorkerType, "id": t.WorkerID, "status": t.Status, "at": at.Format(time.RFC3339)})
		if i < 5 {
			lines = append(lines, fmt.Sprintf("%s failed at %s: %s.", taskWhat(t), hhmm(at), t.Status))
		}
	}
	target := appID
	if id := storeID(stores, failed[0].store()); id != "" {
		target = id
	}
	title := taskWhat(failed[0]) + " failed"
	if len(failed) > 1 {
		title = fmt.Sprintf("%d PBS tasks failed", len(failed))
	}
	at := time.Unix(failed[len(failed)-1].StartTime, 0).UTC()
	inc := integration.Incident{
		ID: "tasks.failed", Scenario: "pbs-task-failed", Class: integration.ClassProblem, Severity: integration.SeverityAttention,
		Title: title, Summary: strings.Join(lines, " "), Target: target, OpenedAt: at,
		Facts:        map[string]any{"failed": len(failed), "tasks": list},
		Suggested:    integration.Action{ID: "pbs.task.log", Label: "View the failed task's log", Tier: "T0", Target: appID},
		Alternatives: []integration.Action{{ID: "link.open", Label: "Open Proxmox Backup Server", Tier: "T0", Target: appID}},
		ResolvesWhen: "Resolves when no task has failed for 24 hours.",
	}
	for _, t := range failed {
		res := appID
		if id := storeID(stores, t.store()); id != "" {
			res = id
		}
		inc.Causes = append(inc.Causes, integration.Cause{Text: taskWhat(t) + ": " + t.Status, Resource: res, At: time.Unix(t.StartTime, 0).UTC()})
		if len(inc.Causes) == 5 {
			break
		}
	}
	return inc
}

// upidStart reads a UPID's start time:
// UPID:node:pid:pstart:task_id:starttime:type:id:user:, times in hex.
func upidStart(upid string) (time.Time, bool) {
	f := strings.Split(upid, ":")
	if len(f) < 6 || f[0] != "UPID" {
		return time.Time{}, false
	}
	var t int64
	if _, err := fmt.Sscanf(f[5], "%X", &t); err != nil {
		return time.Time{}, false
	}
	return time.Unix(t, 0).UTC(), true
}

// warnings are the sync-time warnings.
func (in *instance) warnings() []integration.Warning {
	var ws []integration.Warning
	what := map[string]struct{ code, text string }{
		"tasks":      {"tasks_hidden", "Failed tasks"},
		"datastores": {"datastores_hidden", "Maintenance modes"},
		"jobs":       {"jobs_hidden", "Garbage collection, prune and verify jobs"},
		"node":       {"node_status_hidden", "The server's CPU and memory"},
	}
	for _, part := range mapKeys(boolKeys(in.hidden)) {
		w := what[part]
		msg := w.text + " aren't shown: PBS refused the token"
		if r := in.hidden[part]; r != "" {
			msg += " (" + r + ")"
		}
		ws = append(ws, integration.Warning{Code: w.code, Severity: integration.WarnWarning, Message: msg + ". Grant it the Audit role on /."})
	}
	if !in.haveTasks && in.hidden["tasks"] == "" && !in.perms.has("/system/tasks", "Sys.Audit") {
		ws = append(ws, tasksHidden())
	}
	ws = append(ws, in.notes...)
	if w := integration.WriteGrantWarning(in.perms.grants()); w != nil {
		ws = append(ws, *w)
	}
	if in.newer {
		ws = append(ws, newerWarning(in.version))
	}
	return ws
}

func boolKeys(m map[string]string) map[string]bool {
	out := map[string]bool{}
	for k := range m {
		out[k] = true
	}
	return out
}
