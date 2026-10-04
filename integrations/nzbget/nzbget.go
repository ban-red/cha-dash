// Package nzbget reads NZBGet's JSON-RPC API: the download speed, the queue
// and what's left of it, and whether downloading is paused, with basic
// auth. See README.md.
package nzbget

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"dash.cha.sh/internal/integration"
)

// ID is the integration's ID.
const ID = "nzbget"

// minMajor is the oldest NZBGet read: 16 (2016), whose status and listgroups
// answers have every field used here.
const minMajor = 16

// versionEvery is how often the version is read again while watched.
const versionEvery = time.Hour

func init() { integration.Register(Integration{}) }

// Integration is the NZBGet integration.
type Integration struct{}

// Manifest implements integration.Integration.
func (Integration) Manifest() integration.Manifest {
	user := integration.String("Username").Describe("Settings → Security: the RestrictedUsername (it can't see or change settings), or the ControlUsername. Leave both empty if NZBGet has no password.")
	pass := integration.Secret("Password").Describe("That user's password (RestrictedPassword or ControlPassword).")
	return integration.Manifest{
		ID: ID, Name: "NZBGet", Icon: "nzbget", Version: "0.1.0", Tier: integration.Silver,
		Capabilities: []integration.Capability{"downloads.queue"},
		ConfigSchema: integration.Object(
			integration.Prop("url", integration.URL("URL").Describe("NZBGet's web interface, e.g. http://nas.home.arpa:6789, with its path if it's behind a reverse proxy.")),
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

	version string
	readVer time.Time
}

// Close implements integration.Instance.
func (in *instance) Close() error { return nil }

// rpcError is a JSON-RPC error inside a 200.
type rpcError struct {
	Name    string `json:"name"`
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// call runs one JSON-RPC method: POST <url>/jsonrpc. The credentials go in
// basic auth, never in the path (NZBGet also accepts /user:pass/jsonrpc).
func (in *instance) call(ctx context.Context, method string, params []any, dst any) error {
	if params == nil {
		params = []any{}
	}
	body, err := json.Marshal(map[string]any{"version": "1.1", "id": 1, "method": method, "params": params})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, in.base+"/jsonrpc", bytes.NewReader(body))
	if err != nil {
		return &integration.ConfigError{Field: "url", Message: "the URL can't be used: " + err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
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
			return &integration.AuthError{Field: "username", Message: "NZBGet wants a sign-in: enter its username and password"}
		}
		return &integration.AuthError{Field: "password", Message: "NZBGet rejected the username or password"}
	case http.StatusNotFound:
		return &integration.RepairError{Code: integration.CodeAPIChanged, Field: "url",
			Message: "POST " + req.URL.Path + " answered 404 Not Found: check that the URL is NZBGet's web interface"}
	}
	if err := integration.CheckResponse(resp); err != nil {
		return err
	}
	var r struct {
		Result json.RawMessage `json:"result"`
		Error  *rpcError       `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil || (r.Result == nil && r.Error == nil) {
		return &integration.RepairError{Code: integration.CodeAPIChanged, Field: "url",
			Message: "POST " + req.URL.Path + " didn't answer with NZBGet's JSON-RPC: check the URL"}
	}
	if r.Error != nil {
		if strings.Contains(strings.ToLower(r.Error.Message), "access denied") {
			return &integration.AuthError{Field: "username",
				Message: fmt.Sprintf("NZBGet refused %s to this user: use the RestrictedUsername or the ControlUsername, not the AddUsername", method)}
		}
		return &integration.RepairError{Code: integration.CodeAPIChanged,
			Message: fmt.Sprintf("NZBGet answered %s with an error: %s", method, strings.TrimSpace(r.Error.Message))}
	}
	if err := json.Unmarshal(r.Result, dst); err != nil {
		return &integration.RepairError{Code: integration.CodeAPIChanged,
			Message: fmt.Sprintf("NZBGet's %s answer isn't in the expected shape: %v", method, err)}
	}
	return nil
}

// readVersion reads the version and checks it.
func (in *instance) readVersion(ctx context.Context) error {
	var v string
	if err := in.call(ctx, "version", nil, &v); err != nil {
		return err
	}
	v = strings.TrimSpace(v)
	major, err := strconv.Atoi(strings.SplitN(v, ".", 2)[0])
	if err == nil && major < minMajor {
		return &integration.RepairError{Code: integration.CodeUnsupportedVersion,
			Message: fmt.Sprintf("NZBGet %s is older than this integration supports (%d or later)", v, minMajor)}
	}
	in.version = v
	return nil
}

// grant is what an NZBGet user can do. The API doesn't say which of the
// three users signed in without reading the settings (which hold the other
// passwords), so it's the restricted user's grant: it can still pause,
// resume and delete downloads.
func (in *instance) grant() integration.Grant {
	g := integration.Grant{Name: "NZBGet user", Access: integration.AccessReadWrite,
		Detail: "the restricted user can pause, resume and delete downloads but can't see or change settings; the control user can do everything"}
	if in.user == "" {
		g.Access = integration.AccessAdmin
		g.Detail = "NZBGet has no password, so anything that reaches it has full control"
	}
	return g
}

// Test implements integration.Instance.
func (in *instance) Test(ctx context.Context) integration.TestResult {
	if err := in.readVersion(ctx); err != nil {
		return integration.Failed(err)
	}
	var st status
	if err := in.call(ctx, "status", nil, &st); err != nil {
		r := integration.Failed(err)
		r.Version = in.version
		return r
	}
	return integration.TestResult{OK: true, Version: in.version, Grants: []integration.Grant{in.grant()}}
}

// status is the status method's answer (the fields read). Sizes are MiB,
// rates bytes per second.
type status struct {
	RemainingSizeMB *float64 `json:"RemainingSizeMB"`
	DownloadRate    *float64 `json:"DownloadRate"`
	DownloadRateLo  *float64 `json:"DownloadRateLo"` // NZBGet 24+: the rate in two 32-bit halves
	DownloadRateHi  *float64 `json:"DownloadRateHi"`
	DownloadPaused  bool     `json:"DownloadPaused"`
	FreeDiskSpaceMB *float64 `json:"FreeDiskSpaceMB"`
	PostJobCount    *float64 `json:"PostJobCount"`
	QuotaReached    bool     `json:"QuotaReached"`
}

// rate is the download rate in bytes per second, or nil.
func (s status) rate() *float64 {
	if s.DownloadRateLo != nil && s.DownloadRateHi != nil {
		r := *s.DownloadRateHi*4294967296 + *s.DownloadRateLo
		return &r
	}
	return s.DownloadRate
}

// group is one listgroups entry: only its status (names stay home).
type group struct {
	Status string `json:"Status"`
}

// Sync implements integration.Instance: status and listgroups.
func (in *instance) Sync(ctx context.Context, want integration.Interest) error {
	now := in.d.Now()
	if in.version == "" || (want.Watched && now.Sub(in.readVer) >= versionEvery) {
		if err := in.readVersion(ctx); err != nil {
			return err
		}
		in.readVer = now
		in.d.Emit.SetVersion(in.version)
	}
	var st status
	if err := in.call(ctx, "status", nil, &st); err != nil {
		return err
	}
	var groups []group
	if err := in.call(ctx, "listgroups", []any{0}, &groups); err != nil {
		return err
	}
	state := "running"
	if st.DownloadPaused {
		state = "paused"
	}
	h := integration.Health{Status: integration.HealthOK}
	if st.QuotaReached {
		h = integration.Health{Status: integration.HealthAttention, Reason: "NZBGet reached its download quota and stopped downloading"}
	}
	attrs := map[string]any{"type": ID, "url": in.base, "version": in.version, "paused": st.DownloadPaused, "quota_reached": st.QuotaReached}
	if st.FreeDiskSpaceMB != nil {
		attrs["free_disk_gib"] = math.Round(*st.FreeDiskSpaceMB/1024*10) / 10
	}
	if st.PostJobCount != nil {
		attrs["post_jobs"] = *st.PostJobCount
	}
	res := integration.Resource{ID: in.rid, Kind: "app", Name: in.cfg.Name, State: state, Health: h, Attrs: attrs,
		Caps: []string{"downloads.queue"}, Links: []integration.Link{{Title: "NZBGet", URL: in.base}}}
	speed := integration.Unknown()
	if r := st.rate(); r != nil {
		speed = math.Round(*r*8/1e5) / 10
	}
	left := integration.Unknown()
	if st.RemainingSizeMB != nil {
		left = math.Round(*st.RemainingSizeMB/102.4) / 10
	}
	return in.d.Emit.Replace("", integration.Facts{
		Resources: []integration.Resource{res},
		Relations: []integration.Relation{{From: in.d.Emit.Self(), Type: "monitors", To: in.rid}},
		Metrics: []integration.Metric{
			{Resource: in.rid, Key: "dl.mbps", Unit: "Mb/s", Max: 1000, Band: [2]float64{0, 60}, Value: speed},
			{Resource: in.rid, Key: "queue", Unit: "count", Max: 1000, Band: [2]float64{0, 15}, Value: float64(len(groups))},
			{Resource: in.rid, Key: "left.gib", Unit: "GiB", Max: 10000, Band: [2]float64{0, 50}, Value: left},
		},
	})
}
