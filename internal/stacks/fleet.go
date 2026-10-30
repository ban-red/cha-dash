package stacks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"

	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/nodeproto"
	"dash.cha.sh/internal/stackproto"
)

// Fleet stacks (v0.3, wave 14): bringing a running project into a node's
// stacks root, and Cha Dash updating itself on the hub's host.
//
//	stack.move      T1  {"from_path"}: the project's file (and .env) into <root>/<name>; undo un-adopts
//	chadash.update  T3  {"image"}: the hub's container on node:<id> recreated with that tag, pinned; resumes
//
// chadash.update outlives the hub: recreating the hub's container stops
// the hub that runs the task. Its Def resumes (action.Def.Resumes), so at
// the next start the engine hands the task to Resume, which asks the node
// for stack.selfupdate.result and ends the task with it.

// FleetSource names the fleet actions in the engine's registry. It's a
// source of its own: registering ActionSource again would replace the
// stack actions.
const FleetSource = "stacks.fleet"

// Fleet action IDs.
const (
	ActionMove       = "stack.move"
	ActionSelfUpdate = "chadash.update"
)

// NodeTargetPrefix starts chadash.update's target: node:<id>, the node's
// resource in the Home Graph (nodes.ResourceID).
const NodeTargetPrefix = "node:"

// selfUpdateTimeout bounds chadash.update's task, and its Resume: a pull,
// two 120 s gates and two recreates fit with room to spare.
const selfUpdateTimeout = 15 * time.Minute

// resumePoll is how often Resume asks the node again while the update
// runs, or while the node is away.
var resumePoll = 3 * time.Second

// FleetActions are stack.move and chadash.update, for the engine
// (Registry.Register with ActionSource).
func (s *Service) FleetActions() []action.Def {
	return []action.Def{
		{
			ID: ActionMove, Title: "Move", Targets: []string{"stack"}, Tier: action.T1,
			Params:        moveSchema{},
			Resolve:       s.resolveMove,
			Preconditions: s.movePreconditions,
			Label:         func(r action.Request) string { return "Move " + r.Target.Name + " into the stacks root" },
			Steps: func(r action.Request) []action.Step {
				return []action.Step{
					{Label: "Copy " + fromPathOf(r) + " and the .env beside it to the stacks root, making relative paths absolute"},
					{Label: "Commit it as " + r.Target.Name + "'s first version in the root; the old file stays where it is"},
					{Label: "Nothing restarts: the next deploy recreates what the review showed"},
				}
			},
			BlastRadius: func(context.Context, action.Request) []action.Impact { return []action.Impact{} },
			Undo: func(r action.Request) *action.Undo {
				return &action.Undo{Action: ActionUnadopt, Params: json.RawMessage(`{}`), Label: "Un-adopt " + r.Target.Name + " (both copies stay)"}
			},
			Execute: s.executeMove,
		},
		{
			ID: ActionSelfUpdate, Title: "Update Cha Dash", Targets: []string{"node"}, Tier: action.T3, Async: true, Timeout: selfUpdateTimeout,
			Params:        imageSchema{},
			Resolve:       s.resolveSelfUpdate,
			Preconditions: s.selfUpdatePreconditions,
			Label:         func(r action.Request) string { return "Update Cha Dash to " + imageOf(r) },
			Steps: func(r action.Request) []action.Step {
				return []action.Step{
					{Label: "Pull " + selfUpdateImage(imageOf(r)) + " on " + refNode(r) + " and pin the digest it points at"},
					{Label: "Recreate the hub's container with it; the node and every other container stay as they are"},
					{Label: "Cha Dash is away for a moment while it restarts; this page reconnects on its own"},
					{Label: "Hold it to the hub's healthcheck for up to 2 minutes"},
					{Label: "If the check fails, the node puts the previous image back on its own"},
				}
			},
			BlastRadius: s.selfUpdateImpact,
			Execute:     s.executeSelfUpdate,
			Resumes:     true,
			Resume:      s.resumeSelfUpdate,
		},
	}
}

// ── stack.move ───────────────────────────────────────────────────────────

// moveRef is what resolveMove learned about the stack, in Target.Ref.
type moveRef struct {
	Node      string `json:"node"`
	Stack     string `json:"stack"`
	Managed   bool   `json:"managed"`
	Location  string `json:"location"`
	Path      string `json:"path"`
	Protected string `json:"protected,omitempty"`
}

func (s *Service) resolveMove(ctx context.Context, target string) (action.Target, error) {
	node, name, ok := parseTarget(target)
	if !ok {
		return action.Target{}, action.ErrNotFound
	}
	var st stackproto.Stack
	err := s.call(ctx, node, stackproto.VerbGet, stackproto.GetRequest{Stack: name}, &st, s.timeout, nil)
	var off errNodeOffline
	switch e := stackproto.ErrorFrom(err); {
	case errors.As(err, &off):
		return action.Target{}, action.Unavailable(off.Error(), "")
	case errors.As(err, new(errNodeUnknown)), e != nil && e.Code == stackproto.CodeNotFound:
		return action.Target{}, action.ErrNotFound
	case err != nil:
		return action.Target{}, action.Unavailable(firstSentence(err), "")
	}
	ref := moveRef{Node: node, Stack: name, Managed: st.Managed, Location: st.Location, Path: st.Path, Protected: st.ProtectedReason}
	head := ""
	if st.Head != nil {
		head = st.Head.Commit
	}
	raw, _ := json.Marshal(ref)
	sum := sha256.Sum256([]byte(strings.Join([]string{head, fmt.Sprint(st.Managed), st.Location, st.Path}, "\x00")))
	return action.Target{ID: target, Kind: "stack", Name: name, State: hex.EncodeToString(sum[:8]), Ref: string(raw)}, nil
}

func moveRefOf(r action.Request) moveRef {
	var ref moveRef
	_ = json.Unmarshal([]byte(r.Target.Ref), &ref)
	return ref
}

func (s *Service) movePreconditions(_ context.Context, r action.Request) error {
	ref := moveRefOf(r)
	switch {
	case ref.Protected != "":
		return action.Unavailable(ref.Protected, "")
	case ref.Managed && ref.Location == stackproto.LocationRoot:
		return action.Precondition(fmt.Sprintf("%s is already in the stacks root.", ref.Stack))
	}
	return nil
}

func (s *Service) executeMove(ctx context.Context, r action.Request, run *action.TaskRun) error {
	ref := moveRefOf(r)
	run.Step("Copy the file into the stacks root")
	var res stackproto.MoveResult
	err := s.call(ctx, ref.Node, stackproto.VerbMove, stackproto.MoveRequest{Stack: ref.Stack, FromPath: fromPathOf(r), By: r.Actor.Name, Via: r.Actor.Via}, &res, s.timeout, nil)
	if err != nil {
		return nodeFail(err)
	}
	for _, rw := range res.Rewrites {
		run.Logf("Line %d: %s → %s", rw.Line, rw.From, rw.To)
	}
	run.Logf("Commit %s at %s", short(res.Commit), res.Path)
	return nil
}

func fromPathOf(r action.Request) string {
	var p struct {
		FromPath string `json:"from_path"`
	}
	_ = r.Decode(&p)
	return p.FromPath
}

// moveSchema is stack.move's params: {"from_path": "<absolute path>"}, the
// compose file (or its directory) the node reads. Content an importer
// supplies doesn't go through the plan: a compose file can hold
// credentials, and plans and audit records keep their params.
type moveSchema struct{}

func (moveSchema) MarshalJSON() ([]byte, error) {
	return []byte(`{"type":"object","properties":{"from_path":{"type":"string","title":"From","description":"The compose file the project runs from, on the node"}},"required":["from_path"],"additionalProperties":false}`), nil
}

func (moveSchema) Validate(params map[string]any) []action.FieldError {
	for k := range params {
		if k != "from_path" {
			return []action.FieldError{{Path: "/" + k, Message: "stack.move takes only from_path."}}
		}
	}
	p, ok := params["from_path"].(string)
	if !ok || !strings.HasPrefix(p, "/") || len(p) > 4096 || path.Clean(p) != p {
		return []action.FieldError{{Path: "/from_path", Message: "from_path must be an absolute, clean path on the node."}}
	}
	return nil
}

func (moveSchema) Secrets() []string { return nil }

// ── chadash.update ───────────────────────────────────────────────────────

// selfRef is what resolveSelfUpdate learned, in Target.Ref.
type selfRef struct {
	Node string `json:"node"`
	Name string `json:"name"`
}

func (s *Service) resolveSelfUpdate(ctx context.Context, target string) (action.Target, error) {
	id, ok := strings.CutPrefix(target, NodeTargetPrefix)
	if !ok || !nodeIDRE.MatchString(id) {
		return action.Target{}, action.ErrNotFound
	}
	n, err := s.node(ctx, id)
	var off errNodeOffline
	switch {
	case errors.As(err, &off):
		return action.Target{}, action.Unavailable(off.Error(), "")
	case errors.As(err, new(errNodeUnknown)):
		return action.Target{}, action.ErrNotFound
	case err != nil:
		return action.Target{}, err
	}
	raw, _ := json.Marshal(selfRef{Node: id, Name: n.Name})
	// Typed to confirm: the T3 ladder asks for "Cha Dash".
	return action.Target{ID: target, Kind: "node", Name: "Cha Dash", State: id, Ref: string(raw)}, nil
}

func selfRefOf(r action.Request) selfRef {
	var ref selfRef
	_ = json.Unmarshal([]byte(r.Target.Ref), &ref)
	return ref
}

func refNode(r action.Request) string {
	ref := selfRefOf(r)
	if ref.Name != "" {
		return ref.Name
	}
	return ref.Node
}

// selfUpdateOff says why a node whose policy lacks stacks.selfupdate refuses.
func selfUpdateOff(node string) string {
	return fmt.Sprintf("The node %s doesn't allow Cha Dash to update itself: its policy doesn't grant stacks.selfupdate. The compose file Cha Dash ships turns it on for the node beside the hub (CHADASH_NODE_SELFUPDATE).", node)
}

// selfUpdateStatus asks the node whether it may update the hub, and how the
// last update ended. allowed is false when the policy refuses the verb.
func (s *Service) selfUpdateStatus(ctx context.Context, node string) (res stackproto.SelfUpdateResult, allowed bool, err error) {
	err = s.call(ctx, node, stackproto.VerbSelfUpdateResult, stackproto.SelfUpdateResultRequest{}, &res, s.timeout, nil)
	if err != nil && nodeproto.CodeOf(err) == nodeproto.CodeForbidden {
		// The node runtime refused the verb before it ran: its policy.
		return res, false, nil
	}
	if err != nil {
		return res, false, err
	}
	return res, true, nil
}

func (s *Service) selfUpdatePreconditions(ctx context.Context, r action.Request) error {
	ref := selfRefOf(r)
	res, allowed, err := s.selfUpdateStatus(ctx, ref.Node)
	switch {
	case err != nil:
		return action.Unavailable(firstSentence(err), "")
	case !allowed:
		return action.Unavailable(selfUpdateOff(refNode(r)), "")
	case !res.Available:
		return action.Unavailable(res.Unavailable, "")
	case res.Outcome == stackproto.OutcomeRunning:
		return action.Precondition("An update of Cha Dash is already running.")
	}
	return nil
}

func (s *Service) selfUpdateImpact(_ context.Context, _ action.Request) []action.Impact {
	out := []action.Impact{{Resource: action.HubResource, Via: "updates", Depth: 0}}
	if s.graph != nil {
		out = append(out, action.GraphImpact(s.graph, action.HubResource)...)
	}
	return out
}

func (s *Service) executeSelfUpdate(ctx context.Context, r action.Request, run *action.TaskRun) error {
	ref := selfRefOf(r)
	var res stackproto.SelfUpdateResult
	// If this hub is the one recreated, it stops here, mid-call, and the
	// next start resumes the task.
	err := s.call(ctx, ref.Node, stackproto.VerbSelfUpdate, stackproto.SelfUpdateRequest{
		Image: imageOf(r), By: r.Actor.Name, Via: r.Actor.Via, Ref: run.ID(),
	}, &res, 0, progressTo(run))
	if err != nil {
		return nodeFail(err)
	}
	return selfUpdateOutcome(res, run)
}

// resumeSelfUpdate finishes a chadash.update the hub lost track of (it was
// the hub being recreated): it asks the node how the update with this
// task's ID ended, waiting while it still runs or the node is away.
func (s *Service) resumeSelfUpdate(ctx context.Context, t action.Task, run *action.TaskRun) error {
	node, ok := strings.CutPrefix(t.Target, NodeTargetPrefix)
	if !ok {
		return action.Fail("failed", "The task's target isn't a node.")
	}
	run.Step("Ask the node how the update ended")
	for {
		res, allowed, err := s.selfUpdateStatus(ctx, node)
		switch {
		case err == nil && !allowed:
			return action.Fail("interrupted", "Cha Dash restarted during the update, and the node no longer allows self-update, so how it ended isn't known.")
		case err == nil && res.Ref != t.ID:
			return action.Fail("interrupted", "Cha Dash restarted before the node started the update, so nothing was changed.")
		case err == nil && res.Outcome != stackproto.OutcomeRunning:
			return selfUpdateOutcome(res, run)
		}
		// Still running, or the node isn't back yet: ask again.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(resumePoll):
		}
	}
}

// selfUpdateOutcome turns the node's result into the task's.
func selfUpdateOutcome(res stackproto.SelfUpdateResult, run *action.TaskRun) error {
	if res.Commit != "" {
		run.Logf("Commit %s", short(res.Commit))
	}
	if res.Current != "" {
		run.Logf("The hub runs %s", res.Current)
	}
	switch res.Outcome {
	case stackproto.OutcomeDeployed:
		return nil
	case stackproto.OutcomeUnchanged:
		run.Logf("%s", res.Reason)
		return nil
	case stackproto.OutcomeBlocked:
		return action.Fail("blocked", res.Reason)
	case stackproto.OutcomeRolledBack:
		return action.Fail("rolled_back", "The new version failed its health check, so the node put the previous one back on its own: "+res.Reason)
	case stackproto.OutcomeStuck:
		return action.Fail("stuck", "The update failed and so did putting the previous version back: "+res.Reason)
	}
	return action.Fail("failed", "The node gave an unknown outcome: "+res.Outcome)
}

var imageTagRE = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$`)

// selfUpdateImage is the reference a tag names.
func selfUpdateImage(image string) string {
	if strings.Contains(image, "/") {
		return image
	}
	return "ghcr.io/ban-red/chadash:" + image
}

func imageOf(r action.Request) string {
	var p struct {
		Image string `json:"image"`
	}
	_ = r.Decode(&p)
	return p.Image
}

// imageSchema is chadash.update's params: {"image": "<tag>"}, or the hub's
// repository with a tag. The node pins the digest.
type imageSchema struct{}

func (imageSchema) MarshalJSON() ([]byte, error) {
	return []byte(`{"type":"object","properties":{"image":{"type":"string","title":"Image tag","description":"A tag of ghcr.io/ban-red/chadash, such as 0.3.0"}},"required":["image"],"additionalProperties":false}`), nil
}

func (imageSchema) Validate(params map[string]any) []action.FieldError {
	for k := range params {
		if k != "image" {
			return []action.FieldError{{Path: "/" + k, Message: "chadash.update takes only image."}}
		}
	}
	img, _ := params["image"].(string)
	tag, ok := strings.CutPrefix(img, "ghcr.io/ban-red/chadash:")
	if !ok {
		tag = img
	}
	if !imageTagRE.MatchString(tag) {
		return []action.FieldError{{Path: "/image", Message: "image must be a tag of ghcr.io/ban-red/chadash, such as 0.3.0."}}
	}
	return nil
}

func (imageSchema) Secrets() []string { return nil }
