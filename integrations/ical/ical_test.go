package ical

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	goical "github.com/emersion/go-ical"

	"dash.cha.sh/integrations/internal/fixture"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/integration/integrationtest"
)

// The feed's path carries a token, like Google's secret address. It must
// never leave the instance.
const (
	token = "private-0123456789abcdef0123456789abcdef"
	path  = "/calendar/ical/owner.group.example/" + token + "/basic.ics"
)

func server(t *testing.T, file string) *fixture.Server {
	t.Helper()
	return fixture.New(t, fixture.Dir(t, ID), nil, map[string]fixture.Route{
		"GET " + path: {File: file, ContentType: "text/calendar; charset=UTF-8"},
	})
}

func connect(t *testing.T, h *integrationtest.Harness, feed string, cfg map[string]any) integration.Instance {
	t.Helper()
	return h.Connect(Integration{}, cfg, map[string]string{"feed_url": feed})
}

// events reads attrs.events back as plain maps.
func events(t *testing.T, h *integrationtest.Harness) []map[string]any {
	t.Helper()
	r, ok := h.Resource("ical:i_test/calendar/main")
	if !ok {
		t.Fatal("no calendar resource")
	}
	list, _ := r.Attrs["events"].([]any)
	out := make([]map[string]any, 0, len(list))
	for _, e := range list {
		out = append(out, e.(map[string]any))
	}
	return out
}

// noZoneDatabase makes the parser work as in the hub's image, which has no
// zone database: every zone but UTC comes from the feed's VTIMEZONEs.
func noZoneDatabase(t *testing.T) {
	t.Cleanup(func() { loadLocation = time.LoadLocation })
	loadLocation = func(name string) (*time.Location, error) {
		if name == "UTC" {
			return time.UTC, nil
		}
		return nil, errors.New("no zone database")
	}
}

func TestValidate(t *testing.T) {
	feed := map[string]string{"feed_url": "https://calendar.example" + path}
	for _, c := range []struct {
		cfg     map[string]any
		secrets map[string]string
	}{
		{nil, feed},
		{map[string]any{"name": "Family", "tls": map[string]any{"mode": "tofu"}}, feed},
		{nil, map[string]string{"feed_url": "webcal://p01-caldav.icloud.example/published/2/" + token}},
		{nil, map[string]string{"feed_url": "http://nextcloud.lab.example/remote.php/dav/public-calendars/" + token + "?export"}},
	} {
		if errs := integrationtest.Validate(Integration{}, c.cfg, c.secrets); len(errs) != 0 {
			t.Errorf("%v: %v", c.cfg, errs)
		}
	}
	for want, c := range map[string]struct {
		cfg     map[string]any
		secrets map[string]string
	}{
		"/feed_url": {nil, map[string]string{"feed_url": "ftp://calendar.example" + path}},
		"/name":     {map[string]any{"name": strings.Repeat("x", 65)}, feed},
		"":          {nil, nil}, // feed_url is required
	} {
		errs := integrationtest.Validate(Integration{}, c.cfg, c.secrets)
		if len(errs) != 1 || (want != "" && errs[0].Path != want) {
			t.Errorf("%s: %v", want, errs)
			continue
		}
		if strings.Contains(errs[0].Message, token) || strings.Contains(errs[0].Message, "calendar.example") {
			t.Errorf("%s: the message repeats the address: %s", want, errs[0].Message)
		}
	}
	_, m, ok := integration.Default.Get(ID)
	if !ok || !reflect.DeepEqual(m.SecretFields(), []string{"feed_url"}) || m.Poll.Interval != 15*time.Minute || m.Poll.Idle != time.Hour ||
		m.Poll.Timeout != 30*time.Second || m.Category != integration.CatHome || m.Tier != integration.Bronze ||
		!reflect.DeepEqual(m.Capabilities, []integration.Capability{"calendar.events"}) {
		t.Fatalf("manifest = %+v", m)
	}
}

func TestFeedURL(t *testing.T) {
	for in, want := range map[string]string{
		"webcal://calendar.example/a.ics":    "https://calendar.example/a.ics",
		"webcals://calendar.example/a.ics":   "https://calendar.example/a.ics",
		" WEBCAL://calendar.example/a.ics ":  "https://calendar.example/a.ics",
		"https://calendar.example/a.ics#top": "https://calendar.example/a.ics",
		"http://cal.lab.example:8080/x?y=1":  "http://cal.lab.example:8080/x?y=1",
	} {
		u, err := feedURL(in)
		if err != nil || u.String() != want {
			t.Errorf("%q = %v, %v; want %s", in, u, err, want)
		}
	}
	for _, in := range []string{"", "calendar.example/a.ics", "file:///etc/passwd", "https:///a.ics"} {
		if _, err := feedURL(in); err == nil {
			t.Errorf("%q: no error", in)
		}
	}
}

// The Google-style feed, in Europe/London (UTC+1 in the window): a weekly
// event with a moved instance, a cancelled one and an EXDATE; a weekly
// event that ends by a UTC UNTIL; all-day, multi-day and yearly all-day
// events; one in UTC, one in New York, one floating; and the events outside
// the window or cancelled, which don't appear.
var googleEvents = []map[string]any{
	{"start": "2026-10-01", "end": "2026-10-04", "all_day": true, "title": "Conference", "location": "Exhibition centre"},
	{"start": "2026-10-03T19:00:00+01:00", "end": "2026-10-03T22:00:00+01:00", "all_day": false, "title": "Film night", "location": "Community hall, Main Street"},
	{"start": "2026-10-04T15:00:00+01:00", "end": "2026-10-04T16:00:00+01:00", "all_day": false, "title": "Video call"},
	{"start": "2026-10-06T11:00:00+01:00", "end": "2026-10-06T11:30:00+01:00", "all_day": false, "title": "Weekly sync (moved)"},
	{"start": "2026-10-07T19:00:00+01:00", "end": "2026-10-07T20:30:00+01:00", "all_day": false, "title": "Evening class", "location": "Room 4, Learning Centre"},
	{"start": "2026-10-09", "end": "2026-10-10", "all_day": true, "title": "Recycling collection"},
	{"start": "2026-10-10", "end": "2026-10-13", "all_day": true, "title": "Trip to the coast"},
	{"start": "2026-10-11", "end": "2026-10-12", "all_day": true, "title": "Birthday"},
	{"start": "2026-10-12T09:30:00+01:00", "end": "2026-10-12T10:00:00+01:00", "all_day": false, "title": "Weekly sync"},
	{"start": "2026-10-16T08:00:00+01:00", "end": "2026-10-16T09:00:00+01:00", "all_day": false, "title": "Breakfast meeting"},
}

// The Outlook-style feed: Windows zone names, its first VTIMEZONE making it
// Europe/Berlin; a weekly event with an EXDATE, an all-day event, and one in
// a zone nobody knows, read as UTC.
var outlookEvents = []map[string]any{
	{"start": "2026-10-05T14:00:00+02:00", "end": "2026-10-05T15:00:00+02:00", "all_day": false, "title": "Project review", "location": "Meeting room 2"},
	{"start": "2026-10-06T18:00:00+02:00", "end": "2026-10-06T18:30:00+02:00", "all_day": false, "title": "Call with the west coast office"},
	{"start": "2026-10-08", "end": "2026-10-09", "all_day": true, "title": "Office closed"},
	{"start": "2026-10-09T14:00:00+02:00", "end": "2026-10-09T15:00:00+02:00", "all_day": false, "title": "Lunch, then walk"},
	{"start": "2026-10-14T10:00:00+02:00", "end": "2026-10-14T11:00:00+02:00", "all_day": false, "title": "Planning"},
}

func TestGoogle(t *testing.T) {
	srv := server(t, "google.ics")
	h := integrationtest.New(t) // 2026-10-03 14:05 UTC, 15:05 in London
	if err := connect(t, h, srv.URL+path, nil).Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	rid := "ical:i_test/calendar/main"
	r, ok := h.Resource(rid)
	if !ok || r.Kind != "calendar" || r.Name != "Household" || r.State != "online" || r.Health != integration.HealthOK || r.Source != "integration:i_test" {
		t.Fatalf("calendar = %+v", r)
	}
	for k, v := range map[string]any{"type": "calendar", "provider": "ical", "time_zone": "Europe/London"} {
		if r.Attrs[k] != v {
			t.Errorf("attrs[%s] = %v, want %v", k, r.Attrs[k], v)
		}
	}
	if _, ok := r.Attrs["calendar"]; ok {
		t.Error("attrs.calendar is media releases' (Up next): calendars publish attrs.events")
	}
	if caps, _ := r.Attrs["caps"].([]any); !reflect.DeepEqual(caps, []any{"calendar.events"}) {
		t.Errorf("caps = %v", r.Attrs["caps"])
	}
	if got := events(t, h); !reflect.DeepEqual(got, googleEvents) {
		t.Errorf("events =\n%s\nwant\n%s", show(got), show(googleEvents))
	}
	if h.Graph().Relations["integration:i_test monitors "+rid] == nil {
		t.Fatal("the instance should monitor its calendar")
	}

	// Descriptions, attendees, organisers and URLs never leave the feed.
	b, _ := json.Marshal(h.Graph())
	for _, s := range []string{"PRIVATE-DESCRIPTION-MARKER", "meet.example", "mail.example", "Organiser", "Attendee", "Yesterday", "Later event", "Cancelled lunch"} {
		if strings.Contains(string(b), s) {
			t.Errorf("the graph carries %q", s)
		}
	}
	if reqs := srv.Requests(path); len(reqs) != 1 || !strings.HasPrefix(reqs[0].Header.Get("User-Agent"), "chadash/") {
		t.Fatalf("requests = %v", reqs)
	}
}

func TestOutlook(t *testing.T) {
	srv := server(t, "outlook.ics")
	h := integrationtest.New(t)
	inst := connect(t, h, srv.URL+path, map[string]any{"name": "Work"})
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	r, _ := h.Resource("ical:i_test/calendar/main")
	if r.Name != "Work" || r.Attrs["time_zone"] != "Europe/Berlin" {
		t.Fatalf("calendar = %s, %v", r.Name, r.Attrs["time_zone"])
	}
	if got := events(t, h); !reflect.DeepEqual(got, outlookEvents) {
		t.Errorf("events =\n%s\nwant\n%s", show(got), show(outlookEvents))
	}
	// The unknown zone is a warning.
	res := inst.Test(t.Context())
	if !res.OK || len(res.Warnings) != 1 || res.Warnings[0].Code != "unknown_time_zone" || res.Warnings[0].Severity != integration.WarnWarning ||
		!strings.Contains(res.Warnings[0].Message, "Mars Standard Time") {
		t.Fatalf("test = %+v", res)
	}
}

// The hub's image has no zone database: the feeds' VTIMEZONEs give the same
// answer.
func TestNoZoneDatabase(t *testing.T) {
	noZoneDatabase(t)
	for file, want := range map[string]struct {
		zone   string
		events []map[string]any
	}{"google.ics": {"Europe/London", googleEvents}, "outlook.ics": {"Europe/Berlin", outlookEvents}} {
		srv := server(t, file)
		h := integrationtest.New(t)
		if err := connect(t, h, srv.URL+path, nil).Sync(t.Context(), integrationtest.Watched); err != nil {
			t.Fatal(err)
		}
		if r, _ := h.Resource("ical:i_test/calendar/main"); r.Attrs["time_zone"] != want.zone {
			t.Errorf("%s: time_zone = %v", file, r.Attrs["time_zone"])
		}
		if got := events(t, h); !reflect.DeepEqual(got, want.events) {
			t.Errorf("%s: events =\n%s\nwant\n%s", file, show(got), show(want.events))
		}
	}
}

// Across the end of summer time (25 October in London, 29 March is the
// other way): the weekly event keeps its wall-clock time and its offset
// changes.
func TestDaylightSaving(t *testing.T) {
	for _, db := range []bool{true, false} {
		t.Run(fmt.Sprint("database=", db), func(t *testing.T) {
			if !db {
				noZoneDatabase(t)
			}
			srv := server(t, "google.ics")
			h := integrationtest.New(t)
			h.Advance(17 * 24 * time.Hour) // 2026-10-20 14:05 UTC
			if err := connect(t, h, srv.URL+path, nil).Sync(t.Context(), integrationtest.Watched); err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, e := range events(t, h) {
				got = append(got, e["start"].(string)+" "+e["title"].(string))
			}
			want := []string{
				"2026-10-20T12:00:00+01:00 Later event",
				"2026-10-22T09:30:00+01:00 Weekly sync",
				"2026-10-26T09:30:00Z Weekly sync",
				"2026-10-29T09:30:00Z Weekly sync",
				"2026-11-02T09:30:00Z Weekly sync",
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("events =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
		})
	}
}

func TestTest(t *testing.T) {
	srv := server(t, "google.ics")
	h := integrationtest.New(t)
	res := connect(t, h, srv.URL+path, nil).Test(t.Context())
	if !res.OK || len(res.Warnings) != 0 || len(res.Grants) != 1 || res.Grants[0].CanWrite() ||
		res.Grants[0].Detail != `"Household": 10 events from today to 14 days ahead` {
		t.Fatalf("test = %+v", res)
	}
	if _, ok := h.Resource("ical:i_test/calendar/main"); ok {
		t.Fatal("Test must not emit facts")
	}
}

// More than 60 events in the window: the soonest are kept, and Test counts
// them all.
func TestTruncate(t *testing.T) {
	feed := strings.Join([]string{"BEGIN:VCALENDAR", "VERSION:2.0", "X-WR-TIMEZONE:UTC",
		"BEGIN:VEVENT", "UID:often@calendar.example", "SUMMARY:Every four hours",
		"DTSTART:20260901T020000Z", "DURATION:PT30M", "RRULE:FREQ=HOURLY;INTERVAL=4",
		"END:VEVENT", "END:VCALENDAR", ""}, "\r\n")
	srv := fixture.New(t, fixture.Dir(t, ID), nil, map[string]fixture.Route{"GET " + path: {Body: feed}})
	h := integrationtest.New(t)
	inst := connect(t, h, srv.URL+path, nil)
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	got := events(t, h)
	if len(got) != MaxEvents || got[0]["start"] != "2026-10-03T02:00:00Z" || got[0]["end"] != "2026-10-03T02:30:00Z" ||
		got[MaxEvents-1]["start"] != "2026-10-12T22:00:00Z" {
		t.Fatalf("%d events, from %v to %v", len(got), got[0], got[len(got)-1])
	}
	if r, _ := h.Resource("ical:i_test/calendar/main"); r.Name != "Test" || r.Attrs["time_zone"] != "UTC" {
		t.Errorf("no X-WR-CALNAME: the instance's name; got %s, %v", r.Name, r.Attrs["time_zone"])
	}
	if res := inst.Test(t.Context()); res.Grants[0].Detail != `"Test": 84 events from today to 14 days ahead` {
		t.Fatalf("test = %+v", res.Grants)
	}
}

// Less common shapes: DURATION, RDATE (a list, and a PERIOD), a date
// EXDATE on a timed event, a zone known only from its VTIMEZONE, an
// override without its series, a cancelled series with an override, and an
// event without DTSTART.
func TestShapes(t *testing.T) {
	feed := strings.Join([]string{"BEGIN:VCALENDAR", "VERSION:2.0",
		"BEGIN:VTIMEZONE", "TZID:Customized Time Zone",
		"BEGIN:STANDARD", "DTSTART:16010101T000000", "TZOFFSETFROM:+0530", "TZOFFSETTO:+0530", "END:STANDARD",
		"END:VTIMEZONE",
		"BEGIN:VEVENT", "UID:a@calendar.example", "SUMMARY:Rdates", "DTSTART;TZID=Customized Time Zone:20261005T090000", "DURATION:PT1H",
		"RDATE;TZID=Customized Time Zone:20261006T090000,20261007T100000", "RDATE;VALUE=PERIOD:20261008T033000Z/PT1H",
		"EXDATE;VALUE=DATE:20261007", "END:VEVENT",
		"BEGIN:VEVENT", "UID:b@calendar.example", "RECURRENCE-ID:20261009T090000Z", "SUMMARY:Lone instance",
		"DTSTART:20261009T100000Z", "DTEND:20261009T110000Z", "END:VEVENT",
		"BEGIN:VEVENT", "UID:c@calendar.example", "SUMMARY:Gone", "STATUS:CANCELLED", "DTSTART:20261001T100000Z",
		"RRULE:FREQ=DAILY", "END:VEVENT",
		"BEGIN:VEVENT", "UID:c@calendar.example", "RECURRENCE-ID:20261010T100000Z", "SUMMARY:Gone too",
		"DTSTART:20261010T120000Z", "END:VEVENT",
		"BEGIN:VEVENT", "UID:d@calendar.example", "SUMMARY:No start", "END:VEVENT",
		"BEGIN:VEVENT", "UID:e@calendar.example", "DTSTART;VALUE=DATE:20261011", "DURATION:P2D", "END:VEVENT",
		"END:VCALENDAR", ""}, "\r\n")
	srv := fixture.New(t, fixture.Dir(t, ID), nil, map[string]fixture.Route{"GET " + path: {Body: feed}})
	h := integrationtest.New(t)
	if err := connect(t, h, srv.URL+path, nil).Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	want := []map[string]any{
		{"start": "2026-10-05T09:00:00+05:30", "end": "2026-10-05T10:00:00+05:30", "all_day": false, "title": "Rdates"},
		{"start": "2026-10-06T09:00:00+05:30", "end": "2026-10-06T10:00:00+05:30", "all_day": false, "title": "Rdates"},
		{"start": "2026-10-08T09:00:00+05:30", "end": "2026-10-08T10:00:00+05:30", "all_day": false, "title": "Rdates"},
		{"start": "2026-10-09T15:30:00+05:30", "end": "2026-10-09T16:30:00+05:30", "all_day": false, "title": "Lone instance"},
		{"start": "2026-10-11", "end": "2026-10-13", "all_day": true, "title": "Untitled event"},
	}
	if got := events(t, h); !reflect.DeepEqual(got, want) {
		t.Errorf("events =\n%s\nwant\n%s", show(got), show(want))
	}
	// Not an IANA name: no time_zone.
	if r, _ := h.Resource("ical:i_test/calendar/main"); r.Attrs["time_zone"] != nil {
		t.Errorf("time_zone = %v", r.Attrs["time_zone"])
	}
}

// Failures are worded without the address, and nothing the instance says
// carries its token.
func TestFailures(t *testing.T) {
	srv := server(t, "google.ics")
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	for name, c := range map[string]struct {
		route   *fixture.Route
		base    string
		code    string
		field   string
		message string
	}{
		"401":         {route: &fixture.Route{Body: "no", Status: http.StatusUnauthorized}, code: integration.CodeAuth, field: "feed_url", message: "the feed answered 401 Unauthorized"},
		"403":         {route: &fixture.Route{Body: "no", Status: http.StatusForbidden}, code: integration.CodeAuth, field: "feed_url", message: "the feed answered 403 Forbidden"},
		"404":         {route: &fixture.Route{Body: "gone", Status: http.StatusNotFound}, code: integration.CodeAPIChanged, field: "feed_url", message: "the feed answered 404 Not Found"},
		"500":         {route: &fixture.Route{Body: "oops", Status: http.StatusInternalServerError}, code: integration.CodeUpstream, message: "the feed answered 500 Internal Server Error"},
		"html":        {route: &fixture.Route{Body: "<!doctype html><title>Sign in</title>", ContentType: "text/html"}, code: integration.CodeAPIChanged, field: "feed_url", message: "that address isn't an iCal feed"},
		"broken":      {route: &fixture.Route{Body: "BEGIN:VCALENDAR\r\nnot a content line\r\n"}, code: integration.CodeAPIChanged, field: "feed_url", message: "that address isn't an iCal feed"},
		"unreachable": {base: closed.URL, code: integration.CodeUnreachable, message: "couldn't reach 127.0.0.1"},
	} {
		t.Run(name, func(t *testing.T) {
			if c.route != nil {
				srv.Set("GET "+path, *c.route)
			}
			base := cmpOr(c.base, srv.URL)
			h := integrationtest.New(t)
			inst := connect(t, h, base+path, nil)
			err := inst.Sync(t.Context(), integrationtest.Watched)
			code, _, msg := integration.Classify(err)
			if code != c.code || !strings.HasPrefix(msg, c.message) {
				t.Fatalf("%s %q", code, msg)
			}
			if c.field != "" {
				var ae *integration.AuthError
				var re *integration.RepairError
				if !(errors.As(err, &ae) && ae.Field == c.field) && !(errors.As(err, &re) && re.Field == c.field) {
					t.Errorf("field: %#v", err)
				}
			}
			res := inst.Test(t.Context())
			b, _ := json.Marshal(res)
			g, _ := json.Marshal(h.Graph())
			for what, s := range map[string]string{"error": err.Error(), "message": msg, "test": string(b), "logs": h.Logs(), "graph": string(g)} {
				if strings.Contains(s, token) || strings.Contains(s, "owner.group.example") {
					t.Errorf("the %s carries the address: %s", what, s)
				}
			}
		})
	}
	// The checks above looked at requests that did carry the token.
	if reqs := srv.Requests(path); len(reqs) == 0 || !strings.Contains(reqs[0].URL.Path, token) {
		t.Fatalf("requests = %v", reqs)
	}
	// And when it works.
	srv.Set("GET "+path, fixture.Route{File: "google.ics"})
	h := integrationtest.New(t)
	inst := connect(t, h, srv.URL+path, nil)
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(inst.Test(t.Context()))
	g, _ := json.Marshal(h.Graph())
	for what, s := range map[string]string{"test": string(b), "logs": h.Logs(), "graph": string(g), "config": fmt.Sprint(h.StoredConfig())} {
		if strings.Contains(s, token) {
			t.Errorf("the %s carries the token: %s", what, s)
		}
	}
}

func TestText(t *testing.T) {
	for in, want := range map[string]string{
		`Lunch, then walk`:         "Lunch, then walk",
		`Room 4\, Learning Centre`: "Room 4, Learning Centre",
		`a\;b\\c\nd\Ne`:            `a;b\c d e`,
		"  spaced \t  out  ":       "spaced out",
		`trailing\`:                `trailing\`,
		strings.Repeat("é", 250):   strings.Repeat("é", maxText-1) + "…",
	} {
		if got := clip(strings.Join(strings.Fields(unescape(in)), " "), maxText); got != want {
			t.Errorf("%q = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]int{"+0100": 3600, "-0500": -18000, "+0530": 19800, "+053045": 19845, "-0000": 0} {
		if got, ok := utcOffset(&goical.Prop{Value: in}); !ok || got != want {
			t.Errorf("%s = %d, %v", in, got, ok)
		}
	}
	for _, in := range []string{"", "0100", "+01", "+01:00"} {
		if _, ok := utcOffset(&goical.Prop{Value: in}); ok {
			t.Errorf("%q: ok", in)
		}
	}
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func show(es []map[string]any) string {
	var lines []string
	for _, e := range es {
		b, _ := json.Marshal(e)
		lines = append(lines, string(b))
	}
	return strings.Join(lines, "\n")
}
