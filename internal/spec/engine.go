package spec

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
	"github.com/tidwall/gjson"

	"dash.cha.sh/internal/integration"
)

// Connect implements integration.Integration.
func (s *Spec) Connect(_ context.Context, cfg integration.Config, d integration.Deps) (integration.Instance, error) {
	base, err := url.Parse(cfg.String("url"))
	if err != nil || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") {
		return nil, &integration.ConfigError{Message: "url must be an http:// or https:// URL"}
	}
	base.RawQuery, base.Fragment = "", ""
	in := &instance{s: s, cfg: cfg, d: d, base: base, plain: map[string]any{}, secret: map[string]string{}}
	for k, v := range cfg.Values {
		if s.secrets[k] {
			in.secret[k], _ = v.(string)
		} else {
			in.plain[k] = v
		}
	}
	if len(s.actions) > 0 {
		return actor{in}, nil
	}
	return in, nil
}

type instance struct {
	s      *Spec
	cfg    integration.Config
	d      integration.Deps
	base   *url.URL
	plain  map[string]any    // non-secret config: expressions may read it
	secret map[string]string // secret values: templates only
}

func (in *instance) Close() error { return nil }

// key is the instance's part of resource IDs.
func (in *instance) key() string { return cmp(in.cfg.InstanceID, "test") }

// expand fills {{ config.x }}, {{ secret.x }} and {{ instance.id|name }}.
// Compile already checked which may appear where.
func (in *instance) expand(text string) string {
	return templateRE.ReplaceAllStringFunc(text, func(m string) string {
		sub := templateRE.FindStringSubmatch(m)
		switch sub[1] {
		case "secret":
			return in.secret[sub[2]]
		case "config":
			return fmt.Sprint(in.plain[sub[2]])
		case "instance":
			if sub[2] == "id" {
				return in.key()
			}
			return in.cfg.Name
		}
		return ""
	})
}

func (in *instance) env() map[string]any {
	return map[string]any{
		"config":   in.plain,
		"instance": map[string]any{"id": in.key(), "name": in.cfg.Name},
	}
}

// fetch runs the requests in order and returns the expression environment
// and the upstream version.
func (in *instance) fetch(ctx context.Context) (map[string]any, string, error) {
	env := in.env()
	for _, r := range in.s.f.Requests {
		vals, err := in.do(ctx, r, env)
		if err != nil {
			if r.Optional && ctx.Err() == nil {
				env[r.Name] = map[string]any{}
				continue
			}
			// An endpoint that's gone is often the version's doing (an old
			// release without it): a repair that already holds on what was
			// read so far says so better than "the API changed". Conditions
			// that can't be evaluated on part of the data are skipped.
			var re *integration.RepairError
			if errors.As(err, &re) && re.Code == integration.CodeAPIChanged {
				if rerr, _ := in.repair(fill(env, in.s.f.Requests)); rerr != nil {
					return env, in.versionOf(env), rerr
				}
			}
			return env, in.versionOf(env), err
		}
		env[r.Name] = vals
	}
	version := in.versionOf(env)
	rerr, err := in.repair(env)
	if err != nil {
		return env, version, err
	}
	if rerr != nil {
		return env, version, rerr
	}
	return env, version, nil
}

// repair returns the first of the spec's repairs whose condition holds, or
// nil. The second result is a condition or message that failed to evaluate.
func (in *instance) repair(env map[string]any) (*integration.RepairError, error) {
	for _, rp := range in.s.repairs {
		hit, err := run(rp.when, env)
		if err != nil {
			return nil, specErr("repair condition", err)
		}
		if b, _ := hit.(bool); b {
			msg, err := run(rp.msg, env)
			if err != nil {
				return nil, specErr("repair message", err)
			}
			return &integration.RepairError{Code: rp.code, Message: fmt.Sprint(msg)}, nil
		}
	}
	return nil, nil
}

func (in *instance) versionOf(env map[string]any) string {
	if in.s.version == nil {
		return ""
	}
	v, err := run(in.s.version, fill(env, in.s.f.Requests))
	if err != nil || v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

// fill gives requests that haven't run an empty value, so expressions can
// be evaluated part way through.
func fill(env map[string]any, rs requests) map[string]any {
	out := make(map[string]any, len(env)+len(rs))
	for k, v := range env {
		out[k] = v
	}
	for _, r := range rs {
		if _, ok := out[r.Name]; !ok {
			out[r.Name] = map[string]any{}
		}
	}
	return out
}

func (in *instance) do(ctx context.Context, r request, env map[string]any) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, in.s.timeout)
	defer cancel()
	u := *in.base
	p := in.expand(r.Path)
	rawQuery := ""
	if i := strings.IndexByte(p, '?'); i >= 0 {
		p, rawQuery = p[:i], p[i+1:]
	}
	u.Path = strings.TrimSuffix(in.base.Path, "/") + p
	u.RawPath = ""
	q, err := url.ParseQuery(rawQuery)
	if err != nil {
		return nil, specErr("requests."+r.Name+".path", err)
	}
	for k, v := range r.Query {
		q.Set(k, in.expand(v))
	}
	u.RawQuery = q.Encode()
	var body io.Reader
	if r.Body != "" {
		body = strings.NewReader(in.expand(r.Body))
	}
	req, err := http.NewRequestWithContext(ctx, r.Method, u.String(), body)
	if err != nil {
		return nil, specErr("requests."+r.Name, err)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range in.s.f.Source.Headers {
		req.Header.Set(k, in.expand(v))
	}
	for k, v := range r.Headers {
		req.Header.Set(k, in.expand(v))
	}
	resp, err := in.d.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := integration.CheckResponse(resp); err != nil {
		var se *integration.StatusError
		if errors.As(err, &se) && (se.Code == http.StatusNotFound || se.Code == http.StatusGone) {
			msg := fmt.Sprintf("%s's API changed: %s", in.s.manifest.Name, se.Error())
			if v := in.versionOf(env); v != "" {
				msg += fmt.Sprintf(" (detected version %s)", v)
			}
			return nil, &integration.RepairError{Code: integration.CodeAPIChanged, Message: msg}
		}
		return nil, err
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseSize+1))
	if err != nil {
		return nil, err
	}
	if len(b) > MaxResponseSize {
		return nil, fmt.Errorf("%s %s answered more than %d bytes", r.Method, u.Path, MaxResponseSize)
	}
	if !gjson.ValidBytes(b) {
		return nil, fmt.Errorf("%s %s didn't answer JSON", r.Method, u.Path)
	}
	vals := make(map[string]any, len(r.Extract))
	for name, path := range r.Extract {
		res := gjson.GetBytes(b, path)
		if res.Exists() {
			vals[name] = res.Value()
		} else {
			vals[name] = nil
		}
	}
	return vals, nil
}

func run(p *vm.Program, env map[string]any) (any, error) {
	if p == nil {
		return nil, nil
	}
	return expr.Run(p, env)
}

// specErr is a spec that can't do what it says: a bug in the spec or an
// upstream shape it didn't expect. It's retried like any transient error.
func specErr(where string, err error) error {
	return fmt.Errorf("the spec failed at %s: %w", where, err)
}

// Test implements integration.Instance: run every request once.
func (in *instance) Test(ctx context.Context) integration.TestResult {
	start := time.Now()
	_, version, err := in.fetch(ctx)
	if err != nil {
		r := integration.Failed(err)
		r.Version = version
		return r
	}
	return integration.TestResult{OK: true, Version: version, Latency: time.Since(start)}
}

var healths = map[string]bool{
	integration.HealthOK: true, integration.HealthAttention: true, integration.HealthDown: true,
	integration.HealthUnknown: true, integration.HealthMaintenance: true,
}

// Sync implements integration.Instance: fetch, then publish the resources
// and metrics the spec computes. Every request runs whatever is watched.
func (in *instance) Sync(ctx context.Context, _ integration.Interest) error {
	env, version, err := in.fetch(ctx)
	if version != "" {
		in.d.Emit.SetVersion(version)
	}
	if err != nil {
		return err
	}
	self := in.d.Emit.Self()
	ids := map[string]string{}
	var f integration.Facts
	add := func(c compiledResource, id string, env map[string]any) error {
		r, err := in.build(c, id, env)
		if err != nil {
			return err
		}
		f.Resources = append(f.Resources, r)
		f.Relations = append(f.Relations, integration.Relation{From: self, Type: "monitors", To: id})
		if c.tpl.PartOf != "" {
			f.Relations = append(f.Relations, integration.Relation{From: id, Type: "part_of", To: ids[c.tpl.PartOf]})
		}
		return nil
	}
	for _, c := range in.s.res {
		id := in.s.manifest.ID + ":" + in.key() + "/" + c.tpl.ID
		if c.each == nil {
			ids[c.tpl.ID] = id
			if err := add(c, id, env); err != nil {
				return err
			}
			continue
		}
		// One resource per element, in order, the first maxEach; a list
		// that isn't one (an optional request that failed) is none.
		list, err := run(c.each, env)
		if err != nil {
			return specErr(c.tpl.ID+".each", err)
		}
		items, _ := list.([]any)
		seen := map[string]bool{}
		for _, item := range items {
			if len(seen) == maxEach {
				break
			}
			ienv := make(map[string]any, len(env)+1)
			for k, v := range env {
				ienv[k] = v
			}
			ienv["item"] = item
			k, err := run(c.key, ienv)
			if err != nil {
				return specErr(c.tpl.ID+".key", err)
			}
			key := fmt.Sprint(k)
			if k == nil || !keyRE.MatchString(key) || seen[key] {
				continue // no usable key: skipped, not a failed sync
			}
			seen[key] = true
			if err := add(c, id+"/"+key, ienv); err != nil {
				return err
			}
		}
	}
	for _, c := range in.s.metrics {
		v, err := run(c.value, env)
		if err != nil {
			return specErr("metric "+c.tpl.Key, err)
		}
		m := integration.Metric{Resource: ids[c.tpl.Resource], Key: c.tpl.Key, Unit: c.tpl.Unit, Min: c.tpl.Min, Max: c.tpl.Max,
			Band: c.tpl.Band, Warn: c.tpl.Warn, Crit: c.tpl.Crit, Value: number(v)}
		f.Metrics = append(f.Metrics, m)
	}
	return in.d.Emit.Replace("", f)
}

// build computes one resource of c, at id, from env.
func (in *instance) build(c compiledResource, id string, env map[string]any) (integration.Resource, error) {
	r := integration.Resource{ID: id, Kind: c.tpl.Kind, Name: in.cfg.Name, Labels: c.tpl.Labels, Caps: c.tpl.Caps,
		Health: integration.Health{Status: integration.HealthOK}, Attrs: map[string]any{}}
	for _, x := range []struct {
		dst  *string
		p    *vm.Program
		what string
	}{{&r.Name, c.name, "name"}, {&r.State, c.state, "state"}, {&r.Health.Status, c.health, "health"}, {&r.Health.Reason, c.reason, "reason"}} {
		v, err := run(x.p, env)
		if err != nil {
			return r, specErr(c.tpl.ID+"."+x.what, err)
		}
		if v != nil {
			*x.dst = fmt.Sprint(v)
		}
	}
	if !healths[r.Health.Status] {
		return r, specErr(c.tpl.ID+".health", fmt.Errorf("%q isn't ok, attention, down, unknown or maintenance", r.Health.Status))
	}
	for k, p := range c.attrs {
		v, err := run(p, env)
		if err != nil {
			return r, specErr(c.tpl.ID+".attrs."+k, err)
		}
		if v != nil {
			r.Attrs[k] = v
		}
	}
	for i, p := range c.links {
		v, err := run(p, env)
		if err != nil {
			return r, specErr(c.tpl.ID+".links", err)
		}
		if s, ok := v.(string); ok && s != "" {
			r.Links = append(r.Links, integration.Link{Title: c.tpl.Links[i].Title, URL: s})
		}
	}
	return r, nil
}

// number turns an expression result into a metric value; anything that
// isn't a number is unknown.
func number(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case bool:
		if x {
			return 1
		}
		return 0
	}
	return math.NaN()
}
