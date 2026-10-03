package sim

import (
	"fmt"
	"math"
	"time"
)

// Copy follows 02-experience → Voice and copy: symptom first, plain words,
// specific times, calm, and every problem ends in something you can do.

func clock(t time.Time) string { return t.UTC().Format("15:04") }

// when formats t relative to ref: "15:04", "yesterday 15:04" or "Mon 2 Jan 15:04".
func when(t, ref time.Time) string {
	d := dayStart(ref).Sub(dayStart(t))
	switch {
	case d == 0:
		return clock(t)
	case d == 24*time.Hour:
		return "yesterday " + clock(t)
	default:
		return t.UTC().Format("Mon 2 Jan 15:04")
	}
}

func minTime(a, b time.Time) time.Time {
	if a.IsZero() {
		return b
	}
	if b.IsZero() || a.Before(b) {
		return a
	}
	return b
}

func setHealth(r *Resource, st HealthStatus, reason string, since time.Time) {
	if r != nil {
		r.Health = Health{Status: st, Reason: reason, Since: since}
	}
}

// newIncident fills the fields every scenario shares.
func (s *Sim) newIncident(in *instance, class Class, sev Severity, target string) *Incident {
	return &Incident{ID: in.id, Scenario: in.sc, Class: class, Severity: sev, Status: StatusOpen, Target: target,
		OpenedAt: in.open, UpdatedAt: in.open, BlastRadius: s.impact(target)}
}

// action builds an Action; T2 and up carry their own blast radius.
func (s *Sim) action(id, label string, tier Tier, target, detail string) Action {
	a := Action{ID: id, Label: label, Tier: tier, Target: target, Detail: detail}
	if tier >= T2 {
		a.Impact = s.impact(target)
	}
	return a
}

func resolve(inc *Incident, at time.Time, by, text string) {
	inc.Status = StatusResolved
	inc.ResolvedAt = at
	inc.UpdatedAt = at
	inc.ResolvedBy = by
	inc.Resolution = text
}

// ── backup-failed ────────────────────────────────────────────────────────

// homeassistant's nightly backup fails when the QEMU guest agent doesn't
// answer the filesystem freeze. A retry (the suggested action) or the next
// night's run resolves it.
type backupFailed struct{}

const (
	bfGuest = "pve:minig5/qemu/100"
	bfJob   = "pve:minig5/backup/daily"
	bfRetry = 4 * time.Minute
)

func (backupFailed) lead() time.Duration { return 0 }

// nextNightly is the first nightly run of the guest that finishes after open.
func (backupFailed) nextNightly(in *instance) time.Time {
	sl := slotOf(bfGuest)
	for d := dayStart(in.open); ; d = d.Add(24 * time.Hour) {
		if end := sl.end(d); end.After(in.open.Add(time.Hour)) {
			return end
		}
	}
}

func (b backupFailed) horizon(in *instance) time.Time {
	r := b.nextNightly(in)
	if !in.fix.IsZero() {
		r = minTime(r, in.fix.Add(bfRetry))
	}
	return r
}

func (backupFailed) horizonOffset(open, fix time.Duration) time.Duration {
	h := open + 26*time.Hour
	if fix > 0 {
		h = min(h, fix+bfRetry)
	}
	return h
}

func (b backupFailed) changePoints(in *instance) []time.Time {
	out := []time.Time{in.open, b.horizon(in)}
	if !in.fix.IsZero() {
		out = append(out, in.fix)
	}
	return out
}

func (b backupFailed) apply(s *Sim, in *instance, w *world) {
	if within(w.t, in.open, b.horizon(in)) {
		setHealth(w.res[bfJob], HealthAttention, fmt.Sprintf("homeassistant's backup failed at %s", clock(in.open)), in.open)
	}
}

func (b backupFailed) incident(s *Sim, in *instance, t time.Time) *Incident {
	if t.Before(in.open) {
		return nil
	}
	inc := s.newIncident(in, ClassProblem, SeverityAttention, bfGuest)
	lastOK := s.lastBackupOK(bfGuest, in.open.Add(-time.Second))
	nightly := b.nextNightly(in)
	inc.Phase = "failed"
	inc.Title = "Backup of homeassistant failed"
	inc.Summary = fmt.Sprintf("The nightly backup of homeassistant (VM 100 on minig5) failed at %s. The last good backup is from %s.", clock(in.open), when(lastOK, in.open))
	inc.Facts = map[string]any{"task": "vzdump", "error": "qmp command 'guest-fsfreeze-freeze' failed - got timeout", "last_ok": lastOK, "job": bfJob}
	inc.Causes = []Cause{
		{Text: "The QEMU guest agent in homeassistant didn't answer the filesystem freeze (timeout).", Resource: bfGuest, At: in.open},
		{Text: fmt.Sprintf("The other %d guests backed up normally, so PBS and its datastore are reachable.", len(backupSlots)-1), Resource: "pbs:main"},
	}
	inc.Suggested = s.action("pve.guest.backup", "Back up homeassistant now", T1, bfGuest, "Runs a one-off backup to PBS. It takes about 4 minutes and the VM keeps running.")
	inc.Alternatives = []Action{s.action("pbs.task.log", "View the failed task's log", T0, "app:pbs", "")}
	inc.ResolvesWhen = fmt.Sprintf("Resolves when a backup of homeassistant succeeds: a retry, or the next nightly run (done around %s).", clock(nightly))
	if in.fixed(t) && in.fix.Before(b.horizon(in)) {
		inc.Phase = "retrying"
		inc.UpdatedAt = in.fix
		inc.Summary += fmt.Sprintf(" A retry started at %s.", clock(in.fix))
	}
	if h := b.horizon(in); !t.Before(h) {
		if !in.fix.IsZero() && h.Equal(in.fix.Add(bfRetry)) {
			resolve(inc, h, "action", fmt.Sprintf("The retry at %s succeeded.", clock(in.fix)))
		} else {
			resolve(inc, h, "auto", fmt.Sprintf("The nightly backup at %s succeeded.", clock(h)))
		}
	}
	return inc
}

func (backupFailed) touches(string, string) bool { return false }

func (backupFailed) adjust(_ *Sim, _ *instance, _ *metricSpec, _ time.Time, v float64) float64 {
	return v
}

// ── pbs-datastore-filling ────────────────────────────────────────────────

// win-gaming's nightly backup writes 108 GB of new chunks and pushes the PBS
// datastore over 90%. The prune job has been disabled for weeks, so nothing
// makes room. Pruning per policy (the suggested action) brings it back to 86%.
type pbsFilling struct{}

const (
	pbsTarget       = "pbs:main"
	pbsRampBefore   = 14 * time.Minute
	pbsRampAfter    = 8 * time.Minute
	pbsJump         = 5.4 // percentage points written by the big backup (108 GB of 2 TB)
	pbsTrendPerHour = 0.38 / 24
	pbsPruned       = 2 * time.Minute  // prune done
	pbsCollected    = 22 * time.Minute // garbage collection done
)

func (pbsFilling) lead() time.Duration { return pbsRampBefore }

func (pbsFilling) horizon(in *instance) time.Time {
	if in.fix.IsZero() {
		return time.Time{}
	}
	return in.fix.Add(pbsCollected)
}

func (pbsFilling) horizonOffset(open, fix time.Duration) time.Duration {
	if fix == 0 {
		return -1
	}
	return fix + pbsCollected
}

func (p pbsFilling) changePoints(in *instance) []time.Time {
	out := []time.Time{in.open, in.open.Add(pbsRampAfter)}
	if !in.fix.IsZero() {
		out = append(out, in.fix, in.fix.Add(pbsPruned), in.fix.Add(pbsCollected))
	}
	return out
}

// overlay is the datastore usage added by the incident, in percentage points.
func (pbsFilling) overlay(in *instance, t time.Time) float64 {
	pre := func(t time.Time) float64 {
		v := pbsJump * lin(t, in.open.Add(-pbsRampBefore), in.open.Add(pbsRampAfter))
		if d := t.Sub(in.open.Add(pbsRampAfter)); d > 0 {
			v += pbsTrendPerHour * hours(d)
		}
		return v
	}
	if in.fixed(t) {
		return pre(in.fix) * (1 - lin(t, in.fix.Add(pbsPruned), in.fix.Add(pbsCollected)))
	}
	return pre(t)
}

func (p pbsFilling) apply(s *Sim, in *instance, w *world) {
	t := w.t
	ds := w.res[pbsTarget]
	if ds == nil || t.Before(in.open) {
		return
	}
	switch h := p.horizon(in); {
	case !h.IsZero() && !t.Before(h):
		setHealth(ds, HealthOK, "", h)
	case in.fixed(t) && t.Before(in.fix.Add(pbsPruned)):
		setHealth(ds, HealthAttention, "pruning 38 snapshots", in.open)
	case in.fixed(t):
		setHealth(ds, HealthAttention, "garbage collection running", in.open)
	case t.Before(in.open.Add(pbsRampAfter)):
		setHealth(ds, HealthAttention, "90% used, rising during the nightly backups", in.open)
	default:
		setHealth(ds, HealthAttention, "92% used · full in about 3 weeks", in.open)
	}
	if in.fixed(t) && !t.Before(in.fix.Add(pbsPruned)) {
		ds.Attrs["snapshots_beyond_policy"] = 0
		ds.Attrs["prune_last"] = in.fix.Add(pbsPruned)
	}
	if in.fixed(t) && !t.Before(in.fix.Add(pbsCollected)) {
		ds.Attrs["gc_last"] = in.fix.Add(pbsCollected)
	}
}

func (p pbsFilling) incident(s *Sim, in *instance, t time.Time) *Incident {
	if t.Before(in.open) {
		return nil
	}
	inc := s.newIncident(in, ClassProblem, SeverityAttention, pbsTarget)
	inc.Title = "PBS datastore is filling up"
	inc.Phase = "rising"
	inc.Summary = fmt.Sprintf("Datastore main on jupiter crossed 90%% at %s while win-gaming's backup was writing.", clock(in.open))
	inc.Facts = map[string]any{"used_pct": 90, "capacity_tb": 2}
	if !t.Before(in.open.Add(pbsRampAfter)) {
		inc.Phase = "high"
		inc.UpdatedAt = in.open.Add(pbsRampAfter)
		full := int(math.Round((100 - 92) / (pbsTrendPerHour * 24)))
		inc.Summary = fmt.Sprintf("Datastore main is 92%% full (1.84 of 2 TB). At this rate it fills up in about %d weeks.", (full+3)/7)
		inc.Facts = map[string]any{"used_pct": 92, "capacity_tb": 2, "full_in_days": full}
	}
	inc.Causes = []Cause{
		{Text: fmt.Sprintf("The daily prune job has been disabled since %s, so 38 snapshots are outside the retention policy.", pruneDisabledAt.Format("2 Jan")), Resource: pbsTarget, At: pruneDisabledAt},
		{Text: fmt.Sprintf("win-gaming's backup at %s wrote 108 GB, about 12 times a usual night. It's stopped, so each backup reads its whole disk; the new data looks like a game install.", clock(in.open.Add(-pbsRampBefore))), Resource: "pve:io/qemu/201", At: in.open.Add(-pbsRampBefore)},
	}
	inc.Suggested = s.action("pbs.datastore.prune", "Prune per policy", T2, pbsTarget,
		"Removes the 38 snapshots outside keep-daily 7 · weekly 4 · monthly 6, then runs garbage collection (about 20 minutes). Frees about 116 GB (92% → 86%). Pruned snapshots can't be restored.")
	inc.Alternatives = []Action{
		s.action("pbs.prune-job.enable", "Re-enable the daily prune job", T1, pbsTarget, "Prunes every night at 03:30 from now on. Undo turns it off again."),
		s.action("link.open", "Open PBS", T0, "app:pbs", ""),
	}
	inc.ResolvesWhen = "Resolves when usage drops below 88%."
	if in.fixed(t) {
		inc.Phase = "pruning"
		inc.UpdatedAt = in.fix
		inc.Summary = fmt.Sprintf("Pruning 38 snapshots (started %s).", clock(in.fix))
		if !t.Before(in.fix.Add(pbsPruned)) {
			inc.Phase = "collecting"
			inc.UpdatedAt = in.fix.Add(pbsPruned)
			inc.Summary = "Pruned 38 snapshots. Garbage collection is freeing the space, about 20 minutes."
		}
	}
	if h := p.horizon(in); !h.IsZero() && !t.Before(h) {
		resolve(inc, h, "action", "Pruned 38 snapshots and freed about 116 GB. Usage is back to 86%. The daily prune job is still disabled.")
	}
	return inc
}

func (pbsFilling) touches(res, key string) bool {
	return (res == pbsTarget && key == "used.pct") || (res == "docker:jupiter/pbs" && key == "cpu.pct")
}

func (p pbsFilling) adjust(_ *Sim, in *instance, m *metricSpec, t time.Time, v float64) float64 {
	if m.res == "docker:jupiter/pbs" {
		if in.fixed(t) && within(t, in.fix, in.fix.Add(pbsCollected)) {
			v += 22 // prune and garbage collection
		}
		return v
	}
	v += p.overlay(in, t)
	switch {
	case within(t, in.open.Add(-pbsRampBefore), in.open):
		v = math.Min(v, 89.9)
	case !t.Before(in.open) && !in.fixed(t):
		v = math.Max(v, 90.0)
	}
	return v
}

// ── host-memory-pressure ─────────────────────────────────────────────────

// debian-nas (LXC 100 on jupiter-nas) grows from 0.5 to 3.1 GB over two
// hours. jupiter-nas has 8 GB and no swap, so host memory climbs to 95%.
// Restarting the LXC (the suggested action) fixes it.
type hostMemory struct{}

const (
	hmHost    = "pve:jupiter-nas"
	hmGuest   = "pve:jupiter-nas/lxc/100"
	hmLead    = 110 * time.Minute
	hmClimb   = 30 * time.Minute
	hmRestart = 20 * time.Second
	hmSettle  = 10 * time.Minute
	hmAtOpen  = 28.3 // host memory points added by the time it crosses 90%
	hmPeak    = 33.0 // ... and at the plateau (95%)
)

func (hostMemory) lead() time.Duration { return hmLead }

func (hostMemory) horizon(in *instance) time.Time {
	if in.fix.IsZero() {
		return time.Time{}
	}
	return in.fix.Add(hmSettle)
}

func (hostMemory) horizonOffset(open, fix time.Duration) time.Duration {
	if fix == 0 {
		return -1
	}
	return fix + hmSettle
}

func (hostMemory) changePoints(in *instance) []time.Time {
	out := []time.Time{in.open, in.open.Add(hmClimb)}
	if !in.fix.IsZero() {
		out = append(out, in.fix, in.fix.Add(hmRestart), in.fix.Add(hmSettle))
	}
	return out
}

// overlay is the host memory added by the leak, in percentage points.
func (hostMemory) overlay(in *instance, t time.Time) float64 {
	pre := func(t time.Time) float64 {
		switch {
		case t.Before(in.open.Add(-hmLead)):
			return 0
		case t.Before(in.open):
			return hmAtOpen * math.Pow(lin(t, in.open.Add(-hmLead), in.open), 1.3)
		default:
			return hmAtOpen + (hmPeak-hmAtOpen)*lin(t, in.open, in.open.Add(hmClimb))
		}
	}
	if in.fixed(t) {
		return pre(in.fix) * (1 - ease(t, in.fix, in.fix.Add(2*time.Minute)))
	}
	return pre(t)
}

func (h hostMemory) apply(s *Sim, in *instance, w *world) {
	t := w.t
	if t.Before(in.open) {
		return
	}
	host, guest, cockpit := w.res[hmHost], w.res[hmGuest], w.res["app:cockpit"]
	hz := h.horizon(in)
	switch {
	case !hz.IsZero() && !t.Before(hz):
		setHealth(host, HealthOK, "", hz)
	case in.fixed(t):
		setHealth(host, HealthAttention, "memory falling after a restart of debian-nas", in.open)
	case t.Before(in.open.Add(hmClimb)):
		setHealth(host, HealthAttention, "memory above 90% and climbing", in.open)
	default:
		setHealth(host, HealthAttention, "memory 95% · no swap", in.open)
	}
	switch {
	case in.fixed(t) && t.Before(in.fix.Add(hmRestart)):
		guest.State = StateStopped
		setHealth(guest, HealthMaintenance, "restarting", in.fix)
		cockpit.State = StateStopped
		setHealth(cockpit, HealthMaintenance, "debian-nas is restarting", in.fix)
	case in.fixed(t):
		up := in.fix.Add(hmRestart)
		guest.Attrs["started_at"] = up
		setHealth(guest, HealthOK, "", up)
		setHealth(cockpit, HealthOK, "", up)
	default:
		setHealth(guest, HealthAttention, fmt.Sprintf("memory grew from 0.5 GB since %s", clock(in.open.Add(-hmLead))), in.open)
	}
}

func (h hostMemory) incident(s *Sim, in *instance, t time.Time) *Incident {
	if t.Before(in.open) {
		return nil
	}
	inc := s.newIncident(in, ClassProblem, SeverityAttention, hmHost)
	start := in.open.Add(-hmLead)
	inc.Title = "jupiter-nas is running out of memory"
	inc.Phase = "climbing"
	inc.Summary = fmt.Sprintf("Memory crossed 90%% at %s and has been climbing since %s.", clock(in.open), clock(start))
	inc.Facts = map[string]any{"used_pct": 90, "total_gb": 8, "swap_gb": 0}
	if !t.Before(in.open.Add(hmClimb)) {
		inc.Phase = "high"
		inc.UpdatedAt = in.open.Add(hmClimb)
		inc.Summary = "Memory is at 95% of 8 GB and there is no swap. If it fills up, the kernel will stop a process to free memory."
		inc.Facts = map[string]any{"used_pct": 95, "total_gb": 8, "swap_gb": 0}
	}
	inc.Causes = []Cause{
		{Text: fmt.Sprintf("debian-nas (LXC 100) grew from 0.5 GB to 3.1 GB since %s.", clock(start)), Resource: hmGuest, At: start},
		{Text: "No swap is configured on jupiter-nas.", Resource: hmHost},
	}
	inc.Suggested = s.action("pve.guest.restart", "Restart debian-nas", T2, hmGuest,
		"Restarts the LXC, which takes about 20 seconds. Cockpit and the shares it serves are unavailable meanwhile.")
	inc.Alternatives = []Action{
		s.action("link.open", "Open Cockpit", T0, "app:cockpit", ""),
		s.action("pve.host.memory", "See memory by guest", T0, hmHost, ""),
	}
	inc.ResolvesWhen = "Resolves when memory stays below 85% for 10 minutes."
	if in.fixed(t) {
		inc.Phase = "restarting"
		inc.UpdatedAt = in.fix
		inc.Summary = fmt.Sprintf("debian-nas was restarted at %s. Waiting for memory to settle.", clock(in.fix))
	}
	if hz := h.horizon(in); !hz.IsZero() && !t.Before(hz) {
		resolve(inc, hz, "action", fmt.Sprintf("debian-nas restarted at %s. Memory is back to normal, about 62%%.", clock(in.fix)))
	}
	return inc
}

func (hostMemory) touches(res, key string) bool {
	return key == "mem.pct" && (res == hmHost || res == hmGuest)
}

func (h hostMemory) adjust(_ *Sim, in *instance, m *metricSpec, t time.Time, v float64) float64 {
	if m.res == hmGuest {
		if in.fixed(t) {
			return v // a restarted LXC starts with fresh memory
		}
		return v + 2*h.overlay(in, t) // 8 GB host, 4 GB LXC
	}
	v += h.overlay(in, t)
	switch {
	case within(t, in.open.Add(-hmLead), in.open):
		v = math.Min(v, 89.8)
	case !t.Before(in.open) && !in.fixed(t):
		v = math.Max(v, 90.1)
	}
	return v
}

// ── plex-token-revoked ───────────────────────────────────────────────────

// Plex starts answering 401. Plex itself is fine; this is a repair, not an
// outage. Reconnecting with a Plex PIN (the suggested action) fixes it.
type plexToken struct{}

const (
	plexInt       = "integration:plex"
	plexReconnect = 10 * time.Second
)

func (plexToken) lead() time.Duration { return 0 }

func (plexToken) horizon(in *instance) time.Time {
	if in.fix.IsZero() {
		return time.Time{}
	}
	return in.fix.Add(plexReconnect)
}

func (plexToken) horizonOffset(open, fix time.Duration) time.Duration {
	if fix == 0 {
		return -1
	}
	return fix + plexReconnect
}

func (p plexToken) changePoints(in *instance) []time.Time {
	return []time.Time{in.open, p.horizon(in)}
}

func (p plexToken) apply(s *Sim, in *instance, w *world) {
	t := w.t
	r := w.res[plexInt]
	if r == nil || t.Before(in.open) {
		return
	}
	if h := p.horizon(in); !h.IsZero() && !t.Before(h) {
		setHealth(r, HealthOK, "", h)
		return
	}
	r.State = StateError
	setHealth(r, HealthRepair, "token rejected (401)", in.open)
}

func (p plexToken) incident(s *Sim, in *instance, t time.Time) *Incident {
	if t.Before(in.open) {
		return nil
	}
	inc := s.newIncident(in, ClassRepair, SeverityAttention, plexInt)
	inc.Phase = "rejected"
	inc.Title = "Plex rejected its token"
	inc.Summary = fmt.Sprintf("Plex has answered 401 Unauthorized since %s. Plex itself is up; its tiles show values as of %s.", clock(in.open), clock(in.open))
	inc.Facts = map[string]any{"status": 401, "endpoint": "/status/sessions"}
	inc.Causes = []Cause{
		{Text: fmt.Sprintf("The stored token stopped working at %s. It was probably revoked, by signing out all devices or changing the Plex password.", clock(in.open)), Resource: plexInt, At: in.open},
		{Text: "Plex's own HTTP check still passes, so the problem is the connection, not Plex.", Resource: "app:plex"},
	}
	inc.Suggested = s.action("integration.reconnect", "Reconnect Plex", T0, plexInt, "Sign in with a Plex PIN, about 10 seconds. The new token goes into the vault.")
	inc.Alternatives = []Action{s.action("link.open", "Open Plex", T0, "app:plex", "")}
	inc.ResolvesWhen = "Resolves when you reconnect Plex."
	if h := p.horizon(in); !h.IsZero() && !t.Before(h) {
		resolve(inc, h, "action", fmt.Sprintf("Reconnected at %s. Plex's tiles are live again.", clock(in.fix)))
	}
	return inc
}

func (plexToken) touches(string, string) bool { return false }

func (plexToken) adjust(_ *Sim, _ *instance, _ *metricSpec, _ time.Time, v float64) float64 {
	return v
}

// ── karakeep-api-changed ─────────────────────────────────────────────────

// Karakeep is updated to 0.31.0 and its stats endpoint returns 404. Karakeep
// itself stays up; the integration needs its new adapter (the suggested
// action).
type karakeepAPI struct{}

const (
	kkCt       = "docker:pvedocker/karakeep-web-1"
	kkApp      = "app:karakeep"
	kkInt      = "integration:karakeep"
	kkLead     = 3 * time.Minute
	kkRecreate = 20 * time.Second
	kkSettle   = 30 * time.Second
	kkVersion  = "0.31.0"
)

func (karakeepAPI) lead() time.Duration { return kkLead }

func (karakeepAPI) horizon(in *instance) time.Time {
	if in.fix.IsZero() {
		return time.Time{}
	}
	return in.fix.Add(kkSettle)
}

func (karakeepAPI) horizonOffset(open, fix time.Duration) time.Duration {
	if fix == 0 {
		return -1
	}
	return fix + kkSettle
}

func (k karakeepAPI) changePoints(in *instance) []time.Time {
	up := in.open.Add(-kkLead)
	return []time.Time{up, up.Add(kkRecreate), in.open, k.horizon(in)}
}

func (k karakeepAPI) apply(s *Sim, in *instance, w *world) {
	t := w.t
	update := in.open.Add(-kkLead)
	if t.Before(update) {
		return
	}
	ct, app, integ := w.res[kkCt], w.res[kkApp], w.res[kkInt]
	up := update.Add(kkRecreate)
	ct.Attrs["image"] = "ghcr.io/karakeep-app/karakeep:" + kkVersion
	ct.Attrs["container_id"] = containerID(s.rng, kkCt, 1)
	ct.Attrs["started_at"] = up
	app.Attrs["version"] = kkVersion
	integ.Attrs["upstream_version"] = kkVersion
	if t.Before(up) {
		ct.State = StateRestarting
		setHealth(ct, HealthMaintenance, "recreating with image "+kkVersion, update)
		app.State = StateStarting
		setHealth(app, HealthMaintenance, "updating to "+kkVersion, update)
	} else {
		setHealth(ct, HealthOK, "", up)
		setHealth(app, HealthOK, "", up)
	}
	if t.Before(in.open) {
		return
	}
	if h := k.horizon(in); !h.IsZero() && !t.Before(h) {
		integ.Attrs["adapter"] = "≤ 0.31"
		setHealth(integ, HealthOK, "", h)
		return
	}
	integ.State = StateError
	setHealth(integ, HealthRepair, "stats endpoint returned 404 (API changed in "+kkVersion+")", in.open)
}

func (k karakeepAPI) incident(s *Sim, in *instance, t time.Time) *Incident {
	if t.Before(in.open) {
		return nil
	}
	update := in.open.Add(-kkLead)
	inc := s.newIncident(in, ClassRepair, SeverityAttention, kkInt)
	inc.Phase = "api-changed"
	inc.Title = "Karakeep's API changed"
	inc.Summary = fmt.Sprintf("Karakeep was updated to %s at %s, and its stats endpoint now returns 404. Karakeep itself is up; bookmark counts show values as of %s.", kkVersion, clock(update), clock(in.open))
	inc.Facts = map[string]any{"detected_version": kkVersion, "supported": "≤ 0.30", "endpoint": "/api/v1/users/me/stats", "status": 404}
	inc.Causes = []Cause{
		{Text: fmt.Sprintf("karakeep-web-1 was recreated with image %s at %s.", kkVersion, clock(update)), Resource: kkCt, At: update},
		{Text: "This integration supports Karakeep up to 0.30.", Resource: kkInt},
	}
	inc.Suggested = s.action("integration.update", "Update the Karakeep integration", T1, kkInt, "Switches to the adapter for 0.31. Undo puts the previous one back.")
	inc.Alternatives = []Action{
		s.action("stack.rollback", "Roll Karakeep back to 0.30.2", T2, kkApp, "Redeploys the karakeep stack on pvedocker with the previous image."),
		s.action("link.open", "Open Karakeep", T0, kkApp, ""),
	}
	inc.ResolvesWhen = "Resolves when the integration can read Karakeep again."
	if h := k.horizon(in); !h.IsZero() && !t.Before(h) {
		resolve(inc, h, "action", fmt.Sprintf("Integration updated at %s; stats are live again.", clock(in.fix)))
	}
	return inc
}

func (karakeepAPI) touches(string, string) bool { return false }

func (karakeepAPI) adjust(_ *Sim, _ *instance, _ *metricSpec, _ time.Time, v float64) float64 {
	return v
}

// ── qbittorrent-oom ──────────────────────────────────────────────────────

// qBittorrent's memory climbs to its 4 GiB limit; the kernel kills it and
// Docker restarts it. It recovers on its own, but the incident stays open as
// a suggestion until the limit is raised (the suggested action) or 24 hours
// pass without another kill.
type qbitOOM struct{}

const (
	qbCt       = "docker:jupiter/binhex-qbittorrentvpn"
	qbApp      = "app:qbittorrent"
	qbLead     = 25 * time.Minute
	qbRestart  = 20 * time.Second // Docker restarts it
	qbHealthy  = 4 * time.Minute  // VPN up, web UI answering
	qbRecreate = 15 * time.Second // Unraid removes and recreates it
	qbApplied  = 2 * time.Minute
	qbAuto     = 24 * time.Hour
)

func (qbitOOM) lead() time.Duration { return qbLead }

// fixedAt reports whether the fix was applied by t, while the incident was
// still open (a fix scripted after the 24-hour auto-resolve does nothing).
func (qbitOOM) fixedAt(in *instance, t time.Time) bool {
	return in.fixed(t) && in.fix.Before(in.open.Add(qbAuto))
}

func (q qbitOOM) horizon(in *instance) time.Time {
	if q.fixedAt(in, in.fix) {
		return in.fix.Add(qbApplied)
	}
	return in.open.Add(qbAuto)
}

func (qbitOOM) horizonOffset(open, fix time.Duration) time.Duration {
	h := open + qbAuto
	if fix > 0 {
		h = min(h, fix+qbApplied)
	}
	return h
}

func (q qbitOOM) changePoints(in *instance) []time.Time {
	out := []time.Time{in.open, in.open.Add(qbRestart), in.open.Add(qbHealthy), q.horizon(in)}
	if q.fixedAt(in, in.fix) {
		out = append(out, in.fix, in.fix.Add(qbRecreate), in.fix.Add(qbApplied))
	}
	return out
}

func (q qbitOOM) apply(s *Sim, in *instance, w *world) {
	t := w.t
	if t.Before(in.open) {
		return
	}
	app := w.res[qbApp]
	if q.fixedAt(in, t) {
		q.applyFix(s, in, w)
		return
	}
	ct := w.res[qbCt]
	killed, restarted, healthy := in.open, in.open.Add(qbRestart), in.open.Add(qbHealthy)
	deps := []*Resource{w.res["app:sonarr"], w.res["app:radarr"]}
	switch {
	case t.Before(restarted):
		ct.State = StateExited
		ct.Attrs["exit_code"] = 137
		ct.Attrs["oom_killed"] = true
		setHealth(ct, HealthDown, fmt.Sprintf("OOM-killed at %s (limit 4 GiB)", clock(killed)), killed)
		app.State = StateStopped
		setHealth(app, HealthDown, "container exited (out of memory)", killed)
	default:
		ct.Attrs["restart_count"] = 1
		ct.Attrs["started_at"] = restarted
		ct.Attrs["last_exit"] = fmt.Sprintf("137 (OOM-killed) at %s", clock(killed))
		if t.Before(healthy) {
			setHealth(ct, HealthAttention, "starting · VPN connecting", restarted)
			app.State = StateStarting
			setHealth(app, HealthAttention, "starting · VPN connecting", restarted)
		} else {
			setHealth(ct, HealthOK, "", healthy)
			setHealth(app, HealthOK, "", healthy)
		}
	}
	for _, d := range deps {
		if t.Before(healthy) {
			setHealth(d, HealthAttention, fmt.Sprintf("can't reach qBittorrent since %s", clock(killed)), killed)
		} else {
			setHealth(d, HealthOK, "", healthy)
		}
	}
}

// applyFix: Unraid applies the new template by removing the container and
// creating it again, so the resource disappears for a moment.
func (q qbitOOM) applyFix(s *Sim, in *instance, w *world) {
	t := w.t
	app := w.res[qbApp]
	created, done := in.fix.Add(qbRecreate), in.fix.Add(qbApplied)
	// Sonarr and Radarr are fine again since qBittorrent recovered, or since
	// the fix if it came first. The planned recreate doesn't flag them.
	since := minTime(in.open.Add(qbHealthy), in.fix)
	for _, d := range []*Resource{w.res["app:sonarr"], w.res["app:radarr"]} {
		setHealth(d, HealthOK, "", since)
	}
	if t.Before(created) {
		delete(w.res, qbCt)
		app.State = StateStopped
		setHealth(app, HealthMaintenance, "applying a new memory limit", in.fix)
		return
	}
	ct := w.res[qbCt]
	ct.Attrs["container_id"] = containerID(s.rng, qbCt, 1)
	ct.Attrs["mem_limit_mib"] = 6144
	ct.Attrs["started_at"] = created
	if t.Before(done) {
		setHealth(ct, HealthMaintenance, "starting · VPN connecting", created)
		app.State = StateStarting
		setHealth(app, HealthMaintenance, "starting · VPN connecting", in.fix)
	} else {
		setHealth(ct, HealthOK, "", done)
		setHealth(app, HealthOK, "", done)
	}
}

func (q qbitOOM) incident(s *Sim, in *instance, t time.Time) *Incident {
	if t.Before(in.open) {
		return nil
	}
	killed, restarted, healthy := in.open, in.open.Add(qbRestart), in.open.Add(qbHealthy)
	inc := s.newIncident(in, ClassProblem, SeverityDown, qbCt)
	inc.Title = "qBittorrent ran out of memory"
	inc.Phase = "oom-killed"
	inc.Summary = fmt.Sprintf("binhex-qbittorrentvpn hit its 4 GiB memory limit at %s and was killed. Docker restarts it automatically.", clock(killed))
	inc.Facts = map[string]any{"limit_mib": 4096, "exit_code": 137, "restarts": 1}
	switch {
	case q.fixedAt(in, t):
		inc.Severity = SeverityInfo
		inc.Phase = "applying"
		inc.UpdatedAt = in.fix
		inc.Summary = "Recreating binhex-qbittorrentvpn with a 6 GiB memory limit."
	case !t.Before(healthy):
		inc.Severity = SeverityInfo
		inc.Phase = "recovered"
		inc.UpdatedAt = healthy
		inc.Summary = fmt.Sprintf("It was OOM-killed at %s and has run normally since %s. Memory grew from 1.1 to 4 GiB in 25 minutes, so it may happen again.", clock(killed), clock(healthy))
	case !t.Before(restarted):
		inc.Severity = SeverityAttention
		inc.Phase = "restarting"
		inc.UpdatedAt = restarted
		inc.Summary = fmt.Sprintf("Restarted at %s, waiting for the VPN tunnel. Sonarr and Radarr can't download until it's back.", clock(restarted))
	}
	inc.Causes = []Cause{
		{Text: "Memory grew from 1.1 GiB to the 4 GiB limit in 25 minutes.", Resource: qbCt, At: killed.Add(-qbLead)},
		{Text: "About 20 torrents were downloading at the time.", Resource: qbApp, At: killed.Add(-qbLead)},
	}
	inc.Suggested = s.action("container.update", "Raise the memory limit to 6 GiB", T2, qbCt,
		"Unraid recreates binhex-qbittorrentvpn with the new limit, about 2 minutes. Downloads pause meanwhile.")
	inc.Alternatives = []Action{
		s.action("container.logs", "View logs", T0, qbCt, ""),
		s.action("downloads.pause", "Pause all torrents", T1, qbApp, "Undo resumes them."),
	}
	inc.ResolvesWhen = "Resolves when the memory limit is raised, or after 24 hours without another OOM kill."
	if h := q.horizon(in); !t.Before(h) {
		if q.fixedAt(in, in.fix) {
			resolve(inc, h, "action", fmt.Sprintf("Recreated at %s with a 6 GiB limit; running normally.", clock(in.fix)))
		} else {
			resolve(inc, h, "auto", "No further OOM kills in 24 hours.")
		}
	}
	return inc
}

func (qbitOOM) touches(res, key string) bool {
	return res == qbCt && (key == "mem.mib" || key == "cpu.pct")
}

func (q qbitOOM) adjust(_ *Sim, in *instance, m *metricSpec, t time.Time, v float64) float64 {
	ramp0 := in.open.Add(-qbLead)
	if m.key == "cpu.pct" {
		switch {
		case within(t, ramp0, in.open):
			v += 12 * lin(t, ramp0, in.open)
		case within(t, in.open.Add(qbRestart), in.open.Add(qbHealthy)) && !q.fixedAt(in, t):
			v += 15 // start-up
		}
		return v
	}
	// mem.mib
	restart := in.open.Add(qbRestart)
	if q.fixedAt(in, t) {
		restart = in.fix.Add(qbRecreate)
	}
	switch {
	case t.Before(ramp0):
		return v
	case t.Before(in.open):
		return math.Min(4096, v+(4090-v)*math.Pow(lin(t, ramp0, in.open), 1.8))
	default:
		return v * (0.75 + 0.25*ease(t, restart, restart.Add(45*time.Minute)))
	}
}
