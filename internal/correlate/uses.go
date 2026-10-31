package correlate

import (
	"cmp"
	"encoding/json"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/integration"
)

// maxUses bounds the entries read from one resource's uses attr.
const maxUses = 32

// uses joins what an app says it uses (integration.AttrUses) to what
// serves it, and publishes app depends_on <it> with rule "uses". For each
// entry, in order: the app whose URL it is; the route whose open URL covers
// it (the app behind that route when the correlator knows it); the
// container that publishes its host:port (the app in it when there's only
// one). Each entry is one slot. An entry no address joins is a suggestion
// when exactly one app has its name.
func (w *world) uses() {
	appsByKey := map[string][]graph.RID{}
	for a, us := range w.appURLs {
		for _, u := range us {
			appsByKey[u.key()] = append(appsByKey[u.key()], a)
		}
	}
	appsIn := map[graph.RID][]graph.RID{}
	for a, c := range w.appCont {
		appsIn[c] = append(appsIn[c], a)
	}
	behind := func(c graph.RID) graph.RID { // the one app in c, or c
		if as := uniq(appsIn[c]); len(as) == 1 {
			return as[0]
		}
		return c
	}
	routes := w.primaries("route")
	apps := w.primaries("app")
	var cs []cand
	for _, a := range apps {
		for _, m := range w.members(a.ID) {
			for _, e := range usesOf(m) {
				what := cmp.Or(e.Name, e.URL)
				slot := "uses:" + string(a.ID) + ":" + cmp.Or(e.URL, "name:"+compact(e.Name))
				why := w.name(a.ID) + " uses " + what
				if e.Role != "" {
					why += " as its " + e.Role
				}
				found := false
				if u, ok := parseUse(e.URL); ok {
					for _, t := range uniq(appsByKey[u.key()]) {
						cs = append(cs, w.rel(a.ID, DependsOn, t, "uses", Strong, slot, scoreAddress, why+", at "+w.name(t)+"'s URL."))
						found = true
					}
					if !found {
						for _, r := range w.coveringRoutes(routes, u) {
							t := r
							if app, ok := w.routeApp[r]; ok {
								t = app
							}
							cs = append(cs, w.rel(a.ID, DependsOn, t, "uses", Strong, slot, scoreRoute, why+", at a URL the route "+w.name(r)+" serves."))
							found = true
						}
					}
					if !found {
						for _, c := range w.hostAddrs[u.addr()] {
							cs = append(cs, w.rel(a.ID, DependsOn, behind(c), "uses", Strong, slot, w.hostScore(c, u.addr()), why+", at "+u.addr()+", which "+w.name(c)+" publishes."))
							found = true
						}
						for _, c := range w.bridgeAddrs[u.addr()] {
							cs = append(cs, w.rel(a.ID, DependsOn, behind(c), "uses", Strong, slot, scoreBridge, why+", at "+w.name(c)+"'s address on its Docker network."))
							found = true
						}
					}
				}
				if !found && e.Name != "" {
					if t, ok := oneByName(apps, a.ID, e.Name); ok {
						cs = append(cs, w.rel(a.ID, DependsOn, t, "uses", Weak, slot, 5, why+", and "+w.name(t)+" has that name."))
					}
				}
			}
		}
	}
	w.resolve(cs)
}

// coveringRoutes are the most specific routes whose open URL covers u:
// every one with the longest path.
func (w *world) coveringRoutes(routes []*graph.Resource, u webURL) []graph.RID {
	var best []graph.RID
	bestLen := -1
	for _, r := range routes {
		ru, ok := w.routeURL[r.ID]
		if !ok || !u.under(ru) {
			continue
		}
		switch n := len(ru.path); {
		case n > bestLen:
			best, bestLen = []graph.RID{r.ID}, n
		case n == bestLen:
			best = append(best, r.ID)
		}
	}
	return best
}

// oneByName is the only app other than self named name, by its name or
// its type.
func oneByName(apps []*graph.Resource, self graph.RID, name string) (graph.RID, bool) {
	n := compact(name)
	if n == "" {
		return "", false
	}
	var found []graph.RID
	for _, a := range apps {
		if a.ID != self && (compact(a.Name) == n || str(a, "type") == n) {
			found = append(found, a.ID)
		}
	}
	if len(found) != 1 {
		return "", false
	}
	return found[0], true
}

// usesOf reads a resource's uses attr, whatever shape it arrived in, and
// keeps at most maxUses entries with a URL or a name.
func usesOf(r *graph.Resource) []integration.Use {
	v, ok := r.Attrs[integration.AttrUses]
	if !ok {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var us []integration.Use
	if json.Unmarshal(b, &us) != nil {
		return nil
	}
	us = slices.DeleteFunc(us, func(u integration.Use) bool { return u.URL == "" && u.Name == "" })
	return us[:min(len(us), maxUses)]
}

// parseUse reads a Use's URL: http(s) as an app's URL, and any other
// scheme with an explicit port ("mqtt://broker:1883") by its address.
func parseUse(s string) (webURL, bool) {
	if s == "" {
		return webURL{}, false
	}
	if u, ok := parseURL(s); ok {
		return u, true
	}
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil || u.User != nil || u.Port() == "" {
		return webURL{}, false
	}
	n, err := strconv.Atoi(u.Port())
	host := normHost(u.Hostname())
	if err != nil || n <= 0 || n > 65535 || host == "" {
		return webURL{}, false
	}
	return webURL{host: host, port: n}, true
}
