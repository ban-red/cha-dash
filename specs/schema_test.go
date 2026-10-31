package specs

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.yaml.in/yaml/v3"

	"dash.cha.sh/internal/spec"
)

// The published schema (spec.Schema) takes every bundled spec, and refuses
// the mistakes a spec author makes most.
func TestSchemaTakesEveryBundledSpec(t *testing.T) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(spec.Schema()))
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("spec.json", doc); err != nil {
		t.Fatal(err)
	}
	schema, err := c.Compile("spec.json")
	if err != nil {
		t.Fatal(err)
	}
	valid := func(t *testing.T, src string) error {
		t.Helper()
		var v any
		if err := yaml.Unmarshal([]byte(src), &v); err != nil {
			t.Fatal(err)
		}
		// Validate the JSON the YAML decodes to, as an editor would.
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		return schema.Validate(inst)
	}

	names, _ := fs.Glob(FS, "*.yaml")
	if len(names) < 9 {
		t.Fatalf("only %d specs", len(names))
	}
	for _, name := range names {
		b, _ := fs.ReadFile(FS, name)
		if err := valid(t, string(b)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}

	seerr, _ := fs.ReadFile(FS, "seerr.yaml")
	for what, c := range map[string]struct{ from, to string }{
		"api version":       {"apiVersion: chadash/v1alpha1", "apiVersion: chadash/v1"},
		"unknown key":       {"\nevery: ", "\nevry: 60s\nevery: "},
		"typo in a request": {"    optional: true", "    optinal: true"},
		"tier":              {"tier: bronze", "tier: platinum"},
		"field type":        {"type: secret", "type: password"},
		"action tier":       {"tier: T1", "tier: T9"},
		"method":            {"method: POST", "method: PATCH"},
	} {
		src := strings.Replace(string(seerr), c.from, c.to, 1)
		if src == string(seerr) {
			t.Fatalf("%s: seerr.yaml has no %q", what, c.from)
		}
		if valid(t, src) == nil {
			t.Errorf("%s: the schema took it", what)
		}
	}
	// Parse reads methods in any case, so the schema does too.
	if err := valid(t, strings.Replace(string(seerr), "method: POST", "method: post", 1)); err != nil {
		t.Errorf("lower-case method: %v", err)
	}
}
