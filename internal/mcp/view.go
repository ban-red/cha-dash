package mcp

// What an agent token sees, and how it's shaped.
//
// The graph is the one the token's user sees in the browser (the role's
// view: household gets topics.HouseholdGraph), narrowed by the token's read
// scopes (graph.read:host=…,tag=…,lens=…), with attributes whose names look
// like credentials left out as a second fence behind the integrations'
// own redaction.
//
// Text that comes from the lab (names, states, labels, attributes,
// incident titles and summaries, cause hints, plan labels, log lines, a
// person's deny reason) is never a bare string: it's an Untrusted value,
// {"untrusted": true, "value": …}, so a client and its model can tell data
// from instructions. IDs, kinds, health, tiers and states the hub defines
// are plain. Nothing from the lab reaches a tool's description or the
// server's instructions, which are fixed text in internal/msg.

import (
	"context"
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"time"

	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/authz"
	"dash.cha.sh/internal/boards"
	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/topics"
)

// Untrusted is text from the lab, returned as data: whatever it says, it
// isn't an instruction.
type Untrusted struct {
	Untrusted bool `json:"untrusted"`
	Value     any  `json:"value"`
}

// untrusted wraps v, or returns nil for an empty value.
func untrusted(v any) *Untrusted {
	switch x := v.(type) {
	case nil:
		return nil
	case string:
		if x == "" {
			return nil
		}
	case map[string]string:
		if len(x) == 0 {
			return nil
		}
	case map[string]any:
		if len(x) == 0 {
			return nil
		}
	case []string:
		if len(x) == 0 {
			return nil
		}
	}
	return &Untrusted{Untrusted: true, Value: v}
}

// secretKey matches attribute and label names that may hold a credential.
// The integrations never put one in the graph; this is a second fence.
var secretKey = regexp.MustCompile(`(?i)(secret|passw|token|api[_-]?key|apikey|private[_-]?key|credential|cookie|bearer|^env$|^environment$)`)

// scrub drops keys that look like credentials, at every depth.
func scrub(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			if secretKey.MatchString(k) {
				continue
			}
			out[k] = scrub(val)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = scrub(val)
		}
		return out
	}
	return v
}

func scrubLabels(l map[string]string) map[string]string {
	if len(l) == 0 {
		return nil
	}
	out := make(map[string]string, len(l))
	for k, v := range l {
		if !secretKey.MatchString(k) {
			out[k] = v
		}
	}
	return out
}

// Resource is a graph resource as an agent gets it.
type Resource struct {
	ID     string     `json:"id"`
	Kind   string     `json:"kind"`
	Health string     `json:"health,omitempty"`
	Source string     `json:"source,omitempty"`
	Name   *Untrusted `json:"name,omitempty"`
	State  *Untrusted `json:"state,omitempty"`
	Labels *Untrusted `json:"labels,omitempty"`
	Attrs  *Untrusted `json:"attrs,omitempty"`
}

// Relation is a graph relation: IDs and a type, nothing else.
type Relation struct {
	From string `json:"from"`
	Type string `json:"type"`
	To   string `json:"to"`
}

func resourceOut(r *graph.Resource, full bool) Resource {
	out := Resource{ID: string(r.ID), Kind: string(r.Kind), Health: string(r.Health), Source: r.Source,
		Name: untrusted(r.Name), State: untrusted(r.State)}
	if full {
		if l := scrubLabels(r.Labels); len(l) > 0 {
			out.Labels = untrusted(l)
		}
		if len(r.Attrs) > 0 {
			if a, _ := scrub(r.Attrs).(map[string]any); len(a) > 0 {
				out.Attrs = untrusted(a)
			}
		}
	}
	return out
}

// viewer is one MCP request's agent: its role and read scopes.
type viewer struct {
	actor  action.Actor
	role   string
	scopes []string
}

func (v viewer) graphReads() []authz.Scope { return authz.AgentReads(v.scopes, authz.ScopeGraphRead) }
func (v viewer) inboxReads() []authz.Scope { return authz.AgentReads(v.scopes, authz.ScopeInboxRead) }
func (v viewer) logs() bool                { return authz.HasScope(v.scopes, authz.ScopeLogsRead) }

// view is the graph one viewer may read, with what narrowing needs.
type view struct {
	version uint64
	doc     graph.Doc
	reads   []authz.Scope
	facts   *factIndex
	lenses  map[string]*boards.Scope
}

// graphView reads the graph as v's role sees it, ready to narrow by reads
// (graph.read or inbox.read scopes). Nil reads: nothing is visible.
func (s *Server) graphView(ctx context.Context, v viewer, reads []authz.Scope) *view {
	ver, doc := s.topics.Graph.Snapshot(nil)
	if !authz.SeesWholeGraph(v.role) {
		doc = topics.HouseholdGraph(doc)
	}
	gv := &view{version: ver, doc: doc, reads: reads, facts: newFactIndex(doc), lenses: map[string]*boards.Scope{}}
	for _, r := range reads {
		if l := r.Quals["lens"]; l != "" {
			if _, done := gv.lenses[l]; done {
				continue
			}
			gv.lenses[l] = nil
			if s.boards != nil {
				if sc, ok, err := s.boards.BoardScope(ctx, l); err == nil && ok {
					gv.lenses[l] = &sc
				}
			}
		}
	}
	return gv
}

// sees reports whether the view's reads admit resource id: any read scope
// without qualifiers admits all; one with qualifiers admits what matches
// every one of them.
func (gv *view) sees(id string) bool {
	r, ok := gv.doc.Resources[graph.RID(id)]
	if !ok {
		return false
	}
	for _, sc := range gv.reads {
		if gv.matches(sc, r) {
			return true
		}
	}
	return false
}

func (gv *view) matches(sc authz.Scope, r *graph.Resource) bool {
	for k, want := range sc.Quals {
		switch k {
		case "host":
			if !slices.Contains(gv.facts.hosts(string(r.ID)), want) {
				return false
			}
		case "tag":
			if !slices.Contains(gv.facts.tags(string(r.ID)), want) {
				return false
			}
		case "lens":
			l := gv.lenses[want]
			if l == nil || !l.Shows(string(r.ID), string(r.Kind)) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// resources are the visible resources, by ID.
func (gv *view) resources() []*graph.Resource {
	out := make([]*graph.Resource, 0, len(gv.doc.Resources))
	for id, r := range gv.doc.Resources {
		if gv.sees(string(id)) {
			out = append(out, r)
		}
	}
	slices.SortFunc(out, func(a, b *graph.Resource) int { return strings.Compare(string(a.ID), string(b.ID)) })
	return out
}

// relations are the visible relations: both ends visible, by key.
func (gv *view) relations(about string) []Relation {
	keys := make([]string, 0, len(gv.doc.Relations))
	for k, rel := range gv.doc.Relations {
		if about != "" && string(rel.From) != about && string(rel.To) != about {
			continue
		}
		if gv.sees(string(rel.From)) && gv.sees(string(rel.To)) {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	out := make([]Relation, 0, len(keys))
	for _, k := range keys {
		rel := gv.doc.Relations[k]
		out = append(out, Relation{From: string(rel.From), Type: string(rel.Type), To: string(rel.To)})
	}
	return out
}

// ── facts: hosts and tags ────────────────────────────────────────────────

// factIndex answers what a resource runs on and what it's tagged, walking
// runs_on and part_of up from it.
type factIndex struct {
	doc graph.Doc
	up  map[graph.RID][]graph.RID
}

func newFactIndex(doc graph.Doc) *factIndex {
	f := &factIndex{doc: doc, up: map[graph.RID][]graph.RID{}}
	for _, rel := range doc.Relations {
		if rel.Type == "runs_on" || rel.Type == "part_of" {
			f.up[rel.From] = append(f.up[rel.From], rel.To)
		}
	}
	return f
}

// ancestors are id and everything it runs on or is part of, transitively.
func (f *factIndex) ancestors(id string) []*graph.Resource {
	var out []*graph.Resource
	seen := map[graph.RID]bool{}
	queue := []graph.RID{graph.RID(id)}
	for len(queue) > 0 && len(seen) < 64 {
		cur := queue[0]
		queue = queue[1:]
		if seen[cur] {
			continue
		}
		seen[cur] = true
		if r, ok := f.doc.Resources[cur]; ok {
			out = append(out, r)
		}
		queue = append(queue, f.up[cur]...)
	}
	return out
}

// hosts are the IDs and names of id and what it runs on: a host=jupiter
// qualifier matches a container on jupiter, and jupiter itself.
func (f *factIndex) hosts(id string) []string {
	var out []string
	for _, r := range f.ancestors(id) {
		out = append(out, string(r.ID))
		if r.Name != "" {
			out = append(out, r.Name)
		}
	}
	return out
}

// tags are id's tags and its ancestors': attrs.tags (Proxmox's) and the
// chadash.tags label, comma-separated.
func (f *factIndex) tags(id string) []string {
	var out []string
	for _, r := range f.ancestors(id) {
		switch t := r.Attrs["tags"].(type) {
		case []string:
			out = append(out, t...)
		case []any:
			for _, x := range t {
				if s, ok := x.(string); ok {
					out = append(out, s)
				}
			}
		}
		for _, t := range strings.Split(r.Labels["chadash.tags"], ",") {
			if t = strings.TrimSpace(t); t != "" {
				out = append(out, t)
			}
		}
	}
	return out
}

// Facts reads a target's hosts and tags from g, for the policy's host= and
// tag= qualifiers on propose scopes (authz.WithTargetFacts).
func Facts(g *graph.Graph) func(context.Context, string) authz.TargetFacts {
	return func(_ context.Context, target string) authz.TargetFacts {
		if g == nil {
			return authz.TargetFacts{}
		}
		_, doc := g.Snapshot(nil)
		f := newFactIndex(doc)
		return authz.TargetFacts{Hosts: f.hosts(target), Tags: f.tags(target)}
	}
}

// ── incidents, causes, plans, proposals ─────────────────────────────────

// Incident is an open incident as an agent gets it.
type Incident struct {
	ID           string     `json:"id"`
	Class        string     `json:"class"`
	Severity     string     `json:"severity"`
	Status       string     `json:"status"`
	Category     string     `json:"category,omitempty"`
	Target       string     `json:"target"`
	OpenedAt     string     `json:"opened_at,omitempty"`
	Title        *Untrusted `json:"title,omitempty"`
	Summary      *Untrusted `json:"summary,omitempty"`
	ResolvesWhen *Untrusted `json:"resolves_when,omitempty"`
	BlastRadius  []string   `json:"blast_radius,omitempty"`
}

// incidents are the open incidents v may read: the inbox as its role sees
// it (InboxView), without approvals (other agents' and tokens' plans),
// narrowed to targets its inbox.read scopes admit.
func (s *Server) incidents(ctx context.Context, v viewer) []Incident {
	reads := v.inboxReads()
	if len(reads) == 0 || s.topics.Inbox == nil {
		return []Incident{}
	}
	gv := s.graphView(ctx, v, reads)
	_, doc := s.topics.Inbox.Snapshot()
	if s.inboxView != nil {
		doc = topics.ProjectInbox(doc, s.inboxView(ctx, v.role))
	}
	items, _ := doc[topics.Incidents].(map[string]json.RawMessage)
	out := []Incident{}
	for _, raw := range items {
		var in struct {
			ID, Class, Severity, Status, Category, Target, Title, Summary string
			OpenedAt                                                      string `json:"opened_at"`
			ResolvesWhen                                                  string `json:"resolves_when"`
			BlastRadius                                                   []struct {
				Resource string `json:"resource"`
			} `json:"blast_radius"`
		}
		if json.Unmarshal(raw, &in) != nil || in.Status != "open" || in.Class == action.ApprovalClass {
			continue
		}
		if !narrowedOK(gv, reads, in.Target) {
			continue
		}
		it := Incident{ID: in.ID, Class: in.Class, Severity: in.Severity, Status: in.Status, Category: in.Category, Target: in.Target,
			OpenedAt: in.OpenedAt, Title: untrusted(in.Title), Summary: untrusted(in.Summary), ResolvesWhen: untrusted(in.ResolvesWhen)}
		for _, b := range in.BlastRadius {
			if gv.sees(b.Resource) || unqualified(reads) {
				it.BlastRadius = append(it.BlastRadius, b.Resource)
			}
		}
		out = append(out, it)
	}
	slices.SortFunc(out, func(a, b Incident) int { return strings.Compare(a.ID, b.ID) })
	return out
}

// unqualified reports whether one of reads admits everything.
func unqualified(reads []authz.Scope) bool {
	return slices.ContainsFunc(reads, func(sc authz.Scope) bool { return len(sc.Quals) == 0 })
}

// narrowedOK admits an incident's target: everything with an unqualified
// read, else a target the narrowing admits.
func narrowedOK(gv *view, reads []authz.Scope, target string) bool {
	return unqualified(reads) || gv.sees(target)
}

// Cause is a cause hint as an agent gets it.
type Cause struct {
	Resource string     `json:"resource"`
	Rule     string     `json:"rule"`
	At       *time.Time `json:"at,omitempty"`
	Text     *Untrusted `json:"text,omitempty"`
	Path     []Relation `json:"path,omitempty"`
}

func causesOut(cs []topics.Cause) []Cause {
	out := make([]Cause, 0, len(cs))
	for _, c := range cs {
		x := Cause{Resource: c.Resource, Rule: c.Rule, Text: untrusted(c.Text)}
		if !c.At.IsZero() {
			at := c.At
			x.At = &at
		}
		for _, p := range c.Path {
			x.Path = append(x.Path, Relation{From: p.From, Type: string(p.Type), To: p.To})
		}
		out = append(out, x)
	}
	return out
}

// Plan is the engine's plan as an agent gets it.
type Plan struct {
	ID        string          `json:"id"`
	Hash      string          `json:"hash"`
	Action    string          `json:"action"`
	Target    string          `json:"target"`
	Tier      string          `json:"tier"`
	Params    json.RawMessage `json:"params,omitempty"`
	Label     *Untrusted      `json:"label,omitempty"`
	Steps     *Untrusted      `json:"steps,omitempty"`
	Impact    []action.Impact `json:"impact"`
	ExpiresAt time.Time       `json:"expires_at"`
	// WaitsFor says what happens to a proposal of it: a person approves.
	WaitsFor string `json:"waits_for"`
}

func planOut(p action.Plan) Plan {
	steps := make([]string, 0, len(p.Steps))
	for _, st := range p.Steps {
		steps = append(steps, st.Label)
	}
	out := Plan{ID: p.ID, Hash: p.Hash, Action: p.Action, Target: p.Target, Tier: p.Tier.String(), Params: p.Params,
		Label: untrusted(p.Label), Steps: untrusted(steps), Impact: p.Impact, ExpiresAt: p.ExpiresAt, WaitsFor: "a person's approval"}
	if out.Impact == nil {
		out.Impact = []action.Impact{}
	}
	if p.Tier >= action.T2 {
		out.WaitsFor = "a person's approval, with a passkey or password check"
	}
	return out
}

// Proposal is an agent's proposal, as get_proposal and propose return it.
type Proposal struct {
	ID          string     `json:"id"`
	State       string     `json:"state"`
	Action      string     `json:"action"`
	Target      string     `json:"target"`
	Tier        string     `json:"tier"`
	Hash        string     `json:"plan_hash"`
	RequestedAt time.Time  `json:"requested_at"`
	ExpiresAt   time.Time  `json:"expires_at"`
	DecidedAt   *time.Time `json:"decided_at,omitempty"`
	// Reason is why it was denied (a person's words) or expired.
	Reason *Untrusted `json:"reason,omitempty"`
	Task   *TaskOut   `json:"task,omitempty"`
}

// TaskOut is the task an approved proposal started.
type TaskOut struct {
	ID         string     `json:"id"`
	State      string     `json:"state"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	Error      *Untrusted `json:"error,omitempty"`
	// Log is the task's log tail, with logs.read only.
	Log *Untrusted `json:"log,omitempty"`
}

func proposalOut(a action.Approval) Proposal {
	return Proposal{ID: a.ID, State: a.State, Action: a.Plan.Action, Target: a.Plan.Target, Tier: a.Plan.Tier.String(), Hash: a.Plan.Hash,
		RequestedAt: a.RequestedAt, ExpiresAt: a.ExpiresAt, DecidedAt: a.DecidedAt, Reason: untrusted(a.Reason)}
}

func taskOut(t action.Task, logs bool) *TaskOut {
	out := &TaskOut{ID: t.ID, State: t.State, FinishedAt: t.FinishedAt}
	if t.Error != nil {
		out.Error = untrusted(t.Error.Code + ": " + t.Error.Message)
	}
	if logs && len(t.Log) > 0 {
		out.Log = untrusted(slices.Clone(t.Log))
	}
	return out
}
