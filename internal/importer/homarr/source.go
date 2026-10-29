package homarr

import (
	"bytes"
	"strings"

	"dash.cha.sh/internal/importer"
	"dash.cha.sh/internal/importer/importhttp"
)

// ImportSource is the Homarr importer for the shared import API:
// POST /api/import/homarr takes {files: {backup_zip: <base64>}}. Request
// bodies may be up to 32 MiB: the zip, base64-encoded.
func ImportSource(cat importer.Catalog) importhttp.Source {
	return importhttp.Source{
		ID: Source, Name: "Homarr", MaxBytes: MaxZipBytes*4/3 + 1<<20,
		Roles: []importhttp.Role{{Key: "backup_zip", File: "homarr-backup.zip", Required: true, Binary: true}},
		Detect: func(name string, head []byte) (string, bool) {
			return "backup_zip", strings.HasSuffix(strings.ToLower(name), ".zip") && bytes.HasPrefix(head, []byte("PK\x03\x04"))
		},
		Build: func(files map[string][]byte) (*importer.Result, importer.Secrets, error) {
			return Build(files["backup_zip"], cat)
		},
	}
}
