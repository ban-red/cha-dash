package updates

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"

	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/msg"
)

// The rollout planner (docs/plan/14-v0.4.md, "Rollouts"): "Update all", or
// a selection, becomes the steps of one plan.
//
//  1. Order: the graph's depends_on, dependencies first (a database before
//     the app that uses it). A cycle, or a dependency on something the
//     graph doesn't have, falls back to the list's order for the lot.
//  2. Snapshot first, per step, where one is possible: a container of an
//     owned stack in a node's root has its git commit already (git); a
//     container on a PVE guest the correlator matched (same_as), with a
//     Proxmox control token that offers proxmox.guest.snapshot.create,
//     gets a snapshot of that guest (pve), once per guest; otherwise none,
//     and the step says so.
//  3. Each step is the row's own update action: docker.container.update
//     (pull, recreate, the health gate, a rollback on its own) or
//     unraid.container.update. A container in an owned stack is updated
//     the same way: a stack deploy needs a changed draft, and the stack's
//     commit is already its snapshot.
//  4. Held and snoozed containers, rows Cha Dash can't update, and steps
//     that would be T4 (they run Cha Dash itself) are left out and listed.
//
// The plan's hash covers the steps in order, each with its snapshot and the
// digest it updates to, so a newer image or a changed order is a changed
// plan.

// RolloutPlan is what a rollout would do now.
type RolloutPlan struct {
	// Steps run in order; skipped ones follow, listed with their reason.
	Steps []RolloutStep `json:"steps"`
	Hash  string        `json:"hash"`
	// Tier is the highest of the steps' tiers (T2 for a container).
	Tier action.Tier `json:"tier"`
	// Ordered: false when depends_on couldn't decide (a cycle, or an
	// unknown dependency) and the steps keep the list's order.
	Ordered bool `json:"ordered"`
}

// Runnable is the steps that will run, in order.
func (p RolloutPlan) Runnable() []RolloutStep {
	var out []RolloutStep
	for _, s := range p.Steps {
		if s.State != StepSkipped {
			out = append(out, s)
		}
	}
	return out
}

// snapshotAction is the PVE action a step's snapshot runs.
const snapshotAction = "proxmox.guest.snapshot.create"

// Plan plans a rollout of targets (container resource IDs), or of every
// available update when targets is empty.
func (s *Service) Plan(ctx context.Context, targets []string) (RolloutPlan, error) {
	rows, err := s.List(ctx)
	if err != nil {
		return RolloutPlan{}, err
	}
	_, doc := s.g.Snapshot(nil)
	want := map[string]bool{}
	for _, t := range targets {
		want[t] = true
	}
	var run, skip []RolloutStep
	found := map[string]bool{}
	for _, r := range rows {
		if len(want) > 0 && !want[r.Resource] {
			continue
		}
		if len(want) == 0 && r.State != integration.UpdateAvailable && r.State != integration.UpdateHeld {
			continue
		}
		found[r.Resource] = true
		st := RolloutStep{Target: r.Resource, Name: r.Name, Host: r.Host, To: r.Latest, State: StepPending}
		if r.Update != nil {
			st.Action = r.Update.ID
		}
		switch {
		case r.Kind != KindImage:
			st.State, st.Reason = StepSkipped, msg.New("updates.skip_host_packages", "name", r.Name).Text()
		case r.State == integration.UpdateHeld && r.Hold != nil && r.Hold.Kind == KindSnooze:
			st.State, st.Reason = StepSkipped, msg.New("updates.skip_snoozed", "name", r.Name).Text()
		case r.State == integration.UpdateHeld:
			st.State, st.Reason = StepSkipped, msg.New("updates.skip_held", "name", r.Name).Text()
		case r.State != integration.UpdateAvailable:
			st.State, st.Reason = StepSkipped, msg.New("updates.skip_current", "name", r.Name).Text()
		case r.Update == nil:
			st.State, st.Reason = StepSkipped, r.Why
		case s.stepTier(doc, st) == action.T4:
			st.State, st.Reason = StepSkipped, msg.New("updates.skip_runs_hub", "name", r.Name).Text()
		}
		if st.State == StepSkipped {
			skip = append(skip, st)
			continue
		}
		run = append(run, st)
	}
	for _, t := range targets {
		if !found[t] && !slices.ContainsFunc(skip, func(x RolloutStep) bool { return x.Target == t }) {
			skip = append(skip, RolloutStep{Target: t, Name: t, State: StepSkipped, Reason: msg.New("updates.skip_unknown").Text()})
			found[t] = true
		}
	}
	run, ordered := orderSteps(doc, run)
	s.snapshots(ctx, doc, rows, run)
	p := RolloutPlan{Steps: append(run, skip...), Tier: action.T0, Ordered: ordered}
	for _, st := range run {
		p.Tier = max(p.Tier, s.stepTier(doc, st))
		if st.Snapshot == SnapshotPVE {
			p.Tier = max(p.Tier, action.T1)
		}
	}
	p.Hash = planHash(p.Steps)
	return p, nil
}

// planHash is a rollout plan's hash: its steps in order, with their
// snapshots, targets' digests and states.
func planHash(steps []RolloutStep) string {
	type h struct {
		Target, Action, Snapshot, Guest, To string
		State                               StepState
	}
	hs := make([]h, len(steps))
	for i, s := range steps {
		hs[i] = h{s.Target, s.Action, s.Snapshot, s.Guest, s.To, s.State}
	}
	b, _ := json.Marshal(hs)
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// stepTier is the tier a step's update would plan at: its Def's tier,
// raised for its target as planning would (the self-aware T4).
func (s *Service) stepTier(doc graph.Doc, st RolloutStep) action.Tier {
	tier := action.T2
	if s.reg == nil || st.Action == "" {
		return tier
	}
	d, ok := s.reg.Get(st.Action)
	if !ok {
		return tier
	}
	tier = d.Tier
	if d.TierFor != nil {
		t := action.Target{ID: st.Target, Name: st.Name}
		if r := doc.Resources[graph.RID(st.Target)]; r != nil {
			t.Kind = string(r.Kind)
		}
		tier = max(tier, d.TierFor(t))
	}
	return tier
}

// orderSteps puts dependencies first, by the graph's depends_on between
// the steps' containers (and anything same_as them), keeping the list's
// order where depends_on says nothing. A cycle, or a step that depends on
// something the graph doesn't have, keeps the list's order for every step,
// and ordered is false.
func orderSteps(doc graph.Doc, steps []RolloutStep) ([]RolloutStep, bool) {
	if len(steps) < 2 {
		return steps, true
	}
	canon := aliases(doc)
	index := map[graph.RID]int{} // a step's container (canonical) → its position
	for i, st := range steps {
		index[canon(graph.RID(st.Target))] = i
	}
	deps := make([][]int, len(steps)) // i depends on deps[i]
	for _, rel := range doc.Relations {
		if rel.Type != "depends_on" {
			continue
		}
		from, ok := index[canon(rel.From)]
		if !ok {
			continue
		}
		if doc.Resources[rel.To] == nil {
			return steps, false // an unknown dependency
		}
		to, ok := index[canon(rel.To)]
		if !ok || to == from {
			continue
		}
		if !slices.Contains(deps[from], to) {
			deps[from] = append(deps[from], to)
		}
	}
	// Kahn's algorithm, always taking the first step (in the list's order)
	// whose dependencies have all been placed.
	placed := make([]bool, len(steps))
	out := make([]RolloutStep, 0, len(steps))
	for len(out) < len(steps) {
		next := -1
		for i := range steps {
			if placed[i] {
				continue
			}
			ready := true
			for _, d := range deps[i] {
				if !placed[d] {
					ready = false
					break
				}
			}
			if ready {
				next = i
				break
			}
		}
		if next < 0 {
			return steps, false // a cycle
		}
		placed[next] = true
		out = append(out, steps[next])
	}
	return out, true
}

// aliases maps a resource to one ID for everything same_as it.
func aliases(doc graph.Doc) func(graph.RID) graph.RID {
	parent := map[graph.RID]graph.RID{}
	var find func(graph.RID) graph.RID
	find = func(x graph.RID) graph.RID {
		p, ok := parent[x]
		if !ok || p == x {
			return x
		}
		r := find(p)
		parent[x] = r
		return r
	}
	for _, rel := range doc.Relations {
		if rel.Type != "same_as" {
			continue
		}
		a, b := find(rel.From), find(rel.To)
		if a != b {
			// The smaller ID represents the set, so the answer is stable.
			if b < a {
				a, b = b, a
			}
			parent[b] = a
		}
	}
	return find
}

// members is everything same_as id, id included.
func members(doc graph.Doc, id graph.RID) []graph.RID {
	canon := aliases(doc)
	c := canon(id)
	out := []graph.RID{id}
	for rid := range doc.Resources {
		if rid != id && canon(rid) == c {
			out = append(out, rid)
		}
	}
	slices.Sort(out[1:])
	return out
}

// snapshots fills each step's snapshot: git for an owned stack's
// container, pve for a container on a guest a Proxmox control token can
// snapshot, else none with the reason.
func (s *Service) snapshots(ctx context.Context, doc graph.Doc, rows []Row, steps []RolloutStep) {
	byID := map[string]Row{}
	for _, r := range rows {
		byID[r.Resource] = r
	}
	owned := map[string]bool{} // node + project → owned
	for i := range steps {
		st := &steps[i]
		r := byID[st.Target]
		if r.Stack != "" && s.ownedStack != nil {
			node := ""
			if e := doc.Resources[graph.RID(r.Engine)]; e != nil {
				node = stringAttr(e.Attrs, "node")
			}
			if node != "" {
				key := node + "\x00" + r.Stack
				ok, seen := owned[key]
				if !seen {
					ok = s.ownedStack(ctx, node, r.Stack)
					owned[key] = ok
				}
				if ok {
					st.Snapshot = SnapshotGit
					continue
				}
			}
		}
		if guest := guestOf(doc, graph.RID(r.Engine)); guest != "" && s.canAct != nil && s.canAct(snapshotAction, string(guest)) {
			st.Snapshot, st.Guest = SnapshotPVE, string(guest)
		}
	}
}

// guestKinds are Proxmox's guest resource kinds.
var guestKinds = []graph.Kind{"vm", "lxc"}

// guestOf finds the Proxmox guest an engine runs on: what the engine (or
// anything same_as it) runs_on, or anything same_as that, of a guest kind
// and from Proxmox.
func guestOf(doc graph.Doc, engine graph.RID) graph.RID {
	if engine == "" {
		return ""
	}
	for _, e := range members(doc, engine) {
		for _, rel := range doc.Relations {
			if rel.Type != "runs_on" || rel.From != e {
				continue
			}
			for _, m := range members(doc, rel.To) {
				if r := doc.Resources[m]; r != nil && slices.Contains(guestKinds, r.Kind) && strings.HasPrefix(string(m), "proxmox:") {
					return m
				}
			}
		}
	}
	return ""
}
