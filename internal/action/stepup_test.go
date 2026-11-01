package action

import (
	"encoding/json"
	"testing"
	"time"

	"dash.cha.sh/internal/audit"
)

// TestApprovalRecordsStepUpMethod: an approval granted with a step-up
// keeps how the approver confirmed it was them, in the decision, the task,
// the audit record and the Inbox item's resolution (ADR-0017's approved_by
// method).
func TestApprovalRecordsStepUpMethod(t *testing.T) {
	var calls []string
	e, _ := newTokenEnv(t, reboot(&calls))
	p := e.mustPlan(token, "proxmox.guest.reboot", "proxmox:i_x/qemu/201")
	_, apv, err := e.e.ApproveOrSubmit(t.Context(), token, p.ID, ApproveRequest{Hash: p.Hash}, "")
	if err != nil || apv == nil {
		t.Fatalf("submit: %+v %v", apv, err)
	}
	e.clock.Advance(time.Minute)
	approver := stepUpAt(owner, e.clock.Now())
	approver.StepUpMethod = "passkey"
	task, err := e.e.Grant(t.Context(), approver, apv.ID, ApproveRequest{Hash: p.Hash})
	if err != nil {
		t.Fatal(err)
	}
	if done := e.wait(task.ID); done.ApprovedBy == nil || done.ApprovedBy.StepUp != "passkey" {
		t.Fatalf("task approved by %+v", done.ApprovedBy)
	}
	got, _ := e.e.Approval(t.Context(), owner, apv.ID)
	if got.DecidedBy == nil || got.DecidedBy.StepUp != "passkey" {
		t.Fatalf("decided by %+v", got.DecidedBy)
	}
	if it := e.item(apv.ID); it["resolution"] != "Approved by sam, confirmed with a passkey." {
		t.Fatalf("resolution %v", it["resolution"])
	}
	if params := approveParams(t, e); params["step_up"] != "passkey" {
		t.Fatalf("audit params %v", params)
	}
}

// TestDenialRecordsNoStepUp: saying no needs no step-up, so none is kept
// even when the person's session has one.
func TestDenialRecordsNoStepUp(t *testing.T) {
	var calls []string
	e, _ := newTokenEnv(t, reboot(&calls))
	p := e.mustPlan(token, "proxmox.guest.reboot", "proxmox:i_x/qemu/201")
	_, apv, err := e.e.ApproveOrSubmit(t.Context(), token, p.ID, ApproveRequest{Hash: p.Hash}, "")
	if err != nil || apv == nil {
		t.Fatalf("submit: %+v %v", apv, err)
	}
	denier := stepUpAt(owner, e.clock.Now())
	denier.StepUpMethod = "password"
	if _, err := e.e.Deny(t.Context(), denier, apv.ID, DenyRequest{}); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.e.Approval(t.Context(), owner, apv.ID); got.DecidedBy == nil || got.DecidedBy.StepUp != "" {
		t.Fatalf("decided by %+v", got.DecidedBy)
	}
}

// approveParams are the params of the last approval.approve that went
// through.
func approveParams(t *testing.T, e *env) map[string]string {
	t.Helper()
	var params map[string]string
	for _, ev := range e.events() {
		if ev.Action == ActionApprovalApprove && ev.Result == audit.OK {
			if err := json.Unmarshal(ev.Params, &params); err != nil {
				t.Fatal(err)
			}
		}
	}
	return params
}
