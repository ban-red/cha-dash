package stacks

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/stackproto"
)

// ActionUpdate pulls a stack's tags and recreates the services whose image
// moved, with the file unchanged (plan 16, wave 32b).
const ActionUpdate = "stack.update"

// updateAction is stack.update for Actions:
//
//	stack.update  T2  {"services"?}: new images through the deploy's gate, an empty commit; undo is a rollback
//
// Resolve asks the node which images moved (stack.update.check), so the
// plan lists each service's old and new digest and an approval re-checks
// the registry's answer is the same.
func (s *Service) updateAction() action.Def {
	return action.Def{
		ID: ActionUpdate, Title: "Update", Targets: []string{"stack"}, Tier: action.T2, Async: true, Timeout: deployTimeout,
		Params:        servicesSchema{},
		Resolve:       s.resolveFor(ActionUpdate),
		Preconditions: s.updatePreconditions,
		Steps:         updateSteps,
		BlastRadius:   s.blastRadius,
		Undo: func(r action.Request) *action.Undo {
			ref := refOf(r)
			if ref.Head == "" {
				return nil
			}
			return &action.Undo{Action: ActionRollback, Params: commitParams(ref.Head), Label: "Roll back " + r.Target.Name + " to " + short(ref.Head) + " (the images it ran)"}
		},
		Execute: s.executeUpdate,
	}
}

// moves are the images this update would move: those that moved, of the
// services asked for, less those held.
func (ref targetRef) moves(r action.Request) []stackproto.ImageMove {
	want := servicesOf(r)
	var out []stackproto.ImageMove
	for _, m := range ref.Moves {
		if !m.Moved || slices.Contains(ref.Held, m.Service) || (len(want) > 0 && !slices.Contains(want, m.Service)) {
			continue
		}
		out = append(out, m)
	}
	return out
}

func (s *Service) updatePreconditions(_ context.Context, r action.Request) error {
	ref := refOf(r)
	if err := s.common(ref); err != nil {
		return err
	}
	if !ref.Managed {
		return action.Precondition(fmt.Sprintf("%s isn't managed by Cha Dash yet. Adopt it first.", ref.Stack))
	}
	for _, svc := range servicesOf(r) {
		if !slices.ContainsFunc(ref.Moves, func(m stackproto.ImageMove) bool { return m.Service == svc }) {
			return action.Precondition(fmt.Sprintf("%s has no service called %s.", ref.Stack, svc))
		}
	}
	if len(ref.moves(r)) == 0 {
		why := []string{}
		for _, m := range ref.Moves {
			if m.Reason != "" {
				why = append(why, m.Service+": "+strings.TrimSuffix(m.Reason, "."))
			}
		}
		if len(why) > 0 {
			return action.Precondition(fmt.Sprintf("No image of %s moved in its registry (%s).", ref.Stack, strings.Join(why, "; ")))
		}
		return action.Precondition(fmt.Sprintf("Every image of %s is current: none moved in its registry.", ref.Stack))
	}
	return nil
}

func updateSteps(r action.Request) []action.Step {
	ref := refOf(r)
	moves := ref.moves(r)
	steps := []action.Step{{Label: "Check every image can be pulled (a dry run); nothing stops if one can't"}}
	for _, m := range moves {
		steps = append(steps, action.Step{Label: fmt.Sprintf("Pull %s and recreate %s: %s → %s", m.Image, m.Service, shortDigest(m.Current), shortDigest(m.Latest))})
	}
	var left []string
	for _, m := range ref.Moves {
		if !slices.ContainsFunc(moves, func(x stackproto.ImageMove) bool { return x.Service == m.Service }) {
			left = append(left, m.Service)
		}
	}
	if len(left) > 0 {
		steps = append(steps, action.Step{Label: "Leave " + strings.Join(left, ", ") + " as they are"})
	}
	steps = append(steps, action.Step{Label: "Commit the update with the file unchanged, so a rollback returns the images running now"})
	return append(steps, gateSteps(ref.Soak)...)
}

func (s *Service) executeUpdate(ctx context.Context, r action.Request, run *action.TaskRun) error {
	ref := refOf(r)
	_, secrets, err := s.versionAt(ctx, ref, ref.Head)
	if err != nil {
		return err
	}
	defer clear(secrets)
	expect := map[string]string{}
	var services []string
	for _, m := range ref.moves(r) {
		expect[m.Service] = m.Latest
		services = append(services, m.Service)
	}
	var res stackproto.DeployResult
	err = s.call(ctx, ref.Node, stackproto.VerbUpdate, stackproto.UpdateRequest{
		Name: ref.Stack, ExpectHead: ref.Head, Services: services, Expect: expect, Secrets: secrets,
		Hold: s.held(ctx, ref.Node, ref.Stack), By: r.Actor.Name, Via: r.Actor.Via, Ref: run.ID(),
	}, &res, 0, progressTo(run))
	if err != nil {
		return nodeFail(err)
	}
	for _, sp := range res.Plan.Services {
		if sp.From != "" || sp.To != "" {
			run.Logf("%s: %s → %s", sp.Name, shortDigest(sp.From), shortDigest(sp.To))
			if want := expect[sp.Name]; want != "" && sp.To != "" && sp.To != want {
				run.Logf("%s moved again since the plan: it runs %s, not %s", sp.Name, shortDigest(sp.To), shortDigest(want))
			}
		}
	}
	return s.outcome(ctx, ref, res, run)
}

// shortDigest is a digest as people read it: "sha256:" and 12 hex digits.
func shortDigest(d string) string {
	if d == "" {
		return "unknown"
	}
	algo, hex, ok := strings.Cut(d, ":")
	if !ok {
		return d[:min(len(d), 12)]
	}
	return algo + ":" + hex[:min(len(hex), 12)]
}

// ── params ───────────────────────────────────────────────────────────────

func servicesOf(r action.Request) []string {
	var p struct {
		Services []string `json:"services"`
	}
	_ = r.Decode(&p)
	return p.Services
}

// servicesParams is stack.update's params for some services only.
func servicesParams(services ...string) json.RawMessage {
	if len(services) == 0 {
		return json.RawMessage(`{}`)
	}
	b, _ := json.Marshal(map[string][]string{"services": services})
	return b
}

var serviceRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

// servicesSchema is stack.update's params: {"services": [names]}, optional;
// none is every service whose image moved.
type servicesSchema struct{}

func (servicesSchema) MarshalJSON() ([]byte, error) {
	return []byte(`{"type":"object","properties":{"services":{"type":"array","items":{"type":"string","pattern":"^[a-zA-Z0-9][a-zA-Z0-9_.-]*$"},"maxItems":100,"title":"Services"}},"additionalProperties":false}`), nil
}

func (servicesSchema) Validate(params map[string]any) []action.FieldError {
	for k, v := range params {
		if k != "services" {
			return []action.FieldError{{Path: "/" + k, Message: "stack.update takes only services."}}
		}
		list, ok := v.([]any)
		if !ok || len(list) > 100 {
			return []action.FieldError{{Path: "/services", Message: "services must be a list of service names."}}
		}
		for i, x := range list {
			n, ok := x.(string)
			if !ok || !serviceRE.MatchString(n) {
				return []action.FieldError{{Path: fmt.Sprintf("/services/%d", i), Message: "A service name is letters, digits, '.', '_' and '-'."}}
			}
		}
	}
	return nil
}

func (servicesSchema) Secrets() []string { return nil }
