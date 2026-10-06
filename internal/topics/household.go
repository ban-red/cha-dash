package topics

import (
	"context"
	"encoding/json"

	"dash.cha.sh/internal/realtime"
)

// InboxView is how one viewer sees the inbox. With Detail, every incident
// as published. Without it (the household role), only incidents
// ProjectIncident lets through, about resources Relevant accepts, in its
// projection. The zero value shows nothing, so a viewer nobody recognised
// sees an empty inbox.
type InboxView struct {
	Detail bool
	// Relevant reports whether a resource is on one of the viewer's boards.
	Relevant func(resource string) bool
}

// householdFields are the members of an incident the projection keeps,
// copied as published: enough for a tile to say "down since 14:03" and for
// the page to know something is being fixed, nothing about why or what
// else it reaches.
var householdFields = []string{"id", "scenario", "class", "severity", "status", "phase", "title", "target", "opened_at", "updated_at", "resolved_at", "resolved_by"}

// ProjectIncident is the household's view of one incident, and whether
// they see it at all. They see problems (not repairs, which are about an
// integration's credentials, nor the correlator's suggestions), never a
// security finding, and only when the incident's target or a resource in
// its blast radius is relevant. The projection keeps householdFields; facts,
// causes, the blast radius, the summary, what resolves it and the
// resolution are left out (causes and blast_radius as empty lists, summary
// and resolves_when as "", so the shape still parses as an incident).
// Actions are kept only when marked household: true, without their detail
// or impact.
func ProjectIncident(raw json.RawMessage, relevant func(resource string) bool) (json.RawMessage, bool) {
	if relevant == nil {
		return nil, false
	}
	var in map[string]json.RawMessage
	if json.Unmarshal(raw, &in) != nil {
		return nil, false
	}
	var head struct {
		Class       string `json:"class"`
		Category    string `json:"category"`
		Target      string `json:"target"`
		BlastRadius []struct {
			Resource string `json:"resource"`
		} `json:"blast_radius"`
	}
	if json.Unmarshal(raw, &head) != nil || head.Class != "problem" || head.Category == "security" {
		return nil, false
	}
	seen := head.Target != "" && relevant(head.Target)
	for _, im := range head.BlastRadius {
		if seen {
			break
		}
		seen = im.Resource != "" && relevant(im.Resource)
	}
	if !seen {
		return nil, false
	}
	out := make(map[string]json.RawMessage, len(householdFields)+6)
	for _, k := range householdFields {
		if v, ok := in[k]; ok {
			out[k] = v
		}
	}
	out["summary"] = json.RawMessage(`""`)
	out["resolves_when"] = json.RawMessage(`""`)
	out["causes"] = json.RawMessage(`[]`)
	out["blast_radius"] = json.RawMessage(`[]`)
	if a, ok := householdAction(in["suggested"]); ok {
		out["suggested"] = a
	}
	var alts []json.RawMessage
	if json.Unmarshal(in["alternatives"], &alts) == nil {
		var kept []json.RawMessage
		for _, raw := range alts {
			if a, ok := householdAction(raw); ok {
				kept = append(kept, a)
			}
		}
		if len(kept) > 0 {
			out["alternatives"], _ = json.Marshal(kept)
		}
	}
	b, err := json.Marshal(out)
	return b, err == nil
}

// householdAction keeps an action marked household: true, without its
// detail or impact.
func householdAction(raw json.RawMessage) (json.RawMessage, bool) {
	var a struct {
		ID        string          `json:"id"`
		Label     string          `json:"label"`
		Tier      json.RawMessage `json:"tier"`
		Target    string          `json:"target"`
		Household bool            `json:"household"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &a) != nil || !a.Household || a.ID == "" {
		return nil, false
	}
	b, err := json.Marshal(a)
	return b, err == nil
}

// ProjectInbox applies v to an inbox document (a Snapshot's), in place: the
// clock stays, and the incidents are those v shows.
func ProjectInbox(doc map[string]any, v InboxView) map[string]any {
	if v.Detail {
		return doc
	}
	items, ok := doc[Incidents].(map[string]json.RawMessage)
	if !ok {
		return doc
	}
	kept := make(map[string]json.RawMessage, len(items))
	for id, raw := range items {
		if p, ok := ProjectIncident(raw, v.Relevant); ok {
			kept[id] = p
		}
	}
	doc[Incidents] = kept
	return doc
}

// InboxSource serves the inbox topic per viewer: view, called once per
// subscription with the connection's context, says how that viewer sees it.
// Viewers with Detail get the document as published; the others get
// ProjectIncident's projection, kept current as incidents change.
func (s *Set) InboxSource(view func(ctx context.Context) InboxView) realtime.Source {
	return realtime.ProjectedDocSource{Doc: s.Inbox, Topic: Inbox, Coll: Incidents,
		Project: func(ctx context.Context) func(string, json.RawMessage) (json.RawMessage, bool) {
			v := view(ctx)
			if v.Detail {
				return nil
			}
			return func(_ string, raw json.RawMessage) (json.RawMessage, bool) { return ProjectIncident(raw, v.Relevant) }
		}}
}
