package spec_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dash.cha.sh/internal/spec"
	"dash.cha.sh/internal/spec/spectest"
	"dash.cha.sh/specs"
)

// testdata/v1alpha1 holds four bundled specs as they were in v1alpha1,
// before the migration: Seerr (config defaults, actions), Beszel (a default
// in an each template), Backrest (findIndex) and Tailscale (defaults and
// now()).
func alpha(t *testing.T, id string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "v1alpha1", id+".yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// uncommented drops comment lines: the bundled specs' headers were edited
// by hand to name the new version.
func uncommented(b []byte) string {
	var out []string
	for l := range strings.Lines(string(b)) {
		if !strings.HasPrefix(strings.TrimSpace(l), "#") {
			out = append(out, l)
		}
	}
	return strings.Join(out, "")
}

// The migrator made the bundled specs: what it writes from the v1alpha1
// originals is what specs/ holds, comments aside, and it says what changed.
func TestMigrateBundled(t *testing.T) {
	for id, want := range map[string][]string{
		"seerr": {
			"apiVersion: chadash/v1alpha1 becomes chadash/v1beta1.",
			"resources[0].health: drops the ?? 20 after config.pending_warn, its default, which v1beta1 applies.",
		},
		"beszel": {
			"resources[1].health: drops the ?? 90 after config.disk_warn, its default, which v1beta1 applies.",
		},
		"backrest": {
			"resources[1].state: drops the ?? -1 after findIndex, which returns -1 in v1beta1 when nothing matches.",
		},
		"tailscale": {
			"metrics[2].value: drops the ?? 14 after config.expiry_warn_days, its default, which v1beta1 applies.",
			"resources[1].health: drops the ?? 7 after config.stale_days, its default, which v1beta1 applies.",
			"now() is the hub's clock in v1beta1",
		},
	} {
		src := alpha(t, id)
		out, changes, err := spec.Migrate(src)
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		all := strings.Join(changes, "\n")
		for _, w := range want {
			if !strings.Contains(all, w) {
				t.Errorf("%s: no %q in\n%s", id, w, all)
			}
		}
		if strings.Contains(all, "re-encoded") {
			t.Errorf("%s was re-encoded:\n%s", id, all)
		}
		// Comments and layout stay: every comment line is still there, and
		// only lines with a ?? or the apiVersion changed.
		srcLines, outLines := strings.Split(string(src), "\n"), strings.Split(string(out), "\n")
		if len(srcLines) != len(outLines) {
			t.Fatalf("%s: %d lines became %d", id, len(srcLines), len(outLines))
		}
		for i := range srcLines {
			if srcLines[i] != outLines[i] && !strings.Contains(srcLines[i], "??") && !strings.HasPrefix(srcLines[i], "apiVersion:") {
				t.Errorf("%s line %d changed: %q → %q", id, i+1, srcLines[i], outLines[i])
			}
		}
		bundled, _ := fs.ReadFile(specs.FS, id+".yaml")
		if uncommented(out) != uncommented(bundled) {
			t.Errorf("%s: the migration isn't specs/%s.yaml", id, id)
		}
		s, err := spec.Parse(out)
		if err != nil || s.APIVersion() != spec.V1Beta1 {
			t.Fatalf("%s: %v", id, err)
		}
		// A v1beta1 spec comes back as it is.
		again, changes, err := spec.Migrate(out)
		if err != nil || string(again) != string(out) || changes != nil {
			t.Errorf("%s: migrating twice: %v %v", id, changes, err)
		}
	}
}

// Every bundled spec is v1beta1 and has nothing left to migrate.
func TestBundledAreV1Beta1(t *testing.T) {
	names, _ := fs.Glob(specs.FS, "*.yaml")
	for _, n := range names {
		b, _ := fs.ReadFile(specs.FS, n)
		s, err := spec.Parse(b)
		if err != nil || s.APIVersion() != spec.V1Beta1 || spec.MigrationNotes(b) != nil {
			t.Errorf("%s: %v", n, err)
		}
	}
}

// A migrated spec publishes what the v1alpha1 one did: spec test's report
// over the same fixtures is the same, its version and clock lines aside.
// (Tailscale's would differ only by the clock, which is the point.)
func TestMigratedPublishesTheSame(t *testing.T) {
	report := func(b []byte, dir string) string {
		t.Helper()
		var out strings.Builder
		ok, err := spectest.Run(t.Context(), b, dir, &out)
		if err != nil || !ok {
			t.Fatalf("%v\n%s", err, out.String())
		}
		lines := strings.SplitN(out.String(), "\n", 3)
		return lines[2]
	}
	for _, id := range []string{"beszel", "backrest"} {
		src := alpha(t, id)
		out, _, err := spec.Migrate(src)
		if err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join("..", "..", "specs", "testdata", id)
		if a, b := report(src, dir), report(out, dir); a != b {
			t.Errorf("%s publishes differently:\n%s\n---\n%s", id, a, b)
		}
	}
}

// The rewrite's edges: only an exact default goes, parentheses go only
// when they held nothing else, strings and calls are left alone, and what
// can't be rewritten is named.
func TestMigrateEdges(t *testing.T) {
	const head = `apiVersion: chadash/v1alpha1
kind: Integration
meta: { id: widgets, name: Widgets, icon: widgets, version: 0.1.0, tier: bronze, capabilities: [primitives.stat] }
config:
  warn: { type: integer, title: Warn, default: 5 }
  name: { type: string, title: Name, default: "main" }
  zone: { type: string, title: Zone }
requests:
  info:
    path: /api/info
    extract: { list: list, n: n }
resources:
  - id: app
    kind: app
    attrs:
`
	for _, c := range []struct{ in, out, note string }{
		{`'(config.warn ?? 5) * 2'`, `'config.warn * 2'`, "drops the ?? 5 after config.warn"},
		{`'float(config.warn ?? 5)'`, `'float(config.warn)'`, "drops the ?? 5"},
		{`'config.warn ?? 6'`, `'config.warn ?? 6'`, "keeps config.warn ?? 6, but in v1beta1 an unset warn is its default, 5"},
		{`'config.name ?? "main"'`, `'config.name'`, `drops the ?? "main" after config.name`},
		{`'config.zone ?? "x"'`, `'config.zone ?? "x"'`, ""},
		{`'"config.warn ?? 5"'`, `'"config.warn ?? 5"'`, ""},
		{`'info.config.warn ?? 5'`, `'info.config.warn ?? 5'`, ""},
		{`'findIndex(info.list, # > (1 + 2)) ?? -1'`, `'findIndex(info.list, # > (1 + 2))'`, "drops the ?? -1 after findIndex"},
		{`'(findLastIndex(info.list, # > 1) ?? -1) + 1'`, `'(findLastIndex(info.list, # > 1)) + 1'`, "after findLastIndex"},
		{`'findIndex(info.list, # > 1) == nil'`, `'findIndex(info.list, # > 1) == nil'`, "this compares one with nil"},
		{`'config.warn == nil ? 1 : 2'`, `'config.warn == nil ? 1 : 2'`, "config.warn has a default (5)"},
		{`'now().Year()'`, `'now().Year()'`, "now() is the hub's clock"},
		{"|\n        let w = config.warn ?? 5; // a comment, config.warn ?? 5\n        w * 2", "|\n        let w = config.warn; // a comment, config.warn ?? 5\n        w * 2", "drops the ?? 5"},
	} {
		src := head + "      x: " + c.in + "\n"
		out, changes, err := spec.Migrate([]byte(src))
		if err != nil {
			t.Errorf("%s: %v", c.in, err)
			continue
		}
		want := strings.Replace(head, "chadash/v1alpha1", "chadash/v1beta1", 1) + "      x: " + c.out + "\n"
		if string(out) != want {
			t.Errorf("%s: got\n%s", c.in, out)
		}
		all := strings.Join(changes, "\n")
		if c.note != "" && !strings.Contains(all, c.note) {
			t.Errorf("%s: no %q in\n%s", c.in, c.note, all)
		}
		if c.note == "" && len(changes) != 1 {
			t.Errorf("%s: %q", c.in, changes)
		}
	}

	// A double-quoted scalar with escapes can't be edited in place: the
	// YAML is re-encoded, comments kept, and it still loads.
	src := "# Widgets.\n" + head + `      x: "string(config.warn ?? 5) + \"!\""` + "\n"
	out, changes, err := spec.Migrate([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(changes, "\n"), "re-encoded") || !strings.Contains(string(out), "# Widgets.") || !strings.Contains(string(out), "string(config.warn)") {
		t.Fatalf("re-encoded:\n%s\n%q", out, changes)
	}
	if _, err := spec.Parse(out); err != nil {
		t.Fatal(err)
	}

	// A template with an optional field that has a default: v1beta1 fills
	// the default where v1alpha1 wrote <nil>, which is named.
	tpl := strings.Replace(head, "path: /api/info", "path: /api/{{ config.name }}", 1) + "      x: '1'\n"
	if _, changes, err := spec.Migrate([]byte(tpl)); err != nil || !strings.Contains(strings.Join(changes, "\n"), `config.name has a default ("main")`) {
		t.Fatalf("template: %q %v", changes, err)
	}

	// A spec that doesn't load isn't migrated.
	if _, _, err := spec.Migrate([]byte(strings.Replace(head, "kind: app", "kind: ''", 1))); err == nil {
		t.Fatal("migrated a broken spec")
	}
}
