package runtime

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"

	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/integration"
)

// Actions connects running integration instances to the action engine.
//
// Each instance whose integration is an integration.Actor gets two
// instances of its own beside the one Sync runs on: one connected with the
// read config (integration.Manifest.ReadConfig) for the Def hooks that read
// (Resolve, Preconditions, Verify, …), and one with the control config
// (ControlConfig) for Execute. With no control credential there is no
// second instance, and the actions answer 409 unavailable with the
// manifest's sentence and "recipe" naming the instance. An integration
// without a control group (or with a mode) acts with the same credential,
// so one instance serves both.
//
// Several instances of one integration offer the same action IDs, and the
// engine's registry gives an ID to one source. So the registry gets one
// source per integration ("integration/<id>"), and each of its Defs routes
// every hook to the instance that owns the target: the instance whose
// emitter published the resource (its graph Source), else the one named in
// the ID's "<integration>:<instance id>/" prefix. The static fields (tier,
// params, title) come from the first instance, in instance ID order: every
// instance of an integration runs the same code. Attaching an instance (on
// start, restart and reconfigure) re-registers the source with its
// actions; detaching it (stop, disable, delete) removes them, and the
// source goes when its last instance does.
//
// The self-aware T4: a Def marked Disrupts (reboot, stop) is T4 when its
// target runs the hub or the router, transitively over runs_on and part_of
// in the Home Graph (the hub is integration.HubResource or a resource with
// attrs.self; a router is a resource of kind router), on top of whatever
// the integration's own TierFor says.
//
// An Actor's hooks and Execute may run concurrently with each other, never
// on the instance Sync runs on.
type Actions struct {
	reg   *action.Registry
	graph *graph.Graph
	log   *slog.Logger

	mu    sync.RWMutex
	insts map[string]*actorInst // instance ID → its actions
	ids   map[string]string     // action ID → integration ID
	// changed runs after an instance's actions change (outside mu), so
	// incidents' availability can be published again.
	changed func()
}

// actorInst is one instance's actions.
type actorInst struct {
	id, name, integ string
	defs            map[string]action.Def // the read instance's Actions(), by ID
	order           []string              // their IDs, as declared
	ctl             map[string]action.Def // ActionDefs of the control instance; nil without a control credential
	missing         string                // why not, without one
	poke            func()

	mu       sync.Mutex
	closed   bool
	inflight sync.WaitGroup
	release  []func()
}

// acquire marks a hook running on ai's instances, so detaching waits for
// it before closing them.
func (ai *actorInst) acquire() bool {
	ai.mu.Lock()
	defer ai.mu.Unlock()
	if ai.closed {
		return false
	}
	ai.inflight.Add(1)
	return true
}

func (ai *actorInst) done() { ai.inflight.Done() }

// close releases ai's instances once nothing runs on them.
func (ai *actorInst) close() {
	ai.mu.Lock()
	if ai.closed {
		ai.mu.Unlock()
		return
	}
	ai.closed = true
	rel := ai.release
	ai.mu.Unlock()
	go func() {
		ai.inflight.Wait()
		for _, f := range slices.Backward(rel) {
			f()
		}
	}()
}

func (ai *actorInst) syncSoon() {
	if ai.poke != nil {
		ai.poke()
	}
}

// NewActions returns the runtime's bridge to reg. g is the Home Graph the
// instances publish into (for routing, default resolution and the
// self-aware T4).
func NewActions(reg *action.Registry, g *graph.Graph, log *slog.Logger) *Actions {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Actions{reg: reg, graph: g, log: log, insts: map[string]*actorInst{}, ids: map[string]string{}}
}

// OnChange sets what runs after an instance's actions change.
func (a *Actions) OnChange(fn func()) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.changed = fn
}

// ActorSpec is one instance to attach.
type ActorSpec struct {
	Instance    string // the instance ID
	Name        string // its name, for sentences
	Integration integration.Integration
	Manifest    integration.Manifest
	// Config has its secrets resolved, the control group included.
	Config integration.Config
	// Deps builds the Deps for one Connect of cfg; the func releases them.
	// The instances it builds for must not publish facts.
	Deps func(cfg integration.Config) (integration.Deps, func())
	// Poke asks the instance's coordinator to sync soon, after an action
	// ran, so the graph catches up. Optional.
	Poke func()
}

// source is the registry source of an integration's actions.
func source(integ string) string { return "integration/" + integ }

// Attach connects spec's instance for actions and registers them,
// replacing what it had. It reports false, having detached the instance,
// when its integration isn't an Actor.
func (a *Actions) Attach(ctx context.Context, spec ActorSpec) (bool, error) {
	readCfg := spec.Manifest.ReadConfig(spec.Config)
	var release []func()
	fail := func(err error) (bool, error) {
		for _, f := range slices.Backward(release) {
			f()
		}
		a.Detach(spec.Instance)
		return false, err
	}
	read, rel, err := connectActor(ctx, spec, readCfg)
	if rel != nil {
		release = append(release, rel)
	}
	if err != nil || read == nil {
		return fail(err)
	}
	ai := &actorInst{id: spec.Instance, name: spec.Name, integ: spec.Manifest.ID, defs: map[string]action.Def{},
		missing: spec.Manifest.ControlMissing(spec.Name), poke: spec.Poke}
	for _, d := range read.Actions() {
		if _, dup := ai.defs[d.ID]; dup || d.ID == "" {
			return fail(fmt.Errorf("runtime: %s: action %q is empty or declared twice", spec.Manifest.ID, d.ID))
		}
		ai.defs[d.ID] = d
		ai.order = append(ai.order, d.ID)
	}
	if ctlCfg, ok := spec.Manifest.ControlConfig(spec.Config); ok {
		ctl := read
		if len(spec.Manifest.ControlFields()) > 0 {
			var rel func()
			ctl, rel, err = connectActor(ctx, spec, ctlCfg)
			if rel != nil {
				release = append(release, rel)
			}
			if err != nil || ctl == nil {
				return fail(cmp.Or(err, fmt.Errorf("runtime: %s: the control instance isn't an Actor", spec.Manifest.ID)))
			}
		}
		ai.ctl = map[string]action.Def{}
		for _, d := range integration.ActionDefs(ctl) {
			ai.ctl[d.ID] = d
		}
	}
	ai.release = release

	a.mu.Lock()
	old := a.insts[spec.Instance]
	a.insts[spec.Instance] = ai
	err = a.registerLocked(ai.integ)
	if err != nil {
		// Keep the registry as it was: the instance offers nothing.
		delete(a.insts, spec.Instance)
		_ = a.registerLocked(ai.integ)
	}
	changed := a.changed
	a.mu.Unlock()
	if old != nil {
		old.close()
	}
	if err != nil {
		ai.close()
		return false, err
	}
	if changed != nil {
		changed()
	}
	return true, nil
}

// connectActor connects one instance for actions. A nil Actor with no
// error means the integration has no actions.
func connectActor(ctx context.Context, spec ActorSpec, cfg integration.Config) (act integration.Actor, release func(), err error) {
	deps, rel := spec.Deps(cfg)
	ctx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	inst, err := func() (inst integration.Instance, err error) {
		defer func() {
			if v := recover(); v != nil {
				inst, err = nil, &panicError{v}
			}
		}()
		return spec.Integration.Connect(ctx, cfg, deps)
	}()
	if err != nil {
		rel()
		return nil, nil, err
	}
	act, ok := inst.(integration.Actor)
	if !ok {
		_ = safeClose(inst)
		rel()
		return nil, nil, nil
	}
	return act, func() { _ = safeClose(inst); rel() }, nil
}

// Detach removes an instance's actions.
func (a *Actions) Detach(instance string) {
	a.mu.Lock()
	ai := a.insts[instance]
	if ai == nil {
		a.mu.Unlock()
		return
	}
	delete(a.insts, instance)
	if err := a.registerLocked(ai.integ); err != nil {
		a.log.Error("re-registering an integration's actions failed", "integration", ai.integ, "err", err)
	}
	changed := a.changed
	a.mu.Unlock()
	ai.close()
	if changed != nil {
		changed()
	}
}

// Attached lists the instances with actions, sorted.
func (a *Actions) Attached() []string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make([]string, 0, len(a.insts))
	for id := range a.insts {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}

// registerLocked registers integ's source from its attached instances, or
// removes it when none are left. The caller holds a.mu.
func (a *Actions) registerLocked(integ string) error {
	var ids []string
	for id, ai := range a.insts {
		if ai.integ == integ {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	for aid, in := range a.ids {
		if in == integ {
			delete(a.ids, aid)
		}
	}
	var defs []action.Def
	seen := map[string]bool{}
	for _, id := range ids {
		ai := a.insts[id]
		for _, aid := range ai.order {
			if seen[aid] {
				continue
			}
			seen[aid] = true
			defs = append(defs, a.mux(integ, ai.defs[aid]))
		}
	}
	if len(defs) == 0 {
		a.reg.Unregister(source(integ))
		return nil
	}
	if err := a.reg.Register(source(integ), defs...); err != nil {
		a.reg.Unregister(source(integ))
		return err
	}
	for aid := range seen {
		a.ids[aid] = integ
	}
	return nil
}

// instanceOf is the instance that owns target: the one whose emitter
// published it, else the one its ID's prefix names.
func (a *Actions) instanceOf(integ, target string) string {
	if a.graph != nil {
		if r, ok := a.graph.Get(graph.RID(target)); ok {
			if id, ok := strings.CutPrefix(r.Source, "integration:"); ok {
				return id
			}
		}
	}
	rest, ok := strings.CutPrefix(target, integ+":")
	if !ok {
		return ""
	}
	id, _, _ := strings.Cut(rest, "/")
	return id
}

// route finds the instance that owns target and its Def for the action.
func (a *Actions) route(integ, id, target string) (*actorInst, action.Def, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	ai := a.insts[a.instanceOf(integ, target)]
	if ai == nil || ai.integ != integ {
		return nil, action.Def{}, false
	}
	d, ok := ai.defs[id]
	return ai, d, ok
}

// errGone is what a hook answers when the instance that owned the target
// went away between resolving and running.
func errGone() error {
	return action.Unavailable("The integration instance this runs on isn't running right now.", "")
}

// mux is the registered Def for one action of integ: the static fields of
// proto, and hooks that route to the instance owning the target.
func (a *Actions) mux(integ string, proto action.Def) action.Def {
	id := proto.ID
	d := action.Def{
		ID: id, Title: proto.Title, Targets: proto.Targets, Params: proto.Params, Tier: proto.Tier, Scope: proto.Scope,
		Household: proto.Household, ReadsData: proto.ReadsData, Async: proto.Async, Disrupts: proto.Disrupts, Timeout: proto.Timeout, VerifyTimeout: proto.VerifyTimeout,
	}
	d.Resolve = func(ctx context.Context, target string) (action.Target, error) {
		ai, def, ok := a.route(integ, id, target)
		if !ok || !ai.acquire() {
			return action.Target{}, action.ErrNotFound
		}
		defer ai.done()
		if def.Resolve != nil {
			return def.Resolve(ctx, target)
		}
		return action.ResolveInGraph(a.graph, &def, target)
	}
	d.TierFor = func(t action.Target) action.Tier {
		_, def, ok := a.route(integ, id, t.ID)
		if !ok {
			return d.Tier
		}
		tier := def.Tier
		if def.TierFor != nil {
			tier = max(tier, def.TierFor(t))
		}
		if def.Disrupts && a.Critical(t.ID) {
			tier = action.T4
		}
		return tier
	}
	d.Preconditions = func(ctx context.Context, req action.Request) error {
		ai, def, ok := a.route(integ, id, req.Target.ID)
		if !ok || !ai.acquire() {
			return errGone()
		}
		defer ai.done()
		if ai.ctl == nil {
			return action.Unavailable(ai.missing, ai.id)
		}
		cd, ok := ai.ctl[id]
		if !ok {
			return action.Unavailable(fmt.Sprintf("%s's control credential doesn't offer this action.", ai.name), ai.id)
		}
		if def.Preconditions != nil {
			if err := def.Preconditions(ctx, req); err != nil {
				return err
			}
		}
		// The credential that acts says whether it may (its ACL).
		if cd.Permitted != nil {
			return cd.Permitted(ctx, req)
		}
		return nil
	}
	title := cmp.Or(proto.Title, id)
	if proto.Label != nil {
		d.Label = func(req action.Request) string {
			if _, def, ok := a.route(integ, id, req.Target.ID); ok && def.Label != nil {
				return def.Label(req)
			}
			return title + " " + req.Target.Name
		}
	}
	if proto.Steps != nil {
		d.Steps = func(req action.Request) []action.Step {
			if _, def, ok := a.route(integ, id, req.Target.ID); ok && def.Steps != nil {
				return def.Steps(req)
			}
			return nil
		}
	}
	if proto.BlastRadius != nil {
		d.BlastRadius = func(ctx context.Context, req action.Request) []action.Impact {
			if ai, def, ok := a.route(integ, id, req.Target.ID); ok && def.BlastRadius != nil && ai.acquire() {
				defer ai.done()
				return def.BlastRadius(ctx, req)
			}
			return action.GraphImpact(a.graph, req.Target.ID)
		}
	}
	if proto.Undo != nil {
		d.Undo = func(req action.Request) *action.Undo {
			if _, def, ok := a.route(integ, id, req.Target.ID); ok && def.Undo != nil {
				return def.Undo(req)
			}
			return nil
		}
	}
	d.Execute = func(ctx context.Context, req action.Request, run *action.TaskRun) error {
		ai, _, ok := a.route(integ, id, req.Target.ID)
		if !ok || !ai.acquire() {
			return action.Fail("unavailable", "The integration instance this runs on isn't running right now.")
		}
		defer ai.done()
		defer ai.syncSoon()
		cd, ok := ai.ctl[id]
		if !ok {
			return action.Fail("unavailable", ai.missing)
		}
		return cd.Execute(ctx, req, run)
	}
	if proto.Verify != nil {
		d.Verify = func(ctx context.Context, req action.Request, run *action.TaskRun) error {
			ai, def, ok := a.route(integ, id, req.Target.ID)
			if !ok || !ai.acquire() {
				return action.Fail("unavailable", "The integration instance this runs on stopped before the result could be checked.")
			}
			defer ai.done()
			defer ai.syncSoon()
			if def.Verify == nil {
				return nil
			}
			return def.Verify(ctx, req, run)
		}
	}
	return d
}

// maxCriticalDepth bounds the walk for the self-aware T4.
const maxCriticalDepth = 8

// Critical reports whether target runs the hub or the router: it is one,
// or one runs on it or is part of it, transitively, in the Home Graph.
func (a *Actions) Critical(target string) bool {
	if a.graph == nil {
		return false
	}
	_, doc := a.graph.Snapshot(nil)
	on := map[string][]string{} // resource → what runs on or is part of it
	for _, r := range doc.Relations {
		if r.Type == "runs_on" || r.Type == "part_of" {
			on[string(r.To)] = append(on[string(r.To)], string(r.From))
		}
	}
	kinds := map[string]*graph.Resource{}
	for _, r := range doc.Resources {
		kinds[string(r.ID)] = r
	}
	critical := func(id string) bool {
		if id == integration.HubResource {
			return true
		}
		r, ok := kinds[id]
		if !ok {
			return false
		}
		self, _ := r.Attrs["self"].(bool)
		return self || r.Kind == "router"
	}
	seen := map[string]bool{target: true}
	frontier := []string{target}
	for depth := 0; depth <= maxCriticalDepth && len(frontier) > 0; depth++ {
		var next []string
		for _, id := range frontier {
			if critical(id) {
				return true
			}
			for _, up := range on[id] {
				if !seen[up] {
					seen[up] = true
					next = append(next, up)
				}
			}
		}
		frontier = next
	}
	return false
}

// Availability is whether the engine can plan an action now, as incidents
// carry it (integration.Action's plan_ready, unavailable and recipe).
type Availability struct {
	// Known is false for an action no attached instance offers (a link, a
	// navigation, another source's action): the incident's fields are left
	// out, and the web learns availability by planning.
	Known       bool
	PlanReady   bool
	Unavailable string
	Recipe      string
}

// Availability says whether actionID on target can be planned now. The
// rule: the instance owning the target is attached and offers the action;
// it has the control credential (else Unavailable is the manifest's
// sentence and Recipe the instance); and, for a Def without its own
// Resolve, the target is in the Home Graph and of a kind it applies to.
// Preconditions aren't run: they read the upstream, and planning checks
// them with a sentence anyway.
func (a *Actions) Availability(actionID, target string) Availability {
	a.mu.RLock()
	integ, ok := a.ids[actionID]
	a.mu.RUnlock()
	if !ok {
		return Availability{}
	}
	ai, def, ok := a.route(integ, actionID, target)
	switch {
	case !ok:
		return Availability{Known: true, Unavailable: "The integration instance this runs on isn't running right now."}
	case ai.ctl == nil:
		return Availability{Known: true, Unavailable: ai.missing, Recipe: ai.id}
	}
	if _, ok := ai.ctl[actionID]; !ok {
		return Availability{Known: true, Unavailable: fmt.Sprintf("%s's control credential doesn't offer this action.", ai.name), Recipe: ai.id}
	}
	if def.Resolve == nil {
		if _, err := action.ResolveInGraph(a.graph, &def, target); errors.Is(err, action.ErrNotFound) {
			return Availability{Known: true, Unavailable: "Cha Dash doesn't see what this acts on right now."}
		}
	}
	return Availability{Known: true, PlanReady: true}
}

// annotate fills an incident action's availability fields.
func (a *Actions) annotate(act *integration.Action) {
	act.PlanReady, act.Unavailable, act.Recipe = nil, "", ""
	if a == nil || act.ID == "" {
		return
	}
	av := a.Availability(act.ID, act.Target)
	if !av.Known {
		return
	}
	ready := av.PlanReady
	act.PlanReady, act.Unavailable, act.Recipe = &ready, av.Unavailable, av.Recipe
}
