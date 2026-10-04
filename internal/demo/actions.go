package demo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/topics"
	"dash.cha.sh/sim"
)

// ActionSource is the name the Demo Home's actions are registered under.
const ActionSource = "demo"

// fixVerifyTimeout bounds how long a demo fix waits for its incident to
// resolve. The slowest, the PBS prune, takes 22 simulated minutes, and the
// demo runs in real time.
const fixVerifyTimeout = 45 * time.Minute

// Actions are the Demo Home's incident fixes as engine actions, so the demo
// exercises the real plan, approve, task and audit path. There's one Def
// per suggested-action ID the day's script uses (link.open only opens a
// link, so it isn't one), with the simulator's tier:
//
//   - The target is the suggested action's target, and the plan finds the
//     open incident that suggests this action on it. With none, it's
//     refused as a precondition ("nothing open on … needs this").
//   - The label is the suggestion's; the blast radius is the incident's
//     action impact, as the simulator computed it.
//   - Execute presses the fix (Driver.Fix). Verify follows the incident
//     through the scenario's phases, each a task step, until it resolves.
//
// IDs are the simulator's (pve.guest.backup, container.update, …), the ones
// incidents carry, so the web plans exactly what an incident suggests.
func (d *Driver) Actions() []action.Def {
	tiers, order := fixActions(d.opts.Seed)
	defs := make([]action.Def, 0, len(order))
	for _, id := range order {
		defs = append(defs, d.fixDef(id, tiers[id]))
	}
	return defs
}

// fixActions plays the default script's day and collects every suggested
// action ID, with the highest tier it's suggested at.
func fixActions(seed int64) (map[string]sim.Tier, []string) {
	s, err := sim.New(seed)
	if err != nil {
		return nil, nil // New only fails on invalid options, and these are the defaults
	}
	s.Skip(day - time.Second)
	tiers := map[string]sim.Tier{}
	var order []string
	for _, in := range s.Snapshot().Incidents {
		a := in.Suggested
		if a.ID == "" || a.ID == "link.open" {
			continue
		}
		if _, ok := tiers[a.ID]; !ok {
			order = append(order, a.ID)
		}
		tiers[a.ID] = max(tiers[a.ID], a.Tier)
	}
	slices.Sort(order)
	return tiers, order
}

func (d *Driver) fixDef(id string, tier sim.Tier) action.Def {
	t, _ := action.ParseTier(string(tier))
	return action.Def{
		ID:            id,
		Tier:          t,
		VerifyTimeout: fixVerifyTimeout,
		Resolve: func(_ context.Context, target string) (action.Target, error) {
			r, ok := d.t.Graph.Get(graph.RID(target))
			if !ok {
				return action.Target{}, action.ErrNotFound
			}
			name := r.Name
			if name == "" {
				name = target
			}
			in, ok := d.suggesting(id, target)
			if !ok {
				return action.Target{ID: target, Kind: string(r.Kind), Name: name, State: "none"}, nil
			}
			// The incident and whether it's open: a phase change between
			// plan and approve doesn't make the plan wrong.
			return action.Target{ID: target, Kind: string(r.Kind), Name: name, State: in.ID + ":" + in.Status, Ref: in.ID}, nil
		},
		Preconditions: func(_ context.Context, req action.Request) error {
			if req.Target.Ref == "" {
				return action.Precondition(fmt.Sprintf("Nothing open on %s needs this right now.", req.Target.Name))
			}
			if err := d.CanFix(req.Target.Ref); err != nil {
				return action.Precondition(err.Error())
			}
			return nil
		},
		Label: func(req action.Request) string {
			if in, ok := d.incident(req.Target.Ref); ok && in.Suggested.Label != "" {
				return in.Suggested.Label
			}
			return id + " " + req.Target.Name
		},
		Steps: func(req action.Request) []action.Step {
			label := id
			if in, ok := d.incident(req.Target.Ref); ok {
				label = in.Suggested.Label
			}
			return []action.Step{{Label: label}, {Label: "Wait for the incident to resolve"}}
		},
		BlastRadius: func(_ context.Context, req action.Request) []action.Impact {
			in, ok := d.incident(req.Target.Ref)
			if !ok {
				return nil
			}
			out := make([]action.Impact, 0, len(in.Suggested.Impact))
			for _, im := range in.Suggested.Impact {
				out = append(out, action.Impact{Resource: im.Resource, Via: string(im.Via), Depth: im.Depth})
			}
			return out
		},
		Execute: func(_ context.Context, req action.Request, run *action.TaskRun) error {
			if in, ok := d.incident(req.Target.Ref); ok {
				run.Step(in.Suggested.Label)
			}
			if err := d.Fix(req.Target.Ref); err != nil {
				if errors.Is(err, ErrUnknownIncident) {
					return action.Fail("not_found", "That incident is gone.")
				}
				return action.Fail("conflict", err.Error())
			}
			run.Logf("Started at %s (simulated time).", d.Now().Format("15:04:05"))
			return nil
		},
		Verify: func(ctx context.Context, req action.Request, run *action.TaskRun) error {
			return d.awaitResolved(ctx, req.Target.Ref, run)
		},
	}
}

// awaitResolved follows an incident on the inbox until it resolves,
// reporting each phase as a step.
func (d *Driver) awaitResolved(ctx context.Context, id string, run *action.TaskRun) error {
	notify := make(chan struct{}, 1)
	w := d.t.Inbox.Watch(notify, 0)
	defer w.Close()
	last := ""
	for {
		in, ok := d.incident(id)
		switch {
		case !ok:
			return action.Fail("verify_failed", "The incident disappeared before it resolved.")
		case in.Status == sim.StatusResolved:
			run.Progress(1)
			if in.Resolution != "" {
				run.Logf("%s", in.Resolution)
			}
			return nil
		case in.Phase != last:
			last = in.Phase
			run.Step(phaseLabel(in.Phase))
			run.Logf("%s: %s", in.Phase, in.Summary)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-notify:
			w.Snapshot() // discards what's pending, and clears a lag
		}
	}
}

// phaseLabel is a phase as a task step: "pruning" → "Pruning".
func phaseLabel(phase string) string {
	if phase == "" {
		return "Waiting"
	}
	return strings.ToUpper(phase[:1]) + strings.ReplaceAll(phase[1:], "-", " ")
}

// incident reads an incident from the inbox, as published.
func (d *Driver) incident(id string) (sim.Incident, bool) {
	if id == "" {
		return sim.Incident{}, false
	}
	raw := d.t.Inbox.Item(topics.Incidents, id)
	if raw == nil {
		return sim.Incident{}, false
	}
	var in sim.Incident
	if err := json.Unmarshal(raw, &in); err != nil {
		return sim.Incident{}, false
	}
	return in, true
}

// suggesting finds the open incident that suggests action on target, the
// earliest opened if several do.
func (d *Driver) suggesting(action, target string) (sim.Incident, bool) {
	_, doc := d.t.Inbox.Snapshot()
	items, _ := doc[topics.Incidents].(map[string]json.RawMessage)
	var best sim.Incident
	found := false
	for _, raw := range items {
		var in sim.Incident
		if json.Unmarshal(raw, &in) != nil || in.Status != sim.StatusOpen {
			continue
		}
		if in.Suggested.ID != action || in.Suggested.Target != target {
			continue
		}
		if !found || in.OpenedAt.Before(best.OpenedAt) || (in.OpenedAt.Equal(best.OpenedAt) && in.ID < best.ID) {
			best, found = in, true
		}
	}
	return best, found
}
