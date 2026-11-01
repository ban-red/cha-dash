package spectest

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dash.cha.sh/specs"
)

// Every bundled spec lints clean.
func TestLintBundled(t *testing.T) {
	names, _ := fs.Glob(specs.FS, "*.yaml")
	if len(names) < 9 {
		t.Fatalf("only %d specs", len(names))
	}
	for _, name := range names {
		b, _ := fs.ReadFile(specs.FS, name)
		if p := Lint(b); len(p) > 0 {
			t.Errorf("%s: %q", name, p)
		}
	}
}

// Lint finds what the schema refuses and what only the engine's rules do.
func TestLintProblems(t *testing.T) {
	b, _ := fs.ReadFile(specs.FS, "linkding.yaml")
	src := string(b)
	for what, c := range map[string]struct{ from, to, want string }{
		"a typo in a request": {"    extract:\n      version: version", "    extrat:\n      version: version", "extrat"},
		"a secret in a path":  {"path: /api/tags/", "path: /api/tags/{{ secret.token }}", "secret"},
		"not YAML":            {"apiVersion: chadash/v1alpha1", "apiVersion: [", "YAML"},
	} {
		bad := strings.Replace(src, c.from, c.to, 1)
		if bad == src {
			t.Fatalf("%s: no %q", what, c.from)
		}
		p := Lint([]byte(bad))
		if len(p) == 0 || !strings.Contains(strings.Join(p, "\n"), c.want) {
			t.Errorf("%s: %q", what, p)
		}
	}
}

// spec test on a bundled spec prints what its Go test expects
// (specs.TestLinkding), and passes.
func TestRunLinkding(t *testing.T) {
	b, _ := fs.ReadFile(specs.FS, "linkding.yaml")
	var out strings.Builder
	ok, err := Run(t.Context(), b, "../../../specs/testdata/linkding", &out)
	if err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{
		"linkding 0.1.0 (Linkding), chadash/v1alpha1",
		"Test: ok, version 1.42.0",
		"Sync: ok",
		"server       GET /health  200 health.json, asked",
		"bookmarks    GET /api/bookmarks/?limit=1  200 bookmarks.json, asked",
		"linkding:i_spec/app  app \"Test\"  running, ok",
		"archived = 21",
		"tags = 96",
		"linkding:i_spec/app#bookmarks = 1201 count",
		"ok     the instance monitors it",
		"\nPASS\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q in\n%s", want, got)
		}
	}
	if !ok {
		t.Fatalf("failed:\n%s", got)
	}
}

// v0.4's Reach sources pass spec test on their fixtures folders, as the
// plan's done-when asks (docs/plan/14-v0.4.md).
func TestRunReachSources(t *testing.T) {
	for id, want := range map[string][]string{
		"beszel":   {"beszel:i_spec/app#systems = 3 count"},
		"komodo":   {"Test: ok, version 1.19.5", "komodo:i_spec/app#stacks = 3 count"},
		"gatus":    {"gatus:i_spec/app  app \"Test\"  running"},
		"arcane":   {"Test: ok, version 2.17.2", "arcane:i_spec/app#stacks = 3 count", "arcane:i_spec/app#updates = 1 count"},
		"dockhand": {"dockhand:i_spec/app#stacks = 4 count", "dockhand:i_spec/app#updates = 1 count"},
	} {
		b, err := fs.ReadFile(specs.FS, id+".yaml")
		if err != nil {
			t.Fatal(err)
		}
		var out strings.Builder
		ok, err := Run(t.Context(), b, "../../../specs/testdata/"+id, &out)
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		got := out.String()
		for _, w := range append(want, "\nPASS\n") {
			if !strings.Contains(got, w) {
				t.Errorf("%s: no %q in\n%s", id, w, got)
			}
		}
		if !ok {
			t.Errorf("%s failed:\n%s", id, got)
		}
	}
}

// A required request without an answer fails; an answer for a request the
// spec doesn't make is an error.
func TestRunFailures(t *testing.T) {
	b, _ := fs.ReadFile(specs.FS, "linkding.yaml")
	dir := t.TempDir()
	for _, f := range []string{"health.json", "bookmarks.json", "bookmarks_archived.json"} {
		data, err := os.ReadFile(filepath.Join("../../../specs/testdata/linkding", f))
		if err != nil {
			t.Fatal(err)
		}
		name := map[string]string{"health.json": "server.json", "bookmarks.json": "bookmarks.json", "bookmarks_archived.json": "archived.json"}[f]
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var out strings.Builder
	ok, err := Run(t.Context(), b, dir, &out)
	if err != nil {
		t.Fatal(err)
	}
	if ok || !strings.Contains(out.String(), "FAILED: tags isn't optional and has no answer") || !strings.HasSuffix(out.String(), "FAIL\n") {
		t.Fatalf("ok %v:\n%s", ok, out.String())
	}

	if err := os.WriteFile(filepath.Join(dir, "fixtures.yaml"), []byte("answers:\n  nothing: server.json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(t.Context(), b, dir, &out); err == nil || !strings.Contains(err.Error(), `"nothing"`) {
		t.Fatalf("an answer for no request = %v", err)
	}
}
