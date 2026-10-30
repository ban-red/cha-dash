// Package plex reads a Plex Media Server: what's playing (and how it
// plays), what was added lately, and how big the libraries are. Its token can come from plex.tv's PIN
// sign-in (see auth.go), so nobody has to dig an X-Plex-Token out of the
// web app. See README.md.
package plex

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"dash.cha.sh/internal/integration"
)

// ID is the integration's ID.
const ID = "plex"

// librariesEvery is how often the library counts are read again while
// someone watches.
const librariesEvery = 30 * time.Minute

// recentEvery is how often recently added is read again while someone
// watches; recentRows is how many items it keeps.
const (
	recentEvery = 5 * time.Minute
	recentRows  = 8
)

// playingRows is how many sessions now_playing lists.
const playingRows = 8

func init() { integration.Register(Integration{}) }

// Integration is the Plex integration. It implements integration.Authorizer.
type Integration struct{}

// Manifest implements integration.Integration.
func (Integration) Manifest() integration.Manifest {
	token := integration.Secret("Token").Describe("Sign in with Plex to fill this in, or paste an X-Plex-Token. Use the server owner's account: only it can see what's playing.")
	token.Authorize = true
	return integration.Manifest{
		ID: ID, Name: "Plex", Icon: "plex", Version: "0.1.0", Tier: integration.Silver,
		Category:     integration.CatMedia,
		Capabilities: []integration.Capability{"media.sessions", "media.library"},
		ConfigSchema: integration.Object(
			integration.Prop("url", integration.URL("URL").Describe("Your Plex Media Server: https://plex.lab.example, or https://<address>:32400.")),
			integration.TLSProp(),
			integration.Prop("token", token),
		).Require("url", "token"),
		Poll: integration.Poll{Interval: 30 * time.Second, Idle: 5 * time.Minute, Timeout: 30 * time.Second},
	}
}

// Connect implements integration.Integration.
func (Integration) Connect(_ context.Context, cfg integration.Config, d integration.Deps) (integration.Instance, error) {
	return &instance{cfg: cfg, d: d, base: strings.TrimRight(cfg.String("url"), "/"), token: cfg.String("token")}, nil
}

type instance struct {
	cfg   integration.Config
	d     integration.Deps
	base  string
	token string

	// From the last slow read.
	identity  identity
	libraries *libraries // nil until read
	readAt    time.Time
	recent    []map[string]any // nil until read
	recentAt  time.Time
}

// container is Plex's JSON envelope.
type container[T any] struct {
	MediaContainer T `json:"MediaContainer"`
}

type identity struct {
	MachineIdentifier string `json:"machineIdentifier"`
	Version           string `json:"version"`
	Claimed           bool   `json:"claimed"`
}

type sessions struct {
	Size     *int      `json:"size"`
	Metadata []session `json:"Metadata"`
}

type session struct {
	Type             string `json:"type"`
	Title            string `json:"title"`
	GrandparentTitle string `json:"grandparentTitle"` // the show, for an episode
	ParentIndex      *int   `json:"parentIndex"`      // the season
	Index            *int   `json:"index"`            // the episode
	Duration         int64  `json:"duration"`         // ms
	ViewOffset       int64  `json:"viewOffset"`       // ms
	Media            []struct {
		VideoResolution string `json:"videoResolution"`
	} `json:"Media"`
	User *struct {
		Title string `json:"title"`
	} `json:"User"`
	Player *struct {
		Title   string `json:"title"`
		Product string `json:"product"`
		State   string `json:"state"`
		Local   *bool  `json:"local"`
	} `json:"Player"`
	Session *struct {
		Bandwidth float64 `json:"bandwidth"` // kb/s
		Location  string  `json:"location"`  // lan or wan
	} `json:"Session"`
	TranscodeSession *struct {
		VideoDecision string `json:"videoDecision"`
		AudioDecision string `json:"audioDecision"`
	} `json:"TranscodeSession"`
}

// decision is Tautulli's word for how a session plays: "transcode" when
// Plex re-encodes the video or the audio, "direct stream" when it only
// remuxes, "direct play" otherwise.
func (s session) decision() string {
	switch t := s.TranscodeSession; {
	case t == nil:
		return "direct play"
	case t.VideoDecision == "transcode" || t.AudioDecision == "transcode":
		return "transcode"
	default:
		return "direct stream"
	}
}

// title is the session's title as Tautulli's full_title has it: "Show -
// Episode" for an episode, "Artist - Track" for a track.
func (s session) title() string {
	if (s.Type == "episode" || s.Type == "track") && s.GrandparentTitle != "" {
		return s.GrandparentTitle + " - " + s.Title
	}
	return s.Title
}

// remote is true when the player is outside the server's network.
func (s session) remote() bool {
	if s.Session != nil && s.Session.Location != "" {
		return s.Session.Location == "wan"
	}
	return s.Player != nil && s.Player.Local != nil && !*s.Player.Local
}

// playing is what the Sync publishes from one read of /status/sessions.
type playing struct {
	streams, transcode, remote int
	mbps                       float64
	list                       []map[string]any
}

type sections struct {
	Directory []struct {
		Key   string `json:"key"`
		Type  string `json:"type"` // movie, show, artist, photo
		Title string `json:"title"`
	} `json:"Directory"`
}

type sectionAll struct {
	TotalSize *int `json:"totalSize"`
	Size      int  `json:"size"`
}

func (a sectionAll) count() int {
	if a.TotalSize != nil {
		return *a.TotalSize
	}
	return a.Size
}

// libraries are the counts the Demo Home keeps in attrs (movies, shows),
// and the rest of what the sections hold.
type libraries struct {
	movies, shows, episodes, artists, sections int
}

type recentlyAdded struct {
	Metadata []struct {
		Type             string `json:"type"` // movie, show, season, episode, album, …
		Title            string `json:"title"`
		ParentTitle      string `json:"parentTitle"`      // the show for a season, the artist for an album
		GrandparentTitle string `json:"grandparentTitle"` // the show for an episode
		ParentIndex      *int   `json:"parentIndex"`
		Index            *int   `json:"index"`
		Year             int    `json:"year"`
		AddedAt          int64  `json:"addedAt"` // Unix seconds
		LibrarySection   string `json:"librarySectionTitle"`
	} `json:"Metadata"`
}

// get reads path into dst with the token in X-Plex-Token. A 401 is an auth
// Repair on token.
func (in *instance) get(ctx context.Context, path string, q url.Values, dst any) error {
	u := in.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return &integration.ConfigError{Field: "url", Message: "the URL can't be used: " + err.Error()}
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Plex-Token", in.token)
	resp, err := in.d.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return &integration.AuthError{Field: "token",
			Message: "Plex rejected the token: it was signed out, or the account isn't the server's owner. Sign in with Plex again"}
	case http.StatusNotFound:
		return &integration.RepairError{Code: integration.CodeAPIChanged, Field: "url",
			Message: "GET " + path + " answered 404 Not Found: check that the URL is a Plex Media Server"}
	}
	if err := integration.CheckResponse(resp); err != nil {
		return err
	}
	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		return &integration.RepairError{Code: integration.CodeAPIChanged, Field: "url",
			Message: "GET " + path + " didn't answer with Plex's JSON: check that the URL is a Plex Media Server"}
	}
	return nil
}

func (in *instance) readIdentity(ctx context.Context) error {
	var c container[identity]
	if err := in.get(ctx, "/identity", nil, &c); err != nil {
		return err
	}
	if c.MediaContainer.MachineIdentifier == "" {
		return &integration.RepairError{Code: integration.CodeAPIChanged, Field: "url", Message: "/identity has no machine identifier: check that the URL is a Plex Media Server"}
	}
	in.identity = c.MediaContainer
	return nil
}

// streams reads what's playing: the counts, the bandwidth and the first
// sessions as a list. Player addresses and ids aren't kept.
func (in *instance) streams(ctx context.Context) (playing, error) {
	var c container[sessions]
	if err := in.get(ctx, "/status/sessions", nil, &c); err != nil {
		return playing{}, err
	}
	p := playing{streams: len(c.MediaContainer.Metadata), list: []map[string]any{}}
	if c.MediaContainer.Size != nil {
		p.streams = *c.MediaContainer.Size
	}
	var kbps float64
	for _, s := range c.MediaContainer.Metadata {
		d := s.decision()
		if d == "transcode" {
			p.transcode++
		}
		if s.remote() {
			p.remote++
		}
		if s.Session != nil {
			kbps += s.Session.Bandwidth
		}
		if len(p.list) == playingRows || s.Title == "" {
			continue
		}
		row := map[string]any{"title": s.title(), "kind": s.Type, "decision": d, "remote": s.remote()}
		if s.User != nil && s.User.Title != "" {
			row["user"] = s.User.Title
		}
		if s.Player != nil {
			row["state"] = s.Player.State
			if s.Player.Title != "" {
				row["player"] = s.Player.Title
			} else if s.Player.Product != "" {
				row["player"] = s.Player.Product
			}
		}
		if s.Duration > 0 {
			row["progress"] = int(min(100, max(0, s.ViewOffset*100/s.Duration)))
		}
		if s.Type == "episode" && s.ParentIndex != nil && s.Index != nil {
			row["episode"] = fmt.Sprintf("S%d E%d", *s.ParentIndex, *s.Index)
		}
		if len(s.Media) > 0 && s.Media[0].VideoResolution != "" {
			row["resolution"] = resolution(s.Media[0].VideoResolution)
		}
		p.list = append(p.list, row)
	}
	p.mbps = math.Round(kbps/100) / 10
	return p, nil
}

// resolution is Plex's videoResolution as people say it: "1080" is 1080p,
// "4k" is 4K, "sd" is SD.
func resolution(r string) string {
	switch strings.ToLower(r) {
	case "4k":
		return "4K"
	case "sd":
		return "SD"
	}
	if strings.Trim(r, "0123456789") == "" {
		return r + "p"
	}
	return r
}

// readLibraries counts what the sections hold, one zero-size page each:
// movies, shows and their episodes (type 4), and music artists.
func (in *instance) readLibraries(ctx context.Context) (*libraries, error) {
	var c container[sections]
	if err := in.get(ctx, "/library/sections", nil, &c); err != nil {
		return nil, err
	}
	lib := &libraries{sections: len(c.MediaContainer.Directory)}
	count := func(key string, typ string) (int, error) {
		q := url.Values{"X-Plex-Container-Start": {"0"}, "X-Plex-Container-Size": {"0"}}
		if typ != "" {
			q.Set("type", typ)
		}
		var all container[sectionAll]
		if err := in.get(ctx, "/library/sections/"+url.PathEscape(key)+"/all", q, &all); err != nil {
			return 0, err
		}
		return all.MediaContainer.count(), nil
	}
	for _, s := range c.MediaContainer.Directory {
		var dst *int
		switch s.Type {
		case "movie":
			dst = &lib.movies
		case "show":
			dst = &lib.shows
		case "artist":
			dst = &lib.artists
		default:
			continue
		}
		n, err := count(s.Key, "")
		if err != nil {
			return nil, err
		}
		*dst += n
		if s.Type == "show" {
			if n, err = count(s.Key, "4"); err != nil {
				return nil, err
			}
			lib.episodes += n
		}
	}
	return lib, nil
}

// readRecent reads the newest items across the libraries, newest first.
// Plex groups a run of new episodes into their season. Photos and clips
// are left out.
func (in *instance) readRecent(ctx context.Context) ([]map[string]any, error) {
	var c container[recentlyAdded]
	q := url.Values{"X-Plex-Container-Start": {"0"}, "X-Plex-Container-Size": {fmt.Sprint(recentRows * 2)}}
	if err := in.get(ctx, "/library/recentlyAdded", q, &c); err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, m := range c.MediaContainer.Metadata {
		if len(out) == recentRows {
			break
		}
		row := map[string]any{"kind": m.Type, "at": m.AddedAt}
		switch m.Type {
		case "movie":
			row["title"] = m.Title
			if m.Year > 0 {
				row["detail"] = fmt.Sprint(m.Year)
			}
		case "show":
			row["title"] = m.Title
		case "season":
			row["title"], row["detail"] = m.ParentTitle, m.Title
		case "episode":
			row["title"], row["detail"] = m.GrandparentTitle, m.Title
			if m.ParentIndex != nil && m.Index != nil {
				row["detail"] = fmt.Sprintf("S%d E%d · %s", *m.ParentIndex, *m.Index, m.Title)
			}
		case "album":
			row["title"], row["detail"] = m.Title, m.ParentTitle
		default:
			continue
		}
		if row["title"] == "" || m.AddedAt <= 0 {
			continue
		}
		if m.LibrarySection != "" {
			row["library"] = m.LibrarySection
		}
		out = append(out, row)
	}
	return out, nil
}

// grant is what a Plex token can do: it's the account's, so everything the
// account can, the server's settings included. There are no read-only
// tokens.
func grant() integration.Grant {
	return integration.Grant{Name: "Plex account token", Access: integration.AccessAdmin,
		Detail: "a Plex token acts as the account: it can manage the server and the account; Plex has no read-only tokens"}
}

// Test implements integration.Instance: the server's identity, and the
// sessions (which need the owner's token).
func (in *instance) Test(ctx context.Context) integration.TestResult {
	if err := in.readIdentity(ctx); err != nil {
		return integration.Failed(err)
	}
	if _, err := in.streams(ctx); err != nil {
		return integration.Failed(err)
	}
	return integration.TestResult{OK: true, Version: in.identity.Version, Grants: []integration.Grant{grant()}}
}

// Close implements integration.Instance.
func (in *instance) Close() error { return nil }

// Sync implements integration.Instance.
func (in *instance) Sync(ctx context.Context, want integration.Interest) error {
	now := in.d.Now()
	if in.libraries == nil || (want.Watched && now.Sub(in.readAt) >= librariesEvery) {
		if err := in.readIdentity(ctx); err != nil {
			return err
		}
		in.d.Emit.SetVersion(in.identity.Version)
		lib, err := in.readLibraries(ctx)
		if err != nil {
			return err
		}
		in.libraries, in.readAt = lib, now
	}
	if in.recent == nil || (want.Watched && now.Sub(in.recentAt) >= recentEvery) {
		recent, err := in.readRecent(ctx)
		if err != nil {
			return err
		}
		in.recent, in.recentAt = recent, now
	}
	p, err := in.streams(ctx)
	if err != nil {
		return err
	}
	key := in.cfg.InstanceID
	if key == "" {
		key = "test"
	}
	rid := ID + ":" + key + "/app"
	attrs := map[string]any{
		"type": ID, "url": in.base, "version": in.identity.Version, "machine_identifier": in.identity.MachineIdentifier,
		"movies": in.libraries.movies, "shows": in.libraries.shows, "episodes": in.libraries.episodes, "artists": in.libraries.artists,
		"libraries": in.libraries.sections, "now_playing": p.list, "recently_added": in.recent, "streams_remote": p.remote,
	}
	res := integration.Resource{ID: rid, Kind: "app", Name: in.cfg.Name, State: "running",
		Health: integration.Health{Status: integration.HealthOK}, Attrs: attrs,
		Caps: []string{"media.sessions", "media.library"}, Links: []integration.Link{{Title: "Plex", URL: in.base}}}
	return in.d.Emit.Replace("", integration.Facts{
		Resources: []integration.Resource{res},
		Relations: []integration.Relation{{From: in.d.Emit.Self(), Type: "monitors", To: rid}},
		Metrics: []integration.Metric{
			{Resource: rid, Key: "streams", Unit: "count", Max: 10, Band: [2]float64{0, 3}, Value: float64(p.streams)},
			{Resource: rid, Key: "streams.transcode", Unit: "count", Max: 10, Band: [2]float64{0, 2}, Value: float64(p.transcode)},
			{Resource: rid, Key: "bandwidth.mbps", Unit: "Mb/s", Max: 1000, Band: [2]float64{0, 100}, Value: p.mbps},
		},
	})
}
