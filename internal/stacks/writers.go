package stacks

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"dash.cha.sh/internal/livedoc"
	"dash.cha.sh/internal/stackproto"
)

// otherManagers are the stack managers whose specs (specs/dockhand.yaml,
// komodo.yaml, arcane.yaml) publish their stacks to the Home Graph, by the
// resource's type attribute.
var otherManagers = map[string]string{
	"dockhand.stack": "Dockhand",
	"komodo.stack":   "Komodo",
	"arcane.project": "Arcane",
}

// OtherManager is another manager of a stack Cha Dash manages: two
// writers (plan 16, "Two writers"). Either a manager's spec lists a stack
// of the same project name (Resource), or the stack's containers were last
// brought up from another file on its host (File). The page and the Inbox
// say to deploy from one of them only; nothing is refused.
type OtherManager struct {
	Manager  string `json:"manager"`            // Dockhand, Komodo, Arcane, Dockge, or "Another tool"
	Resource string `json:"resource,omitempty"` // its stack in the Home Graph
	File     string `json:"file,omitempty"`     // the file it brought the stack up from
}

// managerOf names the tool a compose file belongs to, by the folder the
// tools keep their stacks in by default; "Another tool" otherwise.
func managerOf(file string) string {
	f := strings.ToLower(file)
	for _, m := range []string{"Dockge", "Dockhand", "Komodo", "Arcane", "Portainer"} {
		if strings.Contains(f, "/"+strings.ToLower(m)) {
			return m
		}
	}
	return "Another tool"
}

// alsoManagedBy lists the other managers of a stack named project: first
// the tool that last brought it up from another file on its host (upFrom,
// from the node), then the managers whose specs list a stack of that name,
// by manager then resource. The managers' APIs don't say which Docker host
// a stack is on in a way the hub can match to a node, so those are by name:
// a project of the same name on another host is named too, which is a
// warning to read, not a refusal. Never nil.
func (s *Service) alsoManagedBy(project, upFrom string) []OtherManager {
	out := []OtherManager{}
	if upFrom != "" {
		out = append(out, OtherManager{Manager: managerOf(upFrom), File: upFrom})
	}
	if s.graph == nil {
		return out
	}
	var named []OtherManager
	_, doc := s.graph.Snapshot(nil)
	for id, r := range doc.Resources {
		if r.Kind != "stack" {
			continue
		}
		m, ok := otherManagers[fmt.Sprint(r.Attrs["type"])]
		if !ok || fmt.Sprint(r.Attrs["project"]) != project {
			continue
		}
		named = append(named, OtherManager{Manager: m, Resource: string(id)})
	}
	slices.SortFunc(named, func(a, b OtherManager) int {
		if c := strings.Compare(a.Manager, b.Manager); c != 0 {
			return c
		}
		return strings.Compare(a.Resource, b.Resource)
	})
	return append(out, named...)
}

// ── the Inbox ────────────────────────────────────────────────────────────

// writersScenario is a two-writers Inbox item's scenario; its ID is
// stack.<node>.<stack>.writers.
const writersScenario = "stack-writers"

// WritersEvery is how often the syncer checks every node's stacks for a
// second manager.
const WritersEvery = 5 * time.Minute

func writersItemID(node, stack string) string { return "stack." + node + "." + stack + ".writers" }

// CheckWriters lists every online node's stacks and keeps one Inbox item,
// at attention, for each managed stack another manager also manages; an
// item whose stack no longer has one resolves. A node that doesn't answer
// keeps its items as they are.
func (s *Service) CheckWriters(ctx context.Context) {
	if s.inbox == nil {
		return
	}
	ns, err := s.nodes(ctx)
	if err != nil {
		s.log.Warn("stacks: two writers: list nodes", "err", err)
		return
	}
	reached := map[string]bool{}
	open := map[string]bool{}
	for _, n := range ns {
		if !n.Online {
			continue
		}
		var list stackproto.ListResponse
		if err := s.call(ctx, n.ID, stackproto.VerbList, stackproto.ListRequest{}, &list, s.timeout, nil); err != nil {
			continue
		}
		reached[n.ID] = true
		for _, st := range list.Stacks {
			if others := s.alsoManagedBy(st.Name, st.UpFrom); len(others) > 0 {
				s.writersItem(n.ID, st.Name, others)
				open[writersItemID(n.ID, st.Name)] = true
			}
		}
	}
	_, doc := s.inbox.Snapshot()
	items, _ := doc[incidentsColl].(map[string]json.RawMessage)
	now := s.now().UTC()
	_, _ = s.inbox.Update(func(tx *livedoc.Tx) error {
		for id, raw := range items {
			var in incident
			if json.Unmarshal(raw, &in) != nil || in.Scenario != writersScenario || in.Status == "resolved" || open[id] {
				continue
			}
			if node, _ := in.Facts["node"].(string); !reached[node] {
				continue
			}
			in.Status, in.Phase, in.UpdatedAt, in.ResolvedAt, in.ResolvedBy = "resolved", "resolved", now, &now, "stacks"
			tx.Put(incidentsColl, id, in)
		}
		return nil
	})
}

// writersItem opens (or updates) a stack's two-writers item.
func (s *Service) writersItem(node, stack string, others []OtherManager) {
	id := writersItemID(node, stack)
	target := Target(node, stack)
	names := make([]string, 0, len(others))
	for _, o := range others {
		if !slices.Contains(names, o.Manager) {
			names = append(names, o.Manager)
		}
	}
	who := names[0]
	if len(names) > 1 {
		who = strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
	}
	summary := "Two tools deploying one stack undo each other's changes: deploy it from one of them only."
	if f := others[0].File; f != "" {
		summary = stack + "'s containers were last brought up from " + f + ", not from Cha Dash's file. " + summary
	}
	now := s.now().UTC()
	_, _ = s.inbox.Update(func(tx *livedoc.Tx) error {
		opened := now
		if raw := tx.Item(incidentsColl, id); raw != nil {
			var old incident
			if json.Unmarshal(raw, &old) == nil && old.Status == "open" {
				opened = old.OpenedAt
			}
		}
		verb := "manages"
		if len(names) > 1 {
			verb = "manage"
		}
		tx.Put(incidentsColl, id, incident{
			ID: id, Scenario: writersScenario, Class: "problem", Severity: "attention", Status: "open", Phase: "open",
			Title: who + " also " + verb + " " + stack, Summary: summary, Target: target, OpenedAt: opened, UpdatedAt: now,
			Facts:  map[string]any{"node": node, "stack": stack, "managers": names},
			Causes: []any{}, BlastRadius: []any{},
			Suggested:    map[string]any{"id": "link.open", "label": "Open Settings → Stacks", "tier": "T0", "target": target},
			ResolvesWhen: "Only Cha Dash manages " + stack + ": the other tool stops listing it, and Cha Dash deploys it last.",
		})
		return nil
	})
}
