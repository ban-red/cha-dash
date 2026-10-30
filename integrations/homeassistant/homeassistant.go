// Package homeassistant is the Home Assistant integration: the service's
// health and version from the REST API, and counts and readings derived
// from /api/states (who's home, lights and switches on, house power and
// energy today). Test reads the token's user over the WebSocket API to
// report its rights honestly. See README.md.
package homeassistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	_ "time/tzdata" // HA reports an IANA time zone; the hub's image has no zoneinfo

	"dash.cha.sh/internal/integration"
)

// ID is the integration's ID.
const ID = "homeassistant"

// Refresh plan.
const (
	statesEveryIdle = 30 * time.Minute // /api/states while nobody watches: one point per history slot
	baselineRetry   = time.Hour        // energy meters whose midnight reading is still missing
	maxBaselines    = 64               // meters per history request
)

func init() { integration.Register(Integration{}) }

// Integration is the Home Assistant integration.
type Integration struct{}

const entityPattern = `^sensor\.[a-z0-9_]+$`

// Manifest implements integration.Integration.
func (Integration) Manifest() integration.Manifest {
	return integration.Manifest{
		ID: ID, Name: "Home Assistant", Icon: "homeassistant", Version: "0.1.0", Tier: integration.Silver,
		Category:     integration.CatHome,
		Capabilities: []integration.Capability{"home.entities"},
		ConfigSchema: integration.Object(
			integration.URLProp(),
			integration.TLSProp(),
			integration.Prop("token", integration.Secret("Long-lived access token").Describe("Create one in your Home Assistant profile, under Security. It carries all of its user's rights.")),
			integration.Prop("power_entity", &integration.Schema{Type: "string", Title: "House power sensor", Pattern: entityPattern,
				Description: "Optional: the sensor that measures the whole house's power. Default: the largest power reading."}),
			integration.Prop("energy_entity", &integration.Schema{Type: "string", Title: "House energy sensor", Pattern: entityPattern,
				Description: "Optional: the meter that measures the whole house's energy. Default: the meter that rose most since midnight."}),
			integration.Prop("scripts", &integration.Schema{Type: "array", Title: "Scripts Cha Dash may run",
				Description: "Optional: the scripts (script.…) Cha Dash may run from tiles and ⌘K. Nothing else can be run: no other script, and no other service.",
				Items:       &integration.Schema{Type: "string", Title: "Script", Pattern: scriptPattern}, MaxItems: new(maxScripts)}),
		).Require("url", "token"),
		Poll: integration.Poll{Interval: 60 * time.Second, Idle: 5 * time.Minute, Timeout: 30 * time.Second},
	}
}

type config struct {
	URL          string   `json:"url"`
	Token        string   `json:"token"`
	PowerEntity  string   `json:"power_entity"`
	EnergyEntity string   `json:"energy_entity"`
	Scripts      []string `json:"scripts"`
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
	// The token goes in a header on every request, never in a URL.
	client, err := d.NewHTTPClient(integration.HTTPOptions{Headers: map[string]string{"Authorization": "Bearer " + c.Token}})
	if err != nil {
		return nil, err
	}
	key := cfg.InstanceID
	if key == "" {
		key = "test"
	}
	name := cfg.Name
	if name == "" {
		name = "Home Assistant"
	}
	return &instance{
		d: d, http: client, base: base, token: c.Token, name: name, link: c.URL,
		appID: ID + ":" + key + "/app", powerEntity: c.PowerEntity, energyEntity: c.EnergyEntity, scripts: c.Scripts,
		needFull: true, tz: time.UTC,
		baselines: map[string]float64{}, tried: map[string]time.Time{},
	}, nil
}

// get fetches path under the instance URL's path.
func (in *instance) get(ctx context.Context, path string, query url.Values) (*http.Response, error) {
	u := in.base.JoinPath(strings.Split(strings.Trim(path, "/"), "/")...)
	if strings.HasSuffix(path, "/") {
		u.Path += "/" // the API root is /api/, and /api is a different route
		u.RawPath = ""
	}
	u.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, &integration.ConfigError{Field: "url", Message: err.Error()}
	}
	resp, err := in.http.Do(req)
	if err != nil {
		return nil, err
	}
	if err := integration.CheckResponse(resp); err != nil {
		drain(resp)
		var se *integration.StatusError
		if errors.As(err, &se) && (se.Code == http.StatusNotFound || se.Code == http.StatusGone) {
			return nil, missing{&integration.RepairError{Code: integration.CodeAPIChanged,
				Message: se.Error() + ": this doesn't look like Home Assistant's REST API (check the URL, and that the api integration is loaded)"}}
		}
		return nil, err
	}
	return resp, nil
}

// errMissing marks an endpoint that answered 404 or 410.
var errMissing = errors.New("homeassistant: no such endpoint")

// missing is an api_changed Repair that errors.Is errMissing.
type missing struct{ *integration.RepairError }

func (m missing) Unwrap() []error { return []error{m.RepairError, errMissing} }

func drain(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
}

func (in *instance) getJSON(ctx context.Context, path string, query url.Values, dst any) error {
	resp, err := in.get(ctx, path, query)
	if err != nil {
		return err
	}
	defer drain(resp)
	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &integration.RepairError{Code: integration.CodeAPIChanged, Message: fmt.Sprintf("GET %s didn't answer JSON Cha Dash understands: %v", path, err)}
	}
	return nil
}

// Close implements integration.Instance.
func (in *instance) Close() error {
	in.http.CloseIdleConnections()
	return nil
}
