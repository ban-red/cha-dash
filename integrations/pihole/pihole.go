// Package pihole reads Pi-hole v6's REST API: the last 24 hours' queries,
// how many were blocked, the blocklist's size and whether blocking is on,
// signed in with a session (the app password). See README.md.
package pihole

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"dash.cha.sh/internal/integration"
)

// ID is the integration's ID.
const ID = "pihole"

// versionEvery is how often the versions are read again while watched.
const versionEvery = time.Hour

func init() { integration.Register(Integration{}) }

// Integration is the Pi-hole integration.
type Integration struct{}

// Manifest implements integration.Integration.
func (Integration) Manifest() integration.Manifest {
	pass := integration.Secret("App password").Describe("Settings → Web interface / API → Configure app password (expert mode). The web password works too, unless two-factor authentication is on. Leave empty if the Pi-hole has no password.")
	return integration.Manifest{
		ID: ID, Name: "Pi-hole", Icon: "pi-hole", Version: "0.1.0", Tier: integration.Silver,
		Category:     integration.CatNetwork,
		Capabilities: []integration.Capability{"dns.filter"},
		ConfigSchema: integration.Object(
			integration.Prop("url", integration.URL("URL").Describe("The Pi-hole's address, e.g. https://pihole.lab.example. The admin page's address (…/admin) works too.")),
			integration.TLSProp(),
			integration.Prop("password", pass),
		).Require("url"),
		Poll: integration.Poll{Interval: time.Minute, Idle: 5 * time.Minute, Timeout: 30 * time.Second},
	}
}

// ValidateConfig implements integration.ConfigValidator.
func (Integration) ValidateConfig(cfg integration.Config) []integration.FieldError {
	if _, err := baseURL(cfg.String("url")); err != nil {
		return []integration.FieldError{{Path: "/url", Message: err.Error()}}
	}
	return nil
}

// baseURL is the Pi-hole's root, where /api lives. The admin page's address
// (…/admin or …/admin/) is taken as its parent.
func baseURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("must be an http:// or https:// URL")
	}
	u.RawQuery, u.Fragment, u.RawPath = "", "", ""
	u.Path = strings.TrimSuffix(strings.TrimSuffix(u.Path, "/"), "/admin")
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
	in := &instance{cfg: cfg, d: d, base: base, link: cfg.String("url"), http: d.HTTP, rid: ID + ":" + key + "/app"}
	if pass := cfg.String("password"); pass != "" {
		in.hasPassword = true
		c, err := d.NewHTTPClient(integration.HTTPOptions{Session: &session{base: base, pass: pass}})
		if err != nil {
			return nil, err
		}
		in.http = c
	}
	return in, nil
}

// session is Pi-hole v6's API session: POST /api/auth with the password
// answers a session id, sent back as X-FTL-SID; an expired one gets 401, so
// the framework logs in again and retries. Pi-hole limits concurrent
// sessions, so the instance keeps one and deletes it when it stops.
type session struct {
	base, pass string

	mu  sync.Mutex
	sid string // "" when the Pi-hole needs no password
}

// authAnswer is POST /api/auth's answer.
type authAnswer struct {
	Session struct {
		Valid bool    `json:"valid"`
		SID   *string `json:"sid"`
	} `json:"session"`
	Error *apiError `json:"error"`
}

// apiError is Pi-hole's error body, {key, message, hint}.
type apiError struct {
	Key string `json:"key"`
}

// Login implements integration.Session.
func (s *session) Login(ctx context.Context, c *http.Client) error {
	body, _ := json.Marshal(map[string]string{"password": s.pass})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.base+"/api/auth", bytes.NewReader(body))
	if err != nil {
		return &integration.ConfigError{Field: "url", Message: "the URL can't be used: " + err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var a authAnswer
	_ = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&a)
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return rejected()
	case http.StatusTooManyRequests:
		msg := "Pi-hole refused a new API session: too many sessions are open, or too many sign-ins failed. It frees sessions as they expire"
		if a.Error != nil && a.Error.Key == "api_seats_exceeded" {
			msg = "Pi-hole has no free API sessions (webserver.api.max_sessions); they free up as they expire"
		}
		return errors.New(msg)
	case http.StatusNotFound:
		return probe(ctx, c, s.base, "POST /api/auth")
	}
	if err := integration.CheckResponse(resp); err != nil {
		return err
	}
	if !a.Session.Valid {
		return rejected()
	}
	s.mu.Lock()
	s.sid = ""
	if a.Session.SID != nil {
		s.sid = *a.Session.SID
	}
	s.mu.Unlock()
	return nil
}

func rejected() error {
	return &integration.AuthError{Field: "password",
		Message: "Pi-hole rejected the password. With two-factor authentication on, use an app password (Settings → Web interface / API)"}
}

// Apply implements integration.Session.
func (s *session) Apply(r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sid != "" {
		r.Header.Set("X-FTL-SID", s.sid)
	}
}

// Logout implements integration.SessionLogout: DELETE /api/auth frees the
// session's seat.
func (s *session) Logout(ctx context.Context, c *http.Client) error {
	s.mu.Lock()
	sid := s.sid
	s.mu.Unlock()
	if sid == "" {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, s.base+"/api/auth", nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-FTL-SID", sid)
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// probe explains a 404 on Pi-hole v6's API: Pi-hole v5 (whose PHP API
// still answers at /admin/api.php) isn't supported, anything else is the
// wrong URL.
func probe(ctx context.Context, c *http.Client, base, what string) error {
	wrong := &integration.RepairError{Code: integration.CodeAPIChanged, Field: "url",
		Message: what + " answered 404 Not Found: check the URL, and that it's Pi-hole v6 or later"}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/admin/api.php?versions", nil)
	if err != nil {
		return wrong
	}
	resp, err := c.Do(req)
	if err != nil {
		return wrong
	}
	defer resp.Body.Close()
	var v5 struct {
		Core string `json:"core_current"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&v5) != nil || v5.Core == "" {
		return wrong
	}
	return &integration.RepairError{Code: integration.CodeUnsupportedVersion,
		Message: fmt.Sprintf("Pi-hole %s isn't supported: Cha Dash reads Pi-hole v6's API. Update the Pi-hole to v6", v5.Core)}
}

type instance struct {
	cfg         integration.Config
	d           integration.Deps
	base        string
	link        string
	http        *http.Client
	hasPassword bool
	rid         string

	// From the last version read; only Sync touches them.
	versions *versions
	readAt   time.Time
}

// Close implements integration.Instance; the framework deletes the session.
func (in *instance) Close() error { return nil }

// get reads path into dst.
func (in *instance) get(ctx context.Context, path string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, in.base+path, nil)
	if err != nil {
		return &integration.ConfigError{Field: "url", Message: "the URL can't be used: " + err.Error()}
	}
	req.Header.Set("Accept", "application/json")
	resp, err := in.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		if !in.hasPassword {
			return &integration.AuthError{Field: "password", Message: "Pi-hole wants a password: enter an app password"}
		}
		return rejected()
	case http.StatusNotFound:
		return probe(ctx, in.d.HTTP, in.base, "GET "+path)
	}
	if err := integration.CheckResponse(resp); err != nil {
		return err
	}
	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		return &integration.RepairError{Code: integration.CodeAPIChanged, Field: "url",
			Message: "GET " + path + " didn't answer with Pi-hole's JSON: check the URL"}
	}
	return nil
}

// component is one part's local and remote version in /api/info/version.
type component struct {
	Local struct {
		Version *string `json:"version"`
	} `json:"local"`
	Remote struct {
		Version *string `json:"version"`
	} `json:"remote"`
}

func (c component) local() string  { return deref(c.Local.Version) }
func (c component) remote() string { return deref(c.Remote.Version) }

// versions is GET /api/info/version.
type versions struct {
	Version struct {
		Core   component `json:"core"`
		Web    component `json:"web"`
		FTL    component `json:"ftl"`
		Docker struct {
			Local  *string `json:"local"`
			Remote *string `json:"remote"`
		} `json:"docker"`
	} `json:"version"`
}

// core is the Pi-hole core version without its v ("v6.1.2" → "6.1.2").
func (v *versions) core() string { return strings.TrimPrefix(v.Version.Core.local(), "v") }

// updateAvailable compares local and remote versions: the image's tag in
// Docker (where the parts update together), else each part's.
func (v *versions) updateAvailable() bool {
	d := v.Version.Docker
	if d.Local != nil && *d.Local != "" {
		return d.Remote != nil && *d.Remote != "" && *d.Remote != *d.Local
	}
	for _, c := range []component{v.Version.Core, v.Version.Web, v.Version.FTL} {
		if c.remote() != "" && c.local() != "" && c.remote() != c.local() {
			return true
		}
	}
	return false
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// summary is GET /api/stats/summary: Pi-hole's in-memory window, the last
// 24 hours.
type summary struct {
	Queries struct {
		Total          *float64 `json:"total"`
		Blocked        *float64 `json:"blocked"`
		PercentBlocked *float64 `json:"percent_blocked"`
	} `json:"queries"`
	Clients struct {
		Active *float64 `json:"active"`
		Total  *float64 `json:"total"`
	} `json:"clients"`
	Gravity struct {
		DomainsBeingBlocked *float64 `json:"domains_being_blocked"`
		LastUpdate          *int64   `json:"last_update"`
	} `json:"gravity"`
}

// blocking is GET /api/dns/blocking.
type blocking struct {
	Blocking string   `json:"blocking"` // enabled, disabled, failed, unknown
	Timer    *float64 `json:"timer"`    // seconds until it flips back, or null
}

func (in *instance) readVersions(ctx context.Context) (*versions, error) {
	var v versions
	if err := in.get(ctx, "/api/info/version", &v); err != nil {
		return nil, err
	}
	if v.core() == "" {
		return nil, &integration.RepairError{Code: integration.CodeAPIChanged, Field: "url",
			Message: "/api/info/version has no core version: check that the URL is a Pi-hole"}
	}
	return &v, nil
}

// grant is what the password can do: Pi-hole has one administrator, and an
// app password is the same account.
func (in *instance) grant() integration.Grant {
	g := integration.Grant{Name: "Pi-hole admin", Access: integration.AccessAdmin,
		Detail: "the password (or app password) signs in as Pi-hole's only administrator: it can change settings, lists and blocking; Pi-hole has no read-only accounts"}
	if !in.hasPassword {
		g.Detail = "the Pi-hole has no password, so anyone who reaches it has full control"
	}
	return g
}

// Test implements integration.Instance.
func (in *instance) Test(ctx context.Context) integration.TestResult {
	v, err := in.readVersions(ctx)
	if err != nil {
		return integration.Failed(err)
	}
	return integration.TestResult{OK: true, Version: v.core(), Grants: []integration.Grant{in.grant()}}
}

// Sync implements integration.Instance.
func (in *instance) Sync(ctx context.Context, want integration.Interest) error {
	now := in.d.Now()
	if in.versions == nil || (want.Watched && now.Sub(in.readAt) >= versionEvery) {
		v, err := in.readVersions(ctx)
		if err != nil {
			return err
		}
		in.versions, in.readAt = v, now
		in.d.Emit.SetVersion(v.core())
	}
	var s summary
	if err := in.get(ctx, "/api/stats/summary", &s); err != nil {
		return err
	}
	var b blocking
	if err := in.get(ctx, "/api/dns/blocking", &b); err != nil {
		return err
	}

	v := in.versions.Version
	attrs := map[string]any{
		"type": ID, "url": in.base, "version": in.versions.core(),
		"ftl_version": strings.TrimPrefix(v.FTL.local(), "v"), "web_version": strings.TrimPrefix(v.Web.local(), "v"),
		"update_available": in.versions.updateAvailable(), "blocking": cmp(b.Blocking, "unknown"),
	}
	if b.Timer != nil {
		attrs["blocking_timer_s"] = math.Round(*b.Timer)
	}
	if s.Gravity.LastUpdate != nil && *s.Gravity.LastUpdate > 0 {
		attrs["gravity_updated_at"] = time.Unix(*s.Gravity.LastUpdate, 0).UTC().Format(time.RFC3339)
	}
	if s.Clients.Total != nil {
		attrs["clients_total"] = *s.Clients.Total
	}
	h := integration.Health{Status: integration.HealthOK}
	switch b.Blocking {
	case "disabled":
		h = integration.Health{Status: integration.HealthAttention, Reason: "Blocking is off"}
		if b.Timer != nil && *b.Timer > 0 {
			h.Reason = fmt.Sprintf("Blocking is paused for another %s", minutes(*b.Timer))
		}
	case "failed":
		h = integration.Health{Status: integration.HealthAttention, Reason: "Pi-hole couldn't turn blocking on"}
	default:
		if n := s.Gravity.DomainsBeingBlocked; n != nil && *n <= 0 {
			h = integration.Health{Status: integration.HealthAttention, Reason: "The blocklist is empty: update gravity"}
		}
	}
	res := integration.Resource{ID: in.rid, Kind: "app", Name: in.cfg.Name, State: "running", Health: h, Attrs: attrs,
		Caps: []string{"dns.filter"}, Links: []integration.Link{{Title: "Pi-hole", URL: in.link}}}
	count := func(k string, max float64, band [2]float64, v *float64) integration.Metric {
		return integration.Metric{Resource: in.rid, Key: k, Unit: "count", Max: max, Band: band, Value: num(v)}
	}
	blocklist := num(s.Gravity.DomainsBeingBlocked)
	if blocklist < 0 {
		blocklist = integration.Unknown() // -1 when gravity can't be read
	}
	return in.d.Emit.Replace("", integration.Facts{
		Resources: []integration.Resource{res},
		Relations: []integration.Relation{{From: in.d.Emit.Self(), Type: "monitors", To: in.rid}},
		Metrics: []integration.Metric{
			count("queries.24h", 1e7, [2]float64{0, 50000}, s.Queries.Total),
			count("blocked.24h", 1e7, [2]float64{0, 15000}, s.Queries.Blocked),
			{Resource: in.rid, Key: "blocked.pct", Unit: "%", Max: 100, Band: [2]float64{5, 35}, Value: round1(num(s.Queries.PercentBlocked))},
			{Resource: in.rid, Key: "blocklist", Unit: "count", Max: 1e7, Band: [2]float64{50000, 5e6}, Value: blocklist},
			count("clients", 1000, [2]float64{0, 50}, s.Clients.Active),
		},
	})
}

// num is a JSON number, or unknown when it's missing.
func num(v *float64) float64 {
	if v == nil {
		return integration.Unknown()
	}
	return *v
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

// minutes renders a timer in whole minutes, rounded up ("1 min", "5 min").
func minutes(s float64) string {
	return fmt.Sprintf("%d min", int(math.Ceil(s/60)))
}

func cmp(a, b string) string {
	if a == "" {
		return b
	}
	return a
}
