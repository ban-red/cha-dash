// Package lidarr reads Lidarr (v1 and later, API v1): the queue, what's
// wanted (missing albums plus cutoff unmet), health checks, and upcoming
// album releases for the media calendar. See README.md.
package lidarr

import (
	"context"
	"strings"
	"time"

	"dash.cha.sh/integrations/internal/arr"
	"dash.cha.sh/internal/integration"
)

// ID is the integration's ID.
const ID = "lidarr"

var app = arr.MediaApp{
	ID: ID, Name: "Lidarr", APIPath: "/api/v1", MinMajor: 1,
	MissingKey: "missing.albums", MissingMax: 10000,
	SearchCommand: "MissingAlbumSearch", MissingNoun: "albums",
	QueueBand: [2]float64{0, 5}, WantedBand: [2]float64{0, 20},
	Calendar: calendar,
}

func init() { integration.Register(Integration{}) }

// Integration is the Lidarr integration.
type Integration struct{}

// Manifest implements integration.Integration.
func (Integration) Manifest() integration.Manifest {
	return integration.Manifest{
		ID: ID, Name: "Lidarr", Icon: "lidarr", Version: "0.1.0", Tier: integration.Silver,
		Category:     integration.CatAutomation,
		Capabilities: []integration.Capability{"media.calendar"},
		ConfigSchema: arr.Schema(app.App),
		Poll:         arr.Poll,
	}
}

// Connect implements integration.Integration.
func (Integration) Connect(_ context.Context, cfg integration.Config, d integration.Deps) (integration.Instance, error) {
	return arr.NewMedia(app, cfg, d), nil
}

// calendar reads the albums releasing in now's window, unmonitored ones
// marked, with their artist.
func calendar(ctx context.Context, c *arr.Client, now time.Time) ([]arr.CalendarItem, error) {
	q := arr.CalendarQuery(now)
	q.Set("includeArtist", "true")
	var albums []album
	if err := c.Get(ctx, "/calendar", q, &albums); err != nil {
		return nil, err
	}
	var out []arr.CalendarItem
	for _, a := range albums {
		if it, ok := a.item(); ok {
			out = append(out, it)
		}
	}
	return out, nil
}

// album is the part of Lidarr's AlbumResource the calendar uses.
type album struct {
	Title       string     `json:"title"`
	AlbumType   string     `json:"albumType"` // Album, EP, Single, Broadcast, Other
	ReleaseDate *time.Time `json:"releaseDate"`
	Monitored   *bool      `json:"monitored"`
	Artist      *struct {
		ArtistName string `json:"artistName"`
	} `json:"artist"`
}

// item is the calendar entry: the artist as the title, the album as the
// detail, with its type when it isn't an album ("Night Ferries · EP").
// Lidarr gives release dates at midnight UTC.
func (a album) item() (arr.CalendarItem, bool) {
	title := strings.TrimSpace(a.Title)
	if a.ReleaseDate == nil || a.ReleaseDate.IsZero() || a.Artist == nil || strings.TrimSpace(a.Artist.ArtistName) == "" || title == "" {
		return arr.CalendarItem{}, false
	}
	detail := title
	switch t := strings.TrimSpace(a.AlbumType); {
	case t == "EP":
		detail += " · EP"
	case t != "" && !strings.EqualFold(t, "album"):
		detail += " · " + strings.ToLower(t)
	}
	return arr.CalendarItem{At: *a.ReleaseDate, Title: strings.TrimSpace(a.Artist.ArtistName), Detail: detail, Unmonitored: arr.Unmonitored(a.Monitored)}, true
}
