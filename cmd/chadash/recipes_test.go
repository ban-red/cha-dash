package main

import (
	"strings"
	"testing"

	"dash.cha.sh/internal/integration"
)

// Every integration in the hub comes with a least-privilege recipe, and a
// recipe only fills in, or reads from, fields its integration's form has.
func TestEveryIntegrationHasARecipe(t *testing.T) {
	for _, m := range integration.Default.Manifests() {
		r, ok := integration.Default.Recipe(m.ID)
		if !ok {
			t.Errorf("%s has no recipe (integrations/%s/recipe.md, or specs/recipes/%s.md)", m.ID, m.ID, m.ID)
			continue
		}
		for field := range r.Fills {
			p := m.ConfigSchema.Property(field)
			switch {
			case p == nil:
				t.Errorf("%s: the recipe fills %q, which isn't in the config", m.ID, field)
			case p.WriteOnly:
				t.Errorf("%s: the recipe fills %q, a secret: secrets are never generated", m.ID, field)
			}
		}
		for _, p := range r.Params {
			if field, ok := strings.CutPrefix(p.From, "config."); ok && m.ConfigSchema.Property(field) == nil {
				t.Errorf("%s: param %q reads %q, which isn't in the config", m.ID, p.Name, field)
			}
		}
		// Only the integrations that need no credential say so.
		if (r.Access == integration.RecipeNone) != (len(m.SecretFields()) == 0 && m.ID != "docker") {
			t.Errorf("%s: access %q, with secret fields %v", m.ID, r.Access, m.SecretFields())
		}
	}
	for _, id := range []string{"tautulli", "seerr", "proxmox", "docker"} {
		if _, ok := integration.Default.Recipe(id); !ok {
			t.Errorf("%s: no recipe", id)
		}
	}
}
