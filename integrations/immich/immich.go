// Package immich is the Immich integration: the server's version, and its
// photo, video and storage statistics. Server-wide statistics need an
// admin's API key; with any other key it counts the key's own library and
// says so in a warning. See README.md.
package immich

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"dash.cha.sh/internal/integration"
)

// ID is the integration's ID.
const ID = "immich"

// MinVersion is the oldest Immich this integration reads: 1.107 moved the
// server endpoints from /api/server-info to /api/server.
var MinVersion = Version{Major: 1, Minor: 107}

// Polling: statistics change slowly, and the statistics query walks every
// asset, so the plan is gentler than the default.
const (
	interval = 2 * time.Minute
	idle     = 10 * time.Minute
)

// maxBody caps one response. The largest, server statistics, carries a row
// per user.
const maxBody = 4 << 20

func init() { integration.Register(Integration{}) }

// Integration is the Immich integration.
type Integration struct{}

// Manifest implements integration.Integration.
func (Integration) Manifest() integration.Manifest {
	key := integration.Secret("API key").Describe("Account Settings → API Keys. An admin's key with the server.statistics permission reads server-wide statistics; any other key reads its own library.")
	return integration.Manifest{
		ID: ID, Name: "Immich", Icon: "immich", Version: "0.1.0", Tier: integration.Silver,
		Capabilities: []integration.Capability{"photos.library"},
		ConfigSchema: integration.Object(integration.URLProp(), integration.TLSProp(), integration.Prop("api_key", key)).Require("url", "api_key"),
		Poll:         integration.Poll{Interval: interval, Idle: idle, Timeout: 30 * time.Second},
	}
}

// ValidateConfig implements integration.ConfigValidator: Immich is reached
// over http or https.
func (Integration) ValidateConfig(cfg integration.Config) []integration.FieldError {
	if _, err := baseURL(cfg.String("url")); err != nil {
		return []integration.FieldError{{Path: "/url", Message: err.Error()}}
	}
	return nil
}

// baseURL is the server's root. A URL that ends in /api (the API's own
// address, a common way to write it) is taken as its parent.
func baseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, errors.New("must be an http:// or https:// URL")
	}
	u.RawQuery, u.Fragment, u.RawPath = "", "", ""
	u.Path = strings.TrimSuffix(strings.TrimSuffix(u.Path, "/"), "/api")
	return u, nil
}

// Connect implements integration.Integration.
func (Integration) Connect(_ context.Context, cfg integration.Config, d integration.Deps) (integration.Instance, error) {
	base, err := baseURL(cfg.String("url"))
	if err != nil {
		return nil, &integration.ConfigError{Message: "url " + err.Error(), Field: "url"}
	}
	key := cfg.InstanceID
	if key == "" {
		key = "test"
	}
	name := cfg.Name
	if name == "" {
		name = "Immich"
	}
	return &instance{d: d, base: base, link: cfg.String("url"), key: cfg.String("api_key"),
		rid: ID + ":" + key + "/app", name: name}, nil
}

type instance struct {
	d    integration.Deps
	base *url.URL
	link string // the URL as configured, for the resource's link
	key  string
	rid  string
	name string

	// Guarded by the single Sync goroutine: the last statistics read, kept
	// for syncs nobody watches.
	stats *stats
}

// Close implements integration.Instance.
func (in *instance) Close() error { return nil }

// Version is Immich's version, as GET /api/server/version reports it.
type Version struct {
	Major int `json:"major"`
	Minor int `json:"minor"`
	Patch int `json:"patch"`
}

func (v Version) String() string { return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch) }

// Less reports whether v is older than w.
func (v Version) Less(w Version) bool {
	if v.Major != w.Major {
		return v.Major < w.Major
	}
	if v.Minor != w.Minor {
		return v.Minor < w.Minor
	}
	return v.Patch < w.Patch
}

// get fetches path and decodes the JSON answer into dst. Statuses outside
// 2xx are CheckResponse's errors; callers that tolerate a 403 or a 404 look
// at statusOf.
func (in *instance) get(ctx context.Context, path string, dst any) error {
	u := *in.base
	u.Path += path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("x-api-key", in.key)
	resp, err := in.d.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := integration.CheckResponse(resp); err != nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return err
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return err
	}
	if len(b) > maxBody {
		return fmt.Errorf("GET %s answered more than %d bytes", u.Path, maxBody)
	}
	if err := json.Unmarshal(b, dst); err != nil {
		return fmt.Errorf("GET %s didn't answer Immich's JSON: %w", u.Path, err)
	}
	return nil
}

// statusOf is the HTTP status of a failed request, or 0.
func statusOf(err error) int {
	if se, ok := errors.AsType[*integration.StatusError](err); ok {
		return se.Code
	}
	return 0
}

// gone turns a 404 into "the API changed", naming the version the server
// reported; other errors pass through.
func gone(err error, v Version) error {
	if statusOf(err) != http.StatusNotFound {
		return err
	}
	return &integration.RepairError{Code: integration.CodeAPIChanged,
		Message: fmt.Sprintf("Immich's API changed: %v (detected version %s)", err, v)}
}

// version reads the server's version (public, no key needed). An Immich
// older than MinVersion answers only at /api/server-info: that's an
// unsupported version, with the version it reported.
func (in *instance) version(ctx context.Context) (Version, error) {
	var v Version
	err := in.get(ctx, "/api/server/version", &v)
	if err == nil {
		return v, nil
	}
	if statusOf(err) != http.StatusNotFound {
		return v, err
	}
	var old Version
	if in.get(ctx, "/api/server-info/version", &old) == nil && old.Major > 0 {
		return old, &integration.RepairError{Code: integration.CodeUnsupportedVersion,
			Message: fmt.Sprintf("Immich %s is older than this integration supports (%d.%d or later).", old, MinVersion.Major, MinVersion.Minor)}
	}
	return v, &integration.RepairError{Code: integration.CodeAPIChanged,
		Message: "Immich's API changed: " + err.Error()}
}

// about is GET /api/server/about, the fields this integration shows.
type about struct {
	Version  string `json:"version"`
	Licensed *bool  `json:"licensed"`
}

// readAbout reads the server's about page. It needs a valid key, so it's
// how an unwatched sync notices a revoked one (401). A key without the
// server.about permission (403) is fine: about stays nil.
func (in *instance) readAbout(ctx context.Context, v Version) (*about, error) {
	var a about
	err := in.get(ctx, "/api/server/about", &a)
	switch {
	case err == nil:
		return &a, nil
	case statusOf(err) == http.StatusForbidden:
		return nil, nil
	}
	return nil, gone(err, v)
}

// Statistics scopes.
const (
	scopeServer = "server" // every user's library: an admin's key
	scopeUser   = "user"   // the key's own library: the fallback
)

// stats is what the statistics endpoints said. Counts are -1 when unknown.
type stats struct {
	scope          string
	photos, videos int64
	bytes          int64
	users          int // server scope only
	warning        *integration.Warning
}

// serverStats is GET /api/server/statistics (admin only).
type serverStats struct {
	Photos      int64             `json:"photos"`
	Videos      int64             `json:"videos"`
	Usage       int64             `json:"usage"`
	UsageByUser []json.RawMessage `json:"usageByUser"`
}

// assetStats is GET /api/assets/statistics: the key's own library.
type assetStats struct {
	Images int64 `json:"images"`
	Videos int64 `json:"videos"`
}

// me is the part of GET /api/users/me this integration reads.
type me struct {
	IsAdmin           bool   `json:"isAdmin"`
	QuotaUsageInBytes *int64 `json:"quotaUsageInBytes"`
}

// readStats reads the server's statistics, or, when the key may not (403),
// the key's own library, with a warning saying so.
func (in *instance) readStats(ctx context.Context, v Version) (*stats, error) {
	var ss serverStats
	err := in.get(ctx, "/api/server/statistics", &ss)
	if err == nil {
		return &stats{scope: scopeServer, photos: ss.Photos, videos: ss.Videos, bytes: ss.Usage, users: len(ss.UsageByUser)}, nil
	}
	if statusOf(err) != http.StatusForbidden {
		return nil, gone(err, v)
	}
	st := &stats{scope: scopeUser, photos: -1, videos: -1, bytes: -1}
	var admin *bool
	var u me
	switch err := in.get(ctx, "/api/users/me", &u); {
	case err == nil:
		admin = &u.IsAdmin
		if u.QuotaUsageInBytes != nil {
			st.bytes = *u.QuotaUsageInBytes
		}
	case statusOf(err) != http.StatusForbidden:
		return nil, gone(err, v)
	}
	var as assetStats
	switch err := in.get(ctx, "/api/assets/statistics", &as); {
	case err == nil:
		st.photos, st.videos = as.Images, as.Videos
	case statusOf(err) != http.StatusForbidden:
		return nil, gone(err, v)
	}
	until := "Until then, photos, videos and storage count only the key's own library."
	if st.photos < 0 && st.bytes < 0 {
		until = "The key can't read its own library's statistics either, so photos, videos and storage are unknown."
	}
	st.warning = statsWarning(admin, until)
	return st, nil
}

// statsWarning explains why the server's statistics are out of reach.
func statsWarning(admin *bool, until string) *integration.Warning {
	why := "Immich's server statistics need an admin's API key with the server.statistics permission."
	switch {
	case admin != nil && *admin:
		why = "This admin's API key lacks the server.statistics permission that Immich's server statistics need."
	case admin != nil:
		why = "Immich's server statistics need an admin's API key, and this key's user isn't an admin."
	}
	return &integration.Warning{Code: "statistics_not_admin", Severity: integration.WarnInfo, Message: why + " " + until}
}

// Sync implements integration.Instance. The version and the about page run
// on every sync; statistics only while someone is watching.
func (in *instance) Sync(ctx context.Context, want integration.Interest) error {
	v, err := in.version(ctx)
	if v.Major > 0 {
		in.d.Emit.SetVersion(v.String())
	}
	if err != nil {
		return err
	}
	ab, err := in.readAbout(ctx, v)
	if err != nil {
		return err
	}
	if want.Watched {
		st, err := in.readStats(ctx, v)
		if err != nil {
			return err
		}
		in.stats = st
	}
	if err := in.d.Emit.Replace("", integration.Facts{
		Resources: []integration.Resource{in.app(v, ab)},
		Relations: []integration.Relation{{From: in.d.Emit.Self(), Type: "monitors", To: in.rid}},
	}); err != nil {
		return err
	}
	if !want.Watched {
		return nil
	}
	var ws []integration.Warning
	if in.stats.warning != nil {
		ws = append(ws, *in.stats.warning)
	}
	in.d.Emit.SetWarnings(ws)
	return in.d.Emit.Replace("stats", integration.Facts{Metrics: in.metrics()})
}

// app is the instance's service resource.
func (in *instance) app(v Version, ab *about) integration.Resource {
	attrs := map[string]any{"type": ID, "url": in.link, "version": v.String()}
	if ab != nil && ab.Licensed != nil {
		attrs["licensed"] = *ab.Licensed
	}
	if st := in.stats; st != nil {
		attrs["stats_scope"] = st.scope
		if st.scope == scopeServer {
			attrs["users"] = st.users
		}
	}
	return integration.Resource{
		ID: in.rid, Kind: "app", Name: in.name, State: "running",
		Health: integration.Health{Status: integration.HealthOK},
		Attrs:  attrs,
		Links:  []integration.Link{{Title: "Immich", URL: in.link}},
		Caps:   []string{"photos.library"},
	}
}

// metrics are the Demo Home's Immich series, on its scales.
func (in *instance) metrics() []integration.Metric {
	st := in.stats
	count := func(n int64) float64 {
		if n < 0 {
			return integration.Unknown()
		}
		return float64(n)
	}
	gib := integration.Unknown()
	if st.bytes >= 0 {
		gib = math.Round(float64(st.bytes)/(1<<30)*10) / 10
	}
	return []integration.Metric{
		{Resource: in.rid, Key: "photos", Unit: "count", Max: 1e7, Band: [2]float64{0, 1e6}, Value: count(st.photos)},
		{Resource: in.rid, Key: "videos", Unit: "count", Max: 1e6, Band: [2]float64{0, 1e5}, Value: count(st.videos)},
		{Resource: in.rid, Key: "storage.gib", Unit: "GiB", Max: 1e6, Band: [2]float64{0, 1000}, Value: gib},
	}
}

// apiKey is GET /api/api-keys/me: the key in use and its permissions.
type apiKey struct {
	Name        string   `json:"name"`
	Permissions []string `json:"permissions"`
}

// readActions are the permission actions that only read. Anything else
// (create, update, delete, upload, share, …) can change something.
var readActions = map[string]bool{
	"read": true, "view": true, "download": true, "statistics": true, "about": true, "storage": true,
	"apkLinks": true, "versionCheck": true, "list": true, "get": true, "search": true, "logs": true, "stream": true,
}

// grants turns the key's permissions into grants. "all" is everything its
// user may do.
func grants(k apiKey) []integration.Grant {
	var out []integration.Grant
	for _, p := range k.Permissions {
		if p == "all" {
			out = append(out, integration.Grant{Name: "all", Access: integration.AccessAdmin, Detail: "every permission of the key's user"})
			continue
		}
		action := p[strings.LastIndexByte(p, '.')+1:]
		access := integration.AccessWrite
		if readActions[action] {
			access = integration.AccessRead
		}
		out = append(out, integration.Grant{Name: p, Access: access})
	}
	return out
}

// Test implements integration.Instance: the version, the key's permissions
// (recent releases report them at /api/api-keys/me; older ones answer 404
// and get no grants), and whether the server's statistics are within reach.
func (in *instance) Test(ctx context.Context) integration.TestResult {
	start := time.Now()
	fail := func(v Version, err error) integration.TestResult {
		r := integration.Failed(err)
		if v.Major > 0 {
			r.Version = v.String()
		}
		return r
	}
	v, err := in.version(ctx)
	if err != nil {
		return fail(v, err)
	}
	res := integration.TestResult{OK: true, Version: v.String()}
	var k apiKey
	switch err := in.get(ctx, "/api/api-keys/me", &k); {
	case err == nil:
		res.Grants = grants(k)
	case statusOf(err) == http.StatusNotFound: // older Immich: no read-back
	default:
		return fail(v, err)
	}
	var ss serverStats
	switch err := in.get(ctx, "/api/server/statistics", &ss); {
	case err == nil:
	case statusOf(err) == http.StatusForbidden:
		var admin *bool
		var u me
		if in.get(ctx, "/api/users/me", &u) == nil {
			admin = &u.IsAdmin
		}
		res.Warnings = append(res.Warnings, *statsWarning(admin, "Until then, Cha Dash counts only the key's own library."))
	default:
		return fail(v, err)
	}
	res.Latency = time.Since(start)
	return res
}
