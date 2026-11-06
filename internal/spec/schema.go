package spec

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"sync"
)

// Schema is the JSON Schema (draft 2020-12) of the spec format, generated
// from the types Parse decodes, so the two can't drift. It checks the
// shape: keys, types and the few enums. What Parse checks beyond that
// (templates, patterns, expressions, secrets in paths) stays Parse's.
//
// A spec is YAML; validate it as the JSON it decodes to. Schema is the
// current version's (APIVersion); SchemaFor is any of Versions'.
func Schema() []byte { return SchemaFor(APIVersion) }

// SchemaFor is the schema of one of Versions, or nil. The versions share a
// shape: what v1beta1 changed is what expressions mean, not the keys.
func SchemaFor(version string) []byte {
	if !slices.Contains(Versions, version) {
		return nil
	}
	schemaMu.Lock()
	defer schemaMu.Unlock()
	if b, ok := schemaJSON[version]; ok {
		return b
	}
	s := schemaOf(reflect.TypeFor[file]())
	s["$schema"] = "https://json-schema.org/draft/2020-12/schema"
	s["title"] = "Cha Dash integration spec, " + version
	props := s["properties"].(map[string]any)
	props["apiVersion"] = map[string]any{"const": version}
	props["kind"] = map[string]any{"const": "Integration"}
	s["required"] = []string{"apiVersion", "kind", "meta"}
	b, _ := json.MarshalIndent(s, "", "  ")
	schemaJSON[version] = b
	return b
}

var (
	schemaMu   sync.Mutex
	schemaJSON = map[string][]byte{}
)

// keyed are the collections written as a mapping from a name to an entry
// (their UnmarshalYAML), and the entry's type.
var keyed = map[reflect.Type]reflect.Type{
	reflect.TypeFor[fields]():   reflect.TypeFor[field](),
	reflect.TypeFor[requests](): reflect.TypeFor[request](),
	reflect.TypeFor[targets]():  reflect.TypeFor[targetTpl](),
}

// enums are the closed sets, by "<type>.<yaml key>".
var enums = map[string][]string{
	"field.type":     {"string", "url", "secret", "integer", "number", "boolean"},
	"actionTpl.tier": {"T0", "T1", "T2", "T3", "T4"},
	"meta.tier":      {"bronze", "silver", "gold"},
}

// patterns are the sets Parse reads in any case, by "<type>.<yaml key>",
// spelled out because JSON Schema's patterns are JavaScript's, which have
// no (?i).
var patterns = map[string]string{
	"request.method":  anyCase("GET", "POST"),
	"writeTpl.method": anyCase("POST", "PUT", "DELETE"),
}

// anyCase is a pattern matching each word in any case: GET is
// "[Gg][Ee][Tt]".
func anyCase(words ...string) string {
	alts := make([]string, len(words))
	for i, w := range words {
		var b strings.Builder
		for _, r := range w {
			b.WriteString("[" + strings.ToUpper(string(r)) + strings.ToLower(string(r)) + "]")
		}
		alts[i] = b.String()
	}
	return "^(" + strings.Join(alts, "|") + ")$"
}

// required are the keys an entry must have, by type.
var required = map[string][]string{
	"meta": {"id", "name"},
}

// schemaOf is t's schema.
func schemaOf(t reflect.Type) map[string]any {
	if e, ok := keyed[t]; ok {
		return map[string]any{"type": "object", "additionalProperties": schemaOf(e)}
	}
	switch t.Kind() {
	case reflect.Pointer:
		return schemaOf(t.Elem())
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int64:
		return map[string]any{"type": "integer"}
	case reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.Interface:
		return map[string]any{}
	case reflect.Array:
		return map[string]any{"type": "array", "items": schemaOf(t.Elem()), "minItems": t.Len(), "maxItems": t.Len()}
	case reflect.Slice:
		return map[string]any{"type": "array", "items": schemaOf(t.Elem())}
	case reflect.Map:
		return map[string]any{"type": "object", "additionalProperties": schemaOf(t.Elem())}
	case reflect.Struct:
		props := map[string]any{}
		for f := range t.Fields() {
			name, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
			if name == "" || name == "-" {
				continue // filled from the mapping's key (Name, ID)
			}
			p := schemaOf(f.Type)
			if e, ok := enums[t.Name()+"."+name]; ok {
				p["enum"] = e
			}
			if pat, ok := patterns[t.Name()+"."+name]; ok {
				p["pattern"] = pat
			}
			props[name] = p
		}
		s := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
		if r, ok := required[t.Name()]; ok {
			s["required"] = r
		}
		return s
	}
	panic("spec: no schema for " + t.String())
}
