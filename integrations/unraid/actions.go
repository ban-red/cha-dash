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
// containers and VMs start and stop (T1, each the other's undo), a container
// update (T2), and, from 4.37's published schema, restart, pause and unpause
// containers, pause, resume and reboot VMs, force-stop a VM and remove a
// container. Only a controllable instance (mode control) has them. The key
// is the instance's one key (decision 6): when it lacks a write scope, the
// affected actions refuse with 409 unavailable, naming the scope and how to
// add it. Every Docker and VM mutation needs Update, except removeContainer,
// which needs Docker: Delete; the consent link for it is a separate choice
// (docs/plan/12-unraid-control.md). Array and parity actions aren't offered:
// they need ARRAY: Update, which also lets a key add and remove disks.

// Action IDs.
const (
	ActContainerStart  = "unraid.container.start"
	ActContainerStop   = "unraid.container.stop"
	ActContainerUpdate = "unraid.container.update"
	ActVMStart         = "unraid.vm.start"
	ActVMStop          = "unraid.vm.stop"

	ActContainerRestart = "unraid.container.restart"
	ActContainerPause   = "unraid.container.pause"
	ActContainerUnpause = "unraid.container.unpause"
	ActContainerRemove  = "unraid.container.remove"
	ActVMPause          = "unraid.vm.pause"
	ActVMResume         = "unraid.vm.resume"
	ActVMReboot         = "unraid.vm.reboot"
	ActVMForceStop      = "unraid.vm.force_stop"

	// Parity checks: the array's only actions, and only when parity_control is on.
	ActParityStart  = "unraid.array.parity_start"
	ActParityPause  = "unraid.array.parity_pause"
	ActParityResume = "unraid.array.parity_resume"
	ActParityCancel = "unraid.array.parity_cancel"
)

// The mutations, one operation each. Field names are the published schema's
// (DockerMutations.start, stop, updateContainer; VmMutations.start, stop).
const (
	mContainerStart  = `mutation ChadashContainerStart($id: PrefixedID!) { docker { start(id: $id) { id state } } }`
	mContainerStop   = `mutation ChadashContainerStop($id: PrefixedID!) { docker { stop(id: $id) { id state } } }`
	mContainerUpdate = `mutation ChadashContainerUpdate($id: PrefixedID!) { docker { updateContainer(id: $id) { id state } } }`
	mVMStart         = `mutation ChadashVMStart($id: PrefixedID!) { vm { start(id: $id) } }`
	mVMStop          = `mutation ChadashVMStop($id: PrefixedID!) { vm { stop(id: $id) } }`

	mContainerRestart = `mutation ChadashContainerRestart($id: PrefixedID!) { docker { restart(id: $id) { id state } } }`
	mContainerPause   = `mutation ChadashContainerPause($id: PrefixedID!) { docker { pause(id: $id) { id state } } }`
	mContainerUnpause = `mutation ChadashContainerUnpause($id: PrefixedID!) { docker { unpause(id: $id) { id state } } }`
	// withImage is left out: the image stays, as it does when Unraid's own UI removes a container.
	mContainerRemove = `mutation ChadashContainerRemove($id: PrefixedID!) { docker { removeContainer(id: $id) } }`
	mVMPause         = `mutation ChadashVMPause($id: PrefixedID!) { vm { pause(id: $id) } }`
	mVMResume        = `mutation ChadashVMResume($id: PrefixedID!) { vm { resume(id: $id) } }`
	mVMReboot        = `mutation ChadashVMReboot($id: PrefixedID!) { vm { reboot(id: $id) } }`
	mVMForceStop     = `mutation ChadashVMForceStop($id: PrefixedID!) { vm { forceStop(id: $id) } }`

	// The parity mutations answer a JSON scalar, so they select nothing.
	mParityStart  = `mutation ChadashParityStart($correct: Boolean!) { parityCheck { start(correct: $correct) } }`
	mParityPause  = `mutation ChadashParityPause { parityCheck { pause } }`
	mParityResume = `mutation ChadashParityResume { parityCheck { resume } }`
	mParityCancel = `mutation ChadashParityCancel { parityCheck { cancel } }`
)

// mutations lists every write the integration can send (tests check them).
var mutations = []string{mContainerStart, mContainerStop, mContainerUpdate, mVMStart, mVMStop,
	mContainerRestart, mContainerPause, mContainerUnpause, mContainerRemove, mVMPause, mVMResume, mVMReboot, mVMForceStop,
	mParityStart, mParityPause, mParityResume, mParityCancel}

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
	verb     string // the action `me` lists it under: UPDATE or DELETE
	word     string // verb, as Unraid's key editor shows it
}

var (
	writeDocker = writeScope{"DOCKER", "docker:update_any", "Docker", "change Docker containers", "starting, stopping, updating and pausing containers", "UPDATE", "Update"}
	writeVMs    = writeScope{"VMS", "vms:update_any", "VMs", "start or stop VMs", "starting, stopping and pausing VMs", "UPDATE", "Update"}
	// Not part of control: only with parity_control on, and a key made with the parity consent link.
	writeArray = writeScope{"ARRAY", "array:update_any", "Array", "run parity checks", "starting, pausing, resuming and cancelling parity checks", "UPDATE", "Update"}
	// Not part of control: only a key made with the "full control" consent link has it.
	writeDockerDelete = writeScope{"DOCKER", "docker:delete_any", "Docker", "remove Docker containers", "removing containers", "DELETE", "Delete"}
	controlScopes     = []writeScope{writeDocker, writeVMs}
)

// addScopeHelp says how to give the key a missing write scope.
func addScopeHelp(s writeScope) string {
	return "Re-run the consent link in this instance's settings, or edit the key in Unraid under " + keysPage +
		" and give it " + s.label + ": " + s.word + " (" + s.scope + ")."
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
	cRestart := def(ActContainerRestart, "Restart", "container", action.T2, "Ask Unraid to restart %s", "Wait for it to report running")
	cPause := def(ActContainerPause, "Pause", "container", action.T1, "Ask Unraid to pause %s", "Wait for it to report paused")
	cPause.Undo = act.UndoBy(ActContainerUnpause, "Unpause")
	cUnpause := def(ActContainerUnpause, "Unpause", "container", action.T1, "Ask Unraid to unpause %s", "Wait for it to report running")
	cUnpause.Undo = act.UndoBy(ActContainerPause, "Pause")
	cRemove := def(ActContainerRemove, "Remove", "container", action.T3, "Ask Unraid to remove %s (its image stays)", "Wait for it to leave Unraid's list")
	vPause := def(ActVMPause, "Pause", "vm", action.T1, "Ask Unraid to pause %s", "Wait for it to report paused")
	vPause.Undo = act.UndoBy(ActVMResume, "Resume")
	vResume := def(ActVMResume, "Resume", "vm", action.T1, "Ask Unraid to resume %s", "Wait for it to report running")
	vResume.Undo = act.UndoBy(ActVMPause, "Pause")
	vReboot := def(ActVMReboot, "Reboot", "vm", action.T2, "Ask Unraid to reboot %s", "Wait for it to report running")
	vReboot.VerifyTimeout = 3 * time.Minute
	vForce := def(ActVMForceStop, "Force stop", "vm", action.T3, "Ask Unraid to cut the power to %s", "Wait for it to report stopped")
	vForce.Timeout, vForce.VerifyTimeout = 5*time.Minute, 3*time.Minute
	// Stopping, recreating, pausing or removing what runs the hub or the
	// router takes them down too: the runtime raises these to T4 then
	// (self-aware T4).
	for _, d := range []*action.Def{&cStop, &cUpdate, &vStop, &cRestart, &cPause, &cRemove, &vPause, &vReboot, &vForce} {
		d.Disrupts = true
	}
	defs := []action.Def{cStart, cStop, cUpdate, cRestart, cPause, cUnpause, cRemove, vStart, vStop, vPause, vResume, vReboot, vForce}
	if !in.parity {
		return defs
	}
	pStart := def(ActParityStart, "Start a parity check", "array", action.T2, "Ask Unraid to start a parity check on %s", "Wait for it to report running")
	pStart.Undo = act.UndoBy(ActParityCancel, "Cancel the parity check")
	pStart.Params = integration.Params(integration.Object(
		integration.Prop("correct", integration.Boolean("Correct errors it finds").Describe("Writes the correction to the parity disk. Leave it off to only check.")),
	))
	pPause := def(ActParityPause, "Pause the parity check", "array", action.T1, "Ask Unraid to pause the parity check on %s", "Wait for it to report paused")
	pPause.Undo = act.UndoBy(ActParityResume, "Resume the parity check")
	pResume := def(ActParityResume, "Resume the parity check", "array", action.T1, "Ask Unraid to resume the parity check on %s", "Wait for it to report running")
	pResume.Undo = act.UndoBy(ActParityPause, "Pause the parity check")
	pCancel := def(ActParityCancel, "Cancel the parity check", "array", action.T2, "Ask Unraid to cancel the parity check on %s", "Wait for it to stop")
	return append(defs, pStart, pPause, pResume, pCancel)
}

// resolver resolves this instance's containers or VMs with a fresh read, so
// preconditions see the state as of now, not as of the last sync.
func (in *instance) resolver(kind string) func(context.Context, string) (action.Target, error) {
	return func(ctx context.Context, target string) (action.Target, error) {
		rest, ok := strings.CutPrefix(target, in.prefix)
		if kind == "array" && ok && rest == "array" {
			rt, err := in.find(ctx, kind, "")
			if err != nil {
				return action.Target{}, err
			}
			in.actMu.Lock()
			in.resolved[target] = rt
			in.actMu.Unlock()
			return action.Target{ID: target, Kind: kind, Name: rt.name, State: act.Fingerprint(kind, rt.name, rt.state), Ref: rt.ref}, nil
		}
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
	if kind == "array" {
		var r struct {
			Array arrayData `json:"array"`
		}
		if err := in.query(ctx, "ChadashArray", qArray, &r); err != nil {
			return resolvedTarget{}, in.readRefused(err)
		}
		p, state := r.Array.Parity, "IDLE"
		switch {
		case p.Paused != nil && *p.Paused || p.Status == "PAUSED":
			state = "PAUSED"
		case p.Running != nil && *p.Running || p.Status == "RUNNING":
			state = "RUNNING"
		}
		return resolvedTarget{kind: kind, name: "the array", state: state, ref: "array"}, nil
	}
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
	if fe, ok := errors.AsType[*forbiddenError](err); ok {
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
	switch {
	case strings.HasPrefix(actionID, "unraid.vm."):
		return writeVMs
	case strings.HasPrefix(actionID, "unraid.array."):
		return writeArray
	case actionID == ActContainerRemove:
		return writeDockerDelete
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
	if known && !me.can(s.resource, s.verb) {
		return action.Unavailable("The API key can't "+s.what+": it's missing "+s.label+": "+s.word+" ("+s.scope+"). "+addScopeHelp(s), in.instanceID)
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
	case ActContainerPause:
		switch rt.state {
		case "PAUSED":
			return action.Precondition(name + " is already paused.")
		case "RUNNING":
		default:
			return action.Precondition(name + " isn't running, so there's nothing to pause.")
		}
	case ActContainerUnpause:
		if rt.state != "PAUSED" {
			return action.Precondition(name + " isn't paused.")
		}
	case ActContainerRemove:
		if rt.state == "RUNNING" || rt.state == "PAUSED" {
			return action.Precondition(name + " is " + strings.ToLower(rt.state) + ". Stop it first; Cha Dash only removes a container that has stopped.")
		}
	case ActVMPause:
		switch vmState(rt.state) {
		case "paused":
			return action.Precondition(name + " is already paused.")
		case "running":
		default:
			return action.Precondition(name + " isn't running, so there's nothing to pause.")
		}
	case ActVMResume:
		if vmState(rt.state) != "paused" {
			return action.Precondition(name + " isn't paused.")
		}
	case ActVMReboot:
		if vmState(rt.state) != "running" {
			return action.Precondition(name + " isn't running, so there's nothing to reboot.")
		}
	case ActVMForceStop:
		if vmState(rt.state) == "stopped" {
			return action.Precondition(name + " is already shut down.")
		}
	case ActParityStart:
		if rt.state != "IDLE" {
			return action.Precondition("A parity check is already " + strings.ToLower(rt.state) + ".")
		}
	case ActParityPause:
		switch rt.state {
		case "PAUSED":
			return action.Precondition("The parity check is already paused.")
		case "IDLE":
			return action.Precondition("No parity check is running.")
		}
	case ActParityResume:
		if rt.state != "PAUSED" {
			return action.Precondition("The parity check isn't paused.")
		}
	case ActParityCancel:
		if rt.state == "IDLE" {
			return action.Precondition("No parity check is running.")
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
	case ActContainerRestart:
		op, doc, verb = "ChadashContainerRestart", mContainerRestart, "restart"
	case ActContainerPause:
		op, doc, verb = "ChadashContainerPause", mContainerPause, "pause"
	case ActContainerUnpause:
		op, doc, verb = "ChadashContainerUnpause", mContainerUnpause, "unpause"
	case ActContainerRemove:
		op, doc, verb = "ChadashContainerRemove", mContainerRemove, "remove"
	case ActVMPause:
		op, doc, verb = "ChadashVMPause", mVMPause, "pause"
	case ActVMResume:
		op, doc, verb = "ChadashVMResume", mVMResume, "resume"
	case ActVMReboot:
		op, doc, verb = "ChadashVMReboot", mVMReboot, "reboot"
	case ActVMForceStop:
		op, doc, verb = "ChadashVMForceStop", mVMForceStop, "force stop"
	case ActParityStart:
		op, doc, verb = "ChadashParityStart", mParityStart, "start a parity check on"
	case ActParityPause:
		op, doc, verb = "ChadashParityPause", mParityPause, "pause the parity check on"
	case ActParityResume:
		op, doc, verb = "ChadashParityResume", mParityResume, "resume the parity check on"
	case ActParityCancel:
		op, doc, verb = "ChadashParityCancel", mParityCancel, "cancel the parity check on"
	default:
		return action.Fail("unknown_action", "Unraid has no action "+req.Action+".")
	}
	if req.Target.Ref == "" {
		return action.Fail("not_found", "Cha Dash couldn't find "+req.Target.Name+" in Unraid.")
	}
	// The parity mutations take no id; only start takes a flag.
	vars := map[string]any{"id": req.Target.Ref}
	if strings.HasPrefix(req.Action, "unraid.array.") {
		vars = nil
		if req.Action == ActParityStart {
			var p struct {
				Correct bool `json:"correct"`
			}
			if err := req.Decode(&p); err != nil {
				return action.Fail("invalid_params", "The parity check options aren't valid.")
			}
			vars = map[string]any{"correct": p.Correct}
		}
	}
	run.Step("Ask Unraid to " + verb + " " + req.Target.Name)
	var data map[string]map[string]json.RawMessage
	err := in.do(ctx, in.mutationClient(), op, doc, vars, &data)
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
		return action.Fail("missing_permission", "Unraid refused: the API key can't "+s.what+" (it needs "+s.label+": "+s.word+"). "+addScopeHelp(s))
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
	case ActVMStop, ActVMForceStop:
		want, step, words = func(s string) bool { return vmState(s) == "stopped" }, "Wait for it to report stopped", "stopped"
	case ActContainerRestart, ActContainerUnpause:
		want, step, words = func(s string) bool { return s == "RUNNING" }, "Wait for it to report running", "running"
	case ActContainerPause:
		want, step, words = func(s string) bool { return s == "PAUSED" }, "Wait for it to report paused", "paused"
	case ActContainerRemove:
		want, step, words = func(string) bool { return false }, "Wait for it to leave Unraid's list", "gone"
	case ActVMPause:
		want, step, words = func(s string) bool { return vmState(s) == "paused" }, "Wait for it to report paused", "paused"
	case ActVMResume, ActVMReboot:
		want, step, words = func(s string) bool { return vmState(s) == "running" }, "Wait for it to report running", "running"
	case ActParityStart, ActParityResume:
		want, step, words = func(s string) bool { return s == "RUNNING" }, "Wait for the parity check to report running", "running"
	case ActParityPause:
		want, step, words = func(s string) bool { return s == "PAUSED" }, "Wait for the parity check to report paused", "paused"
	case ActParityCancel:
		want, step, words = func(s string) bool { return s == "IDLE" }, "Wait for the parity check to stop", "idle"
	default:
		return nil
	}
	gone := req.Action == ActContainerRemove
	return act.Until(ctx, run, step, func(ctx context.Context) (bool, error) {
		rt, err := in.find(ctx, kind, key)
		switch {
		case errors.Is(err, action.ErrNotFound) && gone:
			return true, nil
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
