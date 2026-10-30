package arr

import (
	"context"
	"fmt"
	"maps"
	"net/url"
	"strings"
	"time"
)

// ListMax is how many entries the queue and wanted lists carry for a tile.
const ListMax = 25

// QueueItem is one download in the queue, as attrs.queue_items carries it:
// what it is (the series, movie or artist, then the episode or album), how far
// along, and how long is left. Error says why one is stuck.
type QueueItem struct {
	Title    string `json:"title"`
	Detail   string `json:"detail,omitempty"`
	Status   string `json:"status"` // downloading, queued, paused, completed, warning, failed…
	Progress int    `json:"progress"`
	LeftS    int    `json:"left_s,omitempty"`
	Client   string `json:"client,omitempty"`
	Protocol string `json:"protocol,omitempty"`
	Error    string `json:"error,omitempty"`
}

// WantedItem is one missing episode, movie or album, as attrs.wanted_items
// carries it. At is Unix seconds (the air or release date), 0 when unknown.
type WantedItem struct {
	Title  string `json:"title"`
	Detail string `json:"detail,omitempty"`
	At     int64  `json:"at,omitempty"`
}

// queueRecord is the part of a /queue record the tile reads, for all three
// apps: the include* parameters add the series or movie or artist.
type queueRecord struct {
	Title                string  `json:"title"`
	Status               string  `json:"status"`
	TrackedDownloadState string  `json:"trackedDownloadState"`
	TrackedStatus        string  `json:"trackedDownloadStatus"`
	Size                 float64 `json:"size"`
	SizeLeft             float64 `json:"sizeleft"`
	TimeLeft             string  `json:"timeleft"`
	Protocol             string  `json:"protocol"`
	DownloadClient       string  `json:"downloadClient"`
	ErrorMessage         string  `json:"errorMessage"`
	StatusMessages       []struct {
		Messages []string `json:"messages"`
	} `json:"statusMessages"`
	Series  *struct{ Title string } `json:"series"`
	Episode *struct {
		SeasonNumber, EpisodeNumber int
		Title                       string
	} `json:"episode"`
	Movie *struct {
		Title string
		Year  int
	} `json:"movie"`
	Artist *struct {
		ArtistName string `json:"artistName"`
	} `json:"artist"`
	Album *struct{ Title string } `json:"album"`
}

func (r queueRecord) item() (QueueItem, bool) {
	title, detail := strings.TrimSpace(r.Title), ""
	switch {
	case r.Series != nil && r.Series.Title != "":
		title = r.Series.Title
		if e := r.Episode; e != nil {
			detail = fmt.Sprintf("S%02dE%02d", e.SeasonNumber, e.EpisodeNumber)
			if t := strings.TrimSpace(e.Title); t != "" && t != "TBA" {
				detail += " · " + t
			}
		}
	case r.Movie != nil && r.Movie.Title != "":
		title = r.Movie.Title
		if r.Movie.Year > 0 {
			detail = fmt.Sprint(r.Movie.Year)
		}
	case r.Artist != nil && r.Artist.ArtistName != "":
		title = r.Artist.ArtistName
		if r.Album != nil {
			detail = strings.TrimSpace(r.Album.Title)
		}
	}
	if title == "" {
		return QueueItem{}, false
	}
	it := QueueItem{Title: title, Detail: detail, Status: strings.ToLower(r.Status), Client: r.DownloadClient, Protocol: r.Protocol}
	if r.Size > 0 {
		it.Progress = min(100, max(0, int((r.Size-r.SizeLeft)/r.Size*100)))
	}
	if t, ok := parseTimeLeft(r.TimeLeft); ok && it.Status == "downloading" {
		it.LeftS = int(t.Seconds())
	}
	// A warning or an error says why in its status messages.
	if s := strings.ToLower(r.TrackedStatus); s == "warning" || s == "error" || strings.EqualFold(r.TrackedDownloadState, "importBlocked") {
		it.Status = strings.ToLower(r.TrackedStatus)
		if it.Status == "" {
			it.Status = "warning"
		}
		it.Error = strings.TrimSpace(r.ErrorMessage)
		for _, m := range r.StatusMessages {
			if it.Error == "" && len(m.Messages) > 0 {
				it.Error = strings.TrimSpace(m.Messages[0])
			}
		}
	}
	return it, true
}

// parseTimeLeft reads the *arrs' "hh:mm:ss" (or "d.hh:mm:ss") timeleft.
func parseTimeLeft(s string) (time.Duration, bool) {
	if s == "" {
		return 0, false
	}
	days := 0
	if d, rest, ok := strings.Cut(s, "."); ok && !strings.Contains(d, ":") {
		if _, err := fmt.Sscanf(d, "%d", &days); err != nil {
			return 0, false
		}
		s = rest
	}
	var h, m, sec int
	if _, err := fmt.Sscanf(s, "%d:%d:%d", &h, &m, &sec); err != nil {
		return 0, false
	}
	return time.Duration(days*24+h)*time.Hour + time.Duration(m)*time.Minute + time.Duration(sec)*time.Second, true
}

// QueueItems reads the first ListMax downloads of /queue. extra carries the
// app's include parameters. A version without the endpoint gives none.
func (c *Client) QueueItems(ctx context.Context, extra url.Values) ([]QueueItem, error) {
	q := url.Values{"page": {"1"}, "pageSize": {fmt.Sprint(ListMax)}, "includeUnknownSeriesItems": {"false"}}
	maps.Copy(q, extra)
	var p struct {
		Records []queueRecord `json:"records"`
	}
	if _, err := c.GetOptional(ctx, "/queue", q, &p); err != nil {
		return nil, err
	}
	out := []QueueItem{}
	for _, r := range p.Records {
		if it, ok := r.item(); ok {
			out = append(out, it)
		}
	}
	return out, nil
}

// wantedRecord is the part of a /wanted/missing record the tile reads: an
// episode (with its series), a movie, or an album (with its artist).
type wantedRecord struct {
	Title           string                  `json:"title"`
	Year            int                     `json:"year"`
	SeasonNumber    int                     `json:"seasonNumber"`
	EpisodeNumber   int                     `json:"episodeNumber"`
	AirDateUTC      *time.Time              `json:"airDateUtc"`
	InCinemas       *time.Time              `json:"inCinemas"`
	DigitalRelease  *time.Time              `json:"digitalRelease"`
	PhysicalRelease *time.Time              `json:"physicalRelease"`
	ReleaseDate     *time.Time              `json:"releaseDate"`
	Series          *struct{ Title string } `json:"series"`
	Artist          *struct {
		ArtistName string `json:"artistName"`
	} `json:"artist"`
}

func (r wantedRecord) item(now time.Time) (WantedItem, bool) {
	title, detail := strings.TrimSpace(r.Title), ""
	var at *time.Time
	switch {
	case r.Series != nil && r.Series.Title != "": // an episode
		title = r.Series.Title
		detail = fmt.Sprintf("S%02dE%02d", r.SeasonNumber, r.EpisodeNumber)
		if t := strings.TrimSpace(r.Title); t != "" && t != "TBA" {
			detail += " · " + t
		}
		at = r.AirDateUTC
	case r.Artist != nil && r.Artist.ArtistName != "": // an album
		detail, title, at = strings.TrimSpace(r.Title), r.Artist.ArtistName, r.ReleaseDate
	case r.AirDateUTC != nil: // an episode without its series
		detail = fmt.Sprintf("S%02dE%02d", r.SeasonNumber, r.EpisodeNumber)
		at = r.AirDateUTC
	case r.ReleaseDate != nil:
		at = r.ReleaseDate
	default: // a movie: the latest release that has happened
		if r.Year > 0 {
			detail = fmt.Sprint(r.Year)
		}
		for _, t := range []*time.Time{r.PhysicalRelease, r.DigitalRelease, r.InCinemas} {
			if t != nil && !t.IsZero() && !t.After(now) && (at == nil || t.After(*at)) {
				at = t
			}
		}
	}
	if title == "" {
		return WantedItem{}, false
	}
	it := WantedItem{Title: title, Detail: detail}
	if at != nil && !at.IsZero() {
		it.At = at.Unix()
	}
	return it, true
}

// Missing reads the missing count and the newest ListMax entries in one
// request. extra carries the app's sort and include parameters. found is
// false for a version without the endpoint.
func (c *Client) Missing(ctx context.Context, extra url.Values, now time.Time) (total int, items []WantedItem, found bool, err error) {
	q := url.Values{"page": {"1"}, "pageSize": {fmt.Sprint(ListMax)}, "monitored": {"true"}, "sortDirection": {"descending"}}
	maps.Copy(q, extra)
	var p struct {
		Paging
		Records []wantedRecord `json:"records"`
	}
	found, err = c.GetOptional(ctx, "/wanted/missing", q, &p)
	if err != nil || !found {
		return 0, nil, found, err
	}
	items = []WantedItem{}
	for _, r := range p.Records {
		if it, ok := r.item(now); ok {
			items = append(items, it)
		}
	}
	return p.TotalRecords, items, true, nil
}
