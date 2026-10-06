package action

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"slices"
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

// ActionsFor lists the actions the actor could plan on target, by label. It
// runs the first half of planning for each registered action, without
// saving or auditing anything (it's a read):
//
//   - an action that doesn't apply to the target (Resolve answers not
//     found, or the resource's kind isn't one of Targets) is left out;
//   - so is one the policy refuses the actor outright. One that only needs
//     a step-up is listed: planning it asks for one;
//   - a precondition refusal ("pluto is already stopped") leaves it out,
//     since there's nothing to offer; an unavailable refusal lists it with
//     Available false and the sentence, so the page can say why.
//
// Preconditions should be cheap reads; they run for every action on every
// listing.
func (e *Engine) ActionsFor(ctx context.Context, a Actor, target string) ([]Offer, error) {
	out := []Offer{}
	if target == "" {
		return out, nil
	}
	for _, id := range e.reg.IDs() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		d, ok := e.reg.Get(id)
		if !ok {
			continue
		}
		t, err := e.resolve(ctx, d, target)
		if err != nil {
			continue
		}
		tier := d.Tier
		if d.TierFor != nil {
			tier = max(tier, d.TierFor(t))
		}
		if err := e.policy.Authorize(WithDef(ctx, d), a, d.scope(), t.ID, tier); err != nil && !errors.Is(err, ErrStepUpRequired) {
			continue
		}
		r := Request{Action: d.ID, Target: t, Params: emptyParams, Actor: a}
		o := Offer{ID: d.ID, Label: labelOf(d, r), Tier: tier, Available: true, Household: d.Household}
		if d.Preconditions != nil {
			if err := d.Preconditions(ctx, r); err != nil {
				ref := refusalOf(err, "")
				if ref.Code != "unavailable" {
					continue
				}
				o.Available, o.Unavailable, o.Recipe = false, ref.Message, ref.Recipe
			}
		}
		out = append(out, o)
	}
	slices.SortStableFunc(out, func(x, y Offer) int { return cmp.Compare(x.Label, y.Label) })
	return out, nil
}

var emptyParams = json.RawMessage("{}")
