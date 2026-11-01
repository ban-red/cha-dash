// Package causes finds cause hints: for every primary resource that isn't
// ok, and every open problem, what it needs that isn't ok either or changed
// around when the trouble began. A hint is a sentence and the resource it
// points at, never a verdict: it changes no health and opens no incident.
//
// Find is the engine, a pure function of the graph, the clock, the open
// incidents, the health history and the events the Service remembers. The
// Service runs it whenever the graph or the inbox changes, writes the
// causes topic, and adds the hints to open problem incidents. The design
// is in docs/plan/09-v0.3.md, "Cause hints"; README.md has the rules.
package causes

import (
	"cmp"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/history"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/topics"
)

// The walk and the window ([G3]).
const (
	// MaxDepth is how many relations the walk follows from a symptom.
	MaxDepth = 3
	// MaxCauses is how many causes a symptom gets.
	MaxCauses = 3
	// Before and After bound the window around when a symptom began in
	// which a change counts: polls lag, so a cause can show up after.
	Before = 30 * time.Minute
	After  = 2 * time.Minute
)

// Rules: what made a resource a cause, in Cause.Rule.
const (
	RuleOOMKilled = "oom_killed"   // a container was OOM-killed
	RuleExited    = "exited"       // a container exited
	RuleRestarted = "restarted"    // a container's restart count went up
	RuleStarted   = "started"      // a container, guest or host started
	RuleIncident  = "incident"     // an open problem on it
	RuleChanged   = "state_change" // its health changed in the window (history)
	RuleHealth    = "health"       // it isn't ok now
)

// Relation types the walk follows.
const (
	sameAs             graph.RelType = "same_as"
	runsOn             graph.RelType = "runs_on"
	partOf             graph.RelType = "part_of"
	dependsOn          graph.RelType = "depends_on"
	storesOn           graph.RelType = "stores_on"
	routesTo           graph.RelType = "routes_to"
	monitors           graph.RelType = "monitors"
	providesNetworkFor graph.RelType = "provides_network_for"
)

// Event is something that happened to a resource, which the Service
// remembers past the graph: a container that was OOM-killed and restarted
// no longer says so.
type Event struct {
	Rule string    // RuleOOMKilled, RuleExited, RuleRestarted
	At   time.Time // when
	Code int       // RuleExited: the exit code
}

// Input is what Find reads.
type Input struct {
	Doc graph.Doc
	Now time.Time
	// Incidents are the inbox's incidents by ID; only open problems count.
	Incidents map[string]integration.Incident
	// Changes is a resource's health changes over [from, to), the one in
	// effect at from first (history.Recorder.States). Nil when history is
	// off or not kept, as in demo mode.
	Changes func(resource string, from, to time.Time) []history.StateChange
	// Events are what the Service remembers, by resource.
	Events map[graph.RID][]Event
	// Clock formats a time of day for the sentences ("14:03"). Nil: UTC.
	Clock func(time.Time) string
}

// Find returns the causes of every symptom that has any, the first first.
func Find(in Input) map[graph.RID][]topics.Cause {
	h := newHome(in)
	out := map[graph.RID][]topics.Cause{}
	for _, s := range h.symptoms() {
		if cs := h.causesOf(s); len(cs) > 0 {
			out[s.id] = cs
		}
	}
	return out
}

// edge is one step the walk can take: from needs to, through rel.
type edge struct {
	to  graph.RID
	rel graph.Relation
}

type home struct {
	in        Input
	res       map[graph.RID]*graph.Resource
	primary   map[graph.RID]graph.RID
	members   map[graph.RID][]*graph.Resource
	needs     map[graph.RID][]edge
	runsHere  map[graph.RID][]graph.RID // container → apps that run on it
	incidents map[graph.RID][]integration.Incident
}

func newHome(in Input) *home {
	if in.Clock == nil {
		in.Clock = func(t time.Time) string { return t.UTC().Format("15:04") }
	}
	h := &home{in: in, res: in.Doc.Resources, primary: map[graph.RID]graph.RID{}, members: map[graph.RID][]*graph.Resource{},
		needs: map[graph.RID][]edge{}, runsHere: map[graph.RID][]graph.RID{}, incidents: map[graph.RID][]integration.Incident{}}
	same := map[graph.RID]graph.RID{}
	for _, r := range in.Doc.Relations {
		if r.Type == sameAs {
			same[r.From] = r.To
		}
	}
	for id := range h.res {
		p := id
		for range 4 { // the correlator points a member straight at its primary
			next, ok := same[p]
			if !ok || h.res[next] == nil {
				break
			}
			p = next
		}
		h.primary[id] = p
	}
	for _, id := range slices.Sorted(maps.Keys(h.res)) {
		p := h.primary[id]
		h.members[p] = append(h.members[p], h.res[id])
	}
	seen := map[string]bool{}
	keys := slices.Sorted(maps.Keys(in.Doc.Relations))
	for _, k := range keys {
		r := in.Doc.Relations[k]
		from, to := h.canon(r.From), h.canon(r.To)
		if from == "" || to == "" || from == to {
			continue
		}
		switch r.Type {
		case runsOn, partOf, dependsOn, storesOn, routesTo, monitors:
		case providesNetworkFor:
			from, to = to, from
		default:
			continue
		}
		if key := string(from) + " " + string(to); !seen[key] {
			seen[key] = true
			h.needs[from] = append(h.needs[from], edge{to: to, rel: graph.Relation{From: h.canon(r.From), Type: r.Type, To: h.canon(r.To)}})
		}
		if r.Type == runsOn && h.res[from] != nil && h.res[from].Kind == "app" {
			h.runsHere[to] = append(h.runsHere[to], from)
		}
	}
	for _, id := range slices.Sorted(maps.Keys(in.Incidents)) {
		inc := in.Incidents[id]
		if inc.Status != integration.StatusOpen || inc.Class != integration.ClassProblem || inc.Category == integration.CategorySecurity {
			continue
		}
		if t := h.canon(graph.RID(inc.Target)); t != "" {
			h.incidents[t] = append(h.incidents[t], inc)
		}
	}
	return h
}

// canon is a resource's primary, or "" when the graph doesn't have it.
func (h *home) canon(id graph.RID) graph.RID {
	if p, ok := h.primary[id]; ok {
		return p
	}
	return ""
}

// health is the worst of a primary's members', as tiles merge it.
func (h *home) health(id graph.RID) graph.Health {
	worst, rank := graph.HealthOK, 0
	for _, m := range h.members[id] {
		if r := healthRank[m.Health]; r > rank {
			worst, rank = m.Health, r
		}
	}
	return worst
}

var healthRank = map[graph.Health]int{graph.HealthMaintenance: 1, graph.HealthAttention: 2, graph.HealthDown: 3}

// since is when a primary's current health began: the newest health.since
// among the members with that health.
func (h *home) since(id graph.RID, health graph.Health) (time.Time, bool) {
	var at time.Time
	for _, m := range h.members[id] {
		if m.Health != health {
			continue
		}
		if t, ok := attrTime(m, integration.AttrHealthSince); ok && t.After(at) {
			at = t
		}
	}
	return at, !at.IsZero()
}

// symptom is a resource to explain, and when its trouble began.
type symptom struct {
	id    graph.RID
	began time.Time
}

// symptoms are the primaries that are down or need attention, and the
// targets of open problems, sorted by ID. An integration's own resource
// isn't one: its trouble is a repair.
func (h *home) symptoms() []symptom {
	began := map[graph.RID]time.Time{}
	for p := range h.members {
		if r := h.res[p]; r != nil && r.Kind == "integration" {
			continue
		}
		if hl := h.health(p); hl == graph.HealthDown || hl == graph.HealthAttention {
			t, ok := h.since(p, hl)
			if !ok {
				t = h.in.Now
			}
			began[p] = t
		}
	}
	for t, incs := range h.incidents {
		for _, inc := range incs {
			at := inc.OpenedAt
			if at.IsZero() {
				at = h.in.Now
			}
			if b, ok := began[t]; !ok || at.Before(b) {
				began[t] = at
			}
		}
	}
	out := make([]symptom, 0, len(began))
	for id, t := range began {
		out = append(out, symptom{id, t})
	}
	slices.SortFunc(out, func(a, b symptom) int { return cmp.Compare(a.id, b.id) })
	return out
}

// candidate is a resource on the walk with a reason to be a cause.
type candidate struct {
	id     graph.RID
	path   []graph.Relation // from the symptom to id
	rule   string
	at     time.Time
	what   string // the cause's sentence
	plain  bool   // only "isn't ok now": a repeat when it's on a root's path
	parent graph.RID
}

// causesOf walks from s, breadth first, and ranks what it finds.
func (h *home) causesOf(s symptom) []topics.Cause {
	from, to := s.began.Add(-Before), s.began.Add(After)
	type node struct {
		id   graph.RID
		path []graph.Relation
	}
	visited := map[graph.RID]bool{s.id: true}
	queue := []node{{id: s.id}}
	var cands []candidate
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		if len(n.path) == MaxDepth {
			continue
		}
		for _, e := range h.needs[n.id] {
			if visited[e.to] {
				continue
			}
			visited[e.to] = true
			path := append(slices.Clone(n.path), e.rel)
			if c, ok := h.reason(e.to, from, to); ok {
				c.path, c.parent = path, n.id
				cands = append(cands, c)
			}
			queue = append(queue, node{id: e.to, path: path})
		}
	}
	return h.rank(s, cands)
}

// reason is why id may be a cause, the strongest reason first: an event
// in the window, an open problem, a change in the window, or that it
// isn't ok now. A problem or a health that began after the window is no
// cause: it came after the symptom.
func (h *home) reason(id graph.RID, from, to time.Time) (candidate, bool) {
	in := func(t time.Time) bool { return !t.Before(from) && !t.After(to) }
	name := h.causeName(id)
	clock := h.in.Clock
	var evs []Event
	evs = append(evs, h.in.Events[id]...)
	for _, m := range h.members[id] {
		evs = append(evs, eventsOf(m)...)
	}
	slices.SortStableFunc(evs, func(a, b Event) int { return cmp.Compare(eventRank[a.Rule], eventRank[b.Rule]) })
	for _, e := range evs {
		if !in(e.At) {
			continue
		}
		c := candidate{id: id, rule: e.Rule, at: e.At}
		switch e.Rule {
		case RuleOOMKilled:
			c.what = name + " was OOM-killed at " + clock(e.At) + "."
		case RuleExited:
			c.what = name + " exited (code " + strconv.Itoa(e.Code) + ") at " + clock(e.At) + "."
		case RuleRestarted:
			c.what = name + " restarted at " + clock(e.At) + "."
		case RuleStarted:
			verb := " started at "
			if h.res[id] != nil && h.res[id].Kind == "host" {
				verb = " booted at "
			}
			c.what = name + verb + clock(e.At) + "."
		}
		return c, true
	}
	for _, inc := range h.incidents[id] {
		if inc.OpenedAt.After(to) {
			continue
		}
		return candidate{id: id, rule: RuleIncident, at: inc.OpenedAt,
			what: name + " has a problem: " + strings.TrimSuffix(inc.Title, ".") + "."}, true
	}
	health := h.health(id)
	if h.in.Changes != nil {
		// The first time in the window a member went from ok to something
		// worse. The row in effect at from is moved to from: not a change.
		var first history.StateChange
		for _, m := range h.members[id] {
			for _, ch := range h.in.Changes(string(m.ID), from, to.Add(time.Millisecond)) {
				if ch.At.After(from) && in(ch.At) && healthRank[graph.Health(ch.Status)] > 0 && (first.At.IsZero() || ch.At.Before(first.At)) {
					first = ch
				}
			}
		}
		if !first.At.IsZero() {
			return candidate{id: id, rule: RuleChanged, at: first.At, what: name + " " + went(first.Status) + " at " + clock(first.At) + "."}, true
		}
	}
	if healthRank[health] > 0 {
		c := candidate{id: id, rule: RuleHealth, plain: true}
		since, ok := h.since(id, health)
		if ok && since.After(to) {
			return candidate{}, false
		}
		if ok {
			c.at = since
		}
		c.what = name + " " + is(health)
		if r := h.reasonText(id, health); r != "" {
			c.what += " (" + r + ")"
		}
		if ok {
			c.what += " since " + clock(since)
		}
		c.what += "."
		return c, true
	}
	return candidate{}, false
}

var eventRank = map[string]int{RuleOOMKilled: 0, RuleExited: 1, RuleRestarted: 2, RuleStarted: 3}

func went(status string) string {
	switch graph.Health(status) {
	case graph.HealthDown:
		return "went down"
	case graph.HealthAttention:
		return "began to need attention"
	case graph.HealthMaintenance:
		return "went into maintenance"
	}
	return "became " + status
}

func is(h graph.Health) string {
	switch h {
	case graph.HealthDown:
		return "has been down"
	case graph.HealthAttention:
		return "has needed attention"
	}
	return "has been in maintenance"
}

// reasonText is the health reason of the member with that health.
func (h *home) reasonText(id graph.RID, health graph.Health) string {
	for _, m := range h.members[id] {
		if m.Health == health {
			if s, _ := m.Attrs[integration.AttrHealthReason].(string); s != "" {
				return s
			}
		}
	}
	return ""
}

// eventsOf reads what a resource's attrs say happened: a container's last
// exit (OOM or not) and start, a guest's start, a host's boot.
func eventsOf(r *graph.Resource) []Event {
	var out []Event
	switch r.Kind {
	case "container":
		end, ok := attrTime(r, "finished_at")
		if !ok && r.State == "exited" {
			end, ok = attrTime(r, integration.AttrHealthSince)
		}
		if ok {
			if oom, _ := r.Attrs["oom_killed"].(bool); oom {
				out = append(out, Event{Rule: RuleOOMKilled, At: end})
			} else if code, ok := attrInt(r, "exit_code"); ok {
				out = append(out, Event{Rule: RuleExited, At: end, Code: code})
			}
		}
		if t, ok := attrTime(r, "started_at"); ok {
			out = append(out, Event{Rule: RuleStarted, At: t})
		}
	case "vm", "lxc":
		if t, ok := attrTime(r, "started_at"); ok {
			out = append(out, Event{Rule: RuleStarted, At: t})
		}
	case "host":
		if t, ok := attrTime(r, "boot_at"); ok {
			out = append(out, Event{Rule: RuleStarted, At: t})
		}
	}
	return out
}

// causeName names a cause: a container one app runs on is that app's
// container ("qBittorrent's container").
func (h *home) causeName(id graph.RID) string {
	r := h.res[id]
	if r != nil && r.Kind == "container" {
		if apps := h.runsHere[id]; len(apps) == 1 {
			return h.name(apps[0]) + "'s container"
		}
		return "The container " + h.name(id)
	}
	return h.name(id)
}

func (h *home) name(id graph.RID) string {
	if r := h.res[id]; r != nil && r.Name != "" {
		return r.Name
	}
	return string(id)
}

// rank orders the candidates: the root first (of the candidates with none
// further along their own walk, the furthest, then the closest in time);
// then the rest, the nearer first, then by how close in time. A plain
// "isn't ok" on the root's path says the same thing again and is left out.
func (h *home) rank(s symptom, cands []candidate) []topics.Cause {
	if len(cands) == 0 {
		return nil
	}
	isCand := map[graph.RID]bool{}
	for _, c := range cands {
		isCand[c.id] = true
	}
	hasCause := map[graph.RID]bool{} // a candidate another candidate's path goes through
	for _, c := range cands {
		for _, r := range c.path[:len(c.path)-1] {
			if t := h.stepTarget(r); isCand[t] {
				hasCause[t] = true
			}
		}
	}
	dist := func(c candidate) time.Duration {
		if c.at.IsZero() {
			return 1 << 62
		}
		return max(c.at.Sub(s.began), s.began.Sub(c.at))
	}
	var root *candidate
	for i, c := range cands {
		if hasCause[c.id] {
			continue
		}
		if root == nil || cmp.Or(cmp.Compare(len(root.path), len(c.path)), cmp.Compare(dist(c), dist(*root)), cmp.Compare(c.id, root.id)) < 0 {
			root = &cands[i]
		}
	}
	onRootPath := map[graph.RID]bool{}
	for _, r := range root.path {
		onRootPath[h.stepTarget(r)] = true
	}
	all := []candidate{*root}
	for _, c := range cands {
		if c.id != root.id && !(c.plain && onRootPath[c.id]) {
			all = append(all, c)
		}
	}
	slices.SortStableFunc(all[1:], func(a, b candidate) int {
		return cmp.Or(cmp.Compare(len(a.path), len(b.path)), cmp.Compare(dist(a), dist(b)), cmp.Compare(a.id, b.id))
	})
	out := make([]topics.Cause, 0, min(len(all), MaxCauses))
	for _, c := range all[:min(len(all), MaxCauses)] {
		out = append(out, h.cause(s, c))
	}
	return out
}

// stepTarget is the resource a step leads to: the walk can follow
// provides_network_for backwards.
func (h *home) stepTarget(r graph.Relation) graph.RID {
	if r.Type == providesNetworkFor {
		return r.From
	}
	return r.To
}

func (h *home) cause(s symptom, c candidate) topics.Cause {
	steps := make([]topics.Step, len(c.path))
	for i, r := range c.path {
		steps[i] = topics.Step{From: string(r.From), Type: r.Type, To: string(r.To)}
	}
	text := c.what
	why := h.why(s.id, c)
	if why != "" {
		text += " " + why
	}
	// what and why are Text's two halves: a tile shows only what.
	args := map[string]any{"symptom": string(s.id), "cause": string(c.id), "what": c.what}
	if why != "" {
		args["why"] = why
	}
	if !c.at.IsZero() {
		args["at"] = c.at
	}
	return topics.Cause{Text: text, Resource: string(c.id), At: c.at, Rule: c.rule,
		Message: "cause." + c.rule, Args: args, Path: steps}
}

// why says how the symptom needs the cause, along the path: "Sonarr
// depends on qBittorrent." A container named for its app ends the phrase
// at the app ("qBittorrent's container … Sonarr depends on qBittorrent").
func (h *home) why(sym graph.RID, c candidate) string {
	path := c.path
	if r := h.res[c.id]; r != nil && r.Kind == "container" {
		prev := sym
		if len(path) > 1 {
			prev = h.stepTarget(path[len(path)-2])
		}
		if apps := h.runsHere[c.id]; len(apps) == 1 && prev == apps[0] {
			path = path[:len(path)-1]
		}
	}
	var b strings.Builder
	subject := sym
	for i, r := range path {
		next := h.stepTarget(r)
		if i == 0 {
			b.WriteString(h.phrase(subject, r, next))
		} else {
			b.WriteString(", which " + strings.TrimPrefix(h.phrase(subject, r, next), h.name(subject)+" "))
		}
		subject = next
	}
	if b.Len() == 0 {
		return ""
	}
	return b.String() + "."
}

// phrase is one step in words, subject first.
func (h *home) phrase(subject graph.RID, r graph.Relation, next graph.RID) string {
	s, n := h.name(subject), h.name(next)
	switch r.Type {
	case runsOn:
		return s + " runs on " + n
	case partOf:
		return s + " is part of " + n
	case dependsOn:
		return s + " depends on " + n
	case storesOn:
		return s + " stores on " + n
	case routesTo:
		return s + " routes to " + n
	case monitors:
		return s + " watches " + n
	case providesNetworkFor:
		return s + " gets its network from " + n
	}
	return s + " needs " + n
}

// attrTime reads a time attr: a time.Time, or an RFC 3339 string.
func attrTime(r *graph.Resource, key string) (time.Time, bool) {
	switch v := r.Attrs[key].(type) {
	case time.Time:
		return v, !v.IsZero()
	case string:
		t, err := time.Parse(time.RFC3339Nano, v)
		return t, err == nil && !t.IsZero()
	}
	return time.Time{}, false
}

func attrInt(r *graph.Resource, key string) (int, bool) {
	switch v := r.Attrs[key].(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case float64:
		return int(v), true
	}
	return 0, false
}
