package causes

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"time"

	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/history"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/livedoc"
	"dash.cha.sh/internal/topics"
)

// History is the health history Find reads (history.Recorder).
type History interface {
	States(ctx context.Context, resource string, from, to time.Time) ([]history.StateChange, error)
}

// Options configure a Service.
type Options struct {
	// Topics is the set it reads the graph and inbox from, and writes the
	// causes and the incidents' hints to. In demo mode it's the Demo
	// Home's: the engine runs over it as over a real home.
	Topics *topics.Set
	// History is the health history; nil when it's off or not kept.
	History History
	Log     *slog.Logger
	// Now is the clock (nil: time.Now); demo mode passes the simulator's.
	Now func() time.Time
	// Clock formats a time of day in the sentences (nil: the hub's local
	// time, "15:04").
	Clock func(time.Time) string
	// Debounce is how long Run waits after a change (default 1 s), and
	// Every how often it runs regardless, as the window moves (default
	// 1 minute).
	Debounce, Every time.Duration
}

// remember is how long an event stays remembered after it happened.
const remember = time.Hour

// Service keeps the causes topic and the incidents' hints current. Safe for
// concurrent use.
type Service struct {
	o   Options
	log *slog.Logger

	mu       sync.Mutex
	events   map[graph.RID][]Event
	restarts map[graph.RID]int // a container's last restart_count
	notes    map[string][]integration.Cause
}

// New returns a service over opts.Topics. Call Run to start it.
func New(o Options) *Service {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Clock == nil {
		o.Clock = func(t time.Time) string { return t.Local().Format("15:04") }
	}
	if o.Debounce <= 0 {
		o.Debounce = time.Second
	}
	if o.Every <= 0 {
		o.Every = time.Minute
	}
	log := o.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Service{o: o, log: log, events: map[graph.RID][]Event{}, restarts: map[graph.RID]int{}, notes: map[string][]integration.Cause{}}
}

// Run finds causes whenever the graph or the inbox changes, and every
// Every, until ctx ends.
func (s *Service) Run(ctx context.Context) {
	gn, in := make(chan struct{}, 1), make(chan struct{}, 1)
	gw := s.o.Topics.Graph.Watch(nil, gn, 0)
	defer gw.Close()
	iw := s.o.Topics.Inbox.Watch(in, 0)
	defer iw.Close()
	tick := time.NewTicker(s.o.Every)
	defer tick.Stop()
	for {
		if err := s.Sync(ctx); err != nil && ctx.Err() == nil {
			s.log.Warn("finding causes failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-gn:
		case <-in:
		case <-tick.C:
		}
		t := time.NewTimer(s.o.Debounce)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		if _, _, err := gw.Drain(); err != nil {
			gw.Snapshot()
		}
		if _, _, err := iw.Drain(); err != nil {
			iw.Snapshot()
		}
	}
}

// Sync runs one pass now.
func (s *Service) Sync(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.o.Now()
	_, doc := s.o.Topics.Graph.Snapshot(nil)
	s.observe(doc, now)

	_, snap := s.o.Topics.Inbox.Snapshot()
	items, _ := snap[topics.Incidents].(map[string]json.RawMessage)
	incidents := map[string]integration.Incident{}
	for id, raw := range items {
		var inc integration.Incident
		if json.Unmarshal(raw, &inc) == nil {
			strip(&inc, s.notes[id])
			incidents[id] = inc
		}
	}

	in := Input{Doc: doc, Now: now, Incidents: incidents, Events: s.events, Clock: s.o.Clock}
	if s.o.History != nil {
		memo := map[string][]history.StateChange{}
		in.Changes = func(res string, from, to time.Time) []history.StateChange {
			k := res + "\x00" + from.String()
			if v, ok := memo[k]; ok {
				return v
			}
			v, err := s.o.History.States(ctx, res, from, to)
			if err != nil {
				s.log.Debug("causes: reading history failed", "resource", res, "err", err)
			}
			memo[k] = v
			return v
		}
	}
	found := Find(in)
	all := make(map[string][]topics.Cause, len(found))
	for id, cs := range found {
		all[string(id)] = cs
	}
	if err := s.o.Topics.Causes.Replace(all); err != nil {
		return err
	}
	return s.incidents(found, doc, incidents)
}

// observe remembers the events the graph shows now, so they outlive it: an
// OOM-killed container restarts and stops saying so. A restart_count that
// went up is a restart, now.
func (s *Service) observe(doc graph.Doc, now time.Time) {
	primary := map[graph.RID]graph.RID{}
	for _, r := range doc.Relations {
		if r.Type == sameAs {
			primary[r.From] = r.To
		}
	}
	seen := map[graph.RID]bool{}
	for id, r := range doc.Resources {
		if r.Kind != "container" {
			continue
		}
		seen[id] = true
		p := id
		if to, ok := primary[id]; ok {
			p = to
		}
		for _, e := range eventsOf(r) {
			if e.Rule != RuleStarted && !slices.Contains(s.events[p], e) {
				s.events[p] = append(s.events[p], e)
			}
		}
		if n, ok := attrInt(r, "restart_count"); ok {
			if last, known := s.restarts[id]; known && n > last {
				at := now
				if t, ok := attrTime(r, "started_at"); ok && !t.After(now) {
					at = t
				}
				s.events[p] = append(s.events[p], Event{Rule: RuleRestarted, At: at})
			}
			s.restarts[id] = n
		}
	}
	for id := range s.restarts {
		if !seen[id] {
			delete(s.restarts, id)
		}
	}
	for id, evs := range s.events {
		evs = slices.DeleteFunc(evs, func(e Event) bool { return now.Sub(e.At) > remember })
		if len(evs) == 0 {
			delete(s.events, id)
		} else {
			s.events[id] = evs
		}
	}
}

// incidents adds the hints to open problem incidents, after the
// incident's own causes (and the correlator's, in front of them). What it
// added last time is taken off first, if it's still there. Repairs, hints,
// suggestions and security findings get none. Only incidents it adds to,
// or added to before, are rewritten.
func (s *Service) incidents(found map[graph.RID][]topics.Cause, doc graph.Doc, incidents map[string]integration.Incident) error {
	primary := map[graph.RID]graph.RID{}
	for _, r := range doc.Relations {
		if r.Type == sameAs {
			primary[r.From] = r.To
		}
	}
	// What each incident gains, from the incidents as read (stripped).
	want := map[string][]integration.Cause{}
	for id, inc := range incidents {
		if inc.Status != integration.StatusOpen || inc.Class != integration.ClassProblem || inc.Category == integration.CategorySecurity {
			continue
		}
		target := graph.RID(inc.Target)
		if p, ok := primary[target]; ok {
			target = p
		}
		var add []integration.Cause
		for _, c := range found[target] {
			if !slices.ContainsFunc(inc.Causes, func(o integration.Cause) bool { return o.Resource == c.Resource }) {
				add = append(add, integration.Cause{Text: c.Text, Resource: c.Resource, At: c.At})
			}
		}
		if len(add) > 0 {
			want[id] = add
		}
	}
	ids := slices.Collect(maps.Keys(want))
	for id := range s.notes {
		if _, ok := want[id]; !ok {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	next := map[string][]integration.Cause{}
	_, err := s.o.Topics.Inbox.Update(func(tx *livedoc.Tx) error {
		// Read, change and write each incident under the inbox's lock, so a
		// concurrent write by its instance is never lost.
		for _, id := range ids {
			raw := tx.Item(topics.Incidents, id)
			if raw == nil {
				continue
			}
			var inc integration.Incident
			if err := json.Unmarshal(raw, &inc); err != nil {
				continue
			}
			strip(&inc, s.notes[id])
			if add := want[id]; len(add) > 0 && inc.Status == integration.StatusOpen {
				inc.Causes = append(slices.Clone(inc.Causes), add...)
				next[id] = add
			}
			if inc.Causes == nil {
				inc.Causes = []integration.Cause{}
			}
			if inc.BlastRadius == nil {
				inc.BlastRadius = []integration.Impact{}
			}
			b, err := json.Marshal(inc)
			if err != nil {
				return err
			}
			if !bytes.Equal(raw, b) {
				tx.Put(topics.Incidents, id, json.RawMessage(b))
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.notes = next
	return nil
}

// strip takes off the hints added before, if the incident still ends with
// them: its instance may have rewritten it since.
func strip(in *integration.Incident, added []integration.Cause) {
	k := len(added)
	if k == 0 || len(in.Causes) < k {
		return
	}
	a, _ := json.Marshal(in.Causes[len(in.Causes)-k:])
	b, _ := json.Marshal(added)
	if bytes.Equal(a, b) {
		in.Causes = slices.Clone(in.Causes[:len(in.Causes)-k])
	}
}
