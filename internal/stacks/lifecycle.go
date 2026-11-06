package stacks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/stackproto"
)

// A whole stack's lifecycle (plan 16, wave 32a): stop, start, restart,
// down and remove, each a node verb (stackproto.VerbStop and the rest).
const (
	ActionStop    = "stack.stop"
	ActionStart   = "stack.start"
	ActionRestart = "stack.restart"
	ActionDown    = "stack.down"
	ActionRemove  = "stack.remove"
)

// lifecycleTimeout bounds stop, start, down and remove: Compose's own
// stop timeout per container, times a few. A restart waits for the health
// gate too, and a start that deploys takes a deploy's.
const lifecycleTimeout = 10 * time.Minute

func isLifecycle(id string) bool {
	switch id {
	case ActionStop, ActionStart, ActionRestart, ActionDown, ActionRemove:
		return true
	}
	return false
}

// lifecycleActions are, for Actions:
//
//	stack.stop     T1  compose stop: the containers stay; undo starts it
//	stack.start    T1  compose start, or a deploy of the last commit when it has no containers; undo stops it
//	stack.restart  T1  compose restart, then the health gate, reporting only
//	stack.down     T2  compose down: containers and networks go, volumes stay; undo starts it (a deploy)
//	stack.remove   T3  down, then the folder leaves the root as a commit; {"volumes", "confirm_volumes"}; undo restores the last commit
func (s *Service) lifecycleActions() []action.Def {
	return []action.Def{
		{
			ID: ActionStop, Title: "Stop", Targets: []string{"stack"}, Tier: action.T1, Async: true, Timeout: lifecycleTimeout,
			Resolve:       s.resolveFor(ActionStop),
			Preconditions: s.lifecyclePreconditions,
			Steps:         lifecycleSteps,
			BlastRadius:   s.blastRadius,
			Undo: func(r action.Request) *action.Undo {
				return &action.Undo{Action: ActionStart, Params: json.RawMessage(`{}`), Label: "Start " + r.Target.Name}
			},
			Execute: s.executeLifecycle,
		},
		{
			ID: ActionStart, Title: "Start", Targets: []string{"stack"}, Tier: action.T1, Async: true, Timeout: deployTimeout,
			Resolve:       s.resolveFor(ActionStart),
			Preconditions: s.lifecyclePreconditions,
			Steps:         lifecycleSteps,
			BlastRadius:   s.blastRadius,
			Undo: func(r action.Request) *action.Undo {
				return &action.Undo{Action: ActionStop, Params: json.RawMessage(`{}`), Label: "Stop " + r.Target.Name}
			},
			Execute: s.executeLifecycle,
		},
		{
			ID: ActionRestart, Title: "Restart", Targets: []string{"stack"}, Tier: action.T1, Async: true, Timeout: lifecycleTimeout,
			Resolve:       s.resolveFor(ActionRestart),
			Preconditions: s.lifecyclePreconditions,
			Steps:         lifecycleSteps,
			BlastRadius:   s.blastRadius,
			Execute:       s.executeLifecycle,
		},
		{
			ID: ActionDown, Title: "Take down", Targets: []string{"stack"}, Tier: action.T2, Async: true, Timeout: lifecycleTimeout,
			Resolve:       s.resolveFor(ActionDown),
			Preconditions: s.lifecyclePreconditions,
			Steps:         lifecycleSteps,
			BlastRadius:   s.blastRadius,
			Undo: func(r action.Request) *action.Undo {
				ref := refOf(r)
				label := "Start " + r.Target.Name + " again"
				if ref.Head != "" {
					label += " (deploys " + short(ref.Head) + ")"
				}
				return &action.Undo{Action: ActionStart, Params: json.RawMessage(`{}`), Label: label}
			},
			Execute: s.executeLifecycle,
		},
		{
			ID: ActionRemove, Title: "Remove", Targets: []string{"stack"}, Tier: action.T3, Async: true, Timeout: lifecycleTimeout,
			Params:        removeSchema{},
			Resolve:       s.resolveFor(ActionRemove),
			Preconditions: s.lifecyclePreconditions,
			Label: func(r action.Request) string {
				if removeOf(r).Volumes {
					return "Remove " + r.Target.Name + " and its volumes"
				}
				return "Remove " + r.Target.Name
			},
			Steps:       lifecycleSteps,
			BlastRadius: s.blastRadius,
			Undo: func(r action.Request) *action.Undo {
				ref := refOf(r)
				if ref.Head == "" {
					return nil
				}
				label := "Restore " + r.Target.Name + " from " + short(ref.Head)
				if removeOf(r).Volumes && len(ref.Volumes) > 0 {
					label += " (its volumes start empty)"
				}
				return &action.Undo{Action: ActionRollback, Params: commitParams(ref.Head), Label: label}
			},
			Execute: s.executeLifecycle,
		},
	}
}

// ── preconditions and steps ──────────────────────────────────────────────

func (s *Service) lifecyclePreconditions(_ context.Context, r action.Request) error {
	ref := refOf(r)
	if ref.Protected != "" {
		return action.Unavailable(ref.Protected, "")
	}
	if !ref.Managed {
		return action.Precondition(fmt.Sprintf("%s isn't managed by Cha Dash yet. Adopt it first.", ref.Stack))
	}
	switch r.Action {
	case ActionStop:
		if ref.Status == stackproto.StatusStopped || ref.Status == stackproto.StatusDown {
			return action.Precondition(fmt.Sprintf("%s isn't running.", ref.Stack))
		}
	case ActionStart:
		if ref.Status == stackproto.StatusRunning {
			return action.Precondition(fmt.Sprintf("%s is already running.", ref.Stack))
		}
		if ref.Status == stackproto.StatusDown && ref.Drift {
			// Starting it deploys the last commit, which would overwrite the edits.
			return s.common(ref)
		}
	case ActionRestart:
		if ref.Status == stackproto.StatusDown {
			return action.Precondition(fmt.Sprintf("%s has no containers to restart. Start it instead.", ref.Stack))
		}
	case ActionDown:
		if ref.Status == stackproto.StatusDown {
			return action.Precondition(fmt.Sprintf("%s is already down.", ref.Stack))
		}
	case ActionRemove:
		if err := s.common(ref); err != nil {
			return err
		}
		if p := removeOf(r); p.Volumes && strings.TrimSpace(p.ConfirmVolumes) != ref.Stack {
			return action.Precondition(fmt.Sprintf("To remove %s's volumes too, type its name (%s) again.", ref.Stack, ref.Stack))
		}
	}
	return nil
}

func lifecycleSteps(r action.Request) []action.Step {
	ref := refOf(r)
	who := ref.Stack
	if len(ref.Containers) > 0 {
		who = strings.Join(ref.Containers, ", ")
	}
	kept := func(prefix string) []action.Step {
		if len(ref.Volumes) == 0 {
			return nil
		}
		return []action.Step{{Label: prefix + strings.Join(ref.Volumes, ", ")}}
	}
	switch r.Action {
	case ActionStop:
		return []action.Step{{Label: "Stop " + who}, {Label: "The containers stay, so Start brings them back as they were"}}
	case ActionStart:
		if ref.Status == stackproto.StatusDown {
			steps := []action.Step{{Label: ref.Stack + " has no containers, so this deploys its last commit (" + short(ref.Head) + ")"}}
			return append(steps, gateSteps(ref.Soak)...)
		}
		return []action.Step{{Label: "Start " + who}}
	case ActionRestart:
		soak := ref.Soak
		if soak == "" {
			soak = "20s"
		}
		return []action.Step{
			{Label: "Restart " + who},
			{Label: "Wait for every container to come back up (and healthy, where it has a healthcheck), then hold for " + soak},
			{Label: "The health gate only reports: nothing rolls back"},
		}
	case ActionDown:
		return append([]action.Step{
			{Label: "Remove " + who + " and the stack's networks"},
		}, kept("Keep the volumes ")...)
	case ActionRemove:
		steps := []action.Step{{Label: "Take " + ref.Stack + " down: remove " + who + " and the stack's networks"}}
		if removeOf(r).Volumes {
			if len(ref.Volumes) > 0 {
				steps = append(steps, action.Step{Label: "Delete the volumes " + strings.Join(ref.Volumes, ", ") + " and the data in them"})
			}
		} else {
			steps = append(steps, kept("Keep the volumes ")...)
		}
		if ref.Location == stackproto.LocationExternal {
			steps = append(steps, action.Step{Label: "Stop managing it; its files stay at " + ref.Path})
		} else {
			steps = append(steps, action.Step{Label: "Delete its folder from the stacks root, with its .env and anything else kept there, as a commit: its history keeps the file, not the .env"})
		}
		return steps
	}
	return nil
}

// ── execute ──────────────────────────────────────────────────────────────

var verbOf = map[string]string{
	ActionStop: stackproto.VerbStop, ActionStart: stackproto.VerbStart, ActionRestart: stackproto.VerbRestart,
	ActionDown: stackproto.VerbDown, ActionRemove: stackproto.VerbRemove,
}

func (s *Service) executeLifecycle(ctx context.Context, r action.Request, run *action.TaskRun) error {
	ref := refOf(r)
	var req any = stackproto.LifecycleRequest{Name: ref.Stack, ExpectHead: ref.Head, By: r.Actor.Name, Via: r.Actor.Via}
	if r.Action == ActionRemove {
		req = stackproto.RemoveRequest{Name: ref.Stack, ExpectHead: ref.Head, Volumes: removeOf(r).Volumes, By: r.Actor.Name, Via: r.Actor.Via}
	}
	var res stackproto.LifecycleResult
	err := s.call(ctx, ref.Node, verbOf[r.Action], req, &res, 0, progressTo(run))
	if e := stackproto.ErrorFrom(err); e != nil && e.Code == stackproto.CodeNotDeployed && r.Action == ActionStart {
		// Never up, or taken down: deploying the last commit is how it comes back.
		return s.deployHead(ctx, r, ref, run)
	}
	if err != nil {
		if _, code, msg := statusOf(err); code == "forbidden" && r.Action == ActionRemove {
			return action.Fail(code, msg+" Removing stacks is off by default: add remove to stacks.allow in the node's policy.yaml to turn it on.")
		}
		return nodeFail(err)
	}
	if res.Commit != nil {
		run.Logf("Commit %s", short(res.Commit.Commit))
	}
	if len(res.VolumesKept) > 0 {
		run.Logf("Volumes kept: %s", strings.Join(res.VolumesKept, ", "))
	}
	if len(res.VolumesRemoved) > 0 {
		run.Logf("Volumes removed: %s", strings.Join(res.VolumesRemoved, ", "))
	}
	if res.FilesKept != "" {
		run.Logf("Its files stay at %s", res.FilesKept)
	}
	if res.Healthy != nil && !*res.Healthy {
		return action.Fail("unhealthy", fmt.Sprintf("%s restarted, but %s didn't pass the health gate. Nothing was rolled back.", ref.Stack, strings.Join(res.Unhealthy, ", ")))
	}
	return nil
}

// deployHead deploys a stack's last commit as it is: start on a stack with
// no containers. The secrets it sends are only those an earlier deploy
// granted to this stack on this node, as for a rollback.
func (s *Service) deployHead(ctx context.Context, r action.Request, ref targetRef, run *action.TaskRun) error {
	run.Step("Deploy " + ref.Stack + " at " + short(ref.Head))
	content, secrets, err := s.versionAt(ctx, ref, ref.Head)
	if err != nil {
		return err
	}
	defer clear(secrets)
	var res stackproto.DeployResult
	err = s.call(ctx, ref.Node, stackproto.VerbDeploy, stackproto.DeployRequest{
		Stack: ref.Stack, Content: content, Secrets: secrets, ExpectHead: ref.Head,
		By: r.Actor.Name, Via: r.Actor.Via, Ref: run.ID(), Hold: s.held(ctx, ref.Node, ref.Stack),
	}, &res, 0, progressTo(run))
	if err != nil {
		return nodeFail(err)
	}
	return s.outcome(ctx, ref, res, run)
}

// versionAt reads a stack's file at a commit from its node, with the
// secrets it references. The text and the names come from the node, so
// the hub resolves only names an earlier deploy already granted to this
// stack on this node. The caller clears the map.
func (s *Service) versionAt(ctx context.Context, ref targetRef, commit string) (string, map[string]string, error) {
	var old stackproto.Stack
	if err := s.call(ctx, ref.Node, stackproto.VerbGet, stackproto.GetRequest{Stack: ref.Stack, At: commit}, &old, s.timeout, nil); err != nil {
		return "", nil, nodeFail(err)
	}
	names := sortedUnique(append(slices.Clone(old.Secrets), secretRefs(old.Content)...))
	var notGranted errSecretNotGranted
	if err := s.grantedOnly(ctx, ref.Node, ref.Stack, names); errors.As(err, &notGranted) {
		return "", nil, action.Fail("secret_not_granted", notGranted.Error())
	} else if err != nil {
		return "", nil, err
	}
	secrets, err := s.resolveSecrets(ctx, names)
	if err != nil {
		return "", nil, action.Fail("secret_missing", firstSentenceOf(err))
	}
	return old.Content, secrets, nil
}

// ── params ───────────────────────────────────────────────────────────────

type removeParams struct {
	Volumes        bool   `json:"volumes"`
	ConfirmVolumes string `json:"confirm_volumes"`
}

func removeOf(r action.Request) removeParams {
	var p removeParams
	_ = r.Decode(&p)
	return p
}

// removeSchema is stack.remove's params: {"volumes": bool,
// "confirm_volumes": the stack's name, typed again when volumes is true}.
type removeSchema struct{}

func (removeSchema) MarshalJSON() ([]byte, error) {
	return []byte(`{"type":"object","properties":{"volumes":{"type":"boolean","title":"Also delete its volumes"},"confirm_volumes":{"type":"string","title":"Type the stack's name to delete its volumes"}},"additionalProperties":false}`), nil
}

func (removeSchema) Validate(params map[string]any) []action.FieldError {
	for k, v := range params {
		switch k {
		case "volumes":
			if _, ok := v.(bool); !ok {
				return []action.FieldError{{Path: "/volumes", Message: "volumes must be true or false."}}
			}
		case "confirm_volumes":
			if _, ok := v.(string); !ok {
				return []action.FieldError{{Path: "/confirm_volumes", Message: "confirm_volumes must be the stack's name."}}
			}
		default:
			return []action.FieldError{{Path: "/" + k, Message: "stack.remove takes only volumes and confirm_volumes."}}
		}
	}
	return nil
}

func (removeSchema) Secrets() []string { return nil }
