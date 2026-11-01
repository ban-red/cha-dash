package updates

import (
	"context"
	crand "crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/msg"
)

// Maintenance windows (docs/plan/14-v0.4.md, "Maintenance windows"): a time
// range, once or weekly, for a set of hosts. A rollout scheduled into one
// runs when it opens. While one is open, incidents on its hosts are
// expected: they don't notify, and one still open when it closes notifies
// then (Expected, which internal/notify asks).

// week is how far apart a weekly window's openings are.
const week = 7 * 24 * time.Hour

// Window limits.
const (
	MaxWindowName  = 80
	MaxWindowHosts = 100
	// MaxWindows is how many windows a hub keeps.
	MaxWindows = 50
)

// Occurrence is the opening of w that's open at t or opens next after it:
// its start and end. ok is false when w has none left (a one-off window
// that has ended).
func (w MaintenanceWindow) Occurrence(t time.Time) (start, end time.Time, ok bool) {
	if !w.Weekly {
		return w.StartsAt, w.EndsAt, t.Before(w.EndsAt)
	}
	if t.Before(w.EndsAt) {
		return w.StartsAt, w.EndsAt, true
	}
	k := t.Sub(w.StartsAt) / week
	start, end = w.StartsAt.Add(k*week), w.EndsAt.Add(k*week)
	if !t.Before(end) {
		start, end = start.Add(week), end.Add(week)
	}
	return start, end, true
}

// Open reports whether w is open at t.
func (w MaintenanceWindow) Open(t time.Time) bool {
	start, end, ok := w.Occurrence(t)
	return ok && !t.Before(start) && t.Before(end)
}

// WindowInput is what POST and PUT /api/updates/windows take.
type WindowInput struct {
	Name     string    `json:"name"`
	Hosts    []string  `json:"hosts"`
	StartsAt time.Time `json:"starts_at"`
	EndsAt   time.Time `json:"ends_at"`
	Weekly   bool      `json:"weekly"`
}

// InvalidError is a refused input, with the sentence to show.
type InvalidError struct{ M msg.M }

func (e *InvalidError) Error() string { return e.M.Text() }

func invalid(m msg.M) error { return &InvalidError{M: m} }

// check validates in, as the table's CHECKs would, with sentences.
func (in *WindowInput) check() error {
	in.Name = strings.TrimSpace(in.Name)
	switch {
	case in.Name == "" || utf8.RuneCountInString(in.Name) > MaxWindowName:
		return invalid(msg.New("updates.window_name", "max", MaxWindowName))
	case len(in.Hosts) == 0 || len(in.Hosts) > MaxWindowHosts:
		return invalid(msg.New("updates.window_hosts", "max", MaxWindowHosts))
	case in.StartsAt.IsZero() || in.EndsAt.IsZero() || !in.EndsAt.After(in.StartsAt):
		return invalid(msg.New("updates.window_times"))
	case in.Weekly && in.EndsAt.Sub(in.StartsAt) > MaxWeeklyWindow:
		return invalid(msg.New("updates.window_weekly"))
	}
	seen := map[string]bool{}
	hosts := in.Hosts[:0]
	for _, h := range in.Hosts {
		h = strings.TrimSpace(h)
		if h == "" || len(h) > 500 {
			return invalid(msg.New("updates.window_hosts", "max", MaxWindowHosts))
		}
		if !seen[h] {
			seen[h] = true
			hosts = append(hosts, h)
		}
	}
	slices.Sort(hosts)
	in.Hosts = hosts
	in.StartsAt, in.EndsAt = in.StartsAt.UTC().Truncate(time.Minute), in.EndsAt.UTC().Truncate(time.Minute)
	if !in.EndsAt.After(in.StartsAt) {
		return invalid(msg.New("updates.window_times"))
	}
	return nil
}

// ErrNoWindow is a window that doesn't exist.
var ErrNoWindow = errors.New("updates: no such window")

// Windows lists the windows, soonest first.
func (s *Service) Windows(ctx context.Context) ([]MaintenanceWindow, error) {
	rows, err := s.st.Reader().QueryContext(ctx, `SELECT w.id, w.name, w.hosts, w.starts_at, w.ends_at, w.weekly, COALESCE(u.username, ''), w.created_at
		FROM maintenance_windows w LEFT JOIN users u ON u.id = w.created_by ORDER BY w.starts_at, w.id`)
	if err != nil {
		return nil, fmt.Errorf("updates: read windows: %w", err)
	}
	defer rows.Close()
	out := []MaintenanceWindow{}
	for rows.Next() {
		var w MaintenanceWindow
		var hosts string
		var start, end, created int64
		var weekly int
		if err := rows.Scan(&w.ID, &w.Name, &hosts, &start, &end, &weekly, &w.CreatedBy, &created); err != nil {
			return nil, fmt.Errorf("updates: read windows: %w", err)
		}
		_ = json.Unmarshal([]byte(hosts), &w.Hosts)
		w.StartsAt, w.EndsAt, w.CreatedAt = time.UnixMilli(start).UTC(), time.UnixMilli(end).UTC(), time.UnixMilli(created).UTC()
		w.Weekly = weekly == 1
		out = append(out, w)
	}
	return out, rows.Err()
}

// Window reads one window.
func (s *Service) Window(ctx context.Context, id string) (MaintenanceWindow, error) {
	ws, err := s.Windows(ctx)
	if err != nil {
		return MaintenanceWindow{}, err
	}
	for _, w := range ws {
		if w.ID == id {
			return w, nil
		}
	}
	return MaintenanceWindow{}, ErrNoWindow
}

// CreateWindow adds a window. userID may be empty.
func (s *Service) CreateWindow(ctx context.Context, in WindowInput, userID string) (MaintenanceWindow, error) {
	if err := in.check(); err != nil {
		return MaintenanceWindow{}, err
	}
	var n int
	if err := s.st.Reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM maintenance_windows`).Scan(&n); err != nil {
		return MaintenanceWindow{}, err
	}
	if n >= MaxWindows {
		return MaintenanceWindow{}, invalid(msg.New("updates.window_too_many", "max", MaxWindows))
	}
	id := "mw_" + strings.ToLower(crand.Text())
	hosts, _ := json.Marshal(in.Hosts)
	var by any
	if userID != "" {
		by = userID
	}
	_, err := s.st.Writer().ExecContext(ctx, `INSERT INTO maintenance_windows (id, name, hosts, starts_at, ends_at, weekly, created_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, id, in.Name, string(hosts), in.StartsAt.UnixMilli(), in.EndsAt.UnixMilli(), boolInt(in.Weekly), by, s.now().UnixMilli())
	if err != nil {
		return MaintenanceWindow{}, fmt.Errorf("updates: create window: %w", err)
	}
	s.loadWindows(ctx)
	return s.Window(ctx, id)
}

// UpdateWindow changes a window. Rollouts scheduled into it move with it.
func (s *Service) UpdateWindow(ctx context.Context, id string, in WindowInput) (MaintenanceWindow, error) {
	if err := in.check(); err != nil {
		return MaintenanceWindow{}, err
	}
	hosts, _ := json.Marshal(in.Hosts)
	res, err := s.st.Writer().ExecContext(ctx, `UPDATE maintenance_windows SET name = ?, hosts = ?, starts_at = ?, ends_at = ?, weekly = ? WHERE id = ?`,
		in.Name, string(hosts), in.StartsAt.UnixMilli(), in.EndsAt.UnixMilli(), boolInt(in.Weekly), id)
	if err != nil {
		return MaintenanceWindow{}, fmt.Errorf("updates: update window: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return MaintenanceWindow{}, ErrNoWindow
	}
	s.loadWindows(ctx)
	w, err := s.Window(ctx, id)
	if err != nil {
		return w, err
	}
	// Its scheduled rollouts run at its (new) next opening.
	if start, _, ok := w.Occurrence(s.now()); ok {
		_, err = s.st.Writer().ExecContext(ctx, `UPDATE rollouts SET scheduled_for = ? WHERE window_id = ? AND state = 'scheduled'`, start.UnixMilli(), id)
	}
	return w, err
}

// DeleteWindow removes a window. Rollouts scheduled into it are cancelled:
// they were meant for it.
func (s *Service) DeleteWindow(ctx context.Context, id string) error {
	err := s.st.WriteTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE rollouts SET state = 'cancelled', finished_at = ? WHERE window_id = ? AND state IN ('scheduled', 'waiting')`,
			s.now().UnixMilli(), id); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM maintenance_windows WHERE id = ?`, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNoWindow
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.loadWindows(ctx)
	return nil
}

// loadWindows refreshes the windows Expected reads.
func (s *Service) loadWindows(ctx context.Context) {
	ws, err := s.Windows(ctx)
	if err != nil {
		s.log.Warn("updates: read windows", "err", err)
		return
	}
	s.winMu.Lock()
	s.windows = ws
	s.winMu.Unlock()
}

// Expected says whether an incident on target that opens at t is
// expected: a maintenance window is open at t for a host target is, runs on
// or is part of (or anything same_as those). It names the window. Notify
// asks it so an expected incident doesn't notify while the window is open;
// the incident timeline records it as expected.
func (s *Service) Expected(target string, t time.Time) (string, bool) {
	s.winMu.RLock()
	var open []MaintenanceWindow
	for _, w := range s.windows {
		if w.Open(t) {
			open = append(open, w)
		}
	}
	s.winMu.RUnlock()
	if len(open) == 0 || target == "" {
		return "", false
	}
	_, doc := s.g.Snapshot(nil)
	under := hostsUnder(doc, graph.RID(target))
	for _, w := range open {
		for _, h := range w.Hosts {
			if under[graph.RID(h)] {
				return w.ID, true
			}
		}
	}
	return "", false
}

// ExpectedNow is Expected at t, without the window: internal/notify's
// Options.Expected.
func (s *Service) ExpectedNow(target string, t time.Time) bool {
	_, ok := s.Expected(target, t)
	return ok
}

// hostsUnder is target and everything it runs on or is part of,
// transitively, with whatever is same_as any of them.
func hostsUnder(doc graph.Doc, target graph.RID) map[graph.RID]bool {
	canon := aliases(doc)
	seen := map[graph.RID]bool{}
	queue := []graph.RID{target}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if seen[id] {
			continue
		}
		seen[id] = true
		for _, rel := range doc.Relations {
			if (rel.Type == "runs_on" || rel.Type == "part_of") && rel.From == id && !seen[rel.To] {
				queue = append(queue, rel.To)
			}
		}
		c := canon(id)
		for rid := range doc.Resources {
			if !seen[rid] && canon(rid) == c {
				queue = append(queue, rid)
			}
		}
	}
	return seen
}

// covers reports whether w's hosts cover target.
func (w MaintenanceWindow) covers(doc graph.Doc, target string) bool {
	under := hostsUnder(doc, graph.RID(target))
	for _, h := range w.Hosts {
		if under[graph.RID(h)] {
			return true
		}
	}
	return false
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
