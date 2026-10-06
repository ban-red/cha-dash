package arr

import (
	"net/url"
	"slices"
	"time"
)

// CalendarItem is one upcoming release, in the Demo Home's shape: the app
// resource's attrs.calendar is a list of {at, title, detail?}, soonest first
// (sim.CalendarItem; the web's CalendarWidget reads it). Unmonitored marks
// a release the *arr isn't monitoring: the widget shows those only on a
// tile whose options say unmonitored: true.
type CalendarItem struct {
	At          time.Time `json:"at"`
	Title       string    `json:"title"`
	Detail      string    `json:"detail,omitempty"`
	Unmonitored bool      `json:"unmonitored,omitempty"`
}

// The calendar's window and size.
const (
	CalendarBack  = 12 * time.Hour     // keep what aired this morning
	CalendarAhead = 7 * 24 * time.Hour // "Up next" covers the week
	CalendarMax   = 30
)

// CalendarQuery is the start and end parameters for now's window, with
// unmonitored releases too (each item says which), so a tile can choose.
func CalendarQuery(now time.Time) url.Values {
	now = now.UTC()
	return url.Values{
		"start":       {now.Add(-CalendarBack).Format(time.RFC3339)},
		"end":         {now.Add(CalendarAhead).Format(time.RFC3339)},
		"unmonitored": {"true"},
	}
}

// Unmonitored reads an upstream's monitored flag: only an explicit false
// is unmonitored.
func Unmonitored(monitored *bool) bool { return monitored != nil && !*monitored }

// Calendar keeps the items inside now's window, soonest first (ties by
// title), at most CalendarMax monitored and CalendarMax unmonitored ones, so
// unmonitored releases never crowd out the monitored. It never returns nil, so an empty calendar
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
	var mon, unmon int
	return slices.DeleteFunc(out, func(it CalendarItem) bool {
		n := &mon
		if it.Unmonitored {
			n = &unmon
		}
		*n++
		return *n > CalendarMax
	})
}
