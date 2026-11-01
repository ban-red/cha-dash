package action

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"dash.cha.sh/internal/audit"
)

// A parent task runs other actions as its steps: each is authorized,
// guarded, locked, verified and audited on its own, under the parent's
// tier, and a failing step's error reaches the parent.
func TestNestedSteps(t *testing.T) {
	var calls []string
	step := reboot(&calls)
	big := Def{ID: "proxmox.guest.delete", Title: "Delete", Targets: []string{"vm"}, Tier: T3,
		Execute: func(context.Context, Request, *TaskRun) error { calls = append(calls, "delete"); return nil }}
	var stepErrs []error
	parent := Def{
		ID: "updates.rollout", Tier: T1, Async: true,
		Resolve: func(_ context.Context, target string) (Target, error) {
			if target != "chadash:updates" {
				return Target{}, ErrNotFound
			}
			return Target{ID: target, Name: "the updates"}, nil
		},
		// Two steps make it T2.
		TierOf: func(context.Context, Request) Tier { return T2 },
		Execute: func(ctx context.Context, r Request, run *TaskRun) error {
			stepErrs = append(stepErrs,
				run.Nested(ctx, r.Actor, "proxmox.guest.reboot", "proxmox:i_x/qemu/201", nil),
				run.Nested(ctx, r.Actor, "proxmox.guest.delete", "proxmox:i_x/qemu/201", nil),
				run.Nested(ctx, r.Actor, "proxmox.guest.reboot", "proxmox:i_x/qemu/999", nil),
			)
			return stepErrs[0]
		},
	}
	e := newEnv(t, OwnerOnly{}, step, big, parent)
	refused := false
	e.guard = func(_ context.Context, r Request) error {
		if r.Action == "proxmox.guest.reboot" && refused {
			return Precondition("held")
		}
		return nil
	}
	p := e.mustPlan(owner, "updates.rollout", "chadash:updates")
	if p.Tier != T2 || p.Confirm != "hold" {
		t.Fatalf("plan = %+v, want T2 from TierOf", p)
	}
	task, err := e.approve(owner, p, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := e.wait(task.ID); got.State != StateSucceeded {
		t.Fatalf("task = %+v", got)
	}
	if strings.Join(calls, ",") != "execute:proxmox:i_x/qemu/201,verify" {
		t.Fatalf("calls = %v", calls)
	}
	if stepErrs[0] != nil {
		t.Fatalf("reboot step = %v", stepErrs[0])
	}
	var fe *Error
	if !errors.As(stepErrs[1], &fe) || fe.Code != "forbidden" || !strings.Contains(fe.Message, "higher than the T2") {
		t.Fatalf("a T3 step under a T2 task = %v", stepErrs[1])
	}
	if !errors.As(stepErrs[2], &fe) || fe.Code != "not_found" {
		t.Fatalf("a missing target = %v", stepErrs[2])
	}
	// Each step has its own record, carrying the parent task.
	var steps []audit.Event
	for _, ev := range e.events() {
		if ev.Task == task.ID && ev.Action != "updates.rollout" {
			steps = append(steps, ev)
		}
	}
	if len(steps) != 3 || steps[0].Result != audit.OK || steps[0].Tier != "T2" || steps[1].Result != audit.Refused || steps[2].Result != audit.Refused {
		t.Fatalf("step records = %+v", steps)
	}

	// The hub's Guard refuses a step as it refuses a plan.
	refused, stepErrs, calls = true, nil, nil
	p = e.mustPlan(owner, "updates.rollout", "chadash:updates")
	task, err = e.approve(owner, p, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := e.wait(task.ID); got.State != StateFailed || got.Error == nil || got.Error.Code != "precondition" {
		t.Fatalf("guarded = %+v", got)
	}
	if len(calls) != 0 {
		t.Fatalf("a guarded step ran: %v", calls)
	}
}

// A plan run on a schedule is authorized again for its maker, without a
// step-up, and never at T4.
func TestRunScheduled(t *testing.T) {
	ran := 0
	t3 := Def{ID: "updates.rollout", Tier: T3,
		Params: rawSchema(`{"type":"object"}`),
		Resolve: func(_ context.Context, target string) (Target, error) {
			return Target{ID: target, Name: "the updates"}, nil
		},
		TierOf: func(_ context.Context, r Request) Tier {
			var p struct{ Tier string }
			_ = json.Unmarshal(r.Params, &p)
			if p.Tier == "T4" {
				return T4
			}
			return T3
		},
		Execute: func(context.Context, Request, *TaskRun) error { ran++; return nil }}
	e := newEnv(t, OwnerOnly{AllowT4: true}, t3)
	scheduled := Actor{Kind: KindUser, ID: owner.ID, Name: owner.Name, Role: "owner", Via: "schedule"}

	task, apv, err := e.e.RunScheduled(t.Context(), scheduled, PlanRequest{Action: "updates.rollout", Target: "chadash:updates"})
	if err != nil || apv != nil {
		t.Fatalf("run = %v %v", apv, err)
	}
	if got := e.wait(task.ID); got.State != StateSucceeded || ran != 1 {
		t.Fatalf("task = %+v, ran %d", got, ran)
	}

	_, _, err = e.e.RunScheduled(t.Context(), scheduled, PlanRequest{Action: "updates.rollout", Target: "chadash:updates", Params: json.RawMessage(`{"tier":"T4"}`)})
	refusal(t, err, "step_up_required")

	demoted := scheduled
	demoted.Role = "operator"
	_, _, err = e.e.RunScheduled(t.Context(), demoted, PlanRequest{Action: "updates.rollout", Target: "chadash:updates"})
	refusal(t, err, "forbidden")
	if ran != 1 {
		t.Fatalf("ran %d times", ran)
	}
}
