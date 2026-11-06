package stacks

import (
	"fmt"
	"slices"
	"strings"
)

// otherManagers are the stack managers whose specs (specs/dockhand.yaml,
// komodo.yaml, arcane.yaml) publish their stacks to the Home Graph, by the
// resource's type attribute.
var otherManagers = map[string]string{
	"dockhand.stack": "Dockhand",
	"komodo.stack":   "Komodo",
	"arcane.project": "Arcane",
}

// OtherManager is another manager that lists a stack of the same project
// name: two writers (plan 16, "Two writers"). The page says to deploy from
// one of them only; nothing is refused.
type OtherManager struct {
	Manager  string `json:"manager"`  // Dockhand, Komodo, Arcane
	Resource string `json:"resource"` // its stack in the Home Graph
}

// alsoManagedBy lists the other managers with a stack named project, by
// manager then resource. The managers' APIs don't say which Docker host a
// stack is on in a way the hub can match to a node, so it's by name: a
// project of the same name on another host is named too, which is a
// warning to read, not a refusal. Never nil.
func (s *Service) alsoManagedBy(project string) []OtherManager {
	out := []OtherManager{}
	if s.graph == nil {
		return out
	}
	_, doc := s.graph.Snapshot(nil)
	for id, r := range doc.Resources {
		if r.Kind != "stack" {
			continue
		}
		m, ok := otherManagers[fmt.Sprint(r.Attrs["type"])]
		if !ok || fmt.Sprint(r.Attrs["project"]) != project {
			continue
		}
		out = append(out, OtherManager{Manager: m, Resource: string(id)})
	}
	slices.SortFunc(out, func(a, b OtherManager) int {
		if c := strings.Compare(a.Manager, b.Manager); c != 0 {
			return c
		}
		return strings.Compare(a.Resource, b.Resource)
	})
	return out
}
