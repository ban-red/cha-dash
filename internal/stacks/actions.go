package stacks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/livedoc"
	"dash.cha.sh/internal/stackproto"
)

// ActionSource names the stacks' actions in the engine's registry.
const ActionSource = "stacks"

// Action IDs.
const (
	ActionDeploy   = "stack.deploy"
	ActionRollback = "stack.rollback"
	ActionAdopt    = "stack.adopt"
	ActionUnadopt  = "stack.unadopt"
)

// deployTimeout bounds a deploy's task: pulls, up --wait (2 minutes by
// default), the soak, and possibly a rollback's.
const deployTimeout = 30 * time.Minute

// Actions are the stack actions for the engine (Registry.Register with
// ActionSource):
//
//	stack.deploy    T2  the saved draft, through the node's seven steps; undo is a rollback
//	stack.rollback  T2  {"commit"}: an earlier version as a new commit, pinned; undo rolls forward
//	stack.adopt     T1  adopt a running project in place (or accept edits made outside); undo un-adopts
//	stack.unadopt   T1  stop managing a stack; nothing on disk or in Docker changes; undo adopts
//
// Targets are stack:<node>/<name>. Each Resolve reads the stack from its
// node, so a plan shows the real services and an approval re-checks that
// nothing changed in between.
func (s *Service) Actions() []action.Def {
	return []action.Def{
		{
			ID: ActionDeploy, Title: "Deploy", Targets: []string{"stack"}, Tier: action.T2, Async: true, Timeout: deployTimeout,
			Resolve:       s.resolveFor(ActionDeploy),
			Preconditions: s.deployPreconditions,
			Label:         func(r action.Request) string { return "Deploy " + r.Target.Name },
			Steps:         deploySteps,
			BlastRadius:   s.blastRadius,
			Undo: func(r action.Request) *action.Undo {
				ref := refOf(r)
				if ref.Head == "" {
					return nil
				}
				return &action.Undo{Action: ActionRollback, Params: commitParams(ref.Head), Label: "Roll back " + r.Target.Name + " to " + short(ref.Head)}
			},
			Execute: s.executeDeploy,
		},
		{
			ID: ActionRollback, Title: "Roll back", Targets: []string{"stack"}, Tier: action.T2, Async: true, Timeout: deployTimeout,
			Params:        commitSchema{},
			Resolve:       s.resolveFor(ActionRollback),
			Preconditions: s.rollbackPreconditions,
			Label: func(r action.Request) string {
				return "Roll back " + r.Target.Name + " to " + short(commitOf(r))
			},
			Steps:       rollbackSteps,
			BlastRadius: s.blastRadius,
			Undo: func(r action.Request) *action.Undo {
				ref := refOf(r)
				if ref.Head == "" {
					return nil
				}
				return &action.Undo{Action: ActionRollback, Params: commitParams(ref.Head), Label: "Return " + r.Target.Name + " to " + short(ref.Head)}
			},
			Execute: s.executeRollback,
		},
		{
			ID: ActionAdopt, Title: "Adopt", Targets: []string{"stack"}, Tier: action.T1,
			Resolve:       s.resolveFor(ActionAdopt),
			Preconditions: s.adoptPreconditions,
			Label: func(r action.Request) string {
				if refOf(r).Managed {
					return "Accept edits to " + r.Target.Name
				}
				return "Adopt " + r.Target.Name
			},
			Steps: func(r action.Request) []action.Step {
				if refOf(r).Managed {
					return []action.Step{{Label: "Commit the file on disk as the stack's new version"}, {Label: "Nothing restarts"}}
				}
				return []action.Step{{Label: "Start versioning " + r.Target.Name + " where it is"}, {Label: "Nothing moves and nothing restarts"}}
			},
			BlastRadius: func(context.Context, action.Request) []action.Impact { return []action.Impact{} },
			Undo: func(r action.Request) *action.Undo {
				if refOf(r).Managed {
					return nil // accepting edits isn't undone by forgetting the stack
				}
				return &action.Undo{Action: ActionUnadopt, Params: json.RawMessage(`{}`), Label: "Un-adopt " + r.Target.Name}
			},
			Execute: s.executeAdopt,
		},
		{
			ID: ActionUnadopt, Title: "Un-adopt", Targets: []string{"stack"}, Tier: action.T1,
			Resolve:       s.resolveFor(ActionUnadopt),
			Preconditions: s.unadoptPreconditions,
			Steps: func(r action.Request) []action.Step {
				return []action.Step{{Label: "Stop versioning " + r.Target.Name}, {Label: "Its files and containers stay as they are"}}
			},
			BlastRadius: func(context.Context, action.Request) []action.Impact { return []action.Impact{} },
			Undo: func(r action.Request) *action.Undo {
				return &action.Undo{Action: ActionAdopt, Params: json.RawMessage(`{}`), Label: "Adopt " + r.Target.Name + " again"}
			},
			Execute: s.executeUnadopt,
		},
	}
}

// ── the resolved target ──────────────────────────────────────────────────

// targetRef is what Resolve learned about the stack, carried to the other
// hooks in Target.Ref (JSON). It never holds secret values.
type targetRef struct {
	Node          string           `json:"node"`
	Stack         string           `json:"stack"`
	Exists        bool             `json:"exists"`
	Managed       bool             `json:"managed"`
	Drift         bool             `json:"drift"`
	Protected     string           `json:"protected,omitempty"` // the reason
	Head          string           `json:"head,omitempty"`
	History       []string         `json:"history,omitempty"` // commit hashes, newest first
	DraftRevision int64            `json:"draft_revision,omitempty"`
	DraftSHA      string           `json:"draft_sha,omitempty"`
	Problem       string           `json:"problem,omitempty"` // why it can't run, a sentence
	Plan          *stackproto.Plan `json:"plan,omitempty"`
	Soak          string           `json:"soak,omitempty"`
}

func refOf(r action.Request) targetRef {
	var ref targetRef
	_ = json.Unmarshal([]byte(r.Target.Ref), &ref)
	return ref
}

// resolveFor reads the stack from its node: whether it exists, its head,
// its draft, and for a deploy the plan, so the plan's steps name the
// services. The target's state (what an approval must find unchanged) is
// the head and the draft.
func (s *Service) resolveFor(id string) func(ctx context.Context, target string) (action.Target, error) {
	return func(ctx context.Context, target string) (action.Target, error) {
		node, name, ok := parseTarget(target)
		if !ok {
			return action.Target{}, action.ErrNotFound
		}
		ref := targetRef{Node: node, Stack: name}
		var st stackproto.Stack
		err := s.call(ctx, node, stackproto.VerbGet, stackproto.GetRequest{Stack: name}, &st, s.timeout, nil)
		var off errNodeOffline
		switch e := stackproto.ErrorFrom(err); {
		case errors.As(err, &off):
			return action.Target{}, action.Unavailable(off.Error(), "")
		case e != nil && e.Code == stackproto.CodeNotFound:
			if id != ActionDeploy {
				return action.Target{}, action.ErrNotFound
			}
		case err != nil:
			return action.Target{}, action.Unavailable(firstSentence(err), "")
		default:
			ref.Exists, ref.Managed, ref.Drift = true, st.Managed, st.Drift
			ref.Protected = st.ProtectedReason
			if st.Head != nil {
				ref.Head = st.Head.Commit
			}
			for _, c := range st.History {
				ref.History = append(ref.History, c.Commit)
			}
			if soak, ok := st.Meta["soak"].(string); ok {
				ref.Soak = soak
			}
		}
		state := []string{ref.Head, fmt.Sprint(ref.Managed), fmt.Sprint(ref.Drift)}
		if id == ActionDeploy {
			draft, err := s.draft(ctx, node, name)
			if err != nil {
				return action.Target{}, err
			}
			if !ref.Exists && draft == nil {
				return action.Target{}, action.ErrNotFound
			}
			if draft != nil {
				ref.DraftRevision, ref.DraftSHA = draft.Revision, draft.sha()
				if ref.Problem == "" && ref.Protected == "" && !ref.Drift {
					ref.Plan, ref.Problem = s.planDraft(ctx, node, name, draft.Content)
				}
				if m := metaSoak(draft.Content); m != "" {
					ref.Soak = m
				}
			}
			state = append(state, ref.DraftSHA)
		}
		raw, _ := json.Marshal(ref)
		sum := sha256.Sum256([]byte(strings.Join(state, "\x00")))
		return action.Target{ID: target, Kind: "stack", Name: name, State: hex.EncodeToString(sum[:8]), Ref: string(raw)}, nil
	}
}

// planDraft validates and plans a draft; problem is why it can't deploy.
func (s *Service) planDraft(ctx context.Context, node, name, content string) (*stackproto.Plan, string) {
	v, err := s.validate(ctx, node, name, content)
	if err != nil {
		return nil, firstSentence(err)
	}
	if !v.OK {
		for _, m := range v.Messages {
			if m.Severity == stackproto.SeverityError {
				return nil, "The draft isn't valid yet: " + m.Message
			}
		}
		return nil, "The draft isn't valid yet."
	}
	return v.Plan, ""
}

var soakRE = regexp.MustCompile(`(?m)^\s+soak:\s*["']?([0-9][0-9a-z.]*)["']?\s*$`)

// metaSoak finds x-chadash's soak in a file, for the plan's wording only.
func metaSoak(content string) string {
	i := strings.Index(content, "x-chadash:")
	if i < 0 {
		return ""
	}
	if m := soakRE.FindStringSubmatch(content[i:]); m != nil {
		if d, err := time.ParseDuration(m[1]); err == nil {
			return d.String()
		}
	}
	return ""
}

func firstSentence(err error) string {
	_, _, msg := statusOf(err)
	return msg
}

// ── preconditions ────────────────────────────────────────────────────────

func (s *Service) common(ref targetRef) error {
	if ref.Protected != "" {
		return action.Unavailable(ref.Protected, "")
	}
	if ref.Drift {
		return action.Precondition(fmt.Sprintf("%s was edited outside Cha Dash since its last commit. Accept the edits first (Adopt), or put the file back.", ref.Stack))
	}
	return nil
}

func (s *Service) deployPreconditions(_ context.Context, r action.Request) error {
	ref := refOf(r)
	if err := s.common(ref); err != nil {
		return err
	}
	switch {
	case ref.Exists && !ref.Managed:
		return action.Precondition(fmt.Sprintf("%s isn't managed by Cha Dash yet. Adopt it first.", ref.Stack))
	case ref.DraftSHA == "":
		return action.Precondition(fmt.Sprintf("There's nothing to deploy: save a draft of %s first.", ref.Stack))
	case ref.Problem != "":
		return action.Precondition(ref.Problem)
	case ref.Plan != nil && !ref.Plan.Changed:
		return action.Precondition(fmt.Sprintf("The draft is the same as what %s runs, so there's nothing to deploy.", ref.Stack))
	}
	return nil
}

func (s *Service) rollbackPreconditions(_ context.Context, r action.Request) error {
	ref := refOf(r)
	if err := s.common(ref); err != nil {
		return err
	}
	c := commitOf(r)
	if !ref.Managed {
		return action.Precondition(fmt.Sprintf("%s isn't managed by Cha Dash, so it has no history to roll back to.", ref.Stack))
	}
	if strings.HasPrefix(ref.Head, c) {
		return action.Precondition(fmt.Sprintf("%s is already at %s.", ref.Stack, short(c)))
	}
	for _, h := range ref.History {
		if strings.HasPrefix(h, c) {
			return nil
		}
	}
	return action.Precondition(fmt.Sprintf("%s isn't in %s's recent history.", short(c), ref.Stack))
}

func (s *Service) adoptPreconditions(_ context.Context, r action.Request) error {
	ref := refOf(r)
	if ref.Managed && !ref.Drift {
		return action.Precondition(fmt.Sprintf("%s is already managed by Cha Dash.", ref.Stack))
	}
	return nil
}

func (s *Service) unadoptPreconditions(_ context.Context, r action.Request) error {
	if !refOf(r).Managed {
		return action.Precondition(fmt.Sprintf("%s isn't managed by Cha Dash.", r.Target.Name))
	}
	return nil
}

// ── steps and blast radius ───────────────────────────────────────────────

func gateSteps(soak string) []action.Step {
	if soak == "" {
		soak = "20s"
	}
	return []action.Step{
		{Label: "Start the services and wait until they're up (and healthy, where they have a healthcheck)"},
		{Label: "Hold to the health gate: every container must stay up for " + soak + ", with no restarts"},
		{Label: "If the gate fails, roll back on its own to the version running now"},
	}
}

func deploySteps(r action.Request) []action.Step {
	ref := refOf(r)
	steps := []action.Step{}
	if ref.Plan != nil && len(ref.Plan.RootEquivalent) > 0 {
		// The node's policy allows these; the person confirming should know
		// what they amount to before the hold ring.
		steps = append(steps, action.Step{Label: "Root-equivalent on the node's host, which its policy allows: " +
			strings.Join(ref.Plan.RootEquivalent, "; ") + ". Whoever can deploy this file controls that host."})
	}
	steps = append(steps, action.Step{Label: "Check every image can be pulled (a dry run); nothing stops if one can't"})
	if ref.Plan != nil {
		var pulls []string
		for _, sp := range ref.Plan.Services {
			if sp.Pull {
				pulls = append(pulls, sp.Image)
			}
		}
		if len(pulls) > 0 {
			steps = append(steps, action.Step{Label: "Pull " + strings.Join(pulls, ", ")})
		}
		for _, sp := range ref.Plan.Services {
			switch sp.Action {
			case stackproto.ActionCreate:
				steps = append(steps, action.Step{Label: "Create " + sp.Name})
			case stackproto.ActionRecreate:
				steps = append(steps, action.Step{Label: fmt.Sprintf("Recreate %s (%s; down about 2 s)", sp.Name, sp.Reason)})
			case stackproto.ActionRemove:
				steps = append(steps, action.Step{Label: "Remove " + sp.Name + " (no longer in the file)"})
			}
		}
		if ref.Plan.RecreateAll {
			steps = append(steps, action.Step{Label: "The containers were made by another Compose version, so everything is recreated once"})
		}
	}
	return append(steps, gateSteps(ref.Soak)...)
}

func rollbackSteps(r action.Request) []action.Step {
	ref := refOf(r)
	return append([]action.Step{
		{Label: "Restore " + short(commitOf(r)) + " as a new commit, pinned to the images it last ran"},
		{Label: "Check every image can be pulled (a dry run); nothing stops if one can't"},
	}, gateSteps(ref.Soak)...)
}

// blastRadius is what a deploy can take down: the stack's resources in the
// Home Graph (the Docker integration's stack:<project>), their containers,
// and everything that depends on or runs on those.
func (s *Service) blastRadius(_ context.Context, r action.Request) []action.Impact {
	out := []action.Impact{}
	if s.graph == nil {
		return out
	}
	ref := refOf(r)
	_, doc := s.graph.Snapshot(nil)
	seen := map[string]bool{}
	for id, res := range doc.Resources {
		if res.Kind != "stack" || (res.Name != ref.Stack && fmt.Sprint(res.Attrs["project"]) != ref.Stack) {
			continue
		}
		if !seen[string(id)] {
			seen[string(id)] = true
			out = append(out, action.Impact{Resource: string(id), Via: "deploys", Depth: 0})
		}
		for _, im := range action.GraphImpact(s.graph, string(id)) {
			if !seen[im.Resource] {
				seen[im.Resource] = true
				out = append(out, im)
			}
		}
	}
	return out
}

// ── execute ──────────────────────────────────────────────────────────────

// progressTo feeds a node's progress messages to the task.
func progressTo(run *action.TaskRun) func(json.RawMessage) {
	return func(raw json.RawMessage) {
		var p stackproto.Progress
		if json.Unmarshal(raw, &p) != nil {
			return
		}
		if p.Step != "" {
			run.Step(p.Step)
			run.Logf("%s", p.Step)
		}
		if p.Line != "" {
			run.Logf("%s", p.Line)
		}
		if p.Fraction != nil {
			run.Progress(*p.Fraction)
		}
	}
}

func (s *Service) executeDeploy(ctx context.Context, r action.Request, run *action.TaskRun) error {
	ref := refOf(r)
	draft, err := s.draft(ctx, ref.Node, ref.Stack)
	if err != nil {
		return err
	}
	if draft == nil || draft.sha() != ref.DraftSHA {
		return action.Fail("conflict", "The draft changed since this was planned. Plan the deploy again.")
	}
	run.Step("Read the secrets the file references")
	refs := secretRefs(draft.Content)
	secrets, err := s.resolveSecrets(ctx, refs)
	if err != nil {
		return action.Fail("secret_missing", firstSentenceOf(err))
	}
	defer clear(secrets)
	// The draft is the hub's content, so these names are the stack's: a
	// later rollback may send them again, and nothing else.
	if err := s.grantSecrets(ctx, ref.Node, ref.Stack, refs); err != nil {
		return err
	}
	var res stackproto.DeployResult
	err = s.call(ctx, ref.Node, stackproto.VerbDeploy, stackproto.DeployRequest{
		Stack: ref.Stack, Content: draft.Content, Secrets: secrets, ExpectHead: ref.Head,
		By: r.Actor.Name, Via: r.Actor.Via, Ref: run.ID(),
	}, &res, 0, progressTo(run))
	if err != nil {
		return nodeFail(err)
	}
	if err := s.outcome(ctx, ref, res, run); err != nil {
		return err
	}
	// The draft is what runs now; clear it (unless someone saved a newer one).
	if res.Outcome == stackproto.OutcomeDeployed || res.Outcome == stackproto.OutcomeUnchanged {
		if err := s.deleteDraft(context.WithoutCancel(ctx), ref.Node, ref.Stack, ref.DraftRevision); err != nil {
			s.log.Warn("stacks: couldn't clear the deployed draft", "stack", ref.Stack, "err", err)
		}
	}
	return nil
}

func (s *Service) executeRollback(ctx context.Context, r action.Request, run *action.TaskRun) error {
	ref := refOf(r)
	commit := commitOf(r)
	// The old version may reference other secrets than today's file. Its
	// text and its list of names come from the node, so the hub sends only
	// names an earlier deploy already granted to this stack on this node.
	var old stackproto.Stack
	if err := s.call(ctx, ref.Node, stackproto.VerbGet, stackproto.GetRequest{Stack: ref.Stack, At: commit}, &old, s.timeout, nil); err != nil {
		return nodeFail(err)
	}
	names := sortedUnique(append(slices.Clone(old.Secrets), secretRefs(old.Content)...))
	var notGranted errSecretNotGranted
	if err := s.grantedOnly(ctx, ref.Node, ref.Stack, names); errors.As(err, &notGranted) {
		return action.Fail("secret_not_granted", notGranted.Error())
	} else if err != nil {
		return err
	}
	secrets, err := s.resolveSecrets(ctx, names)
	if err != nil {
		return action.Fail("secret_missing", firstSentenceOf(err))
	}
	defer clear(secrets)
	var res stackproto.DeployResult
	err = s.call(ctx, ref.Node, stackproto.VerbRollback, stackproto.RollbackRequest{
		Stack: ref.Stack, Commit: commit, Secrets: secrets, ExpectHead: ref.Head,
		By: r.Actor.Name, Via: r.Actor.Via, Ref: run.ID(),
	}, &res, 0, progressTo(run))
	if err != nil {
		return nodeFail(err)
	}
	return s.outcome(ctx, ref, res, run)
}

// outcome turns a deploy's result into the task's: deployed succeeds;
// blocked, rolled back and stuck fail with their sentence. Stuck also
// raises an Inbox item; a later good deploy resolves it.
func (s *Service) outcome(ctx context.Context, ref targetRef, res stackproto.DeployResult, run *action.TaskRun) error {
	if res.Commit != "" {
		run.Logf("Commit %s", short(res.Commit))
	}
	switch res.Outcome {
	case stackproto.OutcomeDeployed:
		s.stuck(ref, "", false)
		return nil
	case stackproto.OutcomeUnchanged:
		run.Logf("%s", res.Reason)
		return nil
	case stackproto.OutcomeBlocked:
		return action.Fail("blocked", res.Reason)
	case stackproto.OutcomeRolledBack:
		s.stuck(ref, "", false)
		return action.Fail("rolled_back", "The new version failed its health check, so Cha Dash rolled back on its own: "+res.Reason)
	case stackproto.OutcomeStuck:
		s.stuck(ref, res.Reason, true)
		return action.Fail("stuck", "The deploy failed and so did the rollback: "+res.Reason)
	}
	return action.Fail("failed", "The node gave an unknown outcome: "+res.Outcome)
}

func (s *Service) executeAdopt(ctx context.Context, r action.Request, run *action.TaskRun) error {
	ref := refOf(r)
	var res stackproto.AdoptResult
	if err := s.call(ctx, ref.Node, stackproto.VerbAdopt, stackproto.AdoptRequest{Stack: ref.Stack, By: r.Actor.Name, Via: r.Actor.Via}, &res, s.timeout, nil); err != nil {
		return nodeFail(err)
	}
	run.Logf("Commit %s at %s", short(res.Commit), res.Path)
	return nil
}

func (s *Service) executeUnadopt(ctx context.Context, r action.Request, run *action.TaskRun) error {
	ref := refOf(r)
	var res stackproto.UnadoptResult
	if err := s.call(ctx, ref.Node, stackproto.VerbUnadopt, stackproto.UnadoptRequest{Stack: ref.Stack, By: r.Actor.Name, Via: r.Actor.Via}, &res, s.timeout, nil); err != nil {
		return nodeFail(err)
	}
	run.Logf("Commit %s", short(res.Commit))
	return nil
}

func nodeFail(err error) error {
	if e := stackproto.ErrorFrom(err); e != nil {
		return action.Fail(e.Code, e.Message)
	}
	_, code, msg := statusOf(err)
	return action.Fail(code, msg)
}

func firstSentenceOf(err error) string {
	var miss errSecretMissing
	if errors.As(err, &miss) {
		return miss.Error()
	}
	return "Couldn't read the stack's secrets from the vault."
}

// ── stuck ────────────────────────────────────────────────────────────────

// incident is the Inbox item's shape (sim.Incident's JSON).
type incident struct {
	ID           string         `json:"id"`
	Scenario     string         `json:"scenario"`
	Class        string         `json:"class"`
	Severity     string         `json:"severity"`
	Status       string         `json:"status"`
	Phase        string         `json:"phase"`
	Title        string         `json:"title"`
	Summary      string         `json:"summary"`
	Target       string         `json:"target"`
	OpenedAt     time.Time      `json:"opened_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
	ResolvedAt   *time.Time     `json:"resolved_at,omitempty"`
	Facts        map[string]any `json:"facts,omitempty"`
	Causes       []any          `json:"causes"`
	BlastRadius  []any          `json:"blast_radius"`
	Suggested    map[string]any `json:"suggested"`
	ResolvesWhen string         `json:"resolves_when"`
	ResolvedBy   string         `json:"resolved_by,omitempty"`
}

// stuck opens (or resolves) the stack's "stuck" Inbox item.
func (s *Service) stuck(ref targetRef, reason string, open bool) {
	if s.inbox == nil {
		return
	}
	id := "stack." + ref.Node + "." + ref.Stack + ".stuck"
	target := Target(ref.Node, ref.Stack)
	now := s.now().UTC()
	_, _ = s.inbox.Update(func(tx *livedoc.Tx) error {
		raw := tx.Item(incidentsColl, id)
		if !open {
			if raw == nil {
				return nil
			}
			var in incident
			if json.Unmarshal(raw, &in) != nil || in.Status == "resolved" {
				return nil
			}
			in.Status, in.Phase, in.UpdatedAt, in.ResolvedAt, in.ResolvedBy = "resolved", "resolved", now, &now, "action"
			tx.Put(incidentsColl, id, in)
			return nil
		}
		in := incident{
			ID: id, Scenario: "stack-stuck", Class: "problem", Severity: "down", Status: "open", Phase: "open",
			Title:   ref.Stack + " is stuck: its deploy and the rollback both failed",
			Summary: reason + " Open Settings → Stacks to deploy a fixed version or roll back to one that worked.",
			Target:  target, OpenedAt: now, UpdatedAt: now,
			Facts:  map[string]any{"node": ref.Node, "stack": ref.Stack},
			Causes: []any{}, BlastRadius: []any{},
			Suggested:    map[string]any{"id": "link.open", "label": "Open Settings → Stacks", "tier": "T0", "target": target},
			ResolvesWhen: "A deploy or rollback of " + ref.Stack + " passes its health gate.",
		}
		tx.Put(incidentsColl, id, in)
		return nil
	})
}

// incidentsColl is the inbox topic's collection (topics.Incidents).
const incidentsColl = "incidents"

// ── params ───────────────────────────────────────────────────────────────

var commitRE = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

func commitParams(h string) json.RawMessage {
	b, _ := json.Marshal(map[string]string{"commit": h})
	return b
}

func commitOf(r action.Request) string {
	var p struct {
		Commit string `json:"commit"`
	}
	_ = r.Decode(&p)
	return p.Commit
}

// commitSchema is stack.rollback's params: {"commit": "<7–40 hex>"}.
type commitSchema struct{}

func (commitSchema) MarshalJSON() ([]byte, error) {
	return []byte(`{"type":"object","properties":{"commit":{"type":"string","pattern":"^[0-9a-f]{7,40}$","title":"Commit"}},"required":["commit"],"additionalProperties":false}`), nil
}

func (commitSchema) Validate(params map[string]any) []action.FieldError {
	for k := range params {
		if k != "commit" {
			return []action.FieldError{{Path: "/" + k, Message: "stack.rollback takes only commit."}}
		}
	}
	c, ok := params["commit"].(string)
	if !ok || !commitRE.MatchString(c) {
		return []action.FieldError{{Path: "/commit", Message: "commit must be a commit hash (7 to 40 lowercase hex digits)."}}
	}
	return nil
}

func (commitSchema) Secrets() []string { return nil }

func short(h string) string {
	if len(h) > 7 {
		return h[:7]
	}
	return h
}
