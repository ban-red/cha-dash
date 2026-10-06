package correlate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/livedoc"
	"dash.cha.sh/internal/store"
	"dash.cha.sh/internal/topics"
)

// Options configure a Correlator.
type Options struct {
	// Topics is the set the integration instances write to; the correlator
	// reads its graph and inbox and writes its links and suggestions back.
	Topics *topics.Set
	// Store persists the owner's decisions. Nil keeps them in memory.
	Store *store.Store
	Log   *slog.Logger
	// Now is the clock (nil: time.Now).
	Now func() time.Time
	// Debounce is how long Run waits after a change before correlating, so
	// a burst of facts is one pass (default 500 ms).
	Debounce time.Duration
}

// ResolvedRetention is how long a decided suggestion stays in the inbox,
// like a resolved incident.
const ResolvedRetention = 24 * time.Hour

// SuggestionPrefix starts the inbox ID of every suggestion.
const SuggestionPrefix = "correlate."

// Errors the API maps to statuses.
var (
	ErrNotFound = errors.New("correlate: not found")
	ErrInvalid  = errors.New("correlate: invalid decision")
)

// Correlator runs the correlation over the live graph: it publishes links
// into the graph, suggestions into the inbox, and the causes it can add to
// other instances' incidents. Safe for concurrent use.
type Correlator struct {
	t        *topics.Set
	db       decisions
	log      *slog.Logger
	now      func() time.Time
	debounce time.Duration

	mu        sync.Mutex // serialises passes and decisions
	mem       []Decision // decisions, without a store
	version   uint64     // graph version the result reflects
	decs      []Decision // decisions the result reflects
	fresh     bool       // result has been computed
	result    Result
	published map[string]graph.Relation // relations in the graph, by key
	sugg      map[string]*suggestion    // inbox ID → suggestion
	notes     map[string]note           // incident ID → what was added to it
}

type suggestion struct {
	link       Link
	opened     time.Time
	resolved   time.Time
	resolution string
}

// New returns a correlator over opts.Topics. Call Run to start it.
func New(opts Options) *Correlator {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Log == nil {
		opts.Log = slog.New(slog.DiscardHandler)
	}
	if opts.Debounce <= 0 {
		opts.Debounce = 500 * time.Millisecond
	}
	return &Correlator{
		t: opts.Topics, db: decisions{opts.Store}, log: opts.Log, now: opts.Now, debounce: opts.Debounce,
		published: map[string]graph.Relation{}, sugg: map[string]*suggestion{}, notes: map[string]note{},
	}
}

func (c *Correlator) clock() time.Time { return c.now().UTC().Truncate(time.Second) }

// Run correlates whenever the graph or the inbox changes, until ctx ends.
func (c *Correlator) Run(ctx context.Context) {
	gn, in := make(chan struct{}, 1), make(chan struct{}, 1)
	gw := c.t.Graph.Watch(nil, gn, 0)
	defer gw.Close()
	iw := c.t.Inbox.Watch(in, 0)
	defer iw.Close()
	for {
		if err := c.Sync(ctx); err != nil && ctx.Err() == nil {
			c.log.Warn("correlating failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-gn:
		case <-in:
		}
		t := time.NewTimer(c.debounce)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		// The watchers only say "something changed"; each pass reads fresh
		// snapshots. Draining keeps their queues from overflowing.
		if _, _, err := gw.Drain(); err != nil {
			gw.Snapshot()
		}
		if _, _, err := iw.Drain(); err != nil {
			iw.Snapshot()
		}
	}
}

// Sync runs one pass now.
func (c *Correlator) Sync(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sync(ctx)
}

func (c *Correlator) loadDecisions(ctx context.Context) ([]Decision, error) {
	if c.db.st == nil {
		return slices.Clone(c.mem), nil
	}
	return c.db.load(ctx)
}

func (c *Correlator) sync(ctx context.Context) error {
	decs, err := c.loadDecisions(ctx)
	if err != nil {
		return err
	}
	ver, doc := c.t.Graph.Snapshot(nil)
	if !c.fresh || ver != c.version || !slices.EqualFunc(decs, c.decs, func(a, b Decision) bool { return a == b }) {
		// Publishing bumps the version, so the next pass correlates once
		// more, finds the same links and changes nothing.
		c.result = Correlate(doc, decs)
		c.fresh, c.decs, c.version = true, decs, ver
		if err := c.publish(doc); err != nil {
			c.fresh = false
			return err
		}
	}
	return c.inbox(doc)
}

// publish makes the graph hold exactly the result's links. It never takes
// over or removes a relation a source published under the same key (none
// of the correlator's relation kinds overlap with what sources publish).
func (c *Correlator) publish(doc graph.Doc) error {
	want := map[string]graph.Relation{}
	for _, l := range c.result.Links {
		want[l.Key()] = l.relation()
	}
	next := map[string]graph.Relation{}
	_, err := c.t.Graph.Update(func(tx *graph.Tx) error {
		for k, rel := range c.published {
			if _, keep := want[k]; keep {
				continue
			}
			if cur := doc.Relations[k]; cur != nil && cur.Source != Source {
				continue
			}
			tx.Unrelate(rel.From, rel.Type, rel.To)
		}
		for k, rel := range want {
			if cur := doc.Relations[k]; cur != nil && cur.Source != Source {
				continue
			}
			_, okFrom := tx.Get(rel.From)
			_, okTo := tx.Get(rel.To)
			if !okFrom || !okTo {
				continue
			}
			if err := tx.Relate(rel); err != nil {
				return err
			}
			next[k] = rel
		}
		return nil
	})
	if err != nil {
		return err
	}
	c.published = next
	return nil
}

// inbox brings the inbox in line: suggestions in, decided ones resolved,
// and the causes explain finds added to other instances' incidents.
func (c *Correlator) inbox(doc graph.Doc) error {
	now := c.clock()
	_, snap := c.t.Inbox.Snapshot()
	items, _ := snap[topics.Incidents].(map[string]json.RawMessage)
	incidents := map[string]integration.Incident{}
	for id, raw := range items {
		if strings.HasPrefix(id, SuggestionPrefix) {
			continue
		}
		var in integration.Incident
		if err := json.Unmarshal(raw, &in); err != nil {
			continue
		}
		strip(&in, c.notes[id])
		incidents[id] = in
	}
	notes := explain(doc, c.result.Links, incidents)

	want := map[string]Link{}
	for _, s := range c.result.Suggestions {
		want[suggestionID(s)] = s
	}
	for id, l := range want {
		switch s, ok := c.sugg[id]; {
		case !ok || !s.resolved.IsZero():
			// New, or decided and then forgotten: open (again).
			c.sugg[id] = &suggestion{link: l, opened: now}
		default:
			s.link = l
		}
	}
	var drop []string
	for id, s := range c.sugg {
		_, wanted := want[id]
		if s.resolved.IsZero() && !wanted || !s.resolved.IsZero() && now.Sub(s.resolved) > ResolvedRetention {
			drop = append(drop, id)
		}
	}

	ids := make([]string, 0, len(notes)+len(c.notes))
	for id := range notes {
		ids = append(ids, id)
	}
	for id := range c.notes {
		if _, ok := notes[id]; !ok {
			ids = append(ids, id)
		}
	}
	nextNotes := map[string]note{}
	_, err := c.t.Inbox.Update(func(tx *livedoc.Tx) error {
		for _, id := range drop {
			tx.Delete(topics.Incidents, id)
		}
		for id, s := range c.sugg {
			if slices.Contains(drop, id) {
				continue
			}
			b, err := json.Marshal(renderSuggestion(doc, id, s))
			if err != nil {
				return err
			}
			if !bytes.Equal(tx.Item(topics.Incidents, id), b) {
				tx.Put(topics.Incidents, id, json.RawMessage(b))
			}
		}
		// Read, change and write each incident under the inbox's lock, so a
		// concurrent write by its instance is never lost.
		for _, id := range ids {
			raw := tx.Item(topics.Incidents, id)
			if raw == nil {
				continue
			}
			var in integration.Incident
			if err := json.Unmarshal(raw, &in); err != nil {
				continue
			}
			strip(&in, c.notes[id])
			n, ok := notes[id]
			if ok && in.Status == integration.StatusOpen {
				in.Causes = append(slices.Clone(n.Causes), in.Causes...)
				in.BlastRadius = append(slices.Clone(in.BlastRadius), n.Impacts...)
				nextNotes[id] = n
			}
			if in.Causes == nil {
				in.Causes = []integration.Cause{}
			}
			if in.BlastRadius == nil {
				in.BlastRadius = []integration.Impact{}
			}
			b, err := json.Marshal(in)
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
	for _, id := range drop {
		delete(c.sugg, id)
	}
	c.notes = nextNotes
	return nil
}

// strip takes off what the correlator added to an incident before, if it's
// still there: the instance may have rewritten the incident since.
func strip(in *integration.Incident, n note) {
	if k := len(n.Causes); k > 0 && len(in.Causes) >= k && sameJSON(in.Causes[:k], n.Causes) {
		in.Causes = slices.Clone(in.Causes[k:])
	}
	if k := len(n.Impacts); k > 0 && len(in.BlastRadius) >= k && sameJSON(in.BlastRadius[len(in.BlastRadius)-k:], n.Impacts) {
		in.BlastRadius = slices.Clone(in.BlastRadius[:len(in.BlastRadius)-k])
	}
}

func sameJSON(a, b any) bool {
	x, err1 := json.Marshal(a)
	y, err2 := json.Marshal(b)
	return err1 == nil && err2 == nil && bytes.Equal(x, y)
}

// suggestionID is a suggestion's inbox ID, stable for the pair.
func suggestionID(l Link) string {
	k := keyOf(l.From, l.Type, l.To)
	sum := sha256.Sum256([]byte(string(k.from) + "\x00" + string(k.typ) + "\x00" + string(k.to)))
	return SuggestionPrefix + hex.EncodeToString(sum[:8])
}

// Links returns what the correlator publishes now, with each link's rule,
// confidence and evidence.
func (c *Correlator) Links() []Link {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.result.Links)
}

// Suggestion is an open suggestion as the API lists it.
type Suggestion struct {
	ID string `json:"id"`
	Link
	Title   string    `json:"title"`
	Summary string    `json:"summary"`
	Opened  time.Time `json:"opened_at"`
}

// Suggestions returns the open suggestions, oldest first.
func (c *Correlator) Suggestions() []Suggestion {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, doc := c.t.Graph.Snapshot(nil)
	var out []Suggestion
	for id, s := range c.sugg {
		if !s.resolved.IsZero() {
			continue
		}
		in := renderSuggestion(doc, id, s)
		out = append(out, Suggestion{ID: id, Link: s.link, Title: in.Title, Summary: in.Summary, Opened: s.opened})
	}
	slices.SortFunc(out, func(a, b Suggestion) int {
		if c := a.Opened.Compare(b.Opened); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out
}

// Decisions returns the owner's decisions, oldest first.
func (c *Correlator) Decisions(ctx context.Context) ([]Decision, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.loadDecisions(ctx)
}

// relTypes are the relations a decision can be about.
var relTypes = []graph.RelType{SameAs, RunsOn, RoutesTo, StoresOn, DependsOn, Monitors}

// DecisionTypes are the relations a decision can be about (Home files
// carry decisions; internal/export checks them against this).
func DecisionTypes() []graph.RelType { return slices.Clone(relTypes) }

// StoredPair is a pair in the order the correlation_decisions table keeps
// it: a same_as pair with the smaller resource ID first, others as given.
func StoredPair(from graph.RID, typ graph.RelType, to graph.RID) (graph.RID, graph.RID) {
	k := keyOf(from, typ, to)
	return k.from, k.to
}

// Decide records the owner's verdict on a pair and correlates again.
// Both resources must be in the graph. userID is who decided ("" if
// unknown).
func (c *Correlator) Decide(ctx context.Context, from graph.RID, typ graph.RelType, to graph.RID, verdict Verdict, userID string) (Decision, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.decide(ctx, from, typ, to, verdict, "", userID)
}

// DecideSuggestion answers an open suggestion.
func (c *Correlator) DecideSuggestion(ctx context.Context, id string, verdict Verdict, userID string) (Decision, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s, ok := c.sugg[id]
	if !ok || !s.resolved.IsZero() {
		return Decision{}, ErrNotFound
	}
	return c.decide(ctx, s.link.From, s.link.Type, s.link.To, verdict, s.link.Rule, userID)
}

func (c *Correlator) decide(ctx context.Context, from graph.RID, typ graph.RelType, to graph.RID, verdict Verdict, rule, userID string) (Decision, error) {
	if verdict != Confirm && verdict != Reject || !slices.Contains(relTypes, typ) || from == "" || to == "" || from == to {
		return Decision{}, ErrInvalid
	}
	a, okA := c.t.Graph.Get(from)
	b, okB := c.t.Graph.Get(to)
	if !okA || !okB {
		return Decision{}, ErrNotFound
	}
	k := keyOf(from, typ, to)
	d := Decision{ID: newDecisionID(), From: k.from, Type: typ, To: k.to, Verdict: verdict, Rule: rule, CreatedAt: c.clock()}
	fromInst, toInst := instanceOf(&a), instanceOf(&b)
	if k.from != from {
		fromInst, toInst = toInst, fromInst
	}
	d, err := c.db.save(ctx, d, fromInst, toInst, userID)
	if err != nil {
		return Decision{}, err
	}
	if c.db.st == nil {
		if i := slices.IndexFunc(c.mem, func(x Decision) bool { return keyOf(x.From, x.Type, x.To) == k }); i >= 0 {
			d.ID = c.mem[i].ID
			c.mem = slices.Delete(c.mem, i, i+1)
		}
		c.mem = append(c.mem, d)
	}
	if s, ok := c.sugg[suggestionID(Link{From: from, Type: typ, To: to})]; ok && s.resolved.IsZero() {
		s.resolved = c.clock()
		switch {
		case verdict == Reject:
			s.resolution = "Kept separate. Cha Dash won't suggest this again."
		case typ == SameAs:
			s.resolution = "Merged. Cha Dash shows them as one."
		default:
			s.resolution = "Linked."
		}
	}
	return d, c.sync(ctx)
}

// Forget deletes a decision, so the rules judge the pair again.
func (c *Correlator) Forget(ctx context.Context, id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.db.st == nil {
		n := len(c.mem)
		c.mem = slices.DeleteFunc(c.mem, func(d Decision) bool { return d.ID == id })
		if len(c.mem) == n {
			return ErrNotFound
		}
	} else if ok, err := c.db.remove(ctx, id); err != nil {
		return err
	} else if !ok {
		return ErrNotFound
	}
	return c.sync(ctx)
}
