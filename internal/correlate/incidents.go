package correlate

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"

	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/integration"
)

// note is what the correlator adds to an incident another instance raised:
// causes in front of the instance's own, impacts after its blast radius.
// The incident is never duplicated; it gains the links one instance can't
// see.
type note struct {
	Causes  []integration.Cause  `json:"causes,omitempty"`
	Impacts []integration.Impact `json:"impacts,omitempty"`
}

func (n note) empty() bool { return len(n.Causes) == 0 && len(n.Impacts) == 0 }

// view is the graph as the correlator's links complete it, for walking
// across sources.
type view struct {
	res map[graph.RID]*graph.Resource
	out map[graph.RID][]graph.Relation
	in  map[graph.RID][]graph.Relation
}

func newView(doc graph.Doc, links []Link) *view {
	v := &view{res: doc.Resources, out: map[graph.RID][]graph.Relation{}, in: map[graph.RID][]graph.Relation{}}
	add := func(rel graph.Relation) {
		v.out[rel.From] = append(v.out[rel.From], rel)
		v.in[rel.To] = append(v.in[rel.To], rel)
	}
	for _, rel := range doc.Relations {
		if rel.Source != Source {
			add(*rel)
		}
	}
	for _, l := range links {
		add(l.relation())
	}
	return v
}

func (v *view) to(id graph.RID, typ graph.RelType, kind graph.Kind) []graph.RID {
	var out []graph.RID
	for _, rel := range v.out[id] {
		if r := v.res[rel.To]; rel.Type == typ && r != nil && r.Kind == kind && !slices.Contains(out, rel.To) {
			out = append(out, rel.To)
		}
	}
	slices.Sort(out)
	return out
}

func (v *view) from(id graph.RID, typ graph.RelType, kind graph.Kind) []graph.RID {
	var out []graph.RID
	for _, rel := range v.in[id] {
		if r := v.res[rel.From]; rel.Type == typ && r != nil && r.Kind == kind && !slices.Contains(out, rel.From) {
			out = append(out, rel.From)
		}
	}
	slices.Sort(out)
	return out
}

// canon follows same_as to the primary.
func (v *view) canon(id graph.RID) graph.RID {
	for _, rel := range v.out[id] {
		if rel.Type == SameAs {
			return rel.To
		}
	}
	return id
}

func (v *view) name(id graph.RID) string {
	if r := v.res[id]; r != nil && r.Name != "" {
		return r.Name
	}
	return string(id)
}

// instanceName is the name of the integration instance that raised an
// incident ("<instance id>.<local id>").
func (v *view) instanceName(incident string) string {
	inst, _, _ := strings.Cut(incident, ".")
	if r := v.res[graph.RID("integration:"+inst)]; r != nil && r.Name != "" {
		return r.Name
	}
	return inst
}

// roots are what an incident's trouble comes down to, when that's a backup
// target: the PBS datastore (or PBS) that a backup job, guest or storage
// writes to, or that the incident is about. A failure to reach an endpoint
// no integration reads is a root too, by address ("endpoint:<host:port>").
func (v *view) roots(in integration.Incident) []string {
	var out []string
	add := func(s string) {
		if s != "" && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	seeds := []graph.RID{graph.RID(in.Target)}
	for _, c := range in.Causes {
		if c.Resource != "" {
			seeds = append(seeds, graph.RID(c.Resource))
		}
	}
	var storages []graph.RID
	for _, s := range seeds {
		r := v.res[s]
		if r == nil {
			continue
		}
		switch r.Kind {
		case "backup_target":
			add(string(v.canon(s)))
			// A PBS incident about datastores sharing one filesystem names them all.
			for _, name := range factStrings(in.Facts, "stores") {
				for _, app := range v.to(s, "part_of", "app") {
					for _, d := range v.from(app, "part_of", "backup_target") {
						if str(v.res[d], "store") == name {
							add(string(v.canon(d)))
						}
					}
				}
			}
		case "app":
			if str(r, "type") == "pbs" {
				add(string(v.canon(s)))
			}
		case "storage":
			storages = append(storages, s)
		case "backup_job":
			storages = append(storages, v.to(s, "stores_on", "storage")...)
		case "vm", "lxc":
			for _, j := range v.to(s, "backed_up_by", "backup_job") {
				storages = append(storages, v.to(j, "stores_on", "storage")...)
			}
		}
	}
	found := len(out)
	for _, s := range storages {
		for _, d := range v.to(s, StoresOn, "backup_target") {
			add(string(v.canon(d)))
		}
	}
	if len(out) > found || len(storages) == 0 {
		return out
	}
	ep, _ := in.Facts["endpoint"].(string)
	addr, ok := parseAddr(ep)
	if !ok {
		return out
	}
	// The PBS answering at the endpoint, and its datastore the storage names.
	for _, a := range v.pbsAt(addr) {
		matched := false
		for _, s := range storages {
			for _, d := range v.from(a, "part_of", "backup_target") {
				if ds := str(v.res[s], "pbs.datastore"); ds != "" && ds == str(v.res[d], "store") {
					add(string(v.canon(d)))
					matched = true
				}
			}
		}
		if !matched {
			add(string(a))
		}
	}
	if len(out) == found {
		add("endpoint:" + addr)
	}
	return out
}

// pbsAt are the PBS apps one of whose URLs is at addr.
func (v *view) pbsAt(addr string) []graph.RID {
	var out []graph.RID
	for id, r := range v.res {
		if r.Kind != "app" || str(r, "type") != "pbs" || v.canon(id) != id {
			continue
		}
		for _, s := range append([]string{str(r, "url")}, links(r)...) {
			if u, ok := parseURL(s); ok && u.addr() == addr {
				out = append(out, id)
				break
			}
		}
	}
	slices.Sort(out)
	return out
}

func factStrings(facts map[string]any, key string) []string {
	var out []string
	switch v := facts[key].(type) {
	case []any:
		for _, x := range v {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
	case []string:
		out = v
	}
	return out
}

// explain links incidents that share a root across instances. A PVE backup
// failure whose job writes to a PBS datastore gains a cause pointing at the
// datastore when PBS has a problem of its own there (an incident, the
// datastore's health, or Cha Dash failing to read that PBS too), and a
// cause per other instance whose backups failed against the same root. The
// PBS incident gains the backup jobs that write there in its blast radius.
func explain(doc graph.Doc, links []Link, incidents map[string]integration.Incident) map[string]note {
	v := newView(doc, links)
	var entries []entry
	ids := make([]string, 0, len(incidents))
	for id := range incidents {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		in := incidents[id]
		if in.Class != integration.ClassProblem || in.Status != integration.StatusOpen {
			continue
		}
		rs := v.roots(in)
		if len(rs) == 0 {
			continue
		}
		entries = append(entries, entry{id: id, in: in, roots: rs, servers: v.servers(rs),
			atRoot: slices.Contains(rs, string(v.canon(graph.RID(in.Target))))})
	}
	byRoot, byServer := map[string][]int{}, map[string][]int{}
	for i, e := range entries {
		for _, r := range e.roots {
			byRoot[r] = append(byRoot[r], i)
		}
		for _, s := range e.servers {
			byServer[s] = append(byServer[s], i)
		}
	}
	notes := map[string]note{}
	for _, e := range entries {
		var n note
		if e.atRoot {
			// Who writes here: the backup jobs whose storage is this datastore.
			have := map[string]bool{}
			for _, imp := range e.in.BlastRadius {
				have[imp.Resource] = true
			}
			for _, r := range e.roots {
				for _, s := range v.from(graph.RID(r), StoresOn, "storage") {
					for _, j := range v.from(s, "stores_on", "backup_job") {
						if !have[string(j)] {
							have[string(j)] = true
							n.Impacts = append(n.Impacts, integration.Impact{Resource: string(j), Via: "stores_on", Depth: 2})
						}
					}
				}
			}
			if !n.empty() {
				notes[e.id] = n
			}
			continue
		}
		root := e.roots[0]
		rid := graph.RID(root)
		inst, _, _ := strings.Cut(e.id, ".")
		if r := v.res[rid]; r != nil {
			what := describeTarget(v, rid)
			var pbs []string
			for _, i := range byRoot[root] {
				if entries[i].atRoot {
					pbs = append(pbs, entries[i].id)
				}
			}
			switch {
			case len(pbs) > 0:
				n.Causes = append(n.Causes, integration.Cause{Resource: root, At: incidents[pbs[0]].OpenedAt,
					Text: fmt.Sprintf("It writes to %s, which has a problem of its own: %s.", what, strings.TrimSuffix(incidents[pbs[0]].Title, "."))})
			case r.Health == graph.HealthDown || r.Health == graph.HealthAttention || r.Health == graph.HealthUnknown:
				n.Causes = append(n.Causes, integration.Cause{Resource: root, At: since(r),
					Text: fmt.Sprintf("It writes to %s, which is %s.", what, healthWords(r))})
			default:
				if st, bad := v.sourceTrouble(r); bad {
					n.Causes = append(n.Causes, integration.Cause{Resource: root,
						Text: fmt.Sprintf("It writes to %s, and Cha Dash can't read that PBS either (%s).", what, st)})
				}
			}
		}
		// The same server failing other instances' backups.
		var peers []string
		for _, s := range e.servers {
			for _, i := range byServer[s] {
				p := entries[i]
				pinst, _, _ := strings.Cut(p.id, ".")
				if p.atRoot || pinst == inst || slices.Contains(peers, p.id) {
					continue
				}
				peers = append(peers, p.id)
			}
		}
		for _, p := range peers[:min(3, len(peers))] {
			pin := incidents[p]
			text := fmt.Sprintf("Backups on %s to the same PBS failed too: %s.", v.instanceName(p), strings.TrimSuffix(pin.Title, "."))
			if ep, ok := strings.CutPrefix(root, "endpoint:"); ok {
				text = fmt.Sprintf("Backups on %s can't reach %s either: %s.", v.instanceName(p), ep, strings.TrimSuffix(pin.Title, "."))
			}
			n.Causes = append(n.Causes, integration.Cause{Text: text, Resource: pin.Target, At: pin.OpenedAt})
		}
		if !n.empty() {
			notes[e.id] = n
		}
	}
	return notes
}

// entry is an open problem with the roots it comes down to.
type entry struct {
	id      string
	in      integration.Incident
	roots   []string
	servers []string // the PBS (or endpoint) behind each root
	atRoot  bool     // the incident is about the root itself (PBS raised it)
}

// servers maps roots to the server behind them: a datastore's PBS, a PBS
// itself, an endpoint.
func (v *view) servers(roots []string) []string {
	var out []string
	for _, r := range roots {
		s := r
		if res := v.res[graph.RID(r)]; res != nil && res.Kind == "backup_target" {
			if apps := v.to(graph.RID(r), "part_of", "app"); len(apps) > 0 {
				s = string(v.canon(apps[0]))
			}
		}
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

func describeTarget(v *view, id graph.RID) string {
	r := v.res[id]
	if r.Kind == "backup_target" {
		where := ""
		for _, app := range v.to(id, "part_of", "app") {
			where = " on " + v.name(app)
		}
		return "PBS datastore " + cmp.Or(str(r, "store"), r.Name) + where
	}
	return v.name(id)
}

func healthWords(r *graph.Resource) string {
	reason := str(r, integration.AttrHealthReason)
	switch {
	case reason != "":
		return reason
	case r.Health == graph.HealthDown:
		return "down"
	case r.Health == graph.HealthUnknown:
		return "in an unknown state"
	}
	return "in need of attention"
}

func since(r *graph.Resource) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, str(r, integration.AttrHealthSince))
	return t.UTC()
}

// sourceTrouble reports when the hub can't read the integration that
// publishes r: its instance resource isn't ok.
func (v *view) sourceTrouble(r *graph.Resource) (string, bool) {
	inst := v.res[graph.RID(r.Source)]
	if inst == nil || inst.Kind != "integration" {
		return "", false
	}
	switch s := str(inst, "status"); s {
	case "error", "repair":
		return s, true
	}
	return "", false
}
