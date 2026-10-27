package integration

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Schema is the JSON Schema subset instance configs use. It marshals to
// standard JSON Schema (properties in declaration order, so forms can follow
// it) and validates decoded JSON. Supported keywords: type, title,
// description, properties, required, additionalProperties (false only),
// items, enum (with titles, as oneOf), default, format (uri, hostname), pattern, minLength,
// maxLength, minimum, maximum, minItems, maxItems and writeOnly, plus the
// extensions x-chadash-authorize and x-chadash-group. Anything a schema
// can't say goes in a ConfigValidator.
type Schema struct {
	Type        string // object, array, string, integer, number, boolean
	Title       string
	Description string
	Properties  []Property
	Required    []string
	// Closed rejects properties that aren't listed (additionalProperties:
	// false). The helpers set it on every object.
	Closed bool
	Items  *Schema
	Enum   []any
	// EnumTitles label Enum's values, one each, for forms. They marshal as
	// oneOf: [{const, title}] beside the enum.
	EnumTitles []string
	Default    any
	Format     string // "uri" or "hostname"
	Pattern    string
	MinLength  *int
	MaxLength  *int
	Minimum    *float64
	Maximum    *float64
	MinItems   *int
	MaxItems   *int
	// WriteOnly marks a secret: kept in the vault, never returned. Only
	// top-level string properties may be write-only.
	WriteOnly bool
	// Authorize marks a secret the integration's sign-in flow can fill in
	// (see Authorizer): "x-chadash-authorize": true, so the form offers a
	// "Sign in with …" button. Only on write-only properties.
	Authorize bool
	// Group puts a top-level property in a named group of the form:
	// "x-chadash-group": "control" marks the control credential's fields
	// (see Manifest.Control). GroupControl is the only group.
	Group string

	re *regexp.Regexp // Pattern, compiled by Check
}

// Property is a named member of an object schema.
type Property struct {
	Name   string
	Schema *Schema
}

// FieldError is one validation failure. Path is a JSON Pointer into the
// config ("/targets/0/url"); "" is the config itself.
type FieldError struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

func (e FieldError) String() string {
	if e.Path == "" {
		return e.Message
	}
	return e.Path + ": " + e.Message
}

// Prop pairs a name with a schema.
func Prop(name string, s *Schema) Property { return Property{Name: name, Schema: s} }

// Object is a closed object schema with the given properties.
func Object(props ...Property) *Schema {
	return &Schema{Type: "object", Properties: props, Closed: true}
}

// String is a string field.
func String(title string) *Schema { return &Schema{Type: "string", Title: title} }

// URL is an absolute URL field (format uri).
func URL(title string) *Schema { return &Schema{Type: "string", Title: title, Format: "uri"} }

// Secret is a write-only, non-empty string field.
func Secret(title string) *Schema {
	return &Schema{Type: "string", Title: title, WriteOnly: true, MinLength: new(1), MaxLength: new(8192)}
}

// Integer is an integer field within [min, max].
func Integer(title string, min, max float64) *Schema {
	return &Schema{Type: "integer", Title: title, Minimum: &min, Maximum: &max}
}

// Boolean is a boolean field.
func Boolean(title string) *Schema { return &Schema{Type: "boolean", Title: title} }

// Enum is a string field restricted to values.
func Enum(title string, values ...string) *Schema {
	s := &Schema{Type: "string", Title: title}
	for _, v := range values {
		s.Enum = append(s.Enum, v)
	}
	return s
}

// Titled labels an enum's values, in order, and returns s.
func (s *Schema) Titled(titles ...string) *Schema {
	s.EnumTitles = titles
	return s
}

// Array is an array of items.
func Array(title string, items *Schema) *Schema {
	return &Schema{Type: "array", Title: title, Items: items}
}

// URLProp is the standard "url" property: where the source is.
func URLProp() Property { return Prop("url", URL("URL")) }

// TLSProp is the standard "tls" property: {"mode", "pin"}.
func TLSProp() Property { return Prop("tls", TLSSchema()) }

// TLSSchema is the shape of every instance's tls key.
func TLSSchema() *Schema {
	s := Object(
		Prop("mode", Enum("Certificate check", string(TLSVerify), string(TLSPin), string(TLSTOFU), string(TLSInsecure)).WithDefault(string(TLSVerify))),
		Prop("pin", &Schema{Type: "string", Title: "SHA-256 fingerprint", Pattern: pinPattern}),
	)
	s.Title = "TLS"
	return s
}

// InGroup puts s in a form group (GroupControl) and returns s.
func (s *Schema) InGroup(g string) *Schema {
	s.Group = g
	return s
}

// Require marks properties as required and returns s.
func (s *Schema) Require(names ...string) *Schema {
	s.Required = append(s.Required, names...)
	return s
}

// Describe sets the description and returns s.
func (s *Schema) Describe(d string) *Schema {
	s.Description = d
	return s
}

// WithDefault sets the default and returns s.
func (s *Schema) WithDefault(v any) *Schema {
	s.Default = v
	return s
}

// Property returns the named property's schema, or nil.
func (s *Schema) Property(name string) *Schema {
	for _, p := range s.Properties {
		if p.Name == name {
			return p.Schema
		}
	}
	return nil
}

var (
	propNameRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	types      = []string{"object", "array", "string", "integer", "number", "boolean"}
)

// Check validates the schema itself, compiles patterns and normalises enum
// and default values to their JSON form. Register calls it on every
// manifest; call it before Validate on a schema built elsewhere.
func (s *Schema) Check() error {
	if s.Type != "object" {
		return errors.New("schema: the config schema must be an object")
	}
	return s.check("", true)
}

func (s *Schema) check(path string, top bool) error {
	fail := func(format string, a ...any) error {
		return fmt.Errorf("schema %s: %s", cmp(path, "/"), fmt.Sprintf(format, a...))
	}
	if !slices.Contains(types, s.Type) {
		return fail("unsupported type %q", s.Type)
	}
	if s.WriteOnly && (!top || s.Type != "string") {
		return fail("writeOnly is only allowed on top-level string properties")
	}
	if s.Authorize && !s.WriteOnly {
		return fail("x-chadash-authorize is only allowed on write-only properties")
	}
	if s.Group != "" && (!top || path == "") {
		return fail("x-chadash-group is only allowed on top-level properties")
	}
	if s.Group != "" && s.Group != GroupControl {
		return fail("x-chadash-group %q isn't %q", s.Group, GroupControl)
	}
	if len(s.Properties) > 0 && s.Type != "object" {
		return fail("properties on a %s", s.Type)
	}
	if s.Items != nil && s.Type != "array" {
		return fail("items on a %s", s.Type)
	}
	if s.Type == "array" && s.Items == nil {
		return fail("an array needs items")
	}
	if (s.Format != "" || s.Pattern != "" || s.MinLength != nil || s.MaxLength != nil) && s.Type != "string" {
		return fail("string keywords on a %s", s.Type)
	}
	if (s.Minimum != nil || s.Maximum != nil) && s.Type != "integer" && s.Type != "number" {
		return fail("minimum/maximum on a %s", s.Type)
	}
	if (s.MinItems != nil || s.MaxItems != nil) && s.Type != "array" {
		return fail("minItems/maxItems on a %s", s.Type)
	}
	switch s.Format {
	case "", "uri", "hostname":
	default:
		return fail("unsupported format %q", s.Format)
	}
	if s.Pattern != "" {
		re, err := regexp.Compile(s.Pattern)
		if err != nil {
			return fail("pattern: %v", err)
		}
		s.re = re
	}
	if len(s.EnumTitles) > 0 && len(s.EnumTitles) != len(s.Enum) {
		return fail("%d enum titles for %d values", len(s.EnumTitles), len(s.Enum))
	}
	for i, v := range s.Enum {
		n, err := normalize(v)
		if err != nil {
			return fail("enum: %v", err)
		}
		s.Enum[i] = n
	}
	if s.Default != nil {
		n, err := normalize(s.Default)
		if err != nil {
			return fail("default: %v", err)
		}
		s.Default = n
	}
	seen := map[string]bool{}
	for _, p := range s.Properties {
		if !propNameRE.MatchString(p.Name) {
			return fail("property name %q must match %s", p.Name, propNameRE)
		}
		if seen[p.Name] {
			return fail("duplicate property %q", p.Name)
		}
		seen[p.Name] = true
		if p.Schema == nil {
			return fail("property %q has no schema", p.Name)
		}
		if err := p.Schema.check(path+"/"+p.Name, top && path == ""); err != nil {
			return err
		}
	}
	for _, r := range s.Required {
		if !seen[r] {
			return fail("required property %q isn't defined", r)
		}
	}
	if s.Items != nil {
		if err := s.Items.check(path+"/items", false); err != nil {
			return err
		}
	}
	return nil
}

func cmp(a, b string) string {
	if a == "" {
		return b
	}
	return a
}

// normalize turns a Go value into its JSON form (float64 for numbers).
func normalize(v any) (any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out any
	err = json.Unmarshal(b, &out)
	return out, err
}

// Validate checks decoded JSON (maps, slices, strings, float64, bools)
// against the schema. Call Check first.
func (s *Schema) Validate(v any) []FieldError {
	var errs []FieldError
	s.validate(v, "", &errs)
	return errs
}

func (s *Schema) validate(v any, path string, errs *[]FieldError) {
	add := func(format string, a ...any) {
		*errs = append(*errs, FieldError{Path: path, Message: fmt.Sprintf(format, a...)})
	}
	if v == nil {
		add("must be %s, not null", article(s.Type))
		return
	}
	switch s.Type {
	case "object":
		m, ok := v.(map[string]any)
		if !ok {
			add("must be an object")
			return
		}
		for _, r := range s.Required {
			if x, ok := m[r]; !ok || x == nil || x == "" {
				*errs = append(*errs, FieldError{Path: path + "/" + escape(r), Message: "is required"})
			}
		}
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			ps := s.Property(k)
			if ps == nil {
				if s.Closed {
					*errs = append(*errs, FieldError{Path: path + "/" + escape(k), Message: "isn't a known field"})
				}
				continue
			}
			if x := m[k]; x != nil && !(x == "" && slices.Contains(s.Required, k)) {
				ps.validate(x, path+"/"+escape(k), errs)
			}
		}
	case "array":
		a, ok := v.([]any)
		if !ok {
			add("must be an array")
			return
		}
		if s.MinItems != nil && len(a) < *s.MinItems {
			add("needs at least %d %s", *s.MinItems, plural(*s.MinItems, "item"))
		}
		if s.MaxItems != nil && len(a) > *s.MaxItems {
			add("allows at most %d %s", *s.MaxItems, plural(*s.MaxItems, "item"))
		}
		for i, x := range a {
			s.Items.validate(x, path+"/"+strconv.Itoa(i), errs)
		}
	case "string":
		str, ok := v.(string)
		if !ok {
			add("must be a string")
			return
		}
		n := utf8.RuneCountInString(str)
		if s.MinLength != nil && n < *s.MinLength {
			add("must be at least %d %s", *s.MinLength, plural(*s.MinLength, "character"))
		}
		if s.MaxLength != nil && n > *s.MaxLength {
			add("must be at most %d %s", *s.MaxLength, plural(*s.MaxLength, "character"))
		}
		if re := s.pattern(); re != nil && !re.MatchString(str) {
			add("isn't in the expected format")
		}
		if msg := checkFormat(s.Format, str); msg != "" {
			add("%s", msg)
		}
	case "integer", "number":
		f, ok := v.(float64)
		if !ok {
			add("must be a number")
			return
		}
		if s.Type == "integer" && (f != math.Trunc(f) || math.Abs(f) > 1<<53) {
			add("must be a whole number")
			return
		}
		if s.Minimum != nil && f < *s.Minimum {
			add("must be at least %s", fmtNum(*s.Minimum))
		}
		if s.Maximum != nil && f > *s.Maximum {
			add("must be at most %s", fmtNum(*s.Maximum))
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			add("must be true or false")
			return
		}
	}
	if len(s.Enum) > 0 && !slices.ContainsFunc(s.Enum, func(e any) bool { return reflect.DeepEqual(e, v) }) {
		vals := make([]string, len(s.Enum))
		for i, e := range s.Enum {
			b, _ := json.Marshal(e)
			vals[i] = string(b)
		}
		add("must be one of %s", strings.Join(vals, ", "))
	}
}

// pattern is the compiled Pattern. A schema that skipped Check compiles it
// here each time rather than caching it, so Validate never writes to s.
func (s *Schema) pattern() *regexp.Regexp {
	if s.re != nil || s.Pattern == "" {
		return s.re
	}
	re, err := regexp.Compile(s.Pattern)
	if err != nil {
		return matchNothing
	}
	return re
}

func checkFormat(format, s string) string {
	switch format {
	case "uri":
		u, err := url.Parse(s)
		switch {
		case err != nil || u.Scheme == "" || !u.IsAbs():
			return "must be an absolute URL, like https://host:port"
		case u.User != nil:
			return "must not contain a username or password; use the credential fields"
		case u.Host == "" && u.Scheme != "unix":
			return "needs a host"
		}
	case "hostname":
		if s == "" || strings.ContainsAny(s, " /\\?#@") {
			return "must be a host name or IP address"
		}
		if net.ParseIP(strings.Trim(s, "[]")) == nil && !hostnameRE.MatchString(s) {
			return "must be a host name or IP address"
		}
	}
	return ""
}

var matchNothing = regexp.MustCompile(`[^\x00-\x{10FFFF}]`)

var hostnameRE = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,62})(\.[A-Za-z0-9]([A-Za-z0-9-]{0,62}))*\.?$`)

func article(t string) string {
	switch t {
	case "object", "array", "integer":
		return "an " + t
	}
	return "a " + t
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

func fmtNum(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

func escape(s string) string { return strings.NewReplacer("~", "~0", "/", "~1").Replace(s) }

// MarshalJSON writes standard JSON Schema with properties in order.
func (s *Schema) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	first := true
	field := func(name string, v any) error {
		if !first {
			b.WriteByte(',')
		}
		first = false
		k, _ := json.Marshal(name)
		b.Write(k)
		b.WriteByte(':')
		enc, err := json.Marshal(v)
		if err != nil {
			return err
		}
		b.Write(enc)
		return nil
	}
	var err error
	set := func(name string, v any, ok bool) {
		if ok && err == nil {
			err = field(name, v)
		}
	}
	set("type", s.Type, s.Type != "")
	set("title", s.Title, s.Title != "")
	set("description", s.Description, s.Description != "")
	set("format", s.Format, s.Format != "")
	set("enum", s.Enum, len(s.Enum) > 0)
	if len(s.EnumTitles) > 0 {
		type choice struct {
			Const any    `json:"const"`
			Title string `json:"title"`
		}
		oneOf := make([]choice, len(s.Enum))
		for i, v := range s.Enum {
			oneOf[i] = choice{v, s.EnumTitles[i]}
		}
		set("oneOf", oneOf, true)
	}
	set("default", s.Default, s.Default != nil)
	if len(s.Properties) > 0 && err == nil {
		if !first {
			b.WriteByte(',')
		}
		first = false
		b.WriteString(`"properties":{`)
		for i, p := range s.Properties {
			if i > 0 {
				b.WriteByte(',')
			}
			k, _ := json.Marshal(p.Name)
			b.Write(k)
			b.WriteByte(':')
			enc, perr := json.Marshal(p.Schema)
			if perr != nil {
				return nil, perr
			}
			b.Write(enc)
		}
		b.WriteByte('}')
	}
	set("required", s.Required, len(s.Required) > 0)
	set("additionalProperties", false, s.Closed && s.Type == "object")
	set("items", s.Items, s.Items != nil)
	set("pattern", s.Pattern, s.Pattern != "")
	set("minLength", s.MinLength, s.MinLength != nil)
	set("maxLength", s.MaxLength, s.MaxLength != nil)
	set("minimum", s.Minimum, s.Minimum != nil)
	set("maximum", s.Maximum, s.Maximum != nil)
	set("minItems", s.MinItems, s.MinItems != nil)
	set("maxItems", s.MaxItems, s.MaxItems != nil)
	set("writeOnly", true, s.WriteOnly)
	set("x-chadash-authorize", true, s.Authorize)
	set("x-chadash-group", s.Group, s.Group != "")
	if err != nil {
		return nil, err
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}
