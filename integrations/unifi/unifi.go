// Package unifi reads a UniFi Network Application, read only: its adopted
// devices (gateways, switches, access points) with their state, uptime and
// firmware, the site's client counts, and the WAN's health. It signs in
// with a local account's session, on a UniFi OS console or a standalone
// controller. See README.md for why not the API key.
package unifi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"dash.cha.sh/internal/integration"
)

// ID is the integration's ID.
const ID = "unifi"

// Platforms: where the Network Application runs, which decides the login
// and the API's path.
const (
	PlatformUniFiOS    = "unifi_os"   // a console (UDM, UCG, UDR, Cloud Key Gen2+, UNVR, self-hosted UniFi OS Server)
	PlatformStandalone = "standalone" // the Network Application installed on its own (Docker, a VM), usually port 8443
)

// Codes of repairs this integration raises besides the framework's.
const CodeMissingPermission = "missing_permission"

// versionEvery is how often Sync reads the version again.
const versionEvery = time.Hour

func init() { integration.Register(Integration{}) }

// Integration is the UniFi integration.
type Integration struct{}

// Manifest implements integration.Integration.
func (Integration) Manifest() integration.Manifest {
	platform := integration.Enum("Platform", PlatformUniFiOS, PlatformStandalone).
		Titled("UniFi OS console", "Standalone Network Application").
		WithDefault(PlatformUniFiOS).
		Describe("A UniFi OS console (Dream Machine, Cloud Gateway, Cloud Key Gen2 and later, UniFi OS Server) or the Network Application installed on its own.")
	site := integration.String("Site").WithDefault("default").
		Describe("The site's short name: default for the first site, or the code in the site's address (/manage/<site>/…).")
	site.Pattern, site.MaxLength = `^[A-Za-z0-9_-]+$`, new(64)
	user := integration.String("Username").Describe("A local account with the View Only role in UniFi Network.")
	user.MaxLength = new(128)
	return integration.Manifest{
		ID: ID, Name: "UniFi Network", Icon: "unifi", Version: "0.1.0", Tier: integration.Silver,
		Category:     integration.CatNetwork,
		Capabilities: []integration.Capability{"net.devices", "net.clients", "net.wan"},
		ConfigSchema: integration.Object(
			integration.Prop("url", integration.URL("URL").Describe("The console's address, https://10.0.0.1 for a console or https://host:8443 for a standalone controller.")),
			integration.TLSProp(),
			integration.Prop("platform", platform),
			integration.Prop("site", site),
			integration.Prop("username", user),
			integration.Prop("password", integration.Secret("Password")),
		).Require("url", "username", "password"),
		Poll: integration.Poll{Interval: 30 * time.Second, Idle: time.Minute, Timeout: 20 * time.Second},
	}
}

// ValidateConfig implements integration.ConfigValidator.
func (Integration) ValidateConfig(cfg integration.Config) []integration.FieldError {
	if _, err := baseOf(cfg.String("url")); err != nil {
		return []integration.FieldError{{Path: "/url", Message: err.Error()}}
	}
	return nil
}

func baseOf(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return nil, errors.New("must be the console's https:// address, without a user name")
	}
	u.RawQuery, u.Fragment = "", ""
	u.Path = strings.TrimRight(u.Path, "/")
	return u, nil
}

// Connect implements integration.Integration. It does no I/O.
func (Integration) Connect(_ context.Context, cfg integration.Config, d integration.Deps) (integration.Instance, error) {
	base, err := baseOf(cfg.String("url"))
	if err != nil {
		return nil, &integration.ConfigError{Field: "url", Message: "url " + err.Error()}
	}
	platform := cfg.String("platform")
	if platform == "" {
		platform = PlatformUniFiOS
	}
	site := cfg.String("site")
	if site == "" {
		site = "default"
	}
	key := cfg.InstanceID
	if key == "" {
		key = "test"
	}
	in := &instance{
		d: d, name: cfg.Name, prefix: ID + ":" + key + "/", base: base.String(),
		platform: platform, site: site,
	}
	if in.name == "" {
		in.name = "UniFi"
	}
	if platform == PlatformUniFiOS {
		in.api = in.base + "/proxy/network"
	} else {
		in.api = in.base
	}
	s := &session{base: in.base, platform: platform, user: cfg.String("username"), pass: cfg.String("password")}
	c, err := d.NewHTTPClient(integration.HTTPOptions{Session: s})
	if err != nil {
		return nil, err
	}
	in.http = c
	return in, nil
}

// session is the cookie login. UniFi OS: POST /api/auth/login with JSON
// {username, password}, answered with a TOKEN cookie and an X-CSRF-Token
// header. The standalone controller: POST /api/login, answered with
// unifises and csrf_token cookies. Reads need only the cookies; the CSRF
// token is kept for the logout. An expired session answers 401, so the
// framework signs in again and retries once.
type session struct {
	base, platform string
	user, pass     string

	mu      sync.Mutex
	cookies []*http.Cookie
	csrf    string
}

func (s *session) loginPath() string {
	if s.platform == PlatformUniFiOS {
		return "/api/auth/login"
	}
	return "/api/login"
}

func (s *session) other() string {
	if s.platform == PlatformUniFiOS {
		return "Standalone Network Application"
	}
	return "UniFi OS console"
}

// Login implements integration.Session. The password goes only into the
// request body; no error repeats the body.
func (s *session) Login(ctx context.Context, c *http.Client) error {
	body := map[string]any{"username": s.user, "password": s.pass}
	if s.platform == PlatformUniFiOS {
		body["rememberMe"] = true
	} else {
		body["remember"] = true
	}
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	path := s.loginPath()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.base+path, bytes.NewReader(b))
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
	answer, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	switch resp.StatusCode {
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden:
		return &integration.AuthError{Field: "password", Message: "UniFi rejected the username or password"}
	case 499: // UniFi OS: the account asks for a second factor
		return &integration.AuthError{Field: "username",
			Message: "UniFi asks this account for two-factor authentication; use a local account without it (see the recipe)"}
	case http.StatusNotFound, http.StatusMethodNotAllowed:
		return &integration.RepairError{Code: integration.CodeAPIChanged, Field: "platform",
			Message: fmt.Sprintf("%s%s answered %d: this isn't a UniFi login there. If it's a %s, change Platform, and check the URL", s.base, path, resp.StatusCode, s.other())}
	}
	if err := integration.CheckResponse(resp); err != nil {
		return err
	}
	if s.platform == PlatformStandalone {
		var env envelope
		if json.Unmarshal(answer, &env) == nil && env.Meta.RC != "" && env.Meta.RC != "ok" {
			return &integration.AuthError{Field: "password", Message: "UniFi rejected the username or password"}
		}
	}
	var kept []*http.Cookie
	csrf := resp.Header.Get("X-Csrf-Token")
	for _, ck := range resp.Cookies() {
		if ck.Value == "" {
			continue
		}
		kept = append(kept, &http.Cookie{Name: ck.Name, Value: ck.Value})
		if ck.Name == "csrf_token" && csrf == "" {
			csrf = ck.Value
		}
	}
	if len(kept) == 0 {
		return &integration.RepairError{Code: integration.CodeAPIChanged, Field: "platform",
			Message: fmt.Sprintf("%s%s accepted the sign-in but set no session cookie; check Platform and the URL", s.base, path)}
	}
	s.mu.Lock()
	s.cookies, s.csrf = kept, csrf
	s.mu.Unlock()
	return nil
}

// Apply implements integration.Session.
func (s *session) Apply(r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ck := range s.cookies {
		r.AddCookie(ck)
	}
}

// Expired implements integration.SessionExpiry: only a 401 is an expired
// session. A 403 is an account without access, which signing in again
// doesn't change.
func (s *session) Expired(resp *http.Response) bool {
	return resp.StatusCode == http.StatusUnauthorized
}

// Logout implements integration.SessionLogout, so a hub restart doesn't
// leave sessions behind on the console.
func (s *session) Logout(ctx context.Context, c *http.Client) error {
	path := "/api/logout"
	if s.platform == PlatformUniFiOS {
		path = "/api/auth/logout"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.base+path, nil)
	if err != nil {
		return err
	}
	s.Apply(req)
	s.mu.Lock()
	if s.csrf != "" {
		req.Header.Set("X-Csrf-Token", s.csrf)
	}
	s.mu.Unlock()
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

type instance struct {
	d        integration.Deps
	name     string
	prefix   string // "unifi:<instance id>/"
	base     string // the console's URL
	api      string // the Network Application's API root: base, or base + /proxy/network
	platform string
	site     string
	http     *http.Client

	mu      sync.Mutex // Sync's state
	version string
	readVer time.Time
}

func (in *instance) id(path string) string { return in.prefix + path }

// Close implements integration.Instance; the framework logs the session out.
func (in *instance) Close() error { return nil }

// envelope is the classic API's answer: {"meta": {"rc": "ok"}, "data": […]}.
type envelope struct {
	Meta struct {
		RC  string `json:"rc"`
		Msg string `json:"msg"`
	} `json:"meta"`
	Data json.RawMessage `json:"data"`
}

// get reads one classic API path (under the API root) into dst, the
// envelope's data.
func (in *instance) get(ctx context.Context, path string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, in.api+path, nil)
	if err != nil {
		return &integration.ConfigError{Field: "url", Message: "the URL can't be used: " + err.Error()}
	}
	req.Header.Set("Accept", "application/json")
	resp, err := in.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, rerr := io.ReadAll(resp.Body)
	var env envelope
	decoded := rerr == nil && json.Unmarshal(b, &env) == nil
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusBadRequest:
		if decoded && env.Meta.Msg == "api.err.NoSiteContext" {
			return in.noSite()
		}
		return integration.CheckResponse(resp)
	case http.StatusUnauthorized:
		// Signed in again and still refused.
		return &integration.AuthError{Field: "password", Message: "UniFi signed the account in but refuses its session; check the account under UniFi Network's admins"}
	case http.StatusForbidden:
		return &integration.RepairError{Code: CodeMissingPermission, Field: "username",
			Message: fmt.Sprintf("The account can't read the site %q in UniFi Network. Give it the View Only role there (see the recipe); Cha Dash reconnects on its own.", in.site)}
	case http.StatusNotFound:
		return in.notUniFi(path, "404 Not Found")
	default:
		return integration.CheckResponse(resp)
	}
	if rerr != nil {
		return rerr
	}
	if !decoded {
		return in.notUniFi(path, "the answer isn't the JSON it expects")
	}
	if env.Meta.RC != "ok" {
		if env.Meta.Msg == "api.err.NoSiteContext" {
			return in.noSite()
		}
		return in.notUniFi(path, "meta.rc is "+fmt.Sprintf("%q", env.Meta.RC))
	}
	if err := json.Unmarshal(env.Data, dst); err != nil {
		return in.notUniFi(path, "its data isn't what it expects")
	}
	return nil
}

func (in *instance) noSite() error {
	return &integration.ConfigError{Field: "site", Message: fmt.Sprintf("UniFi Network has no site %q, or the account can't see it. Test connection lists the sites it can see.", in.site)}
}

func (in *instance) notUniFi(path, detail string) error {
	hint := ""
	if in.platform == PlatformStandalone {
		hint = " If it's a UniFi OS console, change Platform."
	}
	return &integration.RepairError{Code: integration.CodeAPIChanged, Field: "url",
		Message: fmt.Sprintf("%s%s doesn't answer like UniFi Network's API (%s).%s", in.api, path, detail, hint)}
}

func (in *instance) sitePath(stat string) string {
	return "/api/s/" + url.PathEscape(in.site) + "/" + stat
}

// Sync implements integration.Instance: the version (hourly), the devices
// and the site's health, published as one scope, then the WAN incident.
// Both reads are one small request each, so nobody watching changes
// nothing but the poll interval.
func (in *instance) Sync(ctx context.Context, _ integration.Interest) error {
	in.mu.Lock()
	defer in.mu.Unlock()
	now := in.d.Now()
	if in.version == "" || now.Sub(in.readVer) >= versionEvery {
		var sys []sysinfo
		if err := in.get(ctx, in.sitePath("stat/sysinfo"), &sys); err != nil {
			return err
		}
		if len(sys) > 0 {
			in.version, in.readVer = sys[0].Version, now
		}
	}
	var devices []device
	if err := in.get(ctx, in.sitePath("stat/device"), &devices); err != nil {
		return err
	}
	var health []subsystem
	if err := in.get(ctx, in.sitePath("stat/health"), &health); err != nil {
		return err
	}
	m := in.model(devices, health)
	if err := in.d.Emit.Replace("", m.facts()); err != nil {
		return err
	}
	if err := in.wanIncident(m); err != nil {
		return err
	}
	in.d.Emit.SetVersion(in.version)
	return nil
}

// sysinfo is one entry of stat/sysinfo.
type sysinfo struct {
	Version    string `json:"version"`
	UDMVersion string `json:"udm_version"`
}

// siteInfo is one entry of api/self/sites: the sites the account sees and
// its role on each.
type siteInfo struct {
	Name string `json:"name"`
	Desc string `json:"desc"`
	Role string `json:"role"`
}

// Test implements integration.Instance: sign in, find the site and the
// account's role on it, and read the version.
func (in *instance) Test(ctx context.Context) integration.TestResult {
	var sites []siteInfo
	if err := in.get(ctx, "/api/self/sites", &sites); err != nil {
		return integration.Failed(err)
	}
	var res integration.TestResult
	var mine *siteInfo
	var names []string
	for i := range sites {
		names = append(names, sites[i].Name)
		if sites[i].Name == in.site {
			mine = &sites[i]
		}
	}
	if mine == nil {
		res.ErrorCode = integration.CodeInvalidConfig
		res.ErrorMessage = fmt.Sprintf("UniFi Network has no site %q this account can see; it sees %s.", in.site, strings.Join(names, ", "))
		return res
	}
	res.Grants = []integration.Grant{grant(*mine)}
	var sys []sysinfo
	if err := in.get(ctx, in.sitePath("stat/sysinfo"), &sys); err != nil {
		failed := integration.Failed(err)
		failed.Grants = res.Grants
		return failed
	}
	if len(sys) > 0 {
		res.Version = sys[0].Version
	}
	res.OK = true
	return res
}

// grant is the account's role on the site, as UniFi Network names it.
func grant(s siteInfo) integration.Grant {
	where := "site " + s.Name
	if s.Desc != "" {
		where = "site " + s.Desc + " (" + s.Name + ")"
	}
	switch s.Role {
	case "readonly":
		return integration.Grant{Name: "UniFi Network: View Only", Access: integration.AccessRead, Detail: where}
	case "admin":
		return integration.Grant{Name: "UniFi Network: Admin", Access: integration.AccessAdmin,
			Detail: where + ": can change the network, restart and upgrade devices"}
	case "":
		return integration.Grant{Name: "UniFi Network", Access: integration.AccessRead, Detail: where + "; UniFi didn't say the account's role"}
	}
	// Site Admin, Hotspot Operator and custom roles can all change something.
	return integration.Grant{Name: "UniFi Network: " + s.Role, Access: integration.AccessWrite, Write: true, Detail: where}
}
