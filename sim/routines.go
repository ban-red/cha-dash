package sim

import "time"

// Routines are the everyday rhythm of the home, independent of incidents:
// nightly backups, disks spinning down, camera detections and the weather.
// Like everything else they are pure functions of time.

// ── nightly backups ──────────────────────────────────────────────────────

const backupWindow = 2 * time.Hour // runs start at 02:00 UTC

func slotOf(guest string) backupSlot {
	for _, s := range backupSlots {
		if s.guest == guest {
			return s
		}
	}
	panic("sim: no backup slot for " + guest)
}

func (sl backupSlot) begin(day time.Time) time.Time { return day.Add(backupWindow + sl.start) }
func (sl backupSlot) end(day time.Time) time.Time   { return day.Add(backupWindow + sl.start + sl.dur) }

// guestBackup is one guest's backup within a run.
type guestBackup struct {
	guest      string
	begin, end time.Time // a failure ends when it happens
	ok         bool
	err        string // why it failed, as the task log says
}

// jobRun is one run of a backup job: the scheduled one, or one started by
// hand. A run that couldn't reach its target stops before any guest: every
// guest fails at its start, with the task's error.
type jobRun struct {
	job        string
	begin, end time.Time
	manual     bool // started by hand: the Proxmox integration doesn't count it as a run of the job
	whole      bool // a manual run of the whole job (not one guest's backup): the job shows as running
	err        string
	guests     []guestBackup
}

func (r jobRun) failed() int {
	n := 0
	for _, g := range r.guests {
		if !g.ok {
			n++
		}
	}
	return n
}

// firstErr is the run's error as the Proxmox integration reports it: the
// task's own, else the first failed guest's.
func (r jobRun) firstErr() string {
	if r.err != "" {
		return r.err
	}
	for _, g := range r.guests {
		if !g.ok {
			return g.err
		}
	}
	return ""
}

func (r *jobRun) span() {
	for i, g := range r.guests {
		if i == 0 || g.begin.Before(r.begin) {
			r.begin = g.begin
		}
		if g.end.After(r.end) {
			r.end = g.end
		}
	}
}

func (r *jobRun) guest(id string) *guestBackup {
	for i := range r.guests {
		if r.guests[i].guest == id {
			return &r.guests[i]
		}
	}
	return nil
}

// backupShaper is a scenario that changes backup runs: a guest that fails
// one night, a target that can't be reached, a retry started by hand.
type backupShaper interface {
	// shapeNight changes a job's scheduled run on day. r.begin and r.end are
	// set from the guests afterwards.
	shapeNight(s *Sim, in *instance, day time.Time, r *jobRun)
	// manualRuns are the runs the scenario starts by hand.
	manualRuns(s *Sim, in *instance) []jobRun
}

// manualRun lays out a run of the whole job started by hand at begin, its
// guests one after another as the slots go.
func manualRun(job string, begin time.Time) jobRun {
	r := jobRun{job: job, manual: true, whole: true}
	at := begin
	for _, sl := range backupSlots {
		if sl.job == job {
			r.guests = append(r.guests, guestBackup{guest: sl.guest, begin: at, end: at.Add(sl.dur), ok: true})
			at = at.Add(sl.dur)
		}
	}
	r.span()
	return r
}

// nightly is a job's scheduled run on day, as the script's scenarios shape it.
func (s *Sim) nightly(job string, day time.Time) jobRun {
	r := jobRun{job: job}
	for _, sl := range backupSlots {
		if sl.job == job {
			r.guests = append(r.guests, guestBackup{guest: sl.guest, begin: sl.begin(day), end: sl.end(day), ok: true})
		}
	}
	for _, in := range s.inst {
		if sh, ok := in.impl.(backupShaper); ok {
			sh.shapeNight(s, in, day, &r)
		}
	}
	r.span()
	return r
}

// jobBegin is when a job's scheduled run on day starts.
func jobBegin(job string, day time.Time) time.Time {
	return day.Add(backupWindow + jobOf(job).offset)
}

// nightOf is the day of the latest scheduled run of job that began at or
// before t.
func nightOf(job string, t time.Time) time.Time {
	day := dayStart(t)
	if jobBegin(job, day).After(t) {
		day = day.Add(-24 * time.Hour)
	}
	return day
}

// recentRuns are a job's scheduled runs of the four nights up to t's day,
// then the runs started by hand, as far as the task list goes.
func (s *Sim) recentRuns(job string, t time.Time) []jobRun {
	today := dayStart(t)
	out := make([]jobRun, 0, 5)
	for i := 3; i >= 0; i-- {
		out = append(out, s.nightly(job, today.Add(-time.Duration(i)*24*time.Hour)))
	}
	for _, in := range s.inst {
		if sh, ok := in.impl.(backupShaper); ok {
			for _, r := range sh.manualRuns(s, in) {
				if r.job == job {
					out = append(out, r)
				}
			}
		}
	}
	return out
}

// lastBackupOK is the guest's latest successful backup at or before t.
func (s *Sim) lastBackupOK(guest string, t time.Time) time.Time {
	var last time.Time
	for _, r := range s.recentRuns(slotOf(guest).job, t) {
		if g := r.guest(guest); g != nil && g.ok && !g.end.After(t) && g.end.After(last) {
			last = g.end
		}
	}
	return last
}

// jobHistory is what the Proxmox integration knows about a job's scheduled
// runs at t: how many finished, how many failed and when, and when the last
// good one ended.
type jobHistory struct {
	runs, failed int
	failedAt     []string
	lastOK       time.Time
}

func (s *Sim) jobHistory(job string, t time.Time) jobHistory {
	var h jobHistory
	for _, r := range s.recentRuns(job, t) {
		if r.manual || r.end.After(t) {
			continue
		}
		h.runs++
		if r.failed() > 0 {
			h.failed++
			h.failedAt = append(h.failedAt, r.begin.UTC().Format(time.RFC3339))
		} else if r.end.After(h.lastOK) {
			h.lastOK = r.end
		}
	}
	return h
}

// failedBackupsIn counts the backup tasks that failed on PBS in (from, to]:
// the guest-agent timeouts. A run that couldn't reach PBS, or a guest that
// couldn't start, never made a PBS task.
func (s *Sim) failedBackupsIn(from, to time.Time) int {
	n := 0
	for _, in := range s.inst {
		if in.sc == ScenarioBackupFailed && in.open.After(from) && !in.open.After(to) {
			n++
		}
	}
	return n
}

func (s *Sim) applyBackups(w *world) {
	t := w.t
	runs := make(map[string][]jobRun, len(backupJobs))
	for _, j := range backupJobs {
		runs[j.id] = s.recentRuns(j.id, t)
	}
	for _, sl := range backupSlots {
		g := w.res[sl.guest]
		if g == nil {
			continue
		}
		var last *guestBackup
		var lastOK time.Time
		for i := range runs[sl.job] {
			gb := runs[sl.job][i].guest(sl.guest)
			if gb == nil || gb.end.After(t) {
				continue
			}
			if last == nil || !gb.end.Before(last.end) {
				last = gb
			}
			if gb.ok && gb.end.After(lastOK) {
				lastOK = gb.end
			}
		}
		if last != nil {
			g.Attrs["backup.last_at"] = last.end
			g.Attrs["backup.last_status"] = map[bool]string{true: "ok", false: "failed"}[last.ok]
			if !last.ok {
				g.Attrs["backup.last_error"] = last.err
			}
		}
		if !lastOK.IsZero() {
			g.Attrs["backup.last_ok"] = lastOK
		}
	}
	for _, j := range backupJobs {
		r := w.res[j.id]
		if r == nil {
			continue
		}
		var last *jobRun
		var lastOK time.Time
		for i := range runs[j.id] {
			run := &runs[j.id][i]
			if within(t, run.begin, run.end) && (!run.manual || run.whole) {
				r.State = StateRunning
			}
			if run.manual || run.end.After(t) {
				continue
			}
			if last == nil || run.begin.After(last.begin) {
				last = run
			}
			if run.failed() == 0 && run.end.After(lastOK) {
				lastOK = run.end
			}
		}
		if last == nil {
			continue
		}
		failed := last.failed()
		r.Attrs["last_run.start"] = last.begin
		r.Attrs["last_run.end"] = last.end
		r.Attrs["last_run.status"] = map[bool]string{true: "ok", false: "failed"}[failed == 0]
		r.Attrs["last_run.ok"] = len(last.guests) - failed
		r.Attrs["last_run.failed"] = failed
		if failed > 0 {
			r.Attrs["last_run.error"] = last.firstErr()
		}
		if !lastOK.IsZero() {
			r.Attrs["last_ok"] = lastOK
		}
	}
}

// backupChangePoints lists, in (from, to], when runs start and end and when
// each guest's backup finishes or fails.
func (s *Sim) backupChangePoints(from, to time.Time) []time.Time {
	var out []time.Time
	add := func(r jobRun) {
		for _, t := range []time.Time{r.begin, r.end} {
			if t.After(from) && !t.After(to) {
				out = append(out, t)
			}
		}
		for _, g := range r.guests {
			if g.end.After(from) && !g.end.After(to) {
				out = append(out, g.end)
			}
		}
	}
	for day := dayStart(from).Add(-24 * time.Hour); !day.After(to); day = day.Add(24 * time.Hour) {
		for _, j := range backupJobs {
			add(s.nightly(j.id, day))
		}
	}
	for _, in := range s.inst {
		if sh, ok := in.impl.(backupShaper); ok {
			for _, r := range sh.manualRuns(s, in) {
				add(r)
			}
		}
	}
	return out
}

// ── disks spinning down ──────────────────────────────────────────────────

// diskSpin is when jupiter's array disks spin, as times of day (UTC). Unraid
// spins a disk down after 30 idle minutes. disk1–3 hold the media and photos
// that are read all day, so they never stop. The mover (cache to array, from
// 03:40) writes to disk6 and parity; evening viewing reads older shows from
// disk4; disk5 is cold storage. A disk not listed always spins.
var diskSpin = map[string][][2]time.Duration{
	"parity": {{3*time.Hour + 40*time.Minute, 4*time.Hour + 45*time.Minute}},
	"disk4":  {{19 * time.Hour, 23*time.Hour + 30*time.Minute}},
	"disk5":  nil,
	"disk6":  {{3*time.Hour + 40*time.Minute, 4*time.Hour + 45*time.Minute}},
}

// moverRuns is 1 while the mover writes to the array (03:40 to about 04:15).
func moverRuns(t time.Time) float64 { return bump(t, 3.95, 0.3) }

func diskSpins(name string, t time.Time) bool {
	wins, listed := diskSpin[name]
	if !listed {
		return true
	}
	of := t.Sub(dayStart(t))
	for _, w := range wins {
		if of >= w[0] && of < w[1] {
			return true
		}
	}
	return false
}

// unraidUnread reports whether Cha Dash can't read jupiter's Unraid API at
// t, and since when: its resources then keep what was read last.
func (s *Sim) unraidUnread(t time.Time) (time.Time, bool) {
	for _, in := range s.inst {
		if in.sc != ScenarioUnraidAPI || t.Before(in.open) {
			continue
		}
		if h := in.impl.horizon(in); h.IsZero() || t.Before(h) {
			return in.open, true
		}
	}
	return time.Time{}, false
}

// applyDisks spins disks down: state idle, which the Unraid integration
// reports with no temperature.
func (s *Sim) applyDisks(w *world) {
	at := w.t
	if since, unread := s.unraidUnread(at); unread {
		at = since
	}
	for name := range diskSpin {
		if r := w.res["unraid:jupiter/disk/"+name]; r != nil && !diskSpins(name, at) {
			r.State = StateIdle
		}
	}
}

func diskChangePoints(from, to time.Time) []time.Time {
	var out []time.Time
	for day := dayStart(from); !day.After(to); day = day.Add(24 * time.Hour) {
		for _, wins := range diskSpin {
			for _, w := range wins {
				for _, t := range []time.Time{day.Add(w[0]), day.Add(w[1])} {
					if t.After(from) && !t.After(to) {
						out = append(out, t)
					}
				}
			}
		}
	}
	return out
}

// ── camera detections ────────────────────────────────────────────────────

type weighted struct {
	label string
	w     float64
}

type camera struct {
	id, name string
	dayRate  float64 // detections per hour, 07:00–21:00
	labels   []weighted
}

var cameraTable = []camera{
	{"driveway", "Driveway", 0.9, []weighted{{"car", 0.5}, {"person", 0.45}, {"dog", 0.05}}},
	{"side_deck", "Side deck", 0.6, []weighted{{"person", 0.7}, {"cat", 0.3}}},
	{"backyard", "Backyard", 0.4, []weighted{{"dog", 0.4}, {"person", 0.35}, {"cat", 0.25}}},
}

// Detection is one Frigate event.
type Detection struct {
	At    time.Time `json:"at"`
	Label string    `json:"label"`
	Score float64   `json:"score"`
}

// detections returns a camera's events in hour bucket k (unix hours), in
// time order. Up to two per hour, more by day than by night.
func (s *Sim) detections(c camera, k int64) []Detection {
	key := hashKey("cam:" + c.id)
	h := float64(((k % 24) + 24) % 24)
	rate := 0.12
	if h >= 7 && h < 21 {
		rate = c.dayRate
	}
	var out []Detection
	for j := int64(0); j < 2; j++ {
		n := k*2 + j
		if s.rng.u(key, n) >= rate/2 {
			continue
		}
		at := time.Unix(k*3600+int64(s.rng.u(key^1, n)*3600), 0).UTC()
		u, label := s.rng.u(key^2, n), c.labels[len(c.labels)-1].label
		for _, l := range c.labels {
			if u < l.w {
				label = l.label
				break
			}
			u -= l.w
		}
		out = append(out, Detection{At: at, Label: label, Score: roundTo(0.7+0.25*s.rng.u(key^3, n), 2)})
	}
	if len(out) == 2 && out[1].At.Before(out[0].At) {
		out[0], out[1] = out[1], out[0]
	}
	return out
}

// detectionsIn counts a camera's events in (from, to].
func (s *Sim) detectionsIn(c camera, from, to time.Time) int {
	n := 0
	for k := floorDiv(from.Unix(), 3600); k <= floorDiv(to.Unix(), 3600); k++ {
		for _, d := range s.detections(c, k) {
			if d.At.After(from) && !d.At.After(to) {
				n++
			}
		}
	}
	return n
}

// lastDetection is a camera's latest event at or before t ("" = any label),
// looking back up to three days.
func (s *Sim) lastDetection(c camera, t time.Time, label string) (Detection, bool) {
	k0 := floorDiv(t.Unix(), 3600)
	for k := k0; k > k0-72; k-- {
		ds := s.detections(c, k)
		for i := len(ds) - 1; i >= 0; i-- {
			if !ds[i].At.After(t) && (label == "" || ds[i].Label == label) {
				return ds[i], true
			}
		}
	}
	return Detection{}, false
}

func (s *Sim) applyCameras(w *world) {
	for _, c := range cameraTable {
		r := w.res["frigate:camera/"+c.id]
		if r == nil {
			continue
		}
		if d, ok := s.lastDetection(c, w.t, ""); ok {
			r.Attrs["last_event.at"] = d.At
			r.Attrs["last_event.label"] = d.Label
			r.Attrs["last_event.score"] = d.Score
		}
		if d, ok := s.lastDetection(c, w.t, "person"); ok {
			r.Attrs["last_person.at"] = d.At
			r.Attrs["last_person.score"] = d.Score
		}
	}
}

func (s *Sim) cameraChangePoints(from, to time.Time) []time.Time {
	var out []time.Time
	for _, c := range cameraTable {
		for k := floorDiv(from.Unix(), 3600); k <= floorDiv(to.Unix(), 3600); k++ {
			for _, d := range s.detections(c, k) {
				if d.At.After(from) && !d.At.After(to) {
					out = append(out, d.At)
				}
			}
		}
	}
	return out
}

// Detections lists a camera's Frigate events in (from, to], oldest first.
// Camera IDs are the part after "frigate:camera/" (driveway, side_deck,
// backyard).
func (s *Sim) Detections(cameraID string, from, to time.Time) []Detection {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Detection
	for _, c := range cameraTable {
		if c.id != cameraID {
			continue
		}
		span := to.Sub(from)
		if span > 7*24*time.Hour || span < 0 {
			return nil
		}
		for k := floorDiv(from.Unix(), 3600); k <= floorDiv(to.Unix(), 3600); k++ {
			for _, d := range s.detections(c, k) {
				if d.At.After(from) && !d.At.After(to) {
					out = append(out, d)
				}
			}
		}
	}
	return out
}
