// Package nextcloud reads Nextcloud's serverinfo (Monitoring) API: users,
// active users, files, shares and free space, with a serverinfo token or an
// administrator's app password. See README.md.
package nextcloud

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"dash.cha.sh/internal/integration"
)

// ID is the integration's ID.
const ID = "nextcloud"

// infoPath is the serverinfo endpoint. skipApps and skipUpdate leave out the
// app and update checks, which ask the app store and are slow; releases
// without the parameters ignore them.
const infoPath = "/ocs/v2.php/apps/serverinfo/api/v1/info"

// lowSpace is the free space below which the app needs attention: uploads
// start failing.
const lowSpace = 5 // GiB

func init() { integration.Register(Integration{}) }

// Integration is the Nextcloud integration.
type Integration struct{}

// Manifest implements integration.Integration.
func (Integration) Manifest() integration.Manifest {
	token := integration.Secret("Serverinfo token").Describe("The token set with occ config:app:set serverinfo token. It can read the monitoring endpoint and nothing else. Or leave it empty and use a username and app password.")
	user := integration.String("Username").Describe("An administrator's username, if you don't use a token.")
	pass := integration.Secret("App password").Describe("An app password for that user (Personal settings → Security → Devices & sessions).")
	return integration.Manifest{
		ID: ID, Name: "Nextcloud", Icon: "nextcloud", Version: "0.1.0", Tier: integration.Silver,
		Capabilities: []integration.Capability{"files.storage"},
		ConfigSchema: integration.Object(
			integration.Prop("url", integration.URL("URL").Describe("Nextcloud's address, with its path if it has one, e.g. https://nextcloud.home.arpa.")),
			integration.TLSProp(),
			integration.Prop("token", token),
			integration.Prop("username", user),
			integration.Prop("password", pass),
		).Require("url"),
		Poll: integration.Poll{Interval: 2 * time.Minute, Idle: 10 * time.Minute, Timeout: 30 * time.Second},
	}
}

// ValidateConfig implements integration.ConfigValidator: a token, or a
// username with an app password.
func (Integration) ValidateConfig(cfg integration.Config) []integration.FieldError {
	token, user, pass := cfg.String("token"), cfg.String("username"), cfg.String("password")
	switch {
	case user != "" && pass == "":
		return []integration.FieldError{{Path: "/password", Message: "is required with a username"}}
	case user == "" && pass != "":
		return []integration.FieldError{{Path: "/username", Message: "is required with an app password"}}
	case token == "" && user == "":
		return []integration.FieldError{{Path: "/token", Message: "is required, unless you sign in with a username and app password"}}
	}
	return nil
}

// Connect implements integration.Integration.
func (Integration) Connect(_ context.Context, cfg integration.Config, d integration.Deps) (integration.Instance, error) {
	key := cfg.InstanceID
	if key == "" {
		key = "test"
	}
	return &instance{cfg: cfg, d: d, base: strings.TrimRight(cfg.String("url"), "/"), token: cfg.String("token"),
		user: cfg.String("username"), pass: cfg.String("password"), rid: ID + ":" + key + "/app"}, nil
}

type instance struct {
	cfg        integration.Config
	d          integration.Deps
	base       string
	token      string
	user, pass string
	rid        string
}

// Close implements integration.Instance.
func (in *instance) Close() error { return nil }

// num is a number serverinfo may also send as a string, or as false when it
// can't tell (freespace with disk_free_space disabled); anything else is
// unknown.
type num struct{ v *float64 }

func (n *num) UnmarshalJSON(b []byte) error {
	var x any
	if err := json.Unmarshal(b, &x); err != nil {
		return err
	}
	switch v := x.(type) {
	case float64:
		n.v = &v
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
			n.v = &f
		}
	}
	return nil
}

// value is the number, or integration.Unknown().
func (n num) value() float64 {
	if n.v == nil {
		return integration.Unknown()
	}
	return *n.v
}

// envelope is every OCS answer. data is an object on success and often an
// empty array on failure.
type envelope struct {
	OCS struct {
		Meta struct {
			Status     string `json:"status"`
			StatusCode int    `json:"statuscode"`
			Message    string `json:"message"`
		} `json:"meta"`
		Data json.RawMessage `json:"data"`
	} `json:"ocs"`
}

// info is serverinfo's data (the fields read).
type info struct {
	Nextcloud struct {
		System struct {
			Version   string `json:"version"`
			FreeSpace num    `json:"freespace"` // bytes
		} `json:"system"`
		Storage struct {
			NumUsers num `json:"num_users"`
			NumFiles num `json:"num_files"`
		} `json:"storage"`
		Shares struct {
			NumShares num `json:"num_shares"`
		} `json:"shares"`
	} `json:"nextcloud"`
	Server struct {
		PHP struct {
			Version string `json:"version"`
		} `json:"php"`
		Database struct {
			Type    string `json:"type"`
			Version string `json:"version"`
		} `json:"database"`
	} `json:"server"`
	ActiveUsers struct {
		Last5Minutes num `json:"last5minutes"`
		Last1Hour    num `json:"last1hour"`
		Last24Hours  num `json:"last24hours"`
	} `json:"activeUsers"`
}

// read fetches serverinfo.
func (in *instance) read(ctx context.Context) (info, error) {
	var out info
	q := url.Values{"format": {"json"}, "skipApps": {"true"}, "skipUpdate": {"true"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, in.base+infoPath+"?"+q.Encode(), nil)
	if err != nil {
		return out, &integration.ConfigError{Field: "url", Message: "the URL can't be used: " + err.Error()}
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("OCS-APIRequest", "true")
	if in.token != "" {
		req.Header.Set("NC-Token", in.token)
	} else {
		req.SetBasicAuth(in.user, in.pass)
	}
	resp, err := in.d.HTTP.Do(req)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return out, in.rejected()
	case http.StatusForbidden:
		return out, in.notAdmin()
	case http.StatusNotFound:
		return out, &integration.RepairError{Code: integration.CodeAPIChanged, Field: "url",
			Message: "GET " + infoPath + " answered 404 Not Found: check the URL, and that the Monitoring app (serverinfo) is enabled"}
	}
	if err := integration.CheckResponse(resp); err != nil {
		return out, err
	}
	var env envelope
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil || env.OCS.Meta.StatusCode == 0 {
		return out, &integration.RepairError{Code: integration.CodeAPIChanged, Field: "url",
			Message: "GET " + infoPath + " didn't answer with Nextcloud's JSON: check the URL (a login page, or a proxy in front?)"}
	}
	// v2 OCS maps its status onto HTTP, but a proxy or an old release may
	// still send a 200 with the status inside.
	switch env.OCS.Meta.StatusCode {
	case 100, 200:
	case 401, 997:
		return out, in.rejected()
	case 403:
		return out, in.notAdmin()
	default:
		return out, &integration.RepairError{Code: integration.CodeAPIChanged,
			Message: fmt.Sprintf("Nextcloud's serverinfo answered status %d: %s", env.OCS.Meta.StatusCode, strings.TrimSpace(env.OCS.Meta.Message))}
	}
	if err := json.Unmarshal(env.OCS.Data, &out); err != nil {
		return out, &integration.RepairError{Code: integration.CodeAPIChanged,
			Message: "Nextcloud's serverinfo data isn't in the expected shape: " + err.Error()}
	}
	return out, nil
}

func (in *instance) rejected() error {
	if in.token != "" {
		return &integration.AuthError{Field: "token", Message: "Nextcloud rejected the serverinfo token"}
	}
	return &integration.AuthError{Field: "password", Message: "Nextcloud rejected the username or app password"}
}

func (in *instance) notAdmin() error {
	if in.token != "" {
		return &integration.AuthError{Field: "token", Message: "Nextcloud refused the serverinfo token"}
	}
	return &integration.AuthError{Field: "username",
		Message: "Nextcloud's serverinfo needs an administrator: use an admin's app password, or a serverinfo token instead"}
}

// grant is what the credential can do.
func (in *instance) grant() integration.Grant {
	if in.token != "" {
		return integration.Grant{Name: "Nextcloud serverinfo token", Access: integration.AccessRead,
			Detail: "reads the monitoring endpoint and nothing else"}
	}
	return integration.Grant{Name: "Nextcloud admin account", Access: integration.AccessAdmin,
		Detail: "an app password has all its account's rights, and serverinfo needs an administrator; a serverinfo token can only read"}
}

// Test implements integration.Instance.
func (in *instance) Test(ctx context.Context) integration.TestResult {
	inf, err := in.read(ctx)
	if err != nil {
		return integration.Failed(err)
	}
	return integration.TestResult{OK: true, Version: inf.Nextcloud.System.Version, Grants: []integration.Grant{in.grant()}}
}

// Sync implements integration.Instance: one serverinfo read.
func (in *instance) Sync(ctx context.Context, _ integration.Interest) error {
	inf, err := in.read(ctx)
	if err != nil {
		return err
	}
	data, sys := inf, inf.Nextcloud.System
	if sys.Version != "" {
		in.d.Emit.SetVersion(sys.Version)
	}
	free := integration.Unknown()
	if sys.FreeSpace.v != nil {
		free = math.Round(*sys.FreeSpace.v/(1<<30)*10) / 10
	}
	h := integration.Health{Status: integration.HealthOK}
	if !math.IsNaN(free) && free < lowSpace {
		h = integration.Health{Status: integration.HealthAttention, Reason: fmt.Sprintf("Nextcloud has %.1f GiB free", free)}
	}
	attrs := map[string]any{"type": ID, "url": in.base}
	for k, v := range map[string]string{"version": sys.Version, "php_version": data.Server.PHP.Version, "database": data.Server.Database.Type} {
		if v != "" {
			attrs[k] = v
		}
	}
	if u := data.Nextcloud.Storage.NumUsers.v; u != nil {
		attrs["users"] = *u
	}
	res := integration.Resource{ID: in.rid, Kind: "app", Name: in.cfg.Name, State: "running", Health: h, Attrs: attrs,
		Caps: []string{"files.storage"}, Links: []integration.Link{{Title: "Nextcloud", URL: in.base}}}
	count := func(k string, max float64, band [2]float64, v float64) integration.Metric {
		return integration.Metric{Resource: in.rid, Key: k, Unit: "count", Max: max, Band: band, Value: v}
	}
	return in.d.Emit.Replace("", integration.Facts{
		Resources: []integration.Resource{res},
		Relations: []integration.Relation{{From: in.d.Emit.Self(), Type: "monitors", To: in.rid}},
		Metrics: []integration.Metric{
			count("users", 10000, [2]float64{0, 50}, data.Nextcloud.Storage.NumUsers.value()),
			count("users.active", 10000, [2]float64{0, 10}, data.ActiveUsers.Last5Minutes.value()),
			count("users.24h", 10000, [2]float64{0, 20}, data.ActiveUsers.Last24Hours.value()),
			count("files", 1e9, [2]float64{0, 1e7}, data.Nextcloud.Storage.NumFiles.value()),
			count("shares", 1e6, [2]float64{0, 1000}, data.Nextcloud.Shares.NumShares.value()),
			{Resource: in.rid, Key: "free.gib", Unit: "GiB", Max: 1e5, Band: [2]float64{50, 1e5}, Value: free},
		},
	})
}
