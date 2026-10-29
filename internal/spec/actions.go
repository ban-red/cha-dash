package spec

// Actions in a spec (v0.2): a spec's instance implements integration.Actor
// when the spec has an actions section. The section is deliberately small:
//
//   - targets are param-addressed, "<spec id>:<instance id>/<target>/<param>",
//     with the param checked against an anchored pattern before anything is
//     sent. A read (a templated GET) resolves one: it proves the target
//     exists and gives its name, its state (the plan's fingerprint) and the
//     values preconditions and verify read. A resource template with each
//     may publish the same IDs, so ⌘K finds them, but resolving never
//     depends on the graph;
//   - the write is one POST, PUT or DELETE to a templated path, where every
//     value is a target's param or an action param with a pattern (or an
//     integer or an enum), URL-path-escaped, and never "." or "..". The body
//     is an optional JSON object computed from params and the read;
//   - headers are the spec's source headers, so the credential is the
//     instance's one; no secret is ever templated into a path or a body, and
//     no response body reaches a log, an error or a task.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
	"github.com/tidwall/gjson"
	"go.yaml.in/yaml/v3"

	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/integration"
)

// Limits on the actions section.
const (
	maxActions = 16
	maxTargets = 8
	maxParam   = 128 // bytes of a target's param or a path param
	// verifyEvery is how often verify reads the target again.
	verifyEvery = 2 * time.Second
	// DefaultVerifyTimeout bounds a spec action's verify unless it says.
	DefaultVerifyTimeout = 30 * time.Second
)

// verifyInterval is verifyEvery; tests shorten it.
var verifyInterval = verifyEvery

type targetTpl struct {
	ID      string
	Kind    string     `yaml:"kind"`
	Pattern string     `yaml:"pattern"` // anchored: the param after …/<target>/
	Read    targetRead `yaml:"read"`
	Name    string     `yaml:"name"`  // expr over {config, instance, target}
	State   string     `yaml:"state"` // expr: what an approval must find unchanged
}

type targetRead struct {
	Path    string            `yaml:"path"`    // {{ target.id }} only
	Extract map[string]string `yaml:"extract"` // name → gjson path; not id or name
}

// targets keeps the file's order.
type targets []targetTpl

func (t *targets) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: targets must be a mapping of target names", n.Line)
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		var x targetTpl
		if err := n.Content[i+1].Decode(&x); err != nil {
			return err
		}
		x.ID = n.Content[i].Value
		*t = append(*t, x)
	}
	return nil
}

type actionTpl struct {
	ID            string     `yaml:"id"`     // <spec id>.<target>.<verb>
	Target        string     `yaml:"target"` // a target's name
	Title         string     `yaml:"title"`
	Tier          string     `yaml:"tier"`  // T0 … T4
	Label         string     `yaml:"label"` // expr; default title + the target's name
	Params        fields     `yaml:"params"`
	Preconditions []condTpl  `yaml:"preconditions"`
	Request       writeTpl   `yaml:"request"`
	Verify        *verifyTpl `yaml:"verify"`
	Undo          string     `yaml:"undo"` // another action's ID, on the same target, without params
	Household     bool       `yaml:"household"`
}

type condTpl struct {
	When    string `yaml:"when"`    // expr → bool over the read
	Message string `yaml:"message"` // expr → the sentence
}

type writeTpl struct {
	Method string            `yaml:"method"` // POST, PUT or DELETE
	Path   string            `yaml:"path"`   // {{ target.id }}, {{ params.x }}
	Body   map[string]string `yaml:"body"`   // JSON object: key → expr
}

type verifyTpl struct {
	Until   string `yaml:"until"`   // expr → bool over a fresh read
	Message string `yaml:"message"` // expr → the sentence when it never holds
	Timeout string `yaml:"timeout"` // default DefaultVerifyTimeout, at most a minute
}

type compiledTarget struct {
	tpl         targetTpl
	re          *regexp.Regexp
	name, state *vm.Program
}

type compiledAction struct {
	tpl     actionTpl
	target  *compiledTarget
	tier    action.Tier
	params  *integration.Schema
	label   *vm.Program
	conds   []compiledCond
	body    map[string]*vm.Program
	until   *vm.Program
	failMsg *vm.Program
	verifyT time.Duration
}

type compiledCond struct{ when, msg *vm.Program }

var (
	actionTplRE = regexp.MustCompile(`\{\{\s*(target|params)\.([a-z_][a-z0-9_]*)\s*\}\}`)
	anyTplRE    = regexp.MustCompile(`\{\{[^}]*\}\}`)
)

// checkActionPath checks a read's or a write's path: absolute, on the
// instance's origin, no query, and templates only for the values allowed.
func checkActionPath(where, p string, ok func(ns, name string) error) error {
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.ContainsAny(p, "#\\?%") || strings.Contains(p, "..") {
		return fmt.Errorf("%s: path must be an absolute path on the instance's origin, without a query", where)
	}
	if len(anyTplRE.FindAllString(p, -1)) != len(actionTplRE.FindAllString(p, -1)) || strings.Count(p, "{{") != len(actionTplRE.FindAllString(p, -1)) {
		return fmt.Errorf("%s: only {{ target.id }} and {{ params.<name> }} may appear in a path (never a secret or config)", where)
	}
	for _, m := range actionTplRE.FindAllStringSubmatch(p, -1) {
		if err := ok(m[1], m[2]); err != nil {
			return fmt.Errorf("%s: %w", where, err)
		}
	}
	return nil
}

func anchored(p string) bool { return strings.HasPrefix(p, "^") && strings.HasSuffix(p, "$") }

func (s *Spec) compileActions() error {
	f := s.f
	if len(f.Targets) == 0 && len(f.Actions) == 0 {
		return nil
	}
	if len(f.Targets) > maxTargets || len(f.Actions) > maxActions {
		return fmt.Errorf("a spec has at most %d targets and %d actions", maxTargets, maxActions)
	}
	base := map[string]any{"config": map[string]any{}, "instance": map[string]any{}, "target": map[string]any{}}
	compileIn := func(env map[string]any, where, src string) (*vm.Program, error) {
		if strings.TrimSpace(src) == "" {
			return nil, nil
		}
		p, err := expr.Compile(src, expr.Env(env), expr.MaxNodes(500))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", where, err)
		}
		return p, nil
	}
	s.targets = map[string]*compiledTarget{}
	for _, t := range f.Targets {
		where := "targets." + t.ID
		switch {
		case !nameRE.MatchString(t.ID) || s.targets[t.ID] != nil:
			return fmt.Errorf("%s: target names are unique lowercase identifiers", where)
		case t.Kind == "" || strings.ContainsAny(t.Kind, " \t/"):
			return fmt.Errorf("%s: needs a kind", where)
		case !anchored(t.Pattern):
			return fmt.Errorf("%s: pattern must be anchored with ^ and $", where)
		case len(t.Read.Extract) == 0:
			return fmt.Errorf("%s.read: extract at least one value", where)
		}
		re, err := regexp.Compile(t.Pattern)
		if err != nil {
			return fmt.Errorf("%s.pattern: %w", where, err)
		}
		if re.MatchString("") || re.MatchString(".") || re.MatchString("..") || re.MatchString("a/b") {
			return fmt.Errorf("%s.pattern: must refuse empty, \".\", \"..\" and anything with a /", where)
		}
		if err := checkActionPath(where+".read.path", t.Read.Path, func(ns, name string) error {
			if ns != "target" || name != "id" {
				return errors.New("a read's path may use {{ target.id }} only")
			}
			return nil
		}); err != nil {
			return err
		}
		for k, p := range t.Read.Extract {
			if !nameRE.MatchString(k) || p == "" || k == "id" || k == "name" {
				return fmt.Errorf("%s.read.extract: %q needs a lowercase name (not id or name) and a gjson path", where, k)
			}
		}
		c := &compiledTarget{tpl: t, re: re}
		if c.name, err = compileIn(base, where+".name", t.Name); err != nil {
			return err
		}
		if c.state, err = compileIn(base, where+".state", t.State); err != nil {
			return err
		}
		s.targets[t.ID] = c
	}

	ids := map[string]bool{}
	for _, a := range f.Actions {
		where := "actions." + a.ID
		tgt := s.targets[a.Target]
		prefix := s.manifest.ID + "." + a.Target + "."
		verb, _ := strings.CutPrefix(a.ID, prefix)
		switch {
		case tgt == nil:
			return fmt.Errorf("%s: target %q isn't one of the spec's targets", where, a.Target)
		case !strings.HasPrefix(a.ID, prefix) || !nameRE.MatchString(verb) || ids[a.ID]:
			return fmt.Errorf("%s: an action's id is a unique %s<verb>", where, prefix)
		case strings.TrimSpace(a.Title) == "":
			return fmt.Errorf("%s: needs a title", where)
		}
		ids[a.ID] = true
		tier, err := action.ParseTier(a.Tier)
		if err != nil {
			return fmt.Errorf("%s: tier must be T0 … T4", where)
		}
		c := &compiledAction{tpl: a, target: tgt, tier: tier, body: map[string]*vm.Program{}, verifyT: DefaultVerifyTimeout}

		// Params: plain typed fields, never secrets. A param used in a path
		// needs a pattern, an enum or to be an integer.
		inPath := map[string]bool{}
		if len(a.Params) > 0 {
			var props []integration.Property
			var required []string
			for _, fl := range a.Params {
				if !nameRE.MatchString(fl.Name) {
					return fmt.Errorf("%s.params: %q isn't a lowercase identifier", where, fl.Name)
				}
				if fl.Type == "secret" || fl.Type == "url" {
					return fmt.Errorf("%s.params.%s: an action param is a string, integer, number or boolean", where, fl.Name)
				}
				sc, err := fieldSchema(fl)
				if err != nil {
					return fmt.Errorf("%s.params.%s: %w", where, fl.Name, err)
				}
				props = append(props, integration.Prop(fl.Name, sc))
				if fl.Required {
					required = append(required, fl.Name)
				}
			}
			c.params = integration.Object(props...).Require(required...)
			if err := c.params.Check(); err != nil {
				return fmt.Errorf("%s.params: %w", where, err)
			}
		}
		param := func(name string) *field {
			for i := range a.Params {
				if a.Params[i].Name == name {
					return &a.Params[i]
				}
			}
			return nil
		}

		w := &c.tpl.Request
		w.Method = strings.ToUpper(w.Method)
		if !slices.Contains([]string{http.MethodPost, http.MethodPut, http.MethodDelete}, w.Method) {
			return fmt.Errorf("%s.request: method must be POST, PUT or DELETE", where)
		}
		if err := checkActionPath(where+".request.path", w.Path, func(ns, name string) error {
			if ns == "target" {
				if name != "id" {
					return errors.New("a path may use {{ target.id }}, not other values of the read")
				}
				return nil
			}
			p := param(name)
			switch {
			case p == nil:
				return fmt.Errorf("{{ params.%s }} isn't one of the action's params", name)
			case !p.Required:
				return fmt.Errorf("{{ params.%s }} must be required to appear in a path", name)
			case p.Type == "integer" || len(p.Enum) > 0 || p.Pattern != "":
				inPath[name] = true
				return nil
			}
			return fmt.Errorf("{{ params.%s }} needs a pattern, an enum or to be an integer to appear in a path", name)
		}); err != nil {
			return err
		}
		if len(w.Body) > 0 && w.Method == http.MethodDelete {
			return fmt.Errorf("%s.request: a DELETE has no body", where)
		}
		env := map[string]any{"config": map[string]any{}, "instance": map[string]any{}, "target": map[string]any{}, "params": map[string]any{}}
		for k, src := range w.Body {
			if k == "" || src == "" {
				return fmt.Errorf("%s.request.body: every key needs an expression", where)
			}
			if c.body[k], err = compileIn(env, where+".request.body."+k, src); err != nil {
				return err
			}
		}
		if c.label, err = compileIn(env, where+".label", a.Label); err != nil {
			return err
		}
		for i, pc := range a.Preconditions {
			pw := fmt.Sprintf("%s.preconditions[%d]", where, i)
			if pc.When == "" || pc.Message == "" {
				return fmt.Errorf("%s: needs when and message", pw)
			}
			var cc compiledCond
			if cc.when, err = compileIn(env, pw+".when", pc.When); err != nil {
				return err
			}
			if cc.msg, err = compileIn(env, pw+".message", pc.Message); err != nil {
				return err
			}
			c.conds = append(c.conds, cc)
		}
		if v := a.Verify; v != nil {
			if v.Until == "" || v.Message == "" {
				return fmt.Errorf("%s.verify: needs until and message", where)
			}
			if c.until, err = compileIn(env, where+".verify.until", v.Until); err != nil {
				return err
			}
			if c.failMsg, err = compileIn(env, where+".verify.message", v.Message); err != nil {
				return err
			}
			if v.Timeout != "" {
				d, err := time.ParseDuration(v.Timeout)
				if err != nil || d <= 0 || d > time.Minute {
					return fmt.Errorf("%s.verify.timeout must be a duration up to a minute", where)
				}
				c.verifyT = d
			}
		}
		s.actions = append(s.actions, c)
	}
	for _, c := range s.actions {
		if u := c.tpl.Undo; u != "" && (!ids[u] || u == c.tpl.ID) {
			return fmt.Errorf("actions.%s.undo: %q isn't another of the spec's actions", c.tpl.ID, u)
		} else if u != "" {
			for _, o := range s.actions {
				if o.tpl.ID == u && (o.target != c.target || o.params != nil) {
					return fmt.Errorf("actions.%s.undo: %s must act on the same target and take no params", c.tpl.ID, u)
				}
			}
		}
	}
	return nil
}

// actor is an instance of a spec with actions.
type actor struct{ *instance }

var _ integration.Actor = actor{}

// Actions implements integration.Actor.
func (a actor) Actions() []action.Def {
	defs := make([]action.Def, 0, len(a.s.actions))
	for _, c := range a.s.actions {
		d := action.Def{
			ID: c.tpl.ID, Title: c.tpl.Title, Targets: []string{c.target.tpl.Kind}, Tier: c.tier,
			Household: c.tpl.Household, VerifyTimeout: c.verifyT,
			Resolve: func(ctx context.Context, target string) (action.Target, error) {
				t, _, err := a.resolve(ctx, c.target, target)
				return t, err
			},
			Preconditions: func(ctx context.Context, req action.Request) error { return a.preconditions(c, req) },
			Execute: func(ctx context.Context, req action.Request, run *action.TaskRun) error {
				return a.execute(ctx, c, req, run)
			},
		}
		if c.params != nil {
			d.Params = integration.Params(c.params)
		}
		if c.label != nil {
			d.Label = func(req action.Request) string { return a.label(c, req) }
		}
		if c.until != nil {
			d.Verify = func(ctx context.Context, req action.Request, run *action.TaskRun) error {
				return a.verify(ctx, c, req, run)
			}
		}
		if u := c.tpl.Undo; u != "" {
			var title string
			for _, o := range a.s.actions {
				if o.tpl.ID == u {
					title = o.tpl.Title
				}
			}
			d.Undo = func(r action.Request) *action.Undo {
				return &action.Undo{Action: u, Params: json.RawMessage(`{}`), Label: title + " " + r.Target.Name}
			}
		}
		defs = append(defs, d)
	}
	return defs
}

// Execute implements integration.Actor. Every Def has its own Execute, so
// this only answers an action the spec doesn't have.
func (a actor) Execute(_ context.Context, req action.Request, _ *action.TaskRun) error {
	return action.Fail("unknown_action", fmt.Sprintf("%s has no action %s.", a.s.manifest.Name, req.Action))
}

// targetEnv is the expression environment for a target's read values.
func (a actor) targetEnv(vals map[string]any) map[string]any {
	env := a.env()
	env["target"] = vals
	return env
}

// resolve reads a target. It answers action.ErrNotFound for an ID that
// isn't this instance's, doesn't match the target's pattern, or that the
// upstream says doesn't exist, before or without sending anything where
// it can. vals are the read's values, with id and name.
func (a actor) resolve(ctx context.Context, t *compiledTarget, target string) (action.Target, map[string]any, error) {
	prefix := a.s.manifest.ID + ":" + a.key() + "/" + t.tpl.ID + "/"
	param, ok := strings.CutPrefix(target, prefix)
	if !ok || len(param) > maxParam || !t.re.MatchString(param) {
		return action.Target{}, nil, action.ErrNotFound
	}
	vals, err := a.read(ctx, t, param)
	if err != nil {
		return action.Target{}, nil, err
	}
	env := a.targetEnv(vals)
	name := target
	if t.name != nil {
		v, err := run(t.name, env)
		if err != nil {
			return action.Target{}, nil, a.specFail(t.tpl.ID+".name", err)
		}
		if v != nil && fmt.Sprint(v) != "" {
			name = fmt.Sprint(v)
		}
	}
	vals["name"] = name
	state := ""
	if t.state != nil {
		v, err := run(t.state, env)
		if err != nil {
			return action.Target{}, nil, a.specFail(t.tpl.ID+".state", err)
		}
		state = fmt.Sprint(v)
	}
	ref, _ := json.Marshal(vals)
	return action.Target{ID: target, Kind: t.tpl.Kind, Name: name, State: fingerprint(t.tpl.Kind, name, state), Ref: string(ref)}, vals, nil
}

// fingerprint is a target's State: kind, name and the spec's state.
func fingerprint(parts ...string) string {
	b, _ := json.Marshal(parts)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:8])
}

// refVals are the read values resolve kept in the target's Ref.
func refVals(t action.Target) map[string]any {
	vals := map[string]any{}
	_ = json.Unmarshal([]byte(t.Ref), &vals)
	return vals
}

func (a actor) read(ctx context.Context, t *compiledTarget, param string) (map[string]any, error) {
	p, err := fillPath(t.tpl.Read.Path, map[string]string{"target.id": param})
	if err != nil {
		return nil, action.ErrNotFound
	}
	status, b, err := a.call(ctx, http.MethodGet, p, nil)
	if err != nil {
		return nil, err
	}
	switch {
	case status == http.StatusNotFound || status == http.StatusGone:
		return nil, action.ErrNotFound
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return nil, action.Unavailable(fmt.Sprintf("%s refused %s's credential. Check it in the instance's settings.", a.s.manifest.Name, a.cfg.Name), a.cfg.InstanceID)
	case status < 200 || status > 299:
		return nil, fmt.Errorf("%s answered %d %s", a.s.manifest.Name, status, http.StatusText(status))
	case !gjson.ValidBytes(b):
		return nil, fmt.Errorf("%s didn't answer JSON", a.s.manifest.Name)
	}
	vals := map[string]any{"id": param}
	for name, path := range t.tpl.Read.Extract {
		if res := gjson.GetBytes(b, path); res.Exists() {
			vals[name] = res.Value()
		} else {
			vals[name] = nil
		}
	}
	return vals, nil
}

// actionEnv is the environment for an action's expressions: the target as
// resolved (its read values, id and name) and the params.
func (a actor) actionEnv(vals map[string]any, req action.Request) map[string]any {
	env := a.targetEnv(vals)
	params := map[string]any{}
	_ = json.Unmarshal(req.Params, &params)
	env["params"] = params
	return env
}

func (a actor) preconditions(c *compiledAction, req action.Request) error {
	env := a.actionEnv(refVals(req.Target), req)
	for i, pc := range c.conds {
		hit, err := run(pc.when, env)
		if err != nil {
			return a.specFail(fmt.Sprintf("%s.preconditions[%d]", c.tpl.ID, i), err)
		}
		if b, _ := hit.(bool); b {
			msg, err := run(pc.msg, env)
			if err != nil {
				return a.specFail(fmt.Sprintf("%s.preconditions[%d]", c.tpl.ID, i), err)
			}
			return action.Precondition(fmt.Sprint(msg))
		}
	}
	return nil
}

func (a actor) label(c *compiledAction, req action.Request) string {
	v, err := run(c.label, a.actionEnv(refVals(req.Target), req))
	if err != nil || v == nil || fmt.Sprint(v) == "" {
		return c.tpl.Title + " " + req.Target.Name
	}
	return fmt.Sprint(v)
}

func (a actor) execute(ctx context.Context, c *compiledAction, req action.Request, tr *action.TaskRun) error {
	vals := refVals(req.Target)
	env := a.actionEnv(vals, req)
	params, _ := env["params"].(map[string]any)
	values := map[string]string{"target.id": fmt.Sprint(vals["id"])}
	for k, v := range params {
		switch x := v.(type) {
		case string:
			values["params."+k] = x
		case float64:
			values["params."+k] = fmt.Sprint(x)
		}
	}
	p, err := fillPath(c.tpl.Request.Path, values)
	if err != nil {
		return action.Fail("invalid", "A value for the path isn't allowed there.")
	}
	var body []byte
	if len(c.body) > 0 {
		obj := make(map[string]any, len(c.body))
		for k, prog := range c.body {
			v, err := run(prog, env)
			if err != nil {
				return a.specFail(c.tpl.ID+".request.body."+k, err)
			}
			obj[k] = v
		}
		if body, err = json.Marshal(obj); err != nil {
			return a.specFail(c.tpl.ID+".request.body", err)
		}
	}
	if tr != nil {
		tr.Step(a.label(c, req))
	}
	status, _, err := a.call(ctx, c.tpl.Request.Method, p, body)
	if err != nil {
		return action.Fail("unreachable", err.Error())
	}
	name := a.s.manifest.Name
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return action.Fail("forbidden", fmt.Sprintf("%s refused: %s's credential can't do this.", name, a.cfg.Name))
	case status == http.StatusNotFound || status == http.StatusGone:
		return action.Fail("not_found", fmt.Sprintf("%s says %s doesn't exist any more.", name, req.Target.Name))
	case status < 200 || status > 299:
		return action.Fail("upstream", fmt.Sprintf("%s answered %d %s.", name, status, http.StatusText(status)))
	}
	return nil
}

// verify reads the target again until the spec's condition holds.
func (a actor) verify(ctx context.Context, c *compiledAction, req action.Request, tr *action.TaskRun) error {
	if tr != nil {
		tr.Step("Check the result")
	}
	vals := refVals(req.Target)
	param := fmt.Sprint(vals["id"])
	// last is the latest read's error; a read the deadline cut counts only
	// when nothing else is known (no read answered, none failed before).
	var last error
	answered := false
	for {
		fresh, err := a.read(ctx, c.target, param)
		if err == nil {
			fresh["name"] = req.Target.Name
			ok, err := run(c.until, a.actionEnv(fresh, req))
			if err != nil {
				return a.specFail(c.tpl.ID+".verify.until", err)
			}
			if b, _ := ok.(bool); b {
				return nil
			}
			last, answered = nil, true
		} else if ctx.Err() == nil || (last == nil && !answered) {
			last = err
		}
		t := time.NewTimer(verifyInterval)
		select {
		case <-ctx.Done():
			t.Stop()
			switch {
			case errors.Is(last, action.ErrNotFound):
				return action.Fail("verify_failed", fmt.Sprintf("%s says %s doesn't exist any more.", a.s.manifest.Name, req.Target.Name))
			case last != nil && !errors.Is(last, context.DeadlineExceeded) && !errors.Is(last, context.Canceled):
				return action.Fail("verify_failed", last.Error())
			}
			msg, err := run(c.failMsg, a.actionEnv(vals, req))
			if err != nil || msg == nil {
				return action.Fail("verify_failed", "The change didn't show in time.")
			}
			return action.Fail("verify_failed", fmt.Sprint(msg))
		case <-t.C:
		}
	}
}

// specFail is a spec that can't evaluate what it says: a bug in the spec
// or an upstream shape it didn't expect.
func (a actor) specFail(where string, err error) error {
	return action.Fail("spec_failed", fmt.Sprintf("%s's spec failed at %s: %v", a.s.manifest.Name, where, err))
}

// fillPath expands an action path's templates. Each value must be
// non-empty, not "." or "..", and at most maxParam bytes; it's escaped as
// one path segment, so a "/" can't add one.
func fillPath(p string, values map[string]string) (string, error) {
	var bad error
	out := actionTplRE.ReplaceAllStringFunc(p, func(m string) string {
		sub := actionTplRE.FindStringSubmatch(m)
		v, ok := values[sub[1]+"."+sub[2]]
		if !ok || v == "" || v == "." || v == ".." || len(v) > maxParam {
			bad = fmt.Errorf("spec: no usable value for %s.%s", sub[1], sub[2])
			return ""
		}
		return url.PathEscape(v)
	})
	return out, bad
}

// call sends one action request with the spec's source headers and returns
// the status and at most MaxResponseSize bytes of the body. escPath is
// already escaped. Errors never carry the URL, a header or the body.
func (a actor) call(ctx context.Context, method, escPath string, body []byte) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, a.s.timeout)
	defer cancel()
	u := *a.base
	u.RawPath = strings.TrimSuffix(a.base.EscapedPath(), "/") + escPath
	var err error
	if u.Path, err = url.PathUnescape(u.RawPath); err != nil {
		return 0, nil, errors.New("the request's path isn't valid")
	}
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), rd)
	if err != nil {
		return 0, nil, errors.New("the request couldn't be made")
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range a.s.f.Source.Headers {
		req.Header.Set(k, a.expand(v))
	}
	resp, err := a.d.HTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return 0, nil, fmt.Errorf("%s didn't answer in time", a.s.manifest.Name)
		}
		return 0, nil, fmt.Errorf("couldn't reach %s", a.s.manifest.Name)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseSize+1))
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("%s's answer was cut short", a.s.manifest.Name)
	}
	if len(b) > MaxResponseSize {
		return resp.StatusCode, nil, fmt.Errorf("%s answered more than %d bytes", a.s.manifest.Name, MaxResponseSize)
	}
	return resp.StatusCode, b, nil
}
