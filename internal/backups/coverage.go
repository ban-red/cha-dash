package backups

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/msg"
)

// State is how a protectable resource stands.
type State string

const (
	// Fresh: covered, and its newest good backup is within the objective.
	Fresh State = "fresh"
	// Stale: covered, but its newest good backup is older than the
	// objective, or every run failed.
	Stale State = "stale"
	// Uncovered: nothing backs it up.
	Uncovered State = "uncovered"
	// Unknown: covered with no run seen yet, or Cha Dash can't read what
	// would cover it.
	Unknown State = "unknown"
)

// Where an item's objective comes from.
const (
	FromOverride = "override" // backup_objectives
	FromSchedule = "schedule" // the covering job's schedule
	FromDefault  = "default"  // no schedule to read: DefaultDaily
)

// BackedUpBy is the relation from a protectable resource to what covers
// it: Proxmox publishes guest → backup job, the correlator a spec's
// target's backup.covers (correlate.BackedUpBy).
const BackedUpBy graph.RelType = "backed_up_by"

// Cover is one thing that backs a resource up: a backup job (Proxmox), or a
// target a backup tool reports (a spec with backups.targets). The newest
// good run is on it, as the plan puts it "on the edge".
type Cover struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	Schedule string `json:"schedule,omitempty"`
	// LastOK is the newest good run that covered the resource.
	LastOK time.Time `json:"last_ok,omitzero"`
	// LastAt and LastStatus ("ok", "failed") are the newest run.
	LastAt     time.Time `json:"last_at,omitzero"`
	LastStatus string    `json:"last_status,omitempty"`
	LastError  string    `json:"last_error,omitempty"`
	// Disabled: the job is switched off, so it covers nothing now.
	Disabled bool `json:"disabled,omitempty"`
	// Target is where its backups land, a backup_target (a PBS datastore)
	// reached over stores_on, when the graph knows it.
	Target     string `json:"target,omitempty"`
	TargetName string `json:"target_name,omitempty"`
}

// Hint is why a resource's backups are stale, when something on the way
// isn't ok: the datastore they land on, or the job's failed run. A hint,
// never a verdict, like the cause engine's.
type Hint struct {
	Text     string         `json:"text"`
	Resource string         `json:"resource"`
	Message  string         `json:"message"`
	Args     map[string]any `json:"args,omitempty"`
	// Incident is the open problem the hint names, when it names one: the
	// datastore's ("mars is 95% full") or the job's.
	Incident string `json:"incident,omitempty"`
}

// Item is one protectable resource on the coverage map: the backups
// topic's {"items": {resource id: Item}}.
type Item struct {
	Resource string `json:"resource"`
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	State    State  `json:"state"`
	// By is what covers it, directly or through its guest.
	By []Cover `json:"by"`
	// Through is the guest whose backup a container inherits (decision 8),
	// when nothing backs the container up directly.
	Through     string `json:"through,omitempty"`
	ThroughName string `json:"through_name,omitempty"`
	// LastOK is the newest good backup by anything in By.
	LastOK time.Time `json:"last_ok,omitzero"`
	// MaxAgeMS is the objective, and Objective where it came from
	// (override, schedule or default).
	MaxAgeMS  int64  `json:"max_age_ms"`
	Objective string `json:"objective"`
	// Text says the state in a sentence; Message and Args name it.
	Text    string         `json:"text"`
	Message string         `json:"message"`
	Args    map[string]any `json:"args,omitempty"`
	Cause   *Hint          `json:"cause,omitempty"`
}

// Compute is the coverage map: every protectable primary in doc (guests;
// containers with volumes; Unraid shares and its flash drive), what covers
// it, and how fresh its newest good backup is against its objective
// (overrides by resource ID, else the covering schedule's). A pure
// function of its inputs.
func Compute(doc graph.Doc, overrides map[string]time.Duration, now time.Time) map[string]Item {
	return ComputeWith(doc, overrides, now, nil)
}

// ComputeWith is Compute with the inbox's incidents: an open problem on
// where a stale resource's backups land, or on the job that makes them,
// is named as the hint ("stale because PBS's datastore is full"). Hints
// (info), security findings and anything but an open problem never are.
func ComputeWith(doc graph.Doc, overrides map[string]time.Duration, now time.Time, incidents map[string]integration.Incident) map[string]Item {
	h := newHome(doc)
	h.problems = problemsOf(h, incidents)
	out := map[string]Item{}
	var containers []*graph.Resource
	for _, id := range slices.Sorted(maps.Keys(doc.Resources)) {
		r := doc.Resources[id]
		if h.canon(id) != id || !h.protectable(r) {
			continue
		}
		if r.Kind == "container" {
			containers = append(containers, r)
			continue
		}
		out[string(id)] = h.item(r, overrides, now)
	}
	// Containers last: one without a backup of its own inherits its guest's.
	for _, r := range containers {
		it := h.item(r, overrides, now)
		if it.State == Uncovered && len(it.By) == 0 {
			if g, ok := h.guestOf(r.ID); ok {
				if gi, ok := out[string(g)]; ok && gi.State != Uncovered {
					it = h.inherit(r, gi, overrides, now)
				}
			}
		}
		out[string(r.ID)] = it
	}
	return out
}

type home struct {
	doc     graph.Doc
	primary map[graph.RID]graph.RID
	members map[graph.RID][]graph.RID
	out     map[graph.RID][]*graph.Relation
	// problems are the open problems by their target's primary, oldest
	// first.
	problems map[graph.RID][]problem
}

// problem is an open problem incident a hint can name.
type problem struct {
	id     string
	title  string
	opened time.Time
}

// problemsOf keeps the open problems that can explain something: not a
// hint (info, advice rather than a fault, as the cause engine has it), not
// a security finding.
func problemsOf(h *home, incidents map[string]integration.Incident) map[graph.RID][]problem {
	out := map[graph.RID][]problem{}
	for _, id := range slices.Sorted(maps.Keys(incidents)) {
		inc := incidents[id]
		if inc.Status != integration.StatusOpen || inc.Class != integration.ClassProblem || inc.Category == integration.CategorySecurity ||
			inc.Severity == integration.SeverityInfo || inc.Target == "" || inc.Title == "" {
			continue
		}
		t := h.canon(graph.RID(inc.Target))
		out[t] = append(out[t], problem{id: id, title: strings.TrimSuffix(inc.Title, "."), opened: inc.OpenedAt})
	}
	for t := range out {
		slices.SortStableFunc(out[t], func(a, b problem) int { return a.opened.Compare(b.opened) })
	}
	return out
}

func newHome(doc graph.Doc) *home {
	h := &home{doc: doc, primary: map[graph.RID]graph.RID{}, members: map[graph.RID][]graph.RID{}, out: map[graph.RID][]*graph.Relation{}}
	for _, k := range slices.Sorted(maps.Keys(doc.Relations)) {
		rel := doc.Relations[k]
		if rel.Type == "same_as" {
			h.primary[rel.From] = rel.To
		}
		h.out[rel.From] = append(h.out[rel.From], rel)
	}
	for id := range doc.Resources {
		p := h.canon(id)
		h.members[p] = append(h.members[p], id)
	}
	for p := range h.members {
		slices.SortFunc(h.members[p], func(a, b graph.RID) int {
			// The primary first, then by ID.
			if (a == p) != (b == p) {
				if a == p {
					return -1
				}
				return 1
			}
			return cmp.Compare(a, b)
		})
	}
	return h
}

func (h *home) canon(id graph.RID) graph.RID {
	if p, ok := h.primary[id]; ok {
		return p
	}
	return id
}

func (h *home) res(id graph.RID) *graph.Resource { return h.doc.Resources[id] }

func (h *home) name(id graph.RID) string {
	if r := h.res(id); r != nil && r.Name != "" {
		return r.Name
	}
	return string(id)
}

// related are the primaries id's identity relates to over typ.
func (h *home) related(id graph.RID, typ graph.RelType) []graph.RID {
	var out []graph.RID
	for _, m := range h.members[h.canon(id)] {
		for _, rel := range h.out[m] {
			if rel.Type == typ && h.res(rel.To) != nil {
				if t := h.canon(rel.To); !slices.Contains(out, t) {
					out = append(out, t)
				}
			}
		}
	}
	return out
}

// protectable: a guest; a container with volumes (its mounts, or shares it
// stores on); an Unraid share; a boot flash drive.
func (h *home) protectable(r *graph.Resource) bool {
	switch r.Kind {
	case "vm", "lxc", "share":
		return true
	case "disk":
		return attrString(r, "role") == "boot"
	case "container":
		for _, m := range h.members[r.ID] {
			if mr := h.res(m); mr != nil && hasMounts(mr) {
				return true
			}
		}
		return len(h.related(r.ID, "stores_on")) > 0
	}
	return false
}

// hasMounts reads the Docker integration's mounts attr: a list of
// {type, source, target}.
func hasMounts(r *graph.Resource) bool {
	switch v := r.Attrs["mounts"].(type) {
	case []any:
		return len(v) > 0
	case []map[string]any:
		return len(v) > 0
	case []map[string]string:
		return len(v) > 0
	}
	return false
}

func (h *home) item(r *graph.Resource, overrides map[string]time.Duration, now time.Time) Item {
	it := Item{Resource: string(r.ID), Kind: string(r.Kind), Name: cmp.Or(r.Name, string(r.ID)), By: []Cover{}}
	for _, j := range h.related(r.ID, BackedUpBy) {
		it.By = append(it.By, h.cover(r.ID, j))
	}
	h.judge(&it, overrides, now)
	if it.State == Uncovered && (r.Kind == "vm" || r.Kind == "lxc") && h.jobsUnread(r.ID) {
		it.State = Unknown
		it.say(msg.New("backups.state_unread"))
	}
	return it
}

// jobsUnread: Proxmox sets backup.covered on its guests only when it could
// read the backup jobs; without it, "not covered" may be a token that can't
// see them.
func (h *home) jobsUnread(id graph.RID) bool {
	for _, m := range h.members[id] {
		r := h.res(m)
		if r == nil || !strings.HasPrefix(string(m), "proxmox:") {
			continue
		}
		if _, ok := r.Attrs["backup.covered"]; !ok {
			return true
		}
	}
	return false
}

// cover reads one coverer: its schedule, the newest good run that covered
// res (the guest's own backup.last_ok beats the job's), and where it lands.
func (h *home) cover(res, j graph.RID) Cover {
	jr := h.res(j)
	c := Cover{ID: string(j), Kind: string(jr.Kind), Name: h.name(j)}
	c.Schedule = cmp.Or(attrString(jr, "schedule"), attrString(jr, "backup.schedule"))
	if on, ok := jr.Attrs["enabled"].(bool); ok && !on {
		c.Disabled = true
	}
	c.LastOK = latest(attrTime(jr, "last_ok"), attrTime(jr, "backup.last_ok"))
	c.LastAt = latest(attrTime(jr, "last_run.end"), attrTime(jr, "backup.last_at"))
	c.LastStatus = cmp.Or(attrString(jr, "last_run.status"), attrString(jr, "backup.last_status"))
	c.LastError = cmp.Or(attrString(jr, "last_run.error"), attrString(jr, "backup.last_error"))
	// A guest's own record, from the source that published the job.
	src := sourceOf(j)
	for _, m := range h.members[h.canon(res)] {
		mr := h.res(m)
		if mr == nil || sourceOf(m) != src {
			continue
		}
		c.LastOK = latest(c.LastOK, attrTime(mr, "backup.last_ok"))
		if t := attrTime(mr, "backup.last_at"); !t.IsZero() && !t.Before(c.LastAt) {
			c.LastAt, c.LastStatus = t, cmp.Or(attrString(mr, "backup.last_status"), c.LastStatus)
			c.LastError = attrString(mr, "backup.last_error")
		}
	}
	if c.LastStatus == "ok" {
		c.LastError = ""
	}
	if t, ok := h.landsOn(j); ok {
		c.Target, c.TargetName = string(t), h.name(t)
	}
	return c
}

// landsOn follows stores_on from a job, breadth first and at most three
// relations, to the first backup target (job → storage → PBS datastore).
func (h *home) landsOn(j graph.RID) (graph.RID, bool) {
	if r := h.res(j); r != nil && r.Kind == "backup_target" {
		return "", false
	}
	seen := map[graph.RID]bool{j: true}
	front := []graph.RID{j}
	for range 3 {
		var next []graph.RID
		for _, id := range front {
			for _, t := range h.related(id, "stores_on") {
				if seen[t] {
					continue
				}
				seen[t] = true
				if h.res(t).Kind == "backup_target" {
					return t, true
				}
				next = append(next, t)
			}
		}
		front = next
	}
	return "", false
}

// judge sets the item's objective, newest good backup, state and hint.
func (h *home) judge(it *Item, overrides map[string]time.Duration, now time.Time) {
	var active []Cover
	for _, c := range it.By {
		if !c.Disabled {
			active = append(active, c)
		}
	}
	objective, from := DefaultDaily, FromDefault
	for _, c := range active {
		if d, ok := ScheduleObjective(c.Schedule); ok && (from == FromDefault || d < objective) {
			objective, from = d, FromSchedule
		}
	}
	if d, ok := overrides[it.Resource]; ok && d > 0 {
		objective, from = d, FromOverride
	}
	it.MaxAgeMS, it.Objective = objective.Milliseconds(), from
	failed := false
	for _, c := range active {
		it.LastOK = latest(it.LastOK, c.LastOK)
		failed = failed || c.LastStatus == "failed"
	}
	within := words(objective)
	switch {
	case len(active) == 0:
		it.State = Uncovered
		if len(it.By) > 0 {
			it.say(msg.New("backups.state_disabled"))
		} else {
			it.say(msg.New("backups.state_uncovered"))
		}
	case it.LastOK.IsZero() && !failed:
		it.State = Unknown
		it.say(msg.New("backups.state_never"))
	case it.LastOK.IsZero():
		it.State = Stale
		it.say(msg.New("backups.state_failing"))
	case now.Sub(it.LastOK) > objective:
		it.State = Stale
		it.say(msg.New("backups.state_stale", "objective", within))
	default:
		it.State = Fresh
		it.say(msg.New("backups.state_fresh", "objective", within))
	}
	if it.State == Stale || failed {
		it.Cause = h.hint(active)
	}
}

// hint says why backups are stale, conservatively: an open problem on a
// datastore they land on, else one that isn't ok itself, else a covering
// job whose last run failed with a reason, else an open problem on that
// job. Nothing that is fine is blamed.
func (h *home) hint(cs []Cover) *Hint {
	for _, c := range cs {
		if c.Target == "" {
			continue
		}
		if ps := h.problems[h.canon(graph.RID(c.Target))]; len(ps) > 0 {
			m := msg.New("backups.cause_target_incident", "target", c.TargetName, "problem", ps[0].title)
			return &Hint{Text: m.Text(), Resource: c.Target, Message: m.ID, Args: m.Args, Incident: ps[0].id}
		}
	}
	for _, c := range cs {
		if c.Target == "" {
			continue
		}
		t := h.res(graph.RID(c.Target))
		if t == nil || (t.Health != graph.HealthDown && t.Health != graph.HealthAttention) {
			continue
		}
		reason := attrString(t, "health.reason")
		var m msg.M
		if reason != "" {
			m = msg.New("backups.cause_target", "target", c.TargetName, "reason", reason)
		} else {
			m = msg.New("backups.cause_target_down", "target", c.TargetName)
		}
		return &Hint{Text: m.Text(), Resource: c.Target, Message: m.ID, Args: m.Args}
	}
	for _, c := range cs {
		if c.LastStatus == "failed" && c.LastError != "" {
			m := msg.New("backups.cause_failed", "job", c.Name, "error", c.LastError)
			return &Hint{Text: m.Text(), Resource: c.ID, Message: m.ID, Args: m.Args}
		}
	}
	for _, c := range cs {
		if ps := h.problems[h.canon(graph.RID(c.ID))]; len(ps) > 0 {
			m := msg.New("backups.cause_job_incident", "job", c.Name, "problem", ps[0].title)
			return &Hint{Text: m.Text(), Resource: c.ID, Message: m.ID, Args: m.Args, Incident: ps[0].id}
		}
	}
	return nil
}

// guestOf is the PVE guest a container runs in, through its engine
// (container runs_on engine runs_on host or guest) and the guest's
// same_as identity: the Docker host and the guest are one machine.
func (h *home) guestOf(c graph.RID) (graph.RID, bool) {
	var places []graph.RID
	for _, t := range h.related(c, "runs_on") {
		switch h.res(t).Kind {
		case "engine":
			places = append(places, h.related(t, "runs_on")...)
		default:
			places = append(places, t)
		}
	}
	for _, p := range places {
		for _, m := range h.members[h.canon(p)] {
			if k := h.res(m).Kind; k == "vm" || k == "lxc" {
				return h.canon(m), true
			}
		}
	}
	return "", false
}

// inherit gives a container its guest's coverage (decision 8): the guest's
// covers and newest backup, the container's own objective if it has one.
func (h *home) inherit(r *graph.Resource, g Item, overrides map[string]time.Duration, now time.Time) Item {
	it := Item{Resource: string(r.ID), Kind: string(r.Kind), Name: cmp.Or(r.Name, string(r.ID)), By: g.By,
		Through: g.Resource, ThroughName: g.Name}
	h.judge(&it, overrides, now)
	if it.State == Unknown && g.State == Unknown {
		it.Message, it.Args, it.Text = g.Message, g.Args, g.Text
	}
	m := msg.New("backups.through", "guest", g.Name)
	it.Text += " " + m.Text()
	return it
}

func (it *Item) say(m msg.M) {
	it.Text, it.Message, it.Args = m.Text(), m.ID, m.Args
}

// words is an objective in words: "26 hours", "8 days".
func words(d time.Duration) string {
	if d < 48*time.Hour {
		h := int(d.Round(time.Hour) / time.Hour)
		if h == 1 {
			return "1 hour"
		}
		return fmt.Sprintf("%d hours", h)
	}
	days := int(d.Round(24*time.Hour) / (24 * time.Hour))
	return fmt.Sprintf("%d days", days)
}

func sourceOf(id graph.RID) string {
	s, rest, _ := strings.Cut(string(id), ":")
	inst, _, _ := strings.Cut(rest, "/")
	return s + ":" + inst
}

func latest(ts ...time.Time) time.Time {
	var out time.Time
	for _, t := range ts {
		if t.After(out) {
			out = t
		}
	}
	return out
}

func attrString(r *graph.Resource, key string) string {
	s, _ := r.Attrs[key].(string)
	return s
}

func attrTime(r *graph.Resource, key string) time.Time {
	switch v := r.Attrs[key].(type) {
	case time.Time:
		return v.UTC()
	case string:
		if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}
