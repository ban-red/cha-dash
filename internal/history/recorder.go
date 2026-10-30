// Package history keeps metrics history on disk (ADR-0031, 13 · History).
//
// Samples roll up in memory into fixed-grid tiers: the open minute is an
// accumulator, and when a slot closes its point goes into the tier's open
// chunk and marks the next tier's slot as pending. A higher slot folds the
// points of the tier below once time passes its end. Chunks are written to
// history.db when they close, open ones every 10 minutes, so a crash loses
// at most that much, and a restart picks the open chunks up again.
package history

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"time"

	"dash.cha.sh/internal/store"
)

const (
	// flushEvery is how often closed chunks and new series are written.
	flushEvery = time.Minute
	// checkpointEvery is how often open chunks are written.
	checkpointEvery = 10 * time.Minute
	// retainEvery is how often retention runs.
	retainEvery = time.Hour
	// forgetAfter is how long a series with no chunks left is kept.
	forgetAfter = day
)

// noSlot marks a tier with nothing pending.
const noSlot = math.MinInt64

// Options configure a Recorder.
type Options struct {
	Store *store.Store // history.db (store.OpenHistory), migrated
	Level Level        // Standard or LongTerm
	Now   func() time.Time
	Log   *slog.Logger
}

// Recorder rolls samples up and writes them to history.db. Record and
// Sparkline are safe to call from any goroutine; Run does the writing.
type Recorder struct {
	st    *store.Store
	level Level
	top   Tier
	now   func() time.Time
	log   *slog.Logger

	// flushMu keeps a write (flush, retention) and Clear apart, so a flush
	// can't write rows for series Clear just removed.
	flushMu sync.Mutex

	mu     sync.Mutex
	series map[string]*series
	byID   map[int64]*series
	nextID int64
	sealed []chunkRow // closed chunks not yet written
	fresh  []*series  // series rows not yet written
	// states is each resource's newest health rows (states.go), and
	// pendingStates the rows not yet written.
	states        map[string]*stateTrack
	pendingStates []stateRow

	lastCheckpoint, lastRetain time.Time

	onLevel func(Level) // OnLevel's hook

	// maxBytes caps history.db (0: no cap), and trimmedAt is when retention
	// last dropped points to keep under it. Both under mu.
	maxBytes  int64
	trimmedAt time.Time
	// excluded are instances whose samples aren't recorded (their health
	// still is). Under mu.
	excluded map[string]bool
}

// OnLevel calls f with the level now and after every SetLevel. The hub
// uses it to poll for history while nobody watches only while it records.
// Set it once, before serving.
func (r *Recorder) OnLevel(f func(Level)) {
	r.onLevel = f
	f(r.Level())
}

type series struct {
	id                  int64
	key, instance, unit string
	firstAt, lastAt     int64
	fresh               bool // its row isn't written yet
	touched             bool // sampled since the last checkpoint

	acc  Point          // the open minute, at pend[Minute]
	pend [Day + 1]int64 // per tier, the slot whose point isn't written to it yet
	open [Day + 1]*chunk
}

type chunk struct {
	start int64
	pts   []Point
	dirty bool // changed since it was last written
}

type chunkRow struct {
	series int64
	tier   Tier
	start  int64
	data   []byte
}

// Open loads the series and the open chunks from history.db.
func Open(ctx context.Context, o Options) (*Recorder, error) {
	if _, err := ParseLevel(string(o.Level)); err != nil {
		return nil, err
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	now := o.Now()
	r := &Recorder{st: o.Store, level: o.Level, top: o.Level.top(), now: o.Now, log: o.Log,
		series: map[string]*series{}, byID: map[int64]*series{}, nextID: 1, states: map[string]*stateTrack{},
		lastCheckpoint: now, lastRetain: now.Add(-retainEvery)}

	rows, err := r.st.Reader().QueryContext(ctx, `SELECT id, key, instance, unit, first_at, last_at FROM series`)
	if err != nil {
		return nil, fmt.Errorf("history: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		s := newSeries()
		if err := rows.Scan(&s.id, &s.key, &s.instance, &s.unit, &s.firstAt, &s.lastAt); err != nil {
			return nil, fmt.Errorf("history: %w", err)
		}
		r.series[s.key], r.byID[s.id] = s, s
		r.nextID = max(r.nextID, s.id+1)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("history: %w", err)
	}

	for t := Minute; t <= r.top; t++ {
		if err := r.loadOpen(ctx, t, now.UnixMilli()); err != nil {
			return nil, err
		}
	}
	if err := r.loadStates(ctx); err != nil {
		return nil, err
	}
	r.repend(Quarter)
	return r, nil
}

// loadOpen reads tier t's chunks that are open at ms into memory, so new
// points add to them rather than replace them.
func (r *Recorder) loadOpen(ctx context.Context, t Tier, ms int64) error {
	start := floor(ms, t.chunk())
	rows, err := r.st.Reader().QueryContext(ctx, `SELECT series, data FROM chunks WHERE tier = ? AND start = ?`, int64(t), start)
	if err != nil {
		return fmt.Errorf("history: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var data []byte
		if err := rows.Scan(&id, &data); err != nil {
			return fmt.Errorf("history: %w", err)
		}
		pts, err := decode(data, t.size())
		s := r.byID[id]
		if err != nil || s == nil {
			r.log.Warn("history: skipping an unreadable chunk", "series", id, "tier", t, "start", start, "err", err)
			continue
		}
		if s.open[t] == nil {
			s.open[t] = &chunk{start: start, pts: pts}
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("history: %w", err)
	}
	return nil
}

// repend makes a higher slot pending again when its lower points were
// written but not yet folded, from tier from up.
func (r *Recorder) repend(from Tier) {
	for _, s := range r.series {
		for t := max(from, Quarter); t <= r.top; t++ {
			lower := s.open[t-1]
			if lower == nil {
				continue
			}
			last := -1
			for i, p := range lower.pts {
				if p.N > 0 {
					last = i
				}
			}
			if last < 0 {
				continue
			}
			slot := floor(lower.start+int64(last)*(t-1).step(), t.step())
			if c := s.open[t]; c == nil || c.start != floor(slot, t.chunk()) || c.pts[(slot-c.start)/t.step()].N == 0 {
				s.pend[t] = slot
			}
		}
	}
}

// SetLevel changes what the recorder keeps from now on. Tiers the new
// level drops are closed and written as they are; tiers it adds pick up
// their open chunks from disk. Off records nothing until another level.
func (r *Recorder) SetLevel(ctx context.Context, l Level) error {
	if _, err := ParseLevel(string(l)); err != nil {
		return err
	}
	r.mu.Lock()
	old := r.top
	if l.top() < old {
		// Write what the dropped tiers have so far, then close their
		// chunks. That folds the open slots below them early too; a slot
		// that gets more points is folded again, whole, when it closes. The
		// open minute is written only for Off: later samples would replace it.
		from := Quarter
		if l == Off {
			from = Minute
		}
		for _, s := range r.series {
			for t := from; t <= old; t++ {
				r.close(s, t, math.MaxInt64)
			}
			for t := l.top() + 1; t <= old; t++ {
				if s.open[t] != nil {
					r.seal(s, t)
				}
				s.pend[t] = noSlot
			}
		}
	}
	r.level, r.top = l, l.top()
	r.mu.Unlock()
	if r.onLevel != nil {
		r.onLevel(l)
	}
	if l.top() <= old {
		return r.flush(ctx, false)
	}
	now := r.now().UnixMilli()
	r.mu.Lock()
	defer r.mu.Unlock()
	for t := max(old+1, Minute); t <= r.top; t++ {
		if err := r.loadOpen(ctx, t, now); err != nil {
			return err
		}
	}
	r.repend(old + 1)
	return nil
}

func newSeries() *series {
	s := &series{}
	for i := range s.pend {
		s.pend[i] = noSlot
	}
	return s
}

// Record folds one sample in. NaN and infinite values are gaps, and a
// sample older than its series' open minute is dropped.
func (r *Recorder) Record(key, instance, unit string, at time.Time, v float64) {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return
	}
	ms := at.UnixMilli()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.top == 0 || r.excluded[instance] {
		return
	}
	s := r.series[key]
	if s == nil {
		s = newSeries()
		s.id, s.key, s.firstAt, s.fresh = r.nextID, key, ms, true
		r.nextID++
		r.series[key], r.byID[s.id] = s, s
		r.fresh = append(r.fresh, s)
	}
	if s.instance != instance || s.unit != unit {
		s.instance, s.unit, s.touched = instance, unit, true
	}
	r.advance(s, ms)
	slot := floor(ms, Minute.step())
	switch p := s.pend[Minute]; {
	case p == noSlot:
		if c := s.open[Minute]; c != nil && slot < c.start {
			return
		}
		s.pend[Minute] = slot
	case slot < p:
		return
	}
	s.acc.add(float32(v))
	s.lastAt, s.touched = max(s.lastAt, ms), true
}

// advance writes every pending slot that ms is past the end of, then
// closes the chunks ms is past the end of.
func (r *Recorder) advance(s *series, ms int64) {
	for t := Minute; t <= r.top; t++ {
		r.close(s, t, ms)
	}
	for t := Minute; t <= r.top; t++ {
		if c := s.open[t]; c != nil && ms >= c.start+t.chunk() {
			r.seal(s, t)
		}
	}
}

// close writes tier t's pending slot once ms is past its end: the open
// minute for Minute, the fold of the tier below for the others.
func (r *Recorder) close(s *series, t Tier, ms int64) {
	slot := s.pend[t]
	if slot == noSlot || ms < slot+t.step() {
		return
	}
	s.pend[t] = noSlot
	var p Point
	if t == Minute {
		p, s.acc = s.acc, Point{}
	} else if lower := s.open[t-1]; lower != nil && slot >= lower.start && slot < lower.start+(t-1).chunk() {
		i := int((slot - lower.start) / (t - 1).step())
		p = fold(lower.pts[i : i+int(t.step()/(t-1).step())])
	}
	if p.N == 0 {
		return
	}
	if t < r.top {
		// An older higher slot folds from the lower chunk before put can close it.
		up := floor(slot, (t + 1).step())
		if q := s.pend[t+1]; q != noSlot && q != up {
			r.close(s, t+1, math.MaxInt64)
		}
		defer func() { s.pend[t+1] = up }()
	}
	r.put(s, t, slot, p)
}

// put stores a point in tier t's open chunk, closing the chunk before it.
func (r *Recorder) put(s *series, t Tier, slot int64, p Point) {
	start := floor(slot, t.chunk())
	c := s.open[t]
	if c != nil && start < c.start {
		return // the clock went back
	}
	if c != nil && c.start != start {
		r.seal(s, t)
		c = nil
	}
	if c == nil {
		c = &chunk{start: start, pts: make([]Point, t.size())}
		s.open[t] = c
	}
	c.pts[(slot-start)/t.step()] = p
	c.dirty = true
}

// seal queues tier t's open chunk to be written for the last time.
func (r *Recorder) seal(s *series, t Tier) {
	if c := s.open[t]; c.dirty {
		r.sealed = append(r.sealed, chunkRow{series: s.id, tier: t, start: c.start, data: encode(c.pts)})
	}
	s.open[t] = nil
}

// Run writes what the recorder has gathered every minute, until ctx ends.
// Call Close after it returns.
func (r *Recorder) Run(ctx context.Context) {
	tick := time.NewTicker(flushEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			r.tick(ctx, r.now())
		}
	}
}

// tick closes what time has closed, writes, and every hour applies retention.
func (r *Recorder) tick(ctx context.Context, now time.Time) {
	r.mu.Lock()
	ms := now.UnixMilli()
	for _, s := range r.series {
		r.advance(s, ms)
	}
	checkpoint := now.Sub(r.lastCheckpoint) >= checkpointEvery
	r.mu.Unlock()
	if err := r.flush(ctx, checkpoint); err != nil {
		r.log.Warn("history: write failed", "err", err)
	} else if checkpoint {
		r.lastCheckpoint = now
	}
	if now.Sub(r.lastRetain) >= retainEvery {
		if err := r.retain(ctx, now); err != nil {
			r.log.Warn("history: retention failed", "err", err)
		}
		r.lastRetain = now
	}
}

// Close writes the open minutes and every open chunk, and marks every
// resource stopped. The open minutes are written as they are, without
// folding them upward, so a restart within the minute only loses that
// minute's samples from before it.
func (r *Recorder) Close(ctx context.Context) error {
	r.mu.Lock()
	for _, s := range r.series {
		if s.acc.N > 0 {
			r.put(s, Minute, s.pend[Minute], s.acc)
		}
	}
	r.stopAll(r.now().UnixMilli())
	r.mu.Unlock()
	return r.flush(ctx, true)
}

// flush writes new series and closed chunks, and with checkpoint the open
// chunks and each touched series' last sample time. Encoding happens under
// the lock and writing after it, so Record never waits on the disk. A
// failed write is queued again.
func (r *Recorder) flush(ctx context.Context, checkpoint bool) error {
	r.flushMu.Lock()
	defer r.flushMu.Unlock()
	type seriesRow struct {
		id                  int64
		key, instance, unit string
		firstAt, lastAt     int64
	}
	type openRef struct {
		s *series
		t Tier
		c *chunk
	}
	r.mu.Lock()
	sealed, fresh, states := r.sealed, r.fresh, r.pendingStates
	r.sealed, r.fresh, r.pendingStates = nil, nil, nil
	var rows []seriesRow
	var chunks []chunkRow
	var written []openRef
	row := func(s *series) {
		rows = append(rows, seriesRow{s.id, s.key, s.instance, s.unit, s.firstAt, s.lastAt})
		s.touched = false
	}
	for _, s := range fresh {
		s.fresh = false
		row(s)
	}
	for _, s := range r.series {
		if !checkpoint {
			break
		}
		if s.touched {
			row(s)
		}
		for t, c := range s.open {
			if c != nil && c.dirty {
				chunks = append(chunks, chunkRow{series: s.id, tier: Tier(t), start: c.start, data: encode(c.pts)})
				c.dirty = false
				written = append(written, openRef{s, Tier(t), c})
			}
		}
	}
	r.mu.Unlock()
	if len(rows) == 0 && len(sealed) == 0 && len(chunks) == 0 && len(states) == 0 {
		return nil
	}

	err := r.st.WriteTx(ctx, func(tx *sql.Tx) error {
		for _, s := range rows {
			if _, err := tx.ExecContext(ctx, `INSERT INTO series (id, key, instance, unit, first_at, last_at) VALUES (?, ?, ?, ?, ?, ?)
				ON CONFLICT (id) DO UPDATE SET instance = excluded.instance, unit = excluded.unit, last_at = excluded.last_at`,
				s.id, s.key, s.instance, s.unit, s.firstAt, s.lastAt); err != nil {
				return err
			}
		}
		for _, c := range append(sealed, chunks...) {
			if _, err := tx.ExecContext(ctx, `INSERT INTO chunks (series, tier, start, data) VALUES (?, ?, ?, ?)
				ON CONFLICT (series, tier, start) DO UPDATE SET data = excluded.data`,
				c.series, int64(c.tier), c.start, c.data); err != nil {
				return err
			}
		}
		for _, st := range states {
			if _, err := tx.ExecContext(ctx, `INSERT INTO states (resource, at, status) VALUES (?, ?, ?)
				ON CONFLICT (resource, at) DO UPDATE SET status = excluded.status`, st.resource, st.at, st.status); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		r.mu.Lock()
		r.sealed = append(sealed, r.sealed...)
		r.pendingStates = append(states, r.pendingStates...)
		for _, s := range fresh {
			s.fresh = true
		}
		r.fresh = append(fresh, r.fresh...)
		for _, s := range rows {
			if x := r.byID[s.id]; x != nil {
				x.touched = true
			}
		}
		for _, w := range written {
			if w.s.open[w.t] == w.c {
				w.c.dirty = true
			}
		}
		r.mu.Unlock()
		return fmt.Errorf("history: %w", err)
	}
	return nil
}

// retain deletes chunks past their tier's retention, then series that have
// no chunks left and haven't been sampled for a day, and reclaims a little
// space.
func (r *Recorder) retain(ctx context.Context, now time.Time) error {
	r.flushMu.Lock()
	defer r.flushMu.Unlock()
	ms := now.UnixMilli()
	var gone []int64
	var goneStates []string
	r.mu.Lock()
	level := r.level
	r.mu.Unlock()
	err := r.st.WriteTx(ctx, func(tx *sql.Tx) error {
		var err error
		if goneStates, err = retainStates(ctx, tx, level, ms); err != nil {
			return err
		}
		for t := Minute; t <= Day; t++ {
			keep := level.keep(t)
			if keep == 0 {
				continue
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM chunks WHERE tier = ? AND start <= ?`, int64(t), ms-keep.Milliseconds()-t.chunk()); err != nil {
				return err
			}
		}
		rows, err := tx.QueryContext(ctx, `DELETE FROM series WHERE last_at < ? AND NOT EXISTS (SELECT 1 FROM chunks WHERE chunks.series = series.id) RETURNING id`,
			ms-forgetAfter.Milliseconds())
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				return err
			}
			gone = append(gone, id)
		}
		return rows.Err()
	})
	if err != nil {
		return err
	}
	r.mu.Lock()
	cutoff := ms - level.keepStates().Milliseconds()
	for _, id := range goneStates {
		if t := r.states[id]; t != nil && t.last.at < cutoff {
			delete(r.states, id)
		}
	}
	for _, id := range gone {
		s := r.byID[id]
		if s == nil {
			continue
		}
		if s.lastAt < ms-forgetAfter.Milliseconds() && s.acc.N == 0 && !hasOpen(s) {
			delete(r.series, s.key)
			delete(r.byID, id)
		} else if !s.fresh {
			// Sampled again meanwhile: its row goes back.
			s.fresh, s.touched = true, true
			r.fresh = append(r.fresh, s)
		}
	}
	r.mu.Unlock()
	trimmed, err := r.trim(ctx, now)
	if err != nil {
		return err
	}
	vacuum := `PRAGMA incremental_vacuum(256)`
	if trimmed {
		vacuum = `PRAGMA incremental_vacuum` // give it all back: the file is over its limit
	}
	_, err = r.st.Writer().ExecContext(ctx, vacuum)
	return err
}

func hasOpen(s *series) bool {
	for _, c := range s.open {
		if c != nil {
			return true
		}
	}
	return false
}

// Range is a run of one tier's points: point i is at Start + i × Step.
type Range struct {
	Unit   string
	Start  time.Time
	Step   time.Duration
	Points []Point
}

// Level is what the recorder keeps.
func (r *Recorder) Level() Level {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.level
}

// Known says whether the recorder has seen a series.
func (r *Recorder) Known(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.series[key] != nil
}

// Query returns a series' points from from to to, in the finest tier that
// gives at most max points (the coarsest written tier when none does). An
// unknown series is an empty range.
func (r *Recorder) Query(ctx context.Context, key string, from, to time.Time, maxPoints int) (Range, error) {
	r.mu.Lock()
	top := max(r.top, Quarter) // with recording off, what was kept is still there
	r.mu.Unlock()
	t := top
	for c := Minute; c <= top; c++ {
		if to.Sub(from)/tiers[c].step <= time.Duration(maxPoints) {
			t = c
			break
		}
	}
	start := floor(from.UnixMilli(), t.step())
	pts, err := r.points(ctx, key, t, start, to.UnixMilli())
	var unit string
	r.mu.Lock()
	if s := r.series[key]; s != nil {
		unit = s.unit
	}
	r.mu.Unlock()
	return Range{Unit: unit, Start: time.UnixMilli(start).UTC(), Step: tiers[t].step, Points: pts}, err
}

// Sparkline is n points step apart, the newest at newest, each the average
// of the 15-minute points in its slot; NaN where there are none. step is a
// multiple of 15 minutes. It's how a series' sparkline gets its past back
// after a restart.
func (r *Recorder) Sparkline(key string, newest time.Time, n int, step time.Duration) []float64 {
	per := int(step / tiers[Quarter].step)
	if per < 1 || step%tiers[Quarter].step != 0 {
		return nil
	}
	from := newest.UnixMilli() - int64(n-1)*step.Milliseconds()
	pts, err := r.points(context.Background(), key, Quarter, from, newest.UnixMilli()+step.Milliseconds())
	if err != nil {
		r.log.Warn("history: sparkline", "series", key, "err", err)
		return nil
	}
	out := make([]float64, n)
	for i := range out {
		out[i] = math.NaN()
		if lo := i * per; lo < len(pts) {
			if p := fold(pts[lo:min(lo+per, len(pts))]); p.N > 0 {
				out[i] = float64(p.Avg)
			}
		}
	}
	return out
}

// points reads tier t's points of a series in [from, to) (from aligned to
// the step) from disk, then the open chunk from memory over them.
func (r *Recorder) points(ctx context.Context, key string, t Tier, from, to int64) ([]Point, error) {
	if to <= from {
		return nil, nil
	}
	step := t.step()
	out := make([]Point, (to-from+step-1)/step)
	r.mu.Lock()
	s := r.series[key]
	var open *chunk
	var id int64
	var sealed []chunkRow // closed, but not written yet
	if s != nil {
		id = s.id
		if c := s.open[t]; c != nil {
			open = &chunk{start: c.start, pts: append([]Point(nil), c.pts...)}
		}
		for _, c := range r.sealed {
			if c.series == id && c.tier == t {
				sealed = append(sealed, c)
			}
		}
	}
	r.mu.Unlock()
	if s == nil {
		return out, nil
	}
	place := func(start int64, pts []Point) {
		for i, p := range pts {
			if at := start + int64(i)*step; at >= from && at < to {
				out[(at-from)/step] = p
			}
		}
	}
	rows, err := r.st.Reader().QueryContext(ctx, `SELECT start, data FROM chunks WHERE series = ? AND tier = ? AND start > ? AND start < ? ORDER BY start`,
		id, int64(t), from-t.chunk(), to)
	if err != nil {
		return nil, fmt.Errorf("history: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var start int64
		var data []byte
		if err := rows.Scan(&start, &data); err != nil {
			return nil, fmt.Errorf("history: %w", err)
		}
		pts, err := decode(data, t.size())
		if err != nil {
			continue
		}
		place(start, pts)
	}
	if err := rows.Err(); err != nil && !errors.Is(err, context.Canceled) {
		return nil, fmt.Errorf("history: %w", err)
	}
	for _, c := range sealed {
		if pts, err := decode(c.data, t.size()); err == nil {
			place(c.start, pts)
		}
	}
	if open != nil {
		place(open.start, open.pts)
	}
	return out, nil
}

// DefaultMaxBytes is history.db's size limit until an admin sets one: 1 GB,
// in the decimal units Settings → History shows.
const DefaultMaxBytes = 1_000_000_000

// SetLimit caps history.db at limit bytes of pages in use; 0 is no cap.
// Retention enforces it, hourly.
func (r *Recorder) SetLimit(limit int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.maxBytes = limit
}

// Limit is the cap SetLimit set.
func (r *Recorder) Limit() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.maxBytes
}

// trimRounds bounds one trim; each drops a tenth of a tier.
const trimRounds = 50

// trim keeps history.db under its limit: while the pages in use are over
// it, the tier holding the most data loses its oldest tenth. That's
// usually the finest tier, whose points are the least needed once they're
// a few days old; health rows are never trimmed. It reports whether it
// dropped anything.
func (r *Recorder) trim(ctx context.Context, now time.Time) (bool, error) {
	limit := r.Limit()
	if limit <= 0 {
		return false, nil
	}
	w := r.st.Writer()
	trimmed := false
	for range trimRounds {
		var pages, free, size int64
		if err := w.QueryRowContext(ctx, `SELECT (SELECT page_count FROM pragma_page_count), (SELECT freelist_count FROM pragma_freelist_count), (SELECT page_size FROM pragma_page_size)`).
			Scan(&pages, &free, &size); err != nil {
			return trimmed, fmt.Errorf("history: %w", err)
		}
		if (pages-free)*size <= limit {
			break
		}
		var tier, n int64
		err := w.QueryRowContext(ctx, `SELECT tier, count(*) FROM chunks GROUP BY tier ORDER BY sum(length(data)) DESC LIMIT 1`).Scan(&tier, &n)
		if errors.Is(err, sql.ErrNoRows) {
			break // only health rows left
		}
		if err != nil {
			return trimmed, fmt.Errorf("history: %w", err)
		}
		if _, err := w.ExecContext(ctx, `DELETE FROM chunks WHERE id IN (SELECT id FROM chunks WHERE tier = ? ORDER BY start LIMIT ?)`, tier, max(1, n/10)); err != nil {
			return trimmed, fmt.Errorf("history: %w", err)
		}
		trimmed = true
	}
	if trimmed {
		r.mu.Lock()
		r.trimmedAt = now
		r.mu.Unlock()
		r.log.Warn("history: over its size limit; dropped the oldest points", "limit_bytes", limit)
	}
	return trimmed, nil
}
