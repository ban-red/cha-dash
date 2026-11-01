package caddy

import (
	_ "embed"

	"dash.cha.sh/internal/integration"
)

// recipe is how to reach the admin API safely; the settings form shows it
// beside the fields.
//
//go:embed recipe.md
var recipe []byte

func init() { integration.RegisterRecipe(ID, recipe) }
