// Package adguard reads AdGuard Home's control API: the last 24 hours'
// queries, how many were blocked, the blocklist's size, and whether
// protection is on, with basic auth. See README.md.
package adguard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"dash.cha.sh/internal/integration"
)

// ID is the integration's ID.
const ID = "adguard"

// filtersEvery is how often the filter lists (the blocklist size) are read
// again while watched. AdGuard Home updates them every 24 hours by default.
const filtersEvery = 30 * time.Minute

// day is the stats window asked for, in milliseconds (the recent parameter,
// AdGuard Home v0.107.72 and later).
const day = "86400000"

func init() { integration.Register(Integration{}) }

// Integration is the AdGuard Home integration.
type Integration struct{}

// Manifest implements integration.Integration.
func (Integration) Manifest() integration.Manifest {
	user := integration.String("Username").Describe("An AdGuard Home user (AdGuardHome.yaml → users). Leave both empty if it has none.")
	pass := integration.Secret("Password").Describe("That user's password.")
	return integration.Manifest{
		ID: ID, Name: "AdGuard Home", Icon: "adguard-home", Version: "0.1.0", Tier: integration.Silver,
		Category:     integration.CatNetwork,
		Capabilities: []integration.Capability{"dns.filter"},
		ConfigSchema: integration.Object(
			integration.Prop("url", integration.URL("URL").Describe("The web interface's address, e.g. https://adguard.lab.example.")),
			integration.TLSProp(),
			integration.Prop("username", user),
			integration.Prop("password", pass),
		).Require("url"),
		Poll: integration.Poll{Interval: time.Minute, Idle: 5 * time.Minute, Timeout: 30 * time.Second},
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
	key := cfg.InstanceID
	if key == "" {
		key = "test"
	}
	return &instance{cfg: cfg, d: d, base: strings.TrimRight(cfg.String("url"), "/"), user: cfg.String("username"),
		pass: cfg.String("password"), rid: ID + ":" + key + "/app"}, nil
}

type instance struct {
	cfg        integration.Config
	d          integration.Deps
	base       string
	user, pass string
	rid        string

	// From the last filter-list read; only Sync touches them.
	filters *filtering
	readAt  time.Time
}

// Close implements integration.Instance.
func (in *instance) Close() error { return nil }

// status is GET /control/status (the fields read).
type status struct {
	Version                 string  `json:"version"`
	Running                 bool    `json:"running"`
	ProtectionEnabled       bool    `json:"protection_enabled"`
	ProtectionDisabledUntil *string `json:"protection_disabled_until"`
}

func (s status) version() string { return strings.TrimPrefix(s.Version, "v") }

// stats is GET /control/stats (the fields read). With a statistics interval
// of up to 7 days the arrays hold one entry per hour, oldest first; beyond
// that, one per day.
type stats struct {
	TimeUnits            string    `json:"time_units"`
	DNSQueries           []float64 `json:"dns_queries"`
	BlockedFiltering     []float64 `json:"blocked_filtering"`
	ReplacedSafebrowsing []float64 `json:"replaced_safebrowsing"`
	ReplacedParental     []float64 `json:"replaced_parental"`
}

// filtering is GET /control/filtering/status (the fields read; user rules
// and list URLs aren't).
type filtering struct {
	Enabled bool `json:"enabled"`
	Filters []struct {
		Enabled    bool    `json:"enabled"`
		RulesCount float64 `json:"rules_count"`
	} `json:"filters"`
}

// rules is the blocklist's size: the rules of the enabled filter lists.
func (f *filtering) rules() (rules float64, lists int) {
	for _, l := range f.Filters {
		if l.Enabled {
			rules += l.RulesCount
			lists++
		}
	}
	return rules, lists
}

// get reads path into dst with basic auth.
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
	if in.user != "" {
		req.SetBasicAuth(in.user, in.pass)
	}
	resp, err := in.d.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		if in.user == "" {
			return &integration.AuthError{Field: "username", Message: "AdGuard Home wants a sign-in: enter its username and password"}
		}
		return &integration.AuthError{Field: "password", Message: "AdGuard Home rejected the username or password"}
	case http.StatusNotFound:
		return &integration.RepairError{Code: integration.CodeAPIChanged, Field: "url",
			Message: "GET " + path + " answered 404 Not Found: check that the URL is AdGuard Home's web interface"}
	}
	if err := integration.CheckResponse(resp); err != nil {
		return err
	}
	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		return &integration.RepairError{Code: integration.CodeAPIChanged, Field: "url",
			Message: "GET " + path + " didn't answer with AdGuard Home's JSON: check the URL"}
	}
	return nil
}

func (in *instance) readStatus(ctx context.Context) (status, error) {
	var s status
	if err := in.get(ctx, "/control/status", nil, &s); err != nil {
		return s, err
	}
	if s.Version == "" {
		return s, &integration.RepairError{Code: integration.CodeAPIChanged, Field: "url",
			Message: "/control/status has no version: check that the URL is AdGuard Home"}
	}
	return s, nil
}

// readStats asks for the last 24 hours. A statistics interval shorter than
// that makes AdGuard Home refuse recent with a 400; then it asks for the
// whole interval. Releases before v0.107.72 ignore recent.
func (in *instance) readStats(ctx context.Context) (stats, error) {
	var st stats
	err := in.get(ctx, "/control/stats", url.Values{"recent": {day}}, &st)
	var se *integration.StatusError
	if errors.As(err, &se) && se.Code == http.StatusBadRequest {
		st = stats{}
		err = in.get(ctx, "/control/stats", nil, &st)
	}
	return st, err
}

// window sums the last 24 hourly entries: queries, and what was blocked by
// filters, Safe Browsing or parental control. hours is how many entries
// there were (fewer with a shorter statistics interval); 0 when the arrays
// hold days or nothing, and the sums are unknown.
func (st stats) window() (queries, blocked float64, hours int) {
	if st.TimeUnits != "hours" || len(st.DNSQueries) == 0 {
		return integration.Unknown(), integration.Unknown(), 0
	}
	hours = min(24, len(st.DNSQueries))
	last := func(xs []float64) float64 {
		var sum float64
		for _, x := range xs[max(0, len(xs)-hours):] {
			sum += x
		}
		return sum
	}
	return last(st.DNSQueries), last(st.BlockedFiltering) + last(st.ReplacedSafebrowsing) + last(st.ReplacedParental), hours
}

// grant is what an AdGuard Home user can do: every user is an
// administrator.
func (in *instance) grant() integration.Grant {
	g := integration.Grant{Name: "AdGuard Home user", Access: integration.AccessAdmin,
		Detail: "AdGuard Home's users are all administrators: they can change filters, clients and settings; there are no read-only users"}
	if in.user == "" {
		g.Detail = "AdGuard Home has no users, so anyone who reaches it has full control"
	}
	return g
}

// Test implements integration.Instance.
func (in *instance) Test(ctx context.Context) integration.TestResult {
	s, err := in.readStatus(ctx)
	if err != nil {
		return integration.Failed(err)
	}
	res := integration.TestResult{OK: true, Version: s.version(), Grants: []integration.Grant{in.grant()}}
	st, err := in.readStats(ctx)
	if err != nil {
		r := integration.Failed(err)
		r.Version = s.version()
		return r
	}
	if _, _, hours := st.window(); hours == 0 && st.TimeUnits == "days" {
		res.Warnings = append(res.Warnings, integration.Warning{Code: "stats_days", Severity: integration.WarnInfo,
			Message: "With a statistics interval over 7 days, AdGuard Home before v0.107.72 can't give 24-hour totals, so the query counts stay unknown. Update AdGuard Home, or shorten the interval."})
	}
	return res
}

// Sync implements integration.Instance.
func (in *instance) Sync(ctx context.Context, want integration.Interest) error {
	now := in.d.Now()
	s, err := in.readStatus(ctx)
	if err != nil {
		return err
	}
	in.d.Emit.SetVersion(s.version())
	st, err := in.readStats(ctx)
	if err != nil {
		return err
	}
	if in.filters == nil || (want.Watched && now.Sub(in.readAt) >= filtersEvery) {
		var f filtering
		if err := in.get(ctx, "/control/filtering/status", nil, &f); err != nil {
			return err
		}
		in.filters, in.readAt = &f, now
	}
	queries, blocked, hours := st.window()
	pct := integration.Unknown()
	switch {
	case hours > 0 && queries > 0:
		pct = math.Round(blocked/queries*1000) / 10
	case hours > 0:
		pct = 0
	}
	rules, lists := in.filters.rules()

	attrs := map[string]any{
		"type": ID, "url": in.base, "version": s.version(), "running": s.Running,
		"protection_enabled": s.ProtectionEnabled, "filtering_enabled": in.filters.Enabled, "filters": lists,
	}
	if hours > 0 {
		attrs["stats_hours"] = hours
	}
	var until time.Time
	if s.ProtectionDisabledUntil != nil {
		if t, err := time.Parse(time.RFC3339, *s.ProtectionDisabledUntil); err == nil && t.After(now) {
			until = t
			attrs["protection_paused_until"] = t.UTC().Format(time.RFC3339)
		}
	}
	h := integration.Health{Status: integration.HealthOK}
	switch {
	case !s.Running:
		h = integration.Health{Status: integration.HealthDown, Reason: "AdGuard Home's DNS server isn't running"}
	case !s.ProtectionEnabled && !until.IsZero():
		h = integration.Health{Status: integration.HealthAttention,
			Reason: fmt.Sprintf("Protection is paused for another %d min", int(math.Ceil(until.Sub(now).Minutes())))}
	case !s.ProtectionEnabled:
		h = integration.Health{Status: integration.HealthAttention, Reason: "Protection is off"}
	case !in.filters.Enabled:
		h = integration.Health{Status: integration.HealthAttention, Reason: "Filtering is off, so nothing is blocked"}
	}
	res := integration.Resource{ID: in.rid, Kind: "app", Name: in.cfg.Name, State: "running", Health: h, Attrs: attrs,
		Caps: []string{"dns.filter"}, Links: []integration.Link{{Title: "AdGuard Home", URL: in.base}}}
	return in.d.Emit.Replace("", integration.Facts{
		Resources: []integration.Resource{res},
		Relations: []integration.Relation{{From: in.d.Emit.Self(), Type: "monitors", To: in.rid}},
		Metrics: []integration.Metric{
			{Resource: in.rid, Key: "queries.24h", Unit: "count", Max: 1e7, Band: [2]float64{0, 50000}, Value: queries},
			{Resource: in.rid, Key: "blocked.24h", Unit: "count", Max: 1e7, Band: [2]float64{0, 15000}, Value: blocked},
			{Resource: in.rid, Key: "blocked.pct", Unit: "%", Max: 100, Band: [2]float64{5, 35}, Value: pct},
			{Resource: in.rid, Key: "blocklist", Unit: "count", Max: 1e7, Band: [2]float64{50000, 5e6}, Value: rules},
		},
	})
}
