package history

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"time"
)

// The hub's own statuses, beside the integrations' (ok, attention, down, …).
const (
	// StatusStopped is written at a clean shutdown for every resource:
	// Cha Dash wasn't watching until the next row.
	StatusStopped = "stopped"
	// StatusGone is written when a resource leaves the graph.
	StatusGone = "gone"
)

// keepStates is how long state rows stay; the row in effect at the cutoff
// always stays, so a resource that never changes keeps its start.
func (l Level) keepStates() time.Duration {
	if l == LongTerm {
		return 730 * day
	}
	return 90 * day
}

type stateRow struct {
	resource string
	at       int64
	status   string
}

// stateTrack is a resource's newest row, and the one before it, which
// Since looks at when the newest is a shutdown.
type stateTrack struct {
	last, before stateRow
}

// StateChange is one row: the status from At until the next change.
type StateChange struct {
	At     time.Time
	Status string
}

// loadStates reads every resource's newest two rows.
func (r *Recorder) loadStates(ctx context.Context) error {
	rows, err := r.st.Reader().QueryContext(ctx, `SELECT resource, at, status FROM (
		SELECT resource, at, status, row_number() OVER (PARTITION BY resource ORDER BY at DESC) AS n FROM states
	) WHERE n <= 2 ORDER BY resource, at`)
	if err != nil {
		return fmt.Errorf("history: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var s stateRow
		if err := rows.Scan(&s.resource, &s.at, &s.status); err != nil {
			return fmt.Errorf("history: %w", err)
		}
		t := r.states[s.resource]
		if t == nil {
			t = &stateTrack{}
			r.states[s.resource] = t
		}
		t.before, t.last = t.last, s
	}
	return rows.Err()
}

// Since is when a resource's status began, as far as history knows: the
// start of the newest row when it has that status (looking past a clean
// shutdown), otherwise now. It's how "down since" survives a restart.
func (r *Recorder) Since(resource, status string, now time.Time) time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	t := r.states[resource]
	if t == nil {
		return now
	}
	row := t.last
	if row.status == StatusStopped {
		row = t.before
	}
	if row.status == status {
		return time.UnixMilli(row.at).UTC()
	}
	return now
}

// State records that resource has status, which began at since. Nothing is
// written when that's already its newest status.
func (r *Recorder) State(resource, status string, since time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.top > 0 {
		r.state(resource, status, since.UnixMilli())
	}
}

// Gone records that resource left the graph.
func (r *Recorder) Gone(resource string, at time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if t := r.states[resource]; r.top > 0 && t != nil && t.last.status != StatusGone {
		r.state(resource, StatusGone, at.UnixMilli())
	}
}

func (r *Recorder) state(resource, status string, at int64) {
	t := r.states[resource]
	if t == nil {
		t = &stateTrack{}
		r.states[resource] = t
	}
	if t.last.status == status {
		return
	}
	if t.last.status != "" && at <= t.last.at {
		at = t.last.at + 1 // a row never goes before the one it follows
	}
	row := stateRow{resource: resource, at: at, status: status}
	t.before, t.last = t.last, row
	r.pendingStates = append(r.pendingStates, row)
}

// stopAll marks every resource still in the graph as stopped, at a clean
// shutdown.
func (r *Recorder) stopAll(at int64) {
	for id, t := range r.states {
		if t.last.status != StatusStopped && t.last.status != StatusGone {
			r.state(id, StatusStopped, at)
		}
	}
}

// States is a resource's status changes over [from, to): the row in effect
// at from (moved to from) and every change after it. Empty when history
// knows nothing of it then.
func (r *Recorder) States(ctx context.Context, resource string, from, to time.Time) ([]StateChange, error) {
	lo, hi := from.UnixMilli(), to.UnixMilli()
	rows, err := r.st.Reader().QueryContext(ctx, `SELECT at, status FROM states WHERE resource = ? AND at < ? AND at >= coalesce(
		(SELECT max(at) FROM states WHERE resource = ? AND at <= ?), ?) ORDER BY at`, resource, hi, resource, lo, lo)
	if err != nil {
		return nil, fmt.Errorf("history: %w", err)
	}
	defer rows.Close()
	var list []stateRow
	for rows.Next() {
		s := stateRow{resource: resource}
		if err := rows.Scan(&s.at, &s.status); err != nil {
			return nil, fmt.Errorf("history: %w", err)
		}
		list = append(list, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("history: %w", err)
	}
	// Rows not yet written.
	r.mu.Lock()
	for _, s := range r.pendingStates {
		if s.resource == resource && s.at < hi {
			list = append(list, s)
		}
	}
	r.mu.Unlock()
	slices.SortStableFunc(list, func(a, b stateRow) int { return int(a.at - b.at) })
	list = slices.CompactFunc(list, func(a, b stateRow) bool { return a.at == b.at })
	// Keep only the row in effect at from, and those after it.
	first := 0
	for i, s := range list {
		if s.at <= lo {
			first = i
		}
	}
	var out []StateChange
	for _, s := range list[first:] {
		out = append(out, StateChange{At: time.UnixMilli(max(s.at, lo)).UTC(), Status: s.status})
	}
	return out, nil
}

// Summary is how long a resource spent in each status over a range, and
// the share of the time it was watched that it was up (ok or attention).
// Time before the first row, stopped, gone, unknown and maintenance count
// as unwatched. Up is -1 when none of the range was watched.
type Summary struct {
	Totals map[string]time.Duration
	Up     float64
}

// Summarize totals changes over [from, to).
func Summarize(changes []StateChange, from, to time.Time) Summary {
	s := Summary{Totals: map[string]time.Duration{}, Up: -1}
	var watched, up time.Duration
	for i, c := range changes {
		end := to
		if i+1 < len(changes) {
			end = changes[i+1].At
		}
		start := c.At
		if start.Before(from) {
			start = from
		}
		if !end.After(start) {
			continue
		}
		d := end.Sub(start)
		s.Totals[c.Status] += d
		switch c.Status {
		case StatusStopped, StatusGone, "unknown", "maintenance":
		case "ok", "attention":
			watched += d
			up += d
		default:
			watched += d
		}
	}
	if watched > 0 {
		s.Up = float64(up) / float64(watched)
	}
	return s
}

// retainStates deletes rows older than retention, keeping the one in
// effect at the cutoff, and forgets resources whose newest row is a
// shutdown or a removal from before it.
func retainStates(ctx context.Context, tx *sql.Tx, level Level, ms int64) ([]string, error) {
	cutoff := ms - level.keepStates().Milliseconds()
	if _, err := tx.ExecContext(ctx, `DELETE FROM states WHERE at < ? AND EXISTS (
		SELECT 1 FROM states n WHERE n.resource = states.resource AND n.at > states.at AND n.at <= ?)`, cutoff, cutoff); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `DELETE FROM states WHERE at < ? AND status IN ('stopped', 'gone') AND NOT EXISTS (
		SELECT 1 FROM states n WHERE n.resource = states.resource AND n.at > states.at) RETURNING resource`, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var gone []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		gone = append(gone, id)
	}
	return gone, rows.Err()
}
