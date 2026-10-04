// Package specs bundles the Tier 1 integration specs (<id>.yaml) and
// registers them at init, like a Tier 0 integration. The format is
// internal and unstable until v1.0; see internal/README.md.
package specs

import (
	"embed"

	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/spec"
)

//go:embed *.yaml
var FS embed.FS

func init() {
	if err := spec.RegisterFS(FS, integration.Default); err != nil {
		panic(err)
	}
}
