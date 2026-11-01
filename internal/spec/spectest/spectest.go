// Package spectest checks a spec without a hub, for its authors: Lint runs
// the published schema and the engine's load-time rules, and Run runs the
// spec against recorded answers and reports what it publishes, with the
// checks the bundled specs' tests make. chadash spec lint and chadash spec
// test are its commands.
//
// A fixtures folder answers the spec's requests by name: <request>.json is
// a 200 with that body, and a request without a file gets a 404. A
// fixtures.yaml, when there is one, says more:
//
//	config:  { inbox_warn: 10 }   # non-secret config; url is the fake upstream
//	secrets: { token: fake-token } # each defaults to "fake-<name>"
//	answers:
//	  server: health.json            # a file, answered with a 200
//	  stats: { status: 401 }         # a status, with an optional file
//
// Fixtures are recorded answers with personal data taken out; they're
// served on 127.0.0.1 only.
package spectest

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.yaml.in/yaml/v3"
	"golang.org/x/text/language"
	"golang.org/x/text/message"

	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/runtime"
	"dash.cha.sh/internal/spec"
	"dash.cha.sh/internal/topics"
)

// MaxSize is the largest spec a hub installs: the largest bundled one is
// under 8 KiB.
const MaxSize = 64 << 10

// Lint checks a spec against the published schema, then the engine's
// load-time rules (spec.Parse: patterns, templates, where secrets may go)
// and the size a hub installs. It returns each problem as a sentence; none
// means it would load.
func Lint(b []byte) []string {
	var problems []string
	if len(b) > MaxSize {
		problems = append(problems, fmt.Sprintf("It's %d KiB, and a hub installs specs of at most %d KiB.", (len(b)+1023)>>10, MaxSize>>10))
	}
	var v any
	if err := yaml.Unmarshal(b, &v); err != nil {
		return []string{"It isn't YAML: " + err.Error()}
	}
	schema, err := compiled()
	if err != nil {
		return []string{"The schema didn't compile: " + err.Error()}
	}
	js, err := json.Marshal(v)
	if err == nil {
		inst, uerr := jsonschema.UnmarshalJSON(bytes.NewReader(js))
		if uerr != nil {
			err = uerr
		} else if verr := schema.Validate(inst); verr != nil {
			if ve, ok := errors.AsType[*jsonschema.ValidationError](verr); ok {
				problems = append(problems, leaves(ve)...)
			} else {
				problems = append(problems, verr.Error())
			}
		}
	}
	if err != nil {
		problems = append(problems, "It doesn't convert to JSON: "+err.Error())
	}
	if _, err := spec.Parse(b); err != nil {
		problems = append(problems, err.Error())
	}
	return problems
}

var (
	schemaOnce sync.Once
	schemaC    *jsonschema.Schema
	schemaErr  error
)

func compiled() (*jsonschema.Schema, error) {
	schemaOnce.Do(func() {
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(spec.Schema()))
		if err != nil {
			schemaErr = err
			return
		}
		c := jsonschema.NewCompiler()
		if err := c.AddResource("spec.json", doc); err != nil {
			schemaErr = err
			return
		}
		schemaC, schemaErr = c.Compile("spec.json")
	})
	return schemaC, schemaErr
}

var english = message.NewPrinter(language.English)

// leaves are a validation error's innermost causes, "at /path: why".
func leaves(ve *jsonschema.ValidationError) []string {
	if len(ve.Causes) == 0 {
		at := "/" + strings.Join(ve.InstanceLocation, "/")
		return []string{fmt.Sprintf("at %s: %s", at, ve.ErrorKind.LocalizedString(english))}
	}
	var out []string
	for _, c := range ve.Causes {
		out = append(out, leaves(c)...)
	}
	return out
}

// fixtures is a fixtures.yaml.
type fixtures struct {
	Config  map[string]any    `yaml:"config"`
	Secrets map[string]string `yaml:"secrets"`
	Answers map[string]answer `yaml:"answers"`
}

type answer struct {
	File   string `yaml:"file"`
	Status int    `yaml:"status"`
}

func (a *answer) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		a.File, a.Status = n.Value, http.StatusOK
		return nil
	}
	type plain answer
	var p plain
	if err := n.Decode(&p); err != nil {
		return err
	}
	*a = answer(p)
	if a.Status == 0 {
		a.Status = http.StatusOK
	}
	return nil
}

func loadFixtures(dir string, probes []spec.Probe) (fixtures, error) {
	var f fixtures
	b, err := os.ReadFile(filepath.Join(dir, "fixtures.yaml"))
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return f, err
	default:
		dec := yaml.NewDecoder(bytes.NewReader(b))
		dec.KnownFields(true)
		if err := dec.Decode(&f); err != nil && !errors.Is(err, io.EOF) {
			return f, fmt.Errorf("fixtures.yaml: %w", err)
		}
	}
	if f.Answers == nil {
		f.Answers = map[string]answer{}
	}
	for _, p := range probes {
		if _, ok := f.Answers[p.Name]; ok {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, p.Name+".json")); err == nil {
			f.Answers[p.Name] = answer{File: p.Name + ".json", Status: http.StatusOK}
		}
	}
	for name, a := range f.Answers {
		if !slices.ContainsFunc(probes, func(p spec.Probe) bool { return p.Name == name }) {
			return f, fmt.Errorf("fixtures.yaml answers %q, which the spec doesn't request", name)
		}
		if a.File != "" {
			if strings.Contains(a.File, "..") || filepath.IsAbs(a.File) {
				return f, fmt.Errorf("fixtures.yaml: %s's file must be in the fixtures folder", name)
			}
			if _, err := os.Stat(filepath.Join(dir, a.File)); err != nil {
				return f, fmt.Errorf("fixtures: %s: %w", name, err)
			}
		}
	}
	return f, nil
}

// instanceID is the instance the spec runs as: resource IDs carry it.
const instanceID = "i_spec"

// Run runs the spec in specYAML once against the answers in dir and writes
// what it published to w. It returns false when a check failed: the
// connection test, a required request without an answer, or the app
// resource every spec publishes.
func Run(ctx context.Context, specYAML []byte, dir string, w io.Writer) (bool, error) {
	s, err := spec.Parse(specYAML)
	if err != nil {
		return false, err
	}
	m := s.Manifest()

	// The config: url is the fake upstream, secrets default to fakes.
	var secretNames []string
	for _, p := range m.ConfigSchema.Properties {
		if p.Schema.WriteOnly {
			secretNames = append(secretNames, p.Name)
		}
	}
	slices.Sort(secretNames)
	// Probes need the config to fill templates, and the fixtures need the
	// probes' names: read the fixtures with the names first.
	f, err := loadFixtures(dir, s.Probes(integration.Config{InstanceID: instanceID, Values: map[string]any{}}))
	if err != nil {
		return false, err
	}
	values := map[string]any{}
	maps.Copy(values, f.Config)
	for _, name := range secretNames {
		values[name] = cmp.Or(f.Secrets[name], "fake-"+name)
	}

	up := &upstream{dir: dir, answers: f.Answers}
	srv := httptest.NewServer(up)
	defer srv.Close()
	values["url"] = srv.URL
	cfg := integration.Config{InstanceID: instanceID, Name: "Test", Values: values}
	up.probes = s.Probes(cfg)
	if errs := m.ConfigSchema.Validate(values); len(errs) > 0 {
		var msgs []string
		for _, e := range errs {
			msgs = append(msgs, cmp.Or(e.Path, "/")+": "+e.Message)
		}
		return false, fmt.Errorf("the fixtures' config doesn't fit the spec: %s", strings.Join(msgs, "; "))
	}

	now := time.Date(2026, 10, 3, 14, 5, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	set := topics.New(graph.New(), now)
	em := runtime.NewEmitter(set, instanceID, clock)
	if err := em.PublishSelf(integration.Resource{Kind: "integration", Name: "Test", State: "connected", Health: integration.Health{Status: integration.HealthOK}}); err != nil {
		return false, err
	}
	deps, _, release := runtime.NewDeps(runtime.DepsOptions{Config: cfg, Emit: em, Now: clock})
	defer release()
	inst, err := s.Connect(ctx, cfg, deps)
	if err != nil {
		return false, err
	}
	defer inst.Close()

	ok := true
	fmt.Fprintf(w, "%s %s (%s), %s\n\n", m.ID, m.Version, m.Name, spec.APIVersion)

	res := inst.Test(ctx)
	if res.OK {
		fmt.Fprintf(w, "Test: ok, version %s\n", cmp.Or(res.Version, "unknown"))
	} else {
		ok = false
		fmt.Fprintf(w, "Test: FAILED, %s: %s\n", res.ErrorCode, res.ErrorMessage)
	}
	syncErr := inst.Sync(ctx, integration.Interest{Watched: true, Metrics: true})
	if code, _, msg := integration.Classify(syncErr); syncErr != nil {
		fmt.Fprintf(w, "Sync: %s: %s\n", code, msg)
	} else {
		fmt.Fprintln(w, "Sync: ok")
	}

	fmt.Fprintln(w, "\nRequests")
	asked := up.seen()
	for _, p := range up.probes {
		a, has := f.Answers[p.Name]
		state := "not asked"
		if asked[p.Name] {
			state = "asked"
		}
		what := "no answer (404)"
		if has {
			what = fmt.Sprintf("%d %s", a.Status, a.File)
		}
		fmt.Fprintf(w, "  %-12s %s %s  %s, %s\n", p.Name, p.Method, p.Path+query(p.Query), what, state)
		if !has && !p.Optional {
			ok = false
			fmt.Fprintf(w, "    FAILED: %s isn't optional and has no answer\n", p.Name)
		}
	}
	for _, u := range up.stray() {
		fmt.Fprintf(w, "  ?            %s, which matches no request: answered 404\n", u)
	}

	_, doc := set.Graph.Snapshot(nil)
	fmt.Fprintln(w, "\nResources")
	ids := slices.Sorted(maps.Keys(doc.Resources))
	for _, id := range ids {
		r := doc.Resources[id]
		if r.Kind == "integration" {
			continue
		}
		fmt.Fprintf(w, "  %s  %s %q  %s, %s\n", r.ID, r.Kind, r.Name, cmp.Or(r.State, "-"), cmp.Or(string(r.Health), "-"))
		for _, k := range slices.Sorted(maps.Keys(r.Attrs)) {
			if k == "links" {
				continue
			}
			v, _ := json.Marshal(r.Attrs[k])
			fmt.Fprintf(w, "      %s = %s\n", k, v)
		}
	}

	fmt.Fprintln(w, "\nMetrics")
	_, metrics := set.Metrics.Snapshot()
	series, _ := metrics[topics.Series].(map[string]json.RawMessage)
	if len(series) == 0 {
		fmt.Fprintln(w, "  none")
	}
	for _, k := range slices.Sorted(maps.Keys(series)) {
		b := series[k]
		var se struct {
			Value any    `json:"value"`
			Unit  string `json:"unit"`
		}
		_ = json.Unmarshal(b, &se)
		v, _ := json.Marshal(se.Value)
		fmt.Fprintf(w, "  %s = %s %s\n", k, v, se.Unit)
	}

	fmt.Fprintln(w, "\nChecks")
	app := graph.RID(m.ID + ":" + instanceID + "/app")
	check := func(pass bool, what string) {
		mark := "ok    "
		if !pass {
			mark, ok = "FAILED", false
		}
		fmt.Fprintf(w, "  %s %s\n", mark, what)
	}
	r, has := doc.Resources[app]
	check(has && r.Kind == "app", "it publishes "+string(app)+", kind app")
	if has {
		check(r.Name == "Test" && r.State == "running", "named after the instance, state running")
		check(r.Attrs["type"] == m.ID, "attrs.type is "+m.ID)
		links, _ := r.Attrs["links"].([]any)
		first, _ := func() (map[string]any, bool) {
			if len(links) == 0 {
				return nil, false
			}
			l, ok := links[0].(map[string]any)
			return l, ok
		}()
		check(len(links) == 1 && first["url"] == srv.URL, "one link, to the configured URL")
		check(doc.Relations["integration:"+instanceID+" monitors "+string(app)] != nil, "the instance monitors it")
	}
	if ok {
		fmt.Fprintln(w, "\nPASS")
	} else {
		fmt.Fprintln(w, "\nFAIL")
	}
	return ok, nil
}

func query(q url.Values) string {
	if len(q) == 0 {
		return ""
	}
	return "?" + q.Encode()
}

// upstream answers the spec's requests from the fixtures, matching method,
// path, query and (for a POST) body as the engine sends them.
type upstream struct {
	dir     string
	answers map[string]answer
	probes  []spec.Probe

	mu     sync.Mutex
	asked  map[string]bool
	others []string
}

func (u *upstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body []byte
	if r.Body != nil {
		body, _ = io.ReadAll(io.LimitReader(r.Body, 1<<20))
	}
	name := ""
	for _, p := range u.probes {
		if p.Method == r.Method && p.Path == r.URL.Path && sameQuery(p.Query, r.URL.Query()) && (p.Body == "" || p.Body == string(body)) {
			name = p.Name
			break
		}
	}
	u.mu.Lock()
	if name == "" {
		u.others = append(u.others, r.Method+" "+r.URL.RequestURI())
	} else {
		if u.asked == nil {
			u.asked = map[string]bool{}
		}
		u.asked[name] = true
	}
	u.mu.Unlock()
	a, ok := u.answers[name]
	if name == "" || !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(a.Status)
	if a.File != "" {
		b, err := os.ReadFile(filepath.Join(u.dir, a.File))
		if err == nil {
			_, _ = w.Write(b)
		}
	}
}

func sameQuery(a, b url.Values) bool {
	return a.Encode() == b.Encode()
}

func (u *upstream) seen() map[string]bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return maps.Clone(u.asked)
}

func (u *upstream) stray() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return slices.Clone(u.others)
}
