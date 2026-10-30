package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/livedoc"
	"dash.cha.sh/internal/security"
	"dash.cha.sh/internal/topics"
)

// History matches the Demo Home's sparklines: 48 points, 30 minutes apart,
// on the Unix-epoch grid, newest last.
const (
	HistoryPoints = 48
	HistoryStep   = 30 * time.Minute
)

// History keeps series and health on disk (internal/history). The emitter
// records every valid sample and every status change there. When a series
// first appears it takes its sparkline's past from it, and when a resource
// first appears, when its status began, so a restart blanks neither.
type History interface {
	Record(key, instance, unit string, at time.Time, v float64)
	// Sparkline is points averages step apart, the newest at newest; NaN
	// where there's nothing.
	Sparkline(key string, newest time.Time, points int, step time.Duration) []float64
	// Since is when resource's status began, if history has it as the
	// newest; otherwise now.
	Since(resource, status string, now time.Time) time.Time
	// State records resource's status, begun at since; Gone that it left.
	State(resource, status string, since time.Time)
	Gone(resource string, at time.Time)
}

// ResolvedRetention is how long a resolved incident stays in the inbox.
const ResolvedRetention = 24 * time.Hour

// Emitter is an instance's integration.Emitter: it writes resources and
// relations to the Home Graph, series to the metrics topic and incidents to
// the inbox, in the shapes the demo uses, and remembers what the instance
// owns so Replace can remove what it no longer reports.
//
// One Emitter lives as long as its instance, across restarts, so a config
// change doesn't make the instance's resources blink out and back.
type Emitter struct {
	t      *topics.Set
	id     string // instance ID
	self   string // "integration:<id>"
	now    func() time.Time
	report func(version string, warnings []integration.Warning, setVersion, setWarnings bool)
	// posture is the hub's security posture, read under mu each time an
	// incident is published. Nil publishes declared severities.
	posture func() security.Posture
	// annotate fills an incident action's availability (plan_ready,
	// unavailable, recipe) as it's published. Nil leaves them out.
	annotate func(*integration.Action)
	// history records samples; nil records nothing.
	history History

	mu     sync.Mutex
	scopes map[string]*owned
	marks  map[string]healthMark // resource ID → current health status and since
	series map[string]*seriesDoc // series key → document
	// incidents are as the instance raised them, with the declared
	// severity; the inbox gets published(in).
	incidents map[string]integration.Incident
}

type owned struct {
	res     map[string]bool
	rel     map[string]graph.Relation
	series  map[string]bool
	skipped int
}

func newOwned() *owned {
	return &owned{res: map[string]bool{}, rel: map[string]graph.Relation{}, series: map[string]bool{}}
}

type healthMark struct {
	status string
	since  time.Time
}

// seriesDoc is one metrics series, exactly the simulator's Series shape.
type seriesDoc struct {
	Resource string            `json:"resource"`
	Metric   string            `json:"metric"`
	Unit     string            `json:"unit"`
	Min      float64           `json:"min"`
	Max      float64           `json:"max"`
	Band     [2]float64        `json:"band"`
	Warn     float64           `json:"warn,omitempty"`
	Crit     float64           `json:"crit,omitempty"`
	Value    integration.Num   `json:"value"`
	AsOf     time.Time         `json:"as_of,omitzero"`
	Stale    bool              `json:"stale,omitempty"`
	History  []integration.Num `json:"history"`

	axis time.Time // time of the newest history point
	// The valid samples of the newest slot, whose average the point
	// becomes when the history scrolls past it.
	slotSum float64
	slotN   int
}

// SelfID is an instance's own resource ID.
func SelfID(instanceID string) string { return "integration:" + instanceID }

// NewEmitter returns the emitter for one instance writing into t. now is the
// clock (nil: time.Now).
func NewEmitter(t *topics.Set, instanceID string, now func() time.Time) *Emitter {
	if now == nil {
		now = time.Now
	}
	return &Emitter{
		t: t, id: instanceID, self: SelfID(instanceID), now: now,
		report:    func(string, []integration.Warning, bool, bool) {},
		scopes:    map[string]*owned{},
		marks:     map[string]healthMark{},
		series:    map[string]*seriesDoc{},
		incidents: map[string]integration.Incident{},
	}
}

var _ integration.Emitter = (*Emitter)(nil)

// Self implements integration.Emitter.
func (e *Emitter) Self() string { return e.self }

// SetVersion implements integration.Emitter.
func (e *Emitter) SetVersion(v string) { e.report(v, nil, true, false) }

// SetWarnings implements integration.Emitter.
func (e *Emitter) SetWarnings(ws []integration.Warning) {
	e.report("", slices.Clone(ws), false, true)
}

func (e *Emitter) clock() time.Time { return e.now().UTC().Truncate(time.Second) }

// Replace implements integration.Emitter.
func (e *Emitter) Replace(scope string, f integration.Facts) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.clock()
	prev := e.scopes[scope]
	if prev == nil {
		prev = newOwned()
	}
	next := newOwned()

	resources := make([]graph.Resource, 0, len(f.Resources))
	marks := map[string]healthMark{}
	for _, r := range f.Resources {
		switch {
		case r.ID == e.self:
			return fmt.Errorf("runtime: %s is the instance's own resource; the framework maintains it", r.ID)
		case next.res[r.ID]:
			return fmt.Errorf("runtime: resource %s listed twice", r.ID)
		case e.ownedElsewhere(scope, r.ID):
			return fmt.Errorf("runtime: resource %s is already published under another scope", r.ID)
		}
		next.res[r.ID] = true
		gr, mark, err := e.toGraph(r, e.self, now)
		if err != nil {
			return err
		}
		resources = append(resources, gr)
		marks[r.ID] = mark
	}

	// Validate the metrics before touching anything: a batch applies whole
	// or not at all.
	for _, m := range f.Metrics {
		if m.Resource == "" || m.Key == "" || strings.ContainsAny(m.Key, " #") {
			return fmt.Errorf("runtime: metric %q of %q needs a resource and a key without spaces or #", m.Key, m.Resource)
		}
		key := topics.SeriesKey(m.Resource, m.Key)
		if next.series[key] {
			return fmt.Errorf("runtime: series %s listed twice", key)
		}
		if e.seriesElsewhere(scope, key) {
			return fmt.Errorf("runtime: series %s is already published under another scope", key)
		}
		next.series[key] = true
	}

	if _, err := e.t.Graph.Update(func(tx *graph.Tx) error {
		for id := range prev.res {
			if next.res[id] {
				continue
			}
			if cur, ok := tx.Get(graph.RID(id)); ok && cur.Source == e.self {
				tx.Remove(graph.RID(id))
			}
		}
		// Drop every relation the scope had; Relate below re-stages the ones
		// listed again, and a later stage of the same key wins, so only real
		// changes reach the graph.
		for _, rel := range prev.rel {
			tx.Unrelate(rel.From, rel.Type, rel.To)
		}
		for _, r := range resources {
			if err := tx.Upsert(r); err != nil {
				return err
			}
		}
		next.skipped = 0
		for _, rel := range f.Relations {
			gr := graph.Relation{From: graph.RID(rel.From), Type: graph.RelType(rel.Type), To: graph.RID(rel.To), Source: e.self}
			_, okFrom := tx.Get(gr.From)
			_, okTo := tx.Get(gr.To)
			if !okFrom || !okTo {
				next.skipped++
				continue
			}
			if err := tx.Relate(gr); err != nil {
				return err
			}
			next.rel[gr.Key()] = gr
		}
		return nil
	}); err != nil {
		return err
	}
	for _, id := range slices.Sorted(maps.Keys(marks)) {
		e.recordState(id, marks[id])
	}
	maps.Copy(e.marks, marks)
	for id := range prev.res {
		if !next.res[id] && !e.ownedElsewhere(scope, id) {
			delete(e.marks, id)
			if e.history != nil {
				e.history.Gone(id, now)
			}
		}
	}

	if _, err := e.t.Metrics.Update(func(tx *livedoc.Tx) error {
		for _, m := range f.Metrics {
			key := topics.SeriesKey(m.Resource, m.Key)
			tx.Put(topics.Series, key, e.sample(key, m, now))
		}
		for key := range prev.series {
			if !next.series[key] && !e.seriesElsewhere(scope, key) {
				tx.Delete(topics.Series, key)
			}
		}
		return nil
	}); err != nil {
		return err
	}
	for key := range prev.series {
		if !next.series[key] && !e.seriesElsewhere(scope, key) {
			delete(e.series, key)
		}
	}
	e.scopes[scope] = next
	return nil
}

func (e *Emitter) ownedElsewhere(scope, id string) bool {
	for s, o := range e.scopes {
		if s != scope && o.res[id] {
			return true
		}
	}
	return false
}

func (e *Emitter) seriesElsewhere(scope, key string) bool {
	for s, o := range e.scopes {
		if s != scope && o.series[key] {
			return true
		}
	}
	return false
}

// toGraph maps a resource onto the graph the way demo mode does: fields the
// graph doesn't model go into attrs, and attrs are normalised to plain JSON.
func (e *Emitter) toGraph(r integration.Resource, source string, now time.Time) (graph.Resource, healthMark, error) {
	if r.ID == "" || r.Kind == "" {
		return graph.Resource{}, healthMark{}, fmt.Errorf("runtime: resource %q needs an id and a kind", r.ID)
	}
	attrs := map[string]any{}
	if len(r.Attrs) > 0 {
		b, err := json.Marshal(r.Attrs)
		if err != nil {
			return graph.Resource{}, healthMark{}, fmt.Errorf("runtime: %s attrs: %w", r.ID, err)
		}
		if err := json.Unmarshal(b, &attrs); err != nil {
			return graph.Resource{}, healthMark{}, fmt.Errorf("runtime: %s attrs: %w", r.ID, err)
		}
	}
	status := r.Health.Status
	if status == "" {
		status = integration.HealthUnknown
	}
	mark := healthMark{status: status, since: r.Health.Since.UTC().Truncate(time.Second)}
	if r.Health.Since.IsZero() {
		mark.since = now
		if prev, ok := e.marks[r.ID]; ok && prev.status == status {
			mark.since = prev.since
		} else if !ok && e.history != nil {
			mark.since = e.history.Since(r.ID, status, now)
		}
	}
	if r.Desired != "" {
		attrs[integration.AttrDesired] = r.Desired
	}
	if r.Health.Reason != "" {
		attrs[integration.AttrHealthReason] = r.Health.Reason
	}
	attrs[integration.AttrHealthSince] = mark.since.Format(time.RFC3339Nano)
	if len(r.Links) > 0 {
		links := make([]any, len(r.Links))
		for i, l := range r.Links {
			links[i] = map[string]any{"title": l.Title, "url": l.URL}
		}
		attrs[integration.AttrLinks] = links
	}
	if len(r.Caps) > 0 {
		caps := make([]any, len(r.Caps))
		for i, c := range r.Caps {
			caps[i] = c
		}
		attrs[integration.AttrCaps] = caps
	}
	return graph.Resource{
		ID: graph.RID(r.ID), Kind: graph.Kind(r.Kind), Name: r.Name, Source: source,
		Labels: r.Labels, Attrs: attrs, State: r.State, Health: graph.Health(status),
	}, mark, nil
}

// sample folds one sample into its series. The history keeps the demo's
// grid: when a sample lands in a new 30-minute slot the history scrolls and
// the sample becomes the newest point, so sparklines move every 30 minutes
// rather than under the reader on every poll. The slot scrolled past gets
// the average of its samples. A new series takes its past from history.
func (e *Emitter) sample(key string, m integration.Metric, now time.Time) *seriesDoc {
	v := integration.Num(m.Value)
	slot := now.Truncate(HistoryStep)
	s := e.series[key]
	if s == nil {
		s = &seriesDoc{History: make([]integration.Num, HistoryPoints)}
		for i := range s.History {
			s.History[i] = integration.Num(integration.Unknown())
		}
		if e.history != nil {
			past := e.history.Sparkline(key, slot, HistoryPoints, HistoryStep)
			for i := 0; i < len(past) && i < HistoryPoints-1; i++ {
				if !math.IsNaN(past[i]) {
					s.History[i] = integration.Num(past[i])
				}
			}
		}
		e.series[key] = s
	}
	if e.history != nil && v.Valid() {
		e.history.Record(key, e.id, m.Unit, now, m.Value)
	}
	s.Resource, s.Metric, s.Unit = m.Resource, m.Key, m.Unit
	s.Min, s.Max, s.Band, s.Warn, s.Crit = m.Min, m.Max, m.Band, m.Warn, m.Crit
	s.Value, s.AsOf, s.Stale = v, now, false
	switch {
	case s.axis.IsZero():
		s.axis = slot
		s.History[HistoryPoints-1] = v
	case slot.After(s.axis):
		h := s.History
		if s.slotN > 0 {
			h[len(h)-1] = integration.Num(s.slotSum / float64(s.slotN))
		}
		s.slotSum, s.slotN = 0, 0
		shift := int(slot.Sub(s.axis) / HistoryStep)
		n := min(shift, len(h))
		copy(h, h[n:])
		for i := len(h) - n; i < len(h); i++ {
			h[i] = integration.Num(integration.Unknown())
		}
		h[len(h)-1] = v
		s.axis = slot
	}
	if v.Valid() && slot.Equal(s.axis) {
		s.slotSum += float64(v)
		s.slotN++
	}
	return s
}

// MarkStale flags every series of the instance as stale: the values are
// last-known, the source couldn't be read. The next successful sample clears it.
func (e *Emitter) MarkStale() {
	e.mu.Lock()
	defer e.mu.Unlock()
	var stale []string
	for key, s := range e.series {
		if s.Value.Valid() && !s.Stale {
			s.Stale = true
			stale = append(stale, key)
		}
	}
	if len(stale) == 0 {
		return
	}
	_, _ = e.t.Metrics.Update(func(tx *livedoc.Tx) error {
		for _, key := range stale {
			tx.Put(topics.Series, key, e.series[key])
		}
		return nil
	})
}

// PublishSelf publishes the instance's own resource. The coordinator calls
// it on every status change; contract tests call it so relations to Self
// have an endpoint.
func (e *Emitter) PublishSelf(r integration.Resource) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	r.ID = e.self
	gr, mark, err := e.toGraph(r, "hub", e.clock())
	if err != nil {
		return err
	}
	if _, err := e.t.Graph.Update(func(tx *graph.Tx) error { return tx.Upsert(gr) }); err != nil {
		return err
	}
	e.recordState(e.self, mark)
	e.marks[e.self] = mark
	return nil
}

// recordState writes a resource's status to history when it's new or
// changed. Call it before the mark replaces the old one.
func (e *Emitter) recordState(id string, m healthMark) {
	if e.history == nil {
		return
	}
	if old, ok := e.marks[id]; !ok || old.status != m.status {
		e.history.State(id, m.status, m.since)
	}
}

// Clear removes everything the instance published: its resources (its own
// included), relations, series and incidents. Used when an instance is
// deleted or disabled.
func (e *Emitter) Clear() {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, _ = e.t.Graph.Update(func(tx *graph.Tx) error {
		for _, o := range e.scopes {
			for id := range o.res {
				if cur, ok := tx.Get(graph.RID(id)); ok && cur.Source == e.self {
					tx.Remove(graph.RID(id))
				}
			}
		}
		tx.Remove(graph.RID(e.self))
		return nil
	})
	_, _ = e.t.Metrics.Update(func(tx *livedoc.Tx) error {
		for key := range e.series {
			tx.Delete(topics.Series, key)
		}
		return nil
	})
	_, _ = e.t.Inbox.Update(func(tx *livedoc.Tx) error {
		for id := range e.incidents {
			tx.Delete(topics.Incidents, id)
		}
		return nil
	})
	if e.history != nil {
		now := e.clock()
		for id := range e.marks {
			e.history.Gone(id, now)
		}
	}
	e.scopes = map[string]*owned{}
	e.marks = map[string]healthMark{}
	e.series = map[string]*seriesDoc{}
	e.incidents = map[string]integration.Incident{}
}

func (e *Emitter) incidentID(local string) string { return e.id + "." + local }

// SetPosture makes the emitter publish incidents at the severity the
// security posture gives them (security.Posture.Severity), reading posture
// each time. The Manager sets it on every instance; without it the inbox
// gets the declared severity.
func (e *Emitter) SetPosture(posture func() security.Posture) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.posture = posture
}

// published is an incident as the inbox shows it: a security finding's
// declared severity replaced by the posture's. The caller holds e.mu.
func (e *Emitter) published(in integration.Incident) integration.Incident {
	if e.posture != nil {
		in.Severity = e.posture().Severity(in.Category, in.Severity)
	}
	annotate := e.annotate
	if annotate == nil {
		annotate = func(a *integration.Action) { a.PlanReady, a.Unavailable, a.Recipe = nil, "", "" }
	}
	annotate(&in.Suggested)
	if len(in.Alternatives) > 0 {
		in.Alternatives = slices.Clone(in.Alternatives)
		for i := range in.Alternatives {
			annotate(&in.Alternatives[i])
		}
	}
	return in
}

// SetAnnotate sets how incident actions get their availability.
func (e *Emitter) SetAnnotate(fn func(*integration.Action)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.annotate = fn
}

// sameAvailability reports whether two actions carry the same availability.
func sameAvailability(a, b integration.Action) bool {
	return reflect.DeepEqual(a.PlanReady, b.PlanReady) && a.Unavailable == b.Unavailable && a.Recipe == b.Recipe
}

// withAvailability copies want's availability onto the actions of cur that
// are the same action, and reports whether anything changed.
func withAvailability(cur *integration.Incident, want integration.Incident) bool {
	changed := false
	set := func(c *integration.Action, w integration.Action) {
		if c.ID == w.ID && c.Target == w.Target && !sameAvailability(*c, w) {
			c.PlanReady, c.Unavailable, c.Recipe = w.PlanReady, w.Unavailable, w.Recipe
			changed = true
		}
	}
	set(&cur.Suggested, want.Suggested)
	for i := range cur.Alternatives {
		if i < len(want.Alternatives) {
			set(&cur.Alternatives[i], want.Alternatives[i])
		}
	}
	return changed
}

// Reapply re-publishes the instance's incidents where what the hub adds has
// changed: security findings, open and resolved, at the severity the
// posture gives them now, and actions' availability (plan_ready,
// unavailable). The Manager calls it when the posture changes and when
// instances' actions do, so everything moves at once rather than at the
// next sync. Each changes in place in the inbox, keeping what other
// writers added (the correlator's causes), and nothing else about it
// changes.
func (e *Emitter) Reapply() {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, _ = e.t.Inbox.Update(func(tx *livedoc.Tx) error {
		for id, in := range e.incidents {
			want := e.published(in)
			raw := tx.Item(topics.Incidents, id)
			if raw == nil {
				continue
			}
			var cur integration.Incident
			if err := json.Unmarshal(raw, &cur); err != nil {
				continue
			}
			changed := withAvailability(&cur, want)
			if in.Category == integration.CategorySecurity && cur.Severity != want.Severity {
				cur.Severity, changed = want.Severity, true
			}
			if changed {
				tx.Put(topics.Incidents, id, cur)
			}
		}
		return nil
	})
}

// Raise implements integration.Emitter.
func (e *Emitter) Raise(in integration.Incident) error {
	switch {
	case in.ID == "":
		return errors.New("runtime: an incident needs an id")
	case in.Class != integration.ClassProblem && in.Class != integration.ClassRepair:
		return fmt.Errorf("runtime: incident %s: class %q isn't problem or repair", in.ID, in.Class)
	case in.Severity != integration.SeverityInfo && in.Severity != integration.SeverityAttention && in.Severity != integration.SeverityDown:
		return fmt.Errorf("runtime: incident %s: severity %q isn't info, attention or down", in.ID, in.Severity)
	case in.Category != "" && in.Category != integration.CategorySecurity:
		return fmt.Errorf("runtime: incident %s: category %q isn't security", in.ID, in.Category)
	case in.Title == "":
		return fmt.Errorf("runtime: incident %s needs a title", in.ID)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.clock()
	id := e.incidentID(in.ID)
	prev, had := e.incidents[id]
	in.ID = id
	in.Status = integration.StatusOpen
	in.ResolvedAt, in.ResolvedBy, in.Resolution = time.Time{}, "", ""
	if in.Scenario == "" {
		in.Scenario = in.Class
	}
	if in.Phase == "" {
		in.Phase = PhaseOpen
	}
	if in.Target == "" {
		in.Target = e.self
	}
	if in.Causes == nil {
		in.Causes = []integration.Cause{}
	}
	if in.BlastRadius == nil {
		in.BlastRadius = []integration.Impact{}
	}
	if in.Suggested.ID == "" {
		in.Suggested = integration.Action{ID: "integration.settings", Label: "Open the integration's settings", Tier: "T0", Target: e.self}
	}
	switch {
	case had && prev.Status == integration.StatusOpen:
		in.OpenedAt = prev.OpenedAt
	case in.OpenedAt.IsZero():
		in.OpenedAt = now
	default:
		in.OpenedAt = in.OpenedAt.UTC().Truncate(time.Second)
	}
	in.UpdatedAt = now
	if had {
		same := in
		same.UpdatedAt = prev.UpdatedAt
		if reflect.DeepEqual(same, prev) {
			return nil // nothing changed: keep updated_at, publish nothing
		}
	}
	e.incidents[id] = in
	_, err := e.t.Inbox.Update(func(tx *livedoc.Tx) error {
		tx.Put(topics.Incidents, id, e.published(in))
		return nil
	})
	return err
}

// Resolve implements integration.Emitter.
func (e *Emitter) Resolve(local, resolution string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	id := e.incidentID(local)
	in, ok := e.incidents[id]
	if !ok || in.Status != integration.StatusOpen {
		return nil
	}
	now := e.clock()
	in.Status, in.Phase = integration.StatusResolved, PhaseResolved
	in.ResolvedAt, in.UpdatedAt = now, now
	in.ResolvedBy = "auto"
	in.Resolution = resolution
	e.incidents[id] = in
	_, err := e.t.Inbox.Update(func(tx *livedoc.Tx) error {
		tx.Put(topics.Incidents, id, e.published(in))
		return nil
	})
	return err
}

// open reports whether the instance's incident is open.
func (e *Emitter) open(local string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	in, ok := e.incidents[e.incidentID(local)]
	return ok && in.Status == integration.StatusOpen
}

// Incident phases the framework uses. The web app groups the inbox by them:
// open, being fixed, resolved. Scenario says what kind of incident it is.
const (
	PhaseOpen     = "open"
	PhaseFixing   = "fixing"   // the user changed the instance; waiting for the next try
	PhaseResolved = "resolved" // set by Resolve
)

// setPhase changes an open incident's phase.
func (e *Emitter) setPhase(local, phase string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	id := e.incidentID(local)
	in, ok := e.incidents[id]
	if !ok || in.Status != integration.StatusOpen || in.Phase == phase {
		return
	}
	in.Phase, in.UpdatedAt = phase, e.clock()
	e.incidents[id] = in
	_, _ = e.t.Inbox.Update(func(tx *livedoc.Tx) error {
		tx.Put(topics.Incidents, id, e.published(in))
		return nil
	})
}

// openedAt returns when the instance's open incident opened.
func (e *Emitter) openedAt(local string) (time.Time, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	in, ok := e.incidents[e.incidentID(local)]
	if !ok || in.Status != integration.StatusOpen {
		return time.Time{}, false
	}
	return in.OpenedAt, true
}

// Prune deletes incidents resolved before cutoff.
func (e *Emitter) Prune(cutoff time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	var drop []string
	for id, in := range e.incidents {
		if in.Status == integration.StatusResolved && in.ResolvedAt.Before(cutoff) {
			drop = append(drop, id)
		}
	}
	if len(drop) == 0 {
		return
	}
	_, _ = e.t.Inbox.Update(func(tx *livedoc.Tx) error {
		for _, id := range drop {
			tx.Delete(topics.Incidents, id)
		}
		return nil
	})
	for _, id := range drop {
		delete(e.incidents, id)
	}
}

// owns lists up to n resources the instance published, sorted, for a
// repair's blast radius: what goes stale while it can't be read.
func (e *Emitter) owns(n int) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var ids []string
	for _, o := range e.scopes {
		for id := range o.res {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids[:min(n, len(ids))]
}

// FactStats counts what an instance has published.
type FactStats struct {
	Resources        int `json:"resources"`
	Relations        int `json:"relations"`
	Series           int `json:"series"`
	OpenIncidents    int `json:"open_incidents"`
	SkippedRelations int `json:"skipped_relations"` // endpoints that didn't exist at the last publish
}

// Stats counts what the instance has published.
func (e *Emitter) Stats() FactStats {
	e.mu.Lock()
	defer e.mu.Unlock()
	var s FactStats
	for _, o := range e.scopes {
		s.Resources += len(o.res)
		s.Relations += len(o.rel)
		s.SkippedRelations += o.skipped
	}
	s.Series = len(e.series)
	for _, in := range e.incidents {
		if in.Status == integration.StatusOpen {
			s.OpenIncidents++
		}
	}
	return s
}
