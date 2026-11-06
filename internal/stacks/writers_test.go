package stacks

import (
	"net/http"
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
