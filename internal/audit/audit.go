// Package audit is the hub's append-only, hash-chained audit log: every
// action attempt (including refusals and failures), and settings and user
// changes as actions named settings.<area>.update and users.<verb>.
//
// # The chain
//
// Events are numbered 1, 2, 3, … (seq) and each one carries prev_hash, the
// hash of the event before it; the first event's prev_hash is ZeroHash. An
// event's hash is "sha256:" plus the lowercase hex SHA-256 of its canonical
// JSON. Changing, removing or reordering any event therefore breaks the hash
// of every event after it, and Verify finds the first break.
//
// # The canonical form
//
// The canonical JSON of an event is compact (no whitespace), with the
// members {"id","seq","at","actor","action","target","params","tier",
// "result","error","duration_ms","task","prev_hash"} in exactly that order,
// and no "hash". Within it:
//
//   - at is UTC RFC 3339 with milliseconds: 2026-10-04T14:07:11.250Z.
//   - actor is {"kind","id","name","via","session","ip","on_behalf_of"} in
//     that order; empty members are left out, except kind and name.
//     on_behalf_of (the user an API token acts for) was added in v0.2 after
//     the first events were written; because it's left out when empty, an
//     event without it has exactly the bytes, and the hash, it always had.
//   - params is an object whose keys are sorted at every depth, numbers
//     written exactly as received; secret-typed params are "[redacted]".
//   - target, tier and task are strings, "" when there's none; error is
//     {"code","message"} or null; duration_ms is an integer.
//   - Strings are escaped as Go's encoding/json does, which writes <, > and &
//     as \u003c, \u003e and \u0026.
//
// The database keeps those exact bytes (audit_events.event) next to the
// hash, and the NDJSON export writes them with ,"hash":"…" spliced in before
// the closing brace. So anyone can check an exported chain without this
// code: strip the trailing ,"hash":"…", hash the rest, and compare it with
// the next line's prev_hash.
//
// Nothing here updates or deletes an event: TestNoUpdateOrDelete greps the
// code, and the table's triggers refuse it.
//
// # What the chain can't show
//
// The chain has no external anchor and no key. Anyone who can write the
// SQLite file can drop the triggers, cut events off the end, or rewrite
// events and recompute every hash after them, and Verify will still say the
// chain holds. It detects accidents and edits by someone who doesn't bother
// to recompute, not a determined attacker with the file. To notice those,
// record the head (Head: seq and hash, which Verify reports and the export
// sends as X-Chadash-Audit-Head) somewhere the hub can't write, and check a
// later chain still contains it. A keyed (HMAC) chain is future work.
package audit

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"dash.cha.sh/internal/store"
)

// ZeroHash is the first event's prev_hash.
var ZeroHash = "sha256:" + strings.Repeat("0", 64)

// Redacted replaces the value of a secret-typed param.
const Redacted = "[redacted]"

// Results.
const (
	OK      = "ok"
	Failed  = "failed"
	Refused = "refused"
)

// Actor is who did it.
type Actor struct {
	Kind    string `json:"kind"` // user, token, agent, system
	ID      string `json:"id,omitempty"`
	Name    string `json:"name"`
	Via     string `json:"via,omitempty"` // web, api, mcp, cli
	Session string `json:"session,omitempty"`
	IP      string `json:"ip,omitempty"`
	// OnBehalfOf is the user an API token (or an agent) acts for. Left out
	// when empty, so events recorded before it existed keep their bytes.
	OnBehalfOf string `json:"on_behalf_of,omitempty"`
}

// Error is why an attempt was refused or failed.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Event is one audit record, as the API returns it. Its members are in
// canonical order (see the package doc).
type Event struct {
	ID         string          `json:"id"`
	Seq        int64           `json:"seq"`
	At         Time            `json:"at"`
	Actor      Actor           `json:"actor"`
	Action     string          `json:"action"`
	Target     string          `json:"target"`
	Params     json.RawMessage `json:"params"`
	Tier       string          `json:"tier"`
	Result     string          `json:"result"`
	Error      *Error          `json:"error"`
	DurationMS int64           `json:"duration_ms"`
	Task       string          `json:"task"`
	PrevHash   string          `json:"prev_hash"`
	Hash       string          `json:"hash"`
}

// Time is a timestamp in the canonical form: UTC, RFC 3339, milliseconds.
type Time struct{ time.Time }

const timeLayout = "2006-01-02T15:04:05.000Z07:00"

// MarshalJSON implements json.Marshaler.
func (t Time) MarshalJSON() ([]byte, error) {
	return []byte(`"` + t.UTC().Format(timeLayout) + `"`), nil
}

// UnmarshalJSON implements json.Unmarshaler.
func (t *Time) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	v, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return err
	}
	t.Time = v.UTC()
	return nil
}

// Entry is what a caller records; Append fills in the rest.
type Entry struct {
	Actor  Actor
	Action string
	Target string
	// Params as the caller received them: a JSON object, or nil for none.
	Params json.RawMessage
	// Secrets are the top-level params to redact (the action's secret-typed
	// params). Their values never reach the log.
	Secrets  []string
	Tier     string
	Result   string // OK, Failed or Refused
	Error    *Error
	Duration time.Duration
	Task     string
}

// Log writes and reads the audit log.
type Log struct {
	st  *store.Store
	now func() time.Time
}

// New returns the audit log over st's audit_events table.
func New(st *store.Store) *Log { return &Log{st: st, now: time.Now} }

// SetClock replaces the clock, for tests.
func (l *Log) SetClock(now func() time.Time) { l.now = now }

// Append records e at the end of the chain and returns the stored event.
// The single writer connection serialises appends, so two can't claim the
// same seq.
func (l *Log) Append(ctx context.Context, e Entry) (Event, error) {
	switch e.Result {
	case OK, Failed, Refused:
	default:
		return Event{}, fmt.Errorf("audit: result %q", e.Result)
	}
	if e.Action == "" {
		return Event{}, errors.New("audit: no action")
	}
	params, err := canonicalParams(e.Params, e.Secrets)
	if err != nil {
		return Event{}, fmt.Errorf("audit: params: %w", err)
	}
	ev := Event{
		ID:         "a_" + strings.ToLower(rand.Text()),
		At:         Time{l.now().UTC().Truncate(time.Millisecond)},
		Actor:      e.Actor,
		Action:     e.Action,
		Target:     e.Target,
		Params:     params,
		Tier:       e.Tier,
		Result:     e.Result,
		Error:      e.Error,
		DurationMS: e.Duration.Milliseconds(),
		Task:       e.Task,
	}
	err = l.st.WriteTx(ctx, func(tx *sql.Tx) error {
		var last int64
		prev := ZeroHash
		err := tx.QueryRowContext(ctx, `SELECT seq, hash FROM audit_events ORDER BY seq DESC LIMIT 1`).Scan(&last, &prev)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		ev.Seq, ev.PrevHash = last+1, prev
		body, err := Canonical(ev)
		if err != nil {
			return err
		}
		ev.Hash = HashOf(body)
		_, err = tx.ExecContext(ctx, `INSERT INTO audit_events (seq, id, at, actor_id, actor, action, target, result, event, prev_hash, hash)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			ev.Seq, ev.ID, ev.At.UnixMilli(), ev.Actor.ID, ev.Actor.Name, ev.Action, ev.Target, ev.Result, string(body), ev.PrevHash, ev.Hash)
		return err
	})
	if err != nil {
		return Event{}, fmt.Errorf("audit: append: %w", err)
	}
	return ev, nil
}

// Canonical is ev's canonical JSON: every member but hash, in order. ev's
// params must already be canonical (Append makes them so).
func Canonical(ev Event) ([]byte, error) {
	type canonical struct {
		ID         string          `json:"id"`
		Seq        int64           `json:"seq"`
		At         Time            `json:"at"`
		Actor      Actor           `json:"actor"`
		Action     string          `json:"action"`
		Target     string          `json:"target"`
		Params     json.RawMessage `json:"params"`
		Tier       string          `json:"tier"`
		Result     string          `json:"result"`
		Error      *Error          `json:"error"`
		DurationMS int64           `json:"duration_ms"`
		Task       string          `json:"task"`
		PrevHash   string          `json:"prev_hash"`
	}
	c := canonical{ev.ID, ev.Seq, ev.At, ev.Actor, ev.Action, ev.Target, ev.Params, ev.Tier, ev.Result, ev.Error, ev.DurationMS, ev.Task, ev.PrevHash}
	if c.Params == nil {
		c.Params = json.RawMessage("{}")
	}
	return json.Marshal(c)
}

// HashOf is the hash of a canonical event.
func HashOf(canonical []byte) string {
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// canonicalParams re-encodes params with sorted keys, numbers verbatim, and
// the named top-level secrets redacted. Nil or null is {}.
func canonicalParams(raw json.RawMessage, secrets []string) (json.RawMessage, error) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return json.RawMessage("{}"), nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, errors.New("params must be a JSON object")
	}
	for _, s := range secrets {
		if _, ok := m[s]; ok {
			m[s] = Redacted
		}
	}
	// encoding/json sorts map keys at every depth and writes json.Number as
	// is.
	return json.Marshal(m)
}
