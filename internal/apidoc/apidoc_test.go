package apidoc

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"dash.cha.sh/internal/authz"
)

// TestEveryRouteHasASchema fails on a route in internal/authz's table
// without a row here, and on a row for a route the table doesn't have.
// Optional routes need rows too.
func TestEveryRouteHasASchema(t *testing.T) {
	table := map[string]bool{}
	for _, r := range authz.Routes {
		table[r.Pattern] = true
		if _, ok := rows[r.Pattern]; !ok {
			t.Errorf("%q has no row in internal/apidoc: add one to its area's file", r.Pattern)
		}
	}
	for _, p := range Rows() {
		if !table[p] {
			t.Errorf("internal/apidoc has a row for %q, which isn't in internal/authz/routes.go", p)
		}
		op := rows[p]
		if strings.TrimSpace(op.Summary) == "" || !strings.HasSuffix(op.Summary, ".") {
			t.Errorf("%q: the summary should be a short sentence: %q", p, op.Summary)
		}
		method, _, _ := strings.Cut(p, " ")
		if op.Body != nil && (method == "GET" || method == "DELETE") {
			t.Errorf("%q has a request body", p)
		}
	}
}

// TestDocument checks the generated JSON is OpenAPI 3.1 in shape: the
// version, a path and operation per route with unique IDs, path
// parameters as the template names them, responses on each, and every
// $ref resolving.
func TestDocument(t *testing.T) {
	var doc map[string]any
	if err := json.Unmarshal(JSON(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc["openapi"] != "3.1.0" {
		t.Fatalf("openapi = %v", doc["openapi"])
	}
	info, _ := doc["info"].(map[string]any)
	if info["title"] == "" || info["version"] == nil {
		t.Fatalf("info = %v", info)
	}
	paths, _ := doc["paths"].(map[string]any)
	ids := map[string]string{}
	ops := 0
	for path, item := range paths {
		if !strings.HasPrefix(path, "/") {
			t.Errorf("path %q doesn't start with /", path)
		}
		want := pathParam.FindAllStringSubmatch(path, -1)
		for method, raw := range item.(map[string]any) {
			if !slices.Contains([]string{"get", "put", "post", "delete", "patch"}, method) {
				t.Errorf("%s: method %q", path, method)
			}
			op := raw.(map[string]any)
			ops++
			id, _ := op["operationId"].(string)
			if other, dup := ids[id]; dup || id == "" {
				t.Errorf("%s %s: operationId %q (also %s)", method, path, id, other)
			}
			ids[id] = method + " " + path
			if r, _ := op["responses"].(map[string]any); len(r) < 2 {
				t.Errorf("%s %s: responses %v", method, path, r)
			}
			if _, ok := op["x-chadash-access"].(string); !ok {
				t.Errorf("%s %s: no x-chadash-access", method, path)
			}
			var got []string
			params, _ := op["parameters"].([]any)
			for _, p := range params {
				p := p.(map[string]any)
				if p["in"] == "path" {
					got = append(got, p["name"].(string))
				}
			}
			if len(got) != len(want) {
				t.Errorf("%s %s: path parameters %v", method, path, got)
			}
		}
	}
	if ops != len(authz.Routes) {
		t.Errorf("%d operations for %d routes", ops, len(authz.Routes))
	}

	refs := 0
	var walk func(v any)
	walk = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			if ref, ok := v["$ref"].(string); ok {
				refs++
				if resolve(doc, ref) == nil {
					t.Errorf("$ref %q doesn't resolve", ref)
				}
			}
			for _, e := range v {
				walk(e)
			}
		case []any:
			for _, e := range v {
				walk(e)
			}
		}
	}
	walk(doc)
	if refs < 50 {
		t.Errorf("only %d $refs: the components aren't being used", refs)
	}
}

func resolve(doc map[string]any, ref string) any {
	rest, ok := strings.CutPrefix(ref, "#/")
	if !ok {
		return nil
	}
	var cur any = doc
	for part := range strings.SplitSeq(rest, "/") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[strings.NewReplacer("~1", "/", "~0", "~").Replace(part)]
	}
	return cur
}

// TestSchemas checks the generator against encoding/json's rules.
func TestSchemas(t *testing.T) {
	type inner struct {
		A string `json:"a"`
		B string `json:"b"`
	}
	type sample struct {
		inner
		B       int             `json:"b"` // shadows inner's
		Skip    string          `json:"-"`
		Opt     string          `json:"opt,omitempty"`
		Ptr     *int            `json:"ptr"`
		Raw     json.RawMessage `json:"raw"`
		Any     any             `json:"any"`
		Map     map[string]bool `json:"map"`
		List    []string        `json:"list"`
		Num     int64           `json:"num,string"`
		NoTag   bool
		private int //nolint:unused // here to prove the schema leaves unexported fields out
	}
	s := newSchemas()
	got := s.of(reflect.TypeFor[sample]())
	props := got["properties"].(map[string]any)
	if _, ok := props["Skip"]; ok {
		t.Error("json:\"-\" is in")
	}
	if _, ok := props["private"]; ok {
		t.Error("an unexported field is in")
	}
	check := func(name string, want any) {
		t.Helper()
		b1, _ := json.Marshal(props[name])
		b2, _ := json.Marshal(want)
		if string(b1) != string(b2) {
			t.Errorf("%s = %s, want %s", name, b1, b2)
		}
	}
	check("a", map[string]any{"type": "string"})
	check("b", map[string]any{"type": "integer", "format": "int64"})
	check("ptr", map[string]any{"type": []string{"integer", "null"}, "format": "int64"})
	check("raw", map[string]any{})
	check("any", map[string]any{})
	check("map", map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "boolean"}})
	check("list", map[string]any{"type": "array", "items": map[string]any{"type": "string"}})
	check("num", map[string]any{"type": "string"})
	check("NoTag", map[string]any{"type": "boolean"})
	req := got["required"].([]string)
	if slices.Contains(req, "opt") || !slices.Contains(req, "a") || !slices.Contains(req, "b") {
		t.Errorf("required = %v", req)
	}

	// A named struct from another package is a component, named after
	// its package.
	ref := s.of(reflect.TypeFor[authz.Route]())
	if ref["$ref"] != "#/components/schemas/authz.Route" || s.components["authz.Route"] == nil {
		t.Errorf("authz.Route = %v", ref)
	}
}
