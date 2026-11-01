package incidents

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"time"

	"dash.cha.sh/internal/livedoc"
)

// What the recorder keeps: problems and repairs, the incidents proper. The
// inbox's other classes (the correlator's suggestions, plans waiting for
// approval, nodes waiting to be approved) are requests, not incidents.
var recordedClasses = map[string]bool{"problem": true, "repair": true}

// pruneEvery is how often the recorder applies retention, on the same
// hourly schedule as history's and notifications' own.
const pruneEvery = time.Hour

// RecorderOptions configure a Recorder.
type RecorderOptions struct {
	Store *Store
	// Inbox is the inbox topic's document, the one place every incident is
	// published (the runtime's emitter, the correlator, the Demo Home).
	Inbox *livedoc.Doc
	// Expect says whether an incident opened inside a maintenance window.
	// Nil expects nothing.
	Expect *Expectations
	Log    *slog.Logger
	Now    func() time.Time
}

// Recorder watches the inbox and writes each incident's lifecycle to the
// store: opened (and reopened), a change of severity or phase, each new
// cause, expected, resolved. It reads the inbox the way notifications do
// (a snapshot whenever its version moves), so it sees every source without
// any of them knowing about it.
//
// Nothing records "acknowledged" yet: the hub has no acknowledgement. The
// kind is in the table for when it does.
type Recorder struct {
	store  *Store
	inbox  *livedoc.Doc
	expect *Expectations
	log    *slog.Logger
	now    func() time.Time

	primed      bool
	lastVersion uint64
	lastPrune   time.Time
	seen        map[string]*known
}

// known is what the recorder last wrote about an incident.
type known struct {
	open     bool
	severity string
	phase    string
	causes   map[string]bool // cause keys since it last opened
}

// NewRecorder returns a recorder; Run starts it.
func NewRecorder(o RecorderOptions) *Recorder {
	r := &Recorder{store: o.Store, inbox: o.Inbox, expect: o.Expect, log: o.Log, now: o.Now, seen: map[string]*known{}}
	if r.log == nil {
		r.log = slog.New(slog.DiscardHandler)
	}
	if r.now == nil {
		r.now = time.Now
	}
	return r
}

// Run records until ctx ends.
func (r *Recorder) Run(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		r.Step(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Step is one turn: read the inbox if it changed, write what happened,
// prune once an hour.
func (r *Recorder) Step(ctx context.Context) {
	now := r.now()
	if !r.primed {
		if err := r.prime(ctx, now); err != nil {
			r.log.Warn("incidents: reading the timeline failed; trying again", "err", err)
			return
		}
		r.primed = true
	}
	if r.inbox != nil {
		if v := r.inbox.Version(); v != r.lastVersion {
			_, doc := r.inbox.Snapshot()
			evs := r.observe(now, parse(doc["incidents"]))
			if err := r.store.Append(ctx, evs...); err != nil {
				r.log.Error("incidents: recording events failed", "err", err)
			} else {
				r.lastVersion = v
			}
		}
	}
	if now.Sub(r.lastPrune) >= pruneEvery {
		r.lastPrune = now
		if n, err := r.store.Prune(ctx, now); err != nil {
			r.log.Warn("incidents: pruning failed", "err", err)
		} else if n > 0 {
			r.log.Debug("incidents: pruned old events", "deleted", n)
		}
	}
}

// prime reads what the store already knows, so a restart doesn't record
// every open incident as opened again.
func (r *Recorder) prime(ctx context.Context, now time.Time) error {
	evs, err := r.store.Events(ctx, Query{From: now.Add(-r.store.Keep())})
	if err != nil {
		return err
	}
	seen := map[string]*known{}
	for _, e := range evs {
		k := seen[e.IncidentID]
		if k == nil {
			k = &known{causes: map[string]bool{}}
			seen[e.IncidentID] = k
		}
		d := DataOf(e)
		switch e.Kind {
		case Opened:
			k.open, k.causes = true, map[string]bool{}
		case Resolved:
			k.open = false
		case Caused:
			k.causes[causeKey(d.Resource, d.Text)] = true
		}
		if d.Severity != "" {
			k.severity = d.Severity
		}
		if d.Phase != "" {
			k.phase = d.Phase
		}
	}
	r.seen = seen
	return nil
}

// observed is the part of an inbox incident the recorder reads.
type observed struct {
	ID         string `json:"id"`
	Class      string `json:"class"`
	Category   string `json:"category"`
	Scenario   string `json:"scenario"`
	Severity   string `json:"severity"`
	Status     string `json:"status"`
	Phase      string `json:"phase"`
	Title      string `json:"title"`
	Target     string `json:"target"`
	OpenedAt   string `json:"opened_at"`
	UpdatedAt  string `json:"updated_at"`
	ResolvedAt string `json:"resolved_at"`
	ResolvedBy string `json:"resolved_by"`
	Causes     []struct {
		Text     string `json:"text"`
		Resource string `json:"resource"`
	} `json:"causes"`
}

// parse reads the inbox collection from a livedoc snapshot.
func parse(coll any) map[string]observed {
	out := map[string]observed{}
	m, ok := coll.(map[string]json.RawMessage)
	if !ok {
		return out
	}
	for id, raw := range m {
		var in observed
		if json.Unmarshal(raw, &in) != nil {
			continue
		}
		if in.ID == "" {
			in.ID = id
		}
		out[id] = in
	}
	return out
}

func causeKey(resource, text string) string {
	if resource != "" {
		return "r:" + resource
	}
	return "t:" + text
}

// timeOr is an incident's timestamp, or now when it has none or it's in
// the future.
func timeOr(s string, now time.Time) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil || t.IsZero() || t.After(now) {
		return now
	}
	return t.UTC()
}

// observe compares a snapshot with what was last recorded and returns the
// events that happened, in a stable order. Incidents that left the inbox
// (pruned after 24 hours, or their instance deleted) are forgotten in
// memory, not in the store.
func (r *Recorder) observe(now time.Time, incs map[string]observed) []Event {
	var out []Event
	for id, k := range r.seen {
		if _, ok := incs[id]; !ok && !k.open {
			delete(r.seen, id) // resolved and gone from the inbox: the store remembers
		}
	}
	ids := make([]string, 0, len(incs))
	for id, in := range incs {
		if recordedClasses[in.Class] {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	for _, id := range ids {
		in := incs[id]
		head := Data{Target: in.Target, Class: in.Class, Category: in.Category, Scenario: in.Scenario, Title: clip(in.Title), Severity: in.Severity, Phase: in.Phase}
		ev := func(kind EventKind, at time.Time, d Data) {
			raw, _ := json.Marshal(d)
			out = append(out, Event{ID: newEventID(), IncidentID: id, At: at, Kind: kind, Data: raw})
		}
		k := r.seen[id]
		open := in.Status != "resolved"
		switch {
		case k == nil || (!k.open && open):
			// New to the timeline, or reopened. A resolved incident first
			// seen (on the first start with this version) is backfilled
			// from its own times.
			opened := timeOr(in.OpenedAt, now)
			if k != nil {
				opened = timeOr(in.UpdatedAt, now) // reopened: its opened_at may be the first opening
			}
			k = &known{causes: map[string]bool{}}
			r.seen[id] = k
			ev(Opened, opened, head)
			if open {
				if w, ok := r.expect.Expected(in.Target, opened); ok {
					d := head
					d.Window = w
					ev(Expected, opened, d)
				}
			}
			k.open, k.severity, k.phase = true, in.Severity, in.Phase
		case k.open && open:
			at := timeOr(in.UpdatedAt, now)
			if in.Severity != k.severity && in.Severity != "" {
				d := head
				d.Field, d.From, d.To = "severity", k.severity, in.Severity
				ev(Changed, at, d)
				k.severity = in.Severity
			}
			if in.Phase != k.phase && in.Phase != "" {
				d := head
				d.Field, d.From, d.To = "phase", k.phase, in.Phase
				ev(Changed, at, d)
				k.phase = in.Phase
			}
		}
		if open {
			for _, c := range in.Causes {
				key := causeKey(c.Resource, c.Text)
				if k.causes[key] {
					continue
				}
				k.causes[key] = true
				d := head
				d.Resource, d.Text = c.Resource, clip(c.Text)
				ev(Caused, timeOr(in.UpdatedAt, now), d)
			}
			continue
		}
		if k.open {
			d := head
			d.By = in.ResolvedBy
			ev(Resolved, timeOr(in.ResolvedAt, now), d)
			k.open = false
		}
	}
	return out
}

// maxText bounds the copy an event keeps.
const maxText = 300

func clip(s string) string {
	if len(s) <= maxText {
		return s
	}
	cut := maxText
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut] + "…"
}
