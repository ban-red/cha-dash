package traefik

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"dash.cha.sh/internal/integration"
)

// facts are the app, a resource per HTTP service and per HTTP router, and
// their relations.
func (in *instance) facts(v version, ov overview, m *model) integration.Facts {
	self := in.d.Emit.Self()
	app := in.rid("app")
	f := integration.Facts{Relations: []integration.Relation{{From: self, Type: "monitors", To: app}}}

	health := map[string]integration.Health{}
	for _, name := range m.svcNames {
		s := m.services[name]
		id := in.rid("service/" + name)
		attrs := map[string]any{"provider": s.Provider, "type": cmpStr(s.Type, s.Provider)}
		var urls []string
		for _, sv := range m.servers(name, 0) {
			urls = append(urls, sv.URL)
		}
		switch {
		case len(urls) > 0:
			attrs["servers"], attrs["upstream"] = urls, urls[0]
		case s.Provider == "internal":
			attrs["upstream"] = name
		}
		if len(s.UsedBy) > 0 {
			attrs["used_by"] = s.UsedBy
		}
		h := statusHealth(s.Status, s.Errors)
		if hc, checked := serviceHealth(s); checked {
			attrs["health_check"], attrs["server_status"] = true, s.ServerStatus
			if h.Status == integration.HealthOK {
				h = hc
			}
		}
		health[name] = h
		f.Resources = append(f.Resources, integration.Resource{ID: id, Kind: "service", Name: local(name),
			State: cmpStr(s.Status, "enabled"), Health: h, Attrs: attrs})
		f.Relations = append(f.Relations, integration.Relation{From: id, Type: "part_of", To: app})
	}

	for _, r := range m.routers {
		id := in.rid("route/" + r.Name)
		rule := parseRule(r.Rule)
		mws := m.middlewares(r)
		svc := qualify(r.Service, r.Provider)
		attrs := map[string]any{"rule": r.Rule, "router": r.Name, "provider": r.Provider, "priority": r.Priority,
			"tls": m.tls(r), "service": svc}
		eps := r.EntryPoints
		if len(eps) == 0 {
			eps = r.Using
		}
		if len(eps) > 0 {
			attrs["entrypoint"] = eps[0]
		}
		if len(eps) > 1 {
			attrs["entrypoints"] = eps
		}
		if len(rule.hosts) > 0 {
			attrs["hosts"] = rule.hosts
		}
		if rule.path != "" {
			attrs["path"] = rule.path
		}
		if rule.hostRegexp {
			attrs["host_regexp"] = true
		}
		if len(mws) > 0 {
			names := make([]string, len(mws))
			for i, mw := range mws {
				names[i] = mw.Name
			}
			attrs["middlewares"] = names
		}
		if a := authKind(mws); a != "" {
			attrs["auth"] = a
		}
		if servers := m.servers(svc, 0); len(servers) > 0 {
			attrs["upstream"] = servers[0].URL
			if a, p := addr(servers[0].URL); a != "" {
				attrs["upstream_addr"] = a
				if p != "" {
					attrs["upstream_path"] = p
				}
			}
			if u := pollURL(servers[0], rule, mws); u != "" {
				attrs["poll_url"] = u
			}
		} else if strings.HasSuffix(svc, "@internal") {
			attrs["upstream"] = svc
		}
		res := integration.Resource{ID: id, Kind: "route", Name: local(r.Name), State: cmpStr(r.Status, "enabled"),
			Health: statusHealth(r.Status, r.Errors), Attrs: attrs}
		if u := m.openURL(r, rule); u != "" {
			attrs["url"] = u
			res.Links = []integration.Link{{Title: local(r.Name), URL: u}}
		}
		if sh, ok := health[svc]; ok && res.Health.Status == integration.HealthOK && sh.Status != integration.HealthOK {
			res.Health = integration.Health{Status: integration.HealthAttention, Reason: "backend: " + sh.Reason}
		}
		f.Resources = append(f.Resources, res)
		f.Relations = append(f.Relations,
			integration.Relation{From: id, Type: "part_of", To: app},
			integration.Relation{From: id, Type: "routes_to", To: in.rid("service/" + svc)})
	}
	f.Resources = append(f.Resources, in.app(v, ov, m))
	return f
}

// app is the instance's service resource, the Traefik app the widget
// shows: the router, service and middleware counts, the dashboard as its
// open link and the API as its poll URL.
func (in *instance) app(v version, ov overview, m *model) integration.Resource {
	attrs := map[string]any{"type": ID, "routers": ov.HTTP.Routers.Total, "services": ov.HTTP.Services.Total,
		"middlewares": ov.HTTP.Middlewares.Total, "poll_url": in.base}
	if v.Version != "" {
		attrs["version"] = v.Version
	}
	if len(ov.Providers) > 0 {
		attrs["providers"] = ov.Providers
	}
	if n := ov.TCP.Routers.Total; n > 0 {
		attrs["tcp_routers"] = n
	}
	if n := ov.UDP.Routers.Total; n > 0 {
		attrs["udp_routers"] = n
	}
	h := integration.Health{Status: integration.HealthOK}
	if n := ov.HTTP.Routers.Errors + ov.HTTP.Services.Errors + ov.HTTP.Middlewares.Errors; n > 0 {
		h = integration.Health{Status: integration.HealthAttention,
			Reason: fmt.Sprintf("%d %s in Traefik's HTTP config %s errors", n, plural(n, "entry", "entries"), plural(n, "has", "have"))}
	}
	link := m.dashboardURL()
	if link == "" {
		link = in.base + "/dashboard/"
	}
	return integration.Resource{ID: in.rid("app"), Kind: "app", Name: in.name, State: "running", Desired: "running",
		Health: h, Attrs: attrs, Links: []integration.Link{{Title: in.name, URL: link}}, Caps: []string{"proxy.routes"}}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// dashboard is a router to Traefik's own API and dashboard.
func dashboard(r router) bool {
	svc := qualify(r.Service, r.Provider)
	return svc == "api@internal" || svc == "dashboard@internal"
}

// dashboardURL is the best open URL of a router to the dashboard: TLS
// first, then one without a path, then the higher priority. API-only
// routes (PathPrefix(`/api`)) don't count.
func (m *model) dashboardURL() string {
	type cand struct {
		url   string
		score int
		prio  int64
	}
	var cs []cand
	for _, r := range m.routers {
		rule := parseRule(r.Rule)
		u := m.openURL(r, rule)
		if !dashboard(r) || u == "" || strings.HasPrefix(rule.path, "/api") {
			continue
		}
		c := cand{url: u, prio: r.Priority}
		if m.tls(r) {
			c.score += 2
		}
		if rule.path == "" || rule.path == "/dashboard" {
			c.score++
		}
		cs = append(cs, c)
	}
	if len(cs) == 0 {
		return ""
	}
	sort.SliceStable(cs, func(i, j int) bool {
		if cs[i].score != cs[j].score {
			return cs[i].score > cs[j].score
		}
		return cs[i].prio > cs[j].prio
	})
	return cs[0].url
}

// unguardedDashboards are the hosts where a router serves the dashboard
// with no auth middleware in front.
func (m *model) unguardedDashboards() []string {
	var hosts []string
	for _, r := range m.routers {
		if !dashboard(r) || r.Status == "disabled" || authKind(m.middlewares(r)) != "" {
			continue
		}
		for _, h := range parseRule(r.Rule).hosts {
			if !slices.Contains(hosts, h) {
				hosts = append(hosts, h)
			}
		}
	}
	slices.Sort(hosts)
	return hosts
}

// Hints, by incident ID (local to the instance).
const (
	hintPlainHTTP = "hint.plain_http_credentials"
	hintAPI       = "hint.api_no_auth"
)

// hints raises the credential-hygiene hints that apply and resolves the
// ones that no longer do.
func (in *instance) hints(m *model) error {
	want := map[string]integration.Incident{}
	base := func(id, severity, title, summary, resolves string) integration.Incident {
		return integration.Incident{ID: id, Scenario: "credential-hygiene", Class: integration.ClassProblem,
			Severity: severity, Title: title, Summary: summary, Target: in.rid("app"), ResolvesWhen: resolves,
			Facts: map[string]any{"url": in.base}}
	}
	if in.u.Scheme == "http" && in.user != "" {
		want[hintPlainHTTP] = base(hintPlainHTTP, integration.SeverityAttention, in.name+" sends its credentials in clear text",
			fmt.Sprintf("%s sends its credentials over plain http:// to %s. Use https:// if it supports it.", in.name, in.u.Hostname()),
			"Resolves when the instance's URL uses https://.")
	}
	switch hosts := m.unguardedDashboards(); {
	case in.open:
		want[hintAPI] = base(hintAPI, integration.SeverityAttention, in.name+"'s API is open",
			fmt.Sprintf("Traefik's API answers without credentials at %s: anyone who can reach it can read every router, service and middleware, including basic-auth password hashes. Put it behind an auth middleware, or keep it off the network.", in.u.Host),
			"Resolves when the API asks for credentials.")
	case len(hosts) > 0:
		inc := base(hintAPI, integration.SeverityInfo, in.name+"'s dashboard has no auth",
			fmt.Sprintf("Traefik's dashboard is routed without an auth middleware on %s. Its API asked for credentials, so only the dashboard's page is open today, but one router change could expose the API, and with it basic-auth password hashes.", strings.Join(hosts, ", ")),
			"Resolves when every dashboard router has an auth middleware.")
		inc.Facts["hosts"] = hosts
		want[hintAPI] = inc
	}
	for _, id := range []string{hintPlainHTTP, hintAPI} {
		inc, ok := want[id]
		switch {
		case ok:
			if err := in.d.Emit.Raise(inc); err != nil {
				return err
			}
			in.raised[id] = true
		case in.raised[id]:
			if err := in.d.Emit.Resolve(id, "No longer applies."); err != nil {
				return err
			}
			delete(in.raised, id)
		}
	}
	return nil
}
