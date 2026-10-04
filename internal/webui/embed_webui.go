//go:build webui

package webui

import (
	"embed"
	"io/fs"
)

// dist is Vite's output. `bun run build` at the repo root writes it; the
// build fails here if it hasn't run, which is the point of the tag.
//
//go:embed all:dist
var dist embed.FS

// Embedded reports whether this binary carries the built web app.
const Embedded = true

// Files returns the built SPA.
func Files() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic("webui: " + err.Error()) // "dist" is a valid literal path
	}
	return sub
}
