package yamlboard

import (
	"bytes"

	"dash.cha.sh/internal/importer"
	"dash.cha.sh/internal/importer/importhttp"
)

// MaxBodyBytes bounds a request: one hand-written board.
const MaxBodyBytes = 1 << 20

// ImportSource is the plain-YAML importer for the shared import API:
// POST /api/import/yaml takes {files: {board_yaml}}.
func ImportSource(cat importer.Catalog) importhttp.Source {
	return importhttp.Source{
		ID: Source, Name: "Plain YAML", MaxBytes: MaxBodyBytes,
		Roles: []importhttp.Role{{Key: "board_yaml", File: file, Required: true}},
		Detect: func(_ string, head []byte) (string, bool) {
			return "board_yaml", bytes.Contains(head, []byte("apiVersion: "+APIVersion)) && bytes.Contains(head, []byte("kind: "+Kind))
		},
		Build: func(files map[string][]byte) (*importer.Result, importer.Secrets, error) {
			return Build(files["board_yaml"], cat)
		},
	}
}
