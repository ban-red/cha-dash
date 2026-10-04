// Package spec is the Tier 1 integration engine: an integration written as a
// YAML file instead of Go. A spec names a few JSON requests, extracts values
// from the responses with gjson paths, and computes resources, metrics,
// health and repairs with expr-lang expressions, which are pure and always
// terminate.
//
// The format (apiVersion chadash/v1alpha1) is internal and unstable: it's
// how Cha Dash writes its own simple integrations from v0.1, and it may
// change until it's frozen for the community at v1.0. It's documented in
// internal/README.md; specs/seerr.yaml is a worked example.
//
// Safety: requests only go to the instance's configured origin, with its TLS
// policy, without leaving it on redirects; responses are capped at 1 MiB and
// must be JSON; secrets may appear only in headers, query parameters and
// bodies, never in paths, and are scrubbed from errors and logs by the
// runtime; a spec can't poll faster than every 10 seconds.
package spec

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
	"go.yaml.in/yaml/v3"

	"dash.cha.sh/internal/integration"
)

// APIVersion is the only format version this engine reads.
const APIVersion = "chadash/v1alpha1"

// Limits.
const (
	MinEvery        = 10 * time.Second
	DefaultEvery    = 60 * time.Second
	DefaultTimeout  = 10 * time.Second
	MaxTimeout      = 30 * time.Second
	MaxResponseSize = 1 << 20
	maxRequests     = 8
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
}

type meta struct {
	ID           string   `yaml:"id"`
	Name         string   `yaml:"name"`
	Aliases      []string `yaml:"aliases"`
	Icon         string   `yaml:"icon"`
	Version      string   `yaml:"version"`
	Tier         string   `yaml:"tier"`
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
	Min         *float64 `yaml:"min"`
	Max         *float64 `yaml:"max"`
}

// fields keeps the config's order, so the form follows the file.
type fields []field

func (f *fields) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: config must be a mapping of field names", n.Line)
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		var x field
		if err := n.Content[i+1].Decode(&x); err != nil {
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
		if err := n.Content[i+1].Decode(&x); err != nil {
			return err
		}
		x.Name = n.Content[i].Value
		*r = append(*r, x)
	}
	return nil
}

type resourceTpl struct {
	ID     string            `yaml:"id"` // local; the resource ID is <spec id>:<instance id>/<id>
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
	manifest integration.Manifest
	secrets  map[string]bool
	timeout  time.Duration
	version  *vm.Program
	res      []compiledResource
	metrics  []compiledMetric
	repairs  []compiledRepair
}

type compiledResource struct {
	tpl                         resourceTpl
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
	if f.APIVersion != APIVersion {
		return nil, fmt.Errorf("apiVersion must be %s", APIVersion)
	}
	if f.Kind != "Integration" {
		return nil, errors.New("kind must be Integration")
	}
	s := &Spec{f: f, secrets: map[string]bool{}, timeout: DefaultTimeout}
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
		}
	}
	caps := make([]integration.Capability, len(f.Meta.Capabilities))
	for i, c := range f.Meta.Capabilities {
		caps[i] = integration.Capability(c)
	}
	s.manifest = integration.Manifest{
		ID: f.Meta.ID, Name: f.Meta.Name, Aliases: f.Meta.Aliases, Icon: f.Meta.Icon, Version: f.Meta.Version,
		Tier: integration.Tier(f.Meta.Tier), Capabilities: caps, Engine: integration.EngineSpec,
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
	env := map[string]any{"config": map[string]any{}, "instance": map[string]any{}}
	seen := map[string]bool{}
	for i := range f.Requests {
		r := &f.Requests[i]
		where := "requests." + r.Name
		switch {
		case !nameRE.MatchString(r.Name) || slices.Contains(reserved, r.Name):
			return nil, fmt.Errorf("%s: request names are lowercase identifiers, not %s", where, strings.Join(reserved, ", "))
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

	program := func(where, src string) (*vm.Program, error) {
		if strings.TrimSpace(src) == "" {
			return nil, nil
		}
		p, err := expr.Compile(src, expr.Env(env), expr.MaxNodes(500))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", where, err)
		}
		return p, nil
	}
	var err error
	if s.version, err = program("version", f.Version); err != nil {
		return nil, err
	}
	ids := map[string]bool{}
	for i, r := range f.Resources {
		where := fmt.Sprintf("resources[%d]", i)
		if !localIDRE.MatchString(r.ID) || ids[r.ID] {
			return nil, fmt.Errorf("%s: id %q must be a unique lowercase slug", where, r.ID)
		}
		if r.Kind == "" || strings.ContainsAny(r.Kind, " \t") {
			return nil, fmt.Errorf("%s: needs a kind", where)
		}
		ids[r.ID] = true
		c := compiledResource{tpl: r, attrs: map[string]*vm.Program{}}
		for _, x := range []struct {
			dst  **vm.Program
			name string
			src  string
		}{{&c.name, "name", r.Name}, {&c.state, "state", r.State}, {&c.health, "health", r.Health}, {&c.reason, "reason", r.Reason}} {
			if *x.dst, err = program(where+"."+x.name, x.src); err != nil {
				return nil, err
			}
		}
		for k, src := range r.Attrs {
			if c.attrs[k], err = program(where+".attrs."+k, src); err != nil {
				return nil, err
			}
		}
		for j, l := range r.Links {
			p, err := program(fmt.Sprintf("%s.links[%d]", where, j), l.URL)
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
		case !ids[m.Resource]:
			return nil, fmt.Errorf("%s: resource %q isn't one of the spec's resources", where, m.Resource)
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
	if f.Type == "secret" && f.Default != nil {
		return nil, errors.New("a secret can't have a default")
	}
	s.Description, s.Default = f.Description, f.Default
	return s, nil
}

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
