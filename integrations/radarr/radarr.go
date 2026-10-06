// Package radarr reads Radarr (v3 and later, API v3): the queue, what's
// wanted (missing movies plus cutoff unmet, from v5), health checks, and
// upcoming releases for the media calendar. See README.md.
package radarr

import (
	"context"
	"time"

	"dash.cha.sh/integrations/internal/arr"
	"dash.cha.sh/internal/integration"
)

// ID is the integration's ID.
const ID = "radarr"

var app = arr.MediaApp{
	App:        arr.App{ID: ID, Name: "Radarr", APIPath: "/api/v3", MinMajor: 3},
	MissingKey: "missing.movies", MissingMax: 10000,
	SearchCommand: "MissingMoviesSearch", MissingNoun: "movies",
	QueueBand: [2]float64{0, 5}, WantedBand: [2]float64{0, 20},
	Calendar: calendar,
}

func init() { integration.Register(Integration{}) }

// Integration is the Radarr integration.
type Integration struct{}

// Manifest implements integration.Integration.
func (Integration) Manifest() integration.Manifest {
	return integration.Manifest{
		ID: ID, Name: "Radarr", Icon: "radarr", Version: "0.1.0", Tier: integration.Silver,
		Capabilities: []integration.Capability{"media.calendar"},
		ConfigSchema: arr.Schema(app.App),
		Poll:         arr.Poll,
	}
}

// Connect implements integration.Integration.
func (Integration) Connect(_ context.Context, cfg integration.Config, d integration.Deps) (integration.Instance, error) {
	return arr.NewMedia(app, cfg, d), nil
}

// calendar reads the movies with a release date in now's window, unmonitored
// ones marked: one entry per release date inside it.
func calendar(ctx context.Context, c *arr.Client, now time.Time) ([]arr.CalendarItem, error) {
	var movies []movie
	if err := c.Get(ctx, "/calendar", arr.CalendarQuery(now), &movies); err != nil {
		return nil, err
	}
	var out []arr.CalendarItem
	for _, m := range movies {
		out = append(out, m.items()...)
	}
	return out, nil
}

// movie is the part of Radarr's MovieResource the calendar uses.
type movie struct {
	Title           string     `json:"title"`
	InCinemas       *time.Time `json:"inCinemas"`
	DigitalRelease  *time.Time `json:"digitalRelease"`
	PhysicalRelease *time.Time `json:"physicalRelease"`
	Monitored       *bool      `json:"monitored"`
}

// items are the movie's release dates as calendar entries; arr.Calendar
// keeps the ones inside the window.
func (m movie) items() []arr.CalendarItem {
	if m.Title == "" {
		return nil
	}
	var out []arr.CalendarItem
	for _, r := range []struct {
		at     *time.Time
		detail string
	}{{m.InCinemas, "in cinemas"}, {m.DigitalRelease, "digital release"}, {m.PhysicalRelease, "physical release"}} {
		if r.at != nil && !r.at.IsZero() {
			out = append(out, arr.CalendarItem{At: *r.at, Title: m.Title, Detail: r.detail, Unmonitored: arr.Unmonitored(m.Monitored)})
		}
	}
	return out
}
