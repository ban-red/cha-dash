package sim

import "time"

// Routines are the everyday rhythm of the home, independent of incidents:
// nightly backups and camera detections. Like everything else they are pure
// functions of time.

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

// jobWindow is when a job runs on a given day.
func jobWindow(job string, day time.Time) (begin, end time.Time) {
	for _, sl := range backupSlots {
		if sl.job != job {
			continue
		}
		if b := sl.begin(day); begin.IsZero() || b.Before(begin) {
			begin = b
		}
		if e := sl.end(day); e.After(end) {
			end = e
		}
	}
	return begin, end
}

// failures returns the backup-failed instances that target a guest.
func (s *Sim) failures(guest string) []*instance {
	var out []*instance
	for _, in := range s.inst {
		if in.sc == ScenarioBackupFailed && guest == bfGuest {
			out = append(out, in)
		}
	}
	return out
}

// replaced reports whether a failure took the place of a guest's nightly run
// on day: the failure happened inside that run's slot.
func (s *Sim) replaced(sl backupSlot, day time.Time) bool {
	for _, in := range s.failures(sl.guest) {
		if !in.open.Before(sl.begin(day)) && !in.open.After(sl.end(day)) {
			return true
		}
	}
	return false
}

type backupEvent struct {
	at time.Time
	ok bool
}

// backupEvents lists a guest's recent backup results up to t, in no
// particular order: nightly runs, failures and retries.
func (s *Sim) backupEvents(sl backupSlot, t time.Time) []backupEvent {
	var out []backupEvent
	today := dayStart(t)
	for i := 3; i >= 0; i-- {
		day := today.Add(-time.Duration(i) * 24 * time.Hour)
		if e := sl.end(day); !e.After(t) && !s.replaced(sl, day) {
			out = append(out, backupEvent{e, true})
		}
	}
	for _, in := range s.failures(sl.guest) {
		if !in.open.After(t) {
			out = append(out, backupEvent{in.open, false})
		}
		if !in.fix.IsZero() {
			if r := in.fix.Add(bfRetry); !r.After(t) && in.fix.Before(in.impl.horizon(in)) {
				out = append(out, backupEvent{r, true})
			}
		}
	}
	return out
}

// lastBackupOK is the guest's latest successful backup at or before t.
func (s *Sim) lastBackupOK(guest string, t time.Time) time.Time {
	var last time.Time
	for _, ev := range s.backupEvents(slotOf(guest), t) {
		if ev.ok && ev.at.After(last) {
			last = ev.at
		}
	}
	return last
}

// failedBackupsIn counts failed backup tasks in (from, to].
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
	for _, sl := range backupSlots {
		g := w.res[sl.guest]
		if g == nil {
			continue
		}
		var last backupEvent
		var lastOK time.Time
		for _, ev := range s.backupEvents(sl, t) {
			if !ev.at.Before(last.at) {
				last = ev
			}
			if ev.ok && ev.at.After(lastOK) {
				lastOK = ev.at
			}
		}
		if !last.at.IsZero() {
			g.Attrs["backup.last_at"] = last.at
			g.Attrs["backup.last_status"] = map[bool]string{true: "ok", false: "failed"}[last.ok]
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
		today := dayStart(t)
		if b, e := jobWindow(j.id, today); within(t, b, e) {
			r.State = StateRunning
		}
		// The last finished run.
		day := today
		if _, e := jobWindow(j.id, day); e.After(t) {
			day = day.Add(-24 * time.Hour)
		}
		b, e := jobWindow(j.id, day)
		total, failed := 0, 0
		for _, sl := range backupSlots {
			if sl.job == j.id {
				total++
				if s.replaced(sl, day) {
					failed++
				}
			}
		}
		status := "ok"
		if failed > 0 {
			status = "failed"
		}
		r.Attrs["last_run.start"] = b
		r.Attrs["last_run.end"] = e
		r.Attrs["last_run.status"] = status
		r.Attrs["last_run.ok"] = total - failed
		r.Attrs["last_run.failed"] = failed
	}
}

// backupChangePoints lists slot boundaries in (from, to].
func backupChangePoints(from, to time.Time) []time.Time {
	var out []time.Time
	for day := dayStart(from).Add(-24 * time.Hour); !day.After(to); day = day.Add(24 * time.Hour) {
		for _, sl := range backupSlots {
			for _, t := range []time.Time{sl.begin(day), sl.end(day)} {
				if t.After(from) && !t.After(to) {
					out = append(out, t)
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
