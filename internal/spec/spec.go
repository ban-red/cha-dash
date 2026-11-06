// Package spec is the Tier 1 integration engine: an integration written as a
// YAML file instead of Go. A spec names a few JSON requests, extracts values
// from the responses with gjson paths, and computes resources, metrics,
// health and repairs with expr-lang expressions, which are pure and always
// terminate.
//
// The format is public and versioned (ADR-0025): chadash/v1beta1 is the
// current one, and chadash/v1alpha1 keeps loading, with its own semantics,
// through v0.5.x. Migrate rewrites a v1alpha1 spec as v1beta1 (migrate.go).
// It changes only by a new apiVersion until it's frozen as v1 at 1.0. It's
// documented in docs/specs.md and internal/README.md; specs/seerr.yaml is a
// worked example.
//
// What v1beta1 changed, from writing the bundled specs in v1alpha1:
//   - a config field's default is its value when the instance leaves it
//     unset, in expressions and templates (v1alpha1: null, and "<nil>" in a
//     template), so `config.x ?? 14` beside `default: 14` isn't needed;
//   - now() is the hub's clock (integration.Deps.Now), not the wall clock,
//     so a test can pin it instead of moving fixture dates to the present;
//   - findIndex and findLastIndex return -1 when nothing matches
//     (v1alpha1: null, hence `?? -1` after each).
//
// Safety: requests only go to the instance's configured origin, with its TLS
// policy, without leaving it on redirects; responses are capped at 1 MiB and
// must be JSON; secrets may appear only in headers, query parameters and
// bodies, never in paths, and are scrubbed from errors and logs by the
// runtime; a spec can't poll faster than every 10 seconds.
//
// Actions (v0.2, actions.go): a spec with an actions section implements
// integration.Actor. Targets are addressed by a param checked against an
// anchored pattern, resolved by a templated GET; writes are one POST, PUT or
// DELETE to a templated path whose values are escaped as single segments,
// with the source headers and never a secret in a path or body.
package spec

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/ast"
	"github.com/expr-lang/expr/vm"
	"go.yaml.in/yaml/v3"

	"dash.cha.sh/internal/integration"
)

// The format versions this engine reads. APIVersion is the current one,
// which spec new and spec migrate write.
const (
	V1Alpha1   = "chadash/v1alpha1"
	V1Beta1    = "chadash/v1beta1"
	APIVersion = V1Beta1
)

// Versions are the format versions this engine reads, oldest first.
var Versions = []string{V1Alpha1, V1Beta1}

// Limits.
const (
	MinEvery        = 10 * time.Second
	DefaultEvery    = 60 * time.Second
	DefaultTimeout  = 10 * time.Second
	MaxTimeout      = 30 * time.Second
	MaxResponseSize = 1 << 20
	maxRequests     = 8
	maxEach         = 50 // resources one each template publishes
)

// file is a spec as written.
type file struct {
	APIVersion string        `yaml:"apiVersion"`
	Kind       string        `yaml:"kind"`
	Meta       meta          `yaml:"meta"`
	Config     fields        `yaml:"config"`
	Source     source        `yaml:"source"`
	Every      string        `yaml:"every"`
	Requests   requests      `yaml:"requests"`
	Version    string        `yaml:"version"`
	Resources  []resourceTpl `yaml:"resources"`
	Metrics    []metricTpl   `yaml:"metrics"`
	Repairs    []repairTpl   `yaml:"repairs"`
	Targets    targets       `yaml:"targets"`
	Actions    []actionTpl   `yaml:"actions"`
}

type meta struct {
	ID           string   `yaml:"id"`
	Name         string   `yaml:"name"`
	Aliases      []string `yaml:"aliases"`
	Icon         string   `yaml:"icon"`
	Version      string   `yaml:"version"`
	Tier         string   `yaml:"tier"`
	Category     string   `yaml:"category"`
	Capabilities []string `yaml:"capabilities"`
}

// field is one config key besides url and tls.
type field struct {
	Name        string
	Type        string   `yaml:"type"` // string, url, secret, integer, number, boolean
	Title       string   `yaml:"title"`
	Description string   `yaml:"description"`
	Required    bool     `yaml:"required"`
	Default     any      `yaml:"default"`
	Enum        []string `yaml:"enum"`
	Pattern     string   `yaml:"pattern"` // strings: an anchored regular expression
	MaxLength   *int     `yaml:"max_length"`
	Min         *float64 `yaml:"min"`
	Max         *float64 `yaml:"max"`
}

// decodeStrict decodes n into v and refuses a key v doesn't have, as Parse
// does at the top level: yaml.v3's Node.Decode doesn't, so a typo inside a
// request or a config field would otherwise be dropped without a word.
func decodeStrict(n *yaml.Node, v any) error {
	b, err := yaml.Marshal(n)
	if err != nil {
		return err
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("line %d: %w", n.Line, err)
	}
	return nil
}

// fields keeps the config's order, so the form follows the file.
type fields []field

func (f *fields) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: config must be a mapping of field names", n.Line)
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		var x field
		if err := decodeStrict(n.Content[i+1], &x); err != nil {
			return err
		}
		x.Name = n.Content[i].Value
		*f = append(*f, x)
	}
	return nil
}

type source struct {
	Headers map[string]string `yaml:"headers"`
	Timeout string            `yaml:"timeout"`
}

type request struct {
	Name     string
	Method   string            `yaml:"method"`
	Path     string            `yaml:"path"`
	Query    map[string]string `yaml:"query"`
	Headers  map[string]string `yaml:"headers"`
	Body     string            `yaml:"body"`
	Extract  map[string]string `yaml:"extract"` // variable → gjson path
	Optional bool              `yaml:"optional"`
}

// requests keeps the file's order: they run in it.
type requests []request

func (r *requests) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: requests must be a mapping of request names", n.Line)
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		var x request
		if err := decodeStrict(n.Content[i+1], &x); err != nil {
			return err
		}
		x.Name = n.Content[i].Value
		*r = append(*r, x)
	}
	return nil
}

type resourceTpl struct {
	ID     string            `yaml:"id"`      // local; the resource ID is <spec id>:<instance id>/<id>
	Each   string            `yaml:"each"`    // expr → list: one resource per element (as item), at most maxEach
	Key    string            `yaml:"key"`     // with each: expr → the element's slug; the ID is …/<id>/<key>
	PartOf string            `yaml:"part_of"` // a resource's local id: this one is part_of it
	Kind   string            `yaml:"kind"`
	Name   string            `yaml:"name"`   // expr; default: the instance name
	State  string            `yaml:"state"`  // expr
	Health string            `yaml:"health"` // expr → ok | attention | down | unknown | maintenance; default ok
	Reason string            `yaml:"reason"` // expr
	Attrs  map[string]string `yaml:"attrs"`  // attr → expr; null results are left out
	Labels map[string]string `yaml:"labels"` // literal
	Caps   []string          `yaml:"caps"`
	Links  []linkTpl         `yaml:"links"`
}

type linkTpl struct {
	Title string `yaml:"title"` // literal
	URL   string `yaml:"url"`   // expr
}

type metricTpl struct {
	Resource string     `yaml:"resource"` // a resource's local id
	Key      string     `yaml:"key"`
	Value    string     `yaml:"value"` // expr → number; null is unknown
	Unit     string     `yaml:"unit"`
	Min      float64    `yaml:"min"`
	Max      float64    `yaml:"max"`
	Band     [2]float64 `yaml:"band"`
	Warn     float64    `yaml:"warn"`
	Crit     float64    `yaml:"crit"`
}

type repairTpl struct {
	When    string `yaml:"when"`    // expr → bool
	Code    string `yaml:"code"`    // default api_changed
	Message string `yaml:"message"` // expr → string
}

// Spec is a parsed, compiled spec. It implements integration.Integration.
type Spec struct {
	f        file
	beta     bool           // v1beta1's semantics (see the package doc)
	defaults map[string]any // v1beta1: non-secret config defaults, as JSON has them
	manifest integration.Manifest
	secrets  map[string]bool
	timeout  time.Duration
	version  *vm.Program
	res      []compiledResource
	metrics  []compiledMetric
	repairs  []compiledRepair
	targets  map[string]*compiledTarget
	actions  []*compiledAction
}

type compiledResource struct {
	tpl                         resourceTpl
	each, key                   *vm.Program
	name, state, health, reason *vm.Program
	attrs                       map[string]*vm.Program
	links                       []*vm.Program
}

type compiledMetric struct {
	tpl   metricTpl
	value *vm.Program
}

type compiledRepair struct {
	code      string
	when, msg *vm.Program
}

var (
	nameRE     = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	localIDRE  = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	keyRE      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	metricRE   = regexp.MustCompile(`^[a-z0-9][a-z0-9._]{0,63}$`)
	templateRE = regexp.MustCompile(`\{\{\s*([a-z]+)\.([a-z_][a-z0-9_]*)\s*\}\}`)
	reserved   = []string{"config", "instance", "secret", "url", "tls"}
)

// Parse reads and compiles a spec.
func Parse(b []byte) (*Spec, error) {
	var f file
	dec := yaml.NewDecoder(strings.NewReader(string(b)))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("spec: %w", err)
	}
	s, err := compile(f)
	if err != nil {
		id := f.Meta.ID
		if id == "" {
			id = "?"
		}
		return nil, fmt.Errorf("spec %s: %w", id, err)
	}
	return s, nil
}

func compile(f file) (*Spec, error) {
	if !slices.Contains(Versions, f.APIVersion) {
		return nil, fmt.Errorf("apiVersion must be %s (or %s, which still loads)", APIVersion, V1Alpha1)
	}
	if f.Kind != "Integration" {
		return nil, errors.New("kind must be Integration")
	}
	s := &Spec{f: f, beta: f.APIVersion == V1Beta1, secrets: map[string]bool{}, timeout: DefaultTimeout}
	if f.Source.Timeout != "" {
		d, err := time.ParseDuration(f.Source.Timeout)
		if err != nil || d <= 0 || d > MaxTimeout {
			return nil, fmt.Errorf("source.timeout must be a duration up to %v", MaxTimeout)
		}
		s.timeout = d
	}
	every := DefaultEvery
	if f.Every != "" {
		d, err := time.ParseDuration(f.Every)
		if err != nil || d < MinEvery {
			return nil, fmt.Errorf("every must be a duration of at least %v", MinEvery)
		}
		every = d
	}

	// Config → JSON Schema. url and tls are always there.
	props := []integration.Property{integration.URLProp(), integration.TLSProp()}
	required := []string{"url"}
	plain := map[string]bool{"url": true}
	for _, fl := range f.Config {
		if !nameRE.MatchString(fl.Name) || slices.Contains(reserved, fl.Name) {
			return nil, fmt.Errorf("config field %q: names are lowercase identifiers, not url or tls", fl.Name)
		}
		sc, err := fieldSchema(fl)
		if err != nil {
			return nil, fmt.Errorf("config field %s: %w", fl.Name, err)
		}
		props = append(props, integration.Prop(fl.Name, sc))
		if fl.Required {
			required = append(required, fl.Name)
		}
		if fl.Type == "secret" {
			s.secrets[fl.Name] = true
		} else {
			plain[fl.Name] = true
			if s.beta && fl.Default != nil {
				if s.defaults == nil {
					s.defaults = map[string]any{}
				}
				s.defaults[fl.Name] = jsonValue(fl.Default)
			}
		}
	}
	caps := make([]integration.Capability, len(f.Meta.Capabilities))
	for i, c := range f.Meta.Capabilities {
		caps[i] = integration.Capability(c)
	}
	s.manifest = integration.Manifest{
		ID: f.Meta.ID, Name: f.Meta.Name, Aliases: f.Meta.Aliases, Icon: f.Meta.Icon, Version: f.Meta.Version,
		Tier: integration.Tier(f.Meta.Tier), Category: integration.Category(f.Meta.Category), Capabilities: caps, Engine: integration.EngineSpec,
		ConfigSchema: integration.Object(props...).Require(required...),
		Poll:         integration.Poll{Interval: every, Timeout: min(MaxTimeout, s.timeout*time.Duration(max(1, len(f.Requests)))+5*time.Second)},
	}
	if err := integration.ValidateManifest(s.manifest); err != nil {
		return nil, err
	}

	// Templates: secrets only where they can't end up in a URL path.
	checkTpl := func(where, text string, allowSecret bool) error {
		for _, m := range templateRE.FindAllStringSubmatch(text, -1) {
			switch m[1] {
			case "secret":
				if !allowSecret {
					return fmt.Errorf("%s: secrets can't appear in a path", where)
				}
				if !s.secrets[m[2]] {
					return fmt.Errorf("%s: {{ secret.%s }} isn't a secret field", where, m[2])
				}
			case "config":
				if !plain[m[2]] {
					return fmt.Errorf("%s: {{ config.%s }} isn't a config field (secrets are {{ secret.… }})", where, m[2])
				}
			case "instance":
				if m[2] != "id" && m[2] != "name" {
					return fmt.Errorf("%s: {{ instance.%s }}: only instance.id and instance.name", where, m[2])
				}
			default:
				return fmt.Errorf("%s: unknown template %q", where, m[0])
			}
		}
		if strings.Count(text, "{{") != len(templateRE.FindAllString(text, -1)) {
			return fmt.Errorf("%s: malformed template", where)
		}
		return nil
	}
	for k, v := range f.Source.Headers {
		if err := checkTpl("source.headers."+k, v, true); err != nil {
			return nil, err
		}
	}

	if len(f.Requests) == 0 || len(f.Requests) > maxRequests {
		return nil, fmt.Errorf("a spec needs 1–%d requests", maxRequests)
	}
	env := s.compileEnv()
	seen := map[string]bool{}
	for i := range f.Requests {
		r := &f.Requests[i]
		where := "requests." + r.Name
		switch {
		case !nameRE.MatchString(r.Name) || slices.Contains(reserved, r.Name):
			return nil, fmt.Errorf("%s: request names are lowercase identifiers, not %s", where, strings.Join(reserved, ", "))
		case s.beta && r.Name == "now":
			return nil, fmt.Errorf("%s: now is the clock in %s; name the request something else", where, V1Beta1)
		case seen[r.Name]:
			return nil, fmt.Errorf("%s: listed twice", where)
		case !strings.HasPrefix(r.Path, "/") || strings.HasPrefix(r.Path, "//") || strings.ContainsAny(r.Path, "#\\") || strings.Contains(r.Path, ".."):
			return nil, fmt.Errorf("%s: path must be an absolute path on the instance's origin", where)
		case len(r.Extract) == 0:
			return nil, fmt.Errorf("%s: extract at least one value", where)
		}
		seen[r.Name] = true
		r.Method = strings.ToUpper(cmp(r.Method, "GET"))
		if r.Method != "GET" && r.Method != "POST" {
			return nil, fmt.Errorf("%s: method must be GET or POST", where)
		}
		if r.Body != "" && r.Method != "POST" {
			return nil, fmt.Errorf("%s: only a POST has a body", where)
		}
		if err := checkTpl(where+".path", r.Path, false); err != nil {
			return nil, err
		}
		for k, v := range r.Query {
			if err := checkTpl(where+".query."+k, v, true); err != nil {
				return nil, err
			}
		}
		for k, v := range r.Headers {
			if err := checkTpl(where+".headers."+k, v, true); err != nil {
				return nil, err
			}
		}
		if err := checkTpl(where+".body", r.Body, true); err != nil {
			return nil, err
		}
		for k, p := range r.Extract {
			if !nameRE.MatchString(k) || p == "" {
				return nil, fmt.Errorf("%s.extract: %q needs a lowercase name and a gjson path", where, k)
			}
		}
		env[r.Name] = map[string]any{}
	}

	programIn := func(env map[string]any, where, src string) (*vm.Program, error) {
		if strings.TrimSpace(src) == "" {
			return nil, nil
		}
		p, err := s.compileExpr(env, src)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", where, err)
		}
		return p, nil
	}
	program := func(where, src string) (*vm.Program, error) { return programIn(env, where, src) }
	// An each template's expressions also see the element, as item.
	itemEnv := make(map[string]any, len(env)+1)
	maps.Copy(itemEnv, env)
	itemEnv["item"] = map[string]any{}
	var err error
	if s.version, err = program("version", f.Version); err != nil {
		return nil, err
	}
	ids := map[string]bool{}    // every local id
	single := map[string]bool{} // the ones without each: metrics and part_of may name them
	for i, r := range f.Resources {
		where := fmt.Sprintf("resources[%d]", i)
		if !localIDRE.MatchString(r.ID) || ids[r.ID] {
			return nil, fmt.Errorf("%s: id %q must be a unique lowercase slug", where, r.ID)
		}
		if r.Kind == "" || strings.ContainsAny(r.Kind, " \t") {
			return nil, fmt.Errorf("%s: needs a kind", where)
		}
		if (r.Each == "") != (r.Key == "") {
			return nil, fmt.Errorf("%s: each and key go together", where)
		}
		if r.PartOf != "" && !single[r.PartOf] {
			return nil, fmt.Errorf("%s: part_of %q must be an earlier resource without each", where, r.PartOf)
		}
		ids[r.ID] = true
		penv := env
		if r.Each == "" {
			single[r.ID] = true
		} else {
			penv = itemEnv
		}
		c := compiledResource{tpl: r, attrs: map[string]*vm.Program{}}
		if c.each, err = program(where+".each", r.Each); err != nil {
			return nil, err
		}
		for _, x := range []struct {
			dst  **vm.Program
			name string
			src  string
		}{{&c.key, "key", r.Key}, {&c.name, "name", r.Name}, {&c.state, "state", r.State}, {&c.health, "health", r.Health}, {&c.reason, "reason", r.Reason}} {
			if *x.dst, err = programIn(penv, where+"."+x.name, x.src); err != nil {
				return nil, err
			}
		}
		for k, src := range r.Attrs {
			if c.attrs[k], err = programIn(penv, where+".attrs."+k, src); err != nil {
				return nil, err
			}
		}
		for j, l := range r.Links {
			p, err := programIn(penv, fmt.Sprintf("%s.links[%d]", where, j), l.URL)
			if err != nil {
				return nil, err
			}
			c.links = append(c.links, p)
		}
		s.res = append(s.res, c)
	}
	keys := map[string]bool{}
	for i, m := range f.Metrics {
		where := fmt.Sprintf("metrics[%d]", i)
		switch {
		case !single[m.Resource]:
			return nil, fmt.Errorf("%s: resource %q isn't one of the spec's resources without each", where, m.Resource)
		case !metricRE.MatchString(m.Key) || keys[m.Resource+"#"+m.Key]:
			return nil, fmt.Errorf("%s: key %q must be a unique metric key", where, m.Key)
		case m.Value == "":
			return nil, fmt.Errorf("%s: needs a value", where)
		}
		keys[m.Resource+"#"+m.Key] = true
		p, err := program(where+".value", m.Value)
		if err != nil {
			return nil, err
		}
		s.metrics = append(s.metrics, compiledMetric{tpl: m, value: p})
	}
	for i, r := range f.Repairs {
		where := fmt.Sprintf("repairs[%d]", i)
		if r.When == "" || r.Message == "" {
			return nil, fmt.Errorf("%s: needs when and message", where)
		}
		c := compiledRepair{code: cmp(r.Code, integration.CodeAPIChanged)}
		if c.when, err = program(where+".when", r.When); err != nil {
			return nil, err
		}
		if c.msg, err = program(where+".message", r.Message); err != nil {
			return nil, err
		}
		s.repairs = append(s.repairs, c)
	}
	if err := s.compileActions(); err != nil {
		return nil, err
	}
	return s, nil
}

func fieldSchema(f field) (*integration.Schema, error) {
	var s *integration.Schema
	switch f.Type {
	case "string", "":
		s = integration.String(f.Title)
		for _, e := range f.Enum {
			s.Enum = append(s.Enum, e)
		}
		if f.Pattern != "" && (!strings.HasPrefix(f.Pattern, "^") || !strings.HasSuffix(f.Pattern, "$")) {
			return nil, errors.New("a pattern must be anchored with ^ and $")
		}
		s.Pattern, s.MaxLength = f.Pattern, f.MaxLength
	case "url":
		s = integration.URL(f.Title)
	case "secret":
		s = integration.Secret(f.Title)
	case "integer", "number":
		s = &integration.Schema{Type: f.Type, Title: f.Title, Minimum: f.Min, Maximum: f.Max}
	case "boolean":
		s = integration.Boolean(f.Title)
	default:
		return nil, fmt.Errorf("type %q isn't string, url, secret, integer, number or boolean", f.Type)
	}
	if (f.Pattern != "" || f.MaxLength != nil) && f.Type != "string" && f.Type != "" {
		return nil, errors.New("pattern and max_length are for strings")
	}
	if f.Type == "secret" && f.Default != nil {
		return nil, errors.New("a secret can't have a default")
	}
	s.Description, s.Default = f.Description, f.Default
	return s, nil
}

// int64Builtin is expr-lang's int() at 64 bits on every build. Its own is
// Go's int, 32 bits on the armv7 image, where a byte count or Unix
// milliseconds overflow it. Otherwise it's the same: a number truncates
// toward zero, a string parses as a whole number.
var int64Builtin = expr.Function("int", func(params ...any) (any, error) {
	switch v := params[0].(type) {
	case int:
		return int64(v), nil
	case int8:
		return int64(v), nil
	case int16:
		return int64(v), nil
	case int32:
		return int64(v), nil
	case int64:
		return v, nil
	case uint:
		return int64(v), nil
	case uint8:
		return int64(v), nil
	case uint16:
		return int64(v), nil
	case uint32:
		return int64(v), nil
	case uint64:
		return int64(v), nil
	case float32:
		return int64(v), nil
	case float64:
		return int64(v), nil
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid operation: int(%q)", v)
		}
		return n, nil
	}
	return nil, fmt.Errorf("invalid operation: int(%T)", params[0])
}, new(func(any) int64))

// compileEnv is what every expression sees at compile time; requests add
// their names.
func (s *Spec) compileEnv() map[string]any {
	env := map[string]any{"config": map[string]any{}, "instance": map[string]any{}}
	if s.beta {
		env["now"] = time.Now // the type; the instance's clock at run time
	}
	return env
}

// compileExpr compiles one expression in env, with the version's semantics.
func (s *Spec) compileExpr(env map[string]any, src string) (*vm.Program, error) {
	opts := []expr.Option{expr.Env(env), expr.MaxNodes(500), int64Builtin}
	if s.beta {
		opts = append(opts, expr.Patch(minusOne{}))
	}
	return expr.Compile(src, opts...)
}

// minusOne makes findIndex and findLastIndex -1 when nothing matches
// (v1beta1), as findIndex(…) ?? -1: expr-lang's own answer is nil.
type minusOne struct{}

func (minusOne) Visit(n *ast.Node) {
	b, ok := (*n).(*ast.BuiltinNode)
	if !ok || (b.Name != "findIndex" && b.Name != "findLastIndex") {
		return
	}
	ast.Patch(n, &ast.BinaryNode{Operator: "??", Left: b, Right: &ast.IntegerNode{Value: -1}})
}

// jsonValue is v as encoding/json decodes it, the way an instance's config
// arrives: a YAML default of 14 is 14.0, like the owner's 14.
func jsonValue(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var out any
	if json.Unmarshal(b, &out) != nil {
		return v
	}
	return out
}

// APIVersion is the format version the spec is written in.
func (s *Spec) APIVersion() string { return s.f.APIVersion }

func cmp(a, b string) string {
	if a == "" {
		return b
	}
	return a
}

// Manifest implements integration.Integration.
func (s *Spec) Manifest() integration.Manifest { return s.manifest }

// RegisterFS parses every *.yaml at the root of fsys and registers it.
func RegisterFS(fsys fs.FS, reg *integration.Registry) error {
	names, err := fs.Glob(fsys, "*.yaml")
	if err != nil {
		return err
	}
	for _, n := range names {
		b, err := fs.ReadFile(fsys, n)
		if err != nil {
			return err
		}
		s, err := Parse(b)
		if err != nil {
			return fmt.Errorf("%s: %w", n, err)
		}
		if want := strings.TrimSuffix(path.Base(n), ".yaml"); s.manifest.ID != want {
			return fmt.Errorf("%s: meta.id %q must match the file name", n, s.manifest.ID)
		}
		if err := reg.Register(s); err != nil {
			return err
		}
	}
	return nil
}
