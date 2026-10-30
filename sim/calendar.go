package sim

import (
	"slices"
	"strings"
	"time"
)

// The family's calendars, as Home Assistant publishes them
// (integrations/homeassistant/README.md): a resource per calendar, kind
// calendar, with its events from the start's midnight to two weeks ahead.
// The first Saturday matches the day's story (football 09:30–12:00, the big
// shop 10:30–12:45, in UTC like the rest of it). Everything is invented.

// calEvent is one event: a timed one starts and ends at offsets from the
// start; an all-day one names its first day and how many days it runs.
type calEvent struct {
	title, location string
	from, to        time.Duration // timed
	day, days       int           // all-day: the first day (0 = the start's) and how many
}

func timed(title, location string, day int, from, to string) calEvent {
	at := func(hm string) time.Duration {
		t, err := time.Parse("15:04", hm)
		if err != nil {
			panic("sim: bad time " + hm)
		}
		return time.Duration(day)*24*time.Hour + time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute
	}
	return calEvent{title: title, location: location, from: at(from), to: at(to)}
}

func allDay(title string, day, days int) calEvent {
	return calEvent{title: title, day: day, days: days}
}

var calendarTable = []struct {
	slug, name string
	events     []calEvent
}{
	{"family", "Family", []calEvent{
		timed("Football", "Riverside pitches", 0, "09:30", "12:00"),
		timed("Big shop", "", 0, "10:30", "12:45"),
		timed("Dinner next door", "", 0, "18:30", "21:00"),
		allDay("Grandad's birthday", 1, 1),
		allDay("Bin day", 3, 1),
		timed("Swimming lessons", "Leisure centre", 6, "17:00", "18:00"),
		timed("Football", "Riverside pitches", 7, "09:30", "12:00"),
		allDay("Bin day", 10, 1),
		timed("Dentist", "High Street surgery", 9, "15:30", "16:15"),
		timed("Swimming lessons", "Leisure centre", 13, "17:00", "18:00"),
	}},
	{"school", "School", []calEvent{
		timed("Parents' evening", "", 4, "18:00", "19:30"),
		allDay("Half-term", 5, 3),
		timed("Book fair", "School hall", 11, "15:15", "16:30"),
	}},
}

func (b *builder) calendars() {
	for _, c := range calendarTable {
		id := "homeassistant:calendar/" + c.slug
		events := make([]map[string]any, 0, len(c.events))
		for _, e := range c.events {
			ev := map[string]any{"title": e.title}
			if e.days > 0 {
				first := b.start.AddDate(0, 0, e.day)
				ev["start"], ev["end"], ev["all_day"] = first.Format(time.DateOnly), first.AddDate(0, 0, e.days).Format(time.DateOnly), true
			} else {
				ev["start"], ev["end"], ev["all_day"] = b.start.Add(e.from).UTC().Format(time.RFC3339), b.start.Add(e.to).UTC().Format(time.RFC3339), false
			}
			if e.location != "" {
				ev["location"] = e.location
			}
			events = append(events, ev)
		}
		sortEvents(events)
		b.add(&Resource{ID: id, Kind: KindCalendar, Name: c.name, Source: "integration:homeassistant", State: StateOnline,
			Labels: map[string]string{"chadash.group": "Home"},
			Attrs:  map[string]any{"type": "calendar", "provider": "homeassistant", "time_zone": "UTC", "events": events},
			Caps:   []string{"calendar.events"}})
		b.rel("app:homeassistant", RelExposes, id)
	}
}

// sortEvents puts events soonest first, an all-day one before a timed one
// on its day (the published order): "2026-10-04T" sorts before any time
// that day.
func sortEvents(events []map[string]any) {
	key := func(e map[string]any) string {
		if e["all_day"] == true {
			return e["start"].(string) + "T"
		}
		return e["start"].(string)
	}
	slices.SortStableFunc(events, func(a, b map[string]any) int { return strings.Compare(key(a), key(b)) })
}
