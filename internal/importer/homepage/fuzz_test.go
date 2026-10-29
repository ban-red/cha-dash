package homepage

import (
	"os"
	"path/filepath"
	"testing"
)

// FuzzBuild: whatever YAML someone pastes into the import, Build answers
// with a plan or an error, never a panic.
//
//	go test -run '^$' -fuzz '^FuzzBuild$' -fuzztime 60s ./internal/importer/homepage/
func FuzzBuild(f *testing.F) {
	dir := filepath.Join("..", "..", "..", "testdata", "importers", "homepage", "dogfood")
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			f.Fatal(err)
		}
		return string(b)
	}
	f.Add(read("services.yaml"), read("docker.yaml"), read("settings.yaml"))
	f.Add("- Group:\n    - App:\n        href: http://app.example\n", "", "")
	f.Add("- G: [{A: {widget: {type: sonarr, url: x, key: '{{HOMEPAGE_VAR_K}}'}}}]", "", "layout: {G: {style: row, columns: 9}}")
	f.Add("&a [*a]", "x: &b {y: *b}", "")
	f.Fuzz(func(t *testing.T, services, docker, settings string) {
		_, _, _ = Build(Input{Services: services, Docker: docker, Settings: settings})
	})
}
