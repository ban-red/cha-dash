package action

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"time"
)

// Offer is one action the hub would plan on a target, as GET
// /api/actions?target= lists it for ⌘K and tiles.
type Offer struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Tier  Tier   `json:"tier"`
	// Available is false when planning it would answer unavailable (no
	// control credential, a scope missing from a key); Unavailable is the
	// sentence, and Recipe the instance whose recipe grants it.
	Available   bool   `json:"available"`
	Unavailable string `json:"unavailable,omitempty"`
	Recipe      string `json:"recipe,omitempty"`
	// Household marks an action the household role may run.
	Household bool `json:"household,omitempty"`
}

// Listing limits. They're variables so tests can shorten them.
var (
	// listDeadline bounds one listing: whatever hasn't been read by then
	// is listed as unavailable with listTimedOut.
	listDeadline = 3 * time.Second
	// listParallel is how many actions a listing resolves at once.
	listParallel = 4
)

// listTimedOut is the sentence for an action whose reads didn't finish
// before listDeadline.
const listTimedOut = "Couldn't read its current state in time."

// ActionsFor lists the actions the actor could plan on target, by label. It
// runs the first half of planning for each registered action, without
// saving or auditing anything (it's a read):
//
//   - one the policy refuses the actor outright, at the action's own tier,
//     is left out before anything is read. One that only needs a step-up
//     is listed: planning it asks for one;
//   - an action that doesn't apply to the target (Resolve answers not
//     found, or the resource's kind isn't one of Targets) is left out;
//   - a precondition refusal ("pluto is already stopped") leaves it out,
//     since there's nothing to offer; an unavailable refusal lists it with
//     Available false and the sentence, so the page can say why.
//
// Resolve and Preconditions run listParallel at a time, all within
// listDeadline. A read that fails or doesn't finish in time lists the
// action as unavailable (when the Home Graph says it applies to the
// target) rather than holding up the rest. Defs that read the same thing
// share it through Memo.
func (e *Engine) ActionsFor(ctx context.Context, a Actor, target string) ([]Offer, error) {
	out := []Offer{}
	if target == "" {
		return out, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// The static check first: an action the actor's role can't run at its
	// own tier isn't resolved at all. TierFor only ever raises the tier.
	var defs []*Def
	for _, id := range e.reg.IDs() {
		d, ok := e.reg.Get(id)
		if ok && e.allowed(ctx, a, d, target, d.Tier) {
			defs = append(defs, d)
		}
	}
	if len(defs) == 0 {
		return out, nil
	}

	lctx, cancel := context.WithTimeout(ctx, listDeadline)
	defer cancel()
	lctx = context.WithValue(lctx, memoKey{}, &memo{m: map[string]*memoEntry{}})

	slots := make([]*listSlot, len(defs))
	sem := make(chan struct{}, max(listParallel, 1))
	var wg sync.WaitGroup
	// Actions start in registry order as slots free up; past the deadline
	// the rest don't start at all.
start:
	for i, d := range defs {
		s := &listSlot{}
		slots[i] = s
		select {
		case sem <- struct{}{}:
		case <-lctx.Done():
			break start
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			e.offer(lctx, a, d, target, s)
		}()
	}
	for i := range slots {
		if slots[i] == nil {
			slots[i] = &listSlot{}
		}
	}
	all := make(chan struct{})
	go func() { wg.Wait(); close(all) }()
	select {
	case <-all:
	case <-lctx.Done():
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	for i, d := range defs {
		if o, ok := e.settle(ctx, a, d, target, slots[i]); ok {
			out = append(out, o)
		}
	}
	slices.SortStableFunc(out, func(x, y Offer) int { return cmp.Compare(x.Label, y.Label) })
	return out, nil
}

// allowed asks the policy whether the actor may plan d on target at tier;
// a step-up counts as yes (planning asks for it).
func (e *Engine) allowed(ctx context.Context, a Actor, d *Def, target string, tier Tier) bool {
	err := e.policy.Authorize(WithDef(ctx, d), a, d.scope(), target, tier)
	return err == nil || errors.Is(err, ErrStepUpRequired)
}

// listSlot is one action's progress through a listing. The listing reads
// it once its deadline has passed, while a hook that ignores its context
// may still be running, hence the lock.
type listSlot struct {
	mu       sync.Mutex
	resolved bool   // Resolve answered with a target
	target   Target // when resolved
	tier     Tier   // when resolved
	failed   error  // Resolve's error, other than not found
	late     bool   // failed after the deadline (likely because of it)
	finished bool   // the offer is decided
	keep     bool   // when finished: list it
	offer    Offer  // when keep
}

// offer runs Resolve, the policy and Preconditions for d, recording each
// step in s as it goes.
func (e *Engine) offer(ctx context.Context, a Actor, d *Def, target string, s *listSlot) {
	t, err := e.resolve(ctx, d, target)
	if err != nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		if errors.Is(err, ErrNotFound) && ctx.Err() == nil {
			s.finished = true // doesn't apply here
			return
		}
		s.failed, s.late = err, ctx.Err() != nil
		return
	}
	tier := d.Tier
	if d.TierFor != nil {
		tier = max(tier, d.TierFor(t))
	}
	s.mu.Lock()
	s.resolved, s.target, s.tier = true, t, tier
	s.mu.Unlock()

	keep := e.allowed(ctx, a, d, t.ID, tier)
	r := Request{Action: d.ID, Target: t, Params: emptyParams, Actor: a}
	o := Offer{ID: d.ID, Label: labelOf(d, r), Tier: tier, Available: true, Household: d.Household}
	if keep && d.Preconditions != nil {
		if err := d.Preconditions(ctx, r); err != nil {
			ref := refusalOf(err, "")
			switch {
			case ref.Code == "precondition":
				keep = false
			case ctx.Err() != nil:
				return // settle says it timed out
			case ref.Code != "unavailable":
				keep = false
			default:
				o.Available, o.Unavailable, o.Recipe = false, ref.Message, ref.Recipe
			}
		}
	}
	s.mu.Lock()
	s.finished, s.keep, s.offer = true, keep, o
	s.mu.Unlock()
}

// settle turns a slot into the offer to list, if any, once the listing's
// work has finished or its deadline has passed.
func (e *Engine) settle(ctx context.Context, a Actor, d *Def, target string, s *listSlot) (Offer, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finished {
		return s.offer, s.keep
	}
	unavailable := func(t Target, tier Tier, msg, recipe string) Offer {
		r := Request{Action: d.ID, Target: t, Params: emptyParams}
		return Offer{ID: d.ID, Label: labelOf(d, r), Tier: tier, Unavailable: msg, Recipe: recipe, Household: d.Household}
	}
	if s.resolved {
		// Resolved, but Preconditions didn't answer in time. The policy
		// check on the resolved target may not have run either: ask now
		// (it doesn't read the upstream).
		if !e.allowed(ctx, a, d, s.target.ID, s.tier) {
			return Offer{}, false
		}
		return unavailable(s.target, s.tier, listTimedOut, ""), true
	}
	// Resolve failed or didn't answer. List it only when the Home Graph
	// says it applies to the target, so a slow source doesn't fill the
	// list with actions for other kinds of resource.
	t, err := graphTarget(e.graph, d, target)
	if err != nil {
		return Offer{}, false
	}
	msg, recipe := listTimedOut, ""
	if s.failed != nil && !s.late && !errors.Is(s.failed, context.DeadlineExceeded) {
		if ref := refusalOf(s.failed, ""); ref.Code == "unavailable" {
			msg, recipe = ref.Message, ref.Recipe
		}
	}
	return unavailable(t, d.Tier, msg, recipe), true
}

var emptyParams = json.RawMessage("{}")

// ── the per-listing memo ─────────────────────────────────────────────────

type memoKey struct{}

type memo struct {
	mu sync.Mutex
	m  map[string]*memoEntry
}

type memoEntry struct {
	done chan struct{}
	v    any
	err  error
}

// Memo runs fn once per key within one ActionsFor listing and hands every
// caller the same answer, so Defs that read the same thing (a source's
// list of guests, one guest's status) read it once per listing rather than
// once per action. Outside a listing (planning, approval, execution) it
// simply calls fn. Keys must be unique across sources: start them with the
// source and instance ("proxmox/<instance>/cluster/resources"). Callers
// sharing a key must share T.
func Memo[T any](ctx context.Context, key string, fn func(context.Context) (T, error)) (T, error) {
	m, _ := ctx.Value(memoKey{}).(*memo)
	if m == nil {
		return fn(ctx)
	}
	m.mu.Lock()
	en, ok := m.m[key]
	if !ok {
		en = &memoEntry{done: make(chan struct{})}
		m.m[key] = en
		m.mu.Unlock()
		func() {
			defer close(en.done)
			en.v, en.err = fn(ctx)
		}()
	} else {
		m.mu.Unlock()
		select {
		case <-en.done:
		case <-ctx.Done():
			var zero T
			return zero, ctx.Err()
		}
	}
	v, ok := en.v.(T)
	if !ok && en.v != nil {
		return fn(ctx) // a key shared across types: don't guess
	}
	return v, en.err
}
