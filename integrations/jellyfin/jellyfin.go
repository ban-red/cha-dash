// Package jellyfin reads a Jellyfin server: what's playing, and how big the
// libraries are. It feeds the Now playing tile the way Plex does. See
// README.md.
package jellyfin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"dash.cha.sh/internal/integration"
)

// ID is the integration's ID.
const ID = "jellyfin"

// slowEvery is how often the server info and library counts are read again
// while someone watches.
const slowEvery = 30 * time.Minute

// activeWithin is the sessions window, in seconds: what Jellyfin's own
// dashboard asks for.
const activeWithin = "960"

// MinVersion is the oldest Jellyfin this integration reads.
var MinVersion = [2]int{10, 8}

func init() { integration.Register(Integration{}) }

// Integration is the Jellyfin integration.
type Integration struct{}

// Manifest implements integration.Integration.
func (Integration) Manifest() integration.Manifest {
	key := integration.Secret("API key").Describe("Dashboard → API Keys → New API key. Jellyfin's API keys act as an administrator.")
	return integration.Manifest{
		ID: ID, Name: "Jellyfin", Icon: "jellyfin", Version: "0.1.0", Tier: integration.Silver,
		Category:     integration.CatMedia,
		Capabilities: []integration.Capability{"media.sessions", "media.library"},
		ConfigSchema: integration.Object(
			integration.Prop("url", integration.URL("URL").Describe("Your Jellyfin server: https://jellyfin.lab.example, or http://<address>:8096.")),
			integration.TLSProp(),
			integration.Prop("api_key", key),
		).Require("url", "api_key"),
		Poll: integration.Poll{Interval: 30 * time.Second, Idle: 5 * time.Minute, Timeout: 30 * time.Second},
	}
}

// ValidateConfig implements integration.ConfigValidator.
func (Integration) ValidateConfig(cfg integration.Config) []integration.FieldError {
	if _, err := baseURL(cfg.String("url")); err != nil {
		return []integration.FieldError{{Path: "/url", Message: err.Error()}}
	}
	return nil
}

// baseURL is the server's root. The web app's address (…/web or …/web/) is
// taken as its parent, since that's what the browser shows.
func baseURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("must be an http:// or https:// URL")
	}
	u.RawQuery, u.Fragment, u.RawPath = "", "", ""
	u.Path = strings.TrimSuffix(strings.TrimSuffix(u.Path, "/"), "/web")
	return strings.TrimRight(u.String(), "/"), nil
}

// Connect implements integration.Integration.
func (Integration) Connect(_ context.Context, cfg integration.Config, d integration.Deps) (integration.Instance, error) {
	base, err := baseURL(cfg.String("url"))
	if err != nil {
		return nil, &integration.ConfigError{Field: "url", Message: "url " + err.Error()}
	}
	key := cfg.InstanceID
	if key == "" {
		key = "test"
	}
	return &instance{cfg: cfg, d: d, base: base, link: cfg.String("url"), token: cfg.String("api_key"), rid: ID + ":" + key + "/app"}, nil
}

type instance struct {
	cfg   integration.Config
	d     integration.Deps
	base  string
	link  string // the URL as configured
	token string
	rid   string

	// From the last slow read; only Sync touches them.
	info   *systemInfo
	counts *itemCounts
	readAt time.Time
}

// systemInfo is GET /System/Info (the fields read).
type systemInfo struct {
	ServerName        string `json:"ServerName"`
	Version           string `json:"Version"`
	ProductName       string `json:"ProductName"`
	ID                string `json:"Id"`
	HasPendingRestart bool   `json:"HasPendingRestart"`
}

// itemCounts is GET /Items/Counts.
type itemCounts struct {
	MovieCount   int `json:"MovieCount"`
	SeriesCount  int `json:"SeriesCount"`
	EpisodeCount int `json:"EpisodeCount"`
	SongCount    int `json:"SongCount"`
	AlbumCount   int `json:"AlbumCount"`
	BookCount    int `json:"BookCount"`
}

// session is one entry of GET /Sessions (the fields read).
type session struct {
	NowPlayingItem *struct{} `json:"NowPlayingItem"` // null or absent when nothing plays
	PlayState      struct {
		PlayMethod string `json:"PlayMethod"` // DirectPlay, DirectStream, Transcode
	} `json:"PlayState"`
}

// authorization is the header Jellyfin reads an API key from. X-Emby-Token
// and the api_key parameter are legacy forms that newer servers turn off
// (EnableLegacyAuthorization).
func (in *instance) authorization() string {
	t := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(in.token)
	return `MediaBrowser Token="` + t + `"`
}

// get reads path into dst. A 401 or 403 is an auth Repair on api_key.
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
	req.Header.Set("Authorization", in.authorization())
	resp, err := in.d.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return &integration.AuthError{Field: "api_key",
			Message: "Jellyfin rejected the API key: it was deleted, or it isn't one. Create one under Dashboard → API Keys"}
	case http.StatusNotFound:
		return &integration.RepairError{Code: integration.CodeAPIChanged, Field: "url",
			Message: "GET " + path + " answered 404 Not Found: check that the URL is a Jellyfin server"}
	}
	if err := integration.CheckResponse(resp); err != nil {
		return err
	}
	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		return &integration.RepairError{Code: integration.CodeAPIChanged, Field: "url",
			Message: "GET " + path + " didn't answer with Jellyfin's JSON: check that the URL is a Jellyfin server"}
	}
	return nil
}

// readInfo reads the server's identity and checks it's a Jellyfin we read.
func (in *instance) readInfo(ctx context.Context) (*systemInfo, error) {
	var si systemInfo
	if err := in.get(ctx, "/System/Info", nil, &si); err != nil {
		return nil, err
	}
	if si.ProductName != "" && !strings.Contains(si.ProductName, "Jellyfin") {
		return nil, &integration.RepairError{Code: integration.CodeAPIChanged, Field: "url",
			Message: fmt.Sprintf("the URL answers as %s, not Jellyfin", si.ProductName)}
	}
	if si.ID == "" || si.Version == "" {
		return nil, &integration.RepairError{Code: integration.CodeAPIChanged, Field: "url",
			Message: "/System/Info has no server id or version: check that the URL is a Jellyfin server"}
	}
	if older(si.Version, MinVersion) {
		return nil, &integration.RepairError{Code: integration.CodeUnsupportedVersion,
			Message: fmt.Sprintf("Jellyfin %s is older than this integration supports (%d.%d or later)", si.Version, MinVersion[0], MinVersion[1])}
	}
	return &si, nil
}

// older reports whether version v ("10.10.7") is older than major.minor.
// A version it can't parse isn't called old.
func older(v string, min [2]int) bool {
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 2 {
		return false
	}
	major, err1 := strconv.Atoi(parts[0])
	minor, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return false
	}
	return major < min[0] || major == min[0] && minor < min[1]
}

// streams counts the sessions playing something (paused ones included) and
// those of them transcoding.
func (in *instance) streams(ctx context.Context) (playing, transcoding int, err error) {
	var ss []session
	if err := in.get(ctx, "/Sessions", url.Values{"activeWithinSeconds": {activeWithin}}, &ss); err != nil {
		return 0, 0, err
	}
	for _, s := range ss {
		if s.NowPlayingItem == nil {
			continue
		}
		playing++
		if s.PlayState.PlayMethod == "Transcode" {
			transcoding++
		}
	}
	return playing, transcoding, nil
}

// grant is what a Jellyfin API key can do: everything an administrator can.
func grant() integration.Grant {
	return integration.Grant{Name: "Jellyfin API key", Access: integration.AccessAdmin,
		Detail: "Jellyfin's API keys act as an administrator: they can manage users, libraries and the server; there are no read-only keys"}
}

// Test implements integration.Instance: the server's identity, and the
// sessions (which check the key).
func (in *instance) Test(ctx context.Context) integration.TestResult {
	si, err := in.readInfo(ctx)
	if err != nil {
		return integration.Failed(err)
	}
	if _, _, err := in.streams(ctx); err != nil {
		r := integration.Failed(err)
		r.Version = si.Version
		return r
	}
	return integration.TestResult{OK: true, Version: si.Version, Grants: []integration.Grant{grant()}}
}

// Close implements integration.Instance.
func (in *instance) Close() error { return nil }

// Sync implements integration.Instance.
func (in *instance) Sync(ctx context.Context, want integration.Interest) error {
	now := in.d.Now()
	if in.info == nil || (want.Watched && now.Sub(in.readAt) >= slowEvery) {
		si, err := in.readInfo(ctx)
		if err != nil {
			return err
		}
		in.d.Emit.SetVersion(si.Version)
		var c itemCounts
		if err := in.get(ctx, "/Items/Counts", nil, &c); err != nil {
			return err
		}
		in.info, in.counts, in.readAt = si, &c, now
	}
	playing, transcoding, err := in.streams(ctx)
	if err != nil {
		return err
	}
	attrs := map[string]any{
		"type": ID, "url": in.base, "version": in.info.Version, "server_id": in.info.ID, "server_name": in.info.ServerName,
		"pending_restart": in.info.HasPendingRestart,
		"movies":          in.counts.MovieCount, "shows": in.counts.SeriesCount, "episodes": in.counts.EpisodeCount,
		"albums": in.counts.AlbumCount, "songs": in.counts.SongCount, "books": in.counts.BookCount,
	}
	res := integration.Resource{ID: in.rid, Kind: "app", Name: in.cfg.Name, State: "running",
		Health: integration.Health{Status: integration.HealthOK}, Attrs: attrs,
		Caps: []string{"media.sessions", "media.library"}, Links: []integration.Link{{Title: "Jellyfin", URL: in.link}}}
	return in.d.Emit.Replace("", integration.Facts{
		Resources: []integration.Resource{res},
		Relations: []integration.Relation{{From: in.d.Emit.Self(), Type: "monitors", To: in.rid}},
		Metrics: []integration.Metric{
			{Resource: in.rid, Key: "streams", Unit: "count", Max: 10, Band: [2]float64{0, 3}, Value: float64(playing)},
			{Resource: in.rid, Key: "streams.transcode", Unit: "count", Max: 10, Band: [2]float64{0, 2}, Value: float64(transcoding)},
		},
	})
}
