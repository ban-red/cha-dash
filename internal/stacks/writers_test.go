package stacks

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"dash.cha.sh/internal/graph"
)

// A stack Cha Dash manages that Dockhand, Komodo or Arcane also lists, by
// project name, says so on its page and in the list: deploy from one of
// them only (plan 16, "Two writers"). A deploy still goes ahead.
func TestTwoWritersAreNamed(t *testing.T) {
	h := newHub(t)
	h.saveDraft("media", "services:\n  web:\n    image: nginx:1.27\n")
	if _, task := h.run(ActionDeploy, Target("local", "media"), ""); task.State != "succeeded" {
		t.Fatalf("deploy: %+v", task)
	}
	if _, err := h.g.Update(func(tx *graph.Tx) error {
		for _, r := range []graph.Resource{
			{ID: "dockhand:i_dh/stack/media", Kind: "stack", Name: "media", Source: "integration:i_dh", Attrs: map[string]any{"type": "dockhand.stack", "project": "media"}},
			{ID: "komodo:i_ko/stack/abc", Kind: "stack", Name: "media", Source: "integration:i_ko", Attrs: map[string]any{"type": "komodo.stack", "project": "media"}},
			// Other projects and other kinds don't count.
			{ID: "arcane:i_ar/stack/vpn", Kind: "stack", Name: "vpn", Source: "integration:i_ar", Attrs: map[string]any{"type": "arcane.project", "project": "vpn"}},
			{ID: "docker:i_d/stack/media", Kind: "stack", Name: "media", Source: "integration:i_d", Attrs: map[string]any{"project": "media"}},
		} {
			if err := tx.Upsert(r); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	w := h.do("GET", "/api/stacks/local/media", nil)
	d := decode[StackDetail](t, w)
	if w.Code != http.StatusOK || len(d.AlsoManagedBy) != 2 || d.AlsoManagedBy[0].Manager != "Dockhand" || d.AlsoManagedBy[1].Manager != "Komodo" ||
		d.AlsoManagedBy[0].Resource != "dockhand:i_dh/stack/media" {
		t.Fatalf("detail = %d %+v", w.Code, d.AlsoManagedBy)
	}
	list := decode[struct {
		Nodes []NodeView `json:"nodes"`
	}](t, h.do("GET", "/api/stacks", nil))
	var found bool
	for _, st := range list.Nodes[0].Stacks {
		if st.Name == "media" {
			found = true
			if len(st.AlsoManagedBy) != 2 {
				t.Fatalf("list row = %+v", st)
			}
		}
	}
	if !found {
		t.Fatal("media isn't listed")
	}
	// Alone, it says nothing, as an empty list.
	h.g.Update(func(tx *graph.Tx) error {
		tx.Remove("dockhand:i_dh/stack/media")
		tx.Remove("komodo:i_ko/stack/abc")
		return nil
	})
	if d := decode[StackDetail](t, h.do("GET", "/api/stacks/local/media", nil)); d.AlsoManagedBy == nil || len(d.AlsoManagedBy) != 0 {
		t.Fatalf("alone = %#v", d.AlsoManagedBy)
	}
}

// The Inbox has an item at attention for a managed stack with a second
// manager: by name (a manager's spec), or by the file another tool last
// brought it up from on its host, named first. It resolves once neither
// holds, and stays while the node can't be reached.
func TestTwoWritersInTheInbox(t *testing.T) {
	h := newHub(t)
	h.saveDraft("media", "services:\n  web:\n    image: nginx:1.27\n")
	if _, task := h.run(ActionDeploy, Target("local", "media"), ""); task.State != "succeeded" {
		t.Fatalf("deploy: %+v", task)
	}
	item := func() (incident, bool) {
		raw := h.inbox.Item(incidentsColl, writersItemID("local", "media"))
		var in incident
		return in, raw != nil && json.Unmarshal(raw, &in) == nil
	}
	h.svc.CheckWriters(t.Context())
	if _, ok := item(); ok {
		t.Fatal("alone: no item")
	}

	// Dockge brought it up last, from its own folder; Dockhand lists it too.
	h.docker.SetConfigFiles("media", "/opt/dockge/stacks/media/compose.yaml")
	h.g.Update(func(tx *graph.Tx) error {
		return tx.Upsert(graph.Resource{ID: "dockhand:i_dh/stack/media", Kind: "stack", Name: "media", Source: "integration:i_dh", Attrs: map[string]any{"type": "dockhand.stack", "project": "media"}})
	})
	h.svc.CheckWriters(t.Context())
	in, ok := item()
	if !ok || in.Status != "open" || in.Severity != "attention" || in.Scenario != writersScenario || in.Title != "Dockge and Dockhand also manage media" ||
		!strings.Contains(in.Summary, "last brought up from /opt/dockge/stacks/media/compose.yaml") || in.Target != Target("local", "media") {
		t.Fatalf("item = %+v", in)
	}
	d := decode[StackDetail](t, h.do("GET", "/api/stacks/local/media", nil))
	if len(d.AlsoManagedBy) != 2 || d.AlsoManagedBy[0].Manager != "Dockge" || d.AlsoManagedBy[0].File != "/opt/dockge/stacks/media/compose.yaml" || d.AlsoManagedBy[1].Manager != "Dockhand" {
		t.Fatalf("the page = %+v", d.AlsoManagedBy)
	}

	// The node is away: the item stays.
	h.online = false
	h.svc.CheckWriters(t.Context())
	if in, _ := item(); in.Status != "open" {
		t.Fatalf("offline = %+v", in)
	}
	h.online = true

	// Dockhand stops listing it and Cha Dash deploys it again: resolved.
	h.g.Update(func(tx *graph.Tx) error { tx.Remove("dockhand:i_dh/stack/media"); return nil })
	h.docker.SetConfigFiles("media", h.root+"/media/compose.yaml")
	h.svc.CheckWriters(t.Context())
	if in, _ := item(); in.Status != "resolved" || in.ResolvedAt == nil {
		t.Fatalf("resolved = %+v", in)
	}
}
