package sim

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"
)

// DefaultStart is the default virtual start time: midnight UTC on Saturday
// 3 October 2026, the default script's day.
var DefaultStart = time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)

type config struct {
	start       time.Time
	script      Script
	sampleEvery time.Duration
	histPoints  int
	histStep    time.Duration
	shown       time.Duration // added to times in copy (WithShownOffset)
}

// Option configures a Sim.
type Option func(*config)

// WithStart sets the virtual start time (default DefaultStart). Script
// offsets count from it. The home's routines (nightly backups at 02:00,
// evening media, daylight on the cameras) follow the UTC wall clock, so the
// default script's story lines up best with a start at 00:00 UTC.
func WithStart(t time.Time) Option {
	return func(c *config) { c.start = t.UTC().Truncate(time.Second) }
}

// WithShownOffset shifts the times the copy quotes ("since 12:05") by d,
// whole minutes, for a page that shows the simulated clock shifted by d.
// Times in data stay as they are.
func WithShownOffset(d time.Duration) Option {
	return func(c *config) { c.shown = d.Truncate(time.Minute) }
}

// WithScript replaces the default script. An empty script gives a quiet home.
func WithScript(s Script) Option { return func(c *config) { c.script = slices.Clone(s) } }

// WithSampleInterval sets how often Advance emits metric samples (default
// 30s, whole seconds, at least 1s). Samples fall on multiples of the interval
// since the Unix epoch.
func WithSampleInterval(d time.Duration) Option { return func(c *config) { c.sampleEvery = d } }

// WithHistory sets the sparkline history in snapshots (default 48 points,
// 30 minutes apart: the last 24 hours).
func WithHistory(points int, step time.Duration) Option {
	return func(c *config) { c.histPoints, c.histStep = points, step }
}

// Sim is a deterministic simulation of the Demo Home. It is safe for
// concurrent use.
type Sim struct {
	mu      sync.Mutex
	seed    int64
	cfg     config
	rng     rng
	top     *topology
	inst    []*instance
	byID    map[string]*instance
	now     time.Time
	seq     uint64
	cur     *world
	impacts map[string][]Impact // blast radius cache, over the static relations
}

// New creates a simulation at its start time. The same seed and options
// always produce the same home, metrics and event stream.
func New(seed int64, opts ...Option) (*Sim, error) {
	cfg := config{start: DefaultStart, script: DefaultScript(), sampleEvery: 30 * time.Second, histPoints: 48, histStep: 30 * time.Minute}
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.sampleEvery < time.Second || cfg.sampleEvery%time.Second != 0 {
		return nil, fmt.Errorf("sim: sample interval %v must be whole seconds, at least 1s", cfg.sampleEvery)
	}
	if cfg.histPoints < 2 || cfg.histStep < time.Second {
		return nil, errors.New("sim: history needs at least 2 points and a step of at least 1s")
	}
	if err := cfg.script.Validate(); err != nil {
		return nil, err
	}
	s := &Sim{seed: seed, cfg: cfg, rng: newRNG(seed), now: cfg.start, byID: map[string]*instance{}, impacts: map[string][]Impact{}}
	s.top = buildTopology(cfg.start, s.rng)
	for _, e := range cfg.script {
		id := e.ID
		if id == "" {
			id = string(e.Scenario)
		}
		in := &instance{id: id, sc: e.Scenario, impl: scenarios[e.Scenario], open: cfg.start.Add(time.Duration(e.Open))}
		if e.Fix != 0 {
			in.fix = cfg.start.Add(time.Duration(e.Fix))
		}
		s.inst = append(s.inst, in)
		s.byID[id] = in
	}
	slices.SortStableFunc(s.inst, func(a, b *instance) int { return cmp.Or(a.open.Compare(b.open), cmp.Compare(a.id, b.id)) })
	for _, m := range s.top.specs {
		for _, in := range s.inst {
			if in.impl.touches(m.res, m.key) {
				m.adj = append(m.adj, in)
			}
		}
	}
	s.cur = s.world(s.now, true)
	return s, nil
}

// Seed returns the simulation's seed.
func (s *Sim) Seed() int64 { return s.seed }

// Start returns the virtual start time.
func (s *Sim) Start() time.Time { return s.cfg.start }

// Now returns the virtual clock.
func (s *Sim) Now() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.now
}

// Script returns the script the simulation runs.
func (s *Sim) Script() Script { return slices.Clone(s.cfg.script) }

// Advance moves the clock forward by d and returns every event in (now,
// now+d], in order. The result does not depend on how the time is split
// into calls: Advance(time.Hour) gives the same events as sixty calls of
// Advance(time.Minute).
func (s *Sim) Advance(d time.Duration) []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	if d <= 0 {
		return nil
	}
	return s.advance(s.now.Add(d), true)
}

// AdvanceTo moves the clock forward to t (no-op if t is not after now).
func (s *Sim) AdvanceTo(t time.Time) []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !t.After(s.now) {
		return nil
	}
	return s.advance(t, true)
}

// Skip moves the clock forward by d without producing events. The state
// afterwards is identical to Advance(d); only the event stream (and Seq) is
// skipped, so subscribers must take a new Snapshot.
func (s *Sim) Skip(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if d <= 0 {
		return
	}
	s.now = s.now.Add(d)
	s.cur = s.world(s.now, true)
}

// Fix applies an open incident's suggested action now, as if the user had
// pressed it, and returns the events it causes at once. The incident then
// resolves the way the scenario describes (for example, after garbage
// collection finishes). It replaces any later scripted fix.
func (s *Sim) Fix(id string) ([]Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	in, err := s.fixable(id)
	if err != nil {
		return nil, err
	}
	in.fix = s.now
	w := s.world(s.now, true)
	ev := s.diff(s.cur, w)
	s.cur = w
	return ev, nil
}

// CanFix reports whether Fix would accept the incident now, without fixing
// it: nil, or the error Fix would return.
func (s *Sim) CanFix(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.fixable(id)
	return err
}

// fixable returns the incident if it can be fixed now. The caller holds s.mu.
func (s *Sim) fixable(id string) (*instance, error) {
	in, ok := s.byID[id]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownIncident, id)
	}
	if s.now.Before(in.open) {
		return nil, fmt.Errorf("%w: incident %q opens %s", ErrNotOpen, id, in.open.Format(time.RFC3339))
	}
	if h := in.impl.horizon(in); !h.IsZero() && !s.now.Before(h) {
		return nil, fmt.Errorf("%w: incident %q", ErrResolved, id)
	}
	if !in.fix.IsZero() && !in.fix.After(s.now) {
		return nil, fmt.Errorf("%w: incident %q, since %s", ErrFixing, id, in.fix.Format(time.RFC3339))
	}
	return in, nil
}

// BlastRadius returns what an action on the resource would affect, over the
// Demo Home's relations (see the package BlastRadius).
func (s *Sim) BlastRadius(id string) []Impact {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.impactOf(id))
}

func (s *Sim) impactOf(id string) []Impact {
	if im, ok := s.impacts[id]; ok {
		return im
	}
	im := BlastRadius(s.top.rels, id)
	s.impacts[id] = im
	return im
}

// impact is impactOf for scenario code (called with the lock held).
func (s *Sim) impact(id string) []Impact { return slices.Clone(s.impactOf(id)) }

// ── the world at time t ──────────────────────────────────────────────────

// world is the non-metric state at one moment: resources, relations and
// incidents. It is a pure function of the seed, the script (plus fixes) and
// the time, and only changes at change points.
type world struct {
	t         time.Time
	res       map[string]*Resource
	rels      []Relation
	incidents []*Incident
}

func cloneResource(r *Resource) *Resource {
	c := *r
	c.Attrs = maps.Clone(r.Attrs)
	if c.Attrs == nil {
		c.Attrs = map[string]any{}
	}
	return &c
}

// world computes the state at t. Without full, incidents are skipped (enough
// to tell which metrics are readable).
func (s *Sim) world(t time.Time, full bool) *world {
	w := &world{t: t, res: make(map[string]*Resource, len(s.top.res))}
	for id, r := range s.top.res {
		w.res[id] = cloneResource(r)
	}
	s.applyBackups(w)
	s.applyDisks(w)
	s.applyCameras(w)
	s.applyWeather(w)
	for _, in := range s.inst {
		in.impl.apply(s, in, w)
	}
	for _, r := range w.res {
		if len(r.Attrs) == 0 {
			r.Attrs = nil
		}
	}
	w.rels = make([]Relation, 0, len(s.top.rels))
	for _, rel := range s.top.rels {
		if w.res[rel.From] != nil && w.res[rel.To] != nil {
			w.rels = append(w.rels, rel)
		}
	}
	if full {
		for _, in := range s.inst {
			if inc := in.impl.incident(s, in, t); inc != nil {
				w.incidents = append(w.incidents, inc)
			}
		}
		s.correlate(w)
		slices.SortFunc(w.incidents, func(a, b *Incident) int {
			return cmp.Or(a.OpenedAt.Compare(b.OpenedAt), cmp.Compare(a.ID, b.ID))
		})
	}
	return w
}

// changePoints lists the moments in (from, to] where the world may change.
func (s *Sim) changePoints(from, to time.Time) []time.Time {
	pts := s.backupChangePoints(from, to)
	pts = append(pts, diskChangePoints(from, to)...)
	pts = append(pts, weatherChangePoints(from, to)...)
	pts = append(pts, s.cameraChangePoints(from, to)...)
	for _, in := range s.inst {
		for _, t := range in.impl.changePoints(in) {
			if !t.IsZero() && t.After(from) && !t.After(to) {
				pts = append(pts, t)
			}
		}
	}
	slices.SortFunc(pts, time.Time.Compare)
	return slices.CompactFunc(pts, time.Time.Equal)
}

// ── metrics ──────────────────────────────────────────────────────────────

// eval gives metric functions access to the simulation and the world.
type eval struct {
	s *Sim
	w *world
}

func (e *eval) raw(res, key string, t time.Time) float64 {
	m := e.s.top.spec(res, key)
	if m == nil {
		panic("sim: no metric " + res + "|" + key)
	}
	return e.s.raw(m, t, e.w)
}

func (s *Sim) raw(m *metricSpec, t time.Time, w *world) float64 {
	v := m.fn(&eval{s: s, w: w}, m, t)
	for _, in := range m.adj {
		v = in.impl.adjust(s, in, m, t, v)
	}
	return v
}

func reporting(state string) bool {
	switch state {
	case StateRunning, StateOnline, StateIdle, StateConnected, StateEnabled:
		return true
	}
	return false
}

// available reports whether a metric can be read in w: its resource exists
// and is up, and the integration that reports it is connected. A readable
// series can still say "unknown" (a spun-down disk's temperature); one that
// can't be read keeps its last-known value, marked stale.
func (s *Sim) available(m *metricSpec, w *world) bool {
	r := w.res[m.res]
	if r == nil || !reporting(r.State) {
		return false
	}
	if m.source != "" {
		src := w.res[m.source]
		if src == nil || src.State != StateConnected {
			return false
		}
	}
	return true
}

func (s *Sim) value(m *metricSpec, t time.Time, w *world) Num {
	if !s.available(m, w) || (m.awake && w.res[m.res].State == StateIdle) {
		return Unknown()
	}
	return Num(roundTo(clamp(s.raw(m, t, w), m.min, m.max), m.dec))
}

func (s *Sim) every() int64 { return int64(s.cfg.sampleEvery / time.Second) }

// lastTick is the latest sample time at or before t.
func (s *Sim) lastTick(t time.Time) time.Time {
	return time.Unix(floorDiv(t.Unix(), s.every())*s.every(), 0).UTC()
}

// nextTick is the first sample time after t.
func (s *Sim) nextTick(t time.Time) time.Time {
	return time.Unix((floorDiv(t.Unix(), s.every())+1)*s.every(), 0).UTC()
}

// samples are the readable series at t. A series reported as unknown comes
// with a null value; one that can't be read isn't sampled at all.
func (s *Sim) samples(t time.Time, w *world) []Sample {
	out := make([]Sample, 0, len(s.top.specs))
	for _, m := range s.top.specs {
		if s.available(m, w) {
			out = append(out, Sample{Resource: m.res, Metric: m.key, Value: s.value(m, t, w)})
		}
	}
	return out
}

// ── advancing ────────────────────────────────────────────────────────────

func (s *Sim) advance(end time.Time, metrics bool) []Event {
	var out []Event
	cps := s.changePoints(s.now, end)
	tick := s.nextTick(s.now)
	for i := 0; i < len(cps) || (metrics && !tick.After(end)); {
		var t time.Time
		switch {
		case i < len(cps) && (!metrics || tick.After(end) || !cps[i].After(tick)):
			t = cps[i]
		default:
			t = tick
		}
		if i < len(cps) && cps[i].Equal(t) {
			w := s.world(t, true)
			out = append(out, s.diff(s.cur, w)...)
			s.cur = w
			i++
		}
		if metrics && tick.Equal(t) {
			if smp := s.samples(t, s.cur); len(smp) > 0 {
				s.seq++
				out = append(out, Event{Seq: s.seq, At: t, Type: EventMetrics, Samples: smp})
			}
			tick = tick.Add(s.cfg.sampleEvery)
		}
	}
	s.now = end
	return out
}

func encode(v any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		panic("sim: " + err.Error())
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}

// diff turns two consecutive worlds into events, ordered so that a consumer
// applying them one by one never holds a relation to a missing resource:
// relation removals, resource removals, resource upserts, relation upserts,
// then incident events.
func (s *Sim) diff(a, b *world) []Event {
	var out []Event
	emit := func(e Event) {
		s.seq++
		e.Seq, e.At = s.seq, b.t
		out = append(out, e)
	}
	inA := map[Relation]bool{}
	for _, r := range a.rels {
		inA[r] = true
	}
	inB := map[Relation]bool{}
	for _, r := range b.rels {
		inB[r] = true
	}
	for _, r := range a.rels {
		if !inB[r] {
			emit(Event{Type: EventRelationRemove, Relation: &r})
		}
	}
	for _, id := range slices.Sorted(maps.Keys(a.res)) {
		if b.res[id] == nil {
			emit(Event{Type: EventResourceRemove, ID: id})
		}
	}
	for _, id := range slices.Sorted(maps.Keys(b.res)) {
		rb := b.res[id]
		if ra := a.res[id]; ra == nil || !bytes.Equal(encode(ra), encode(rb)) {
			c := exportResource(rb)
			emit(Event{Type: EventResourceUpsert, Resource: &c})
		}
	}
	for _, r := range b.rels {
		if !inA[r] {
			emit(Event{Type: EventRelationUpsert, Relation: &r})
		}
	}
	prev := map[string]*Incident{}
	for _, inc := range a.incidents {
		prev[inc.ID] = inc
	}
	next := slices.Clone(b.incidents)
	slices.SortFunc(next, func(x, y *Incident) int { return cmp.Compare(x.ID, y.ID) })
	for _, inc := range next {
		p := prev[inc.ID]
		switch {
		case p == nil:
			emit(Event{Type: EventIncidentOpen, Incident: exportIncident(inc)})
			if inc.Status == StatusResolved { // opened and resolved at the same instant
				emit(Event{Type: EventIncidentResolve, Incident: exportIncident(inc)})
			}
		case bytes.Equal(encode(p), encode(inc)):
		case inc.Status == StatusResolved && p.Status == StatusOpen:
			emit(Event{Type: EventIncidentResolve, Incident: exportIncident(inc)})
		default:
			emit(Event{Type: EventIncidentUpdate, Incident: exportIncident(inc)})
		}
	}
	return out
}

// ── snapshots ────────────────────────────────────────────────────────────

// exportResource deep-copies a resource for callers, so changing a snapshot
// or an event can never change the simulation.
func exportResource(r *Resource) Resource {
	c := *r
	c.Labels = maps.Clone(r.Labels)
	c.Attrs = maps.Clone(r.Attrs)
	c.Links = slices.Clone(r.Links)
	c.Caps = slices.Clone(r.Caps)
	return c
}

func exportAction(a Action) Action {
	a.Impact = slices.Clone(a.Impact)
	return a
}

func exportIncident(in *Incident) *Incident {
	c := *in
	c.Facts = maps.Clone(in.Facts)
	c.Causes = slices.Clone(in.Causes)
	c.BlastRadius = slices.Clone(in.BlastRadius)
	c.Suggested = exportAction(in.Suggested)
	c.Alternatives = nil
	for _, a := range in.Alternatives {
		c.Alternatives = append(c.Alternatives, exportAction(a))
	}
	return &c
}

// Snapshot returns the full state at the current time, including a short
// history of every metric for sparklines.
func (s *Sim) Snapshot() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := State{Seed: s.seed, Start: s.cfg.start, Now: s.now, Seq: s.seq}
	for _, id := range slices.Sorted(maps.Keys(s.cur.res)) {
		st.Resources = append(st.Resources, exportResource(s.cur.res[id]))
	}
	st.Relations = slices.Clone(s.cur.rels)
	for _, inc := range s.cur.incidents {
		st.Incidents = append(st.Incidents, *exportIncident(inc))
	}
	if st.Incidents == nil {
		st.Incidents = []Incident{}
	}
	st.History, st.Series = s.series(s.now)
	return st
}

// series computes every metric's latest value and history as of now.
func (s *Sim) series(now time.Time) (HistoryAxis, []Series) {
	n, step := s.cfg.histPoints, s.cfg.histStep
	end := s.lastTick(now)
	ax := HistoryAxis{Start: end.Add(-time.Duration(n-1) * step), End: end, StepSeconds: int(step / time.Second), Points: n}
	worlds := make([]*world, n)
	for i := range n {
		worlds[i] = s.world(ax.Start.Add(time.Duration(i)*step), false)
	}
	out := make([]Series, 0, len(s.top.specs))
	for _, m := range s.top.specs {
		se := Series{Resource: m.res, Metric: m.key, Unit: m.unit, Min: m.min, Max: m.max, Band: m.band, Warn: m.warn, Crit: m.crit,
			History: make([]Num, n)}
		for i, w := range worlds {
			se.History[i] = s.value(m, w.t, w)
		}
		se.Value = se.History[n-1]
		switch {
		case s.available(m, worlds[n-1]):
			se.AsOf = end // read, even if what it says is "unknown"
		default:
			// Not readable: the last sample stands. A known value is stale;
			// an unknown one stays unknown, as of when it was read.
			if v, at, ok := s.lastKnown(m, end); ok {
				se.Value, se.AsOf, se.Stale = v, at, v.Valid()
			}
		}
		out = append(out, se)
	}
	return ax, out
}

// lastKnown finds a metric's most recent sample before end, walking back
// through change points (up to two days). Its value may be unknown: a
// spun-down disk's temperature.
func (s *Sim) lastKnown(m *metricSpec, end time.Time) (Num, time.Time, bool) {
	// The world is constant between change points, so test one tick per
	// interval, newest first. The interval ending at `end` is already known
	// to be unreadable.
	cps := s.changePoints(end.Add(-48*time.Hour), end)
	for i, cp := range slices.Backward(cps) {
		tk := s.lastTick(cp.Add(-time.Nanosecond))
		if i > 0 && tk.Before(cps[i-1]) {
			continue // no sample tick in this interval
		}
		if w := s.world(tk, false); s.available(m, w) {
			return s.value(m, tk, w), tk, true
		}
	}
	return Unknown(), time.Time{}, false
}
