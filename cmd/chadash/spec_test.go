package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/spec"
)

// spec lint on a v1alpha1 spec passes and says what v1beta1 changes;
// spec migrate rewrites it in place, after which lint has nothing to say.
func TestSpecMigrateCommand(t *testing.T) {
	b, err := os.ReadFile("../../internal/spec/testdata/v1alpha1/seerr.yaml")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "seerr.yaml")
	if err := os.WriteFile(path, b, 0o640); err != nil {
		t.Fatal(err)
	}
	code, stdout, _ := runCmd(t, nil, "spec", "lint", path)
	if code != 0 || !strings.Contains(stdout, path+": ok, in chadash/v1alpha1, which loads through v0.5.x. What chadash/v1beta1 changes:") ||
		!strings.Contains(stdout, "  resources[0].health: drops the ?? 20 after config.pending_warn") ||
		!strings.HasSuffix(stdout, "  chadash spec migrate "+path+" rewrites it.\n") {
		t.Fatalf("lint v1alpha1: %d\n%s", code, stdout)
	}

	code, stdout, stderr := runCmd(t, nil, "spec", "migrate", path)
	if code != 0 || !strings.HasPrefix(stdout, path+": migrated to chadash/v1beta1\n  apiVersion: chadash/v1alpha1 becomes chadash/v1beta1.\n") {
		t.Fatalf("migrate: %d %q %q", code, stdout, stderr)
	}
	out, _ := os.ReadFile(path)
	if !strings.Contains(string(out), "\napiVersion: chadash/v1beta1\n") || strings.Contains(string(out), "?? 20") || !strings.HasPrefix(string(out), "# Seerr") {
		t.Fatalf("migrated:\n%s", out)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o640 {
		t.Errorf("mode %v", info.Mode())
	}
	if code, stdout, _ := runCmd(t, nil, "spec", "lint", path); code != 0 || stdout != path+": ok\n" {
		t.Fatalf("lint after: %d %q", code, stdout)
	}
	if code, stdout, _ := runCmd(t, nil, "spec", "migrate", path); code != 0 || stdout != path+": already chadash/v1beta1\n" {
		t.Fatalf("migrate again: %d %q", code, stdout)
	}
	if code, _, stderr := runCmd(t, nil, "spec", "migrate", filepath.Join(t.TempDir(), "none.yaml")); code != 1 || !strings.Contains(stderr, "none.yaml") {
		t.Fatalf("migrate a missing file: %d %q", code, stderr)
	}
}

// spec new writes a spec, its fixtures, a recipe and a README that pass
// lint and spec test as they are; it refuses a bad ID and files that exist.
func TestSpecNewCommand(t *testing.T) {
	dir := t.TempDir()
	code, stdout, stderr := runCmd(t, nil, "spec", "new", "-dir", dir, "acme")
	if code != 0 {
		t.Fatalf("new: %d %q %q", code, stdout, stderr)
	}
	root := filepath.Join(dir, "acme")
	for _, f := range []string{"spec.yaml", "fixtures/fixtures.yaml", "fixtures/status.json", "recipe.md", "README.md"} {
		if !strings.Contains(stdout, "wrote "+filepath.Join(root, f)+"\n") {
			t.Errorf("no %s in %q", f, stdout)
		}
	}
	specPath, fixtures := filepath.Join(root, "spec.yaml"), filepath.Join(root, "fixtures")
	if code, stdout, _ := runCmd(t, nil, "spec", "lint", specPath); code != 0 || stdout != specPath+": ok\n" {
		t.Fatalf("lint: %d %q", code, stdout)
	}
	code, stdout, _ = runCmd(t, nil, "spec", "test", specPath, fixtures)
	if code != 0 || !strings.HasPrefix(stdout, "acme 0.1.0 (Acme), chadash/v1beta1\nClock: ") || !strings.Contains(stdout, `acme:i_spec/app  app "Test"  running, ok`) || !strings.HasSuffix(stdout, "\nPASS\n") {
		t.Fatalf("test: %d\n%s", code, stdout)
	}
	b, _ := os.ReadFile(specPath)
	if s, err := spec.Parse(b); err != nil || s.Manifest().ID != "acme" || s.APIVersion() != spec.V1Beta1 {
		t.Fatalf("spec: %v", err)
	}
	md, _ := os.ReadFile(filepath.Join(root, "recipe.md"))
	if _, err := integration.ParseRecipe(md); err != nil {
		t.Fatalf("recipe: %v", err)
	}

	// Nothing is overwritten, not even part of it.
	if err := os.Remove(filepath.Join(root, "recipe.md")); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := runCmd(t, nil, "spec", "new", "-dir", dir, "acme"); code != 1 || !strings.Contains(stderr, "spec.yaml exists already") {
		t.Fatalf("again: %d %q", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(root, "recipe.md")); err == nil {
		t.Fatal("a refused new wrote a file")
	}
	for _, id := range []string{"Acme", "a", "acme-app", "../acme", "9acme", strings.Repeat("a", 33)} {
		if code, _, stderr := runCmd(t, nil, "spec", "new", "-dir", dir, id); code != 1 || !strings.Contains(stderr, "isn't a spec ID") {
			t.Errorf("%q: %d %q", id, code, stderr)
		}
	}
	if code, _, stderr := runCmd(t, nil, "spec", "new"); code != 2 || !strings.Contains(stderr, "chadash spec new [-dir <folder>] <id>") {
		t.Fatalf("new without an id: %d %q", code, stderr)
	}
}
