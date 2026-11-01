package apidoc

import (
	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/audit"
)

// Mirrors of what internal/action's handlers write.
type (
	taskBody struct {
		Task action.Task `json:"task"`
	}
	// approved is a plan's approval: the task it started, or, when the plan
	// waits for a person, no task and the approval.
	approved struct {
		Task     *action.Task     `json:"task"`
		Approval *action.Approval `json:"approval,omitempty"`
	}
	approvalList struct {
		Approvals []action.Approval `json:"approvals"`
	}
	approvalBody struct {
		Approval action.Approval `json:"approval"`
	}
)

func init() {
	add("actions", map[string]Op{
		"POST /api/actions/plan": {Summary: "Plan an action on a target: what it would do, its tier and its hash.", Body: action.PlanRequest{}, Resp: &action.Plan{}},
		"POST /api/plans/{id}/approve": {Summary: "Approve a plan by its hash and run it (an Idempotency-Key header makes retries safe).", Body: action.ApproveRequest{}, Status: 202, Resp: approved{},
			Errors: []string{"step_up_required", "plan_changed", "plan_expired"}},
		"GET /api/tasks/{id}":         {Summary: "A task: its state, steps and result.", Resp: action.Task{}},
		"POST /api/tasks/{id}/cancel": {Summary: "Cancel a running task: your own, or anyone's with tasks.all.", Status: 202, Resp: taskBody{}},
		"GET /api/actions":            {Summary: "The actions you could plan on a target.", Query: []Param{{Name: "target", Desc: "A resource ID.", Required: true}}, Resp: []action.Offer{}},
		"GET /api/approvals":          {Summary: "Plans waiting for a person: awaiting first, then the last 24 hours.", Resp: approvalList{}},
		"GET /api/approvals/{id}":     {Summary: "One approval; the token that asked may poll it.", Resp: action.Approval{}},
		"POST /api/approvals/{id}/approve": {Summary: "Approve a waiting plan; it runs as the token that asked.", Body: action.ApproveRequest{}, Status: 202, Resp: taskBody{},
			Errors: []string{"step_up_required", "plan_changed", "approval_expired"}},
		"POST /api/approvals/{id}/deny": {Summary: "Deny a waiting plan.", Body: action.DenyRequest{}, Resp: approvalBody{}, Errors: []string{"already_decided", "approval_expired"}},
	})
	add("audit", map[string]Op{
		"GET /api/audit": {Summary: "A page of the audit log, newest first.", Resp: audit.Page{}, Query: []Param{
			{Name: "actor", Desc: "Only this actor's events."}, {Name: "action", Desc: "Only this action."}, {Name: "target", Desc: "Only this target."},
			{Name: "result", Desc: "ok, failed or refused."}, {Name: "since", Desc: "RFC 3339."}, {Name: "before", Desc: "RFC 3339."},
			{Name: "limit", Desc: "How many."}, {Name: "cursor", Desc: "The previous page's cursor."}}},
		"GET /api/audit/export": {Summary: "Every event as NDJSON, oldest first; the chain's head is in X-Chadash-Audit-Head.", Media: "application/x-ndjson"},
		"GET /api/audit/verify": {Summary: "Check the audit log's hash chain.", Resp: audit.Report{}},
	})
}
