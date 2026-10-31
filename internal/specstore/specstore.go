// Package specstore keeps the specs the owner installs from Settings
// (docs/plan/09-v0.3.md, "The spec format v1alpha1"): Tier 1 integrations
// beside the bundled ones, in installed_specs, registered at start.
//
// An installed spec can do what a bundled one can, which is already
// narrow: requests only under the instance's URL, secrets only where the
// spec names them, sandboxed expressions, at most 16 actions with their
// declared tiers. The risk left is an action that lies about itself, so an
// installed spec's actions stay off until the owner turns them on for it,
// after seeing each one's method, path and tier: until then, planning one
// refuses with a sentence that says so. Its reads are on at once.
//
// An ID a bundled spec or a Go integration uses is refused, and a spec
// with instances can't be removed until they go.
package specstore

import (
	"context"
	crand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/authz"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/spec"
	"dash.cha.sh/internal/spec/spectest"
	"dash.cha.sh/internal/store"
)

// MaxYAML bounds a spec's size (spectest.MaxSize, which lint checks too).
const MaxYAML = spectest.MaxSize

// Options configure a Service.
type Options struct {
	Store    *store.Store
	Registry *integration.Registry // nil: integration.Default
	// Instances counts an integration's instances; Rebind restarts them on
	// what's registered now (runtime.Manager's). Both may be set after New,
	// before the API is served (SetRuntime).
	Instances func(id string) int
	Rebind    func(id string) int
	Now       func() time.Time
	Log       *slog.Logger
}

// Service is the installed specs.
type Service struct {
	st        *store.Store
	reg       *integration.Registry
	instances func(string) int
	rebind    func(string) int
	now       func() time.Time
	log       *slog.Logger

	// Audit records installs, replacements, removals and actions turned on
	// or off. Set by the server.
	Audit *authz.Recorder

	mu      sync.Mutex
	on      map[string]bool   // spec ID → its actions are on
	loadErr map[string]string // spec ID → why the stored spec didn't load

	// writing serialises installs, removals and the actions' switch, so
	// the database and on agree.
	writing sync.Mutex

	actorOf func(*http.Request) (authz.Actor, bool) // tests: authz.ActorFrom otherwise
}

// New returns a Service. Call Load before the runtime starts its instances.
func New(o Options) *Service {
	s := &Service{st: o.Store, reg: o.Registry, instances: o.Instances, rebind: o.Rebind, now: o.Now, log: o.Log, on: map[string]bool{}, loadErr: map[string]string{}}
	if s.reg == nil {
		s.reg = integration.Default
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	return s
}

// SetRuntime sets what counts and restarts instances, once the runtime
// exists (it's made after Load, which must come first).
func (s *Service) SetRuntime(instances, rebind func(string) int) {
	s.instances, s.rebind = instances, rebind
}

// Installed is one installed spec as the API shows it.
type Installed struct {
	ID          string       `json:"id"`
	SHA256      string       `json:"sha256"`
	Source      string       `json:"source,omitempty"`
	ActionsOn   bool         `json:"actions_on"`
	InstalledBy string       `json:"installed_by,omitempty"`
	InstalledAt time.Time    `json:"installed_at"`
	UpdatedAt   time.Time    `json:"updated_at"`
	Instances   int          `json:"instances"`
	Summary     spec.Summary `json:"summary"`
	// Error is why a stored spec didn't load (a hub that tightened a rule):
	// it's listed, not registered, until it's replaced or removed.
	Error string `json:"error,omitempty"`
	YAML  string `json:"yaml"`
}

// Problem is a refusal with a code and a sentence.
type Problem struct {
	Code    string   `json:"code"`
	Message string   `json:"message"`
	Details []string `json:"details,omitempty"`
}

func (p *Problem) Error() string { return p.Message }

func problem(code, msg string, details ...string) *Problem {
	return &Problem{Code: code, Message: msg, Details: details}
}

// installed wraps an installed spec: its manifest says so, and its
// instances' actions refuse while they're off.
type installed struct {
	*spec.Spec
	svc *Service
}

func (i installed) Manifest() integration.Manifest {
	m := i.Spec.Manifest()
	m.Installed = true
	return m
}

func (i installed) Connect(ctx context.Context, cfg integration.Config, d integration.Deps) (integration.Instance, error) {
	inst, err := i.Spec.Connect(ctx, cfg, d)
	if err != nil {
		return nil, err
	}
	if a, ok := inst.(integration.Actor); ok {
		return gated{Instance: inst, actor: a, id: i.Spec.Manifest().ID, svc: i.svc}, nil
	}
	return inst, nil
}

// gated is an installed spec's instance whose actions check, when planned
// and approved, that the owner turned them on.
type gated struct {
	integration.Instance
	actor integration.Actor
	id    string
	svc   *Service
}

// Off is the sentence an installed spec's action refuses with while its
// actions are off.
func Off(name string) string {
	return name + " is an installed spec, and its actions stay off until the owner turns them on in Settings → Integrations → Installed specs."
}

func (g gated) Actions() []action.Def {
	defs := integration.ActionDefs(g.actor)
	name := g.id
	if _, m, ok := g.svc.reg.Get(g.id); ok {
		name = m.Name
	}
	for i := range defs {
		exec := defs[i].Execute
		defs[i].Execute = func(ctx context.Context, req action.Request, run *action.TaskRun) error {
			if !g.svc.ActionsOn(g.id) {
				return action.Fail("actions_off", "The owner turned this spec's actions off.")
			}
			return exec(ctx, req, run)
		}
		pre := defs[i].Preconditions
		defs[i].Preconditions = func(ctx context.Context, req action.Request) error {
			if !g.svc.ActionsOn(g.id) {
				return action.Unavailable(Off(name), "")
			}
			if pre != nil {
				return pre(ctx, req)
			}
			return nil
		}
	}
	return defs
}

func (g gated) Execute(ctx context.Context, req action.Request, run *action.TaskRun) error {
	if !g.svc.ActionsOn(g.id) {
		return action.Fail("actions_off", "The owner turned this spec's actions off.")
	}
	return g.actor.Execute(ctx, req, run)
}

// ActionsOn says whether an installed spec's actions are on.
func (s *Service) ActionsOn(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.on[id]
}

// row is one installed_specs row.
type row struct {
	id, specID, yaml, sum, source, by string
	actionsOn                         bool
	at, updated                       int64
}

const columns = `i.id, i.spec_id, i.yaml, i.sha256, i.source, COALESCE(u.username, ''), i.actions_on, i.installed_at, i.updated_at`

func scan(sc interface{ Scan(...any) error }) (row, error) {
	var r row
	var on int64
	err := sc.Scan(&r.id, &r.specID, &r.yaml, &r.sum, &r.source, &r.by, &on, &r.at, &r.updated)
	r.actionsOn = on == 1
	return r, err
}

func (s *Service) rows(ctx context.Context, where string, args ...any) ([]row, error) {
	q := `SELECT ` + columns + ` FROM installed_specs i LEFT JOIN users u ON u.id = i.installed_by`
	if where != "" {
		q += ` WHERE ` + where
	}
	rs, err := s.st.Reader().QueryContext(ctx, q+` ORDER BY i.spec_id`, args...)
	if err != nil {
		return nil, fmt.Errorf("specstore: read: %w", err)
	}
	defer rs.Close()
	var out []row
	for rs.Next() {
		r, err := scan(rs)
		if err != nil {
			return nil, fmt.Errorf("specstore: read: %w", err)
		}
		out = append(out, r)
	}
	return out, rs.Err()
}

// Load registers every installed spec. One that no longer loads is logged
// and left out; its instances say the hub doesn't include it.
func (s *Service) Load(ctx context.Context) error {
	rows, err := s.rows(ctx, "")
	if err != nil {
		return err
	}
	for _, r := range rows {
		// The same checks as an install: a rule this hub added since, or an
		// integration it now bundles under the same ID, keeps it out.
		var err error
		var sp *spec.Spec
		if p := spectest.Lint([]byte(r.yaml)); len(p) > 0 {
			err = errors.New(p[0])
		} else if sp, err = spec.Parse([]byte(r.yaml)); err == nil {
			if _, _, taken := s.reg.Get(r.specID); taken {
				err = errBundledNow
			} else {
				err = s.reg.Register(installed{Spec: sp, svc: s})
			}
		}
		s.mu.Lock()
		if err != nil {
			s.loadErr[r.specID] = err.Error()
			if errors.Is(err, errBundledNow) {
				s.loadErr[r.specID] = fmt.Sprintf("Cha Dash now includes an integration called %q, so this spec isn't loaded. Remove it, or install it under another meta.id.", r.specID)
			}
			s.log.Warn("specstore: an installed spec didn't load", "spec", r.specID, "err", err)
		} else {
			s.on[r.specID] = r.actionsOn
		}
		s.mu.Unlock()
	}
	return nil
}

// errBundledNow is a stored spec whose ID the hub now has built in.
var errBundledNow = errors.New("specstore: the ID is built in now")

// builtIn says whether id is registered as something other than an
// installed spec: a bundled spec or a Go integration.
func (s *Service) builtIn(id string) bool {
	_, m, ok := s.reg.Get(id)
	return ok && !m.Installed
}

// unregister removes an installed spec from the registry, never anything
// built in under the same ID.
func (s *Service) unregister(id string) {
	if _, m, ok := s.reg.Get(id); ok && m.Installed {
		s.reg.Unregister(id)
	}
}

func (s *Service) view(r row) Installed {
	v := Installed{
		ID: r.specID, SHA256: r.sum, Source: r.source, ActionsOn: r.actionsOn, InstalledBy: r.by,
		InstalledAt: time.UnixMilli(r.at).UTC(), UpdatedAt: time.UnixMilli(r.updated).UTC(), YAML: r.yaml,
	}
	if s.instances != nil {
		v.Instances = s.instances(r.specID)
	}
	s.mu.Lock()
	v.Error = s.loadErr[r.specID]
	s.mu.Unlock()
	sp, err := spec.Parse([]byte(r.yaml))
	if err != nil {
		v.Error = err.Error()
		v.Summary = spec.Summary{ID: r.specID, Secrets: []string{}, Requests: []spec.RequestInfo{}, Actions: []spec.ActionSummary{}}
		return v
	}
	v.Summary = sp.Summarize()
	return v
}

// List is every installed spec.
func (s *Service) List(ctx context.Context) ([]Installed, error) {
	rows, err := s.rows(ctx, "")
	if err != nil {
		return nil, err
	}
	out := make([]Installed, 0, len(rows))
	for _, r := range rows {
		out = append(out, s.view(r))
	}
	return out, nil
}

func (s *Service) get(ctx context.Context, id string) (row, bool, error) {
	rows, err := s.rows(ctx, `i.spec_id = ?`, id)
	if err != nil || len(rows) == 0 {
		return row{}, false, err
	}
	return rows[0], true, nil
}

// Preview checks a spec as Install would, and describes it, without
// installing it. replacing is the installed spec it would replace, if any.
type Preview struct {
	Summary   spec.Summary `json:"summary"`
	Replacing *Installed   `json:"replacing,omitempty"`
}

// Check lints a spec and says whether it may be installed under its ID:
// not a bundled ID, and an installed one only when replace is set.
func (s *Service) Check(ctx context.Context, yaml string, replace bool) (*spec.Spec, Preview, error) {
	if len(yaml) > MaxYAML {
		return nil, Preview{}, problem("too_large", fmt.Sprintf("A spec can be at most %d KiB.", MaxYAML>>10))
	}
	if p := spectest.Lint([]byte(yaml)); len(p) > 0 {
		return nil, Preview{}, problem("invalid_spec", "The spec doesn't load: "+p[0], p...)
	}
	sp, err := spec.Parse([]byte(yaml))
	if err != nil {
		return nil, Preview{}, problem("invalid_spec", err.Error())
	}
	id := sp.Manifest().ID
	pv := Preview{Summary: sp.Summarize()}
	cur, ok, err := s.get(ctx, id)
	if err != nil {
		return nil, pv, err
	}
	if s.builtIn(id) {
		return nil, pv, problem("bundled", fmt.Sprintf("%q is the ID of an integration built into Cha Dash. Give the spec another meta.id.", id))
	}
	if !ok {
		return sp, pv, nil
	}
	v := s.view(cur)
	pv.Replacing = &v
	if !replace {
		return nil, pv, problem("exists", fmt.Sprintf("A spec called %q is installed already. Replace it to install this version.", id))
	}
	return sp, pv, nil
}

// Install adds a spec, or replaces the installed one with the same ID when
// replace is set; its instances restart on the new version. A replacement
// keeps the actions' switch only when its actions and targets are the
// same, every key (spec.Summary.ActionsDigest): changed actions are off
// again until the owner looks. replaced says which it was.
func (s *Service) Install(ctx context.Context, yaml, source, userID string, replace bool) (_ Installed, replaced bool, _ error) {
	s.writing.Lock()
	defer s.writing.Unlock()
	sp, pv, err := s.Check(ctx, yaml, replace)
	if err != nil {
		return Installed{}, false, err
	}
	id := sp.Manifest().ID
	sum := sha256.Sum256([]byte(yaml))
	now := s.now().UnixMilli()
	var by any
	if userID != "" {
		by = userID
	}
	on := false
	if pv.Replacing != nil {
		on = pv.Replacing.ActionsOn && pv.Replacing.Summary.ActionsDigest == pv.Summary.ActionsDigest
		_, err = s.st.Writer().ExecContext(ctx, `UPDATE installed_specs SET yaml = ?, sha256 = ?, source = ?, actions_on = ?, installed_by = ?, updated_at = ? WHERE spec_id = ?`,
			yaml, hex.EncodeToString(sum[:]), source, boolInt(on), by, now, id)
	} else {
		_, err = s.st.Writer().ExecContext(ctx, `INSERT INTO installed_specs (id, spec_id, yaml, sha256, source, actions_on, installed_by, installed_at, updated_at)
			VALUES (?, ?, ?, ?, ?, 0, ?, ?, ?)`, "isp_"+strings.ToLower(crand.Text()), id, yaml, hex.EncodeToString(sum[:]), source, by, now, now)
	}
	if err != nil {
		return Installed{}, false, fmt.Errorf("specstore: install: %w", err)
	}
	s.mu.Lock()
	s.on[id] = on
	delete(s.loadErr, id)
	s.mu.Unlock()
	s.unregister(id)
	if err := s.reg.Register(installed{Spec: sp, svc: s}); err != nil {
		return Installed{}, false, fmt.Errorf("specstore: register %s: %w", id, err)
	}
	// Restart its instances on this version: a replacement's, or rows a
	// removed spec left that now have their integration back.
	if s.rebind != nil && (pv.Replacing != nil || s.instances != nil && s.instances(id) > 0) {
		s.rebind(id)
	}
	r, _, err := s.get(ctx, id)
	if err != nil {
		return Installed{}, false, err
	}
	return s.view(r), pv.Replacing != nil, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ErrNotFound is a spec that isn't installed.
var ErrNotFound = errors.New("specstore: not installed")

// Remove uninstalls a spec with no instances.
func (s *Service) Remove(ctx context.Context, id string) (Installed, error) {
	s.writing.Lock()
	defer s.writing.Unlock()
	r, ok, err := s.get(ctx, id)
	if err != nil {
		return Installed{}, err
	}
	if !ok {
		return Installed{}, ErrNotFound
	}
	v := s.view(r)
	if v.Instances > 0 {
		return v, problem("in_use", fmt.Sprintf("%s has %d instance(s). Delete them in Settings → Integrations first.", cmpName(v), v.Instances))
	}
	if _, err := s.st.Writer().ExecContext(ctx, `DELETE FROM installed_specs WHERE spec_id = ?`, id); err != nil {
		return v, fmt.Errorf("specstore: remove: %w", err)
	}
	s.unregister(id)
	s.mu.Lock()
	delete(s.on, id)
	delete(s.loadErr, id)
	s.mu.Unlock()
	return v, nil
}

// SetActions turns an installed spec's actions on or off. Off takes
// effect at the next plan or approval; on, likewise. Turning them on names
// the version the owner reviewed (its SHA-256): a spec replaced since is
// refused with changed, so actions nobody looked at never turn on.
func (s *Service) SetActions(ctx context.Context, id string, on bool, sha string) (Installed, error) {
	s.writing.Lock()
	defer s.writing.Unlock()
	r, ok, err := s.get(ctx, id)
	if err != nil {
		return Installed{}, err
	}
	if !ok {
		return Installed{}, ErrNotFound
	}
	if on && sha != r.sum {
		return s.view(r), problem("changed", fmt.Sprintf("%s changed since you looked at it. Review its actions again before turning them on.", cmpName(s.view(r))))
	}
	if _, err := s.st.Writer().ExecContext(ctx, `UPDATE installed_specs SET actions_on = ?, updated_at = ? WHERE spec_id = ?`, boolInt(on), s.now().UnixMilli(), id); err != nil {
		return Installed{}, fmt.Errorf("specstore: actions: %w", err)
	}
	s.mu.Lock()
	s.on[id] = on
	s.mu.Unlock()
	if r, _, err = s.get(ctx, id); err != nil {
		return Installed{}, err
	}
	return s.view(r), nil
}

func cmpName(v Installed) string {
	if v.Summary.Name != "" {
		return v.Summary.Name
	}
	return v.ID
}
