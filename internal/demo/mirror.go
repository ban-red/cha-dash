package demo

import (
	"slices"
	"time"

	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/livedoc"
	"dash.cha.sh/internal/security"
	"dash.cha.sh/internal/topics"
	"dash.cha.sh/sim"
)

// mirror projects the simulator onto the hub's topics. The graph and the
// inbox follow the event stream one to one. Metrics keep a little state: a
// metrics event carries bare samples, while the topic carries whole series
// with a sparkline history.
//
// The history stays on the grid of the snapshot it was loaded from (every
// 30 minutes, ending at the snapshot's last sample) and gains a point each
// time a sample lands on that grid. So sparklines scroll every 30 minutes
// instead of shifting under the reader every 30 seconds, and whenever the
// grid lines up with the clock the series equal a fresh simulator snapshot.
type mirror struct {
	t      *topics.Set
	series map[string]*sim.Series // by topics.SeriesKey
	axis   time.Time              // time of the newest history point
	step   time.Duration          // history spacing

	// posture is the hub's security posture: security findings reach the
	// inbox at the severity it gives them. findings are those incidents as
	// the simulator wrote them (declared severity), by ID, so a posture
	// change re-publishes them at once.
	posture  security.Posture
	findings map[string]sim.Incident
}

// publish is an incident as the inbox shows it, remembering a security
// finding's declared severity.
func (m *mirror) publish(in sim.Incident) sim.Incident {
	if in.Category == "" {
		return in
	}
	if m.findings == nil {
		m.findings = map[string]sim.Incident{}
	}
	m.findings[in.ID] = in
	in.Severity = sim.Severity(m.posture.Severity(in.Category, string(in.Severity)))
	return in
}

// setPosture changes the posture and re-publishes every security finding
// under it.
func (m *mirror) setPosture(p security.Posture) error {
	m.posture = p
	if len(m.findings) == 0 {
		return nil
	}
	_, err := m.t.Inbox.Update(func(tx *livedoc.Tx) error {
		for _, in := range m.findings {
			tx.Put(topics.Incidents, in.ID, m.publish(in))
		}
		return nil
	})
	return err
}

// load replaces every topic's contents with st.
func (m *mirror) load(st sim.State) error {
	if err := LoadGraph(m.t.Graph, st); err != nil {
		return err
	}
	m.series = make(map[string]*sim.Series, len(st.Series))
	for _, se := range st.Series {
		se.History = slices.Clone(se.History) // shifted in place later
		m.series[topics.SeriesKey(se.Resource, se.Metric)] = &se
	}
	m.axis = st.History.End
	m.step = time.Duration(st.History.StepSeconds) * time.Second
	if _, err := m.t.Metrics.Update(func(tx *livedoc.Tx) error {
		tx.Clear(topics.Series)
		for k, se := range m.series {
			tx.Put(topics.Series, k, se)
		}
		return nil
	}); err != nil {
		return err
	}
	_, err := m.t.Inbox.Update(func(tx *livedoc.Tx) error {
		tx.Clear(topics.Incidents)
		clear(m.findings)
		for _, in := range st.Incidents {
			tx.Put(topics.Incidents, in.ID, m.publish(in))
		}
		topics.SetNow(tx, st.Now)
		return nil
	})
	return err
}

// apply applies a batch of events, in order, as of the simulator time now.
// Each topic changes in one batch. On error the topics may be partly
// updated; the caller reloads from a fresh snapshot.
func (m *mirror) apply(events []sim.Event, now time.Time) error {
	changed := map[string]bool{}
	var incidents []*sim.Incident
	var graphEvents []sim.Event
	for _, ev := range events {
		switch ev.Type {
		case sim.EventMetrics:
			m.sample(ev, changed)
		case sim.EventIncidentOpen, sim.EventIncidentUpdate, sim.EventIncidentResolve:
			incidents = append(incidents, ev.Incident)
		default:
			graphEvents = append(graphEvents, ev)
		}
	}
	if len(graphEvents) > 0 {
		if _, err := m.t.Graph.Update(func(tx *graph.Tx) error {
			for _, ev := range graphEvents {
				if err := ApplyGraph(tx, ev); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}
	if len(changed) > 0 {
		if _, err := m.t.Metrics.Update(func(tx *livedoc.Tx) error {
			for k := range changed {
				tx.Put(topics.Series, k, m.series[k])
			}
			return nil
		}); err != nil {
			return err
		}
	}
	_, err := m.t.Inbox.Update(func(tx *livedoc.Tx) error {
		for _, in := range incidents { // later events for an incident win
			tx.Put(topics.Incidents, in.ID, m.publish(*in))
		}
		topics.SetNow(tx, now)
		return nil
	})
	return err
}

// sample folds one metrics event into the series, matching what a
// simulator snapshot would say at that moment: a series without a sample
// keeps its last-known value, marked stale (an unknown one stays unknown),
// a sample can itself be unknown (null), and unknown history points are
// null.
func (m *mirror) sample(ev sim.Event, changed map[string]bool) {
	t := ev.At
	vals := make(map[string]sim.Num, len(ev.Samples))
	for _, s := range ev.Samples {
		vals[topics.SeriesKey(s.Resource, s.Metric)] = s.Value
	}
	shift := 0
	for m.step > 0 && !m.axis.Add(m.step).After(t) {
		m.axis = m.axis.Add(m.step)
		shift++
	}
	for k, se := range m.series {
		v, ok := vals[k]
		switch {
		case ok:
			se.Value, se.AsOf, se.Stale = v, t, false
			changed[k] = true
		case se.Value.Valid() && !se.Stale:
			se.Stale = true
			changed[k] = true
		}
		if shift > 0 {
			h := se.History
			n := min(shift, len(h))
			copy(h, h[n:])
			for i := len(h) - n; i < len(h); i++ {
				h[i] = sim.Unknown()
			}
			if ok && m.axis.Equal(t) && len(h) > 0 {
				h[len(h)-1] = v
			}
			changed[k] = true
		}
	}
}
