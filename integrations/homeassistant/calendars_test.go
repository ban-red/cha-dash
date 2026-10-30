package homeassistant

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/integration/integrationtest"
)

func TestCalendars(t *testing.T) {
	f := newFake(t)
	h := integrationtest.New(t)
	inst := connect(t, h, f, nil)
	mustSync(t, inst, integrationtest.Watched)

	// One request per calendar, from local midnight (Europe/Berlin: 22:00
	// UTC the day before) to 14 days ahead.
	reqs := f.eventRequests()
	want := []string{
		"/api/calendars/calendar.family?end=2026-10-16T22%3A00%3A00Z&start=2026-10-02T22%3A00%3A00Z",
		"/api/calendars/calendar.holidays?end=2026-10-16T22%3A00%3A00Z&start=2026-10-02T22%3A00%3A00Z",
	}
	if strings.Join(reqs, " ") != strings.Join(want, " ") {
		t.Fatalf("event requests = %v", reqs)
	}

	cal, ok := h.Resource("homeassistant:i_test/calendar/family")
	if !ok || cal.Kind != "calendar" || cal.Name != "Family" || cal.State != "online" || cal.Source != "integration:i_test" {
		t.Fatalf("calendar = %+v", cal)
	}
	for k, want := range map[string]any{"type": "calendar", "provider": "homeassistant", "time_zone": "Europe/Berlin"} {
		if cal.Attrs[k] != want {
			t.Errorf("attrs.%s = %v, want %v", k, cal.Attrs[k], want)
		}
	}
	if _, ok := cal.Attrs["calendar"]; ok {
		t.Error("attrs.calendar is media releases on the web; events go in attrs.events")
	}
	// Soonest first; an event still running at midnight counts, one that
	// ended before it and one after the window don't. Timed events are in
	// Home Assistant's zone, all-day ones are dates with an exclusive end,
	// and descriptions are never published.
	got, _ := json.Marshal(cal.Attrs["events"])
	wantEvents := `[` +
		`{"all_day":false,"end":"2026-10-03T01:00:00+02:00","start":"2026-10-02T23:00:00+02:00","title":"Late film night"},` +
		`{"all_day":false,"end":"2026-10-05T09:00:00+02:00","start":"2026-10-05T08:30:00+02:00","title":"School run"},` +
		`{"all_day":false,"end":"2026-10-07T09:45:00+02:00","location":"Example Street 1, Townsville","start":"2026-10-07T09:00:00+02:00","title":"Dentist"},` +
		`{"all_day":true,"end":"2026-10-12","start":"2026-10-10","title":"Trip to the coast"}]`
	if string(got) != wantEvents {
		t.Errorf("events = %s\nwant     %s", got, wantEvents)
	}
	if strings.Contains(string(got), "insurance") || strings.Contains(string(got), "bikes") {
		t.Error("a description was published")
	}
	hol, _ := h.Resource("homeassistant:i_test/calendar/holidays")
	if got, _ := json.Marshal(hol.Attrs["events"]); hol.Name != "Public holidays" || string(got) != `[{"all_day":true,"end":"2026-10-04","start":"2026-10-03","title":"Public holiday"}]` {
		t.Errorf("holidays = %s %s", hol.Name, got)
	}
	g := h.Graph()
	for _, rel := range []string{
		"integration:i_test monitors homeassistant:i_test/calendar/family",
		"homeassistant:i_test/app exposes homeassistant:i_test/calendar/family",
		"homeassistant:i_test/app exposes homeassistant:i_test/calendar/holidays",
	} {
		if g.Relations[rel] == nil {
			t.Errorf("no relation %s", rel)
		}
	}
	if se := h.Series("homeassistant:i_test/calendar/family", "events"); se != nil {
		t.Error("calendars have no metrics")
	}
}

func TestCalendarsThrottle(t *testing.T) {
	f := newFake(t)
	h := integrationtest.New(t)
	inst := connect(t, h, f, nil)
	mustSync(t, inst, integrationtest.Watched)
	// Watched syncs read the states every minute, the calendars every 5.
	for range 4 {
		h.Advance(time.Minute)
		mustSync(t, inst, integrationtest.Watched)
	}
	if n := f.count("/api/calendars"); n != 1 {
		t.Fatalf("calendars read %d times in 4 minutes", n)
	}
	h.Advance(time.Minute)
	mustSync(t, inst, integrationtest.Watched)
	if n := f.count("/api/calendars"); n != 2 || len(f.eventRequests()) != 4 {
		t.Fatalf("after 5 minutes: calendars %d, events %d", n, len(f.eventRequests()))
	}
	// Idle, they follow the states: every 30 minutes.
	h.Advance(10 * time.Minute)
	mustSync(t, inst, integration.Interest{})
	if n := f.count("/api/calendars"); n != 2 {
		t.Fatalf("an idle sync between state reads read the calendars (%d)", n)
	}
	h.Advance(20 * time.Minute)
	mustSync(t, inst, integration.Interest{})
	if n := f.count("/api/calendars"); n != 3 {
		t.Fatalf("idle: calendars read %d times", n)
	}
}

func TestCalendarFailures(t *testing.T) {
	f := newFake(t)
	h := integrationtest.New(t)
	inst := connect(t, h, f, nil)
	mustSync(t, inst, integrationtest.Watched)
	before, _ := h.Resource("homeassistant:i_test/calendar/family")

	// One calendar failing keeps its last events; the others refresh.
	f.set("/api/calendars/calendar.family", []byte(`{"not": "a list"}`))
	f.set("/api/calendars/calendar.holidays", []byte(`[]`))
	h.Advance(5 * time.Minute)
	mustSync(t, inst, integrationtest.Watched)
	after, ok := h.Resource("homeassistant:i_test/calendar/family")
	a, _ := json.Marshal(after.Attrs["events"])
	b, _ := json.Marshal(before.Attrs["events"])
	if !ok || string(a) != string(b) {
		t.Fatalf("a failing calendar's events = %s, want %s", a, b)
	}
	if hol, _ := h.Resource("homeassistant:i_test/calendar/holidays"); fmt.Sprint(hol.Attrs["events"]) != "[]" {
		t.Fatalf("holidays = %v", hol.Attrs["events"])
	}
	if !strings.Contains(h.Logs(), "calendar.family") {
		t.Error("the failure isn't logged")
	}

	// A calendar that's gone is gone.
	f.set("/api/calendars", []byte(`[{"entity_id": "calendar.holidays", "name": "Public holidays"}]`))
	h.Advance(5 * time.Minute)
	mustSync(t, inst, integrationtest.Watched)
	if _, ok := h.Resource("homeassistant:i_test/calendar/family"); ok {
		t.Fatal("a removed calendar is still published")
	}

	// No calendar API (404): none, and the sync is fine.
	f.set("/api/calendars", nil)
	h.Advance(5 * time.Minute)
	mustSync(t, inst, integrationtest.Watched)
	if _, ok := h.Resource("homeassistant:i_test/calendar/holidays"); ok {
		t.Fatal("calendars published without /api/calendars")
	}
	if app, _ := h.Resource("homeassistant:i_test/app"); app.Health != "ok" {
		t.Fatalf("app = %+v", app)
	}
}

func TestCalendarsNone(t *testing.T) {
	f := newFake(t)
	f.set("/api/calendars", nil)
	h := integrationtest.New(t)
	mustSync(t, connect(t, h, f, nil), integrationtest.Watched)
	for id := range h.Graph().Resources {
		if strings.Contains(string(id), "/calendar/") {
			t.Fatalf("published %s", id)
		}
	}
	if len(f.eventRequests()) != 0 {
		t.Fatal("asked for events without calendars")
	}
}

func TestCalendarsCap(t *testing.T) {
	f := newFake(t)
	var list []map[string]string
	for i := range 25 {
		list = append(list, map[string]string{"entity_id": fmt.Sprintf("calendar.room_%02d", i), "name": fmt.Sprintf("Room %d", i)})
	}
	b, _ := json.Marshal(list)
	f.set("/api/calendars", b)
	h := integrationtest.New(t)
	mustSync(t, connect(t, h, f, nil), integrationtest.Watched)
	if n := len(f.eventRequests()); n != 20 {
		t.Fatalf("%d event requests, want 20", n)
	}
	n := 0
	for id := range h.Graph().Resources {
		if strings.Contains(string(id), "/calendar/") {
			n++
		}
	}
	if n != 20 {
		t.Fatalf("%d calendars, want 20", n)
	}
	if _, ok := h.Resource("homeassistant:i_test/calendar/room_19"); !ok {
		t.Fatal("the first 20 by entity ID are kept")
	}
}

func TestCalendarEventCap(t *testing.T) {
	f := newFake(t)
	var events []map[string]any
	for i := range 70 {
		day := time.Date(2026, 10, 4+i/10, 8+i%10, 0, 0, 0, time.UTC)
		events = append(events, map[string]any{
			"start": map[string]string{"dateTime": day.Format(time.RFC3339)}, "end": map[string]string{"dateTime": day.Add(30 * time.Minute).Format(time.RFC3339)},
			"summary": fmt.Sprintf("Slot %d", 69-i), // reversed, to check the sort
		})
	}
	for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
		events[i], events[j] = events[j], events[i]
	}
	b, _ := json.Marshal(events)
	f.set("/api/calendars/calendar.family", b)
	h := integrationtest.New(t)
	mustSync(t, connect(t, h, f, nil), integrationtest.Watched)
	cal, _ := h.Resource("homeassistant:i_test/calendar/family")
	got := cal.Attrs["events"].([]any)
	if len(got) != 60 || got[0].(map[string]any)["title"] != "Slot 69" || got[59].(map[string]any)["title"] != "Slot 10" {
		t.Fatalf("%d events, first %v, last %v", len(got), got[0], got[len(got)-1])
	}
}
