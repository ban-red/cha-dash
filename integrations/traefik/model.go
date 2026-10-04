package traefik

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"

	"dash.cha.sh/internal/integration"
)

// The API's shapes, reduced to what the facts use.

type version struct {
	Version  string `json:"Version"`
	Codename string `json:"Codename"`
}

type counts struct {
	Total    int `json:"total"`
	Warnings int `json:"warnings"`
	Errors   int `json:"errors"`
}

type overview struct {
	HTTP struct {
		Routers     counts `json:"routers"`
		Services    counts `json:"services"`
		Middlewares counts `json:"middlewares"`
	} `json:"http"`
	TCP struct {
		Routers counts `json:"routers"`
	} `json:"tcp"`
	UDP struct {
		Routers counts `json:"routers"`
	} `json:"udp"`
	Providers []string `json:"providers"`
}

type entrypoint struct {
	Name    string `json:"name"`
	Address string `json:"address"`
	HTTP    struct {
		TLS *struct{} `json:"tls"` // TLS for every router on it
	} `json:"http"`
}

type router struct {
	Name        string    `json:"name"`
	Provider    string    `json:"provider"`
	Rule        string    `json:"rule"`
	Service     string    `json:"service"`
	EntryPoints []string  `json:"entryPoints"`
	Using       []string  `json:"using"`
	Middlewares []string  `json:"middlewares"`
	Priority    int64     `json:"priority"`
	Status      string    `json:"status"`
	Errors      []string  `json:"error"`
	TLS         *struct{} `json:"tls"` // only its presence: certResolver and options stay behind
}

type server struct {
	URL          string `json:"url"`
	PreservePath bool   `json:"preservePath"` // v3.4+: the URL's path is prepended
}

type service struct {
	Name         string            `json:"name"`
	Provider     string            `json:"provider"`
	Status       string            `json:"status"`
	Type         string            `json:"type"`
	UsedBy       []string          `json:"usedBy"`
	Errors       []string          `json:"error"`
	ServerStatus map[string]string `json:"serverStatus"`
	LoadBalancer *struct {
		Servers     []server  `json:"servers"`
		HealthCheck *struct{} `json:"healthCheck"`
	} `json:"loadBalancer"`
	Weighted *struct {
		Services []struct {
			Name string `json:"name"`
		} `json:"services"`
	} `json:"weighted"`
	Mirroring *struct {
		Service string `json:"service"`
	} `json:"mirroring"`
	Failover *struct {
		Service string `json:"service"`
	} `json:"failover"`
}

// middleware keeps a middleware's name and kind, and the prefixes a strip
// removes (the backend URL needs them). Nothing else is decoded: basicAuth
// and digestAuth users (Traefik returns the password hashes), forwardAuth
// addresses, headers and plugin configs never leave the response.
type middleware struct {
	Name        string `json:"name"`
	Provider    string `json:"provider"`
	Type        string `json:"type"`
	StripPrefix *struct {
		Prefixes []string `json:"prefixes"`
	} `json:"stripPrefix"`
	StripPrefixRegex *struct{} `json:"stripPrefixRegex"`
	Chain            *struct {
		Middlewares []string `json:"middlewares"`
	} `json:"chain"`
}

// qualify names a router's service or middleware with the router's
// provider when it has none: "sonarr" from a file router is sonarr@file.
func qualify(name, provider string) string {
	if strings.Contains(name, "@") || provider == "" {
		return name
	}
	return name + "@" + provider
}

// local is a name without its provider: sonarr@file → sonarr.
func local(name string) string {
	if i := strings.LastIndexByte(name, '@'); i > 0 {
		return name[:i]
	}
	return name
}

// model is the API's lists, indexed.
type model struct {
	routers  []router
	services map[string]service
	mws      map[string]middleware
	eps      map[string]entrypoint
	svcNames []string
}

func newModel(rs []router, ss []service, ms []middleware, es []entrypoint) *model {
	m := &model{routers: rs, services: map[string]service{}, mws: map[string]middleware{}, eps: map[string]entrypoint{}}
	sort.Slice(m.routers, func(i, j int) bool { return m.routers[i].Name < m.routers[j].Name })
	for _, s := range ss {
		m.services[s.Name] = s
		m.svcNames = append(m.svcNames, s.Name)
	}
	slices.Sort(m.svcNames)
	for _, x := range ms {
		m.mws[x.Name] = x
	}
	for _, e := range es {
		m.eps[e.Name] = e
	}
	return m
}

// servers resolves a service to its load balancer's servers, following
// weighted, mirroring and failover services (the first branch) a few deep.
// An internal service (api@internal) has none.
func (m *model) servers(name string, depth int) []server {
	s, ok := m.services[name]
	if !ok || depth > 5 {
		return nil
	}
	switch {
	case s.LoadBalancer != nil:
		return s.LoadBalancer.Servers
	case s.Weighted != nil && len(s.Weighted.Services) > 0:
		return m.servers(qualify(s.Weighted.Services[0].Name, s.Provider), depth+1)
	case s.Mirroring != nil:
		return m.servers(qualify(s.Mirroring.Service, s.Provider), depth+1)
	case s.Failover != nil:
		return m.servers(qualify(s.Failover.Service, s.Provider), depth+1)
	}
	return nil
}

// middlewares resolves a router's middlewares, chains flattened.
func (m *model) middlewares(r router) []middleware {
	var out []middleware
	var walk func(names []string, provider string, depth int)
	walk = func(names []string, provider string, depth int) {
		for _, n := range names {
			mw, ok := m.mws[qualify(n, provider)]
			if !ok {
				out = append(out, middleware{Name: qualify(n, provider)})
				continue
			}
			if mw.Chain != nil && depth < 5 {
				walk(mw.Chain.Middlewares, mw.Provider, depth+1)
				continue
			}
			out = append(out, mw)
		}
	}
	walk(r.Middlewares, r.Provider, 0)
	return out
}

// authKind is the kind of auth middleware in front of a router: "basic",
// "digest", "forward", or "" for none.
func authKind(mws []middleware) string {
	for _, mw := range mws {
		switch mw.Type {
		case "basicauth":
			return "basic"
		case "digestauth":
			return "digest"
		case "forwardauth":
			return "forward"
		}
	}
	return ""
}

// Rule matchers, for Host("a") and Host(`a`) alike.
var (
	hostRE       = regexp.MustCompile(`\bHost\(([^)]*)\)`)
	hostRegexpRE = regexp.MustCompile(`\bHostRegexp\(`)
	pathPrefixRE = regexp.MustCompile("\\bPathPrefix\\(\\s*[`\"]([^`\"]*)[`\"]")
	pathRE       = regexp.MustCompile("\\bPath\\(\\s*[`\"]([^`\"]*)[`\"]")
	quotedRE     = regexp.MustCompile("[`\"]([^`\"]*)[`\"]")
)

// parsedRule is what a rule says about where a router answers.
type parsedRule struct {
	hosts      []string
	hostRegexp bool
	path       string // the PathPrefix or Path, unless it's "/"
}

func parseRule(rule string) parsedRule {
	var p parsedRule
	for _, m := range hostRE.FindAllStringSubmatch(rule, -1) {
		for _, q := range quotedRE.FindAllStringSubmatch(m[1], -1) {
			if q[1] != "" && !slices.Contains(p.hosts, q[1]) {
				p.hosts = append(p.hosts, q[1])
			}
		}
	}
	p.hostRegexp = hostRegexpRE.MatchString(rule)
	if m := pathPrefixRE.FindStringSubmatch(rule); m != nil {
		p.path = m[1]
	} else if m := pathRE.FindStringSubmatch(rule); m != nil {
		p.path = m[1]
	}
	if p.path == "/" {
		p.path = ""
	}
	return p
}

// entrypoint picks the entrypoint an open URL uses: the router's (or, when
// it lists none, the ones Traefik attached it to), preferring the scheme's
// default port.
func (m *model) entrypoint(r router, tls bool) (entrypoint, bool) {
	names := r.EntryPoints
	if len(names) == 0 {
		names = r.Using
	}
	want := "80"
	if tls {
		want = "443"
	}
	var first *entrypoint
	for _, n := range names {
		e, ok := m.eps[n]
		if !ok {
			continue
		}
		if port(e.Address) == want {
			return e, true
		}
		if first == nil {
			first = &e
		}
	}
	if first != nil {
		return *first, true
	}
	return entrypoint{}, false
}

// port is an entrypoint address's port: ":443" → 443, ":8080/tcp" → 8080.
func port(addr string) string {
	addr, _, _ = strings.Cut(addr, "/")
	_, p, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	return p
}

// tls reports whether a router answers TLS: its own tls, or its
// entrypoint's default.
func (m *model) tls(r router) bool {
	if r.TLS != nil {
		return true
	}
	names := r.EntryPoints
	if len(names) == 0 {
		names = r.Using
	}
	for _, n := range names {
		if e, ok := m.eps[n]; ok && e.HTTP.TLS != nil {
			return true
		}
	}
	return false
}

// openURL is where a person opens the route (spike 4.3): https when the
// router has TLS, the entrypoint's port when it isn't the scheme's default,
// the first Host(), then the path. HostRegexp-only rules have none.
func (m *model) openURL(r router, rule parsedRule) string {
	if len(rule.hosts) == 0 {
		return ""
	}
	tls := m.tls(r)
	scheme, def := "http", "80"
	if tls {
		scheme, def = "https", "443"
	}
	host := rule.hosts[0]
	if e, ok := m.entrypoint(r, tls); ok {
		if p := port(e.Address); p != "" && p != def {
			host = net.JoinHostPort(host, p)
		}
	}
	return scheme + "://" + host + rule.path
}

// pollURL is where an API client reaches the backend directly, past any
// auth middleware: the server's origin, plus the path the backend sees. That
// path is the route's prefix unless a strip middleware removes it, after
// the server URL's own path when Traefik keeps it (preservePath). Without
// preservePath Traefik ignores a server URL's path, so a backend URL such
// as http://10.0.11.120:7780/sonarr (an nginx-proxy hop) still gets
// /sonarr/… from the request.
func pollURL(s server, rule parsedRule, mws []middleware) string {
	u, err := url.Parse(s.URL)
	if err != nil || u.Host == "" {
		return ""
	}
	base := u.Scheme + "://" + u.Host
	if s.PreservePath {
		base += strings.TrimSuffix(u.Path, "/")
	}
	if rule.path != "" && !strips(mws, rule.path) {
		base += strings.TrimSuffix(rule.path, "/")
	}
	return base
}

// strips reports whether a router's middlewares strip its path prefix.
func strips(mws []middleware, prefix string) bool {
	for _, mw := range mws {
		if mw.StripPrefixRegex != nil {
			return true
		}
		if mw.StripPrefix != nil && slices.ContainsFunc(mw.StripPrefix.Prefixes, func(p string) bool {
			return p != "/" && strings.HasPrefix(prefix, strings.TrimSuffix(p, "/"))
		}) {
			return true
		}
	}
	return false
}

// addr is a server URL's host:port, the scheme's port when it has none:
// what the correlator matches against published ports.
func addr(raw string) (string, string) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", ""
	}
	p := u.Port()
	if p == "" {
		p = map[string]string{"https": "443", "http": "80", "h2c": "80"}[u.Scheme]
	}
	path := ""
	if u.Path != "" && u.Path != "/" {
		path = u.Path
	}
	return net.JoinHostPort(u.Hostname(), p), path
}

// serviceHealth reads a load balancer's server status, which only means
// something when the service has a health check: without one Traefik says
// UP for every server (spike 4.3: 9 of them had nothing behind).
func serviceHealth(s service) (integration.Health, bool) {
	if s.LoadBalancer == nil || s.LoadBalancer.HealthCheck == nil || len(s.ServerStatus) == 0 {
		return integration.Health{Status: integration.HealthOK}, false
	}
	down := 0
	for _, st := range s.ServerStatus {
		if !strings.EqualFold(st, "UP") {
			down++
		}
	}
	switch {
	case down == len(s.ServerStatus):
		return integration.Health{Status: integration.HealthDown, Reason: "every server fails Traefik's health check"}, true
	case down > 0:
		return integration.Health{Status: integration.HealthAttention, Reason: fmt.Sprintf("%d of %d servers fail Traefik's health check", down, len(s.ServerStatus))}, true
	}
	return integration.Health{Status: integration.HealthOK}, true
}

// statusHealth maps a router's or service's status.
func statusHealth(status string, errs []string) integration.Health {
	reason := ""
	if len(errs) > 0 {
		reason = errs[0]
	}
	switch status {
	case "warning":
		return integration.Health{Status: integration.HealthAttention, Reason: cmpStr(reason, "Traefik reports a warning")}
	case "disabled":
		return integration.Health{Status: integration.HealthDown, Reason: cmpStr(reason, "disabled by Traefik")}
	}
	return integration.Health{Status: integration.HealthOK}
}

func cmpStr(a, b string) string {
	if a == "" {
		return b
	}
	return a
}
