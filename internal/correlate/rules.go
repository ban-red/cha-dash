package correlate

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"dash.cha.sh/internal/graph"
)

// identities merges what several sources report as one thing: same_as.
// Hosts and containers go first, since guests are keyed by their node and
// engines by their containers.
func (w *world) identities() {
	var first []cand
	first = append(first, w.sameHosts()...)
	first = append(first, w.sameDisks()...)
	first = append(first, w.sameContainers()...)
	first = append(first, w.sameLabels()...)
	first = append(first, w.sameApps()...)
	first = append(first, w.sameChecks()...)
	for k, d := range w.dec {
		if k.typ == SameAs && d.Verdict == Confirm && w.res[d.From] != nil && w.res[d.To] != nil {
			first = append(first, cand{Link: Link{From: d.From, Type: SameAs, To: d.To, Rule: cmp.Or(d.Rule, "user"),
				Confidence: Confirmed, Evidence: "You confirmed this."}})
		}
	}
	w.unite(first)
	w.unite(append(w.sameGuests(), w.sameEngines()...))
	for root, ids := range w.uf.members {
		best := ids[0]
		for _, id := range ids[1:] {
			a, b := w.res[id], w.res[best]
			if a != nil && (b == nil || rank(a) < rank(b) || rank(a) == rank(b) && a.ID < b.ID) {
				best = id
			}
		}
		w.primary[root] = best
	}
	w.weakSameApps()
}

// unite applies same_as candidates, most certain first.
func (w *world) unite(cs []cand) {
	slices.SortFunc(cs, func(a, b cand) int {
		return cmp.Or(cmp.Compare(confidenceRank[a.Confidence], confidenceRank[b.Confidence]), cmp.Compare(a.Key(), b.Key()))
	})
	for _, c := range cs {
		if c.Confidence == Weak {
			continue
		}
		if d, ok := w.dec[keyOf(c.From, SameAs, c.To)]; ok && d.Verdict == Reject {
			continue
		}
		if w.uf.union(c.From, c.To) {
			for _, id := range []graph.RID{c.From, c.To} {
				if _, ok := w.joined[id]; !ok {
					w.joined[id] = c.Link
				}
			}
		}
	}
}

func same(a, b *graph.Resource, rule string, conf Confidence, evidence string) cand {
	return cand{Link: Link{From: a.ID, Type: SameAs, To: b.ID, Rule: rule, Confidence: conf, Evidence: evidence}}
}

// pairs links every resource in each group to the group's first one from
// another source.
func pairs(groups map[string][]*graph.Resource, rule string, conf Confidence, evidence func(key string) string) []cand {
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var out []cand
	for _, k := range keys {
		g := groups[k]
		for i := 1; i < len(g); i++ {
			for j := range i {
				if !sameSource(g[i], g[j]) {
					out = append(out, same(g[i], g[j], rule, conf, evidence(k)))
					break
				}
			}
		}
	}
	return out
}

// sameHosts: two hosts of one platform at one routable address (two
// Proxmox instances reading one cluster).
func (w *world) sameHosts() []cand {
	groups := map[string][]*graph.Resource{}
	for _, h := range w.kinds["host"] {
		if ip := str(h, "ip"); routable(ip) {
			k := str(h, "platform") + "|" + ip
			groups[k] = append(groups[k], h)
		}
	}
	return pairs(groups, "ip", Strong, func(k string) string {
		_, ip, _ := strings.Cut(k, "|")
		return "Both hosts have the address " + ip + "."
	})
}

func (w *world) sameDisks() []cand {
	groups := map[string][]*graph.Resource{}
	for _, d := range w.kinds["disk"] {
		if s := strings.TrimSpace(str(d, "serial")); s != "" {
			groups[s] = append(groups[s], d)
		}
	}
	return pairs(groups, "serial", Exact, func(k string) string { return "Both report disk serial " + k + "." })
}

// sameContainers matches Docker container IDs, a short (12-character) ID
// against a full one by prefix.
func (w *world) sameContainers() []cand {
	groups := map[string][]*graph.Resource{}
	full := map[graph.RID]string{}
	for _, c := range w.kinds["container"] {
		id := strings.ToLower(strings.TrimSpace(str(c, "container_id")))
		if len(id) < 12 || strings.Trim(id, "0123456789abcdef") != "" {
			continue
		}
		full[c.ID] = id
		groups[id[:12]] = append(groups[id[:12]], c)
	}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var out []cand
	for _, k := range keys {
		g := groups[k]
		for i := 1; i < len(g); i++ {
			for j := range i {
				a, b := full[g[i].ID], full[g[j].ID]
				if sameSource(g[i], g[j]) || !strings.HasPrefix(a, b) && !strings.HasPrefix(b, a) {
					continue
				}
				out = append(out, same(g[i], g[j], "container_id", Exact, "Both report container ID "+k+"."))
				break
			}
		}
	}
	return out
}

// sameLabels: resources of one kind that carry the same chadash.id label.
func (w *world) sameLabels() []cand {
	groups := map[string][]*graph.Resource{}
	for kind, rs := range w.kinds {
		for _, r := range rs {
			if v := strings.TrimSpace(r.Labels["chadash.id"]); v != "" {
				k := string(kind) + "|" + v
				groups[k] = append(groups[k], r)
			}
		}
	}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var out []cand
	for _, k := range keys {
		g := groups[k]
		_, v, _ := strings.Cut(k, "|")
		for i := 1; i < len(g); i++ {
			out = append(out, same(g[i], g[0], "chadash.id", Exact, "Both are labelled chadash.id="+v+"."))
		}
	}
	return out
}

// sameApps: two app instances of one type at one URL, or one Plex server.
func (w *world) sameApps() []cand {
	byURL := map[string][]*graph.Resource{}
	byMachine := map[string][]*graph.Resource{}
	for _, a := range w.kinds["app"] {
		t := str(a, "type")
		if t == "" {
			continue
		}
		if u, ok := parseURL(str(a, "url")); ok {
			k := t + "|" + u.key()
			byURL[k] = append(byURL[k], a)
		}
		if m := str(a, "machine_identifier"); m != "" {
			byMachine[t+"|"+m] = append(byMachine[t+"|"+m], a)
		}
	}
	out := pairs(byURL, "url", Strong, func(k string) string {
		_, u, _ := strings.Cut(k, "|")
		return "Both instances read " + u + "."
	})
	return append(out, pairs(byMachine, "machine_identifier", Exact, func(string) string { return "Both report the same server identifier." })...)
}

// sameChecks: two checks (built-in, Uptime Kuma) of one target.
func (w *world) sameChecks() []cand {
	groups := map[string][]*graph.Resource{}
	for _, c := range w.kinds["check"] {
		t := str(c, "target")
		if u, ok := parseURL(t); ok {
			groups["url|"+u.key()] = append(groups["url|"+u.key()], c)
		} else if a, ok := parseAddr(t); ok {
			groups["addr|"+a] = append(groups["addr|"+a], c)
		}
	}
	return pairs(groups, "target", Strong, func(k string) string {
		_, t, _ := strings.Cut(k, "|")
		return "Both check " + t + "."
	})
}

// sameGuests: one VMID on one node (the node itself merged first), as two
// Proxmox instances on one cluster see it.
func (w *world) sameGuests() []cand {
	groups := map[string][]*graph.Resource{}
	for _, kind := range []graph.Kind{"vm", "lxc"} {
		for _, g := range w.kinds[kind] {
			vmid, ok := num(g, "vmid")
			nodes := w.targets(g.ID, RunsOn, "host")
			if !ok || len(nodes) != 1 {
				continue
			}
			k := fmt.Sprintf("%s|%d|%s", kind, int(vmid), w.uf.find(nodes[0].ID))
			groups[k] = append(groups[k], g)
		}
	}
	return pairs(groups, "vmid", Exact, func(k string) string {
		parts := strings.SplitN(k, "|", 3)
		return "Both report VMID " + parts[1] + " on the same node."
	})
}

// sameEngines: two Docker instances that list the same containers read one
// engine.
func (w *world) sameEngines() []cand {
	byContainer := map[graph.RID][]*graph.Resource{}
	for _, e := range w.kinds["engine"] {
		for _, c := range w.sources(e.ID, RunsOn, "container") {
			root := w.uf.find(c.ID)
			if !slices.ContainsFunc(byContainer[root], func(x *graph.Resource) bool { return x.ID == e.ID }) {
				byContainer[root] = append(byContainer[root], e)
			}
		}
	}
	groups := map[string][]*graph.Resource{}
	for _, es := range byContainer {
		if len(es) < 2 {
			continue
		}
		slices.SortFunc(es, func(a, b *graph.Resource) int { return cmp.Compare(a.ID, b.ID) })
		groups[string(es[0].ID)] = es
	}
	return pairs(groups, "containers", Exact, func(string) string { return "Both engines list the same containers." })
}

// weakSameApps suggests merging the only two apps of a type when they run
// the same version: probably one service reached two ways.
func (w *world) weakSameApps() {
	byType := map[string][]*graph.Resource{}
	for _, a := range w.primaries("app") {
		if t := str(a, "type"); t != "" {
			byType[t] = append(byType[t], a)
		}
	}
	var cs []cand
	for t, as := range byType {
		if len(as) != 2 {
			continue
		}
		v := str(as[0], "version")
		if v == "" || v != str(as[1], "version") {
			continue
		}
		cs = append(cs, same(as[1], as[0], "version", Weak,
			fmt.Sprintf("They're the only two %s instances, and both run version %s.", t, v)))
	}
	w.resolve(cs)
}

// placeEngines finds the host or guest each Docker engine runs on: the
// host whose own integration reports the engine's containers (Unraid), the
// host or guest at the engine's address, or (weakly) one with its name.
func (w *world) placeEngines() {
	w.indexIPs()
	for _, e := range w.primaries("engine") {
		slot := "runs_on:" + string(e.ID)
		cs := w.confirmed(e.ID, RunsOn, slot)
		members := w.members(e.ID)
		votes := map[graph.RID]int{}
		for _, m := range members {
			for _, c := range w.sources(m.ID, RunsOn, "container") {
				hosts := map[graph.RID]bool{}
				for _, cm := range w.members(c.ID) {
					for _, h := range w.targets(cm.ID, RunsOn, "host") {
						hosts[w.canon(h.ID)] = true
					}
				}
				for h := range hosts {
					votes[h]++
				}
			}
		}
		if len(votes) == 1 {
			for h, n := range votes {
				cs = append(cs, cand{Link: Link{From: e.ID, Type: RunsOn, To: h, Rule: "containers", Confidence: Exact,
					Evidence: fmt.Sprintf("%s reports %d of its containers by the same container IDs.", w.name(h), n)}, slot: slot, score: scoreAddress + 5})
			}
		}
		for _, m := range members {
			if ip := str(m, "ip"); routable(ip) {
				for _, t := range w.ips[ip] {
					cs = append(cs, cand{Link: Link{From: e.ID, Type: RunsOn, To: t, Rule: "ip", Confidence: Strong,
						Evidence: "The engine's address " + ip + " is " + w.name(t) + "'s."}, slot: slot, score: scoreAddress})
				}
			}
		}
		if len(cs) == 0 {
			for _, m := range members {
				hn := strings.ToLower(str(m, "hostname"))
				if hn == "" {
					continue
				}
				for _, kind := range []graph.Kind{"host", "vm", "lxc"} {
					for _, h := range w.primaries(kind) {
						if strings.EqualFold(str(h, "hostname"), hn) || strings.EqualFold(h.Name, hn) {
							cs = append(cs, cand{Link: Link{From: e.ID, Type: RunsOn, To: h.ID, Rule: "hostname", Confidence: Weak,
								Evidence: "The engine reports the host name " + hn + "."}, slot: slot, score: 1})
						}
					}
				}
			}
		}
		for _, l := range w.resolve(cs) {
			w.place[l.From] = l.To
		}
	}
}

// indexIPs maps routable addresses to the hosts and guests that have them.
func (w *world) indexIPs() {
	for _, kind := range []graph.Kind{"host", "vm", "lxc"} {
		for _, r := range w.kinds[kind] {
			if ip := str(r, "ip"); routable(ip) {
				id := w.canon(r.ID)
				if !slices.Contains(w.ips[ip], id) {
					w.ips[ip] = append(w.ips[ip], id)
				}
			}
		}
	}
}

// engineHosts are the names an engine's published ports answer on: its own
// address, and the address of the host or guest it runs on.
func (w *world) engineHosts(e graph.RID) []string {
	var hosts []string
	addHost := func(h string) {
		h = normHost(h)
		if usableHost(h) && !slices.Contains(hosts, h) {
			hosts = append(hosts, h)
		}
	}
	for _, m := range w.members(e) {
		addHost(str(m, "ip"))
		addHost(str(m, "host"))
	}
	if t, ok := w.place[w.canon(e)]; ok {
		for _, m := range w.members(t) {
			if ip := str(m, "ip"); routable(ip) {
				addHost(ip)
			}
		}
	}
	return hosts
}

// addresses indexes where each container answers: published ports on its
// engine's addresses, ports on its bridge IPs, and its labels.
func (w *world) addresses() {
	put := func(m map[string][]graph.RID, k string, id graph.RID) {
		if !slices.Contains(m[k], id) {
			m[k] = append(m[k], id)
		}
	}
	for _, c := range w.kinds["container"] {
		id := w.canon(c.ID)
		if u, ok := parseURL(c.Labels["homepage.href"]); ok {
			put(w.href, u.key(), id)
		}
		if u, ok := parseURL(c.Labels["homepage.widget.url"]); ok {
			put(w.widget, u.key(), id)
		}
		engines := w.targets(c.ID, RunsOn, "engine")
		if len(engines) != 1 {
			continue
		}
		hosts := w.engineHosts(engines[0].ID)
		for _, p := range strList(c, "published") {
			b, ok := parseBinding(p)
			if !ok || b.proto != "tcp" {
				continue
			}
			switch {
			case b.hostIP == "":
				for _, h := range hosts {
					put(w.hostAddrs, joinAddr(h, b.hostPort), id)
				}
			case routable(b.hostIP):
				put(w.hostAddrs, joinAddr(b.hostIP, b.hostPort), id)
			}
		}
		var ports []int
		for _, p := range strList(c, "ports") {
			if pp, ok := parsePort(p); ok && pp.proto == "tcp" {
				ports = append(ports, pp.port)
			}
		}
		if str(c, "network_mode") == "host" {
			for _, h := range hosts {
				for _, p := range ports {
					put(w.hostAddrs, joinAddr(h, p), id)
				}
			}
			continue
		}
		for _, ip := range strList(c, "ips") {
			if !routable(ip) {
				continue
			}
			for _, p := range ports {
				put(w.bridgeAddrs, joinAddr(normHost(ip), p), id)
			}
		}
	}
	for _, a := range w.kinds["app"] {
		var us []webURL
		for _, s := range append([]string{str(a, "url"), str(a, "poll_url")}, links(a)...) {
			if u, ok := parseURL(s); ok && !slices.Contains(us, u) {
				us = append(us, u)
			}
		}
		id := w.canon(a.ID)
		w.appURLs[id] = append(w.appURLs[id], us...)
	}
	for _, r := range w.kinds["route"] {
		if u, ok := parseURL(str(r, "url")); ok {
			w.routeURL[r.ID] = u
		}
	}
}

// routesToContainers: a route's backend is a port a container publishes,
// or the container's bridge address (Traefik's Docker provider), or the
// container's homepage.href is the route's URL.
func (w *world) routesToContainers() {
	for _, r := range w.primaries("route") {
		slot := "routes_to:container:" + string(r.ID)
		cs := w.confirmedKind(r.ID, RoutesTo, slot, "container")
		if ua, ok := parseAddr(str(r, "upstream_addr")); ok {
			for _, c := range w.hostAddrs[ua] {
				cs = append(cs, w.rel(r.ID, RoutesTo, c, "backend", Strong, slot, scoreAddress,
					"The route's backend "+ua+" is a port "+w.name(c)+" publishes."))
			}
			for _, c := range w.bridgeAddrs[ua] {
				cs = append(cs, w.rel(r.ID, RoutesTo, c, "backend", Strong, slot, scoreBridge,
					"The route's backend "+ua+" is "+w.name(c)+"'s address on its Docker network."))
			}
		}
		if u, ok := w.routeURL[r.ID]; ok {
			for _, c := range w.href[u.key()] {
				cs = append(cs, w.rel(r.ID, RoutesTo, c, "homepage.href", Strong, slot, scoreLabel,
					w.name(c)+"'s homepage.href label is the route's URL."))
			}
		}
		for _, l := range w.resolve(cs) {
			w.routeCont[l.From] = l.To
		}
	}
}

// appsToRoutes: an app's URL is served by the most specific route whose
// open URL it falls under, or a route's backend URL is the app's.
func (w *world) appsToRoutes() {
	routes := w.primaries("route")
	byRoute := map[graph.RID][]cand{}
	for _, a := range w.primaries("app") {
		var best []graph.RID
		bestLen := -1
		for _, u := range w.appURLs[a.ID] {
			for _, r := range routes {
				ru, ok := w.routeURL[r.ID]
				if !ok || !u.under(ru) {
					continue
				}
				switch n := len(ru.path); {
				case n > bestLen:
					best, bestLen = []graph.RID{r.ID}, n
				case n == bestLen && !slices.Contains(best, r.ID):
					best = append(best, r.ID)
				}
			}
		}
		for _, r := range best {
			byRoute[r] = append(byRoute[r], w.rel(r, RoutesTo, a.ID, "url", Strong, "", scoreRoute,
				w.name(a.ID)+"'s URL is served by this route."))
		}
		for _, u := range w.appURLs[a.ID] {
			for _, r := range routes {
				for _, k := range []string{"poll_url", "upstream"} {
					if bu, ok := parseURL(str(r, k)); ok && bu.key() == u.key() {
						byRoute[r.ID] = append(byRoute[r.ID], w.rel(r.ID, RoutesTo, a.ID, "backend", Strong, "", scoreAddress,
							"The route's backend is "+w.name(a.ID)+"'s URL."))
					}
				}
			}
		}
	}
	var cs []cand
	for _, r := range routes {
		slot := "routes_to:app:" + string(r.ID)
		cs = append(cs, w.confirmedKind(r.ID, RoutesTo, slot, "app")...)
		for _, c := range byRoute[r.ID] {
			c.slot = slot
			cs = append(cs, c)
		}
	}
	for _, l := range w.resolve(cs) {
		w.routeApp[l.From] = l.To
		if _, taken := w.appRoute[l.To]; !taken {
			w.appRoute[l.To] = l.From
		}
	}
}

// appsToContainers finds the container an app runs in: a chadash.id label
// naming it, a homepage label with its URL, a published port its URL
// reaches, or the container behind the route serving its URL (unless that
// route goes through another proxy, which its backend path gives away).
func (w *world) appsToContainers() {
	byLabel := map[string][]graph.RID{}
	for _, c := range w.kinds["container"] {
		if v := strings.TrimSpace(c.Labels["chadash.id"]); v != "" {
			byLabel[v] = append(byLabel[v], w.canon(c.ID))
		}
	}
	for _, a := range w.primaries("app") {
		if len(w.targets(a.ID, RunsOn)) > 0 {
			w.placedApps[a.ID] = true // its own integration says where it runs
			continue
		}
		slot := "runs_on:" + string(a.ID)
		cs := w.confirmed(a.ID, RunsOn, slot)
		for _, m := range w.members(a.ID) {
			for _, v := range []string{string(m.ID), instanceOf(m)} {
				for _, c := range byLabel[v] {
					cs = append(cs, w.rel(a.ID, RunsOn, c, "chadash.id", Exact, slot, scoreUser, w.name(c)+" is labelled chadash.id="+v+"."))
				}
			}
		}
		for _, u := range w.appURLs[a.ID] {
			for _, c := range w.href[u.key()] {
				cs = append(cs, w.rel(a.ID, RunsOn, c, "homepage.href", Strong, slot, scoreLabel, w.name(c)+"'s homepage.href label is "+w.name(a.ID)+"'s URL."))
			}
			for _, c := range w.widget[u.key()] {
				cs = append(cs, w.rel(a.ID, RunsOn, c, "homepage.widget.url", Strong, slot, scoreLabel, w.name(c)+"'s homepage.widget.url label is "+w.name(a.ID)+"'s URL."))
			}
			if u.path != "" {
				continue // a path means a proxy in front, more often than not
			}
			for _, c := range w.hostAddrs[u.addr()] {
				cs = append(cs, w.rel(a.ID, RunsOn, c, "address", Strong, slot, scoreAddress, w.name(a.ID)+"'s URL reaches "+u.addr()+", which "+w.name(c)+" publishes."))
			}
			for _, c := range w.bridgeAddrs[u.addr()] {
				cs = append(cs, w.rel(a.ID, RunsOn, c, "address", Strong, slot, scoreBridge, w.name(a.ID)+"'s URL reaches "+w.name(c)+"'s address on its Docker network."))
			}
		}
		if r, ok := w.appRoute[a.ID]; ok && str(w.res[r], "upstream_path") == "" {
			if c, ok := w.routeCont[r]; ok {
				cs = append(cs, w.rel(a.ID, RunsOn, c, "route", Strong, slot, scoreRoute, "The route serving "+w.name(a.ID)+"'s URL goes to "+w.name(c)+"."))
			}
		}
		for _, l := range w.resolve(cs) {
			w.placedApps[l.From] = true
			if r := w.res[l.To]; r != nil && r.Kind == "container" {
				w.appCont[l.From] = l.To
			}
		}
	}
}

// byIP places what the network knows only by address: an app whose URL is
// a literal address with no container behind it, and a router, run on the
// host or guest with that address.
func (w *world) byIP() {
	var cs []cand
	for _, a := range w.primaries("app") {
		if w.placedApps[a.ID] {
			continue
		}
		slot := "runs_on:" + string(a.ID)
		for _, u := range w.appURLs[a.ID] {
			if u.path != "" {
				continue
			}
			for _, t := range w.ips[u.host] {
				cs = append(cs, w.rel(a.ID, RunsOn, t, "ip", Strong, slot, scoreIP, w.name(a.ID)+"'s URL is at "+u.host+", "+w.name(t)+"'s address."))
			}
		}
	}
	for _, r := range w.primaries("router") {
		if len(w.targets(r.ID, RunsOn)) > 0 {
			continue
		}
		slot := "runs_on:" + string(r.ID)
		cs = append(cs, w.confirmed(r.ID, RunsOn, slot)...)
		if ip := str(r, "ip"); routable(ip) {
			for _, t := range w.ips[ip] {
				if k := w.res[t].Kind; k == "vm" || k == "lxc" {
					cs = append(cs, w.rel(r.ID, RunsOn, t, "ip", Strong, slot, scoreIP, "The router's address "+ip+" is "+w.name(t)+"'s."))
				}
			}
		}
	}
	for _, l := range w.resolve(cs) {
		w.placedApps[l.From] = true
	}
}

// routesToAppsViaContainer: a route to a container that runs exactly one
// app routes to that app.
func (w *world) routesToAppsViaContainer() {
	apps := map[graph.RID][]graph.RID{}
	for a, c := range w.appCont {
		apps[c] = append(apps[c], a)
	}
	var cs []cand
	for _, r := range w.primaries("route") {
		c, ok := w.routeCont[r.ID]
		if _, routed := w.routeApp[r.ID]; routed || !ok || len(apps[c]) != 1 {
			continue
		}
		a := apps[c][0]
		cs = append(cs, w.rel(r.ID, RoutesTo, a, "container", Strong, "routes_to:app:"+string(r.ID), scoreRoute,
			"The route goes to "+w.name(c)+", where "+w.name(a)+" runs."))
	}
	for _, l := range w.resolve(cs) {
		w.routeApp[l.From] = l.To
	}
}

// checks: a check monitors the app, route and container its target names.
func (w *world) checks() {
	routes := w.primaries("route")
	byKey := map[string][]graph.RID{}
	byAddr := map[string][]graph.RID{}
	for a, us := range w.appURLs {
		for _, u := range us {
			byKey[u.key()] = append(byKey[u.key()], a)
			if u.path == "" {
				byAddr[u.addr()] = append(byAddr[u.addr()], a)
			}
		}
	}
	var cs []cand
	for _, k := range w.primaries("check") {
		t := str(k, "target")
		appSlot, routeSlot, contSlot := "monitors:app:"+string(k.ID), "monitors:route:"+string(k.ID), "monitors:container:"+string(k.ID)
		var addr string
		if u, ok := parseURL(t); ok {
			for _, a := range uniq(byKey[u.key()]) {
				cs = append(cs, w.rel(k.ID, Monitors, a, "target", Strong, appSlot, scoreAddress, "The check's target is "+w.name(a)+"'s URL."))
			}
			bestLen := -1
			var best []graph.RID
			for _, r := range routes {
				ru, ok := w.routeURL[r.ID]
				if !ok || !u.under(ru) {
					continue
				}
				if n := len(ru.path); n > bestLen {
					best, bestLen = []graph.RID{r.ID}, n
				} else if n == bestLen {
					best = append(best, r.ID)
				}
			}
			for _, r := range best {
				cs = append(cs, w.rel(k.ID, Monitors, r, "target", Strong, routeSlot, scoreRoute, "The check's target is served by this route."))
			}
			for _, c := range w.href[u.key()] {
				cs = append(cs, w.rel(k.ID, Monitors, c, "homepage.href", Strong, contSlot, scoreLabel, "The check's target is "+w.name(c)+"'s homepage.href."))
			}
			if u.path == "" {
				addr = u.addr()
			}
		} else if a, ok := parseAddr(t); ok {
			addr = a
			for _, app := range uniq(byAddr[a]) {
				cs = append(cs, w.rel(k.ID, Monitors, app, "target", Strong, appSlot, scoreAddress, "The check's target is "+w.name(app)+"'s address."))
			}
		}
		if addr != "" {
			for _, c := range w.hostAddrs[addr] {
				cs = append(cs, w.rel(k.ID, Monitors, c, "address", Strong, contSlot, scoreAddress, "The check's target "+addr+" is a port "+w.name(c)+" publishes."))
			}
		}
	}
	w.resolve(cs)
}

// storages joins a Proxmox storage of type pbs to the PBS datastore it
// writes to: the PBS instance at the storage's server, its datastore by
// name. Without the server, a datastore name that only one PBS has is a
// suggestion.
func (w *world) storages() {
	type store struct {
		id   graph.RID
		host []string
	}
	byName := map[string][]store{}
	for _, d := range w.primaries("backup_target") {
		name := str(d, "store")
		if name == "" {
			continue
		}
		var hosts []string
		for _, m := range w.members(d.ID) {
			for _, app := range w.targets(m.ID, "part_of", "app") {
				for _, u := range w.appURLs[w.canon(app.ID)] {
					hosts = append(hosts, u.host)
				}
			}
		}
		byName[name] = append(byName[name], store{d.ID, hosts})
	}
	var cs []cand
	for _, s := range w.primaries("storage") {
		server, ds := normHost(str(s, "pbs.server")), str(s, "pbs.datastore")
		if server == "" || ds == "" {
			continue
		}
		slot := "stores_on:" + string(s.ID)
		cs = append(cs, w.confirmed(s.ID, StoresOn, slot)...)
		matched := false
		for _, st := range byName[ds] {
			if slices.Contains(st.host, server) {
				matched = true
				cs = append(cs, w.rel(s.ID, StoresOn, st.id, "pbs", Exact, slot, scoreAddress,
					"The storage writes to datastore "+ds+" on the PBS at "+server+"."))
			}
		}
		if !matched && len(byName[ds]) == 1 {
			cs = append(cs, w.rel(s.ID, StoresOn, byName[ds][0].id, "datastore", Weak, slot, 1,
				"The storage writes to a PBS datastore named "+ds+" at "+server+", and only this PBS has one by that name."))
		}
	}
	w.resolve(cs)
}

// dependencies that are evidenced: Compose's depends_on (the label
// com.docker.compose.depends_on), and Unraid's Docker, which runs only
// while the array is started.
func (w *world) dependencies() {
	type svc struct{ source, project, service string }
	bySvc := map[svc][]graph.RID{}
	for _, c := range w.kinds["container"] {
		p, s := c.Labels["com.docker.compose.project"], c.Labels["com.docker.compose.service"]
		if p != "" && s != "" {
			k := svc{c.Source, p, s}
			bySvc[k] = append(bySvc[k], c.ID)
		}
	}
	var cs []cand
	for _, c := range w.kinds["container"] {
		dep, p := c.Labels["com.docker.compose.depends_on"], c.Labels["com.docker.compose.project"]
		if dep == "" || p == "" {
			continue
		}
		for _, item := range strings.Split(dep, ",") {
			name, _, _ := strings.Cut(strings.TrimSpace(item), ":")
			for _, t := range bySvc[svc{c.Source, p, name}] {
				cs = append(cs, w.rel(c.ID, DependsOn, t, "compose", Exact, "", scoreUser,
					"Compose project "+p+" says "+c.Labels["com.docker.compose.service"]+" depends on "+name+"."))
			}
		}
	}
	for e, h := range w.place {
		unraidDocker := slices.ContainsFunc(w.members(e), func(m *graph.Resource) bool { return str(m, "manager") == "unraid" })
		unraidHost := slices.ContainsFunc(w.members(h), func(m *graph.Resource) bool { return str(m, "platform") == "unraid" })
		if !unraidDocker || !unraidHost {
			continue
		}
		for _, m := range w.members(h) {
			for _, arr := range w.sources(m.ID, "part_of", "array") {
				cs = append(cs, w.rel(e, DependsOn, arr.ID, "unraid", Strong, "", scoreAddress,
					"Unraid runs Docker only while the array is started."))
			}
		}
	}
	slices.SortFunc(cs, func(a, b cand) int { return cmp.Compare(a.Key(), b.Key()) })
	w.resolve(cs)
}

// names suggests the container an unplaced app runs in by name: the
// container, its Compose service or its image named like the app.
func (w *world) names() {
	containers := w.primaries("container")
	claimed := map[string]bool{} // "type|container": an app of that type runs there
	for a, c := range w.appCont {
		claimed[str(w.res[a], "type")+"|"+string(c)] = true
	}
	var cs []cand
	for _, a := range w.primaries("app") {
		if w.placedApps[a.ID] {
			continue
		}
		typ, an := str(a, "type"), compact(a.Name)
		if len(typ) < 3 {
			continue
		}
		best, bestScore := []graph.RID{}, 0
		for _, c := range containers {
			if claimed[typ+"|"+string(c.ID)] {
				continue
			}
			s := 0
			for _, m := range w.members(c.ID) {
				img := str(m, "image")
				img = img[strings.LastIndexByte(img, '/')+1:]
				img, _, _ = strings.Cut(img, ":")
				for _, n := range []string{m.Name, m.Labels["com.docker.compose.service"], img} {
					switch ts := tokens(n); {
					case n == "":
					case an != "" && compact(n) == an:
						s = max(s, 3)
					case slices.Contains(ts, typ):
						s = max(s, 2)
					case slices.ContainsFunc(ts, func(t string) bool { return strings.HasPrefix(t, typ) }):
						s = max(s, 1)
					}
				}
			}
			switch {
			case s > bestScore:
				best, bestScore = []graph.RID{c.ID}, s
			case s == bestScore && s > 0:
				best = append(best, c.ID)
			}
		}
		if len(best) == 1 {
			cs = append(cs, w.rel(a.ID, RunsOn, best[0], "name", Weak, "runs_on:"+string(a.ID), bestScore,
				w.name(a.ID)+" and the container "+w.name(best[0])+" have matching names."))
		}
	}
	w.resolve(cs)
}

// confirmedRest publishes confirmations no rule above considered.
func (w *world) confirmedRest() {
	keys := make([]pairKey, 0, len(w.dec))
	for k := range w.dec {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b pairKey) int {
		return cmp.Or(cmp.Compare(a.from, b.from), cmp.Compare(a.typ, b.typ), cmp.Compare(a.to, b.to))
	})
	var cs []cand
	for _, k := range keys {
		d := w.dec[k]
		if d.Verdict != Confirm || d.Type == SameAs || w.res[d.From] == nil || w.res[d.To] == nil {
			continue
		}
		cs = append(cs, cand{Link: Link{From: d.From, Type: d.Type, To: d.To, Rule: cmp.Or(d.Rule, "user"), Confidence: Confirmed,
			Evidence: "You confirmed this."}, score: scoreConfirmed})
	}
	w.resolve(cs)
}

// confirmedKind are confirmations from id to resources of one kind.
func (w *world) confirmedKind(id graph.RID, typ graph.RelType, slot string, kind graph.Kind) []cand {
	var out []cand
	for _, c := range w.confirmed(id, typ, slot) {
		if r := w.res[c.To]; r != nil && r.Kind == kind {
			out = append(out, c)
		}
	}
	return out
}

func (w *world) rel(from graph.RID, typ graph.RelType, to graph.RID, rule string, conf Confidence, slot string, score int, evidence string) cand {
	return cand{Link: Link{From: from, Type: typ, To: to, Rule: rule, Confidence: conf, Evidence: evidence}, slot: slot, score: score}
}

func uniq(ids []graph.RID) []graph.RID {
	out := slices.Clone(ids)
	slices.Sort(out)
	return slices.Compact(out)
}
