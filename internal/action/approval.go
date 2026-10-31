package action

// Plans that wait for a person (03 · Architecture → Action engine, Approve;
// ADR-0017). When the policy says a plan's maker can't run it alone
// (ApprovalPolicy: an API token's plans from T2 up, and later every agent's
// plan), or the plan was made with RequireApproval (a git sync's "draft and
// ask"), approving it doesn't run it: it becomes an Approval, awaiting a
// person. The plan is unchanged and keeps its hash; its actor is the
// originator. An Inbox item (class "approval") shows it to approvers, who
// approve it in a session, with a step-up made after it was asked, bound to
// the plan's hash; or deny it. Unanswered, it expires after ApprovalTTL.
// The task that an approval starts runs as the originator, with ApprovedBy
// naming the person. Every step is audited: approval.request (by the
// originator), approval.approve and approval.deny (by the person, refusals
// included) and approval.expire (by the hub).

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"dash.cha.sh/internal/audit"
	"dash.cha.sh/internal/livedoc"
)

// ApprovalTTL is how long a plan waits for a person by default.
const ApprovalTTL = time.Hour

// Approval states.
const (
	ApprovalAwaiting = "awaiting_approval"
	ApprovalApproved = "approved"
	ApprovalDenied   = "denied"
	ApprovalExpired  = "expired"
)

// The audit log's names for an approval's steps.
const (
	ActionApprovalRequest = "approval.request"
	ActionApprovalApprove = "approval.approve"
	ActionApprovalDeny    = "approval.deny"
	ActionApprovalExpire  = "approval.expire"
)

// ApprovalClass is the Inbox item's class for a plan awaiting a person.
const ApprovalClass = "approval"

// Originator is who made a plan that waits, as approvals show it: the
// token (agent, sync) and the user it acts for, or the person. Never a
// session or an address.
type Originator struct {
	Kind     string `json:"kind"`
	ID       string `json:"id"`
	Name     string `json:"name"`
	User     string `json:"user,omitempty"`
	UserName string `json:"user_name,omitempty"`
}

func originatorOf(a Actor) Originator {
	return Originator{Kind: a.Kind, ID: a.ID, Name: a.Name, User: a.User, UserName: a.UserName}
}

// Approval is a plan waiting for a person, or the decision on one: the
// shape of GET /api/approvals/{id} and of the Inbox item's "approval".
type Approval struct {
	ID          string     `json:"id"`
	State       string     `json:"state"` // awaiting_approval | approved | denied | expired
	Plan        Plan       `json:"plan"`  // as shown; approving must echo its hash
	Originator  Originator `json:"originator"`
	RequestedAt time.Time  `json:"requested_at"`
	ExpiresAt   time.Time  `json:"expires_at"`
	DecidedAt   *time.Time `json:"decided_at"`
	DecidedBy   *TaskActor `json:"decided_by"`
	// Reason says why it was denied or expired.
	Reason string  `json:"reason,omitempty"`
	Task   *string `json:"task"` // the task an approval started
}

// DenyRequest is the body of POST /api/approvals/{id}/deny.
type DenyRequest struct {
	Reason string `json:"reason,omitempty"`
}

// stored is an approval with what the engine needs beside its shape.
type storedApproval struct {
	Approval
	originator Actor
	plan       *storedPlan
}

const approvalQuery = `SELECT id, plan_id, originator, state, reason, created_at, expires_at, decided_at, decided_by, task_id FROM approvals`

func (e *Engine) scanApproval(ctx context.Context, row interface{ Scan(...any) error }) (*storedApproval, error) {
	var a storedApproval
	var planID, orig string
	var created, expires int64
	var decided sql.NullInt64
	var by, task sql.NullString
	if err := row.Scan(&a.ID, &planID, &orig, &a.State, &a.Reason, &created, &expires, &decided, &by, &task); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(orig), &a.originator); err != nil {
		return nil, err
	}
	p, err := e.loadPlan(ctx, planID)
	if err != nil {
		return nil, err
	}
	a.plan = p
	a.Plan = p.doc
	a.Originator = originatorOf(a.originator)
	a.RequestedAt, a.ExpiresAt = time.UnixMilli(created).UTC(), time.UnixMilli(expires).UTC()
	a.Plan.ExpiresAt = a.ExpiresAt
	if decided.Valid {
		t := time.UnixMilli(decided.Int64).UTC()
		a.DecidedAt = &t
	}
	if by.Valid {
		var who Actor
		if json.Unmarshal([]byte(by.String), &who) == nil {
			a.DecidedBy = &TaskActor{Kind: who.Kind, Name: who.Name}
		}
	}
	if task.Valid && task.String != "" {
		a.Task = &task.String
	}
	return &a, nil
}

func (e *Engine) loadApproval(ctx context.Context, id string) (*storedApproval, error) {
	return e.scanApproval(ctx, e.st.Reader().QueryRowContext(ctx, approvalQuery+` WHERE id = ?`, id))
}

// approvalOfPlan returns the approval a plan became, or sql.ErrNoRows.
func (e *Engine) approvalOfPlan(ctx context.Context, planID string) (Approval, error) {
	var id string
	if err := e.st.Reader().QueryRowContext(ctx, `SELECT id FROM approvals WHERE plan_id = ?`, planID).Scan(&id); err != nil {
		return Approval{}, err
	}
	a, err := e.freshApproval(ctx, id)
	if err != nil {
		return Approval{}, err
	}
	return a.Approval, nil
}

// freshApproval loads an approval, expiring it first if its time is up.
func (e *Engine) freshApproval(ctx context.Context, id string) (*storedApproval, error) {
	a, err := e.loadApproval(ctx, id)
	if err != nil {
		return nil, err
	}
	if a.State == ApprovalAwaiting && !e.now().Before(a.ExpiresAt) {
		e.expire(ctx, a, "Nobody approved it in time.")
		return e.loadApproval(ctx, id)
	}
	return a, nil
}

// submit turns an approved-by-its-maker plan into an approval awaiting a
// person: stored, audited, in the Inbox, and timed to expire.
func (e *Engine) submit(ctx context.Context, a Actor, p *storedPlan, t Target) (*Approval, error) {
	now := e.now().UTC().Truncate(time.Millisecond)
	apv := Approval{
		ID: "apv_" + strings.ToLower(rand.Text()), State: ApprovalAwaiting, Plan: p.doc,
		Originator: originatorOf(a), RequestedAt: now, ExpiresAt: now.Add(e.approvalTTL),
	}
	apv.Plan.ExpiresAt = apv.ExpiresAt
	orig, _ := json.Marshal(a)
	err := e.st.WriteTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO approvals (id, plan_id, originator_id, originator, state, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			apv.ID, p.id, a.ID, string(orig), apv.State, now.UnixMilli(), apv.ExpiresAt.UnixMilli()); err != nil {
			return err
		}
		// The plan stays approvable (by a person) as long as the approval waits.
		_, err := tx.ExecContext(ctx, `UPDATE plans SET expires_at = ? WHERE id = ?`, apv.ExpiresAt.UnixMilli(), p.id)
		return err
	})
	if err != nil {
		// Two requests for the same plan at once: the other one won.
		if other, oerr := e.approvalOfPlan(ctx, p.id); oerr == nil {
			return &other, nil
		}
		return nil, fmt.Errorf("action: submit plan for approval: %w", err)
	}
	e.recordApproval(ctx, a, ActionApprovalRequest, &apv, p, audit.OK, nil, "")
	e.publishApproval(apv, t.ID)
	e.schedule(apv.ID, apv.ExpiresAt)
	return &apv, nil
}

// Approval returns one approval: to the actor that asked, or to anyone the
// policy lets decide approvals. Otherwise ErrNotFound, so IDs don't leak.
func (e *Engine) Approval(ctx context.Context, a Actor, id string) (Approval, error) {
	apv, err := e.freshApproval(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return Approval{}, ErrNotFound
	} else if err != nil {
		return Approval{}, err
	}
	if apv.originator.ID != a.ID || apv.originator.Kind != a.Kind {
		if e.policy.Authorize(ctx, a, ScopeApprovals, apv.plan.target, T0) != nil {
			return Approval{}, ErrNotFound
		}
	}
	return apv.Approval, nil
}

// Approvals lists the approvals awaiting a person, then those decided in
// the last RecentTasks, newest first, at most MaxTasks: for people the
// policy lets decide approvals (a *Refusal otherwise).
func (e *Engine) Approvals(ctx context.Context, a Actor) ([]Approval, error) {
	if err := e.policy.Authorize(ctx, a, ScopeApprovals, "", T0); err != nil {
		return nil, forbidden(err)
	}
	e.ExpireApprovals(ctx)
	rows, err := e.st.Reader().QueryContext(ctx, `SELECT id FROM approvals WHERE state = ? OR created_at >= ? ORDER BY state <> ?, created_at DESC LIMIT ?`,
		ApprovalAwaiting, e.now().Add(-RecentTasks).UnixMilli(), ApprovalAwaiting, MaxTasks)
	if err != nil {
		return nil, fmt.Errorf("action: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]Approval, 0, len(ids))
	for _, id := range ids {
		if a, err := e.loadApproval(ctx, id); err == nil { // else its plan was pruned in between
			out = append(out, a.Approval)
		}
	}
	return out, nil
}

// Grant approves an approval for its originator and starts the task, as
// the originator, with ApprovedBy naming the person. It needs a person the
// policy lets decide approvals at the plan's tier (a step-up from T2, made
// after the approval was asked, so the confirmation is for this plan), the
// plan's hash, an unchanged target, an originator still allowed to plan it,
// and an approver allowed to run it themselves. A plan that can't run any
// more (the target changed, the token was revoked) ends its approval as
// expired, with the reason. Refusals are *Refusal and are audited.
func (e *Engine) Grant(ctx context.Context, a Actor, id string, req ApproveRequest) (Task, error) {
	apv, err := e.freshApproval(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		r := &Refusal{Status: http.StatusNotFound, Code: "not_found", Message: "There's no such approval."}
		e.recordApproval(ctx, a, ActionApprovalApprove, &Approval{ID: clip(id, 100)}, nil, audit.Refused, r, "")
		return Task{}, r
	} else if err != nil {
		return Task{}, err
	}
	p := apv.plan
	refuse := func(r *Refusal) (Task, error) {
		e.recordApproval(ctx, a, ActionApprovalApprove, &apv.Approval, p, audit.Refused, r, "")
		return Task{}, r
	}
	void := func(r *Refusal) (Task, error) {
		e.expire(ctx, apv, r.Message)
		return refuse(r)
	}
	if err := e.policy.Authorize(ctx, a, ScopeApprovals, p.target, p.tier); err != nil {
		return refuse(forbidden(err))
	}
	if r := decided(apv); r != nil {
		return refuse(r)
	}
	if req.Hash != p.hash {
		return refuse(&Refusal{Status: http.StatusConflict, Code: "plan_changed", Message: "This isn't the plan that was asked for. Reload the Inbox and look again."})
	}
	d, ok := e.reg.Get(p.action)
	if !ok {
		return void(&Refusal{Status: http.StatusConflict, Code: "unavailable", Message: "This action isn't available any more, so it can't run."})
	}
	t, err := e.resolve(ctx, d, p.target)
	if err != nil || t.State != p.state {
		return void(&Refusal{Status: http.StatusConflict, Code: "plan_changed", Message: "The target changed since this was planned, so the approval no longer fits. It has to be planned again."})
	}
	r := Request{Action: d.ID, Target: t, Params: p.params, Actor: apv.originator, Plan: p.id}
	if d.Preconditions != nil {
		if err := d.Preconditions(ctx, r); err != nil {
			return void(refusalOf(err, ""))
		}
	}
	if p.tier >= T3 && strings.TrimSpace(req.Typed) != p.doc.ConfirmText {
		return refuse(&Refusal{Status: http.StatusUnprocessableEntity, Code: "confirm_mismatch", Message: fmt.Sprintf("Type %q to confirm.", p.doc.ConfirmText)})
	}
	if err := e.policy.Authorize(WithDef(ctx, d), apv.originator, d.scope(), t.ID, p.tier); err != nil {
		ref := forbidden(err)
		ref.Message = "Whoever asked can't do this any more: " + ref.Message
		return void(ref)
	}
	if err := e.policy.Authorize(WithDef(ctx, d), a, d.scope(), t.ID, p.tier); err != nil {
		return refuse(forbidden(err))
	}
	// The confirmation must be for this approval: a step-up made after it was asked.
	if p.tier >= T2 && !a.StepUpUntil.Add(-StepUpTTL).After(apv.RequestedAt) {
		return refuse(&Refusal{Status: http.StatusForbidden, Code: "step_up_required", Message: "Confirm it's you first: approving this needs a check made after it was asked."})
	}
	now := e.now().UTC().Truncate(time.Millisecond)
	by, _ := json.Marshal(a)
	task, ref, err := e.launch(ctx, apv.originator, p, d, t, "", &a, func(tx *sql.Tx, task Task) error {
		res, err := tx.ExecContext(ctx, `UPDATE approvals SET state = ?, decided_at = ?, decided_by = ?, task_id = ? WHERE id = ? AND state = ?`,
			ApprovalApproved, now.UnixMilli(), string(by), task.ID, apv.ID, ApprovalAwaiting)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return &Refusal{Status: http.StatusConflict, Code: "already_decided", Message: "Someone else decided this a moment ago."}
		}
		return nil
	}, func(task Task) {
		apv.State, apv.DecidedAt, apv.DecidedBy, apv.Task = ApprovalApproved, &now, &TaskActor{Kind: a.Kind, Name: a.Name}, &task.ID
		e.recordApproval(ctx, a, ActionApprovalApprove, &apv.Approval, p, audit.OK, nil, task.ID)
	})
	if ref != nil {
		return refuse(ref)
	}
	if err != nil {
		return Task{}, err
	}
	e.unschedule(apv.ID)
	e.publishApproval(apv.Approval, t.ID)
	return task, nil
}

// decided refuses an approval that isn't awaiting any more.
func decided(apv *storedApproval) *Refusal {
	switch apv.State {
	case ApprovalAwaiting:
		return nil
	case ApprovalExpired:
		msg := "This request has expired."
		if apv.Reason != "" {
			msg += " " + apv.Reason
		}
		return &Refusal{Status: http.StatusGone, Code: "approval_expired", Message: msg}
	}
	who := "someone"
	if apv.DecidedBy != nil {
		who = apv.DecidedBy.Name
	}
	return &Refusal{Status: http.StatusConflict, Code: "already_decided", Message: fmt.Sprintf("This was already %s by %s.", apv.State, who)}
}

// Deny turns an approval down: a person the policy lets decide approvals
// (no step-up: saying no is always safe). Refusals are audited.
func (e *Engine) Deny(ctx context.Context, a Actor, id string, req DenyRequest) (Approval, error) {
	apv, err := e.freshApproval(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		r := &Refusal{Status: http.StatusNotFound, Code: "not_found", Message: "There's no such approval."}
		e.recordApproval(ctx, a, ActionApprovalDeny, &Approval{ID: clip(id, 100)}, nil, audit.Refused, r, "")
		return Approval{}, r
	} else if err != nil {
		return Approval{}, err
	}
	refuse := func(r *Refusal) (Approval, error) {
		e.recordApproval(ctx, a, ActionApprovalDeny, &apv.Approval, apv.plan, audit.Refused, r, "")
		return Approval{}, r
	}
	if err := e.policy.Authorize(ctx, a, ScopeApprovals, apv.plan.target, T0); err != nil {
		return refuse(forbidden(err))
	}
	if r := decided(apv); r != nil {
		return refuse(r)
	}
	reason := clip(strings.TrimSpace(req.Reason), 300)
	if reason == "" {
		reason = "Denied by " + a.Name + "."
	}
	now := e.now().UTC().Truncate(time.Millisecond)
	by, _ := json.Marshal(a)
	res, err := e.st.Writer().ExecContext(ctx, `UPDATE approvals SET state = ?, reason = ?, decided_at = ?, decided_by = ? WHERE id = ? AND state = ?`,
		ApprovalDenied, reason, now.UnixMilli(), string(by), apv.ID, ApprovalAwaiting)
	if err != nil {
		return Approval{}, fmt.Errorf("action: deny: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return refuse(&Refusal{Status: http.StatusConflict, Code: "already_decided", Message: "Someone else decided this a moment ago."})
	}
	apv.State, apv.Reason, apv.DecidedAt, apv.DecidedBy = ApprovalDenied, reason, &now, &TaskActor{Kind: a.Kind, Name: a.Name}
	e.recordApproval(ctx, a, ActionApprovalDeny, &apv.Approval, apv.plan, audit.OK, nil, "")
	e.unschedule(apv.ID)
	e.publishApproval(apv.Approval, apv.plan.target)
	e.ended(apv.Approval)
	return apv.Approval, nil
}

// Withdraw ends every approval an originator (a token) is waiting on, as
// expired with reason: revoking a token takes back what it asked for.
func (e *Engine) Withdraw(ctx context.Context, originatorID, reason string) {
	e.endAwaiting(ctx, `SELECT id FROM approvals WHERE state = ? AND originator_id = ?`, []any{ApprovalAwaiting, originatorID}, reason)
}

// ExpireApprovals ends the approvals whose time is up. A timer runs it at
// each approval's expiry, and Run every hour as a safety net.
func (e *Engine) ExpireApprovals(ctx context.Context) {
	e.endAwaiting(ctx, `SELECT id FROM approvals WHERE state = ? AND expires_at <= ?`, []any{ApprovalAwaiting, e.now().UnixMilli()}, "Nobody approved it in time.")
}

func (e *Engine) endAwaiting(ctx context.Context, q string, args []any, reason string) {
	rows, err := e.st.Reader().QueryContext(ctx, q, args...)
	if err != nil {
		e.log.Error("action: reading approvals failed", "err", err)
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	// A read cut short still ends the approvals it got to; the next run
	// (at least hourly) picks up the rest.
	if err := rows.Err(); err != nil {
		e.log.Error("action: reading approvals failed", "err", err)
	}
	rows.Close()
	for _, id := range ids {
		if a, err := e.loadApproval(ctx, id); err == nil {
			e.expire(ctx, a, reason)
		}
	}
}

// system is the hub, as the audit log names it when it ends an approval.
var system = Actor{Kind: KindSystem, Name: "Cha Dash"}

// expire ends an awaiting approval as expired, with reason.
func (e *Engine) expire(ctx context.Context, a *storedApproval, reason string) {
	now := e.now().UTC().Truncate(time.Millisecond)
	res, err := e.st.Writer().ExecContext(context.WithoutCancel(ctx), `UPDATE approvals SET state = ?, reason = ?, decided_at = ? WHERE id = ? AND state = ?`,
		ApprovalExpired, reason, now.UnixMilli(), a.ID, ApprovalAwaiting)
	if err != nil {
		e.log.Error("action: expiring an approval failed", "approval", a.ID, "err", err)
		return
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return
	}
	a.State, a.Reason, a.DecidedAt = ApprovalExpired, reason, &now
	e.recordApproval(ctx, system, ActionApprovalExpire, &a.Approval, a.plan, audit.OK, nil, "")
	e.unschedule(a.ID)
	e.publishApproval(a.Approval, a.plan.target)
	e.ended(a.Approval)
}

// ended tells Options.OnApprovalEnded about an approval that won't run.
func (e *Engine) ended(apv Approval) {
	if e.onEnded != nil {
		e.onEnded(apv)
	}
}

// recordApproval audits one step of an approval. Params name the approval,
// the plan and the action, never the action's params.
func (e *Engine) recordApproval(ctx context.Context, actor Actor, action string, apv *Approval, p *storedPlan, result string, r *Refusal, task string) {
	params := map[string]string{"approval": apv.ID}
	entry := audit.Entry{Actor: auditActor(actor), Action: action, Result: result, Task: task}
	if p != nil {
		params["plan"], params["action"], params["originator"] = p.id, p.action, apv.Originator.Name
		entry.Target, entry.Tier = p.target, p.tier.String()
	}
	if apv.Reason != "" && result == audit.OK {
		params["reason"] = apv.Reason
	}
	entry.Params, _ = json.Marshal(params)
	if r != nil {
		entry.Error = &audit.Error{Code: r.Code, Message: r.Message}
	}
	if _, err := e.audit.Append(context.WithoutCancel(ctx), entry); err != nil {
		e.log.Error("action: auditing an approval failed", "approval", apv.ID, "step", action, "err", err)
	}
}

// ── the Inbox item ───────────────────────────────────────────────────────

// approvalItem is the Inbox item for an approval: an incident (the inbox
// topic's shape) of class "approval", with the approval itself beside the
// usual fields.
type approvalItem struct {
	ID           string     `json:"id"`
	Scenario     string     `json:"scenario"`
	Class        string     `json:"class"`
	Severity     string     `json:"severity"`
	Status       string     `json:"status"`
	Phase        string     `json:"phase"`
	Title        string     `json:"title"`
	Summary      string     `json:"summary"`
	Target       string     `json:"target"`
	OpenedAt     time.Time  `json:"opened_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	ResolvedAt   *time.Time `json:"resolved_at,omitempty"`
	ResolvedBy   string     `json:"resolved_by,omitempty"`
	Resolution   string     `json:"resolution,omitempty"`
	Facts        any        `json:"facts"`
	Causes       []any      `json:"causes"`
	BlastRadius  []Impact   `json:"blast_radius"`
	ResolvesWhen string     `json:"resolves_when"`
	Approval     Approval   `json:"approval"`
}

// ApprovalItemID is the Inbox item's ID for an approval.
func ApprovalItemID(approval string) string { return "approval." + approval }

const incidentsColl = "incidents"

// publishApproval puts an approval's Inbox item, open while it waits and
// resolved once decided. Resolved items leave the Inbox after RecentTasks.
func (e *Engine) publishApproval(apv Approval, target string) {
	if e.inbox == nil {
		return
	}
	who := apv.Originator.Name
	by := "Planned through the API by a token"
	switch apv.Originator.Kind {
	case KindAgent:
		by = "Planned through the API by an agent"
	case KindSync:
		by = "Planned by a git sync"
	case KindUser:
		by = "Planned by " + who
	}
	item := approvalItem{
		ID: ApprovalItemID(apv.ID), Scenario: "approval", Class: ApprovalClass, Severity: "attention",
		Status: "open", Phase: apv.State,
		Title:   who + " asks to run: " + apv.Plan.Label,
		Summary: fmt.Sprintf("%s, a %s action. It runs only if an admin or the owner approves it here.", by, apv.Plan.Tier),
		Target:  target, OpenedAt: apv.RequestedAt, UpdatedAt: apv.RequestedAt,
		Facts:  map[string]string{"approval": apv.ID, "plan": apv.Plan.ID, "action": apv.Plan.Action, "tier": apv.Plan.Tier.String(), "originator": apv.Originator.ID},
		Causes: []any{}, BlastRadius: apv.Plan.Impact,
		ResolvesWhen: "An admin or the owner approves or denies it, or it expires.",
		Approval:     apv,
	}
	if item.BlastRadius == nil {
		item.BlastRadius = []Impact{}
	}
	if apv.State != ApprovalAwaiting && apv.DecidedAt != nil {
		item.Status, item.Severity, item.UpdatedAt, item.ResolvedAt, item.ResolvedBy = "resolved", "info", *apv.DecidedAt, apv.DecidedAt, apv.State
		switch apv.State {
		case ApprovalApproved:
			item.Resolution = "Approved by " + apv.DecidedBy.Name + "."
		default:
			item.Resolution = apv.Reason
		}
		e.mu.Lock()
		e.resolvedItems[item.ID] = *apv.DecidedAt
		e.mu.Unlock()
	}
	_, _ = e.inbox.Update(func(tx *livedoc.Tx) error {
		tx.Put(incidentsColl, item.ID, item)
		return nil
	})
}

// pruneApprovalItems drops resolved approval items older than RecentTasks
// from the Inbox.
func (e *Engine) pruneApprovalItems() {
	if e.inbox == nil {
		return
	}
	cutoff := e.now().Add(-RecentTasks)
	var drop []string
	e.mu.Lock()
	for id, at := range e.resolvedItems {
		if at.Before(cutoff) {
			drop = append(drop, id)
			delete(e.resolvedItems, id)
		}
	}
	e.mu.Unlock()
	if len(drop) == 0 {
		return
	}
	_, _ = e.inbox.Update(func(tx *livedoc.Tx) error {
		for _, id := range drop {
			tx.Delete(incidentsColl, id)
		}
		return nil
	})
}

// ── expiry timers ────────────────────────────────────────────────────────

// schedule expires approval id at at (a timer; ExpireApprovals decides).
func (e *Engine) schedule(id string, at time.Time) {
	wait := max(at.Sub(e.now()), 0)
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return
	}
	if old := e.timers[id]; old != nil {
		old.Stop()
	}
	e.timers[id] = time.AfterFunc(wait, func() {
		e.mu.Lock()
		closed := e.closed
		delete(e.timers, id)
		e.mu.Unlock()
		if !closed {
			e.ExpireApprovals(e.ctx)
		}
	})
}

func (e *Engine) unschedule(id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if t := e.timers[id]; t != nil {
		t.Stop()
		delete(e.timers, id)
	}
}

// loadApprovals runs at start: approvals whose time ran out while the hub
// was down expire, and the rest return to the Inbox and their timers.
func (e *Engine) loadApprovals(ctx context.Context) error {
	e.ExpireApprovals(ctx)
	rows, err := e.st.Reader().QueryContext(ctx, `SELECT id FROM approvals WHERE state = ?`, ApprovalAwaiting)
	if err != nil {
		return fmt.Errorf("action: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		a, err := e.loadApproval(ctx, id)
		if err != nil {
			continue
		}
		e.publishApproval(a.Approval, a.plan.target)
		e.schedule(a.ID, a.ExpiresAt)
	}
	return nil
}
