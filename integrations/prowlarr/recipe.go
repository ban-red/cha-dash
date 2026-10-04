package prowlarr

import (
	_ "embed"

	"dash.cha.sh/internal/integration"
)

// recipe is how to make a credential with exactly the access this
// integration needs; the settings form shows it beside the fields.
//
//go:embed recipe.md
var recipe []byte

func init() { integration.RegisterRecipe(ID, recipe) }
