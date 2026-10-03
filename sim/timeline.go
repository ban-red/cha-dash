package sim

import (
	"bytes"
	"time"
)

// Timeline is a compact recording of a stretch of simulated time, for design
// prototypes: metric values on a coarse shared axis (columnar), plus every
// non-metric event at its exact time. Replaying Events on top of the
// snapshot taken at Start gives the state at End.
type Timeline struct {
	Seed        int64            `json:"seed"`
	Start       time.Time        `json:"start"`
	End         time.Time        `json:"end"`
	StepSeconds int              `json:"step_s"`
	Frames      int              `json:"frames"` // values per series: Start, Start+step, … End
	Series      []TimelineSeries `json:"series"`
	Incidents   []Incident       `json:"incidents"` // every incident as of End
	Events      []Event          `json:"events"`    // resource, relation and incident events; no metrics
}

// TimelineSeries is one metric's values on the timeline axis.
type TimelineSeries struct {
	Resource string `json:"resource"`
	Metric   string `json:"metric"`
	Unit     string `json:"unit"`
	Values   []Num  `json:"values"`
}

// Record advances the simulation by span and returns a Timeline sampled
// every step (frames at now, now+step, …, now+span). Metric values are exact
// point samples; events are those Advance would return, minus metrics.
func (s *Sim) Record(span, step time.Duration) Timeline {
	s.mu.Lock()
	defer s.mu.Unlock()
	start := s.now
	tl := Timeline{Seed: s.seed, Start: start, End: start.Add(span), StepSeconds: int(step / time.Second), Frames: int(span/step) + 1}
	worlds := make([]*world, tl.Frames)
	for i := range worlds {
		worlds[i] = s.world(start.Add(time.Duration(i)*step), false)
	}
	for _, m := range s.top.specs {
		ts := TimelineSeries{Resource: m.res, Metric: m.key, Unit: m.unit, Values: make([]Num, tl.Frames)}
		for i, w := range worlds {
			ts.Values[i] = s.value(m, w.t, w)
		}
		tl.Series = append(tl.Series, ts)
	}
	tl.Events = s.advance(tl.End, false)
	if tl.Events == nil {
		tl.Events = []Event{}
	}
	tl.Incidents = []Incident{}
	for _, inc := range s.cur.incidents {
		tl.Incidents = append(tl.Incidents, *exportIncident(inc))
	}
	return tl
}

// ── stable JSON ──────────────────────────────────────────────────────────

// EncodeState writes a snapshot as stable, diff-friendly JSON: object keys
// sorted, list items in their sorted order, one list item per line.
func EncodeState(st State) []byte {
	return encodeLines([]field{
		{"seed", st.Seed, false},
		{"start", st.Start, false},
		{"now", st.Now, false},
		{"seq", st.Seq, false},
		{"history", st.History, false},
		{"resources", items(st.Resources), true},
		{"relations", items(st.Relations), true},
		{"series", items(st.Series), true},
		{"incidents", items(st.Incidents), true},
	})
}

// EncodeTimeline writes a timeline the same way as EncodeState.
func EncodeTimeline(tl Timeline) []byte {
	return encodeLines([]field{
		{"seed", tl.Seed, false},
		{"start", tl.Start, false},
		{"end", tl.End, false},
		{"step_s", tl.StepSeconds, false},
		{"frames", tl.Frames, false},
		{"series", items(tl.Series), true},
		{"incidents", items(tl.Incidents), true},
		{"events", items(tl.Events), true},
	})
}

type field struct {
	name  string
	value any
	list  bool // value is []any, one item per line
}

func items[T any](xs []T) []any {
	out := make([]any, len(xs))
	for i, x := range xs {
		out[i] = x
	}
	return out
}

func encodeLines(fields []field) []byte {
	var b bytes.Buffer
	b.WriteString("{\n")
	for i, f := range fields {
		b.WriteString(" ")
		b.Write(encode(f.name))
		b.WriteString(": ")
		if !f.list {
			b.Write(encode(f.value))
		} else if xs := f.value.([]any); len(xs) == 0 {
			b.WriteString("[]")
		} else {
			b.WriteString("[\n")
			for j, x := range xs {
				b.WriteString("  ")
				b.Write(encode(x))
				if j < len(xs)-1 {
					b.WriteString(",")
				}
				b.WriteString("\n")
			}
			b.WriteString(" ]")
		}
		if i < len(fields)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	b.WriteString("}\n")
	return b.Bytes()
}
