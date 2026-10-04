package arr

import (
	"net/url"
	"slices"
	"time"
)

// CalendarItem is one upcoming release, in the Demo Home's shape: the app
// resource's attrs.calendar is a list of {at, title, detail?}, soonest first
// (sim.CalendarItem; the web's CalendarWidget reads it).
type CalendarItem struct {
	At     time.Time `json:"at"`
	Title  string    `json:"title"`
	Detail string    `json:"detail,omitempty"`
}

// The calendar's window and size.
const (
	CalendarBack  = 12 * time.Hour     // keep what aired this morning
	CalendarAhead = 7 * 24 * time.Hour // "Up next" covers the week
	CalendarMax   = 30
)

// CalendarQuery is the start and end parameters for now's window.
func CalendarQuery(now time.Time) url.Values {
	now = now.UTC()
	return url.Values{
		"start": {now.Add(-CalendarBack).Format(time.RFC3339)},
		"end":   {now.Add(CalendarAhead).Format(time.RFC3339)},
	}
}

// Calendar keeps the items inside now's window, soonest first (ties by
// title), at most CalendarMax. It never returns nil, so an empty calendar
// publishes as [] and the widget says "Nothing scheduled".
func Calendar(items []CalendarItem, now time.Time) []CalendarItem {
	from, to := now.Add(-CalendarBack), now.Add(CalendarAhead)
	out := []CalendarItem{}
	for _, it := range items {
		if it.Title == "" || it.At.Before(from) || it.At.After(to) {
			continue
		}
		it.At = it.At.UTC()
		out = append(out, it)
	}
	slices.SortStableFunc(out, func(a, b CalendarItem) int {
		if c := a.At.Compare(b.At); c != 0 {
			return c
		}
		switch {
		case a.Title < b.Title:
			return -1
		case a.Title > b.Title:
			return 1
		}
		return 0
	})
	if len(out) > CalendarMax {
		out = out[:CalendarMax]
	}
	return out
}
