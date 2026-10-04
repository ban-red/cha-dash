// Package uptimekuma is the Uptime Kuma integration: one status page's
// monitors, read from the public status page API, optionally enriched from
// Prometheus /metrics with an API key. Each monitor becomes a check
// resource, and the service counts monitors up and down. See README.md.
package uptimekuma

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"dash.cha.sh/internal/integration"
)

// ID is the integration's ID.
const ID = "uptimekuma"

func init() { integration.Register(Integration{}) }

// Integration is the Uptime Kuma integration.
type Integration struct{}

// Manifest implements integration.Integration.
func (Integration) Manifest() integration.Manifest {
	return integration.Manifest{
		ID: ID, Name: "Uptime Kuma", Icon: "uptimekuma", Version: "0.1.0", Tier: integration.Silver,
		Capabilities: []integration.Capability{"checks.uptime"},
		ConfigSchema: integration.Object(
			integration.URLProp(),
			integration.TLSProp(),
			integration.Prop("slug", &integration.Schema{Type: "string", Title: "Status page", Pattern: `^[A-Za-z0-9][A-Za-z0-9_-]*$`, MaxLength: intp(255),
				Description: "The status page's slug: the last part of its address, /status/<slug>. Its monitors are the ones Cha Dash shows."}),
			integration.Prop("key", integration.Secret("API key").Describe("Optional: an API key (Settings → API Keys) adds response times and certificate expiry from /metrics. It opens nothing else.")),
		).Require("url", "slug"),
		// The heartbeat endpoint is cached for a minute on Uptime Kuma's side.
		Poll: integration.Poll{Interval: 60 * time.Second, Idle: 5 * time.Minute, Timeout: 30 * time.Second},
	}
}

func intp(n int) *int { return &n }

type config struct {
	URL  string `json:"url"`
	Slug string `json:"slug"`
	Key  string `json:"key"`
}

// ValidateConfig implements integration.ConfigValidator.
func (Integration) ValidateConfig(cfg integration.Config) []integration.FieldError {
	var c config
	if err := cfg.Decode(&c); err != nil {
		return []integration.FieldError{{Message: err.Error()}}
	}
	if u := strings.ToLower(c.URL); !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		return []integration.FieldError{{Path: "/url", Message: "must start with http:// or https://"}}
	}
	return nil
}

// Connect implements integration.Integration. It does no I/O.
func (Integration) Connect(_ context.Context, cfg integration.Config, d integration.Deps) (integration.Instance, error) {
	var c config
	if err := cfg.Decode(&c); err != nil {
		return nil, &integration.ConfigError{Message: err.Error()}
	}
	base, err := url.Parse(strings.TrimRight(c.URL, "/"))
	if err != nil || base.Host == "" {
		return nil, &integration.ConfigError{Field: "url", Message: "the url isn't an absolute URL"}
	}
	base.RawQuery, base.Fragment = "", ""
	key := cfg.InstanceID
	if key == "" {
		key = "test"
	}
	name := cfg.Name
	if name == "" {
		name = "Uptime Kuma"
	}
	return &instance{
		d: d, base: base, slug: strings.ToLower(c.Slug), key: c.Key, name: name, link: c.URL,
		prefix: ID + ":" + key,
	}, nil
}

type instance struct {
	d      integration.Deps
	base   *url.URL
	slug   string
	key    string
	name   string
	link   string
	prefix string
}

// Close implements integration.Instance.
func (in *instance) Close() error {
	in.d.HTTP.CloseIdleConnections()
	return nil
}

// get fetches a path under the instance URL's path. An API key, when given,
// goes in the Authorization header as basic auth with an empty username,
// the way Uptime Kuma expects it on /metrics.
func (in *instance) get(ctx context.Context, withKey bool, segs ...string) (*http.Response, error) {
	u := in.base.JoinPath(segs...)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, &integration.ConfigError{Field: "url", Message: err.Error()}
	}
	if withKey {
		req.SetBasicAuth("", in.key)
	}
	return in.d.HTTP.Do(req)
}

func drain(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
}

// statusPageJSON fetches one of the public status page endpoints. They
// need no credential, so their errors are never auth failures: Uptime Kuma
// answers 404 for an unknown slug, 503 when its database is busy, and 403
// for anything else that went wrong.
func (in *instance) statusPageJSON(ctx context.Context, dst any, segs ...string) error {
	resp, err := in.get(ctx, false, segs...)
	if err != nil {
		return err
	}
	defer drain(resp)
	path := "/" + strings.Join(segs, "/")
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var body struct {
			Msg string `json:"msg"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&body)
		switch resp.StatusCode {
		case http.StatusNotFound:
			return &integration.ConfigError{Field: "slug", Message: fmt.Sprintf("Uptime Kuma has no status page %q (GET %s answered 404)", in.slug, path)}
		case http.StatusForbidden, http.StatusUnauthorized:
			msg := strings.TrimSpace(body.Msg)
			if len(msg) > 200 {
				msg = msg[:200]
			}
			if msg == "" {
				msg = http.StatusText(resp.StatusCode)
			}
			return fmt.Errorf("GET %s answered %d: %s", path, resp.StatusCode, msg)
		}
		return integration.CheckResponse(resp)
	}
	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &integration.RepairError{Code: integration.CodeAPIChanged, Message: fmt.Sprintf("GET %s didn't answer JSON Cha Dash understands: %v", path, err)}
	}
	return nil
}

// fetchMetrics reads /metrics with the API key.
func (in *instance) fetchMetrics(ctx context.Context) (promMetrics, error) {
	resp, err := in.get(ctx, true, "metrics")
	if err != nil {
		return promMetrics{}, err
	}
	defer drain(resp)
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return promMetrics{}, &integration.AuthError{Field: "key", Message: "Uptime Kuma rejected the API key on /metrics"}
	case http.StatusNotFound:
		return promMetrics{}, &integration.RepairError{Code: integration.CodeAPIChanged, Field: "key",
			Message: "GET /metrics answered 404: this Uptime Kuma has no metrics endpoint; remove the API key"}
	}
	if err := integration.CheckResponse(resp); err != nil {
		return promMetrics{}, err
	}
	m, err := parseMetrics(resp.Body)
	if err != nil {
		if ctx.Err() != nil {
			return promMetrics{}, ctx.Err()
		}
		return promMetrics{}, &integration.RepairError{Code: integration.CodeAPIChanged, Message: "GET /metrics: " + err.Error()}
	}
	return m, nil
}
