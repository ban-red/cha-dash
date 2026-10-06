package unraid

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"dash.cha.sh/integrations/internal/act"
	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/integration"
)

// Actions (v0.2), through the Unraid API's mutations and never the node:
// containers and VMs start and stop (T1, each the other's undo), and a
// container update (T2). Only a controllable instance (mode control) has
// them. The key is the instance's one key (decision 6): when it lacks a
// write scope, the affected actions refuse with 409 unavailable, naming the
// scope and how to add it. Array and parity actions aren't offered: they
// need ARRAY: Update, which also lets a key add and remove disks.

// Action IDs.
const (
	ActContainerStart  = "unraid.container.start"
	ActContainerStop   = "unraid.container.stop"
	ActContainerUpdate = "unraid.container.update"
	ActVMStart         = "unraid.vm.start"
	ActVMStop          = "unraid.vm.stop"
)

// The mutations, one operation each. Field names are the published schema's
// (DockerMutations.start, stop, updateContainer; VmMutations.start, stop).
const (
	mContainerStart  = `mutation ChadashContainerStart($id: PrefixedID!) { docker { start(id: $id) { id state } } }`
	mContainerStop   = `mutation ChadashContainerStop($id: PrefixedID!) { docker { stop(id: $id) { id state } } }`
	mContainerUpdate = `mutation ChadashContainerUpdate($id: PrefixedID!) { docker { updateContainer(id: $id) { id state } } }`
	mVMStart         = `mutation ChadashVMStart($id: PrefixedID!) { vm { start(id: $id) } }`
	mVMStop          = `mutation ChadashVMStop($id: PrefixedID!) { vm { stop(id: $id) } }`
)

// mutations lists every write the integration can send (tests check them).
var mutations = []string{mContainerStart, mContainerStop, mContainerUpdate, mVMStart, mVMStop}

// mutationTimeout bounds one mutation: Unraid answers a stop or an update
// when it's done, which takes longer than a read's 15 s.
const mutationTimeout = 15 * time.Minute

// grantsFresh is how long the key's grants, read with `me`, are trusted.
const grantsFresh = 30 * time.Second

// writeScope is a write scope a controllable instance needs.
type writeScope struct {
	resource string // as `me` names it
	scope    string // as the consent URL asks for it
	label    string // as Unraid's key editor shows it
	what     string // what it lets the key do
	actions  string // the actions that need it
}

var (
	writeDocker   = writeScope{"DOCKER", "docker:update_any", "Docker", "change Docker containers", "starting, stopping and updating containers"}
	writeVMs      = writeScope{"VMS", "vms:update_any", "VMs", "start or stop VMs", "starting and stopping VMs"}
	controlScopes = []writeScope{writeDocker, writeVMs}
)

// addScopeHelp says how to give the key a missing write scope.
func addScopeHelp(s writeScope) string {
	return "Re-run the consent link in this instance's settings with Control chosen, or edit the key in Unraid under " + keysPage +
		" and give it " + s.label + ": Update (" + s.scope + ")."
}

// resolvedTarget is a container or VM as an action last read it.
type resolvedTarget struct {
	kind    string // container, vm
	name    string
	state   string // Unraid's: RUNNING, EXITED, PAUSED; or a VM's libvirt state
	manager string // a container's: dockerman, composeman
	ref     string // the PrefixedID the mutations take
}

// Actions implements integration.Actor: none on a read-only instance.
func (in *instance) Actions() []action.Def {
	if !in.control {
		return nil
	}
	def := func(id, title, kind string, tier action.Tier, steps ...string) action.Def {
		return action.Def{
			ID: id, Title: title, Targets: []string{kind}, Tier: tier,
			Resolve:       in.resolver(kind),
			Preconditions: in.preconditions,
			Steps: func(r action.Request) []action.Step {
				out := make([]string, len(steps))
				for i, s := range steps {
					out[i] = strings.ReplaceAll(s, "%s", r.Target.Name)
				}
				return act.Steps(out...)
			},
			Verify: in.verify,
		}
	}
	cStart := def(ActContainerStart, "Start", "container", action.T1, "Ask Unraid to start %s", "Wait for it to report running")
	cStart.Undo = act.UndoBy(ActContainerStop, "Stop")
	cStop := def(ActContainerStop, "Stop", "container", action.T1, "Ask Unraid to stop %s", "Wait for it to report stopped")
	cStop.Undo = act.UndoBy(ActContainerStart, "Start")
	cUpdate := def(ActContainerUpdate, "Update", "container", action.T2, "Ask Unraid to pull %s's image and recreate it", "Wait for it to come back")
	cUpdate.Timeout, cUpdate.VerifyTimeout = mutationTimeout, 3*time.Minute
	vStart := def(ActVMStart, "Start", "vm", action.T1, "Ask Unraid to start %s", "Wait for it to report running")
	vStart.Undo = act.UndoBy(ActVMStop, "Shut down")
	vStart.VerifyTimeout = 2 * time.Minute
	vStop := def(ActVMStop, "Shut down", "vm", action.T1, "Ask Unraid to shut %s down", "Wait for it to report stopped")
	vStop.Undo = act.UndoBy(ActVMStart, "Start")
	vStop.Timeout, vStop.VerifyTimeout = 5*time.Minute, 3*time.Minute
	// Stopping or recreating what runs the hub or the router takes them
	// down too: the runtime raises these to T4 then (self-aware T4).
	cStop.Disrupts, cUpdate.Disrupts, vStop.Disrupts = true, true, true
	return []action.Def{cStart, cStop, cUpdate, vStart, vStop}
}

// resolver resolves this instance's containers or VMs with a fresh read, so
// preconditions see the state as of now, not as of the last sync.
func (in *instance) resolver(kind string) func(context.Context, string) (action.Target, error) {
	return func(ctx context.Context, target string) (action.Target, error) {
		rest, ok := strings.CutPrefix(target, in.prefix)
		k, key, _ := strings.Cut(rest, "/")
		if !ok || k != kind || key == "" {
			return action.Target{}, action.ErrNotFound
		}
		rt, err := in.find(ctx, kind, key)
		if err != nil {
			return action.Target{}, err
		}
		in.actMu.Lock()
		in.resolved[target] = rt
		in.actMu.Unlock()
		return action.Target{ID: target, Kind: kind, Name: rt.name, State: act.Fingerprint(kind, rt.name, rt.state), Ref: rt.ref}, nil
	}
}

// find reads one container (by name) or VM (by UUID).
func (in *instance) find(ctx context.Context, kind, key string) (resolvedTarget, error) {
	if kind == "container" {
		var r struct {
			Docker struct {
				Containers []container `json:"containers"`
			} `json:"docker"`
		}
		if err := in.query(ctx, "ChadashDocker", qDocker, &r); err != nil {
			return resolvedTarget{}, in.readRefused(err)
		}
		for _, c := range r.Docker.Containers {
			if len(c.Names) > 0 && strings.TrimPrefix(c.Names[0], "/") == key {
				m, _ := c.Labels[managedLabel].(string)
				return resolvedTarget{kind: kind, name: key, state: strings.ToUpper(c.State), manager: m, ref: c.ID}, nil
			}
		}
		return resolvedTarget{}, action.ErrNotFound
	}
	var r struct {
		VMs struct {
			Domains []vmDomain `json:"domains"`
		} `json:"vms"`
	}
	if err := in.query(ctx, "ChadashVMs", qVMs, &r); err != nil {
		return resolvedTarget{}, in.readRefused(err)
	}
	for _, d := range r.VMs.Domains {
		if localID(d.ID) == key {
			name := d.Name
			if name == "" {
				name = key
			}
			return resolvedTarget{kind: kind, name: name, state: strings.ToUpper(d.State), ref: d.ID}, nil
		}
	}
	return resolvedTarget{}, action.ErrNotFound
}

// readRefused turns a refused read into an unavailable action that says
// which read scope to add.
func (in *instance) readRefused(err error) error {
	var fe *forbiddenError
	if errors.As(err, &fe) {
		r := reads[fe.op]
		return action.Unavailable(fmt.Sprintf("The API key can't read %s (it needs %s: Read), so Cha Dash can't act on them. Give it %s: Read in Unraid under %s, or re-run the consent link in this instance's settings.",
			r.what, r.resource, r.resource, keysPage), in.instanceID)
	}
	return err
}

func (in *instance) lookup(target string) resolvedTarget {
	in.actMu.Lock()
	defer in.actMu.Unlock()
	return in.resolved[target]
}

// grants returns the key's grants, read with `me` at most every
// grantsFresh. known is false when the key can't read ME: then the
// mutation's own answer says whether a scope is missing.
func (in *instance) grants(ctx context.Context) (me meData, known bool, err error) {
	now := in.d.Now()
	in.actMu.Lock()
	if !in.meAt.IsZero() && now.Sub(in.meAt) < grantsFresh {
		defer in.actMu.Unlock()
		if in.me == nil {
			return meData{}, false, nil
		}
		return *in.me, true, nil
	}
	in.actMu.Unlock()
	var r struct {
		Me meData `json:"me"`
	}
	err = in.query(ctx, "ChadashMe", qMe, &r)
	var fe *forbiddenError
	switch {
	case errors.As(err, &fe):
		in.actMu.Lock()
		in.me, in.meAt = nil, now
		in.actMu.Unlock()
		return meData{}, false, nil
	case err != nil:
		return meData{}, false, err
	}
	in.actMu.Lock()
	in.me, in.meAt = &r.Me, now
	in.actMu.Unlock()
	return r.Me, true, nil
}

// scopeFor is the write scope an action needs.
func scopeFor(actionID string) writeScope {
	if strings.HasPrefix(actionID, "unraid.vm.") {
		return writeVMs
	}
	return writeDocker
}

// preconditions refuse an action the key can't do (unavailable, naming the
// scope) or that the target's state makes pointless (a sentence).
func (in *instance) preconditions(ctx context.Context, req action.Request) error {
	s := scopeFor(req.Action)
	me, known, err := in.grants(ctx)
	if err != nil {
		return err
	}
	if known && !me.can(s.resource, "UPDATE") {
		return action.Unavailable("The API key can't "+s.what+": it's missing "+s.label+": Update ("+s.scope+"). "+addScopeHelp(s), in.instanceID)
	}
	rt := in.lookup(req.Target.ID)
	name := req.Target.Name
	switch req.Action {
	case ActContainerStart:
		switch rt.state {
		case "RUNNING":
			return action.Precondition(name + " is already running.")
		case "PAUSED":
			return action.Precondition(name + " is paused. Resume it in Unraid; starting doesn't.")
		}
	case ActContainerStop:
		if rt.state == "EXITED" {
			return action.Precondition(name + " is already stopped.")
		}
	case ActContainerUpdate:
		if rt.manager == "composeman" {
			return action.Precondition(name + " belongs to a Compose Manager stack, which Unraid's container update doesn't cover. Update the stack instead.")
		}
	case ActVMStart:
		switch vmState(rt.state) {
		case "running":
			return action.Precondition(name + " is already running.")
		case "paused":
			return action.Precondition(name + " is paused. Resume it in Unraid; starting doesn't.")
		case "stopping":
			return action.Precondition(name + " is shutting down. Start it once it has stopped.")
		}
	case ActVMStop:
		switch vmState(rt.state) {
		case "stopped":
			return action.Precondition(name + " is already shut down.")
		case "stopping":
			return action.Precondition(name + " is already shutting down.")
		}
	}
	return nil
}

// mutationClient is the HTTP client for mutations: the instance's TLS
// policy, with a timeout long enough for a stop or an update.
func (in *instance) mutationClient() *http.Client {
	in.actMu.Lock()
	defer in.actMu.Unlock()
	if in.longHTTP == nil {
		in.longHTTP = in.d.HTTP
		if in.d.NewHTTPClient != nil {
			if c, err := in.d.NewHTTPClient(integration.HTTPOptions{Timeout: mutationTimeout}); err == nil {
				in.longHTTP = c
			}
		}
	}
	return in.longHTTP
}

// Execute implements integration.Actor: one mutation. The result is
// checked by Verify, through the next read.
func (in *instance) Execute(ctx context.Context, req action.Request, run *action.TaskRun) error {
	var op, doc, verb string
	switch req.Action {
	case ActContainerStart:
		op, doc, verb = "ChadashContainerStart", mContainerStart, "start"
	case ActContainerStop:
		op, doc, verb = "ChadashContainerStop", mContainerStop, "stop"
	case ActContainerUpdate:
		op, doc, verb = "ChadashContainerUpdate", mContainerUpdate, "update"
	case ActVMStart:
		op, doc, verb = "ChadashVMStart", mVMStart, "start"
	case ActVMStop:
		op, doc, verb = "ChadashVMStop", mVMStop, "shut down"
	default:
		return action.Fail("unknown_action", "Unraid has no action "+req.Action+".")
	}
	if req.Target.Ref == "" {
		return action.Fail("not_found", "Cha Dash couldn't find "+req.Target.Name+" in Unraid.")
	}
	run.Step("Ask Unraid to " + verb + " " + req.Target.Name)
	var data map[string]map[string]json.RawMessage
	err := in.do(ctx, in.mutationClient(), op, doc, map[string]any{"id": req.Target.Ref}, &data)
	if err != nil {
		return in.mutationFailed(req, verb, err)
	}
	for _, fields := range data {
		for _, v := range fields {
			if string(v) == "false" {
				return action.Fail("refused", "Unraid didn't "+verb+" "+req.Target.Name+".")
			}
		}
	}
	run.Logf("Unraid accepted: %s %s", verb, req.Target.Name)
	return nil
}

// mutationFailed turns a failed mutation into a sentence.
func (in *instance) mutationFailed(req action.Request, verb string, err error) error {
	var fe *forbiddenError
	var ae *integration.AuthError
	var re *integration.RepairError
	switch {
	case errors.As(err, &fe):
		s := scopeFor(req.Action)
		in.actMu.Lock()
		in.meAt = time.Time{} // read the grants again next time
		in.actMu.Unlock()
		return action.Fail("missing_permission", "Unraid refused: the API key can't "+s.what+" (it needs "+s.label+": Update). "+addScopeHelp(s))
	case errors.As(err, &ae):
		return action.Fail("auth", "Unraid rejected the API key.")
	case errors.As(err, &re) && re.Code == integration.CodeAPIChanged:
		v := "This Unraid API"
		if api := in.apiVersion(); api != "" {
			v = "The Unraid API (" + shortVersion(api) + ")"
		}
		return action.Fail("unsupported", v+" doesn't accept the request to "+verb+" "+req.Target.Name+". A newer Unraid release may.")
	}
	return err
}

// verify waits for the next read to show the result: running after a
// start, stopped after a stop, and, after an update, the container back
// (running again if it was running).
func (in *instance) verify(ctx context.Context, req action.Request, run *action.TaskRun) error {
	_, key, _ := strings.Cut(strings.TrimPrefix(req.Target.ID, in.prefix), "/")
	kind, name := req.Target.Kind, req.Target.Name
	var want func(state string) bool
	var step, words string
	switch req.Action {
	case ActContainerStart:
		want, step, words = func(s string) bool { return s == "RUNNING" }, "Wait for it to report running", "running"
	case ActContainerStop:
		want, step, words = func(s string) bool { return s == "EXITED" }, "Wait for it to report stopped", "stopped"
	case ActContainerUpdate:
		wasRunning := req.Target.State == act.Fingerprint(kind, name, "RUNNING")
		want, step, words = func(s string) bool { return !wasRunning || s == "RUNNING" }, "Wait for it to come back", "running"
	case ActVMStart:
		want, step, words = func(s string) bool { return vmState(s) == "running" }, "Wait for it to report running", "running"
	case ActVMStop:
		want, step, words = func(s string) bool { return vmState(s) == "stopped" }, "Wait for it to report stopped", "stopped"
	default:
		return nil
	}
	return act.Until(ctx, run, step, func(ctx context.Context) (bool, error) {
		rt, err := in.find(ctx, kind, key)
		switch {
		case errors.Is(err, action.ErrNotFound):
			return false, fmt.Errorf("%s isn't listed by Unraid any more", name)
		case err != nil:
			return false, err
		case want(rt.state):
			return true, nil
		}
		return false, fmt.Errorf("%s reports %s, not %s", name, strings.ToLower(rt.state), words)
	})
}
