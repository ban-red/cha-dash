// Package sonarr reads Sonarr (v3 and v4, API v3): the queue, what's wanted
// (missing episodes plus cutoff unmet), health checks, and the upcoming
// episodes for the media calendar. See README.md.
package sonarr

import (
	"context"
	"fmt"
	"strings"
	"time"

	"dash.cha.sh/integrations/internal/arr"
	"dash.cha.sh/internal/integration"
)

// ID is the integration's ID.
const ID = "sonarr"

var app = arr.MediaApp{
	ID: ID, Name: "Sonarr", APIPath: "/api/v3", MinMajor: 3,
	MissingKey: "missing.episodes", MissingMax: 100000,
	SearchCommand: "MissingEpisodeSearch", MissingNoun: "episodes",
	QueueBand: [2]float64{0, 15}, WantedBand: [2]float64{0, 40},
	Calendar: calendar,
}

func init() { integration.Register(Integration{}) }

// Integration is the Sonarr integration.
type Integration struct{}

// Manifest implements integration.Integration.
func (Integration) Manifest() integration.Manifest {
	return integration.Manifest{
		ID: ID, Name: "Sonarr", Icon: "sonarr", Version: "0.1.0", Tier: integration.Silver,
		Capabilities: []integration.Capability{"media.calendar"},
		ConfigSchema: arr.Schema(app.App),
		Poll:         arr.Poll,
	}
}

// Connect implements integration.Integration.
func (Integration) Connect(_ context.Context, cfg integration.Config, d integration.Deps) (integration.Instance, error) {
	return arr.NewMedia(app, cfg, d), nil
}

// calendar reads the episodes airing in now's window, unmonitored ones
// marked, with their series.
func calendar(ctx context.Context, c *arr.Client, now time.Time) ([]arr.CalendarItem, error) {
	q := arr.CalendarQuery(now)
	q.Set("includeSeries", "true")
	var eps []episode
	if err := c.Get(ctx, "/calendar", q, &eps); err != nil {
		return nil, err
	}
	var out []arr.CalendarItem
	for _, e := range eps {
		if it, ok := e.item(); ok {
			out = append(out, it)
		}
	}
	return out, nil
}

// episode is the part of Sonarr's EpisodeResource the calendar uses.
type episode struct {
	SeasonNumber  int        `json:"seasonNumber"`
	EpisodeNumber int        `json:"episodeNumber"`
	Title         string     `json:"title"`
	AirDateUTC    *time.Time `json:"airDateUtc"`
	FinaleType    string     `json:"finaleType"`
	Monitored     *bool      `json:"monitored"`
	Series        *struct {
		Title string `json:"title"`
	} `json:"series"`
}

// item is the calendar entry: the series as the title, the episode as the
// detail ("S02E05 · Title · season finale").
func (e episode) item() (arr.CalendarItem, bool) {
	if e.AirDateUTC == nil || e.Series == nil || e.Series.Title == "" {
		return arr.CalendarItem{}, false
	}
	parts := []string{fmt.Sprintf("S%02dE%02d", e.SeasonNumber, e.EpisodeNumber)}
	if t := strings.TrimSpace(e.Title); t != "" && t != "TBA" {
		parts = append(parts, t)
	}
	switch {
	case e.FinaleType == "series" || e.FinaleType == "season" || e.FinaleType == "midseason":
		parts = append(parts, e.FinaleType+" finale")
	case e.EpisodeNumber == 1 && e.SeasonNumber == 1:
		parts = append(parts, "series premiere")
	case e.EpisodeNumber == 1 && e.SeasonNumber > 1:
		parts = append(parts, "season premiere")
	}
	return arr.CalendarItem{At: *e.AirDateUTC, Title: e.Series.Title, Detail: strings.Join(parts, " · "), Unmonitored: arr.Unmonitored(e.Monitored)}, true
}
