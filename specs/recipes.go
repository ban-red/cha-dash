package specs

import (
	"embed"
	"path"
	"strings"

	"dash.cha.sh/internal/integration"
)

// Recipes holds recipes/<id>.md: how to make a credential with exactly the
// access each spec needs (see integration.Recipe). They live apart from the
// specs because FS takes every *.yaml here as one.
//
//go:embed recipes/*.md
var Recipes embed.FS

func init() {
	entries, err := Recipes.ReadDir("recipes")
	if err != nil {
		panic(err)
	}
	for _, e := range entries {
		md, err := Recipes.ReadFile(path.Join("recipes", e.Name()))
		if err != nil {
			panic(err)
		}
		integration.RegisterRecipe(strings.TrimSuffix(e.Name(), ".md"), md)
	}
}
