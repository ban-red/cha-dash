package correlate

import (
	"fmt"
	"strings"

	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/integration"
)

// ClassSuggestion is the inbox class of the correlator's questions to the
// owner. Suggestions aren't problems: they never reach Needs attention.
const ClassSuggestion = "suggestion"

// Suggestion actions. The web app answers them through
// POST /api/correlate/suggestions/{id}/confirm and …/reject.
const (
	ActionConfirm = "correlate.confirm"
	ActionReject  = "correlate.reject"
)

var kindWords = map[graph.Kind]string{
	"container": "container", "app": "app", "host": "host", "engine": "Docker engine", "storage": "storage",
	"backup_target": "PBS datastore", "route": "route", "check": "check", "vm": "VM", "lxc": "LXC",
	"router": "router", "disk": "disk",
}

// describe is how the inbox names a resource: "the container sonarr (Docker
// on Jupiter)".
func describe(doc graph.Doc, id graph.RID) string {
	r := doc.Resources[id]
	if r == nil {
		return string(id)
	}
	kind := kindWords[r.Kind]
	if kind == "" {
		kind = strings.ReplaceAll(string(r.Kind), "_", " ")
	}
	name := r.Name
	if name == "" {
		name = string(id)
	}
	s := "the " + kind + " " + name
	if r.Kind == "app" {
		s = name
	}
	if inst := doc.Resources[graph.RID(r.Source)]; inst != nil && inst.Name != "" {
		s += " (" + inst.Name + ")"
	}
	return s
}

var verbs = map[graph.RelType]string{
	RunsOn: "run on", RoutesTo: "route to", StoresOn: "write to", DependsOn: "depend on", Monitors: "check",
}

// isMerge says whether a suggestion reads as "the same thing": an identity,
// or an app and the container it runs in, which people call by one name.
func isMerge(doc graph.Doc, l Link) bool {
	if l.Type == SameAs {
		return true
	}
	a, b := doc.Resources[l.From], doc.Resources[l.To]
	return l.Type == RunsOn && a != nil && b != nil && a.Kind == "app" && b.Kind == "container"
}

// renderSuggestion is a suggestion as an inbox item: an incident of class
// "suggestion", severity info, with Merge (or Link) and Keep separate.
func renderSuggestion(doc graph.Doc, id string, s *suggestion) integration.Incident {
	l := s.link
	a, b := describe(doc, l.From), describe(doc, l.To)
	in := integration.Incident{
		ID: id, Class: ClassSuggestion, Severity: integration.SeverityInfo, Status: integration.StatusOpen, Phase: "open",
		Target: string(l.From), OpenedAt: s.opened, UpdatedAt: s.opened,
		Facts: map[string]any{"from": string(l.From), "to": string(l.To), "relation": string(l.Type), "rule": l.Rule,
			"confidence": string(l.Confidence)},
		Causes: []integration.Cause{}, BlastRadius: []integration.Impact{},
		Alternatives: []integration.Action{{ID: ActionReject, Label: "Keep separate", Tier: "T0", Target: string(l.From)}},
	}
	if isMerge(doc, l) {
		in.Scenario = "merge-suggestion"
		in.Title = "These look like the same thing. Merge?"
		in.Summary = fmt.Sprintf("%s and %s: %s", capitalize(a), b, l.Evidence)
		in.Suggested = integration.Action{ID: ActionConfirm, Label: "Merge", Tier: "T0", Target: string(l.From),
			Detail: "Cha Dash then shows them as one thing, with what each source reports. The sources stay as they are."}
		in.ResolvesWhen = "Resolves when you merge them or keep them separate."
	} else {
		in.Scenario = "link-suggestion"
		in.Title = fmt.Sprintf("Does %s %s %s?", a, verbs[l.Type], b)
		in.Summary = l.Evidence
		in.Suggested = integration.Action{ID: ActionConfirm, Label: "Link them", Tier: "T0", Target: string(l.From)}
		in.ResolvesWhen = "Resolves when you link them or keep them separate."
	}
	if !s.resolved.IsZero() {
		in.Status, in.Phase = integration.StatusResolved, "resolved"
		in.ResolvedAt, in.UpdatedAt = s.resolved, s.resolved
		in.ResolvedBy, in.Resolution = "action", s.resolution
	}
	return in
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
