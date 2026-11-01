package action

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"dash.cha.sh/internal/audit"
)

// ── steps that are actions ───────────────────────────────────────────────

// Nested runs another registered action as one step of this task: a
// rollout's snapshot, then its update. The step resolves, is authorized
// for actor and checked by its Preconditions and the hub's Guard, runs
// under its own target's lock, and is verified, as a task of its own would
// be; it reports on this task (its steps, progress and log) and is
// audited as its own record carrying this task's ID.
//
// The task's plan was approved at its tier, step-up included, so a step
// may be at most that tier and doesn't ask for a step-up again. It returns
// nil, or an *Error the caller can show as is.
func (r *TaskRun) Nested(ctx context.Context, actor Actor, actionID, target string, params json.RawMessage) error {
	e := r.e
	d, ok := e.reg.Get(actionID)
	if !ok {
		return Fail("unavailable", fmt.Sprintf("There's no action called %q any more.", actionID))
	}
	p, ref := e.params(d, params)
	if ref != nil {
		return e.nestedRefused(ctx, actor, d, target, params, d.Tier, ref, r.id)
	}
	t, err := e.resolve(ctx, d, target)
	if err != nil {
		return e.nestedRefused(ctx, actor, d, target, p, d.Tier, refusalOf(err, "What this step acts on isn't there any more."), r.id)
	}
	tier := d.Tier
	if d.TierFor != nil {
		tier = max(tier, d.TierFor(t))
	}
	req := Request{Action: d.ID, Target: t, Params: p, Actor: actor, Task: r.id}
	if d.TierOf != nil {
		tier = max(tier, d.TierOf(ctx, req))
	}
	if tier > r.tier {
		return e.nestedRefused(ctx, actor, d, t.ID, p, tier,
			&Refusal{Status: http.StatusForbidden, Code: "forbidden", Message: fmt.Sprintf("This step is %s, higher than the %s that was approved. Run it on its own.", tier, r.tier)}, r.id)
	}
	if err := e.policy.Authorize(WithDef(ctx, d), actor, d.scope(), t.ID, tier); err != nil && !errors.Is(err, ErrStepUpRequired) {
		return e.nestedRefused(ctx, actor, d, t.ID, p, tier, forbidden(err), r.id)
	}
	if err := e.preconditions(ctx, d, req); err != nil {
		return e.nestedRefused(ctx, actor, d, t.ID, p, tier, refusalOf(err, ""), r.id)
	}
	if !e.lock(t.ID, r.id) {
		return e.nestedRefused(ctx, actor, d, t.ID, p, tier,
			&Refusal{Status: http.StatusConflict, Code: "locked", Message: "Something else is already running on " + t.Name + "."}, r.id)
	}
	defer e.unlock(t.ID, r.id)

	started := e.now().UTC().Truncate(time.Millisecond)
	err = e.call(ctx, d.timeout(), func(ctx context.Context) error { return d.Execute(ctx, req, r) })
	if err == nil && d.Verify != nil {
		err = e.call(ctx, d.verifyTimeout(), func(ctx context.Context) error { return d.Verify(ctx, req, r) })
		if err != nil && !isCancel(ctx, err) {
			if _, ok := errors.AsType[*Error](err); !ok {
				err = Fail("verify_failed", "It ran, but the check afterwards failed: "+err.Error())
			}
		}
	}
	result := audit.OK
	var fail *Error
	if err != nil {
		result = audit.Failed
		if ctx.Err() != nil {
			fail = Fail("cancelled", "Stopped before it finished.")
		} else {
			fail = errorOf(err)
		}
	}
	var auditErr *audit.Error
	if fail != nil {
		auditErr = &audit.Error{Code: fail.Code, Message: fail.Message}
	}
	var secrets []string
	if d.Params != nil {
		secrets = d.Params.Secrets()
	}
	if _, aerr := e.audit.Append(context.WithoutCancel(ctx), audit.Entry{
		Actor: auditActor(actor), Action: d.ID, Target: t.ID, Params: p, Secrets: secrets,
		Tier: tier.String(), Result: result, Error: auditErr, Duration: e.now().Sub(started), Task: r.id,
	}); aerr != nil {
		e.log.Error("action: auditing a step failed", "task", r.id, "action", d.ID, "err", aerr)
	}
	if fail != nil {
		return fail
	}
	return nil
}

// nestedRefused audits a step that was refused before it ran and returns
// the refusal as the step's error.
func (e *Engine) nestedRefused(ctx context.Context, a Actor, d *Def, target string, params json.RawMessage, tier Tier, ref *Refusal, task string) error {
	if !json.Valid(params) {
		params = nil
	}
	var secrets []string
	if d.Params != nil {
		secrets = d.Params.Secrets()
	}
	if _, err := e.audit.Append(context.WithoutCancel(ctx), audit.Entry{
		Actor: auditActor(a), Action: d.ID, Target: clip(target, 500), Params: auditParams(params), Secrets: secrets,
		Tier: tier.String(), Result: audit.Refused, Error: &audit.Error{Code: ref.Code, Message: ref.Message}, Task: task,
	}); err != nil {
		e.log.Error("action: auditing a refused step failed", "task", task, "action", d.ID, "err", err)
	}
	return Fail(ref.Code, ref.Message)
}

// ── plans a schedule runs ────────────────────────────────────────────────

// RunScheduled plans req as a and runs it at once, for a person who
// approved it earlier and isn't here now: a rollout scheduled into a
// maintenance window. The caller vouches that a approved this at its
// tier, with the step-up that tier needed then, so the policy is asked
// again (a demoted or disabled user is refused) but a missing step-up
// isn't. T4 is never run this way. A plan the policy says waits for a
// person (an API token's) is submitted as an approval instead; exactly
// one of the task and the approval is set when err is nil. Refusals are
// *Refusal and are audited, as Plan's are.
func (e *Engine) RunScheduled(ctx context.Context, a Actor, req PlanRequest) (Task, *Approval, error) {
	p, err := e.Plan(ctx, a, req)
	if err != nil {
		return Task{}, nil, err
	}
	sp, err := e.loadPlan(ctx, p.ID)
	if err != nil {
		return Task{}, nil, err
	}
	d, ok := e.reg.Get(sp.action)
	if !ok {
		return Task{}, nil, e.refuse(ctx, a, sp.action, sp.target, sp.params, nil, sp.tier.String(),
			&Refusal{Status: http.StatusConflict, Code: "unavailable", Message: "This action isn't available any more."})
	}
	if sp.tier == T4 {
		return Task{}, nil, e.refuse(ctx, a, d.ID, sp.target, sp.params, d, sp.tier.String(),
			&Refusal{Status: http.StatusForbidden, Code: "step_up_required", Message: "A T4 action never runs on a schedule. Run it yourself."})
	}
	t, err := e.resolve(ctx, d, sp.target)
	if err != nil {
		return Task{}, nil, e.refuse(ctx, a, d.ID, sp.target, sp.params, d, sp.tier.String(), refusalOf(err, "That target doesn't exist any more."))
	}
	if e.needsApproval(a, sp) {
		apv, err := e.submit(ctx, a, sp, t)
		return Task{}, apv, err
	}
	task, ref, err := e.launch(ctx, a, sp, d, t, "", nil, nil, nil)
	if ref != nil {
		return Task{}, nil, e.refuse(ctx, a, d.ID, t.ID, sp.params, d, sp.tier.String(), ref)
	}
	return task, nil, err
}
