package frigate

import (
	_ "embed"

	"dash.cha.sh/internal/integration"
)

// recipe is how to make a credential with exactly the access this
// integration needs; the settings form shows it beside the fields.
//
//go:embed recipe.md
var recipe []byte

// controlRecipe is the control variant: the admin account the actions use.
//
//go:embed recipe.control.md
var controlRecipe []byte

func init() {
	integration.RegisterRecipe(ID, recipe)
	integration.RegisterControlRecipe(ID, controlRecipe)
}
