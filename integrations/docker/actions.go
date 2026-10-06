package docker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/integration"
)

// Docker actions run through chadash-node only (ADR-0006): an instance
// whose endpoint is node://<name> offers them, and every other instance
// offers none, so a socket proxy never sees a write. The node's policy has
// the last word: a protected container or a permission the policy doesn't
// grant shows as unavailable when planning, with the node's sentence.
//
//	action                      tier  undo   node verb            policy
//	docker.container.start      T1    stop   container.start      docker.lifecycle
//	docker.container.stop       T1    start  container.stop       docker.lifecycle
//	docker.container.restart    T2    —      container.restart    docker.lifecycle
//	docker.container.recreate   T2    —      container.recreate   docker.update
//	docker.container.pull       T2    —      container.pull       docker.update
//	docker.container.remove     T3    —      container.remove     docker.remove
//	docker.container.logs       T0    —      container.logs       docker.logs
//
// Blast radius is the engine's walk of the Home Graph: compose depends_on
// (published as depends_on relations), Traefik routes (routes_to) and what
// runs on or is part of the container. Remove's plan lists the volumes it
// keeps and the ones it deletes.
var actionSpecs = []struct {
	verb   string // the node verb, and the action's last segment
	title  string
	tier   action.Tier
	policy string
	async  bool
}{
	{"start", "Start", action.T1, "docker.lifecycle", false},
	{"stop", "Stop", action.T1, "docker.lifecycle", false},
	{"restart", "Restart", action.T2, "docker.lifecycle", false},
	{"recreate", "Recreate", action.T2, "docker.update", true},
	{"pull", "Pull the image of", action.T2, "docker.update", true},
	{"remove", "Remove", action.T3, "docker.remove", false},
	{"logs", "Show the logs of", action.T0, "docker.logs", false},
}

// ActionPrefix starts every Docker action ID.
const ActionPrefix = "docker.container."

// Actions implements integration.Actor. Only node:// instances have any.
func (in *instance) Actions() []action.Def {
	if in.ep.node == "" || in.d.Nodes == nil {
		return nil
	}
	defs := make([]action.Def, 0, len(actionSpecs))
	for _, s := range actionSpecs {
		d := action.Def{
			ID: ActionPrefix + s.verb, Title: s.title, Targets: []string{"container"}, Tier: s.tier, Async: s.async,
			Resolve:       in.resolve,
			Preconditions: in.preconditions(s.verb, s.policy),
			Verify:        in.verify(s.verb),
		}
		switch s.verb {
		case "start":
			d.Undo = undo("stop", "Stop")
			d.Steps = in.steps("Ask chadash-node %[2]s to start %[1]s", "Wait for %[1]s to report running")
		case "stop":
			d.Undo = undo("start", "Start")
			d.Steps = in.steps("Ask chadash-node %[2]s to stop %[1]s", "Wait for %[1]s to report stopped")
		case "restart":
			d.Steps = in.steps("Ask chadash-node %[2]s to restart %[1]s", "Wait for %[1]s to report running")
		case "recreate":
			d.Timeout = 10 * time.Minute
			d.Steps = in.steps("Stop %[1]s", "Create a new %[1]s from the same config and the image its tag points at now", "Start it, then remove the old container", "Wait for %[1]s to report running")
		case "pull":
			d.Timeout = 20 * time.Minute
			d.Steps = in.steps("Ask chadash-node %[2]s to pull the image of %[1]s", "It keeps running the old image until it's recreated")
		case "remove":
			d.Params = integration.Params(integration.Object(
				integration.Prop("volumes", integration.Boolean("Also delete its anonymous volumes").Describe("Named volumes always stay.")),
			))
			d.Steps = in.removeSteps
		case "logs":
			d.Steps = in.steps("Read the last 50 lines of %[1]s's logs through chadash-node %[2]s")
		}
		defs = append(defs, d)
	}
	return defs
}

// containerRefInfo is what Resolve stores in Target.Ref.
type containerRefInfo struct {
	ID        string   `json:"id"`
	State     string   `json:"state"`
	Named     []string `json:"named,omitempty"`     // named volumes
	Anonymous []string `json:"anonymous,omitempty"` // anonymous volumes
}

// targetRE is one of this integration's container resource IDs.
var targetRE = regexp.MustCompile(`^docker:([^/]+)/container/([a-zA-Z0-9][a-zA-Z0-9_.-]*)$`)

// resolve finds a container of this instance through the node: the target
// must be one of its resource IDs (docker:<instance>/container/<name>).
func (in *instance) resolve(ctx context.Context, target string) (action.Target, error) {
	m := targetRE.FindStringSubmatch(target)
	if m == nil || m[1] != in.key {
		return action.Target{}, action.ErrNotFound
	}
	ins, err := in.inspectNode(ctx, m[2])
	if integration.NodeErrorCode(err) == "not_found" {
		return action.Target{}, action.ErrNotFound
	}
	if err != nil {
		return action.Target{}, action.Unavailable(in.unreachable(err), in.key)
	}
	ref := containerRefInfo{ID: ins.ID, State: ins.State.Status}
	for _, mp := range ins.Mounts {
		if mp.Type != "volume" || mp.Name == "" {
			continue
		}
		if anonymousVolume.MatchString(mp.Name) {
			ref.Anonymous = append(ref.Anonymous, mp.Name)
		} else {
			ref.Named = append(ref.Named, mp.Name)
		}
	}
	b, _ := json.Marshal(ref)
	sum := sha256.Sum256([]byte(ins.ID + "\x00" + ins.State.Status + "\x00" + ins.Image))
	return action.Target{ID: target, Kind: "container", Name: m[2], State: hex.EncodeToString(sum[:8]), Ref: string(b)}, nil
}

// anonymousVolume is how Docker names a volume nobody named.
var anonymousVolume = regexp.MustCompile(`^[0-9a-f]{64}$`)

// nodeInspect is the part of an inspect actions read.
type nodeInspect struct {
	ID    string `json:"Id"`
	Image string `json:"Image"`
	State struct {
		Status  string `json:"Status"`
		Running bool   `json:"Running"`
	} `json:"State"`
	Mounts []struct {
		Type string `json:"Type"`
		Name string `json:"Name"`
	} `json:"Mounts"`
}

func (in *instance) inspectNode(ctx context.Context, id string) (nodeInspect, error) {
	var ins nodeInspect
	res, err := in.d.Nodes.Call(ctx, in.ep.node, "container.inspect", map[string]string{"id": id}, nil)
	if err != nil {
		return ins, err
	}
	err = json.Unmarshal(res, &ins)
	return ins, err
}

func refOf(req action.Request) containerRefInfo {
	var r containerRefInfo
	_ = json.Unmarshal([]byte(req.Target.Ref), &r)
	return r
}

func (in *instance) unreachable(err error) string {
	if integration.NodeErrorCode(err) == "not_connected" {
		return fmt.Sprintf("chadash-node %s isn't connected to the hub, so Cha Dash can't act on this host right now.", in.ep.node)
	}
	return fmt.Sprintf("chadash-node %s couldn't be asked: %v", in.ep.node, err)
}

// preconditions refuse early: the state must fit the verb, and the node
// must say its policy allows it on this container.
func (in *instance) preconditions(verb, policy string) func(context.Context, action.Request) error {
	return func(ctx context.Context, req action.Request) error {
		ref := refOf(req)
		name := req.Target.Name
		switch {
		case verb == "start" && ref.State == "running":
			return action.Precondition(name + " is already running.")
		case verb == "stop" && ref.State != "running" && ref.State != "restarting" && ref.State != "paused":
			return action.Precondition(name + " isn't running.")
		case verb == "remove" && ref.State == "running":
			return action.Precondition(name + " is running. Stop it first.")
		}
		res, err := in.d.Nodes.Call(ctx, in.ep.node, "container.can", map[string]string{"id": ref.ID, "permission": policy}, nil)
		if err != nil {
			return action.Unavailable(in.unreachable(err), in.key)
		}
		var can struct {
			Allowed bool   `json:"allowed"`
			Reason  string `json:"reason"`
		}
		if json.Unmarshal(res, &can) != nil || !can.Allowed {
			return action.Unavailable(sentence(can.Reason), in.key)
		}
		return nil
	}
}

func sentence(s string) string {
	if s == "" {
		return "chadash-node refuses this."
	}
	s = strings.ToUpper(s[:1]) + s[1:]
	if !strings.HasSuffix(s, ".") {
		s += "."
	}
	return s
}

func undo(verb, title string) func(action.Request) *action.Undo {
	return func(req action.Request) *action.Undo {
		return &action.Undo{Action: ActionPrefix + verb, Params: json.RawMessage("{}"), Label: title + " " + req.Target.Name}
	}
}

// steps fills each label with the container's name (%[1]s) and the
// node's (%[2]s).
func (in *instance) steps(labels ...string) func(action.Request) []action.Step {
	return func(req action.Request) []action.Step {
		out := make([]action.Step, len(labels))
		for i, l := range labels {
			out[i] = action.Step{Label: fmt.Sprintf(l, req.Target.Name, in.ep.node)}
		}
		return out
	}
}

// removeSteps lists what a remove keeps and what it deletes.
func (in *instance) removeSteps(req action.Request) []action.Step {
	var p struct {
		Volumes bool `json:"volumes"`
	}
	_ = req.Decode(&p)
	ref := refOf(req)
	out := []action.Step{{Label: "Remove the container " + req.Target.Name + ". This can't be undone."}}
	for _, v := range ref.Named {
		out = append(out, action.Step{Label: "Keep the volume " + v})
	}
	for _, v := range ref.Anonymous {
		if p.Volumes {
			out = append(out, action.Step{Label: "Delete the anonymous volume " + v[:12] + " and its data"})
		} else {
			out = append(out, action.Step{Label: "Keep the anonymous volume " + v[:12] + " (left unattached)"})
		}
	}
	return out
}

// Execute implements integration.Actor: it calls the node verb named by
// the action and reports the node's stream on the task.
func (in *instance) Execute(ctx context.Context, req action.Request, run *action.TaskRun) error {
	if in.ep.node == "" || in.d.Nodes == nil {
		return action.Fail("unavailable", "Docker actions run through chadash-node, and this instance reads elsewhere.")
	}
	verb, ok := strings.CutPrefix(req.Action, ActionPrefix)
	if !ok {
		return action.Fail("unknown_action", "Not a Docker action.")
	}
	ref := refOf(req)
	params := map[string]any{"id": ref.ID}
	progress := func(json.RawMessage) {}
	switch verb {
	case "remove":
		var p struct {
			Volumes bool `json:"volumes"`
		}
		_ = req.Decode(&p)
		params["volumes"] = p.Volumes
	case "logs":
		params["tail"] = "50"
		progress = func(b json.RawMessage) {
			var l struct{ Stream, Line string }
			if json.Unmarshal(b, &l) == nil {
				run.Logf("%s", l.Line)
			}
		}
	case "pull":
		layers := map[string]bool{}
		progress = func(b json.RawMessage) {
			var p struct {
				Status, ID     string
				Current, Total int64
			}
			if json.Unmarshal(b, &p) != nil {
				return
			}
			if p.Total == 0 && !layers[p.Status+p.ID] {
				layers[p.Status+p.ID] = true
				run.Logf("%s %s", p.Status, p.ID)
			}
		}
	case "recreate":
		progress = func(b json.RawMessage) {
			var s struct{ Step string }
			if json.Unmarshal(b, &s) == nil && s.Step != "" {
				run.Step(s.Step)
				run.Logf("%s", s.Step)
			}
		}
	}
	res, err := in.d.Nodes.Call(ctx, in.ep.node, "container."+verb, params, progress)
	if err != nil {
		return nodeFailure(in.ep.node, err)
	}
	switch verb {
	case "pull":
		var r struct {
			Image   string `json:"image"`
			Changed bool   `json:"changed"`
		}
		_ = json.Unmarshal(res, &r)
		if r.Changed {
			run.Logf("Pulled a newer %s. Recreate %s to run it.", r.Image, req.Target.Name)
		} else {
			run.Logf("%s is already up to date.", r.Image)
		}
	case "remove":
		var r struct{ Kept, Removed []string }
		_ = json.Unmarshal(res, &r)
		run.Logf("Removed %s; kept %d volume(s), deleted %d.", req.Target.Name, len(r.Kept), len(r.Removed))
	}
	return nil
}

// nodeFailure turns a node's refusal into the task's error.
func nodeFailure(node string, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	switch code := integration.NodeErrorCode(err); code {
	case "not_connected":
		return action.Fail("node_unavailable", fmt.Sprintf("chadash-node %s isn't connected to the hub.", node))
	case "":
		return action.Fail("failed", err.Error())
	default:
		return action.Fail(code, sentence(err.Error()))
	}
}

// verify reads the container back through the node until it's in the
// state the verb promised.
func (in *instance) verify(verb string) func(context.Context, action.Request, *action.TaskRun) error {
	want := map[string]string{"start": "running", "restart": "running", "recreate": "running", "stop": "exited"}[verb]
	if verb == "remove" {
		return func(ctx context.Context, req action.Request, _ *action.TaskRun) error {
			_, err := in.inspectNode(ctx, refOf(req).ID)
			if integration.NodeErrorCode(err) == "not_found" {
				return nil
			}
			if err == nil {
				return action.Fail("verify_failed", req.Target.Name+" is still there.")
			}
			return nodeFailure(in.ep.node, err)
		}
	}
	if want == "" {
		return nil
	}
	return func(ctx context.Context, req action.Request, run *action.TaskRun) error {
		run.Step("Wait for " + req.Target.Name + " to report " + map[string]string{"running": "running", "exited": "stopped"}[want])
		for {
			// A recreate made a new container: look it up by name.
			ins, err := in.inspectNode(ctx, req.Target.Name)
			switch {
			case err == nil && ins.State.Status == want:
				return nil
			case err == nil && want == "exited" && ins.State.Status == "created":
				return nil
			case err != nil && ctx.Err() == nil && integration.NodeErrorCode(err) != "not_connected":
				return nodeFailure(in.ep.node, err)
			}
			select {
			case <-ctx.Done():
				state := "unknown"
				if err == nil {
					state = ins.State.Status
				}
				return action.Fail("verify_failed", fmt.Sprintf("%s reports %s, not %s.", req.Target.Name, state, want))
			case <-time.After(time.Second):
			}
		}
	}
}

var _ integration.Actor = (*instance)(nil)
