package incidents

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"dash.cha.sh/internal/store"
)

const day = 24 * time.Hour

// DefaultKeep is how long incident events are kept until an admin chooses
// otherwise in Settings → History (decision 10).
const DefaultKeep = 90 * day

// KeepChoices are the retentions Settings → History offers, shortest first.
var KeepChoices = []time.Duration{7 * day, 30 * day, 90 * day, 180 * day, 365 * day}

// KeepKey is the hub database's settings row for the retention an admin
// chose, a JSON number of milliseconds. Without it, DefaultKeep.
const KeepKey = "incidents.keep"

// ErrBadKeep refuses a retention outside KeepChoices.
var ErrBadKeep = errors.New("incidents: keep is one of 7, 30, 90, 180 or 365 days")

// CheckKeep reports whether d is one of KeepChoices.
func CheckKeep(d time.Duration) error {
	if !slices.Contains(KeepChoices, d) {
		return ErrBadKeep
	}
	return nil
}

// Store keeps incident events in incident_events and deletes them by age.
type Store struct {
	st   *store.Store
	keep atomic.Int64 // nanoseconds
}

// NewStore returns the store over st, keeping DefaultKeep until Load or
// SetKeep says otherwise.
func NewStore(st *store.Store) *Store {
	s := &Store{st: st}
	s.keep.Store(int64(DefaultKeep))
	return s
}

// Load reads the retention an admin chose, if any.
func (s *Store) Load(ctx context.Context) error {
	var raw string
	err := s.st.Reader().QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, KeepKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("incidents: %w", err)
	}
	ms, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || CheckKeep(time.Duration(ms)*time.Millisecond) != nil {
		return nil // a value this version doesn't offer: the default
	}
	s.keep.Store(int64(time.Duration(ms) * time.Millisecond))
	return nil
}

// Keep is the retention in force.
func (s *Store) Keep() time.Duration { return time.Duration(s.keep.Load()) }

// SetKeep changes the retention and saves it for the next start. The next
// prune applies it.
func (s *Store) SetKeep(ctx context.Context, d time.Duration, now time.Time) error {
	if err := CheckKeep(d); err != nil {
		return err
	}
	if _, err := s.st.Writer().ExecContext(ctx, `INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		KeepKey, strconv.FormatInt(d.Milliseconds(), 10), now.UnixMilli()); err != nil {
		return fmt.Errorf("incidents: %w", err)
	}
	s.keep.Store(int64(d))
	return nil
}

// newEventID is a prefixed random ID.
func newEventID() string { return "iev_" + strings.ToLower(rand.Text()) }

// Append writes events in one transaction. An event without an ID gets
// one; one without data gets {}.
func (s *Store) Append(ctx context.Context, evs ...Event) error {
	if len(evs) == 0 {
		return nil
	}
	return s.st.WriteTx(ctx, func(tx *sql.Tx) error {
		for _, e := range evs {
			if e.ID == "" {
				e.ID = newEventID()
			}
			data := string(e.Data)
			if len(e.Data) == 0 {
				data = "{}"
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO incident_events (id, incident_id, at, kind, data) VALUES (?, ?, ?, ?, ?)`,
				e.ID, e.IncidentID, e.At.UnixMilli(), string(e.Kind), data); err != nil {
				return fmt.Errorf("incidents: %w", err)
			}
		}
		return nil
	})
}

// Prune deletes events older than the retention, and reports how many.
func (s *Store) Prune(ctx context.Context, now time.Time) (int64, error) {
	res, err := s.st.Writer().ExecContext(ctx, `DELETE FROM incident_events WHERE at < ?`, now.Add(-s.Keep()).UnixMilli())
	if err != nil {
		return 0, fmt.Errorf("incidents: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// MaxEvents bounds one Events call.
const MaxEvents = 5000

// Query selects events. Zero fields match everything.
type Query struct {
	// Resource keeps the events of incidents whose target it is.
	Resource string
	// Incident keeps one incident's events.
	Incident string
	From, To time.Time // [From, To)
	Kinds    []EventKind
	// Limit is at most MaxEvents (the default). The newest are kept.
	Limit int
}

// Events returns the events q selects, oldest first.
func (s *Store) Events(ctx context.Context, q Query) ([]Event, error) {
	var where []string
	var args []any
	if q.Resource != "" {
		where = append(where, `json_extract(data, '$.target') = ?`)
		args = append(args, q.Resource)
	}
	if q.Incident != "" {
		where = append(where, `incident_id = ?`)
		args = append(args, q.Incident)
	}
	if !q.From.IsZero() {
		where = append(where, `at >= ?`)
		args = append(args, q.From.UnixMilli())
	}
	if !q.To.IsZero() {
		where = append(where, `at < ?`)
		args = append(args, q.To.UnixMilli())
	}
	if len(q.Kinds) > 0 {
		marks := make([]string, len(q.Kinds))
		for i, k := range q.Kinds {
			marks[i] = "?"
			args = append(args, string(k))
		}
		where = append(where, `kind IN (`+strings.Join(marks, ", ")+`)`)
	}
	limit := q.Limit
	if limit <= 0 || limit > MaxEvents {
		limit = MaxEvents
	}
	query := `SELECT id, incident_id, at, kind, data FROM incident_events`
	if len(where) > 0 {
		query += ` WHERE ` + strings.Join(where, ` AND `)
	}
	query += ` ORDER BY at DESC, rowid DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.st.Reader().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("incidents: %w", err)
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var e Event
		var at int64
		var kind, data string
		if err := rows.Scan(&e.ID, &e.IncidentID, &at, &kind, &data); err != nil {
			return nil, fmt.Errorf("incidents: %w", err)
		}
		e.At, e.Kind, e.Data = time.UnixMilli(at).UTC(), EventKind(kind), json.RawMessage(data)
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("incidents: %w", err)
	}
	slices.Reverse(out)
	return out, nil
}

// Data is what every event's data carries about its incident, so the
// timeline can filter and word an event without the incident itself
// (resolved incidents leave the inbox after 24 hours), plus each kind's own
// fields. Fields a kind doesn't use are left out.
type Data struct {
	Target   string `json:"target,omitempty"`
	Class    string `json:"class,omitempty"`
	Category string `json:"category,omitempty"`
	Scenario string `json:"scenario,omitempty"`
	Title    string `json:"title,omitempty"`
	Severity string `json:"severity,omitempty"`
	Phase    string `json:"phase,omitempty"`

	// Changed: which field changed ("severity" or "phase"), from what, to what.
	Field string `json:"field,omitempty"`
	From  string `json:"from,omitempty"`
	To    string `json:"to,omitempty"`
	// Caused: the resource the cause points at, and its text.
	Resource string `json:"resource,omitempty"`
	Text     string `json:"text,omitempty"`
	Rule     string `json:"rule,omitempty"`
	// Expected: the maintenance window it opened in.
	Window string `json:"window,omitempty"`
	// Resolved: "action" or "auto", as the incident says.
	By string `json:"by,omitempty"`
}

// DataOf decodes an event's data; a malformed one reads as empty.
func DataOf(e Event) Data {
	var d Data
	_ = json.Unmarshal(e.Data, &d)
	return d
}
