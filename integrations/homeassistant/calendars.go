package homeassistant

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"strings"
	"time"

	"dash.cha.sh/internal/integration"
)

// haCalendar is one item of /api/calendars.
type haCalendar struct {
	EntityID string `json:"entity_id"`
	Name     string `json:"name"`
}

// haEvent is one item of /api/calendars/<entity_id>, with only the fields
// the integration publishes: never the description.
type haEvent struct {
	Start    haWhen `json:"start"`
	End      haWhen `json:"end"`
	Summary  string `json:"summary"`
	Location string `json:"location"`
}

// haWhen is an event's start or end: {"dateTime": …} for a timed event,
// {"date": "YYYY-MM-DD"} for an all-day one. A bare string is read the same
// way, by its length.
type haWhen struct {
	DateTime string `json:"dateTime"`
	Date     string `json:"date"`
}

func (w *haWhen) UnmarshalJSON(b []byte) error {
	if bytes.HasPrefix(bytes.TrimSpace(b), []byte(`"`)) {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		if len(s) == len("2006-01-02") {
			w.Date = s
		} else {
			w.DateTime = s
		}
		return nil
	}
	type plain haWhen
	return json.Unmarshal(b, (*plain)(w))
}

// calendar is a calendar entity as last read: its events are published
// rows, soonest first (nil until they're first read).
type calendar struct {
	id     string // calendar.…
	name   string
	events []map[string]any
}

// slug is the entity ID after "calendar.".
func (c calendar) slug() string { return strings.TrimPrefix(c.id, "calendar.") }

// readCalendars reads the calendar entities and, for each, its events from
// local midnight today to 14 days ahead; Home Assistant expands recurring
// events. It reports whether there's something new to publish. It's best
// effort: a failure keeps what was last read, and one calendar failing
// keeps its last events.
func (in *instance) readCalendars(ctx context.Context, midnight time.Time) (bool, error) {
	var list []haCalendar
	if err := in.getJSON(ctx, "/api/calendars", nil, &list); err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		if errors.Is(err, errMissing) {
			// No calendar integration loaded, or an older Home Assistant.
			in.calendars = nil
			return true, nil
		}
		in.d.Log.Debug("reading calendars failed", "err", err)
		return false, nil
	}
	list = slices.DeleteFunc(list, func(c haCalendar) bool {
		return !strings.HasPrefix(c.EntityID, "calendar.") || len(c.EntityID) == len("calendar.")
	})
	slices.SortFunc(list, func(a, b haCalendar) int { return strings.Compare(a.EntityID, b.EntityID) })
	list = slices.CompactFunc(list, func(a, b haCalendar) bool { return a.EntityID == b.EntityID })
	list = list[:min(len(list), maxCalendars)]

	last := map[string]calendar{}
	for _, c := range in.calendars {
		last[c.id] = c
	}
	end := midnight.AddDate(0, 0, calendarDays)
	q := url.Values{"start": {midnight.UTC().Format(time.RFC3339)}, "end": {end.UTC().Format(time.RFC3339)}}
	cals := make([]calendar, 0, len(list))
	for _, c := range list {
		cal := last[c.EntityID]
		cal.id, cal.name = c.EntityID, c.Name
		if cal.name == "" {
			cal.name = c.EntityID
		}
		var events []haEvent
		if err := in.getJSON(ctx, "/api/calendars/"+c.EntityID, q, &events); err != nil {
			if ctx.Err() != nil {
				return false, ctx.Err()
			}
			in.d.Log.Debug("reading a calendar's events failed", "calendar", c.EntityID, "err", err)
		} else {
			cal.events = in.eventRows(events, midnight, end)
		}
		cals = append(cals, cal)
	}
	in.calendars = cals
	return true, nil
}

// eventRows turns events into published rows: in the window, soonest first,
// at most maxEvents. Timed events are RFC 3339 in Home Assistant's time
// zone; all-day ones are dates with an exclusive end.
func (in *instance) eventRows(events []haEvent, from, to time.Time) []map[string]any {
	type row struct {
		start, end time.Time
		m          map[string]any
	}
	rows := make([]row, 0, len(events))
	for _, e := range events {
		var r row
		if e.Start.Date != "" {
			s, err := time.ParseInLocation(time.DateOnly, e.Start.Date, in.tz)
			if err != nil {
				continue
			}
			en, err := time.ParseInLocation(time.DateOnly, e.End.Date, in.tz)
			if err != nil || !en.After(s) {
				en = s.AddDate(0, 0, 1)
			}
			r = row{start: s, end: en, m: map[string]any{"start": s.Format(time.DateOnly), "end": en.Format(time.DateOnly), "all_day": true}}
		} else {
			s, err := time.Parse(time.RFC3339, e.Start.DateTime)
			if err != nil {
				continue
			}
			en, err := time.Parse(time.RFC3339, e.End.DateTime)
			if err != nil || en.Before(s) {
				en = s
			}
			s, en = s.In(in.tz), en.In(in.tz)
			r = row{start: s, end: en, m: map[string]any{"start": s.Format(time.RFC3339), "end": en.Format(time.RFC3339), "all_day": false}}
		}
		// Home Assistant answers the events that overlap the window; an
		// instant event at midnight is still today's.
		if !r.start.Before(to) || r.end.Before(from) || r.end.Equal(from) && r.start.Before(from) {
			continue
		}
		r.m["title"] = e.Summary
		if e.Location != "" {
			r.m["location"] = e.Location
		}
		rows = append(rows, r)
	}
	slices.SortStableFunc(rows, func(a, b row) int {
		return cmp.Or(a.start.Compare(b.start), a.end.Compare(b.end))
	})
	out := make([]map[string]any, 0, min(len(rows), maxEvents))
	for _, r := range rows[:min(len(rows), maxEvents)] {
		out = append(out, r.m)
	}
	return out
}

// calendarFacts is one resource per calendar entity.
func (in *instance) calendarFacts() integration.Facts {
	var f integration.Facts
	self := in.d.Emit.Self()
	for _, c := range in.calendars {
		id := ID + ":" + in.key + "/calendar/" + c.slug()
		events := c.events
		if events == nil {
			events = []map[string]any{}
		}
		f.Resources = append(f.Resources, integration.Resource{
			ID: id, Kind: "calendar", Name: c.name, State: "online", Health: integration.Health{Status: integration.HealthOK},
			Attrs: map[string]any{"type": "calendar", "provider": ID, "time_zone": in.tz.String(), "events": events},
			Caps:  []string{"calendar.events"},
		})
		f.Relations = append(f.Relations,
			integration.Relation{From: in.appID, Type: "exposes", To: id},
			integration.Relation{From: self, Type: "monitors", To: id})
	}
	return f
}
