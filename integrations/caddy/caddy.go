// Package caddy reads Caddy's admin API: each reverse_proxy in its HTTP
// servers' routes becomes a route resource with an open URL and its
// upstream's address, in the shape Traefik's routes have, so the correlator
// joins them to containers and apps the same way. Caddy's admin API has no
// authentication, so reaching it at all raises a security finding. See
// README.md.
package caddy

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"dash.cha.sh/internal/integration"
)

// ID is the integration's ID.
const ID = "caddy"

// serversPath is where the admin API lists the HTTP servers' config.
const serversPath = "/config/apps/http/servers"

// hintAdmin is the local ID of the open admin API's finding.
const hintAdmin = "hint.admin_open"

// maxRoutes bounds the routes published from one Caddy.
const maxRoutes = 500

func init() { integration.Register(Integration{}) }

// Integration is the Caddy integration.
type Integration struct{}

// Manifest implements integration.Integration.
func (Integration) Manifest() integration.Manifest {
	return integration.Manifest{
		ID: ID, Name: "Caddy", Icon: "caddy", Version: "0.1.0", Tier: integration.Bronze,
		Category:     integration.CatNetwork,
		Capabilities: []integration.Capability{"proxy.routes"},
		ConfigSchema: integration.Object(
			integration.Prop("url", integration.URL("Admin API URL").Describe("Where Caddy's admin API answers, e.g. http://10.0.0.5:2019. By default it answers only on localhost: see the recipe.")),
			integration.TLSProp(),
		).Require("url"),
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
	in := &instance{d: d, base: strings.TrimSuffix(u.String(), "/"), host: u.Host, key: cfg.InstanceID, name: cfg.Name}
	if in.key == "" {
		in.key = "test"
	}
	if in.name == "" {
		in.name = "Caddy"
	}
	return in, nil
}

type instance struct {
	d    integration.Deps
	base string // the admin API's URL
	host string
	key  string // the instance ID in resource IDs
	name string
}

func (in *instance) rid(path string) string { return ID + ":" + in.key + "/" + path }

// Close implements integration.Instance.
func (in *instance) Close() error {
	in.d.HTTP.CloseIdleConnections()
	return nil
}

// servers reads the HTTP servers' config. Caddy answers null when the
// http app isn't configured.
func (in *instance) servers(ctx context.Context) (map[string]server, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, in.base+serversPath, nil)
	if err != nil {
		return nil, &integration.ConfigError{Field: "url", Message: "url: " + err.Error()}
	}
	resp, err := in.d.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, &integration.RepairError{Code: integration.CodeAPIChanged, Field: "url",
			Message: fmt.Sprintf("Caddy's admin API refused the request (%s). It has no credentials to give it; check that its origins allow this address", resp.Status)}
	case http.StatusNotFound:
		return nil, &integration.RepairError{Code: integration.CodeAPIChanged, Field: "url",
			Message: "GET " + serversPath + " answered 404: this isn't Caddy's admin API (it listens on port 2019 by default)"}
	}
	if err := integration.CheckResponse(resp); err != nil {
		return nil, err
	}
	var out map[string]server
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, &integration.RepairError{Code: integration.CodeAPIChanged, Field: "url",
			Message: "GET " + serversPath + " didn't answer with Caddy's config: " + err.Error()}
	}
	return out, nil
}

// Test implements integration.Instance: the servers' config reads, and the
// API needed no credentials, which is always so.
func (in *instance) Test(ctx context.Context) integration.TestResult {
	start := time.Now()
	if _, err := in.servers(ctx); err != nil {
		return integration.Failed(err)
	}
	return integration.TestResult{OK: true, Latency: time.Since(start),
		Grants: []integration.Grant{{Name: "admin API", Access: integration.AccessAdmin,
			Detail: "Caddy's admin API has no authentication and can change the whole config; Cha Dash only reads it"}},
		Warnings: []integration.Warning{{Code: "admin_open", Severity: integration.WarnWarning,
			Message: "Caddy's admin API answers without credentials: anyone who can reach it can rewrite Caddy's config. Expose it on a private address only."}}}
}

// Sync implements integration.Instance: read the servers, publish the
// routes and the app, and the admin API's finding.
func (in *instance) Sync(ctx context.Context, _ integration.Interest) error {
	srvs, err := in.servers(ctx)
	if err != nil {
		return err
	}
	if err := in.d.Emit.Replace("", in.facts(srvs)); err != nil {
		return err
	}
	if err := in.d.Emit.Raise(integration.Incident{ID: hintAdmin, Scenario: "credential-hygiene", Class: integration.ClassProblem,
		Severity: integration.SeverityAttention, Category: integration.CategorySecurity,
		Title: in.name + "'s admin API is open",
		Summary: fmt.Sprintf("Caddy's admin API answers without credentials at %s: anyone who can reach it can read and replace Caddy's whole config. "+
			"Listen on a private address only, or on localhost and read it through a tunnel.", in.host),
		Target: in.rid("app"), ResolvesWhen: "Resolves when Cha Dash no longer reads this Caddy.",
		Facts: map[string]any{"url": in.base}}); err != nil {
		return err
	}
	return nil
}

// server is one HTTP server of Caddy's config: what it listens on and its
// routes. Handlers' other settings (headers, auth, TLS keys) aren't kept.
type server struct {
	Listen []string `json:"listen"`
	Routes []route  `json:"routes"`
}

type route struct {
	Match  []matcher `json:"match"`
	Handle []handler `json:"handle"`
}

type matcher struct {
	Host []string `json:"host"`
	Path []string `json:"path"`
}

type handler struct {
	Handler   string     `json:"handler"`
	Routes    []route    `json:"routes"` // subroute
	Upstreams []upstream `json:"upstreams"`
	Transport struct {
		TLS *json.RawMessage `json:"tls"`
	} `json:"transport"`
}

type upstream struct {
	Dial string `json:"dial"`
}

// proxy is a reverse_proxy found in the config, with the hosts and path its
// enclosing routes match.
type proxy struct {
	server   string
	scheme   string // the server's: https when it listens on 443
	port     int    // the server's port when it isn't the scheme's
	hosts    []string
	path     string
	dial     string
	upTLS    bool
	position string // where in the config: srv0/2/0
}

// proxies walks a server's routes, subroutes included, and returns every
// reverse_proxy with the match it's under. A route's own match narrows its
// parent's: hosts replace, a path replaces.
func proxies(name string, s server) []proxy {
	// The open URL's scheme and port: https on a server listening on 443,
	// plain http on 80, else http on its first port.
	var ports []int
	for _, l := range s.Listen {
		if _, p, err := net.SplitHostPort(l[strings.LastIndexByte(l, '/')+1:]); err == nil {
			if n, err := strconv.Atoi(p); err == nil {
				ports = append(ports, n)
			}
		}
	}
	scheme, port := "http", 0
	switch {
	case slices.Contains(ports, 443):
		scheme = "https"
	case slices.Contains(ports, 80):
	case len(ports) > 0:
		port = ports[0]
	}
	var out []proxy
	var walk func(rs []route, hosts []string, path, at string)
	walk = func(rs []route, hosts []string, path, at string) {
		for i, r := range rs {
			h, p := hosts, path
			for _, m := range r.Match {
				if len(m.Host) > 0 {
					h = m.Host
				}
				if len(m.Path) > 0 {
					p = m.Path[0]
				}
			}
			pos := at + "/" + strconv.Itoa(i)
			for j, hd := range r.Handle {
				switch hd.Handler {
				case "subroute":
					walk(hd.Routes, h, p, pos+"/"+strconv.Itoa(j))
				case "reverse_proxy":
					for _, u := range hd.Upstreams {
						out = append(out, proxy{server: name, scheme: scheme, port: port, hosts: h, path: p, dial: u.Dial,
							upTLS: hd.Transport.TLS != nil, position: pos})
					}
				}
			}
		}
	}
	walk(s.Routes, nil, "", name)
	return out
}

// facts are the app and a route per reverse_proxy, each part of the app.
func (in *instance) facts(srvs map[string]server) integration.Facts {
	self := in.d.Emit.Self()
	app := in.rid("app")
	f := integration.Facts{Relations: []integration.Relation{{From: self, Type: "monitors", To: app}}}
	ids := map[string]int{}
	n := 0
	for _, name := range sortedKeys(srvs) {
		for _, p := range proxies(name, srvs[name]) {
			if n == maxRoutes {
				break
			}
			res := in.route(p, ids)
			f.Resources = append(f.Resources, res)
			f.Relations = append(f.Relations, integration.Relation{From: res.ID, Type: "part_of", To: app})
			n++
		}
	}
	f.Resources = append(f.Resources, integration.Resource{ID: app, Kind: "app", Name: in.name, State: "running",
		Health: integration.Health{Status: integration.HealthOK},
		Attrs:  map[string]any{"type": ID, "servers": len(srvs), "routes": n, "poll_url": in.base}})
	return f
}

// route is one reverse_proxy as a route resource: named and keyed by its
// first host and path, its open URL from the server's scheme, and its
// upstream's address.
func (in *instance) route(p proxy, ids map[string]int) integration.Resource {
	path := strings.TrimSuffix(p.path, "*")
	path = strings.TrimSuffix(path, "/")
	label := p.server
	if len(p.hosts) > 0 {
		label = p.hosts[0]
	}
	label += path
	key := slug(label)
	if ids[key]++; ids[key] > 1 {
		key += "-" + strconv.Itoa(ids[key])
	}
	rule := ruleOf(p.hosts, p.path)
	attrs := map[string]any{"rule": rule, "entrypoint": p.server, "position": p.position, "tls": p.scheme == "https"}
	if len(p.hosts) > 0 {
		attrs["hosts"] = p.hosts
	}
	if p.path != "" {
		attrs["path"] = p.path
	}
	res := integration.Resource{ID: in.rid("route/" + key), Kind: "route", Name: label, State: "enabled",
		Health: integration.Health{Status: integration.HealthOK}, Attrs: attrs}
	if len(p.hosts) > 0 && !strings.ContainsAny(p.hosts[0], "*{") {
		u := p.scheme + "://" + p.hosts[0]
		if p.port > 0 {
			u += ":" + strconv.Itoa(p.port)
		}
		u += path
		attrs["url"] = u
		res.Links = []integration.Link{{Title: label, URL: u}}
	}
	if h, port, err := net.SplitHostPort(p.dial); err == nil && h != "" && !strings.Contains(p.dial, "{") {
		scheme := "http"
		if p.upTLS {
			scheme = "https"
		}
		attrs["upstream_addr"] = net.JoinHostPort(h, port)
		attrs["upstream"] = scheme + "://" + net.JoinHostPort(h, port)
	} else if p.dial != "" {
		attrs["upstream"] = p.dial // a placeholder or a unix socket: shown, never joined
	}
	return res
}

// ruleOf writes a route's match the way Caddyfile users read it.
func ruleOf(hosts []string, path string) string {
	var parts []string
	if len(hosts) > 0 {
		parts = append(parts, "host "+strings.Join(hosts, " "))
	}
	if path != "" {
		parts = append(parts, "path "+path)
	}
	if len(parts) == 0 {
		return "all requests"
	}
	return strings.Join(parts, ", ")
}

func slug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-.")
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
