package incidents

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"

	"dash.cha.sh/internal/authz"
	"dash.cha.sh/internal/livedoc"
)

// Acknowledging an incident: a person (operator and up; internal/authz
// decides) says "I've seen this, I'm on it" about an open problem. The
// inbox item gains
//
//	"acknowledged": {"by": "sam", "at": "2026-11-01T14:05:00Z", "note": "…"}
//
// the timeline gets an acknowledged event (who, when, the note), and
// notifications stop repeating it while it stays open (internal/notify).
// Un-acknowledging clears it with another acknowledged event, marked
// cleared; resolving the incident clears it with no event of its own.
//
// Many writers replace inbox items whole (the runtime's emitter, the
// correlator, the cause engine, the Demo Home) and none of them knows the
// field. The inbox's Keep function (keepAck) carries it over every Put
// while the item stays an open problem, and drops it once it resolves.
// After a restart, the recorder reads open acknowledgements back from the
// timeline and puts them back when their incidents reappear.

// ackField is the inbox item's member.
const ackField = "acknowledged"

var ackKey = []byte(`"` + ackField + `"`)

// inboxColl is the inbox topic's collection (topics.Incidents).
const inboxColl = "incidents"

// MaxNote bounds an acknowledgement's note, in characters.
const MaxNote = 300

// Ack is an incident's acknowledgement, as the inbox item carries it.
type Ack struct {
	// By is the username of whoever acknowledged it.
	By   string    `json:"by"`
	At   time.Time `json:"at"`
	Note string    `json:"note,omitempty"`
}

// Refusals.
var (
	ErrNoIncident       = errors.New("incidents: no such incident")
	ErrNotProblem       = errors.New("incidents: only a problem can be acknowledged")
	ErrResolved         = errors.New("incidents: it has resolved")
	ErrAcknowledged     = errors.New("incidents: already acknowledged")
	ErrNotAcknowledged  = errors.New("incidents: not acknowledged")
	errBadInboxItemJSON = errors.New("incidents: the inbox item isn't a JSON object")
)

// Acks acknowledges incidents. Safe for concurrent use.
type Acks struct {
	// Audit records the routes' changes. Set by the server.
	Audit *authz.Recorder

	store *Store
	inbox *livedoc.Doc
	now   func() time.Time
	log   *slog.Logger

	// whoFor names the person behind a request; nil uses authz.ActorFrom.
	// Tests replace it.
	whoFor func(*http.Request) (string, bool)

	mu sync.Mutex
	// restored are acknowledgements read back from the timeline at start,
	// waiting for their incident to reappear in the inbox.
	restored map[string]Ack
}

// AcksOptions configure Acks.
type AcksOptions struct {
	Store *Store
	// Inbox is the inbox topic's document. NewAcks installs keepAck on its
	// incidents collection.
	Inbox *livedoc.Doc
	Log   *slog.Logger
	Now   func() time.Time
}

// NewAcks returns Acks over the inbox and the timeline's store. Hand it to
// the Recorder (RecorderOptions.Acks) so acknowledgements survive a restart.
func NewAcks(o AcksOptions) *Acks {
	a := &Acks{store: o.Store, inbox: o.Inbox, now: o.Now, log: o.Log, restored: map[string]Ack{}}
	if a.now == nil {
		a.now = time.Now
	}
	if a.log == nil {
		a.log = slog.New(slog.DiscardHandler)
	}
	if a.inbox != nil {
		a.inbox.Keep(inboxColl, keepAck)
	}
	return a
}

// item is an inbox item decoded for acknowledging.
type item struct {
	m  map[string]json.RawMessage
	in observed
}

func decodeItem(raw json.RawMessage) (item, error) {
	var it item
	if raw == nil {
		return it, ErrNoIncident
	}
	if json.Unmarshal(raw, &it.m) != nil || json.Unmarshal(raw, &it.in) != nil {
		return it, errBadInboxItemJSON
	}
	return it, nil
}

func (it item) ack() (Ack, bool) {
	raw, ok := it.m[ackField]
	if !ok || string(raw) == "null" {
		return Ack{}, false
	}
	var a Ack
	return a, json.Unmarshal(raw, &a) == nil
}

func (it item) head() Data {
	in := it.in
	return Data{Target: in.Target, Class: in.Class, Category: in.Category, Scenario: in.Scenario, Title: clip(in.Title), Severity: in.Severity, Phase: in.Phase}
}

// ackable says whether an item can carry an acknowledgement: an open problem.
func ackable(class, status string) bool { return class == "problem" && status == "open" }

// Acknowledge marks an open problem incident as acknowledged by the user
// named by, with an optional note, and records it on the timeline.
func (a *Acks) Acknowledge(ctx context.Context, id, by, note string) (Ack, error) {
	ack := Ack{By: by, At: a.now().UTC().Truncate(time.Second), Note: CleanNote(note)}
	var head Data
	_, err := a.inbox.Update(func(tx *livedoc.Tx) error {
		it, err := decodeItem(tx.Item(inboxColl, id))
		switch {
		case err != nil:
			return err
		case it.in.Class != "problem":
			return ErrNotProblem
		case it.in.Status != "open":
			return ErrResolved
		}
		if _, ok := it.ack(); ok {
			return ErrAcknowledged
		}
		raw, _ := json.Marshal(ack)
		it.m[ackField] = raw
		tx.Put(inboxColl, id, it.m)
		head = it.head()
		return nil
	})
	if err != nil {
		return Ack{}, err
	}
	a.forget(id)
	d := head
	d.Who, d.Note = ack.By, ack.Note
	if err := a.record(ctx, id, ack.At, d); err != nil {
		a.put(id, nil) // not on the timeline: take it back
		return Ack{}, err
	}
	return ack, nil
}

// Unacknowledge clears an incident's acknowledgement, records who cleared
// it, and returns the acknowledgement it was.
func (a *Acks) Unacknowledge(ctx context.Context, id, by string) (Ack, error) {
	var was Ack
	var head Data
	_, err := a.inbox.Update(func(tx *livedoc.Tx) error {
		it, err := decodeItem(tx.Item(inboxColl, id))
		if err != nil {
			return err
		}
		ack, ok := it.ack()
		if !ok {
			return ErrNotAcknowledged
		}
		was, head = ack, it.head()
		it.m[ackField] = json.RawMessage("null") // keepAck drops it
		tx.Put(inboxColl, id, it.m)
		return nil
	})
	if err != nil {
		return Ack{}, err
	}
	a.forget(id)
	d := head
	d.Who, d.Cleared = by, true
	if err := a.record(ctx, id, a.now().UTC().Truncate(time.Second), d); err != nil {
		a.put(id, &was)
		return Ack{}, err
	}
	return was, nil
}

// Acked returns an incident's acknowledgement, if it has one.
func (a *Acks) Acked(id string) (Ack, bool) {
	it, err := decodeItem(a.inbox.Item(inboxColl, id))
	if err != nil {
		return Ack{}, false
	}
	return it.ack()
}

func (a *Acks) record(ctx context.Context, id string, at time.Time, d Data) error {
	raw, _ := json.Marshal(d)
	return a.store.Append(ctx, Event{IncidentID: id, At: at, Kind: Acknowledged, Data: raw})
}

// put sets (or with nil, clears) an item's acknowledgement directly,
// keeping whatever else the item holds. Used to take back a change the
// timeline didn't record, and to restore one after a restart.
func (a *Acks) put(id string, ack *Ack) {
	_, _ = a.inbox.Update(func(tx *livedoc.Tx) error {
		it, err := decodeItem(tx.Item(inboxColl, id))
		if err != nil {
			return nil
		}
		if ack == nil {
			it.m[ackField] = json.RawMessage("null")
		} else {
			raw, _ := json.Marshal(*ack)
			it.m[ackField] = raw
		}
		tx.Put(inboxColl, id, it.m)
		return nil
	})
}

func (a *Acks) forget(id string) {
	a.mu.Lock()
	delete(a.restored, id)
	a.mu.Unlock()
}

// restore keeps acknowledgements read back from the timeline: open is
// each incident still open there with its acknowledgement in force.
func (a *Acks) restore(open map[string]Ack) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for id, ack := range open {
		a.restored[id] = ack
	}
}

// reconcile puts restored acknowledgements back on their incidents once
// they're in the inbox again and still open, and forgets those that
// resolved, or that are older than the timeline keeps.
func (a *Acks) reconcile(incs map[string]observed, keep time.Duration) {
	a.mu.Lock()
	var apply []string
	for id, ack := range a.restored {
		in, ok := incs[id]
		switch {
		case !ok:
			if a.now().Sub(ack.At) > keep {
				delete(a.restored, id)
			}
		case !ackable(in.Class, in.Status):
			delete(a.restored, id)
		default:
			apply = append(apply, id)
		}
	}
	a.mu.Unlock()
	for _, id := range apply {
		a.mu.Lock()
		ack, ok := a.restored[id]
		delete(a.restored, id)
		a.mu.Unlock()
		if !ok {
			continue // acknowledged or cleared since
		}
		if _, has := a.Acked(id); !has {
			a.put(id, &ack)
			a.log.Debug("incidents: acknowledgement restored", "incident", id)
		}
	}
}

// keepAck is the inbox's Keep function. A Put that says nothing about the
// acknowledgement keeps the one the item had, while it stays an open
// problem; "acknowledged": null clears it; and an item that's no longer an
// open problem never keeps one.
func keepAck(cur, next json.RawMessage) json.RawMessage {
	if !bytes.Contains(next, ackKey) && !bytes.Contains(cur, ackKey) {
		return next
	}
	var n map[string]json.RawMessage
	if json.Unmarshal(next, &n) != nil {
		return next
	}
	open := ackable(str(n["class"]), str(n["status"]))
	if v, ok := n[ackField]; ok {
		if string(v) == "null" || !open {
			delete(n, ackField)
			return encode(n, next)
		}
		return next
	}
	if !open {
		return next
	}
	var c map[string]json.RawMessage
	if json.Unmarshal(cur, &c) != nil {
		return next
	}
	if v, ok := c[ackField]; ok && string(v) != "null" {
		n[ackField] = v
		return encode(n, next)
	}
	return next
}

func str(raw json.RawMessage) string {
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}

func encode(m map[string]json.RawMessage, fallback json.RawMessage) json.RawMessage {
	b, err := json.Marshal(m)
	if err != nil {
		return fallback
	}
	return b
}

// CleanNote trims a note, drops control and invisible format characters
// (newlines become spaces) and cuts it to MaxNote characters.
func CleanNote(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t' || r == '\r':
			return ' '
		case unicode.IsControl(r) || unicode.Is(unicode.Cf, r):
			return -1
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > MaxNote {
		s = string(r[:MaxNote])
	}
	return s
}

// openAcks reads, from the timeline's events (oldest first), each
// incident's acknowledgement still in force: given after its last opening,
// not cleared, not resolved since.
func openAcks(evs []Event) map[string]Ack {
	out := map[string]Ack{}
	for _, e := range evs {
		switch e.Kind {
		case Opened, Resolved:
			delete(out, e.IncidentID)
		case Acknowledged:
			d := DataOf(e)
			if d.Cleared {
				delete(out, e.IncidentID)
			} else {
				out[e.IncidentID] = Ack{By: d.Who, At: e.At, Note: d.Note}
			}
		}
	}
	return out
}
