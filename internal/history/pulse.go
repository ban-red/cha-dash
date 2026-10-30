package history

import (
	"context"
	"fmt"
	"slices"
	"time"
)

// severity orders the statuses a slot reports: a resource counts as the
// worst it was during the slot. 0 is unwatched (stopped, gone, unknown,
// maintenance), which isn't counted.
func severity(status string) int {
	switch status {
	case "ok":
		return 1
	case "attention":
		return 2
	case "repair":
		return 3
	case "down":
		return 4
	}
	return 0
}

// PulseSlot is the whole home over one slot: how many resources were at
// worst ok, attention, repair or down, and which ones weren't ok.
type PulseSlot struct {
	Counts map[string]int
	Issues map[string]string // resource → its worst status, when not ok
}

// Pulse is the whole home's health over a range on a fixed grid, for the
// Briefing's Pulse strip.
type Pulse struct {
	Start time.Time
	Step  time.Duration
	Slots []PulseSlot
}

// Pulse reads every resource's health over [from, to) in n equal slots.
// keep says which resources count (nil: all).
func (r *Recorder) Pulse(ctx context.Context, from, to time.Time, n int, keep func(resource string) bool) (Pulse, error) {
	if n < 1 || !from.Before(to) {
		return Pulse{}, fmt.Errorf("history: bad pulse range")
	}
	lo, hi := from.UnixMilli(), to.UnixMilli()
	step := (hi - lo + int64(n) - 1) / int64(n)
	out := Pulse{Start: from.UTC(), Step: time.Duration(step) * time.Millisecond, Slots: make([]PulseSlot, n)}
	for i := range out.Slots {
		out.Slots[i] = PulseSlot{Counts: map[string]int{}, Issues: map[string]string{}}
	}
	// Each resource's row in effect at from, then the changes after it.
	rows, err := r.st.Reader().QueryContext(ctx, `SELECT resource, max(at), status FROM states WHERE at <= ? GROUP BY resource
		UNION ALL SELECT resource, at, status FROM states WHERE at > ? AND at < ?`, lo, lo, hi)
	if err != nil {
		return out, fmt.Errorf("history: %w", err)
	}
	defer rows.Close()
	by := map[string][]stateRow{}
	for rows.Next() {
		var s stateRow
		if err := rows.Scan(&s.resource, &s.at, &s.status); err != nil {
			return out, fmt.Errorf("history: %w", err)
		}
		by[s.resource] = append(by[s.resource], s)
	}
	if err := rows.Err(); err != nil {
		return out, fmt.Errorf("history: %w", err)
	}
	r.mu.Lock()
	for _, s := range r.pendingStates {
		if s.at < hi {
			by[s.resource] = append(by[s.resource], s)
		}
	}
	r.mu.Unlock()
	for id, list := range by {
		if keep != nil && !keep(id) {
			continue
		}
		slices.SortStableFunc(list, func(a, b stateRow) int { return int(a.at - b.at) })
		// Rows before from but the last are superseded (pending ones can be).
		first := 0
		for i, s := range list {
			if s.at <= lo {
				first = i
			}
		}
		list = list[first:]
		worst := make([]string, n)
		for i, s := range list {
			end := hi
			if i+1 < len(list) {
				end = list[i+1].at
			}
			a, b := max(s.at, lo), min(end, hi)
			if b <= a || severity(s.status) == 0 {
				continue
			}
			for k := (a - lo) / step; k < int64(n) && lo+k*step < b; k++ {
				if severity(s.status) > severity(worst[k]) {
					worst[k] = s.status
				}
			}
		}
		for k, st := range worst {
			if st == "" {
				continue
			}
			out.Slots[k].Counts[st]++
			if st != "ok" {
				out.Slots[k].Issues[id] = st
			}
		}
	}
	return out, nil
}
