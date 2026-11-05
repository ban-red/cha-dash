package apidoc

import (
	"encoding"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"strings"
	"time"

	"dash.cha.sh/internal/audit"
	"dash.cha.sh/internal/httpx"
	"dash.cha.sh/internal/importer"
	"dash.cha.sh/internal/integration"
)

// schemas turns Go types into JSON Schema (draft 2020-12, as OpenAPI 3.1
// uses it), the way encoding/json writes them. Named struct types from
// other packages become components, so a type used by several routes is
// described once; anonymous structs and the mirror types in this package
// are inlined.
type schemas struct {
	components map[string]map[string]any
	names      map[reflect.Type]string
	owner      map[string]reflect.Type
}

func newSchemas() *schemas {
	return &schemas{components: map[string]map[string]any{}, names: map[reflect.Type]string{}, owner: map[string]reflect.Type{}}
}

var (
	timeType    = reflect.TypeFor[time.Time]()
	rawType     = reflect.TypeFor[json.RawMessage]()
	marshaler   = reflect.TypeFor[json.Marshaler]()
	textMarshal = reflect.TypeFor[encoding.TextMarshaler]()
	thisPkg     = reflect.TypeFor[Op]().PkgPath()
)

// selfWritten are types with their own MarshalJSON, by what they write.
// Any other type with one is unconstrained.
var selfWritten = map[reflect.Type]any{
	reflect.TypeFor[audit.Time]():             time.Time{},
	reflect.TypeFor[integration.TestResult](): testResult{},
	reflect.TypeFor[integration.Grant]():      grant{},
	reflect.TypeFor[integration.Num]():        (*float64)(nil),
	reflect.TypeFor[importer.Secret]():        "",
}

// testResult and grant are what integration.TestResult and Grant write.
type (
	testResult struct {
		OK        bool                     `json:"ok"`
		Version   string                   `json:"version"`
		LatencyMS int64                    `json:"latency_ms"`
		Grants    []integration.Grant      `json:"grants"`
		Warnings  []integration.Warning    `json:"warnings"`
		Error     *httpx.ErrorDetail       `json:"error"`
		Cert      *integration.Certificate `json:"certificate,omitempty"`
	}
	grant struct {
		Name   string `json:"name"`
		Access string `json:"access"`
		Write  bool   `json:"write"`
		Detail string `json:"detail,omitempty"`
	}
)

// of is the schema for a value of t.
func (s *schemas) of(t reflect.Type) map[string]any {
	if m, ok := selfWritten[t]; ok {
		return s.of(reflect.TypeOf(m))
	}
	switch {
	case t == timeType:
		return map[string]any{"type": "string", "format": "date-time"}
	case t == rawType:
		return map[string]any{}
	case t.Kind() == reflect.Pointer:
		return nullable(s.of(t.Elem()))
	}
	if t.Name() != "" && t.Kind() != reflect.Interface && (t.Implements(marshaler) || reflect.PointerTo(t).Implements(marshaler)) {
		// It writes itself: its shape isn't in its fields.
		return map[string]any{}
	}
	if t.Name() != "" && t.Kind() != reflect.String && (t.Implements(textMarshal) || reflect.PointerTo(t).Implements(textMarshal)) {
		return map[string]any{"type": "string"}
	}
	switch t.Kind() {
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int64:
		return map[string]any{"type": "integer", "format": "int64"}
	case reflect.Int8, reflect.Int16, reflect.Int32:
		return map[string]any{"type": "integer", "format": "int32"}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return map[string]any{"type": "integer", "minimum": 0}
	case reflect.Float32:
		return map[string]any{"type": "number", "format": "float"}
	case reflect.Float64:
		return map[string]any{"type": "number", "format": "double"}
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Interface:
		return map[string]any{}
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 && t.Kind() == reflect.Slice {
			return map[string]any{"type": "string", "contentEncoding": "base64"}
		}
		return map[string]any{"type": "array", "items": s.of(t.Elem())}
	case reflect.Map:
		return map[string]any{"type": "object", "additionalProperties": s.of(t.Elem())}
	case reflect.Struct:
		if t.Name() == "" || t.PkgPath() == thisPkg {
			return s.object(t)
		}
		return s.ref(t)
	}
	panic(fmt.Sprintf("apidoc: no schema for %s (%s)", t, t.Kind()))
}

// ref registers t as a component and points at it.
func (s *schemas) ref(t reflect.Type) map[string]any {
	name, ok := s.names[t]
	if !ok {
		name = componentName(t)
		if other, taken := s.owner[name]; taken && other != t {
			panic(fmt.Sprintf("apidoc: %s and %s would share the component name %q", t, other, name))
		}
		s.names[t], s.owner[name] = name, t
		s.components[name] = nil // reserved: a recursive type refers to itself
		s.components[name] = s.object(t)
	}
	return map[string]any{"$ref": "#/components/schemas/" + name}
}

// componentName is a type's stable name: its package path below
// internal/, dotted, then the type ("auth.User", "importer.homepage.Plan").
func componentName(t reflect.Type) string {
	p := t.PkgPath()
	if rest, ok := strings.CutPrefix(p, "dash.cha.sh/internal/"); ok {
		p = rest
	} else if i := strings.LastIndex(p, "/"); i >= 0 {
		p = p[i+1:]
	}
	name := strings.NewReplacer("/", ".").Replace(p) + "." + t.Name()
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-' {
			return r
		}
		return '_'
	}, name)
}

// field is one property of an object, with how deep it was promoted from.
type field struct {
	name     string
	schema   map[string]any
	required bool
	depth    int
}

// object is a struct's schema: its JSON fields, embedded structs' fields
// promoted as encoding/json promotes them (the shallower one wins).
func (s *schemas) object(t reflect.Type) map[string]any {
	var fields []field
	s.collect(t, 0, &fields)
	props, byName := map[string]any{}, map[string]field{}
	for _, f := range fields {
		if cur, ok := byName[f.name]; ok && cur.depth <= f.depth {
			continue
		}
		byName[f.name] = f
	}
	var required []string
	for _, f := range fields {
		if byName[f.name].depth != f.depth || props[f.name] != nil {
			continue
		}
		props[f.name] = f.schema
		if f.required {
			required = append(required, f.name)
		}
	}
	out := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func (s *schemas) collect(t reflect.Type, depth int, out *[]field) {
	for f := range t.Fields() {
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, opts, _ := strings.Cut(tag, ",")
		ft := f.Type
		if f.Anonymous && name == "" {
			et := ft
			if et.Kind() == reflect.Pointer {
				et = et.Elem()
			}
			if et.Kind() == reflect.Struct {
				s.collect(et, depth+1, out)
				continue
			}
		}
		if !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		var sc map[string]any
		if hasOpt(opts, "string") && isScalar(ft) {
			sc = map[string]any{"type": "string"}
		} else {
			sc = s.of(ft)
		}
		optional := hasOpt(opts, "omitempty") || hasOpt(opts, "omitzero")
		*out = append(*out, field{name: name, schema: sc, required: !optional, depth: depth})
	}
}

func hasOpt(opts, want string) bool {
	for o := range strings.SplitSeq(opts, ",") {
		if o == want {
			return true
		}
	}
	return false
}

func isScalar(t reflect.Type) bool {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Float32, reflect.Float64, reflect.String:
		return true
	}
	return false
}

// nullable admits null beside s: a pointer encodes nil as null.
func nullable(s map[string]any) map[string]any {
	if len(s) == 0 {
		return s // unconstrained already
	}
	if t, ok := s["type"].(string); ok && s["$ref"] == nil {
		out := map[string]any{}
		maps.Copy(out, s)
		out["type"] = []string{t, "null"}
		return out
	}
	return map[string]any{"anyOf": []any{s, map[string]any{"type": "null"}}}
}
