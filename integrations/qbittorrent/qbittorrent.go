// Package qbittorrent reads qBittorrent's WebUI API v2: transfer speeds and
// how many torrents are seeding, leeching and queued, through the
// incremental sync/maindata feed, signed in with a cookie session. See
// README.md.
package qbittorrent

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"math"
	"net/http"
	"net/url"
	"slices"
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
			integration.Prop("torrent_limit", integration.Integer("Torrents listed", 0, 1000).
				Describe("How many torrents the tile's Torrents view lists, worst first (default 200). Each one is sent to everyone who can see the tile; 0 lists none.")),
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
	in := &instance{cfg: cfg, d: d, base: strings.TrimRight(cfg.String("url"), "/"), http: d.HTTP, limit: listDefault}
	switch n := cfg.Values["torrent_limit"].(type) {
	case float64:
		in.limit = max(0, int(n))
	case int:
		in.limit = max(0, n)
	}
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

	// The sync/maindata feed: the last response id, each torrent by hash,
	// and the merged server state. Only Sync touches them.
	limit    int // how many torrents list() returns
	rid      int64
	torrents map[string]*torrent
	server   map[string]json.RawMessage
	version  string
	readVer  time.Time
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

// torrentDelta is a torrent's changed fields; torrent is what they add up to.
type torrentDelta struct {
	Name     *string  `json:"name"`
	State    *string  `json:"state"`
	Progress *float64 `json:"progress"`
	Size     *int64   `json:"size"`
	DLSpeed  *int64   `json:"dlspeed"`
	UPSpeed  *int64   `json:"upspeed"`
	ETA      *int64   `json:"eta"`
	Ratio    *float64 `json:"ratio"`
	AddedOn  *int64   `json:"added_on"`
}

type torrent struct {
	name, state      string
	progress         float64
	size             int64
	dlSpeed, upSpeed int64
	eta              int64
	ratio            float64
	addedOn          int64
}

func (t *torrent) apply(d torrentDelta) {
	if d.Name != nil {
		t.name = *d.Name
	}
	if d.State != nil {
		t.state = *d.State
	}
	if d.Progress != nil {
		t.progress = *d.Progress
	}
	if d.Size != nil {
		t.size = *d.Size
	}
	if d.DLSpeed != nil {
		t.dlSpeed = *d.DLSpeed
	}
	if d.UPSpeed != nil {
		t.upSpeed = *d.UPSpeed
	}
	if d.ETA != nil {
		t.eta = *d.ETA
	}
	if d.Ratio != nil {
		t.ratio = *d.Ratio
	}
	if d.AddedOn != nil {
		t.addedOn = *d.AddedOn
	}
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
	for _, t := range in.torrents {
		switch st := t.state; {
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
	attrs := map[string]any{"type": ID, "url": in.base, "torrents": len(in.torrents), "errored": errored}
	if in.version != "" {
		attrs["version"] = in.version
	}
	if conn != "" {
		attrs["connection_status"] = conn
	}
	if alt, ok := in.serverBool("use_alt_speed_limits"); ok {
		attrs["alt_speed"] = alt
	}
	if active := in.active(); len(active) > 0 {
		attrs["active"] = active
		if in.limit > 0 {
			attrs["list"] = in.list()
		}
	}
	if r, err := strconv.ParseFloat(in.serverString("global_ratio"), 64); err == nil {
		attrs["ratio"] = r
	}
	if free, ok := in.serverNumber("free_space_on_disk"); ok && free >= 0 {
		attrs["free_gib"] = math.Round(free/(1<<30)*10) / 10
	}
	if peers, ok := in.serverNumber("total_peer_connections"); ok {
		attrs["peers"] = int(peers)
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
	if md.FullUpdate || in.torrents == nil {
		in.torrents, in.server = map[string]*torrent{}, map[string]json.RawMessage{}
	}
	for hash, d := range md.Torrents {
		t, ok := in.torrents[hash]
		if !ok {
			t = &torrent{state: "unknown"}
			in.torrents[hash] = t
		}
		t.apply(d)
	}
	for _, hash := range md.TorrentsRemoved {
		delete(in.torrents, hash)
	}
	maps.Copy(in.server, md.ServerState)
	in.rid = md.RID
}

func (in *instance) serverString(k string) string {
	var s string
	_ = json.Unmarshal(in.server[k], &s)
	return s
}

func (in *instance) serverNumber(k string) (float64, bool) {
	var n float64
	if err := json.Unmarshal(in.server[k], &n); err != nil {
		return 0, false
	}
	return n, true
}

// maxActive is how many torrents the tile gets: it has room for a handful.
const maxActive = 8

// stateWords is a torrent's state in the words a tile shows.
func stateWords(st string) string {
	switch {
	case st == "downloading" || st == "forcedDL":
		return "downloading"
	case st == "stalledDL":
		return "stalled"
	case st == "metaDL" || st == "forcedMetaDL":
		return "fetching metadata"
	case st == "queuedDL" || st == "queuedUP":
		return "queued"
	case seedingStates[st]:
		return "seeding"
	case erroredStates[st]:
		return "error"
	case strings.HasPrefix(st, "stopped") || strings.HasPrefix(st, "paused"):
		return "stopped"
	}
	return st
}

// listDefault is how many torrents the Torrents view gets unless the
// instance's torrent_limit says otherwise.
const listDefault = 200

// rank orders the list by what needs a look: errors, then downloads, then
// the queue, then what uploads, then the rest.
func rank(st string) int {
	switch {
	case erroredStates[st]:
		return 0
	case st == "downloading" || st == "forcedDL":
		return 1
	case leechingStates[st]:
		return 2
	case st == "queuedDL":
		return 3
	case st == "uploading" || st == "forcedUP":
		return 4
	}
	return 5
}

// list is a compact table of the torrents, in rank order, for the Torrents
// view: name, status, progress, speeds and ratio.
func (in *instance) list() []map[string]any {
	all := make([]*torrent, 0, len(in.torrents))
	for _, t := range in.torrents {
		all = append(all, t)
	}
	slices.SortFunc(all, func(a, b *torrent) int {
		return cmp.Or(cmp.Compare(rank(a.state), rank(b.state)), cmp.Compare(b.dlSpeed+b.upSpeed, a.dlSpeed+a.upSpeed), strings.Compare(a.name, b.name))
	})
	out := make([]map[string]any, 0, min(len(all), in.limit))
	for _, t := range all[:min(len(all), in.limit)] {
		out = append(out, map[string]any{
			"name":     t.name,
			"state":    stateWords(t.state),
			"progress": int(math.Floor(t.progress * 100)),
			"dl_mbps":  math.Round(float64(t.dlSpeed)*8/1e5) / 10,
			"ul_mbps":  math.Round(float64(t.upSpeed)*8/1e5) / 10,
			"ratio":    math.Round(t.ratio*100) / 100,
			"added":    t.addedOn,
		})
	}
	return out
}

// active is what is moving, for the tile: the downloads first (fastest
// first, then the queue), then what is uploading. Names are the user's own
// torrents, so they're as visible as the tile is.
func (in *instance) active() []map[string]any {
	var down, up []*torrent
	for _, t := range in.torrents {
		switch {
		case leechingStates[t.state] || t.state == "queuedDL":
			down = append(down, t)
		case seedingStates[t.state] && t.upSpeed > 0:
			up = append(up, t)
		}
	}
	slices.SortFunc(down, func(a, b *torrent) int {
		return cmp.Or(cmp.Compare(b.dlSpeed, a.dlSpeed), cmp.Compare(b.progress, a.progress), strings.Compare(a.name, b.name))
	})
	slices.SortFunc(up, func(a, b *torrent) int {
		return cmp.Or(cmp.Compare(b.upSpeed, a.upSpeed), strings.Compare(a.name, b.name))
	})
	out := []map[string]any{}
	for _, t := range append(down, up...) {
		if len(out) == maxActive {
			break
		}
		item := map[string]any{
			"name":     t.name,
			"state":    stateWords(t.state),
			"progress": int(math.Floor(t.progress * 100)),
			"dl_mbps":  math.Round(float64(t.dlSpeed)*8/1e5) / 10,
			"ul_mbps":  math.Round(float64(t.upSpeed)*8/1e5) / 10,
			"size_gb":  math.Round(float64(t.size)/(1<<30)*10) / 10,
		}
		// 8640000 s is qBittorrent's "never".
		if t.dlSpeed > 0 && t.eta > 0 && t.eta < 8640000 {
			item["eta_s"] = t.eta
		}
		out = append(out, item)
	}
	return out
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
