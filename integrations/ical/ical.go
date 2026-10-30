// Package ical is the calendar feed integration: one iCalendar (.ics) feed,
// read from its address (Google Calendar's secret address, iCloud, Outlook,
// Nextcloud or any other), published as a calendar resource with the events
// from today to two weeks ahead. See README.md.
package ical

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"dash.cha.sh/internal/integration"
)

// ID is the integration's ID.
const ID = "ical"

// Polling: Google refreshes its feeds every few hours, and most providers
// hourly at best, so reading more often only costs requests.
const (
	interval = 15 * time.Minute
	idle     = time.Hour
)

// The window and its cap: events from local midnight today to Days ahead,
// at most MaxEvents of them, soonest kept.
const (
	Days      = 14
	MaxEvents = 60
)

func init() { integration.Register(Integration{}) }

// Integration is the calendar feed integration.
type Integration struct{}

// Manifest implements integration.Integration.
func (Integration) Manifest() integration.Manifest {
	feed := integration.Secret("Feed address").Describe("The calendar's iCal (.ics) address. In Google Calendar: Settings → the calendar → Secret address in iCal format. webcal:// works too.")
	name := integration.String("Calendar name").Describe("Shown on the widget. Defaults to the feed's own name.")
	n := 64
	name.MaxLength = &n
	return integration.Manifest{
		ID: ID, Name: "Calendar feed (iCal)", Icon: "calendar", Version: "0.1.0", Tier: integration.Bronze,
		Category:     integration.CatHome,
		Capabilities: []integration.Capability{"calendar.events"},
		ConfigSchema: integration.Object(integration.Prop("feed_url", feed), integration.Prop("name", name), integration.TLSProp()).
			Require("feed_url"),
		Poll: integration.Poll{Interval: interval, Idle: idle, Timeout: 30 * time.Second},
	}
}

// ValidateConfig implements integration.ConfigValidator. The message never
// repeats the address: it's a secret.
func (Integration) ValidateConfig(cfg integration.Config) []integration.FieldError {
	if _, err := feedURL(cfg.String("feed_url")); err != nil {
		return []integration.FieldError{{Path: "/feed_url", Message: err.Error()}}
	}
	return nil
}

// feedURL is the address to fetch: webcal:// (and webcals://) is read as
// https://, as calendar apps do.
func feedURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, errors.New("must be an https://, http:// or webcal:// address")
	}
	switch strings.ToLower(u.Scheme) {
	case "webcal", "webcals", "https":
		u.Scheme = "https"
	case "http":
		u.Scheme = "http"
	default:
		return nil, errors.New("must be an https://, http:// or webcal:// address")
	}
	if u.Host == "" {
		return nil, errors.New("must be an https://, http:// or webcal:// address")
	}
	u.Fragment = ""
	return u, nil
}

// config is an instance's config.
type config struct {
	FeedURL string `json:"feed_url"`
	Name    string `json:"name"`
}

// Connect implements integration.Integration.
func (Integration) Connect(_ context.Context, cfg integration.Config, d integration.Deps) (integration.Instance, error) {
	var c config
	if err := cfg.Decode(&c); err != nil {
		return nil, &integration.ConfigError{Message: "the config can't be read"}
	}
	u, err := feedURL(c.FeedURL)
	if err != nil {
		return nil, &integration.ConfigError{Field: "feed_url", Message: "feed_url " + err.Error()}
	}
	key := cfg.InstanceID
	if key == "" {
		key = "test"
	}
	return &instance{d: d, url: u, raw: c.FeedURL, name: strings.TrimSpace(c.Name), instName: cfg.Name,
		rid: ID + ":" + key + "/calendar/main"}, nil
}

type instance struct {
	d        integration.Deps
	url      *url.URL
	raw      string // the address as configured, only to scrub it from messages
	name     string // from the config
	instName string
	rid      string
}

// Close implements integration.Instance.
func (in *instance) Close() error { return nil }

// feedError is a failure to read the feed, worded without its address. It
// unwraps to the cause, so the runtime still tells a timeout from a refused
// connection or an untrusted certificate.
type feedError struct {
	msg string
	err error
}

func (e *feedError) Error() string { return e.msg }
func (e *feedError) Unwrap() error { return e.err }

// errNotICal is a 2xx answer that isn't a calendar: a sign-in page, the
// calendar's HTML view, an empty body.
var errNotICal = &integration.RepairError{Code: integration.CodeAPIChanged, Field: "feed_url",
	Message: "that address isn't an iCal feed: copy the calendar's iCal (.ics) address"}

// scrub takes the address, and its path and query on their own, out of a
// message. Errors here are built without the address; this is the second
// line.
func (in *instance) scrub(s string) string {
	for _, part := range []string{in.raw, in.url.String(), in.url.EscapedPath(), in.url.Path, in.url.RawQuery} {
		if len(part) > 1 {
			s = strings.ReplaceAll(s, part, "…")
		}
	}
	return s
}

// fetch reads the feed's body.
func (in *instance) fetch(ctx context.Context) ([]byte, error) {
	host := in.url.Hostname()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, in.url.String(), nil)
	if err != nil {
		return nil, &integration.ConfigError{Field: "feed_url", Message: "the feed address can't be used"}
	}
	req.Header.Set("Accept", "text/calendar, */*;q=0.5")
	resp, err := in.d.HTTP.Do(req)
	if err != nil {
		cause := err
		if ue, ok := errors.AsType[*url.Error](err); ok {
			cause = ue.Err // url.Error prints the whole address
		}
		return nil, &feedError{msg: in.scrub("couldn't reach " + host + ": " + cause.Error()), err: cause}
	}
	defer resp.Body.Close()
	code, text := resp.StatusCode, http.StatusText(resp.StatusCode)
	switch {
	case code == http.StatusUnauthorized || code == http.StatusForbidden:
		return nil, &integration.AuthError{Field: "feed_url",
			Message: fmt.Sprintf("the feed answered %d %s: the address was refused; it may have been reset, so copy it again", code, text)}
	case code == http.StatusNotFound || code == http.StatusGone:
		return nil, &integration.RepairError{Code: integration.CodeAPIChanged, Field: "feed_url",
			Message: fmt.Sprintf("the feed answered %d %s: the address may have been reset or the calendar deleted; copy it again", code, text)}
	case code < 200 || code > 299:
		return nil, fmt.Errorf("the feed answered %d %s", code, text)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &feedError{msg: in.scrub("couldn't read the feed from " + host + ": " + err.Error()), err: err}
	}
	return body, nil
}

// read fetches and parses the feed, and lays out the window's events.
func (in *instance) read(ctx context.Context) (*calendar, []event, int, error) {
	body, err := in.fetch(ctx)
	if err != nil {
		return nil, nil, 0, err
	}
	now := in.d.Now()
	c, err := parse(body, now)
	if err != nil {
		return nil, nil, 0, err
	}
	events, total := c.window(now)
	return c, events, total, nil
}

// calName is the calendar's name: the config's, the feed's, else the
// instance's.
func (in *instance) calName(c *calendar) string {
	switch {
	case in.name != "":
		return in.name
	case c.name != "":
		return c.name
	case in.instName != "":
		return in.instName
	}
	return "Calendar"
}

// Test implements integration.Instance: one fetch, reporting the calendar's
// name and how many events it has in the window.
func (in *instance) Test(ctx context.Context) integration.TestResult {
	c, _, total, err := in.read(ctx)
	if err != nil {
		return integration.Failed(err)
	}
	count := fmt.Sprintf("%d events", total)
	if total == 1 {
		count = "1 event"
	}
	return integration.TestResult{OK: true, Warnings: c.warnings(),
		Grants: []integration.Grant{{Name: "calendar", Access: integration.AccessRead,
			Detail: fmt.Sprintf("%q: %s from today to %d days ahead", in.calName(c), count, Days)}}}
}

// Sync implements integration.Instance.
func (in *instance) Sync(ctx context.Context, _ integration.Interest) error {
	c, events, _, err := in.read(ctx)
	if err != nil {
		return err
	}
	list := make([]any, 0, len(events))
	for _, e := range events {
		m := map[string]any{"start": e.start, "end": e.end, "all_day": e.allDay, "title": e.title}
		if e.location != "" {
			m["location"] = e.location
		}
		list = append(list, m)
	}
	attrs := map[string]any{"type": "calendar", "provider": ID, "events": list}
	if c.zone.name != "" {
		attrs["time_zone"] = c.zone.name
	}
	res := integration.Resource{ID: in.rid, Kind: "calendar", Name: in.calName(c), State: "online",
		Health: integration.Health{Status: integration.HealthOK}, Attrs: attrs, Caps: []string{"calendar.events"}}
	if err := in.d.Emit.Replace("", integration.Facts{
		Resources: []integration.Resource{res},
		Relations: []integration.Relation{{From: in.d.Emit.Self(), Type: "monitors", To: in.rid}},
	}); err != nil {
		return err
	}
	in.d.Emit.SetWarnings(c.warnings())
	return nil
}
