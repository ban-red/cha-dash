// Package runtime runs integration instances: one coordinator per instance
// (jittered polling of what's watched, backoff, auth errors → Repairs), the
// per-instance HTTP client (TLS policy, session cache, timeouts), secrets
// from the vault, the instances API, and the fact emitter that writes into
// the graph, metrics and inbox topics.
package runtime

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/authz"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/security"
	"dash.cha.sh/internal/store"
	"dash.cha.sh/internal/topics"
	"dash.cha.sh/internal/vault"
)

// Options configure a Manager.
type Options struct {
	Store    *store.Store
	Vault    *vault.Vault
	Registry *integration.Registry // nil: integration.Default
	// Topics is where instances publish. In demo mode it's a private set
	// that nobody subscribes to, so real instances never reach the Demo Home.
	Topics *topics.Set
	// Sandboxed says Topics is that private set (reported in diagnostics).
	Sandboxed bool
	Watch     *Watch // nil: nobody is ever watching
	Log       *slog.Logger
	UserAgent string           // default UserAgent()
	RootCAs   *x509.CertPool   // roots for tls mode verify; nil: the system's
	Now       func() time.Time // nil: time.Now
	// Posture is the security posture instances' security findings are
	// published under (see SetPosture). Empty publishes declared severities.
	Posture security.Posture
	// Actions is the action engine's registry. When set, every running
	// instance whose integration is an integration.Actor registers its
	// actions there (see Actions), and incidents' actions carry their
	// availability. Nil runs no actions (demo mode, where instances write
	// to a private graph).
	Actions *action.Registry
	// Nodes reaches chadash-node verbs (Deps.Nodes). Nil: no nodes.
	Nodes integration.NodeCaller
}

// Manager owns every instance: it loads them at boot, runs a coordinator
// per enabled instance, restarts it when its config changes, and serves the
// instances API.
type Manager struct {
	o   Options
	reg *integration.Registry
	log *slog.Logger

	// Audit records creating, changing and deleting instances (names, the
	// fields that changed and the URL's host; never a secret). Nil records
	// nothing. Set it before serving.
	Audit *authz.Recorder

	mu     sync.Mutex
	insts  map[string]*managed
	ctx    context.Context // nil until Start
	cancel context.CancelFunc
	bg     sync.WaitGroup

	auth authState // sign-in flows (authorize.go)

	actions *Actions // nil without Options.Actions

	posture atomic.Value // security.Posture
}

// managed is one instance.
type managed struct {
	life sync.Mutex // serialises restart, update and delete

	mu  sync.Mutex // guards row and gen
	row row
	gen int // bumps on every config change; stale TOFU writes are dropped

	integ integration.Integration // nil when the integration isn't in this build
	man   integration.Manifest
	emit  *Emitter
	st    *state
	coord *coordinator // guarded by life
}

func (mi *managed) snapshotRow() (row, int) {
	mi.mu.Lock()
	defer mi.mu.Unlock()
	r := mi.row
	r.Config = cloneMap(mi.row.Config)
	return r, mi.gen
}

// Errors the API maps to statuses.
var (
	ErrUnknownIntegration = errors.New("runtime: unknown integration")
	ErrNotFound           = errors.New("runtime: no such instance")
	ErrNotRunning         = errors.New("runtime: the instance isn't running")
	ErrNameTaken          = errors.New("runtime: another instance of this integration has that name")
)

// ValidationError lists what's wrong with a config.
type ValidationError struct {
	Fields []integration.FieldError
}

func (e *ValidationError) Error() string {
	parts := make([]string, len(e.Fields))
	for i, f := range e.Fields {
		parts[i] = f.String()
	}
	return "invalid config: " + strings.Join(parts, "; ")
}

// New returns a Manager. Call Start to load and run the stored instances.
func New(o Options) (*Manager, error) {
	if o.Store == nil || o.Vault == nil || o.Topics == nil {
		return nil, errors.New("runtime: Store, Vault and Topics are required")
	}
	if o.Registry == nil {
		o.Registry = integration.Default
	}
	if o.Watch == nil {
		o.Watch = NewWatch()
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	m := &Manager{o: o, reg: o.Registry, log: o.Log, insts: map[string]*managed{}}
	m.posture.Store(o.Posture)
	if o.Actions != nil {
		m.actions = NewActions(o.Actions, o.Topics.Graph, o.Log)
		m.actions.OnChange(m.reapplyAll)
	}
	return m, nil
}

// Actions is the bridge to the action engine, or nil without
// Options.Actions.
func (m *Manager) Actions() *Actions { return m.actions }

// reapplyAll publishes every instance's incidents again where the hub's
// additions changed (actions' availability).
func (m *Manager) reapplyAll() {
	m.mu.Lock()
	insts := m.all()
	m.mu.Unlock()
	for _, mi := range insts {
		mi.emit.Reapply()
	}
}

// Posture is the security posture the instances publish under.
func (m *Manager) Posture() security.Posture { return m.posture.Load().(security.Posture) }

// SetPosture changes the security posture and re-publishes every
// instance's security findings under it before returning.
func (m *Manager) SetPosture(p security.Posture) {
	m.posture.Store(p)
	m.mu.Lock()
	insts := m.all()
	m.mu.Unlock()
	for _, mi := range insts {
		mi.emit.Reapply()
	}
}

// Start loads the stored instances and starts the enabled ones. Instances
// run until Close or until ctx ends.
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	if m.ctx != nil {
		m.mu.Unlock()
		return errors.New("runtime: already started")
	}
	m.ctx, m.cancel = context.WithCancel(ctx)
	m.mu.Unlock()
	if err := m.Reload(ctx); err != nil {
		return err
	}
	m.bg.Go(m.janitor)
	return nil
}

// Close stops every instance and waits for them.
func (m *Manager) Close() {
	m.mu.Lock()
	if m.cancel != nil {
		m.cancel()
	}
	insts := m.all()
	m.mu.Unlock()
	for _, mi := range insts {
		mi.life.Lock()
		m.detach(mi)
		if mi.coord != nil {
			mi.coord.stop()
			mi.coord = nil
		}
		mi.life.Unlock()
	}
	m.bg.Wait()
}

func (m *Manager) running() context.Context {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ctx == nil || m.ctx.Err() != nil {
		return nil
	}
	return m.ctx
}

// all lists the instances; the caller holds m.mu.
func (m *Manager) all() []*managed {
	out := make([]*managed, 0, len(m.insts))
	for _, mi := range m.insts {
		out = append(out, mi)
	}
	return out
}

func (m *Manager) get(id string) *managed {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.insts[id]
}

// ReloadEvery is how often the manager re-reads integration_instances, a
// safety net for writers that don't call Reload (the importer applies in
// its own transaction).
const ReloadEvery = time.Minute

// janitor re-reads the instances every ReloadEvery and drops resolved
// incidents after ResolvedRetention.
func (m *Manager) janitor() {
	reload := time.NewTicker(ReloadEvery)
	defer reload.Stop()
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-reload.C:
			if err := m.Reload(m.ctx); err != nil && m.ctx.Err() == nil {
				m.log.Error("reloading instances failed", "err", err)
			}
		case <-t.C:
			cutoff := m.o.Now().Add(-ResolvedRetention)
			m.mu.Lock()
			insts := m.all()
			m.mu.Unlock()
			for _, mi := range insts {
				mi.emit.Prune(cutoff)
			}
		}
	}
}

// Reload reconciles the running instances with the database: new rows
// start, changed rows restart, deleted rows stop. Start calls it; so can
// anything that writes integration_instances directly (the importer).
func (m *Manager) Reload(ctx context.Context) error {
	rows, err := loadRows(ctx, m.o.Store.Reader())
	if err != nil {
		return fmt.Errorf("runtime: loading instances: %w", err)
	}
	seen := map[string]bool{}
	for _, r := range rows {
		seen[r.ID] = true
		mi := m.get(r.ID)
		if mi == nil {
			m.restart(m.add(r))
			continue
		}
		mi.mu.Lock()
		changed := mi.row.UpdatedAt != r.UpdatedAt || mi.row.Name != r.Name || mi.row.Enabled != r.Enabled || !sameJSON(mi.row.Config, r.Config)
		if changed {
			mi.row = r
			mi.gen++
		}
		mi.mu.Unlock()
		if changed {
			m.restart(mi)
		}
	}
	m.mu.Lock()
	var gone []*managed
	for id, mi := range m.insts {
		if !seen[id] {
			gone = append(gone, mi)
			delete(m.insts, id)
		}
	}
	m.mu.Unlock()
	for _, mi := range gone {
		m.retire(mi)
	}
	return nil
}

// add registers a loaded or created row.
func (m *Manager) add(r row) *managed {
	mi := &managed{row: r, st: &state{}}
	if integ, man, ok := m.reg.Get(r.Integration); ok {
		mi.integ, mi.man = integ, man
	}
	mi.emit = NewEmitter(m.o.Topics, r.ID, m.o.Now)
	mi.emit.report = mi.st.report
	mi.emit.posture = m.Posture
	if m.actions != nil {
		mi.emit.annotate = m.actions.annotate
	}
	m.mu.Lock()
	m.insts[r.ID] = mi
	m.mu.Unlock()
	return mi
}

// retire stops an instance and removes its facts.
func (m *Manager) retire(mi *managed) {
	mi.life.Lock()
	defer mi.life.Unlock()
	m.detach(mi)
	if mi.coord != nil {
		mi.coord.stop()
		mi.coord = nil
	}
	mi.emit.Clear()
}

func (m *Manager) restart(mi *managed) {
	mi.life.Lock()
	defer mi.life.Unlock()
	m.restartLocked(mi)
}

// restartLocked (re)starts mi from its current row. The caller holds mi.life.
func (m *Manager) restartLocked(mi *managed) {
	if mi.coord != nil {
		mi.coord.stop()
		mi.coord = nil
	}
	r, gen := mi.snapshotRow()
	unknown := ""
	if mi.integ == nil {
		unknown = fmt.Sprintf("This hub doesn't include the %q integration.", r.Integration)
	}
	mi.st.reset(!r.Enabled, unknown)
	m.detach(mi)
	if !r.Enabled || mi.integ == nil {
		mi.emit.Clear()
		return
	}
	ctx := m.running()
	if ctx == nil {
		return // not started yet (or shutting down); Start runs it
	}
	log := m.log.With("instance", r.ID, "integration", r.Integration, "name", r.Name)
	full, secrets, err := m.resolve(ctx, mi.man, r.Config)
	if err == nil {
		// Rows written outside the API (the importer) haven't been validated.
		err = validateFull(mi.integ, mi.man, r.ID, full, newRedactor(secrets))
	}
	if err != nil {
		at := m.o.Now().UTC().Truncate(time.Second)
		if field := missingSecret(mi.man, err); field != "" {
			m.needSecret(mi, r, field, at, log)
			return
		}
		mi.st.failure(at, 0, integration.CodeInvalidConfig, integration.Permanent, err.Error())
		log.Warn("instance can't start", "err", err)
		return
	}
	red := newRedactor(secrets)
	log = newRedactLogger(log, red)
	cfg := integration.Config{InstanceID: r.ID, Name: r.Name, Values: full}
	// Sync never sees the control credential: it reads with the read one.
	readCfg := mi.man.ReadConfig(cfg)
	c := &coordinator{
		name: r.Name, integ: mi.integ, man: mi.man, cfg: readCfg,
		emit: mi.emit, st: mi.st, watch: m.o.Watch, log: log, red: red,
		poke: make(chan struct{}, 1),
	}
	c.deps = func() (integration.Deps, func()) {
		d, _, release := m.buildDeps(mi, gen, readCfg, log, mi.emit)
		return d, release
	}
	m.attach(ctx, mi, gen, cfg, log, c.syncSoon)
	mi.coord = c
	c.start(ctx)
}

// attach registers a starting instance's actions with the engine, if it
// has any. The caller holds mi.life.
func (m *Manager) attach(ctx context.Context, mi *managed, gen int, cfg integration.Config, log *slog.Logger, poke func()) {
	if m.actions == nil {
		return
	}
	self := SelfID(cfg.InstanceID)
	_, err := m.actions.Attach(ctx, ActorSpec{
		Instance: cfg.InstanceID, Name: cfg.Name, Integration: mi.integ, Manifest: mi.man, Config: cfg,
		Deps: func(c integration.Config) (integration.Deps, func()) {
			// Action instances publish nothing: Sync owns the facts.
			d, _, release := m.buildDeps(mi, gen, c, log, discard{self: self})
			return d, release
		},
		Poke: poke,
	})
	if err != nil {
		log.Error("registering the instance's actions failed", "err", err)
	}
}

// detach removes an instance's actions. The caller holds mi.life.
func (m *Manager) detach(mi *managed) {
	if m.actions == nil {
		return
	}
	r, _ := mi.snapshotRow()
	m.actions.Detach(r.ID)
}

// resolve returns a stored config with its secrets read from the vault, and
// the secret values (for redaction).
func (m *Manager) resolve(ctx context.Context, man integration.Manifest, stored map[string]any) (map[string]any, []string, error) {
	full := nonSecret(stored, man)
	var values []string
	for field, name := range refsOf(stored, man) {
		v, err := m.o.Vault.Get(ctx, name)
		if errors.Is(err, vault.ErrNotFound) {
			return nil, nil, &missingSecretError{field: field}
		}
		if err != nil {
			return nil, nil, fmt.Errorf("reading the secret for %s: %w", field, err)
		}
		full[field] = string(v)
		values = append(values, string(v))
	}
	return full, values, nil
}

// codeSecretMissing is the status code of an instance that can't start
// because a secret was never entered or its vault entry is gone.
const codeSecretMissing = "secret_missing"

// missingSecretError is a secret reference whose vault entry is gone.
type missingSecretError struct{ field string }

func (e *missingSecretError) Error() string {
	return fmt.Sprintf("the secret for %s is missing from the vault; enter it again", e.field)
}

// missingSecret returns the secret field an instance is missing, when that
// is the only thing stopping it: a reference whose vault entry is gone, or
// required secret fields that were never set (a Home file or Homepage
// import that named a secret it couldn't carry). Anything else wrong with
// the config gives "".
func missingSecret(man integration.Manifest, err error) string {
	if ms, ok := errors.AsType[*missingSecretError](err); ok {
		return ms.field
	}
	var ve *ValidationError
	if !errors.As(err, &ve) || len(ve.Fields) == 0 {
		return ""
	}
	first := ""
	for _, f := range ve.Fields {
		name := strings.TrimPrefix(f.Path, "/")
		if f.Message != "is required" || !man.IsSecret(name) {
			return ""
		}
		if first == "" {
			first = name
		}
	}
	return first
}

// needSecret marks an instance that lacks a secret as needing repair and
// raises the Repair, pointing at the field, so the Inbox's Reconnect opens
// the form on it. Entering the secret restarts the instance, and its first
// good sync resolves the Repair.
func (m *Manager) needSecret(mi *managed, r row, field string, at time.Time, log *slog.Logger) {
	msg := fmt.Sprintf("%s has no %s yet.", r.Name, fieldTitle(mi.man, field))
	mi.st.failure(at, 0, codeSecretMissing, integration.NeedsRepair, msg)
	c := &coordinator{name: r.Name, integ: mi.integ, man: mi.man, cfg: integration.Config{InstanceID: r.ID, Name: r.Name},
		emit: mi.emit, st: mi.st, watch: m.o.Watch, log: log}
	if err := c.raiseRepair(codeSecretMissing, msg, field, 0, at); err != nil {
		log.Error("raising a repair failed", "err", err)
	}
	c.publishSelf()
	log.Warn("instance needs a secret before it can start", "field", field)
}

// fieldTitle is a config field's title from the schema, else its name.
func fieldTitle(man integration.Manifest, field string) string {
	if man.ConfigSchema != nil {
		for _, p := range man.ConfigSchema.Properties {
			if p.Name == field && p.Schema != nil && p.Schema.Title != "" {
				return p.Schema.Title
			}
		}
	}
	return field
}

// buildDeps assembles an instance's Deps. mi is nil for an unsaved config
// under test: nothing is persisted then.
func (m *Manager) buildDeps(mi *managed, gen int, cfg integration.Config, log *slog.Logger, emit integration.Emitter) (integration.Deps, *Client, func()) {
	o := DepsOptions{Config: cfg, Emit: emit, Log: log, Now: m.o.Now, UserAgent: m.o.UserAgent, RootCAs: m.o.RootCAs, Nodes: m.o.Nodes}
	if mi != nil {
		o.UpdateConfig = func(ctx context.Context, mutate func(map[string]any)) error {
			return m.updateConfig(ctx, mi, gen, mutate)
		}
		o.State = dbState{st: m.o.Store, id: cfg.InstanceID, now: m.o.Now}
	}
	return NewDeps(o)
}

// DepsOptions configure NewDeps.
type DepsOptions struct {
	Config    integration.Config
	Emit      integration.Emitter
	Log       *slog.Logger     // nil: discard
	Now       func() time.Time // nil: time.Now
	UserAgent string
	RootCAs   *x509.CertPool
	// UpdateConfig persists a change to the non-secret config, TOFU pins
	// included. Nil drops such changes (an unsaved config under test).
	UpdateConfig func(ctx context.Context, mutate func(map[string]any)) error
	// State is the instance's key-value state. Nil: one in memory, dropped
	// with the Deps (an unsaved config under test).
	State integration.State
	// Nodes is Deps.Nodes. Nil: no nodes.
	Nodes integration.NodeCaller
}

// NewDeps builds the Deps an instance gets: its own HTTP client with the
// config's TLS policy (a TOFU pin is recorded through UpdateConfig), a
// factory for more clients, the emitter, the log, the clock and the
// instance's key-value state. The Manager uses it, and so can contract tests
// (see integrationtest). It returns the instance's own client, and a release
// func that logs out of sessions and closes every client made.
func NewDeps(o DepsOptions) (integration.Deps, *Client, func()) {
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.State == nil {
		o.State = NewMemoryState()
	}
	cfg, log := o.Config, o.Log
	var mu sync.Mutex
	var clients []*Client
	newClient := func(opts integration.HTTPOptions) (*Client, error) {
		co := ClientOptions{HTTPOptions: opts, UserAgent: o.UserAgent, RootCAs: o.RootCAs}
		if co.TLS.Mode == "" {
			co.TLS = cfg.TLS()
			co.OnPin = nil
			if o.UpdateConfig != nil && co.TLS.Mode == integration.TLSTOFU {
				co.OnPin = func(pin string) {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					err := o.UpdateConfig(ctx, func(c map[string]any) {
						t, _ := c["tls"].(map[string]any)
						if t == nil {
							t = map[string]any{"mode": string(integration.TLSTOFU)}
						}
						t["pin"] = pin
						c["tls"] = t
					})
					switch {
					case err == nil:
						log.Info("recorded the server's certificate (trust on first use)", "sha256", pin)
					case !errors.Is(err, errStale):
						log.Error("saving the pinned certificate failed", "err", err)
					}
				}
			}
		}
		c, err := NewHTTPClient(co)
		if err != nil {
			return nil, err
		}
		mu.Lock()
		clients = append(clients, c)
		mu.Unlock()
		return c, nil
	}
	own, err := newClient(integration.HTTPOptions{})
	if err != nil {
		// Only a pin-mode config without a pin gets here, and validation
		// refuses that; keep a verifying client so nothing runs unchecked.
		own, _ = NewHTTPClient(ClientOptions{UserAgent: o.UserAgent, RootCAs: o.RootCAs})
	}
	d := integration.Deps{
		HTTP: own.Client,
		NewHTTPClient: func(opts integration.HTTPOptions) (*http.Client, error) {
			c, err := newClient(opts)
			if err != nil {
				return nil, err
			}
			return c.Client, nil
		},
		Emit:  o.Emit,
		Log:   log,
		Now:   o.Now,
		Nodes: o.Nodes,
		UpdateConfig: func(ctx context.Context, mutate func(map[string]any)) error {
			if o.UpdateConfig == nil {
				return nil
			}
			return o.UpdateConfig(ctx, mutate)
		},
		State: o.State,
	}
	release := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range clients {
			c.Close(ctx)
		}
		own.Close(ctx)
	}
	return d, own, release
}

// errStale means the config changed since the caller's instance started.
var errStale = errors.New("runtime: the instance's config changed meanwhile")

// updateConfig persists a change to an instance's non-secret config without
// restarting it. Secret fields can't be changed this way.
func (m *Manager) updateConfig(ctx context.Context, mi *managed, gen int, mutate func(map[string]any)) error {
	mi.mu.Lock()
	defer mi.mu.Unlock()
	if mi.gen != gen {
		return errStale
	}
	cfg := cloneMap(mi.row.Config)
	refs := refsOf(cfg, mi.man)
	mutate(cfg)
	for _, f := range mi.man.SecretFields() {
		delete(cfg, f)
		if name, ok := refs[f]; ok {
			cfg[f] = integration.SecretRef(name)
		}
	}
	r := mi.row
	r.Config = cfg
	r.UpdatedAt = max(store.NowMillis(), mi.row.UpdatedAt+1)
	if err := saveRow(ctx, m.o.Store, r, false); err != nil {
		return err
	}
	mi.row = r
	return nil
}

// prepared is a validated config ready to save.
type prepared struct {
	stored map[string]any    // non-secret config plus secret references
	full   map[string]any    // with secret values, for Connect and validation
	puts   map[string]string // vault name → value to write
	orphan []string          // vault names the instance no longer references
	values []string          // every secret value, for redaction
}

// prepare validates a config. prev is the stored config being edited (nil
// for a new one); config nil keeps prev's non-secret part; in secrets a
// string sets a field and null or "" clears it.
func (m *Manager) prepare(ctx context.Context, id string, man integration.Manifest, integ integration.Integration,
	prev map[string]any, config map[string]any, secrets map[string]any) (prepared, error) {
	var fields []integration.FieldError
	var plain map[string]any
	if config != nil {
		plain = clean(config).(map[string]any)
	} else {
		plain = nonSecret(prev, man)
	}
	for k, v := range plain {
		if man.IsSecret(k) {
			fields = append(fields, integration.FieldError{Path: "/" + k, Message: "is a secret; send it in secrets"})
		} else if mentionsRef(v) {
			fields = append(fields, integration.FieldError{Path: "/" + k, Message: "can't hold a secret reference"})
		}
	}
	set := map[string]string{}
	cleared := map[string]bool{}
	for k, v := range secrets {
		if !man.IsSecret(k) {
			fields = append(fields, integration.FieldError{Path: "/" + k, Message: "isn't a secret field"})
			continue
		}
		switch x := v.(type) {
		case nil:
			cleared[k] = true
		case string:
			if x == "" {
				cleared[k] = true
			} else {
				set[k] = x
			}
		default:
			fields = append(fields, integration.FieldError{Path: "/" + k, Message: "must be a string"})
		}
	}
	if len(fields) > 0 {
		sortFields(fields)
		return prepared{}, &ValidationError{Fields: fields}
	}

	p := prepared{stored: cloneMap(plain), full: cloneMap(plain), puts: map[string]string{}}
	prevRefs := refsOf(prev, man)
	for _, f := range man.SecretFields() {
		switch {
		case set[f] != "":
			name := secretName(id, f)
			p.full[f], p.stored[f], p.puts[name] = set[f], integration.SecretRef(name), set[f]
			p.values = append(p.values, set[f])
		case cleared[f]:
		case prevRefs[f] != "":
			v, err := m.o.Vault.Get(ctx, prevRefs[f])
			if errors.Is(err, vault.ErrNotFound) {
				fields = append(fields, integration.FieldError{Path: "/" + f, Message: "is missing from the vault; enter it again"})
				continue
			}
			if err != nil {
				return prepared{}, err
			}
			p.full[f], p.stored[f] = string(v), integration.SecretRef(prevRefs[f])
			p.values = append(p.values, string(v))
		}
	}
	if len(fields) > 0 {
		sortFields(fields)
		return prepared{}, &ValidationError{Fields: fields}
	}
	if err := validateFull(integ, man, id, p.full, newRedactor(p.values)); err != nil {
		return prepared{}, err
	}
	for f, name := range prevRefs {
		if ref, _ := p.stored[f].(string); ref != integration.SecretRef(name) {
			p.orphan = append(p.orphan, name)
		}
	}
	return p, nil
}

// validateFull checks a config with its secrets resolved: the schema, the
// tls policy, then the integration's own rules. Messages are scrubbed.
func validateFull(integ integration.Integration, man integration.Manifest, id string, full map[string]any, red redactor) error {
	fields := man.ConfigSchema.Validate(full)
	if len(fields) == 0 {
		if _, err := integration.ParseTLS(full["tls"]); err != nil {
			fields = append(fields, integration.FieldError{Path: "/tls", Message: err.Error()})
		}
	}
	if len(fields) == 0 {
		fields = man.CheckControl(full)
	}
	if len(fields) == 0 {
		if v, ok := integ.(integration.ConfigValidator); ok {
			fields = v.ValidateConfig(integration.Config{InstanceID: id, Values: cloneMap(full)})
		}
	}
	if len(fields) == 0 {
		return nil
	}
	for i := range fields {
		fields[i].Message = red.scrub(fields[i].Message)
	}
	sortFields(fields)
	return &ValidationError{Fields: fields}
}

func sortFields(fs []integration.FieldError) {
	slices.SortStableFunc(fs, func(a, b integration.FieldError) int { return strings.Compare(a.Path, b.Path) })
}

// writeSecrets stores puts and returns an undo that puts the previous
// values back (or deletes entries that didn't exist), for when saving the
// config that references them fails. On error it has already undone.
func (m *Manager) writeSecrets(ctx context.Context, puts map[string]string) (undo func(), err error) {
	prev := map[string][]byte{} // nil: didn't exist
	undo = func() {
		for name, v := range prev {
			var err error
			if v == nil {
				err = m.o.Vault.Delete(ctx, name)
			} else {
				err = m.o.Vault.Put(ctx, name, v)
			}
			if err != nil && !errors.Is(err, vault.ErrNotFound) {
				m.log.Error("restoring a secret failed", "secret", name, "err", err)
			}
		}
	}
	for name, v := range puts {
		old, err := m.o.Vault.Get(ctx, name)
		switch {
		case errors.Is(err, vault.ErrNotFound):
			old = nil
		case err != nil:
			undo()
			return func() {}, fmt.Errorf("runtime: reading a secret: %w", err)
		}
		if err := m.o.Vault.Put(ctx, name, []byte(v)); err != nil {
			undo()
			return func() {}, fmt.Errorf("runtime: storing a secret: %w", err)
		}
		prev[name] = old
	}
	return undo, nil
}

// deleteSecrets removes vault entries no instance other than except
// references any more.
func (m *Manager) deleteSecrets(ctx context.Context, names []string, except string) {
	if len(names) == 0 {
		return
	}
	rows, err := loadRows(ctx, m.o.Store.Reader())
	if err != nil {
		m.log.Error("listing instances to clean up secrets failed", "err", err)
		return
	}
	used := map[string]bool{}
	for _, r := range rows {
		if r.ID == except {
			continue
		}
		for _, v := range r.Config {
			if s, ok := v.(string); ok {
				if n, ok := integration.ParseSecretRef(s); ok {
					used[n] = true
				}
			}
		}
	}
	for _, n := range names {
		if used[n] {
			continue
		}
		if err := m.o.Vault.Delete(ctx, n); err != nil && !errors.Is(err, vault.ErrNotFound) {
			m.log.Error("deleting a secret failed", "secret", n, "err", err)
		}
	}
}

// cleanName checks an instance name.
func cleanName(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" || utf8.RuneCountInString(s) > 64 || strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return "", &ValidationError{Fields: []integration.FieldError{{Path: "/name", Message: "must be 1–64 characters"}}}
	}
	return s, nil
}

// CreateRequest is the body of POST /api/integrations/instances.
type CreateRequest struct {
	Integration string         `json:"integration"`
	Name        string         `json:"name"`
	Config      map[string]any `json:"config"`
	Secrets     map[string]any `json:"secrets"`
	Enabled     *bool          `json:"enabled"` // default true
}

// Create validates and saves a new instance and starts it.
func (m *Manager) Create(ctx context.Context, req CreateRequest) (string, error) {
	integ, man, ok := m.reg.Get(req.Integration)
	if !ok {
		return "", ErrUnknownIntegration
	}
	name, err := cleanName(req.Name)
	if err != nil {
		return "", err
	}
	id := "i_" + strings.ToLower(rand.Text())
	cfg := req.Config
	if cfg == nil {
		cfg = map[string]any{}
	}
	p, err := m.prepare(ctx, id, man, integ, nil, cfg, req.Secrets)
	if err != nil {
		return "", err
	}
	undo, err := m.writeSecrets(ctx, p.puts)
	if err != nil {
		return "", err
	}
	now := store.NowMillis()
	r := row{ID: id, Integration: man.ID, Name: name, Config: p.stored, Enabled: req.Enabled == nil || *req.Enabled, CreatedAt: now, UpdatedAt: now}
	if err := saveRow(ctx, m.o.Store, r, true); err != nil {
		undo()
		if errors.Is(err, errNameTaken) {
			return "", ErrNameTaken
		}
		return "", err
	}
	m.log.Info("integration instance created", "instance", id, "integration", man.ID, "name", name)
	m.restart(m.add(r))
	return id, nil
}

// UpdateRequest is the body of PUT /api/integrations/instances/{id}. Nil
// fields stay as they are. Config replaces the whole non-secret config (send
// tls.pin back to keep a TOFU pin); in Secrets a string sets a field and null
// clears it, and fields left out keep their value.
type UpdateRequest struct {
	Name    *string        `json:"name"`
	Config  map[string]any `json:"config"`
	Secrets map[string]any `json:"secrets"`
	Enabled *bool          `json:"enabled"`
}

// Update changes an instance and restarts it.
func (m *Manager) Update(ctx context.Context, id string, req UpdateRequest) error {
	mi := m.get(id)
	if mi == nil {
		return ErrNotFound
	}
	mi.life.Lock()
	defer mi.life.Unlock()
	prev, _ := mi.snapshotRow()
	next := prev
	if req.Name != nil {
		name, err := cleanName(*req.Name)
		if err != nil {
			return err
		}
		next.Name = name
	}
	if req.Enabled != nil {
		next.Enabled = *req.Enabled
	}
	var p prepared
	undo := func() {}
	if req.Config != nil || req.Secrets != nil {
		if mi.integ == nil {
			return ErrUnknownIntegration
		}
		var err error
		if p, err = m.prepare(ctx, id, mi.man, mi.integ, prev.Config, req.Config, req.Secrets); err != nil {
			return err
		}
		next.Config = p.stored
		if undo, err = m.writeSecrets(ctx, p.puts); err != nil {
			return err
		}
	}
	mi.mu.Lock()
	next.UpdatedAt = max(store.NowMillis(), mi.row.UpdatedAt+1)
	err := saveRow(ctx, m.o.Store, next, false)
	if err == nil {
		mi.row = next
		mi.gen++
	}
	mi.mu.Unlock()
	if err != nil {
		undo()
		switch {
		case errors.Is(err, errNameTaken):
			return ErrNameTaken
		case errors.Is(err, errNotFound):
			return ErrNotFound
		}
		return err
	}
	m.deleteSecrets(ctx, p.orphan, "")
	if req.Config != nil || req.Secrets != nil || req.Enabled != nil {
		// The user acted on the instance: an open repair is being fixed until
		// the next try says otherwise.
		mi.emit.setPhase(repairID, PhaseFixing)
	}
	m.log.Info("integration instance updated", "instance", id, "integration", next.Integration, "name", next.Name, "enabled", next.Enabled)
	m.restartLocked(mi)
	return nil
}

// Delete stops an instance, removes its facts, its row and its secrets.
func (m *Manager) Delete(ctx context.Context, id string) error {
	mi := m.get(id)
	if mi == nil {
		return ErrNotFound
	}
	mi.life.Lock()
	defer mi.life.Unlock()
	m.detach(mi)
	if mi.coord != nil {
		mi.coord.stop()
		mi.coord = nil
	}
	mi.emit.Clear()
	r, _ := mi.snapshotRow()
	if err := deleteRow(ctx, m.o.Store, id); err != nil && !errors.Is(err, errNotFound) {
		return err
	}
	m.mu.Lock()
	delete(m.insts, id)
	m.mu.Unlock()
	m.deleteSecrets(ctx, slices.Collect(mapValues(refsOf(r.Config, mi.man))), id)
	m.log.Info("integration instance deleted", "instance", id, "integration", r.Integration, "name", r.Name)
	return nil
}

func mapValues(m map[string]string) func(func(string) bool) {
	return func(yield func(string) bool) {
		for _, v := range m {
			if !yield(v) {
				return
			}
		}
	}
}

// SecretState is how a secret field appears in responses: only whether it's set.
type SecretState struct {
	Set bool `json:"set"`
}

// InstanceView is an instance as the API returns it. Secrets appear only as
// {"set": bool}.
type InstanceView struct {
	ID          string     `json:"id"`
	Integration string     `json:"integration"`
	Name        string     `json:"name"`
	Enabled     bool       `json:"enabled"`
	Status      string     `json:"status"`
	Version     string     `json:"version"`
	LastOK      *time.Time `json:"last_ok"`
	// LastError is the last failure's message (secrets scrubbed), with its
	// stable code and time beside it; all null when the last sync worked
	// and nothing failed before it.
	LastError     *string                `json:"last_error"`
	LastErrorCode *string                `json:"last_error_code"`
	LastErrorAt   *time.Time             `json:"last_error_at"`
	Warnings      []integration.Warning  `json:"warnings"`
	Config        map[string]any         `json:"config"`
	Secrets       map[string]SecretState `json:"secrets"`
	CreatedAt     time.Time              `json:"created_at"`
	UpdatedAt     time.Time              `json:"updated_at"`
}

func (m *Manager) view(mi *managed) InstanceView {
	r, _ := mi.snapshotRow()
	snap := mi.st.snapshot()
	v := InstanceView{
		ID: r.ID, Integration: r.Integration, Name: r.Name, Enabled: r.Enabled,
		Status: snap.Status, Version: snap.Version,
		Warnings: slices.Concat(snap.Warnings, configWarnings(integration.Config{Values: r.Config})),
		Config:   nonSecret(r.Config, mi.man), Secrets: map[string]SecretState{},
		CreatedAt: time.UnixMilli(r.CreatedAt).UTC(), UpdatedAt: time.UnixMilli(r.UpdatedAt).UTC(),
	}
	if !snap.LastOK.IsZero() {
		t := snap.LastOK
		v.LastOK = &t
	}
	if e := snap.LastErr; e != nil {
		v.LastError, v.LastErrorCode, v.LastErrorAt = &e.Message, &e.Code, &e.At
	}
	refs := refsOf(r.Config, mi.man)
	for _, f := range mi.man.SecretFields() {
		v.Secrets[f] = SecretState{Set: refs[f] != ""}
	}
	for f := range refs {
		v.Secrets[f] = SecretState{Set: true}
	}
	return v
}

// List returns every instance, sorted by integration and name.
func (m *Manager) List() []InstanceView {
	m.mu.Lock()
	insts := m.all()
	m.mu.Unlock()
	out := make([]InstanceView, 0, len(insts))
	for _, mi := range insts {
		out = append(out, m.view(mi))
	}
	slices.SortFunc(out, func(a, b InstanceView) int {
		if c := strings.Compare(a.Integration, b.Integration); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})
	return out
}

// Refresh asks a running instance to sync now with Interest.Refresh set,
// without waiting for it. The result arrives on the usual topics.
func (m *Manager) Refresh(id string) error {
	mi := m.get(id)
	if mi == nil {
		return ErrNotFound
	}
	mi.life.Lock()
	defer mi.life.Unlock()
	if mi.coord == nil {
		return ErrNotRunning
	}
	mi.coord.refreshNow()
	return nil
}

// Get returns one instance.
func (m *Manager) Get(id string) (InstanceView, error) {
	mi := m.get(id)
	if mi == nil {
		return InstanceView{}, ErrNotFound
	}
	return m.view(mi), nil
}

// CatalogEntry is one integration as the catalog serves it: the manifest's
// public fields, plus its least-privilege recipe when it has one.
type CatalogEntry struct {
	integration.Manifest
	Recipe *integration.Recipe `json:"recipe,omitempty"`
}

// Catalog lists the integrations this hub can run, by ID.
func (m *Manager) Catalog() []CatalogEntry {
	ms := m.reg.Manifests()
	out := make([]CatalogEntry, len(ms))
	for i, man := range ms {
		out[i].Manifest = man
		if r, ok := m.reg.Recipe(man.ID); ok {
			out[i].Recipe = &r
		}
	}
	return out
}

// testTimeout bounds a connection test.
const testTimeout = 30 * time.Second

// TestRequest is the body of POST /api/integrations/test: an unsaved
// config, or unsaved edits of a saved instance (InstanceID set), where
// secrets not retyped come from the vault and a nil Config means the saved
// one.
type TestRequest struct {
	Integration string         `json:"integration"`
	InstanceID  string         `json:"instance_id"`
	Config      map[string]any `json:"config"`
	Secrets     map[string]any `json:"secrets"`
}

// Test runs "Test connection" on an unsaved config. Nothing is stored: a
// TOFU certificate is reported in the result, not pinned.
func (m *Manager) Test(ctx context.Context, req TestRequest) (integration.TestResult, error) {
	var (
		integ integration.Integration
		man   integration.Manifest
		prev  map[string]any
		id    = "test"
		name  = "Test"
	)
	config := req.Config
	if req.InstanceID != "" {
		mi := m.get(req.InstanceID)
		if mi == nil {
			return integration.TestResult{}, ErrNotFound
		}
		if mi.integ == nil || (req.Integration != "" && req.Integration != mi.man.ID) {
			return integration.TestResult{}, ErrUnknownIntegration
		}
		r, _ := mi.snapshotRow()
		integ, man, prev, id, name = mi.integ, mi.man, r.Config, r.ID, r.Name
	} else {
		var ok bool
		if integ, man, ok = m.reg.Get(req.Integration); !ok {
			return integration.TestResult{}, ErrUnknownIntegration
		}
		if config == nil {
			config = map[string]any{}
		}
	}
	p, err := m.prepare(ctx, id, man, integ, prev, config, req.Secrets)
	if err != nil {
		return integration.TestResult{}, err
	}
	cfg := integration.Config{InstanceID: req.InstanceID, Name: name, Values: p.full}
	return m.runTest(ctx, nil, 0, integ, man, cfg, newRedactor(p.values)), nil
}

// TestSaved runs "Test connection" on a saved instance. In tofu mode with
// no pin yet, the certificate seen is pinned (the first success). A running
// instance then syncs at once (as Check now does), so a fixed connection
// clears its status and problems without waiting for the next interval.
func (m *Manager) TestSaved(ctx context.Context, id string) (integration.TestResult, error) {
	mi := m.get(id)
	if mi == nil {
		return integration.TestResult{}, ErrNotFound
	}
	if mi.integ == nil {
		return integration.TestResult{}, ErrUnknownIntegration
	}
	r, gen := mi.snapshotRow()
	full, secrets, err := m.resolve(ctx, mi.man, r.Config)
	if err != nil {
		return integration.TestResult{ErrorCode: integration.CodeInvalidConfig, ErrorMessage: err.Error()}, nil
	}
	cfg := integration.Config{InstanceID: r.ID, Name: r.Name, Values: full}
	res := m.runTest(ctx, mi, gen, mi.integ, mi.man, cfg, newRedactor(secrets))
	_ = m.Refresh(id) // a disabled or stopped instance has nothing to sync
	return res, nil
}

func (m *Manager) runTest(ctx context.Context, mi *managed, gen int, integ integration.Integration, man integration.Manifest,
	cfg integration.Config, red redactor) (res integration.TestResult) {
	ctx, cancel := context.WithTimeout(ctx, testTimeout)
	defer cancel()
	log := newRedactLogger(m.log.With("integration", man.ID, "instance", cfg.InstanceID, "test", true), red)
	self := SelfID(cmpStr(cfg.InstanceID, "test"))
	deps, own, release := m.buildDeps(mi, gen, cfg, log, discard{self: self})
	defer release()
	start := m.o.Now()
	res = func() (res integration.TestResult) {
		defer func() {
			if v := recover(); v != nil {
				log.Error("integration panicked in a test", "panic", fmt.Sprint(v))
				code, _, msg := classify(&panicError{v})
				res = integration.TestResult{ErrorCode: code, ErrorMessage: msg}
			}
		}()
		// Test connection tests the credential Cha Dash reads with.
		inst, err := integ.Connect(ctx, man.ReadConfig(cfg), deps)
		if err != nil {
			return integration.Failed(err)
		}
		defer func() { _ = safeClose(inst) }()
		return inst.Test(ctx)
	}()
	if res.Latency == 0 {
		res.Latency = m.o.Now().Sub(start)
	}
	if !res.OK && res.ErrorCode == "" {
		res.ErrorCode = integration.CodeUpstream
	}
	if fp := own.Observed(); fp != "" {
		res.Certificate = &integration.Certificate{SHA256: fp}
	}
	res.Warnings = append(res.Warnings, frameworkWarnings(man, cfg, res.Grants)...)
	res.ErrorMessage = red.scrub(res.ErrorMessage)
	res.Version = red.scrub(res.Version)
	for i := range res.Warnings {
		res.Warnings[i].Message = red.scrub(res.Warnings[i].Message)
	}
	for i := range res.Grants {
		res.Grants[i].Name = red.scrub(res.Grants[i].Name)
	}
	return res
}

// frameworkWarnings are the checks every test gets.
func frameworkWarnings(man integration.Manifest, cfg integration.Config, grants []integration.Grant) []integration.Warning {
	var out []integration.Warning
	if w := integration.WriteGrantWarning(grants, man.ExpectedWrites(cfg)...); w != nil {
		out = append(out, *w)
	}
	if w := controlWarning(man, cfg); w != nil {
		out = append(out, *w)
	}
	out = append(out, configWarnings(cfg)...)
	u, _ := url.Parse(cfg.String("url"))
	if u != nil && u.Scheme == "http" {
		for _, f := range man.SecretFields() {
			if s, _ := cfg.Values[f].(string); s != "" {
				out = append(out, integration.Warning{Code: "plain_http", Severity: integration.WarnWarning,
					Message: "The credential is sent over plain http://, readable by anyone on the network path."})
				break
			}
		}
	}
	return out
}

// controlWarning is the hint that a read-only connection can be given
// control: for an integration with a control credential (or mode) that
// this config doesn't have yet. It points at the recipe's control variant.
func controlWarning(man integration.Manifest, cfg integration.Config) *integration.Warning {
	if man.Control == nil || (len(man.Control.Fields) == 0 && man.Control.Mode == "") {
		return nil
	}
	if _, ok := man.ControlConfig(cfg); ok {
		return nil
	}
	msg := "This connection only reads. To let Cha Dash act on it too (with a plan, a confirmation and an audit record each time), add a control credential: the recipe's control variant makes one with only the rights those actions need."
	if man.Control.Mode != "" {
		msg = "This connection only reads. To let Cha Dash act on it too, switch it to control in its settings."
	}
	return &integration.Warning{Code: "control_available", Severity: integration.WarnInfo, Message: msg}
}

// configWarnings are the risks a saved config keeps carrying. A test reports
// them, and so does the instance's view for as long as the config has them,
// so turning certificate checks off isn't forgotten. They don't change the
// instance's status. Plain http:// stays a test-time warning only: on a LAN
// it's the norm, and a lasting badge on most instances would be noise.
func configWarnings(cfg integration.Config) []integration.Warning {
	u, _ := url.Parse(cfg.String("url"))
	if u != nil && u.Scheme == "https" && cfg.TLS().Mode == integration.TLSInsecure {
		return []integration.Warning{{Code: "tls_insecure", Severity: integration.WarnWarning,
			Message: "Certificate checks are off for this instance, so anyone on the network path could impersonate it. Trust on first use is safer."}}
	}
	return nil
}

func cmpStr(a, b string) string {
	if a == "" {
		return b
	}
	return a
}

// discard is the emitter a test gets: tests must not publish facts.
type discard struct{ self string }

func (d discard) Self() string                          { return d.self }
func (discard) Replace(string, integration.Facts) error { return nil }
func (discard) Raise(integration.Incident) error        { return nil }
func (discard) Resolve(string, string) error            { return nil }
func (discard) SetVersion(string)                       {}
func (discard) SetWarnings([]integration.Warning)       {}
