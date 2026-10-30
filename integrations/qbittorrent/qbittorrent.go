// Package qbittorrent reads qBittorrent's WebUI API v2: transfer speeds and
// how many torrents are seeding, leeching and queued, through the
// incremental sync/maindata feed, signed in with a cookie session. See
// README.md.
package qbittorrent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"dash.cha.sh/internal/integration"
)

// ID is the integration's ID.
const ID = "qbittorrent"

// versionEvery is how often the version is read again while watched.
const versionEvery = time.Hour

func init() { integration.Register(Integration{}) }

// Integration is the qBittorrent integration.
type Integration struct{}

// Manifest implements integration.Integration.
func (Integration) Manifest() integration.Manifest {
	user := integration.String("Username").Describe("The WebUI username (Tools → Options → WebUI). Leave both empty if the WebUI lets the hub in without one.")
	pass := integration.Secret("Password").Describe("The WebUI password.")
	return integration.Manifest{
		ID: ID, Name: "qBittorrent", Icon: "qbittorrent", Version: "0.1.0", Tier: integration.Silver,
		Category:     integration.CatDownloads,
		Capabilities: []integration.Capability{"downloads.queue"},
		ConfigSchema: integration.Object(
			integration.Prop("url", integration.URL("URL").Describe("The WebUI's address.")),
			integration.TLSProp(),
			integration.Prop("username", user),
			integration.Prop("password", pass),
		).Require("url"),
		Poll: integration.Poll{Interval: 30 * time.Second, Idle: 5 * time.Minute, Timeout: 30 * time.Second},
	}
}

// ValidateConfig implements integration.ConfigValidator: a username and a
// password go together.
func (Integration) ValidateConfig(cfg integration.Config) []integration.FieldError {
	user, pass := cfg.String("username"), cfg.String("password")
	switch {
	case user != "" && pass == "":
		return []integration.FieldError{{Path: "/password", Message: "is required with a username"}}
	case user == "" && pass != "":
		return []integration.FieldError{{Path: "/username", Message: "is required with a password"}}
	}
	return nil
}

// Connect implements integration.Integration.
func (Integration) Connect(_ context.Context, cfg integration.Config, d integration.Deps) (integration.Instance, error) {
	in := &instance{cfg: cfg, d: d, base: strings.TrimRight(cfg.String("url"), "/"), http: d.HTTP}
	if user := cfg.String("username"); user != "" {
		s := &session{base: in.base, user: user, pass: cfg.String("password")}
		c, err := d.NewHTTPClient(integration.HTTPOptions{Session: s})
		if err != nil {
			return nil, err
		}
		in.http = c
	}
	return in, nil
}

// session is the WebUI's cookie login: POST /api/v2/auth/login answers
// "Ok." with a session cookie (SID, or QBT_SID_<port> in newer versions;
// every cookie it sets is kept), and an expired session gets 403, so the
// framework logs in again and retries.
type session struct {
	base, user, pass string

	mu      sync.Mutex
	cookies []*http.Cookie
}

// Login implements integration.Session. No Referer or Origin is sent:
// qBittorrent's CSRF check passes requests that carry neither, and a
// Referer would fail behind a proxy that rewrites Host.
func (s *session) Login(ctx context.Context, c *http.Client) error {
	form := url.Values{"username": {s.user}, "password": {s.pass}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.base+"/api/v2/auth/login", strings.NewReader(form.Encode()))
	if err != nil {
		return &integration.ConfigError{Field: "url", Message: "the URL can't be used: " + err.Error()}
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	answer := strings.TrimSpace(string(body))
	switch {
	case resp.StatusCode == http.StatusForbidden:
		return &integration.AuthError{Field: "password",
			Message: "qBittorrent has banned the hub's address after too many failed sign-ins; fix the password, then wait for the ban to lift (Options → WebUI → ban duration)"}
	case resp.StatusCode == http.StatusUnauthorized || answer == "Fails.":
		return &integration.AuthError{Field: "password", Message: "qBittorrent rejected the username or password"}
	case resp.StatusCode == http.StatusNotFound:
		return apiChanged("/api/v2/auth/login")
	}
	if err := integration.CheckResponse(resp); err != nil {
		return err
	}
	var kept []*http.Cookie
	for _, ck := range resp.Cookies() {
		if ck.Value != "" {
			kept = append(kept, &http.Cookie{Name: ck.Name, Value: ck.Value})
		}
	}
	s.mu.Lock()
	s.cookies = kept
	s.mu.Unlock()
	return nil
}

// Apply implements integration.Session.
func (s *session) Apply(r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ck := range s.cookies {
		r.AddCookie(ck)
	}
}

// Logout implements integration.SessionLogout.
func (s *session) Logout(ctx context.Context, c *http.Client) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.base+"/api/v2/auth/logout", nil)
	if err != nil {
		return err
	}
	s.Apply(req)
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func apiChanged(path string) error {
	return &integration.RepairError{Code: integration.CodeAPIChanged, Field: "url",
		Message: "POST/GET " + path + " answered 404 Not Found: check the URL, and that it's qBittorrent 4.1 or later (WebUI API v2)"}
}

type instance struct {
	cfg  integration.Config
	d    integration.Deps
	base string
	http *http.Client

	// The sync/maindata feed: the last response id, each torrent's state by
	// hash, and the merged server state. Only Sync touches them.
	rid     int64
	states  map[string]string
	server  map[string]json.RawMessage
	version string
	readVer time.Time
}

// Close implements integration.Instance; the framework logs the session out.
func (in *instance) Close() error { return nil }

// get reads path into dst (a *string for text answers).
func (in *instance) get(ctx context.Context, path string, q url.Values, dst any) error {
	u := in.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return &integration.ConfigError{Field: "url", Message: "the URL can't be used: " + err.Error()}
	}
	resp, err := in.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return apiChanged(path)
	}
	if resp.StatusCode == http.StatusForbidden && in.cfg.String("username") == "" {
		return &integration.AuthError{Field: "username", Message: "qBittorrent wants a sign-in: enter the WebUI username and password"}
	}
	if err := integration.CheckResponse(resp); err != nil {
		return err
	}
	if s, ok := dst.(*string); ok {
		b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		*s = strings.TrimSpace(string(b))
		return err
	}
	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		return &integration.RepairError{Code: integration.CodeAPIChanged, Field: "url",
			Message: "GET " + path + " didn't answer with qBittorrent's JSON: check the URL"}
	}
	return nil
}

// readVersion reads the app and Web API versions and checks the API is v2.
func (in *instance) readVersion(ctx context.Context) error {
	var app, api string
	if err := in.get(ctx, "/api/v2/app/version", nil, &app); err != nil {
		return err
	}
	if err := in.get(ctx, "/api/v2/app/webapiVersion", nil, &api); err != nil {
		return err
	}
	if !strings.HasPrefix(api, "2.") {
		return &integration.RepairError{Code: integration.CodeUnsupportedVersion,
			Message: fmt.Sprintf("qBittorrent's Web API %s isn't supported (2.x, from qBittorrent 4.1)", api)}
	}
	in.version = strings.TrimPrefix(app, "v")
	return nil
}

// Grant is what the WebUI account can do: everything. qBittorrent has one
// WebUI account and no read-only mode.
func grant(cfg integration.Config) integration.Grant {
	g := integration.Grant{Name: "qBittorrent WebUI", Access: integration.AccessAdmin,
		Detail: "the WebUI account can add, remove and change torrents and settings; qBittorrent has no read-only accounts"}
	if cfg.String("username") == "" {
		g.Detail = "the WebUI lets this address in without signing in, with full control"
	}
	return g
}

// Test implements integration.Instance.
func (in *instance) Test(ctx context.Context) integration.TestResult {
	if err := in.readVersion(ctx); err != nil {
		return integration.Failed(err)
	}
	return integration.TestResult{OK: true, Version: in.version, Grants: []integration.Grant{grant(in.cfg)}}
}

// maindata is a sync/maindata answer. Apart from the first (full_update),
// torrents and server_state carry only the fields that changed.
type maindata struct {
	RID             int64                      `json:"rid"`
	FullUpdate      bool                       `json:"full_update"`
	Torrents        map[string]torrentDelta    `json:"torrents"`
	TorrentsRemoved []string                   `json:"torrents_removed"`
	ServerState     map[string]json.RawMessage `json:"server_state"`
}

type torrentDelta struct {
	State *string `json:"state"`
}

// Torrent states, grouped the way the WebUI's filters group them.
// qBittorrent 5 renamed paused* to stopped*.
var (
	seedingStates  = set("uploading", "stalledUP", "forcedUP")
	leechingStates = set("downloading", "stalledDL", "forcedDL", "metaDL", "forcedMetaDL")
	queuedStates   = set("queuedDL", "queuedUP")
	erroredStates  = set("error", "missingFiles")
)

func set(xs ...string) map[string]bool {
	m := map[string]bool{}
	for _, x := range xs {
		m[x] = true
	}
	return m
}

// Sync implements integration.Instance: one sync/maindata request, folded
// into what the earlier ones said.
func (in *instance) Sync(ctx context.Context, want integration.Interest) error {
	now := in.d.Now()
	if in.version == "" || (want.Watched && now.Sub(in.readVer) >= versionEvery) {
		if err := in.readVersion(ctx); err != nil {
			return err
		}
		in.readVer = now
		in.d.Emit.SetVersion(in.version)
	}
	var md maindata
	if err := in.get(ctx, "/api/v2/sync/maindata", url.Values{"rid": {strconv.FormatInt(in.rid, 10)}}, &md); err != nil {
		in.rid = 0 // start over with a full update
		return err
	}
	in.fold(md)

	var seeding, leeching, queued, errored int
	for _, st := range in.states {
		switch {
		case seedingStates[st]:
			seeding++
		case leechingStates[st]:
			leeching++
		case queuedStates[st]:
			queued++
		case erroredStates[st]:
			errored++
		}
	}
	conn := in.serverString("connection_status")
	h := integration.Health{Status: integration.HealthOK}
	switch {
	case conn == "disconnected":
		h = integration.Health{Status: integration.HealthAttention, Reason: "qBittorrent has no network connection"}
	case errored > 0:
		h = integration.Health{Status: integration.HealthAttention, Reason: fmt.Sprintf("%d %s an error", errored, plural(errored, "torrent has", "torrents have"))}
	}
	key := in.cfg.InstanceID
	if key == "" {
		key = "test"
	}
	rid := ID + ":" + key + "/app"
	attrs := map[string]any{"type": ID, "url": in.base, "torrents": len(in.states), "errored": errored}
	if in.version != "" {
		attrs["version"] = in.version
	}
	if conn != "" {
		attrs["connection_status"] = conn
	}
	if alt, ok := in.serverBool("use_alt_speed_limits"); ok {
		attrs["alt_speed"] = alt
	}
	res := integration.Resource{ID: rid, Kind: "app", Name: in.cfg.Name, State: "running", Health: h, Attrs: attrs,
		Caps: []string{"downloads.queue"}, Links: []integration.Link{{Title: "qBittorrent", URL: in.base}}}
	count := func(k string, max float64, band [2]float64, v int) integration.Metric {
		return integration.Metric{Resource: rid, Key: k, Unit: "count", Max: max, Band: band, Value: float64(v)}
	}
	speed := func(k, field string, band [2]float64) integration.Metric {
		return integration.Metric{Resource: rid, Key: k, Unit: "Mb/s", Max: 1000, Band: band, Value: in.mbps(field)}
	}
	return in.d.Emit.Replace("", integration.Facts{
		Resources: []integration.Resource{res},
		Relations: []integration.Relation{{From: in.d.Emit.Self(), Type: "monitors", To: rid}},
		Metrics: []integration.Metric{
			speed("dl.mbps", "dl_info_speed", [2]float64{0, 60}),
			speed("ul.mbps", "up_info_speed", [2]float64{0, 20}),
			count("seeding", 10000, [2]float64{50, 100}, seeding),
			count("leeching", 1000, [2]float64{0, 30}, leeching),
			count("queued", 1000, [2]float64{0, 10}, queued),
		},
	})
}

// fold applies one maindata answer.
func (in *instance) fold(md maindata) {
	if md.FullUpdate || in.states == nil {
		in.states, in.server = map[string]string{}, map[string]json.RawMessage{}
	}
	for hash, t := range md.Torrents {
		if t.State != nil {
			in.states[hash] = *t.State
		} else if _, ok := in.states[hash]; !ok {
			in.states[hash] = "unknown"
		}
	}
	for _, hash := range md.TorrentsRemoved {
		delete(in.states, hash)
	}
	maps.Copy(in.server, md.ServerState)
	in.rid = md.RID
}

func (in *instance) serverString(k string) string {
	var s string
	_ = json.Unmarshal(in.server[k], &s)
	return s
}

func (in *instance) serverBool(k string) (bool, bool) {
	var b bool
	if err := json.Unmarshal(in.server[k], &b); err != nil {
		return false, false
	}
	return b, true
}

// mbps is a server_state speed (bytes per second) in megabits per second,
// to 0.1, or unknown.
func (in *instance) mbps(k string) float64 {
	var bps float64
	if err := json.Unmarshal(in.server[k], &bps); err != nil {
		return integration.Unknown()
	}
	return math.Round(bps*8/1e5) / 10
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
