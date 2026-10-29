package sim

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
)

// Findings as the v0.1 integrations report them: the same incident IDs (<instance id>.<local id>), scenarios,
// facts and wording as integrations/proxmox, integrations/unraid,
// integrations/docker, integrations/traefik and integrations/frigate, and
// the causes internal/correlate adds across instances.

func (s *Sim) dayHHMM(t time.Time) string { return s.shown(t).Format("Jan 2 15:04") }

// guestsOf is a job's guests, as a backup incident's blast radius.
func guestsOf(job string) []Impact {
	var out []Impact
	for _, sl := range backupSlots {
		if sl.job == job {
			out = append(out, Impact{Resource: sl.guest, Via: RelBackedUpBy, Depth: 1})
		}
	}
	return out
}

// jobEnd is when a job's scheduled run on day ends, as the slots lay it out.
func jobEnd(job string, day time.Time) time.Time {
	var end time.Time
	for _, sl := range backupSlots {
		if sl.job == job && sl.end(day).After(end) {
			end = sl.end(day)
		}
	}
	return end
}

// runHistory is the Proxmox integration's sentence about a job's recent
// runs and its last good one.
func (s *Sim) runHistory(h jobHistory) string {
	out := ""
	switch {
	case h.runs <= 1:
	case h.failed == h.runs:
		out = fmt.Sprintf(" It failed all of the last %d runs.", h.runs)
	default:
		out = fmt.Sprintf(" It failed %d of the last %d runs.", h.failed, h.runs)
	}
	if h.lastOK.IsZero() {
		return out + " No run in the recent tasks succeeded completely."
	}
	return out + " The last good run ended " + s.dayHHMM(h.lastOK) + "."
}

func backupFacts(job string, h jobHistory, err string) map[string]any {
	f := map[string]any{"job": job, "storage": pbsStorage, "task": "vzdump", "error": err, "runs": h.runs, "failed_runs": h.failed, "failed_at": h.failedAt}
	if !h.lastOK.IsZero() {
		f["last_ok"] = h.lastOK
	}
	return f
}

func jobSchedule(job string) string {
	return fmt.Sprintf("daily %02d:%02d", 2, int(jobOf(job).offset/time.Minute))
}

// openJobsAction and taskLogAction are what the Proxmox integration offers
// for a failed backup: v0.1 reads, so they open Proxmox.
func openJobsAction(job string) Action {
	return Action{ID: "link.open", Label: "Open the backup jobs in Proxmox", Tier: T0, Target: job}
}

func taskLogAction(job string) Action {
	return Action{ID: "pve.task.log", Label: "View the failed task's log", Tier: T0, Target: job}
}

const backupResolves = "Resolves when a run of this job succeeds: a retry, or the next scheduled run."

// ── backup-target-unreachable ────────────────────────────────────────────

// PBS on magpie doesn't answer wren's job one night ("No route to host"):
// wren's backups cross the LAN to magpie, and something on the way drops
// them. The run stops before any guest. It resolves with the next run that succeeds: the next
// night's, or a run started by hand (a fix).
type backupUnreachable struct{ job string }

const (
	pbsStorage  = "pbs-magpie" // the PVE storage every job writes to
	pbsEndpoint = ipMagpie + ":8007"
	unreachErr  = "could not activate storage '" + pbsStorage + "': " + pbsStorage + ": error fetching datastores - 500 Can't connect to " + pbsEndpoint + " (No route to host)"
)

func (backupUnreachable) lead() time.Duration { return 0 }

func (b backupUnreachable) night(in *instance) time.Time { return nightOf(b.job, in.open) }
func (b backupUnreachable) failAt(in *instance) time.Time {
	return jobBegin(b.job, b.night(in))
}

// retried reports whether a run started by hand comes before the next night's.
func (b backupUnreachable) retried(in *instance) bool {
	return !in.fix.IsZero() && in.fix.Before(jobBegin(b.job, b.night(in).Add(24*time.Hour)))
}

func (b backupUnreachable) horizon(in *instance) time.Time {
	if b.retried(in) {
		return manualRun(b.job, in.fix).end
	}
	return jobEnd(b.job, b.night(in).Add(24*time.Hour))
}

func (backupUnreachable) horizonOffset(open, fix time.Duration) time.Duration {
	if fix > 0 {
		return min(open+25*time.Hour, fix+time.Hour)
	}
	return open + 25*time.Hour
}

func (b backupUnreachable) changePoints(in *instance) []time.Time {
	return []time.Time{in.open, in.fix, b.horizon(in)}
}

func (b backupUnreachable) shapeNight(_ *Sim, in *instance, day time.Time, r *jobRun) {
	if r.job != b.job || !day.Equal(b.night(in)) {
		return
	}
	at := b.failAt(in)
	r.err = unreachErr
	for i := range r.guests {
		r.guests[i] = guestBackup{guest: r.guests[i].guest, begin: at, end: at, err: unreachErr}
	}
}

func (b backupUnreachable) manualRuns(_ *Sim, in *instance) []jobRun {
	if !b.retried(in) {
		return nil
	}
	return []jobRun{manualRun(b.job, in.fix)}
}

func (b backupUnreachable) apply(s *Sim, in *instance, w *world) {
	if within(w.t, in.open, b.horizon(in)) {
		at := b.failAt(in)
		setHealth(w.res[b.job], HealthAttention, fmt.Sprintf("couldn't reach %s at %s (No route to host)", pbsStorage, s.clock(at)), at)
	}
}

func (b backupUnreachable) incident(s *Sim, in *instance, t time.Time) *Incident {
	if t.Before(in.open) {
		return nil
	}
	j, at := jobOf(b.job), b.failAt(in)
	h := s.jobHistory(b.job, in.open)
	target := fmt.Sprintf("%s (%s)", pbsStorage, pbsEndpoint)
	inc := s.newIncident(in, ClassProblem, SeverityAttention, b.job)
	inc.Scenario, inc.Phase, inc.OpenedAt = "backup-target-unreachable", "open", at
	inc.Title = fmt.Sprintf("Backups on %s couldn't reach %s", j.where, pbsStorage)
	inc.Summary = fmt.Sprintf("%s's %s backup couldn't reach its storage %s on its %s run (No route to host), so nothing was backed up.%s",
		j.where, jobSchedule(b.job), target, s.dayHHMM(at), s.runHistory(h))
	inc.Facts = backupFacts(b.job, h, unreachErr)
	maps.Copy(inc.Facts, map[string]any{"cause": "target_unreachable", "endpoint": pbsEndpoint, "reason": "No route to host"})
	inc.Causes = []Cause{{Text: fmt.Sprintf("%s didn't answer at %s (No route to host). Other hosts that back up to it fail on the same nights.", target, s.clock(at)), Resource: b.job, At: at}}
	inc.BlastRadius = guestsOf(b.job)
	inc.Suggested = openJobsAction(b.job)
	inc.Alternatives = []Action{taskLogAction(b.job)}
	inc.ResolvesWhen = backupResolves
	if hz := b.horizon(in); !t.Before(hz) {
		resolve(inc, hz, "auto", fmt.Sprintf("The job's next run succeeded (seen at %s).", s.clock(hz)))
	}
	return inc
}

func (backupUnreachable) touches(string, string) bool { return false }

func (backupUnreachable) adjust(_ *Sim, _ *instance, _ *metricSpec, _ time.Time, v float64) float64 {
	return v
}

// ── backup-passthrough-conflict ──────────────────────────────────────────

// win-games and studio pass through the same GPU. studio has held it since
// yesterday evening, so backing up the stopped win-games, which has to start
// it, fails. studio's own backup writes 108 GB that night (the PBS datastore
// crosses 90% then). studio is shut down in the evening, and the next
// night's run succeeds. A fix shuts it down and runs the job by hand.
type backupPassthrough struct{}

const (
	ptJob     = "pve:owl/backup/daily"
	ptGuest   = "pve:owl/qemu/301" // win-games
	ptHolder  = "pve:owl/qemu/302" // studio
	ptOther   = "pve:owl/qemu/303" // homework
	ptDevice  = "0000:03:00.0"
	ptBusyErr = "PCI device '" + ptDevice + "' already in use by VMID '302'"
	ptBig     = 22 * time.Minute             // studio's backup that night, writing 108 GB
	ptStop    = 23*time.Hour + 5*time.Minute // studio shut down that evening
)

func (backupPassthrough) lead() time.Duration { return 30 * time.Minute }

func (backupPassthrough) night(in *instance) time.Time { return nightOf(ptJob, in.open) }

func (p backupPassthrough) retried(in *instance) bool {
	return !in.fix.IsZero() && in.fix.Before(jobBegin(ptJob, p.night(in).Add(24*time.Hour)))
}

// stopAt is when studio is shut down and gives the GPU back.
func (p backupPassthrough) stopAt(in *instance) time.Time {
	stop := p.night(in).Add(ptStop)
	if p.retried(in) {
		stop = minTime(stop, in.fix)
	}
	return stop
}

func (p backupPassthrough) horizon(in *instance) time.Time {
	if p.retried(in) {
		return manualRun(ptJob, in.fix.Add(time.Minute)).end
	}
	return jobEnd(ptJob, p.night(in).Add(24*time.Hour))
}

func (backupPassthrough) horizonOffset(open, fix time.Duration) time.Duration {
	if fix > 0 {
		return min(open+25*time.Hour, fix+time.Hour)
	}
	return open + 25*time.Hour
}

func (p backupPassthrough) changePoints(in *instance) []time.Time {
	return []time.Time{in.open, in.fix, p.stopAt(in), p.horizon(in)}
}

func (p backupPassthrough) shapeNight(_ *Sim, in *instance, day time.Time, r *jobRun) {
	if r.job != ptJob || !day.Equal(p.night(in)) {
		return
	}
	at := jobBegin(ptJob, day)
	*r.guest(ptGuest) = guestBackup{guest: ptGuest, begin: at, end: at, err: ptBusyErr}
	*r.guest(ptHolder) = guestBackup{guest: ptHolder, begin: at, end: at.Add(ptBig), ok: true}
	d := r.guest(ptOther)
	*d = guestBackup{guest: d.guest, begin: at.Add(ptBig), end: at.Add(ptBig + slotOf(d.guest).dur), ok: true}
}

func (p backupPassthrough) manualRuns(_ *Sim, in *instance) []jobRun {
	if !p.retried(in) {
		return nil
	}
	return []jobRun{manualRun(ptJob, in.fix.Add(time.Minute))}
}

func (p backupPassthrough) apply(s *Sim, in *instance, w *world) {
	t := w.t
	at := jobBegin(ptJob, p.night(in))
	if within(t, in.open, p.horizon(in)) {
		setHealth(w.res[ptJob], HealthAttention, fmt.Sprintf("win-games's backup failed at %s", s.clock(at)), at)
	}
	if g := w.res[ptHolder]; g != nil && !t.Before(p.stopAt(in)) {
		g.State = StateStopped
		delete(g.Attrs, "started_at")
		setHealth(g, HealthOK, "stopped (expected)", p.stopAt(in))
	}
}

func (p backupPassthrough) incident(s *Sim, in *instance, t time.Time) *Incident {
	if t.Before(in.open) {
		return nil
	}
	at := jobBegin(ptJob, p.night(in))
	h := s.jobHistory(ptJob, in.open)
	gpu := strings.TrimSuffix(ptDevice, ".0")
	inc := s.newIncident(in, ClassProblem, SeverityAttention, ptGuest)
	inc.Scenario, inc.Phase, inc.OpenedAt = "backup-passthrough-conflict", "open", at
	inc.Title = "Backup of win-games failed"
	inc.Summary = fmt.Sprintf("owl's %s backup failed for win-games on its %s run. studio, homework were backed up normally.%s", jobSchedule(ptJob), s.dayHHMM(at), s.runHistory(h))
	inc.Facts = backupFacts(ptJob, h, ptBusyErr)
	maps.Copy(inc.Facts, map[string]any{"vmid": 301, "pci_device": ptDevice, "in_use_by": 302})
	inc.Causes = []Cause{
		{Text: fmt.Sprintf("win-games and studio both pass through PCI device %s. Backing up the stopped win-games has to start it, and that fails while studio runs.", gpu), Resource: ptGuest, At: at},
		{Text: fmt.Sprintf("studio holds %s.", gpu), Resource: ptHolder},
	}
	inc.BlastRadius = guestsOf(ptJob)
	inc.Suggested = openJobsAction(ptJob)
	inc.Alternatives = []Action{taskLogAction(ptJob)}
	inc.ResolvesWhen = backupResolves
	if hz := p.horizon(in); !t.Before(hz) {
		resolve(inc, hz, "auto", fmt.Sprintf("The job's next run succeeded (seen at %s).", s.clock(hz)))
	}
	return inc
}

func (backupPassthrough) touches(string, string) bool { return false }

func (backupPassthrough) adjust(_ *Sim, _ *instance, _ *metricSpec, _ time.Time, v float64) float64 {
	return v
}

// ── guest-error ──────────────────────────────────────────────────────────

// stork's VM sandbox is started by hand and goes into internal-error a
// minute later. It resolves when sandbox leaves that state: a fix is the
// owner stopping it.
type guestError struct{}

const (
	geGuest = "pve:stork/qemu/221" // sandbox
	geBoot  = time.Minute
)

func (guestError) lead() time.Duration { return geBoot }

func (guestError) horizon(in *instance) time.Time { return in.fix }

func (guestError) horizonOffset(open, fix time.Duration) time.Duration {
	if fix == 0 {
		return -1
	}
	return fix
}

func (guestError) changePoints(in *instance) []time.Time {
	return []time.Time{in.open.Add(-geBoot), in.open, in.fix}
}

func (guestError) apply(s *Sim, in *instance, w *world) {
	t, g := w.t, w.res[geGuest]
	if g == nil || t.Before(in.open.Add(-geBoot)) {
		return
	}
	switch {
	case in.fixed(t):
		setHealth(g, HealthOK, "stopped (expected)", in.fix)
	case t.Before(in.open):
		g.State, g.Desired = StateRunning, ""
		g.Attrs["started_at"] = in.open.Add(-geBoot)
		setHealth(g, HealthOK, "", in.open.Add(-geBoot))
	default:
		g.State, g.Desired = StateError, ""
		g.Attrs["pve_status"] = "internal-error"
		setHealth(g, HealthDown, "internal-error: its QEMU process stopped responding", in.open)
	}
}

func (guestError) incident(s *Sim, in *instance, t time.Time) *Incident {
	if t.Before(in.open) {
		return nil
	}
	started := in.open.Add(-geBoot)
	inc := s.newIncident(in, ClassProblem, SeverityDown, geGuest)
	inc.Scenario, inc.Phase = "guest-error", "open"
	inc.Title = "sandbox is in internal-error"
	inc.Summary = "VM 221 (sandbox) on stork is in internal-error: its QEMU process stopped responding."
	inc.Facts = map[string]any{"vmid": 221, "node": "stork", "status": "internal-error"}
	inc.Causes = []Cause{{Text: fmt.Sprintf("The last task on it was qmstart at %s (OK).", s.dayHHMM(started)), Resource: geGuest, At: started}}
	inc.BlastRadius = []Impact{} // nothing runs on it
	inc.Suggested = Action{ID: "link.open", Label: "Open sandbox in Proxmox", Tier: T0, Target: geGuest}
	inc.ResolvesWhen = "Resolves when sandbox leaves its error state."
	if in.fixed(t) {
		resolve(inc, in.fix, "auto", fmt.Sprintf("The guest left its error state (seen at %s).", s.clock(in.fix)))
	}
	return inc
}

func (guestError) touches(string, string) bool { return false }

func (guestError) adjust(_ *Sim, _ *instance, _ *metricSpec, _ time.Time, v float64) float64 {
	return v
}

// ── unraid unsupported_version ───────────────────────────────────────────

// Unraid's API reports a version older than 4.37.4, which the Unraid
// integration refuses (an authorization bug lets any key change settings).
// It reads nothing else meanwhile, so magpie's tiles keep their values as
// of the moment it stopped. The Demo Home shows it as an afternoon episode
// so the rest of the day's Unraid data is live. A fix is the owner updating
// the API: the integration reconnects at its next retry.
type unraidAPI struct{}

const (
	urInt   = unraidSrc
	urRetry = 5 * time.Minute
	urMsg   = "The Unraid API is version 4.37.2. Cha Dash needs 4.37.4 or later, which fixes an authorization bug: on older versions any API key, even a read-only one, can change settings. Update Unraid, or install the latest Unraid Connect plugin, to get API 4.37.4 or later; Cha Dash reconnects on its own."
)

func (unraidAPI) lead() time.Duration { return 0 }

// horizon is the first retry after the fix.
func (unraidAPI) horizon(in *instance) time.Time {
	if in.fix.IsZero() {
		return time.Time{}
	}
	return in.open.Add((in.fix.Sub(in.open)/urRetry + 1) * urRetry)
}

func (unraidAPI) horizonOffset(open, fix time.Duration) time.Duration {
	if fix == 0 {
		return -1
	}
	return fix + urRetry
}

func (u unraidAPI) changePoints(in *instance) []time.Time {
	return []time.Time{in.open, in.fix, u.horizon(in)}
}

func (u unraidAPI) apply(s *Sim, in *instance, w *world) {
	r := w.res[urInt]
	if r == nil || w.t.Before(in.open) {
		return
	}
	if h := u.horizon(in); !h.IsZero() && !w.t.Before(h) {
		setHealth(r, HealthOK, "", h)
		return
	}
	r.State = StateError
	setHealth(r, HealthRepair, urMsg, in.open)
}

func (u unraidAPI) incident(s *Sim, in *instance, t time.Time) *Incident {
	if t.Before(in.open) {
		return nil
	}
	inc := s.newIncident(in, ClassRepair, SeverityAttention, urInt)
	inc.Scenario, inc.Phase = "api-changed", "open" // the hub's repair for an unsupported_version error
	inc.Title = "Unraid's API changed"
	inc.Summary = fmt.Sprintf("Since %s, Unraid has answered in a way this integration doesn't support. Its tiles show values as of then.", s.clock(in.open))
	inc.Facts = map[string]any{"code": "unsupported_version", "instance": "unraid-magpie"}
	inc.Causes = []Cause{{Text: urMsg, Resource: urInt, At: in.open}}
	// What goes stale: what the instance publishes, as the hub lists it.
	inc.BlastRadius = []Impact{}
	for _, id := range slices.Sorted(maps.Keys(s.top.res)) {
		if s.top.res[id].Source == urInt && len(inc.BlastRadius) < 20 {
			inc.BlastRadius = append(inc.BlastRadius, Impact{Resource: id, Via: RelMonitors, Depth: 1})
		}
	}
	inc.Suggested = Action{ID: "integration.settings", Label: "Open the integration", Tier: T0, Target: urInt}
	inc.ResolvesWhen = "Resolves when Cha Dash can read Unraid again."
	if h := u.horizon(in); !h.IsZero() && !t.Before(h) {
		resolve(inc, h, "auto", fmt.Sprintf("Cha Dash could read Unraid again at %s.", s.clock(h)))
	}
	return inc
}

func (unraidAPI) touches(string, string) bool { return false }

func (unraidAPI) adjust(_ *Sim, _ *instance, _ *metricSpec, _ time.Time, v float64) float64 {
	return v
}

// ── hints ────────────────────────────────────────────────────────────────

// hint is a quiet inbox item: a security problem (category security)
// declared at severity info, about how the home is set up rather than
// something that broke. The hub's security posture may publish it at
// another severity (internal/demo). Hints stand from when the
// instance first read the source (the default script opens them before
// the day starts) until the setup changes; a fix is that change.
type hint struct {
	target, scenario     string
	title, summary       string
	facts                map[string]any
	suggested            Action
	resolves, resolution string
}

// settings is the framework's action for an incident that names none.
func settings(instance string) Action {
	return Action{ID: "integration.settings", Label: "Open the integration's settings", Tier: T0, Target: instance}
}

var (
	hintTraefik = hint{target: "app:traefik", scenario: "credential-hygiene",
		title:     "Traefik's dashboard has no auth",
		summary:   "Traefik's dashboard is routed without an auth middleware on traefik.home.example. Its API asked for credentials, so only the dashboard's page is open today, but one router change could expose the API, and with it basic-auth password hashes.",
		facts:     map[string]any{"url": "http://traefik.home.example", "hosts": []string{"traefik.home.example"}},
		suggested: settings("integration:traefik"),
		resolves:  "Resolves when every dashboard router has an auth middleware.", resolution: "No longer applies."}
	hintDocker = hint{target: "docker:magpie", scenario: "credential-hygiene",
		title:     "magpie's socket proxy is too broad",
		summary:   "The socket proxy at " + magpieProxy + " lets every GET under /containers/ through, so anyone who can reach it can read every container's environment, logs and files. Restrict who can connect (a firewall, or wollomatic's -allowfrom), or switch to a path-anchored allowlist like Cha Dash's compose preset.",
		facts:     map[string]any{"endpoint": magpieProxy, "access": "broad-proxy"},
		suggested: settings("integration:docker-magpie"),
		resolves:  "Resolves when the proxy refuses container logs.", resolution: "No longer applies."}
	hintFrigate = hint{target: "app:frigate", scenario: "frigate-unauthenticated-admin",
		title:   "Frigate grants admin without a sign-in",
		summary: "Cha Dash reads Frigate through a Frigate port (55000), which answers without a sign-in and makes anyone who can reach it an admin: they can change the config, restart Frigate and delete recordings.",
		facts:   map[string]any{"port": "55000", "username": "anonymous", "role": "admin"},
		suggested: Action{ID: "integration.settings", Label: "Use the authenticated port", Tier: T0, Target: "integration:frigate",
			Detail: "Create a Frigate user with the viewer role, then point this instance at Frigate's authenticated port (8971) with that user."},
		resolves: "Resolves when Cha Dash signs in to Frigate.", resolution: "Frigate no longer grants admin without a sign-in."}
	// The AdGuard Home instance reads the admin API at its LAN address over
	// http://, so the basic-auth password crosses the network in clear on
	// every sync. The words are the runtime's plain_http warning and
	// Traefik's plain-http hint.
	hintAdGuard = hint{target: "app:adguard", scenario: "credential-hygiene",
		title:     "AdGuard Home sends its credentials in clear text",
		summary:   "AdGuard Home sends its credentials over plain http:// to " + ipAdGuard + ". Anyone on the network path can read the admin password, and every AdGuard Home user is an administrator. Use https:// if it supports it.",
		facts:     map[string]any{"url": "http://" + ipAdGuard, "transport": "http"},
		suggested: settings("integration:adguard"),
		resolves:  "Resolves when the instance's URL uses https://.", resolution: "No longer applies."}
)

func (hint) lead() time.Duration { return 0 }

func (hint) horizon(in *instance) time.Time { return in.fix }

func (hint) horizonOffset(open, fix time.Duration) time.Duration {
	if fix == 0 {
		return -1
	}
	return fix
}

func (hint) changePoints(in *instance) []time.Time { return []time.Time{in.open, in.fix} }

func (hint) apply(*Sim, *instance, *world) {}

func (h hint) incident(s *Sim, in *instance, t time.Time) *Incident {
	if t.Before(in.open) {
		return nil
	}
	inc := s.newIncident(in, ClassProblem, SeverityInfo, h.target)
	inc.Category = CategorySecurity
	inc.Scenario, inc.Phase = Scenario(h.scenario), "open"
	inc.Title, inc.Summary = h.title, h.summary
	inc.Facts = maps.Clone(h.facts)
	inc.Causes, inc.BlastRadius = []Cause{}, []Impact{}
	inc.Suggested = h.suggested
	inc.ResolvesWhen = h.resolves
	if in.fixed(t) {
		resolve(inc, in.fix, "auto", h.resolution)
	}
	return inc
}

func (hint) touches(string, string) bool { return false }

func (hint) adjust(_ *Sim, _ *instance, _ *metricSpec, _ time.Time, v float64) float64 { return v }

// ── across instances ─────────────────────────────────────────────────────

// pbsWhere is how the correlator names the datastore every job writes to.
const pbsWhere = "PBS datastore main on Proxmox Backup Server"

// correlate adds what the hub's correlator adds live (internal/correlate,
// "Incidents across instances"). One instance can't see another's
// incidents, so each open PVE backup failure gains, in front of its own
// causes, one pointing at the PBS datastore when PBS has a problem of its
// own there (its open incident, else the datastore's health), then one per
// other PVE instance whose backups to the same PBS failed too (up to
// three). Resolved incidents keep only their own causes.
func (s *Sim) correlate(w *world) {
	var pbs *Incident
	var fails []*Incident
	for _, in := range w.incidents {
		if in.Status != StatusOpen || in.Class != ClassProblem {
			continue
		}
		switch in.Scenario {
		case ScenarioPBSFilling:
			pbs = in
		case ScenarioBackupFailed, "backup-target-unreachable", "backup-passthrough-conflict":
			fails = append(fails, in)
		}
	}
	slices.SortFunc(fails, func(a, b *Incident) int { return strings.Compare(a.ID, b.ID) })
	where := func(in *Incident) string { return jobOf(in.Facts["job"].(string)).where }
	ds := w.res[pbsTarget]
	for _, f := range fails {
		var add []Cause
		switch {
		case pbs != nil:
			add = append(add, Cause{Text: fmt.Sprintf("It writes to %s, which has a problem of its own: %s.", pbsWhere, strings.TrimSuffix(pbs.Title, ".")),
				Resource: pbsTarget, At: pbs.OpenedAt})
		case ds != nil && ds.Health.Status != HealthOK && ds.Health.Reason != "":
			add = append(add, Cause{Text: fmt.Sprintf("It writes to %s, which is %s.", pbsWhere, ds.Health.Reason), Resource: pbsTarget, At: ds.Health.Since})
		}
		peers := 0
		for _, p := range fails {
			if peers == 3 {
				break
			}
			if where(p) == where(f) {
				continue
			}
			peers++
			add = append(add, Cause{Text: fmt.Sprintf("Backups on %s to the same PBS failed too: %s.", where(p), strings.TrimSuffix(p.Title, ".")),
				Resource: p.Target, At: p.OpenedAt})
		}
		if len(add) > 0 {
			f.Causes = append(add, f.Causes...)
		}
	}
}
