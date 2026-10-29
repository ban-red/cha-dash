// Package arr is what Sonarr, Radarr, Lidarr and Prowlarr share: their REST API
// (v3, or v1 for Lidarr and Prowlarr) with the key in X-Api-Key, the config schema,
// the status, health and paging reads, the app resource, and the grant an
// *arr API key amounts to (full admin: there are no read-only keys).
package arr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"dash.cha.sh/internal/integration"
)

// App says which *arr a client talks to.
type App struct {
	ID       string // integration ID: "sonarr"
	Name     string // "Sonarr"
	APIPath  string // "/api/v3", or "/api/v1" for Prowlarr
	MinMajor int    // the oldest major version with this API (Sonarr 3, Radarr 3, Prowlarr 1)
}

// Schema is the config every *arr takes: url, tls, api_key.
func Schema(app App) *integration.Schema {
	key := integration.Secret("API key").Describe(app.Name + " → Settings → General → Security → API Key.")
	u := integration.URL("URL").Describe("Where " + app.Name + " is, with its URL base if it has one (https://host/" + app.ID + ").")
	return integration.Object(integration.Prop("url", u), integration.TLSProp(), integration.Prop("api_key", key)).Require("url", "api_key")
}

// Client reads one *arr.
type Client struct {
	App  App
	base string
	key  string
	http *http.Client
}

// New returns a client for the config's url and api_key over d.HTTP.
func New(app App, cfg integration.Config, d integration.Deps) *Client {
	return &Client{App: app, base: strings.TrimRight(cfg.String("url"), "/"), key: cfg.String("api_key"), http: d.HTTP}
}

// URL is the configured base URL (with any URL base), for links and attrs.
func (c *Client) URL() string { return c.base }

// errNotFound is a 404 from an endpoint the caller marked optional.
var errNotFound = errors.New("arr: not found")

// Get reads path (under the API path) into dst. A 401 or 403 is an auth
// Repair on api_key, a 404 an api_changed Repair on url (a wrong URL base, or
// a version without this API), and a body that isn't JSON says to check the
// URL.
func (c *Client) Get(ctx context.Context, path string, q url.Values, dst any) error {
	err := c.get(ctx, path, q, dst)
	if errors.Is(err, errNotFound) {
		return &integration.RepairError{Code: integration.CodeAPIChanged, Field: "url",
			Message: fmt.Sprintf("GET %s%s answered 404 Not Found: check the URL (with its URL base, if %s has one) and that it's %s %d or later",
				c.App.APIPath, path, c.App.Name, c.App.Name, c.App.MinMajor)}
	}
	return err
}

// GetOptional is Get for an endpoint older versions lack: a 404 returns
// found false and no error.
func (c *Client) GetOptional(ctx context.Context, path string, q url.Values, dst any) (found bool, err error) {
	err = c.get(ctx, path, q, dst)
	if errors.Is(err, errNotFound) {
		return false, nil
	}
	return err == nil, err
}

func (c *Client) get(ctx context.Context, path string, q url.Values, dst any) error {
	u := c.base + c.App.APIPath + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return &integration.ConfigError{Field: "url", Message: "the URL can't be used: " + err.Error()}
	}
	req.Header.Set("X-Api-Key", c.key)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return errNotFound
	}
	if err := integration.CheckResponse(resp); err != nil {
		var se *integration.StatusError
		if errors.As(err, &se) && (se.Code == http.StatusUnauthorized || se.Code == http.StatusForbidden) {
			return &integration.AuthError{Field: "api_key", Message: c.App.Name + " rejected the API key (" + se.Error() + ")"}
		}
		return err
	}
	mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil || (mt != "" && !strings.HasSuffix(mt, "json")) {
		return &integration.RepairError{Code: integration.CodeAPIChanged, Field: "url",
			Message: fmt.Sprintf("GET %s%s didn't answer with %s's JSON: check the URL, with its URL base if it has one (https://host/%s)",
				c.App.APIPath, path, c.App.Name, c.App.ID)}
	}
	return nil
}

// Status is /system/status.
type Status struct {
	AppName      string `json:"appName"`
	InstanceName string `json:"instanceName"`
	Version      string `json:"version"`
	Branch       string `json:"branch"`
	URLBase      string `json:"urlBase"`
}

// Status reads /system/status and checks it's the right app and version.
func (c *Client) Status(ctx context.Context) (Status, error) {
	var s Status
	if err := c.Get(ctx, "/system/status", nil, &s); err != nil {
		return s, err
	}
	if s.AppName != "" && !strings.EqualFold(s.AppName, c.App.Name) {
		return s, &integration.ConfigError{Field: "url", Message: fmt.Sprintf("that URL is %s, not %s", s.AppName, c.App.Name)}
	}
	var major int
	if _, err := fmt.Sscanf(s.Version, "%d.", &major); err == nil && major < c.App.MinMajor {
		return s, &integration.RepairError{Code: integration.CodeUnsupportedVersion,
			Message: fmt.Sprintf("%s %s is older than this integration supports (%d or later)", c.App.Name, s.Version, c.App.MinMajor)}
	}
	return s, nil
}

// HealthItem is one entry of /health.
type HealthItem struct {
	Source  string `json:"source"`
	Type    string `json:"type"` // ok, notice, warning, error
	Message string `json:"message"`
	WikiURL string `json:"wikiUrl"`
}

// Health reads /health.
func (c *Client) Health(ctx context.Context) ([]HealthItem, error) {
	var h []HealthItem
	err := c.Get(ctx, "/health", nil, &h)
	return h, err
}

// Problems counts the health checks that failed with type error and warning,
// and returns the first error's message.
func Problems(items []HealthItem) (errs, warnings int, first string) {
	for _, h := range items {
		switch strings.ToLower(h.Type) {
		case "error":
			if errs == 0 {
				first = strings.TrimSpace(h.Message)
			}
			errs++
		case "warning":
			warnings++
		}
	}
	return errs, warnings, first
}

// HealthOf turns the app's health checks into the app resource's health:
// attention when a check fails with an error (warnings, such as an update
// being available, don't count), with its message as the reason.
func HealthOf(app App, items []HealthItem) integration.Health {
	errs, _, first := Problems(items)
	if errs == 0 {
		return integration.Health{Status: integration.HealthOK}
	}
	reason := app.Name + ": " + strings.TrimSuffix(first, ".")
	if errs > 1 {
		reason += fmt.Sprintf(" (and %d more)", errs-1)
	}
	return integration.Health{Status: integration.HealthAttention, Reason: reason}
}

// Paging is the envelope of the paged endpoints (wanted/missing, …).
type Paging struct {
	Page         int `json:"page"`
	PageSize     int `json:"pageSize"`
	TotalRecords int `json:"totalRecords"`
}

// Total reads a paged endpoint's totalRecords with a one-record page. An
// endpoint the version lacks gives found false.
func (c *Client) Total(ctx context.Context, path string, q url.Values) (n int, found bool, err error) {
	v := url.Values{"page": {"1"}, "pageSize": {"1"}}
	maps.Copy(v, q)
	var p Paging
	found, err = c.GetOptional(ctx, path, v, &p)
	return p.TotalRecords, found, err
}

// QueueStatus is /queue/status (Sonarr, Radarr).
type QueueStatus struct {
	TotalCount      int  `json:"totalCount"`
	Count           int  `json:"count"`
	UnknownCount    int  `json:"unknownCount"`
	Errors          bool `json:"errors"`
	Warnings        bool `json:"warnings"`
	UnknownErrors   bool `json:"unknownErrors"`
	UnknownWarnings bool `json:"unknownWarnings"`
}

// Queue reads /queue/status.
func (c *Client) Queue(ctx context.Context) (QueueStatus, error) {
	var q QueueStatus
	err := c.Get(ctx, "/queue/status", nil, &q)
	return q, err
}

// Grant is what an *arr API key can do: everything. The *arrs have no
// read-only keys, so the runtime's write-grant warning always shows.
func Grant(app App) integration.Grant {
	return integration.Grant{Name: app.Name + " API key", Access: integration.AccessAdmin,
		Detail: app.Name + " API keys have full access; " + app.Name + " has no read-only keys"}
}

// Test reads /system/status: the version, and the key's grant.
func (c *Client) Test(ctx context.Context) integration.TestResult {
	s, err := c.Status(ctx)
	if err != nil {
		return integration.Failed(err)
	}
	return integration.TestResult{OK: true, Version: s.Version, Grants: []integration.Grant{Grant(c.App)}}
}

// InstanceKey is the instance ID resource IDs use ("test" for an unsaved
// config).
func InstanceKey(cfg integration.Config) string {
	if cfg.InstanceID == "" {
		return "test"
	}
	return cfg.InstanceID
}

// AppResource is the instance's service: <id>:<instance>/app, kind app,
// attrs.type = the integration ID, named after the instance.
func (c *Client) AppResource(cfg integration.Config, version string, h integration.Health, caps ...string) integration.Resource {
	attrs := map[string]any{"type": c.App.ID, "url": c.base}
	if version != "" {
		attrs["version"] = version
	}
	return integration.Resource{
		ID: c.App.ID + ":" + InstanceKey(cfg) + "/app", Kind: "app", Name: cfg.Name,
		State: "running", Health: h, Attrs: attrs, Caps: caps,
		Links: []integration.Link{{Title: c.App.Name, URL: c.base}},
	}
}

// Count is a count metric on the app.
func Count(resource, key string, max float64, band [2]float64, value float64) integration.Metric {
	return integration.Metric{Resource: resource, Key: key, Unit: "count", Min: 0, Max: max, Band: band, Value: value}
}

// Every runs a slow part of a sync: when it has never run, or when it's due
// and someone is watching.
type Every struct {
	D    time.Duration
	last time.Time
}

// Due reports whether the slow part should run now.
func (e *Every) Due(now time.Time, watched bool) bool {
	return e.last.IsZero() || (watched && now.Sub(e.last) >= e.D)
}

// Done records a successful run.
func (e *Every) Done(now time.Time) { e.last = now }

// Value is a known number, or integration.Unknown() when !ok.
func Value(n int, ok bool) float64 {
	if !ok {
		return integration.Unknown()
	}
	return float64(n)
}
