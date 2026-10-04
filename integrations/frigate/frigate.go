// Package frigate is the Frigate NVR integration: the service and its
// cameras, camera health from /api/stats (or the /ws WebSocket while someone
// watches), detector inference time, and tracked objects per camera over the
// last 24 hours. It never requests a picture, snapshot, clip or recording.
// See README.md.
package frigate

import (
	"context"
	"net"
	"net/url"
	"strings"
	"time"

	"dash.cha.sh/internal/integration"
)

// ID is the integration's ID.
const ID = "frigate"

// Refresh plan. Sync runs every Poll.Interval while watched and every
// Poll.Idle otherwise; each sync reads only what's due.
const (
	configEvery        = 15 * time.Minute // /api/config (42 kB) and /api/profile
	eventsEveryWatched = 5 * time.Minute  // /api/events for the last 24 h
	eventsEveryIdle    = 30 * time.Minute // one point per history slot
	eventsDebounce     = 30 * time.Second // after the WebSocket reports a new object
	eventsLimit        = 500
	defaultStatsEvery  = 60 * time.Second // mqtt.stats_interval: how often /ws pushes stats
	internalPort       = "5000"           // Frigate's unauthenticated port (8971 is the authenticated one)
)

func init() { integration.Register(Integration{}) }

// Integration is the Frigate integration.
type Integration struct{}

// Manifest implements integration.Integration.
func (Integration) Manifest() integration.Manifest {
	return integration.Manifest{
		ID: ID, Name: "Frigate", Icon: "frigate", Version: "0.1.0", Tier: integration.Silver,
		Capabilities: []integration.Capability{"cameras", "cameras.events"},
		ConfigSchema: integration.Object(
			integration.Prop("url", integration.URL("URL").Describe("Frigate's address. Prefer the authenticated port (8971) with a viewer account; the internal port (5000) needs no sign-in and grants admin to anyone who can reach it.")),
			integration.TLSProp(),
			integration.Prop("username", integration.String("Username").Describe("A Frigate user, ideally with the viewer role. Leave empty on the internal port.")),
			integration.Prop("password", integration.Secret("Password")),
		).Require("url"),
		Poll: integration.Poll{Interval: 30 * time.Second, Idle: 5 * time.Minute, Timeout: 30 * time.Second},
	}
}

type config struct {
	URL      string `json:"url"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// ValidateConfig implements integration.ConfigValidator: a username and a
// password come together.
func (Integration) ValidateConfig(cfg integration.Config) []integration.FieldError {
	var c config
	if err := cfg.Decode(&c); err != nil {
		return []integration.FieldError{{Message: err.Error()}}
	}
	var errs []integration.FieldError
	if u := strings.ToLower(c.URL); !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		errs = append(errs, integration.FieldError{Path: "/url", Message: "must start with http:// or https://"})
	}
	switch {
	case c.Username != "" && c.Password == "":
		errs = append(errs, integration.FieldError{Path: "/password", Message: "is required with a username"})
	case c.Username == "" && c.Password != "":
		errs = append(errs, integration.FieldError{Path: "/username", Message: "is required with a password"})
	}
	return errs
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
		name = "Frigate"
	}
	in := &instance{
		d: d, base: base, http: d.HTTP, name: name, link: c.URL,
		prefix: ID + ":" + key, creds: c.Username != "",
		cams: map[string]*camera{}, needFull: true, inference: integration.Unknown(),
	}
	in.appID = in.prefix + "/app"
	if in.creds {
		s := &session{login: base.JoinPath("api", "login"), user: c.Username, password: c.Password}
		client, err := d.NewHTTPClient(integration.HTTPOptions{Session: s})
		if err != nil {
			return nil, err
		}
		in.http = client
	}
	return in, nil
}

// port is the URL's port, with the scheme's default.
func port(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	if u.Scheme == "https" {
		return "443"
	}
	return "80"
}

// hostOf returns the host of a camera stream URL (rtsp://user:pass@host/…,
// ffmpeg:http://host/…), or "" for a loopback address or anything that isn't
// a URL. Only the host is kept: never a path or a credential.
func hostOf(src string) string {
	src = strings.TrimPrefix(strings.TrimSpace(src), "ffmpeg:")
	if i := strings.IndexByte(src, '#'); i >= 0 {
		src = src[:i]
	}
	u, err := url.Parse(src)
	if err != nil || u.Host == "" {
		return ""
	}
	h := u.Hostname()
	if h == "localhost" {
		return ""
	}
	if ip := net.ParseIP(h); ip != nil && ip.IsLoopback() {
		return ""
	}
	return h
}

// Close implements integration.Instance. The runtime closes the HTTP
// clients; Frigate's logout only clears the browser cookie, so there's no
// server-side session to end.
func (in *instance) Close() error {
	in.http.CloseIdleConnections()
	return nil
}
