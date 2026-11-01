package apidoc

import (
	"os"
	"path/filepath"
	"testing"
)

// TestWriteOpenAPI writes the document to the file CHADASH_WRITE_OPENAPI
// names, for the docs site (CONTRIBUTING.md, "The docs site"). Without the
// variable it is skipped, so the suite never writes files on its own:
//
//	CHADASH_WRITE_OPENAPI=$PWD/docs-site/public/openapi.json go test ./internal/apidoc/ -run TestWriteOpenAPI
func TestWriteOpenAPI(t *testing.T) {
	path := os.Getenv("CHADASH_WRITE_OPENAPI")
	if path == "" {
		t.Skip("set CHADASH_WRITE_OPENAPI to a file path to write the document")
	}
	if !filepath.IsAbs(path) {
		t.Fatalf("CHADASH_WRITE_OPENAPI must be an absolute path (go test runs in the package's directory): %q", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(JSON(), '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}
