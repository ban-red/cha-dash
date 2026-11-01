// Package npm reads Nginx Proxy Manager: each proxy host (and each of its
// custom locations) becomes a route resource with an open URL and its
// forward address, in the shape Traefik's routes have, so the correlator
// joins them to containers and apps the same way. It signs in with a user's
// email and password for a token that lasts a day, and renews it before it
// lapses. See README.md.
package npm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"dash.cha.sh/internal/integration"
)

// ID is the integration's ID.
const ID = "npm"

// renewBefore is how long before a token lapses it's renewed.
const renewBefore = time.Hour

func init() { integration.Register(Integration{}) }

// Integration is the Nginx Proxy Manager integration.
type Integration struct{}

// Manifest implements integration.Integration.
func (Integration) Manifest() integration.Manifest {
	return integration.Manifest{
		ID: ID, Name: "Nginx Proxy Manager", Icon: "nginx-proxy-manager", Version: "0.1.0", Tier: integration.Bronze,
		Category:     integration.CatNetwork,
		Capabilities: []integration.Capability{"proxy.routes"},
		ConfigSchema: integration.Object(
			integration.Prop("url", integration.URL("URL").Describe("Where Nginx Proxy Manager's admin page answers, e.g. http://npm:81.")),
			integration.TLSProp(),
			integration.Prop("email", integration.String("Email").Describe("The sign-in email of a user that can view proxy hosts.")),
			integration.Prop("password", integration.Secret("Password")),
		).Require("url", "email", "password"),
		Poll: integration.Poll{Interval: 60 * time.Second, Idle: 5 * time.Minute, Timeout: 30 * time.Second},
	}
}

// ValidateConfig implements integration.ConfigValidator.
func (Integration) ValidateConfig(cfg integration.Config) []integration.FieldError {
	if u, err := url.Parse(cfg.String("url")); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return []integration.FieldError{{Path: "/url", Message: "must start with http:// or https://"}}
	}
	return nil
}

// Connect implements integration.Integration. It does no I/O.
func (Integration) Connect(_ context.Context, cfg integration.Config, d integration.Deps) (integration.Instance, error) {
	u, err := url.Parse(cfg.String("url"))
	if err != nil {
		return nil, &integration.ConfigError{Field: "url", Message: "url: " + err.Error()}
	}
	in := &instance{d: d, base: strings.TrimSuffix(u.String(), "/"), key: cfg.InstanceID, name: cfg.Name,
		email: cfg.String("email"), password: cfg.String("password")}
	if in.key == "" {
		in.key = "test"
	}
	if in.name == "" {
		in.name = "Nginx Proxy Manager"
	}
	return in, nil
}

type instance struct {
	d               integration.Deps
	base            string
	key             string
	name            string
	email, password string

	mu      sync.Mutex
	token   string
	expires time.Time
}

func (in *instance) rid(path string) string { return ID + ":" + in.key + "/" + path }

// Close implements integration.Instance.
func (in *instance) Close() error {
	in.d.HTTP.CloseIdleConnections()
	return nil
}

// signIn gets a token for the user: POST /api/tokens.
func (in *instance) signIn(ctx context.Context) error {
	body, _ := json.Marshal(map[string]string{"identity": in.email, "secret": in.password})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, in.base+"/api/tokens", bytes.NewReader(body))
	if err != nil {
		return &integration.ConfigError{Field: "url", Message: "url: " + err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := in.d.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden:
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return &integration.AuthError{Field: "password", Message: "Nginx Proxy Manager refused the email and password"}
	case http.StatusNotFound:
		return &integration.RepairError{Code: integration.CodeAPIChanged, Field: "url",
			Message: "POST /api/tokens answered 404: this isn't Nginx Proxy Manager's admin port (81 by default)"}
	}
	if err := integration.CheckResponse(resp); err != nil {
		return err
	}
	var t struct {
		Token   string    `json:"token"`
		Expires time.Time `json:"expires"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&t); err != nil || t.Token == "" {
		return &integration.RepairError{Code: integration.CodeAPIChanged, Field: "url", Message: "POST /api/tokens didn't answer with a token"}
	}
	in.token, in.expires = t.Token, t.Expires
	if in.expires.IsZero() {
		in.expires = in.d.Now().Add(24 * time.Hour)
	}
	return nil
}

// errExpired is a 401 with a token: it lapsed or was revoked.
var errExpired = errors.New("npm: token refused")

// get GETs an API path with the token, signing in first when there's none
// or it lapses within renewBefore, and once more when it's refused.
func (in *instance) get(ctx context.Context, path string, dst any) error {
	in.mu.Lock()
	defer in.mu.Unlock()
	for attempt := 0; ; attempt++ {
		if in.token == "" || !in.d.Now().Before(in.expires.Add(-renewBefore)) {
			if err := in.signIn(ctx); err != nil {
				return err
			}
		}
		err := in.getWith(ctx, path, in.token, dst)
		if errors.Is(err, errExpired) && attempt == 0 {
			in.token = ""
			continue
		}
		if errors.Is(err, errExpired) {
			return &integration.AuthError{Field: "password", Message: "Nginx Proxy Manager refused a fresh token"}
		}
		return err
	}
}

func (in *instance) getWith(ctx context.Context, path, token string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, in.base+path, nil)
	if err != nil {
		return &integration.ConfigError{Field: "url", Message: "url: " + err.Error()}
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := in.d.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return errExpired
	case http.StatusForbidden:
		return &integration.AuthError{Field: "email", Message: "this user can't view proxy hosts: give it the View permission on them"}
	case http.StatusNotFound:
		return &integration.RepairError{Code: integration.CodeAPIChanged, Field: "url", Message: "GET " + path + " answered 404"}
	}
	if err := integration.CheckResponse(resp); err != nil {
		return err
	}
	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		return &integration.RepairError{Code: integration.CodeAPIChanged, Field: "url", Message: "GET " + path + " didn't answer with JSON: " + err.Error()}
	}
	return nil
}

// status is GET /api/, which answers without a token.
type status struct {
	Status  string `json:"status"`
	Version struct {
		Major    int `json:"major"`
		Minor    int `json:"minor"`
		Revision int `json:"revision"`
	} `json:"version"`
}

func (s status) version() string {
	if s.Version.Major == 0 && s.Version.Minor == 0 {
		return ""
	}
	return fmt.Sprintf("%d.%d.%d", s.Version.Major, s.Version.Minor, s.Version.Revision)
}

// proxyHost is the part of a proxy host the integration reads: where it
// answers and where it forwards. Access lists, certificates' contents and
// advanced nginx config aren't kept.
type proxyHost struct {
	ID            int      `json:"id"`
	DomainNames   []string `json:"domain_names"`
	ForwardScheme string   `json:"forward_scheme"`
	ForwardHost   string   `json:"forward_host"`
	ForwardPort   int      `json:"forward_port"`
	CertificateID any      `json:"certificate_id"` // 0, or an ID, or "new"
	SSLForced     flag     `json:"ssl_forced"`
	Enabled       flag     `json:"enabled"`
	AccessListID  int      `json:"access_list_id"`
	Locations     []struct {
		Path          string `json:"path"`
		ForwardScheme string `json:"forward_scheme"`
		ForwardHost   string `json:"forward_host"`
		ForwardPort   int    `json:"forward_port"`
	} `json:"locations"`
}

// flag is NPM's boolean, sent as true/false or 1/0 by version.
type flag bool

func (f *flag) UnmarshalJSON(b []byte) error {
	s := string(b)
	*f = flag(s == "true" || s == "1")
	return nil
}

// Test implements integration.Instance: the version, a token, and the
// proxy hosts the user can view.
func (in *instance) Test(ctx context.Context) integration.TestResult {
	start := time.Now()
	var st status
	if err := in.getWith(ctx, "/api/", "", &st); err != nil {
		return integration.Failed(err)
	}
	var hosts []proxyHost
	if err := in.get(ctx, "/api/nginx/proxy-hosts", &hosts); err != nil {
		return integration.Failed(err)
	}
	res := integration.TestResult{OK: true, Version: st.version(), Latency: time.Since(start),
		Grants: []integration.Grant{{Name: "proxy hosts", Access: integration.AccessRead, Detail: "Reads the proxy hosts the user can view"}}}
	if strings.HasPrefix(in.base, "http://") {
		res.Warnings = append(res.Warnings, integration.Warning{Code: "plain_http", Severity: integration.WarnWarning,
			Message: "The password crosses the network in clear text over http://: use https:// if Nginx Proxy Manager's admin page has it."})
	}
	return res
}

// Sync implements integration.Instance.
func (in *instance) Sync(ctx context.Context, _ integration.Interest) error {
	var st status
	if err := in.getWith(ctx, "/api/", "", &st); err != nil {
		return err
	}
	in.d.Emit.SetVersion(st.version())
	var hosts []proxyHost
	if err := in.get(ctx, "/api/nginx/proxy-hosts", &hosts); err != nil {
		return err
	}
	return in.d.Emit.Replace("", in.facts(st, hosts))
}

// facts are the app and a route per proxy host and custom location.
func (in *instance) facts(st status, hosts []proxyHost) integration.Facts {
	self := in.d.Emit.Self()
	app := in.rid("app")
	f := integration.Facts{Relations: []integration.Relation{{From: self, Type: "monitors", To: app}}}
	add := func(key, name string, h proxyHost, path, scheme, host string, port int) {
		state := "enabled"
		if !h.Enabled {
			state = "disabled"
		}
		tls := bool(h.SSLForced) || certified(h.CertificateID)
		attrs := map[string]any{"rule": "host " + strings.Join(h.DomainNames, " "), "proxy_host": h.ID, "tls": tls, "entrypoint": "proxy host"}
		if len(h.DomainNames) > 0 {
			attrs["hosts"] = h.DomainNames
		}
		if path != "" && path != "/" {
			attrs["path"] = path
			attrs["rule"] = attrs["rule"].(string) + ", path " + path
		}
		if h.AccessListID > 0 {
			attrs["auth"] = "access list"
		}
		res := integration.Resource{ID: in.rid("route/" + key), Kind: "route", Name: name, State: state,
			Health: integration.Health{Status: integration.HealthOK}, Attrs: attrs}
		if len(h.DomainNames) > 0 && !strings.Contains(h.DomainNames[0], "*") {
			s := "http"
			if tls {
				s = "https"
			}
			u := s + "://" + h.DomainNames[0] + strings.TrimSuffix(path, "/")
			attrs["url"] = u
			res.Links = []integration.Link{{Title: name, URL: u}}
		}
		if host != "" && port > 0 {
			addr := net.JoinHostPort(host, strconv.Itoa(port))
			attrs["upstream_addr"] = addr
			attrs["upstream"] = cmpStr(scheme, "http") + "://" + addr
		}
		f.Resources = append(f.Resources, res)
		f.Relations = append(f.Relations, integration.Relation{From: res.ID, Type: "part_of", To: app})
	}
	for _, h := range hosts {
		name := "proxy host " + strconv.Itoa(h.ID)
		if len(h.DomainNames) > 0 {
			name = h.DomainNames[0]
		}
		add(strconv.Itoa(h.ID), name, h, "", h.ForwardScheme, h.ForwardHost, h.ForwardPort)
		for i, l := range h.Locations {
			add(strconv.Itoa(h.ID)+"-"+strconv.Itoa(i+1), name+strings.TrimSuffix(l.Path, "/"), h, l.Path, l.ForwardScheme, l.ForwardHost, l.ForwardPort)
		}
	}
	attrs := map[string]any{"type": ID, "proxy_hosts": len(hosts)}
	if v := st.version(); v != "" {
		attrs["version"] = v
	}
	f.Resources = append(f.Resources, integration.Resource{ID: app, Kind: "app", Name: in.name, State: "running",
		Health: integration.Health{Status: integration.HealthOK}, Attrs: attrs,
		Links: []integration.Link{{Title: in.name, URL: in.base}}})
	return f
}

// certified reports whether a proxy host has a certificate: an ID above 0.
func certified(v any) bool {
	switch c := v.(type) {
	case float64:
		return c > 0
	case string:
		return c != "" && c != "0"
	}
	return false
}

func cmpStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
