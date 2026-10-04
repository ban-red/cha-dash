// Package transmission reads Transmission's RPC: transfer speeds and how many
// torrents are seeding, downloading and queued, with the session-id
// handshake and optional basic auth. See README.md.
package transmission

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"dash.cha.sh/internal/integration"
)

// ID is the integration's ID.
const ID = "transmission"

// DefaultRPCPath is where Transmission serves its web interface and RPC
// (rpc-url in settings.json); the endpoint is "rpc" under it.
const DefaultRPCPath = "/transmission/"

// minRPCVersion is the oldest RPC version read: 15, Transmission 2.80.
const minRPCVersion = 15

// sessionHeader carries Transmission's CSRF token.
const sessionHeader = "X-Transmission-Session-Id"

var rpcPathRE = regexp.MustCompile(`^(/[A-Za-z0-9._~-]+)*/?$`)

func init() { integration.Register(Integration{}) }

// Integration is the Transmission integration.
type Integration struct{}

// Manifest implements integration.Integration.
func (Integration) Manifest() integration.Manifest {
	path := integration.String("RPC path").Describe("Transmission's rpc-url: where its web interface is, under the URL. The hub posts to rpc under it.").WithDefault(DefaultRPCPath)
	path.Pattern = rpcPathRE.String()
	user := integration.String("Username").Describe("rpc-username in settings.json. Leave both empty if Transmission doesn't ask for one.")
	pass := integration.Secret("Password").Describe("rpc-password in settings.json.")
	return integration.Manifest{
		ID: ID, Name: "Transmission", Icon: "transmission", Version: "0.1.0", Tier: integration.Silver,
		Capabilities: []integration.Capability{"downloads.queue"},
		ConfigSchema: integration.Object(
			integration.Prop("url", integration.URL("URL").Describe("Transmission's address, without the /transmission/ path, e.g. https://torrents.home.arpa or http://nas.home.arpa:9091.")),
			integration.TLSProp(),
			integration.Prop("rpc_path", path),
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
	path := cfg.String("rpc_path")
	if path == "" {
		path = DefaultRPCPath
	}
	if !strings.HasSuffix(path, "/") {
		path += "/"
	}
	base := strings.TrimRight(cfg.String("url"), "/")
	key := cfg.InstanceID
	if key == "" {
		key = "test"
	}
	return &instance{cfg: cfg, d: d, base: base, endpoint: base + path + "rpc", web: base + path + "web/",
		user: cfg.String("username"), pass: cfg.String("password"), rid: ID + ":" + key + "/app"}, nil
}

type instance struct {
	cfg        integration.Config
	d          integration.Deps
	base       string
	endpoint   string // <url><rpc_path>rpc
	web        string // <url><rpc_path>web/, the web interface
	user, pass string
	rid        string

	mu      sync.Mutex
	session string // the last X-Transmission-Session-Id
}

// Close implements integration.Instance.
func (in *instance) Close() error { return nil }

// rpcResponse is the envelope of every answer.
type rpcResponse struct {
	Result    string          `json:"result"`
	Arguments json.RawMessage `json:"arguments"`
}

// call runs one RPC method. Transmission answers 409 with a fresh session id
// to a request that lacks the current one (the first, and any after a
// restart); the call is sent again once with it.
func (in *instance) call(ctx context.Context, method string, args any, dst any) error {
	body, err := json.Marshal(map[string]any{"method": method, "arguments": args})
	if err != nil {
		return err
	}
	retry, err := in.post(ctx, method, body, dst)
	if retry {
		if retry, err = in.post(ctx, method, body, dst); retry {
			return &integration.RepairError{Code: integration.CodeAPIChanged, Field: "rpc_path",
				Message: "Transmission's RPC keeps answering 409 Conflict: check the URL and the RPC path"}
		}
	}
	return err
}

// post sends one request. retry is true when Transmission handed out a new
// session id (409) and the request should go again.
func (in *instance) post(ctx context.Context, method string, body []byte, dst any) (retry bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, in.endpoint, bytes.NewReader(body))
	if err != nil {
		return false, &integration.ConfigError{Field: "url", Message: "the URL can't be used: " + err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	in.mu.Lock()
	if in.session != "" {
		req.Header.Set(sessionHeader, in.session)
	}
	in.mu.Unlock()
	if in.user != "" {
		req.SetBasicAuth(in.user, in.pass)
	}
	resp, err := in.d.HTTP.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusConflict {
		id := resp.Header.Get(sessionHeader)
		drain(resp)
		if id == "" {
			return false, &integration.RepairError{Code: integration.CodeAPIChanged, Field: "rpc_path",
				Message: "POST " + req.URL.Path + " answered 409 without a session id: check the URL and the RPC path"}
		}
		in.mu.Lock()
		in.session = id
		in.mu.Unlock()
		return true, nil
	}
	if err := in.check(resp); err != nil {
		return false, err
	}
	var r rpcResponse
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil || r.Result == "" {
		return false, &integration.RepairError{Code: integration.CodeAPIChanged, Field: "rpc_path",
			Message: "POST " + req.URL.Path + " didn't answer with Transmission's RPC: check the URL and the RPC path"}
	}
	if r.Result != "success" {
		return false, fmt.Errorf("transmission answered %s with %q", method, r.Result)
	}
	if err := json.Unmarshal(r.Arguments, dst); err != nil {
		return false, &integration.RepairError{Code: integration.CodeAPIChanged,
			Message: fmt.Sprintf("Transmission's %s answer isn't in the expected shape: %v", method, err)}
	}
	return false, nil
}

// check turns the RPC's HTTP statuses into the framework's errors.
func (in *instance) check(resp *http.Response) error {
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		drain(resp)
		if in.user == "" {
			return &integration.AuthError{Field: "username", Message: "Transmission wants a sign-in: enter its RPC username and password"}
		}
		return &integration.AuthError{Field: "password", Message: "Transmission rejected the username or password"}
	case http.StatusForbidden:
		drain(resp)
		return &integration.AuthError{Field: "username",
			Message: "Transmission refused the hub's address: add it to rpc-whitelist in settings.json, or turn rpc-whitelist-enabled off and use a password"}
	case http.StatusNotFound:
		drain(resp)
		return &integration.RepairError{Code: integration.CodeAPIChanged, Field: "rpc_path",
			Message: "POST " + resp.Request.URL.Path + " answered 404 Not Found: check the URL and the RPC path (rpc-url, /transmission/ by default)"}
	}
	return integration.CheckResponse(resp)
}

// drain reads what's left of a body worth keeping the connection for.
func drain(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
}

// session is session-get's answer (the fields asked for).
type session struct {
	Version           string `json:"version"` // "4.0.6 (38c164933e)"
	RPCVersion        int    `json:"rpc-version"`
	RPCVersionMinimum int    `json:"rpc-version-minimum"`
	AltSpeedEnabled   *bool  `json:"alt-speed-enabled"`
}

// version is the release without the build hash.
func (s session) version() string {
	v, _, _ := strings.Cut(strings.TrimSpace(s.Version), " ")
	return v
}

// readSession reads session-get and checks the RPC version.
func (in *instance) readSession(ctx context.Context) (session, error) {
	var s session
	err := in.call(ctx, "session-get", map[string]any{"fields": []string{"version", "rpc-version", "rpc-version-minimum", "alt-speed-enabled"}}, &s)
	if err != nil {
		return s, err
	}
	if s.RPCVersion < minRPCVersion {
		return s, &integration.RepairError{Code: integration.CodeUnsupportedVersion,
			Message: fmt.Sprintf("Transmission %s (RPC version %d) is older than this integration supports (2.80 or later)", s.version(), s.RPCVersion)}
	}
	return s, nil
}

// grant is what the RPC account can do: everything.
func (in *instance) grant() integration.Grant {
	g := integration.Grant{Name: "Transmission RPC", Access: integration.AccessAdmin,
		Detail: "the RPC account can add, remove and change torrents and settings; Transmission has no read-only accounts"}
	if in.user == "" {
		g.Detail = "Transmission lets this address in without signing in, with full control"
	}
	return g
}

// Test implements integration.Instance.
func (in *instance) Test(ctx context.Context) integration.TestResult {
	s, err := in.readSession(ctx)
	if err != nil {
		return integration.Failed(err)
	}
	return integration.TestResult{OK: true, Version: s.version(), Grants: []integration.Grant{in.grant()}}
}

// stats is session-stats' answer (the fields read).
type stats struct {
	DownloadSpeed *float64 `json:"downloadSpeed"` // bytes/s
	UploadSpeed   *float64 `json:"uploadSpeed"`
}

// torrent is one torrent-get entry: only its status and error.
type torrent struct {
	Status int `json:"status"`
	Error  int `json:"error"`
}

// Torrent statuses (tr_torrent_activity) and errors (tr_stat_errtype).
const (
	statusStopped      = 0
	statusCheckWait    = 1
	statusCheck        = 2
	statusDownloadWait = 3
	statusDownload     = 4
	statusSeedWait     = 5
	statusSeed         = 6
	errLocal           = 3 // a local problem: a missing file, a full disk
)

// Sync implements integration.Instance: session-get, session-stats and the
// torrents' status, three small calls.
func (in *instance) Sync(ctx context.Context, _ integration.Interest) error {
	s, err := in.readSession(ctx)
	if err != nil {
		return err
	}
	in.d.Emit.SetVersion(s.version())
	var st stats
	if err := in.call(ctx, "session-stats", map[string]any{}, &st); err != nil {
		return err
	}
	var tg struct {
		Torrents []torrent `json:"torrents"`
	}
	if err := in.call(ctx, "torrent-get", map[string]any{"fields": []string{"status", "error"}}, &tg); err != nil {
		return err
	}
	var seeding, leeching, queued, stopped, errored int
	for _, t := range tg.Torrents {
		switch t.Status {
		case statusSeed:
			seeding++
		case statusDownload:
			leeching++
		case statusDownloadWait, statusSeedWait:
			queued++
		case statusStopped:
			stopped++
		}
		if t.Error == errLocal {
			errored++
		}
	}
	h := integration.Health{Status: integration.HealthOK}
	if errored > 0 {
		h = integration.Health{Status: integration.HealthAttention, Reason: fmt.Sprintf("%d %s an error", errored, plural(errored, "torrent has", "torrents have"))}
	}
	attrs := map[string]any{"type": ID, "url": in.base, "version": s.version(), "rpc_version": s.RPCVersion,
		"torrents": len(tg.Torrents), "stopped": stopped, "errored": errored}
	if s.AltSpeedEnabled != nil {
		attrs["alt_speed"] = *s.AltSpeedEnabled
	}
	res := integration.Resource{ID: in.rid, Kind: "app", Name: in.cfg.Name, State: "running", Health: h, Attrs: attrs,
		Caps: []string{"downloads.queue"}, Links: []integration.Link{{Title: "Transmission", URL: in.web}}}
	count := func(k string, max float64, band [2]float64, v int) integration.Metric {
		return integration.Metric{Resource: in.rid, Key: k, Unit: "count", Max: max, Band: band, Value: float64(v)}
	}
	speed := func(k string, bps *float64, band [2]float64) integration.Metric {
		return integration.Metric{Resource: in.rid, Key: k, Unit: "Mb/s", Max: 1000, Band: band, Value: mbps(bps)}
	}
	return in.d.Emit.Replace("", integration.Facts{
		Resources: []integration.Resource{res},
		Relations: []integration.Relation{{From: in.d.Emit.Self(), Type: "monitors", To: in.rid}},
		Metrics: []integration.Metric{
			speed("dl.mbps", st.DownloadSpeed, [2]float64{0, 60}),
			speed("ul.mbps", st.UploadSpeed, [2]float64{0, 20}),
			count("seeding", 10000, [2]float64{50, 100}, seeding),
			count("leeching", 1000, [2]float64{0, 30}, leeching),
			count("queued", 1000, [2]float64{0, 10}, queued),
		},
	})
}

// mbps is bytes per second in megabits per second, to 0.1, or unknown.
func mbps(bps *float64) float64 {
	if bps == nil {
		return integration.Unknown()
	}
	return math.Round(*bps*8/1e5) / 10
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
