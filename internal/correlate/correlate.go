// Package correlate is identity across sources in the Home Graph. It finds
// the resources several integrations report as one thing and publishes
// "member same_as primary" for each, plus the relations between sources
// (runs_on, routes_to, stores_on, depends_on, monitors), without touching
// what the sources published. Weak matches become Inbox suggestions; the
// owner's answers persist. Incidents that share a cause across instances
// gain the causes their own instance can't see. The design is in README.md.
package correlate

import (
	"cmp"
	"fmt"
	"slices"
	"time"

	"dash.cha.sh/internal/graph"
)

// Relation types the correlator publishes.
const (
	SameAs    graph.RelType = "same_as"
	RunsOn    graph.RelType = "runs_on"
	RoutesTo  graph.RelType = "routes_to"
	StoresOn  graph.RelType = "stores_on"
	DependsOn graph.RelType = "depends_on"
	Monitors  graph.RelType = "monitors"
)

// Source is the provenance of every relation the correlator publishes. The
// correlator never reads its own relations back as evidence.
const Source = "correlator"

// Confidence says how a link was decided.
type Confidence string

const (
	// Exact: the same identifier, seen by two sources (a container ID, a
	// VMID on the same node, a disk serial, a chadash.id label).
	Exact Confidence = "exact"
	// Strong: one network identity or one explicit label (an address and
	// port, a route's backend, homepage.href). Published without asking.
	Strong Confidence = "strong"
	// Weak: names alike. Never published on its own; it becomes an Inbox
	// suggestion.
	Weak Confidence = "weak"
	// Confirmed: the owner said so.
	Confirmed Confidence = "confirmed"
)

var confidenceRank = map[Confidence]int{Exact: 0, Confirmed: 1, Strong: 2, Weak: 3}

// Link is one relation the correlator infers, with the rule that found it.
type Link struct {
	From       graph.RID     `json:"from"`
	Type       graph.RelType `json:"type"`
	To         graph.RID     `json:"to"`
	Rule       string        `json:"rule"`
	Confidence Confidence    `json:"confidence"`
	Evidence   string        `json:"evidence"`
}

// Key is the graph relation key the link is published under.
func (l Link) Key() string { return l.relation().Key() }

func (l Link) relation() graph.Relation {
	return graph.Relation{From: l.From, Type: l.Type, To: l.To, Source: Source}
}

// Verdict is the owner's answer to a suggestion.
type Verdict string

const (
	Confirm Verdict = "confirm" // merge, or link
	Reject  Verdict = "reject"  // keep separate; never suggest it again
)

// Decision is the owner's word on one pair. It outranks every rule: a
// confirmed pair is linked whatever the evidence, a rejected one never is.
type Decision struct {
	ID        string        `json:"id"`
	From      graph.RID     `json:"from"`
	Type      graph.RelType `json:"type"`
	To        graph.RID     `json:"to"`
	Verdict   Verdict       `json:"verdict"`
	Rule      string        `json:"rule,omitempty"` // the rule that suggested it, if a suggestion did
	CreatedAt time.Time     `json:"created_at"`
}

// Result is one pass over the graph.
type Result struct {
	// Links are published: exact and strong matches, and confirmed pairs.
	// Endpoints are canonical (see the package documentation), sorted by key.
	Links []Link
	// Suggestions are weak or ambiguous matches nobody has decided yet.
	Suggestions []Link
}

// pairKey identifies a decision; same_as is symmetric, so its pair is sorted.
type pairKey struct {
	from, to graph.RID
	typ      graph.RelType
}

func keyOf(from graph.RID, typ graph.RelType, to graph.RID) pairKey {
	if typ == SameAs && to < from {
		from, to = to, from
	}
	return pairKey{from, to, typ}
}

// Correlate finds the identities and cross-source relations in doc. It is a
// pure function: the same graph and decisions give the same result.
// Relations the correlator published earlier (Source "correlator") are
// ignored, so its output never feeds back into its input.
func Correlate(doc graph.Doc, decisions []Decision) Result {
	w := newWorld(doc, decisions)
	w.identities()
	w.placeEngines()
	w.addresses()
	w.routesToContainers()
	w.appsToRoutes()
	w.appsToContainers()
	w.byIP()
	w.routesToAppsViaContainer()
	w.checks()
	w.storages()
	w.dependencies()
	w.names()
	w.confirmedRest()
	return w.result()
}

// cand is a candidate link. Within a slot (a functional relation such as
// "what this app runs on"), the highest score wins; a tie between distinct
// targets is ambiguous and becomes suggestions.
type cand struct {
	Link
	slot  string
	score int
}

// Scores: confirmed beats any rule; weak scores stay below 10.
const (
	scoreConfirmed = 100
	scoreUser      = 50 // chadash.id
	scoreLabel     = 40 // homepage.href, homepage.widget.url
	scoreAddress   = 30
	scoreBridge    = 25
	scoreRoute     = 20
	scoreIP        = 10
)

type world struct {
	res   map[graph.RID]*graph.Resource
	kinds map[graph.Kind][]*graph.Resource // by ID
	out   map[graph.RID][]*graph.Relation
	in    map[graph.RID][]*graph.Relation
	dec   map[pairKey]Decision

	uf      *unionFind
	joined  map[graph.RID]Link // member → the same_as link that joined it
	primary map[graph.RID]graph.RID

	place       map[graph.RID]graph.RID // engine → the host or guest it runs on
	hostAddrs   map[string][]graph.RID  // "host:port" → containers publishing it
	bridgeAddrs map[string][]graph.RID  // "bridge ip:port" → containers
	href        map[string][]graph.RID  // homepage.href URL key → containers
	widget      map[string][]graph.RID  // homepage.widget.url URL key → containers
	ips         map[string][]graph.RID  // routable IP → hosts and guests
	appURLs     map[graph.RID][]webURL  // app → its URLs
	routeURL    map[graph.RID]webURL    // route → its open URL
	routeCont   map[graph.RID]graph.RID // route → the container it routes to
	routeApp    map[graph.RID]graph.RID // route → the app it routes to
	appRoute    map[graph.RID]graph.RID // app → the route serving its URL
	appCont     map[graph.RID]graph.RID // app → its container
	placedApps  map[graph.RID]bool      // apps with a runs_on of any kind
	linkKeys    map[string]int          // published link key → index in links
	links       []Link
	suggestions []Link
}

func newWorld(doc graph.Doc, decisions []Decision) *world {
	w := &world{
		res: map[graph.RID]*graph.Resource{}, kinds: map[graph.Kind][]*graph.Resource{},
		out: map[graph.RID][]*graph.Relation{}, in: map[graph.RID][]*graph.Relation{},
		dec: map[pairKey]Decision{}, joined: map[graph.RID]Link{}, primary: map[graph.RID]graph.RID{},
		place: map[graph.RID]graph.RID{}, hostAddrs: map[string][]graph.RID{}, bridgeAddrs: map[string][]graph.RID{},
		href: map[string][]graph.RID{}, widget: map[string][]graph.RID{}, ips: map[string][]graph.RID{},
		appURLs: map[graph.RID][]webURL{}, routeURL: map[graph.RID]webURL{},
		routeCont: map[graph.RID]graph.RID{}, routeApp: map[graph.RID]graph.RID{},
		appRoute: map[graph.RID]graph.RID{}, appCont: map[graph.RID]graph.RID{}, placedApps: map[graph.RID]bool{},
		linkKeys: map[string]int{},
	}
	for id, r := range doc.Resources {
		w.res[id] = r
		w.kinds[r.Kind] = append(w.kinds[r.Kind], r)
	}
	for _, rs := range w.kinds {
		slices.SortFunc(rs, func(a, b *graph.Resource) int { return cmp.Compare(a.ID, b.ID) })
	}
	keys := make([]string, 0, len(doc.Relations))
	for k := range doc.Relations {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		rel := doc.Relations[k]
		if rel.Source == Source {
			continue
		}
		w.out[rel.From] = append(w.out[rel.From], rel)
		w.in[rel.To] = append(w.in[rel.To], rel)
	}
	for _, d := range decisions {
		w.dec[keyOf(d.From, d.Type, d.To)] = d
	}
	w.uf = newUnionFind()
	for _, d := range decisions {
		if d.Type == SameAs && d.Verdict == Reject {
			w.uf.forbid(d.From, d.To)
		}
	}
	return w
}

// targets are the resources of a kind that id relates to over typ, by
// relations from the sources.
func (w *world) targets(id graph.RID, typ graph.RelType, kinds ...graph.Kind) []*graph.Resource {
	var out []*graph.Resource
	for _, rel := range w.out[id] {
		if rel.Type != typ {
			continue
		}
		if r := w.res[rel.To]; r != nil && (len(kinds) == 0 || slices.Contains(kinds, r.Kind)) {
			out = append(out, r)
		}
	}
	return out
}

// sources are the resources of a kind that relate to id over typ.
func (w *world) sources(id graph.RID, typ graph.RelType, kinds ...graph.Kind) []*graph.Resource {
	var out []*graph.Resource
	for _, rel := range w.in[id] {
		if rel.Type != typ {
			continue
		}
		if r := w.res[rel.From]; r != nil && (len(kinds) == 0 || slices.Contains(kinds, r.Kind)) {
			out = append(out, r)
		}
	}
	return out
}

// canon is the primary of id's identity.
func (w *world) canon(id graph.RID) graph.RID {
	if p, ok := w.primary[w.uf.find(id)]; ok {
		return p
	}
	return id
}

// isPrimary reports whether r stands for its identity.
func (w *world) isPrimary(r *graph.Resource) bool { return w.canon(r.ID) == r.ID }

// members are every resource of id's identity, primary first.
func (w *world) members(id graph.RID) []*graph.Resource {
	root := w.uf.find(id)
	ids := w.uf.members[root]
	if len(ids) == 0 {
		ids = []graph.RID{id}
	}
	out := make([]*graph.Resource, 0, len(ids))
	p := w.canon(id)
	if r := w.res[p]; r != nil {
		out = append(out, r)
	}
	for _, m := range ids {
		if m != p {
			if r := w.res[m]; r != nil {
				out = append(out, r)
			}
		}
	}
	return out
}

func (w *world) primaries(kind graph.Kind) []*graph.Resource {
	var out []*graph.Resource
	for _, r := range w.kinds[kind] {
		if w.isPrimary(r) {
			out = append(out, r)
		}
	}
	return out
}

func (w *world) name(id graph.RID) string {
	if r := w.res[id]; r != nil && r.Name != "" {
		return r.Name
	}
	return string(id)
}

// decided returns the owner's decision on a pair, by the IDs given or
// their canonical ones.
func (w *world) decided(from graph.RID, typ graph.RelType, to graph.RID) (Decision, bool) {
	if d, ok := w.dec[keyOf(from, typ, to)]; ok {
		return d, true
	}
	d, ok := w.dec[keyOf(w.canon(from), typ, w.canon(to))]
	return d, ok
}

// confirmed are the owner's confirmations of typ from id, as candidates
// that win their slot.
func (w *world) confirmed(id graph.RID, typ graph.RelType, slot string) []cand {
	var out []cand
	for k, d := range w.dec {
		if k.typ != typ || d.Verdict != Confirm || w.canon(d.From) != id || w.res[d.To] == nil {
			continue
		}
		out = append(out, cand{Link: Link{From: d.From, Type: typ, To: d.To, Rule: cmp.Or(d.Rule, "user"), Confidence: Confirmed,
			Evidence: "You confirmed this."}, slot: slot, score: scoreConfirmed})
	}
	slices.SortFunc(out, func(a, b cand) int { return cmp.Compare(a.Key(), b.Key()) })
	return out
}

// resolve canonicalises candidates, drops rejected ones, picks each slot's
// winner and records links and suggestions. It returns the links it
// published, so later rules can build on them.
func (w *world) resolve(cs []cand) []Link {
	slots := map[string][]cand{}
	var order []string
	for _, c := range cs {
		if d, ok := w.decided(c.From, c.Type, c.To); ok && d.Verdict == Reject {
			continue
		}
		c.From, c.To = w.canon(c.From), w.canon(c.To)
		if c.From == c.To {
			continue
		}
		if d, ok := w.decided(c.From, c.Type, c.To); ok && d.Verdict == Reject {
			continue
		}
		s := c.slot
		if s == "" {
			s = "\x00" + c.Key()
		}
		if _, ok := slots[s]; !ok {
			order = append(order, s)
		}
		slots[s] = append(slots[s], c)
	}
	var published []Link
	for _, s := range order {
		group := slots[s]
		best := 0
		for _, c := range group {
			best = max(best, c.score)
		}
		var top []cand
		seen := map[graph.RID]bool{}
		for _, c := range group {
			if c.score == best && !seen[c.To] {
				seen[c.To] = true
				top = append(top, c)
			}
		}
		allConfirmed := true
		for _, c := range top {
			allConfirmed = allConfirmed && c.Confidence == Confirmed
		}
		switch {
		case len(top) == 1 && top[0].Confidence != Weak, allConfirmed:
			for _, c := range top {
				if w.add(c.Link) {
					published = append(published, c.Link)
				}
			}
		case len(top) == 1:
			w.suggest(top[0].Link)
		default:
			slices.SortFunc(top, func(a, b cand) int { return cmp.Compare(a.To, b.To) })
			for _, c := range top[:min(3, len(top))] {
				l := c.Link
				if l.Confidence != Weak {
					l.Evidence += fmt.Sprintf(" It's one of %d matches, so Cha Dash asks first.", len(top))
					l.Confidence = Weak
				}
				w.suggest(l)
			}
		}
	}
	return published
}

// add publishes a link once, keeping the most certain version.
func (w *world) add(l Link) bool {
	if i, ok := w.linkKeys[l.Key()]; ok {
		if confidenceRank[l.Confidence] < confidenceRank[w.links[i].Confidence] {
			w.links[i] = l
		}
		return false
	}
	w.linkKeys[l.Key()] = len(w.links)
	w.links = append(w.links, l)
	return true
}

// suggest records a weak link for the Inbox, unless the owner has decided
// on the pair.
func (w *world) suggest(l Link) {
	if _, ok := w.decided(l.From, l.Type, l.To); ok {
		return
	}
	w.suggestions = append(w.suggestions, l)
}

func (w *world) result() Result {
	// The identities: every member but the primary is same_as the primary.
	for root, ids := range w.uf.members {
		if len(ids) < 2 {
			continue
		}
		p := w.primary[root]
		for _, m := range ids {
			if m == p {
				continue
			}
			l := w.joined[m]
			l.From, l.Type, l.To = m, SameAs, p
			w.add(l)
		}
	}
	links := slices.Clone(w.links)
	slices.SortFunc(links, func(a, b Link) int { return cmp.Compare(a.Key(), b.Key()) })
	var sugg []Link
	seen := map[string]bool{}
	for _, s := range w.suggestions {
		k := keyOf(s.From, s.Type, s.To)
		ks := string(k.from) + " " + string(k.typ) + " " + string(k.to)
		if seen[ks] {
			continue
		}
		if _, published := w.linkKeys[s.Key()]; published {
			continue
		}
		if s.Type == SameAs && w.canon(s.From) == w.canon(s.To) {
			continue
		}
		seen[ks] = true
		sugg = append(sugg, s)
	}
	slices.SortFunc(sugg, func(a, b Link) int { return cmp.Compare(a.Key(), b.Key()) })
	return Result{Links: links, Suggestions: sugg}
}

// unionFind groups same_as members, honouring "keep separate" decisions:
// a union that would put a rejected pair in one identity is refused.
type unionFind struct {
	parent  map[graph.RID]graph.RID
	members map[graph.RID][]graph.RID // root → members, sorted
	cannot  map[graph.RID][]graph.RID
}

func newUnionFind() *unionFind {
	return &unionFind{parent: map[graph.RID]graph.RID{}, members: map[graph.RID][]graph.RID{}, cannot: map[graph.RID][]graph.RID{}}
}

func (u *unionFind) forbid(a, b graph.RID) {
	u.cannot[a] = append(u.cannot[a], b)
	u.cannot[b] = append(u.cannot[b], a)
}

func (u *unionFind) find(x graph.RID) graph.RID {
	for {
		p, ok := u.parent[x]
		if !ok || p == x {
			return x
		}
		if gp, ok := u.parent[p]; ok && gp != p {
			u.parent[x] = gp
		}
		x = p
	}
}

func (u *unionFind) set(x graph.RID) []graph.RID {
	if m, ok := u.members[u.find(x)]; ok {
		return m
	}
	return []graph.RID{x}
}

// union joins a's and b's identities. It reports false when they're one
// already or a rejected pair forbids it.
func (u *unionFind) union(a, b graph.RID) bool {
	ra, rb := u.find(a), u.find(b)
	if ra == rb {
		return false
	}
	sa, sb := u.set(a), u.set(b)
	for _, x := range sa {
		for _, y := range u.cannot[x] {
			if slices.Contains(sb, y) {
				return false
			}
		}
	}
	root, other := ra, rb
	if rb < ra {
		root, other = rb, ra
	}
	u.parent[other] = root
	u.parent[root] = root
	merged := append(slices.Clone(sa), sb...)
	slices.Sort(merged)
	u.members[root] = merged
	delete(u.members, other)
	return true
}

// rank orders the members of an identity: the first becomes the primary,
// the one whose ID readers see. Docker knows a container best (Unraid folds
// "created" into "exited"); the built-in checks are the hub's own.
func rank(r *graph.Resource) int {
	switch integrationOf(r.ID) {
	case "docker", "check":
		return 0
	case "proxmox", "unraid":
		return 1
	}
	return 2
}

func sameSource(a, b *graph.Resource) bool { return a.Source != "" && a.Source == b.Source }
