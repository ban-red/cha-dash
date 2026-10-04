//go:build !webui

package webui

import (
	"embed"
	"io/fs"
)

//go:embed placeholder/index.html
var placeholder embed.FS

// Embedded reports whether this binary carries the built web app.
const Embedded = false

// Files returns a one-page stand-in that explains how to build the web app
// into the binary.
func Files() fs.FS {
	sub, err := fs.Sub(placeholder, "placeholder")
	if err != nil {
		panic("webui: " + err.Error()) // "placeholder" is a valid literal path
	}
	return sub
}
