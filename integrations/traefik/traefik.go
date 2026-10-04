// Package traefik reads a Traefik v2/v3 API: its HTTP routers become route
// resources with an open URL and a backend URL resolved from Traefik's own
// services (never from container labels), its services become service
// resources, and the instance publishes the Traefik app. Middleware configs
// are never kept: only their names and kinds. See README.md.
package traefik

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
const ID = "traefik"

const (
	// perPage and maxPages bound the paginated list endpoints.
	perPage  = 100
	maxPages = 50
	// unauthEvery spaces the unauthenticated probe of the API.
	unauthEvery = 10 * time.Minute
)

func init() { integration.Register(Integration{}) }

// Integration is the Traefik integration.
type Integration struct{}

// Manifest implements integration.Integration.
func (Integration) Manifest() integration.Manifest {
	return integration.Manifest{
		ID: ID, Name: "Traefik", Icon: "traefik", Version: "0.1.0", Tier: integration.Silver,
		Capabilities: []integration.Capability{"proxy.routes"},
		ConfigSchema: integration.Object(
			integration.Prop("url", integration.URL("URL").Describe("Where Traefik's API answers, e.g. http://traefik:8080 or https://traefik.lab.example (the /api paths are added).")),
			integration.TLSProp(),
			integration.Prop("username", integration.String("Username").Describe("For an API behind basic auth.")),
			integration.Prop("password", integration.Secret("Password")),
		).Require("url"),
		Poll: integration.Poll{Interval: 60 * time.Second, Idle: 5 * time.Minute, Timeout: 30 * time.Second},
	}
}

// ValidateConfig implements integration.ConfigValidator.
func (Integration) ValidateConfig(cfg integration.Config) []integration.FieldError {
	var errs []integration.FieldError
	if u, err := url.Parse(cfg.String("url")); err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		errs = append(errs, integration.FieldError{Path: "/url", Message: "must start with http:// or https://"})
	}
	user, pass := cfg.String("username"), cfg.String("password")
	switch {
	case user != "" && pass == "":
		errs = append(errs, integration.FieldError{Path: "/password", Message: "is required with a username"})
	case user == "" && pass != "":
		errs = append(errs, integration.FieldError{Path: "/username", Message: "is required with a password"})
	}
	return errs
}

// Connect implements integration.Integration. It does no I/O.
func (Integration) Connect(_ context.Context, cfg integration.Config, d integration.Deps) (integration.Instance, error) {
	u, err := url.Parse(cfg.String("url"))
	if err != nil {
		return nil, &integration.ConfigError{Field: "url", Message: "url: " + err.Error()}
	}
	base := strings.TrimSuffix(strings.TrimSuffix(u.String(), "/"), "/dashboard")
	in := &instance{d: d, base: strings.TrimSuffix(base, "/"), u: u, key: cfg.InstanceID, name: cfg.Name,
		user: cfg.String("username"), pass: cfg.String("password"), raised: map[string]bool{}}
	if in.key == "" {
		in.key = "test"
	}
	if in.name == "" {
		in.name = "Traefik"
	}
	return in, nil
}

type instance struct {
	d          integration.Deps
	base       string // the API's base URL, without /api
	u          *url.URL
	key        string // the instance ID in resource IDs
	name       string
	user, pass string

	// Sync's state (the runtime calls Sync from one goroutine).
	unauthAt time.Time // last unauthenticated probe
	open     bool      // the API answered it
	raised   map[string]bool
}

func (in *instance) rid(path string) string { return ID + ":" + in.key + "/" + path }

// Close implements integration.Instance.
func (in *instance) Close() error {
	in.d.HTTP.CloseIdleConnections()
	return nil
}

// get GETs an API path, with the credentials when there are any, and
// decodes a 2xx answer into dst.
func (in *instance) get(ctx context.Context, path string, dst any) (http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, in.base+path, nil)
	if err != nil {
		return nil, &integration.ConfigError{Field: "url", Message: "url: " + err.Error()}
	}
	if in.user != "" {
		req.SetBasicAuth(in.user, in.pass)
	}
	resp, err := in.d.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		if in.user == "" {
			return nil, &integration.AuthError{Field: "username", Message: "Traefik's API asks for credentials: enter the username and password"}
		}
		return nil, &integration.AuthError{Field: "password", Message: "Traefik rejected the credentials"}
	case http.StatusNotFound:
		return nil, &integration.RepairError{Code: integration.CodeAPIChanged, Field: "url",
			Message: fmt.Sprintf("GET %s answered 404: Traefik's API isn't served at this URL (enable api, and route it or use the traefik entrypoint)", stripQuery(path))}
	}
	if err := integration.CheckResponse(resp); err != nil {
		return nil, err
	}
	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		return nil, fmt.Errorf("GET %s: %w", stripQuery(path), err)
	}
	return resp.Header, nil
}

func stripQuery(p string) string {
	if i := strings.IndexByte(p, '?'); i >= 0 {
		return p[:i]
	}
	return p
}

// list reads every page of a paginated endpoint. Traefik says the next page
// in X-Next-Page, and 1 on the last one.
func list[T any](ctx context.Context, in *instance, path string) ([]T, error) {
	var out []T
	for page := 1; page <= maxPages; {
		var items []T
		h, err := in.get(ctx, fmt.Sprintf("%s?per_page=%d&page=%d", path, perPage, page), &items)
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
		next, _ := strconv.Atoi(h.Get("X-Next-Page"))
		if next <= page {
			break
		}
		page = next
	}
	return out, nil
}

// unauthenticated reports whether the API answers without credentials. A
// transport error says nothing either way.
func (in *instance) unauthenticated(ctx context.Context) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, in.base+"/api/version", nil)
	if err != nil {
		return false, err
	}
	resp, err := in.d.HTTP.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK, nil
}

// Test implements integration.Instance: the version with the credentials,
// and whether the API also answers without them.
func (in *instance) Test(ctx context.Context) integration.TestResult {
	start := time.Now()
	var v version
	if _, err := in.get(ctx, "/api/version", &v); err != nil {
		return integration.Failed(err)
	}
	res := integration.TestResult{OK: true, Version: v.Version, Latency: time.Since(start),
		Grants: []integration.Grant{{Name: "api", Access: integration.AccessRead, Detail: "Traefik's API is read-only: routers, services, middlewares and entrypoints"}}}
	open := in.user == ""
	if !open {
		open, _ = in.unauthenticated(ctx)
	}
	if open {
		res.Warnings = append(res.Warnings, integration.Warning{Code: "api_no_auth", Severity: integration.WarnWarning,
			Message: "Traefik's API answers without credentials: anyone who can reach it can read every router, service and middleware, including basic-auth password hashes."})
	}
	return res
}

// Sync implements integration.Instance: read everything (the API is cheap:
// about 80 KB on the dogfood home), publish, then the hints.
func (in *instance) Sync(ctx context.Context, _ integration.Interest) error {
	var (
		v   version
		ov  overview
		eps []entrypoint
	)
	if _, err := in.get(ctx, "/api/version", &v); err != nil {
		return err
	}
	in.d.Emit.SetVersion(v.Version)
	if _, err := in.get(ctx, "/api/overview", &ov); err != nil {
		return err
	}
	if _, err := in.get(ctx, "/api/entrypoints", &eps); err != nil {
		return err
	}
	routers, err := list[router](ctx, in, "/api/http/routers")
	if err != nil {
		return err
	}
	services, err := list[service](ctx, in, "/api/http/services")
	if err != nil {
		return err
	}
	mws, err := list[middleware](ctx, in, "/api/http/middlewares")
	if err != nil {
		return err
	}
	now := in.d.Now()
	switch {
	case in.user == "":
		in.open = true // the reads above needed no credentials
	case in.unauthAt.IsZero() || now.Sub(in.unauthAt) >= unauthEvery:
		open, err := in.unauthenticated(ctx)
		if err == nil {
			in.open, in.unauthAt = open, now
		} else if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	m := newModel(routers, services, mws, eps)
	if err := in.d.Emit.Replace("", in.facts(v, ov, m)); err != nil {
		return err
	}
	return in.hints(m)
}
