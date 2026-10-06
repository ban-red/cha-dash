package action

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"dash.cha.sh/internal/audit"
)

// ── plan ─────────────────────────────────────────────────────────────────

// PlanRequest is the body of POST /api/actions/plan.
type PlanRequest struct {
	Action string          `json:"action"`
	Target string          `json:"target"`
	Params json.RawMessage `json:"params,omitempty"`
}

// Plan is what the engine will do, as POST /api/actions/plan returns it.
type Plan struct {
	ID          string          `json:"id"`
	Hash        string          `json:"hash"`
	Action      string          `json:"action"`
	Label       string          `json:"label"`
	Target      string          `json:"target"`
	Params      json.RawMessage `json:"params"`
	Tier        Tier            `json:"tier"`
	Confirm     string          `json:"confirm"` // none, undo, hold, type
	ConfirmText string          `json:"confirm_text,omitempty"`
	StepUp      string          `json:"step_up"` // none, required, fresh
	Steps       []Step          `json:"steps"`
	Impact      []Impact        `json:"impact"`
	Undo        *Undo           `json:"undo"`
	ExpiresAt   time.Time       `json:"expires_at"`
}

// confirmFor is the confirmation UX a tier gets.
func confirmFor(t Tier) string {
	switch t {
	case T0:
		return "none"
	case T1:
		return "undo"
	case T2:
		return "hold"
	}
	return "type"
}

// Plan resolves, authorizes and plans an action. Refusals are *Refusal and
// are audited.
func (e *Engine) Plan(ctx context.Context, a Actor, req PlanRequest) (*Plan, error) {
	d, ok := e.reg.Get(req.Action)
	if !ok {
		return nil, e.refuse(ctx, a, req.Action, req.Target, req.Params, nil, "",
			&Refusal{Status: http.StatusNotFound, Code: "not_found", Message: fmt.Sprintf("There's no action called %q.", req.Action)})
	}
	params, ref := e.params(d, req.Params)
	if ref != nil {
		return nil, e.refuse(ctx, a, d.ID, req.Target, req.Params, d, d.Tier.String(), ref)
	}
	t, err := e.resolve(ctx, d, req.Target)
	if err != nil {
		return nil, e.refuse(ctx, a, d.ID, req.Target, params, d, d.Tier.String(), refusalOf(err, "That target doesn't exist, or this action doesn't apply to it."))
	}
	tier := d.Tier
	if d.TierFor != nil {
		tier = max(tier, d.TierFor(t))
	}
	stepUp := "none"
	switch err := e.policy.Authorize(WithDef(ctx, d), a, d.scope(), t.ID, tier); {
	case errors.Is(err, ErrStepUpRequired):
		stepUp = "required"
	case err != nil:
		return nil, e.refuse(ctx, a, d.ID, t.ID, params, d, tier.String(), forbidden(err))
	}
	if tier == T4 {
		stepUp = "fresh"
	}
	r := Request{Action: d.ID, Target: t, Params: params, Actor: a}
	if d.Preconditions != nil {
		if err := d.Preconditions(ctx, r); err != nil {
			return nil, e.refuse(ctx, a, d.ID, t.ID, params, d, tier.String(), refusalOf(err, ""))
		}
	}

	now := e.now().UTC().Truncate(time.Millisecond)
	p := &Plan{
		ID:     "p_" + strings.ToLower(rand.Text()),
		Action: d.ID, Label: labelOf(d, r), Target: t.ID,
		Params:    redact(params, d),
		Tier:      tier,
		Confirm:   confirmFor(tier),
		StepUp:    stepUp,
		Steps:     stepsOf(d, r),
		Impact:    e.impactOf(ctx, d, r),
		ExpiresAt: now.Add(PlanTTL),
	}
	if p.Confirm == "type" {
		p.ConfirmText = t.Name
	}
	if d.Undo != nil {
		p.Undo = d.Undo(r)
	}
	p.Hash = planHash(d.ID, t.ID, params, tier, p.Impact, t.State)

	doc, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	actor, _ := json.Marshal(a)
	_, err = e.st.Writer().ExecContext(ctx, `INSERT INTO plans (id, hash, session_id, actor_id, actor, action, target, params, tier, state, document, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.Hash, a.Session, a.ID, string(actor), d.ID, t.ID, string(params), tier.String(), t.State, string(doc), now.UnixMilli(), p.ExpiresAt.UnixMilli())
	if err != nil {
		return nil, fmt.Errorf("action: save plan: %w", err)
	}
	return p, nil
}

// params validates raw against d's schema and returns it canonical: sorted
// keys, numbers verbatim.
func (e *Engine) params(d *Def, raw json.RawMessage) (json.RawMessage, *Refusal) {
	invalid := func(msg string, fields []FieldError) *Refusal {
		return &Refusal{Status: http.StatusUnprocessableEntity, Code: "invalid", Message: msg, Fields: fields}
	}
	m := map[string]any{}
	if t := bytes.TrimSpace(raw); len(t) > 0 && !bytes.Equal(t, []byte("null")) {
		if err := json.Unmarshal(t, &m); err != nil || m == nil {
			return nil, invalid("params must be a JSON object.", nil)
		}
	}
	if d.Params == nil {
		if len(m) > 0 {
			return nil, invalid("This action takes no params.", nil)
		}
	} else if errs := d.Params.Validate(m); len(errs) > 0 {
		return nil, invalid(errs[0].Message, errs)
	}
	canon, err := canonicalJSON(raw)
	if err != nil {
		return nil, invalid("params must be a JSON object.", nil)
	}
	return canon, nil
}

func (e *Engine) resolve(ctx context.Context, d *Def, target string) (Target, error) {
	if target == "" {
		return Target{}, ErrNotFound
	}
	if d.Resolve != nil {
		t, err := d.Resolve(ctx, target)
		if err == nil && t.Name == "" {
			t.Name = t.ID
		}
		return t, err
	}
	return graphTarget(e.graph, d, target)
}

func (e *Engine) impactOf(ctx context.Context, d *Def, r Request) []Impact {
	var im []Impact
	if d.BlastRadius != nil {
		im = d.BlastRadius(ctx, r)
	} else {
		im = GraphImpact(e.graph, r.Target.ID)
	}
	if im == nil {
		im = []Impact{}
	}
	return im
}

func labelOf(d *Def, r Request) string {
	if d.Label != nil {
		return d.Label(r)
	}
	if d.Title == "" {
		return d.ID + " " + r.Target.Name
	}
	return d.Title + " " + r.Target.Name
}

func stepsOf(d *Def, r Request) []Step {
	if d.Steps != nil {
		if s := d.Steps(r); len(s) > 0 {
			return s
		}
	}
	s := []Step{{Label: labelOf(d, r)}}
	if d.Verify != nil {
		s = append(s, Step{Label: "Check the result"})
	}
	return s
}

// planHash is the hash an approval must echo: over the action, target,
// params, tier, blast radius and the target's state, as canonical JSON.
func planHash(action, target string, params json.RawMessage, tier Tier, impact []Impact, state string) string {
	b, _ := json.Marshal(struct {
		Action string          `json:"action"`
		Target string          `json:"target"`
		Params json.RawMessage `json:"params"`
		Tier   Tier            `json:"tier"`
		Impact []Impact        `json:"impact"`
		State  string          `json:"state"`
	}{action, target, params, tier, impact, state})
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// canonicalJSON re-encodes a JSON object with sorted keys and numbers
// verbatim; nil or null is {}.
func canonicalJSON(raw json.RawMessage) (json.RawMessage, error) {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || bytes.Equal(t, []byte("null")) {
		return json.RawMessage("{}"), nil
	}
	dec := json.NewDecoder(bytes.NewReader(t))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil || m == nil {
		return nil, errors.New("not an object")
	}
	return json.Marshal(m)
}

// redact replaces d's secret params with audit.Redacted.
func redact(params json.RawMessage, d *Def) json.RawMessage {
	if d == nil || d.Params == nil || len(d.Params.Secrets()) == 0 {
		return params
	}
	dec := json.NewDecoder(bytes.NewReader(params))
	dec.UseNumber()
	var m map[string]any
	if dec.Decode(&m) != nil {
		return json.RawMessage("{}")
	}
	for _, s := range d.Params.Secrets() {
		if _, ok := m[s]; ok {
			m[s] = audit.Redacted
		}
	}
	b, _ := json.Marshal(m)
	return b
}

func forbidden(err error) *Refusal {
	// A policy may explain itself ("Your role (viewer) can't run T2
	// actions."); the bare sentinels get a generic sentence.
	said := func(generic string) string {
		if err == ErrForbidden || err == ErrStepUpRequired {
			return generic
		}
		return err.Error()
	}
	if errors.Is(err, ErrStepUpRequired) {
		return &Refusal{Status: http.StatusForbidden, Code: "step_up_required", Message: said("Confirm it's you first: this needs a fresh sign-in check.")}
	}
	return &Refusal{Status: http.StatusForbidden, Code: "forbidden", Message: said("You don't have permission to do this.")}
}

// refusalOf turns a Def hook's error into a refusal.
func refusalOf(err error, notFound string) *Refusal {
	var r *Refusal
	switch {
	case errors.As(err, &r):
		return r
	case errors.Is(err, ErrNotFound):
		return &Refusal{Status: http.StatusNotFound, Code: "not_found", Message: notFound}
	}
	return &Refusal{Status: http.StatusConflict, Code: "unavailable", Message: err.Error()}
}

// refuse audits a refused attempt and returns the refusal.
func (e *Engine) refuse(ctx context.Context, a Actor, action, target string, params json.RawMessage, d *Def, tier string, r *Refusal) *Refusal {
	if !json.Valid(params) {
		params = nil
	}
	var secrets []string
	if d != nil && d.Params != nil {
		secrets = d.Params.Secrets()
	}
	if action == "" {
		action = "unknown"
	}
	// Whatever the request named goes on record, but not without bound.
	action, target = clip(action, 200), clip(target, 500)
	if _, err := e.audit.Append(context.WithoutCancel(ctx), audit.Entry{
		Actor: auditActor(a), Action: action, Target: target, Params: auditParams(params), Secrets: secrets,
		Tier: tier, Result: audit.Refused, Error: &audit.Error{Code: r.Code, Message: r.Message},
	}); err != nil {
		e.log.Error("action: auditing a refusal failed", "action", action, "err", err)
	}
	return r
}

// auditParams keeps params that are a JSON object; anything else (a
// malformed request) is recorded as no params.
func auditParams(p json.RawMessage) json.RawMessage {
	if _, err := canonicalJSON(p); err != nil {
		return nil
	}
	return p
}

func auditActor(a Actor) audit.Actor {
	return audit.Actor{Kind: a.Kind, ID: a.ID, Name: a.Name, Via: a.Via, Session: a.Session, IP: a.IP}
}

// ── approve ──────────────────────────────────────────────────────────────

// ApproveRequest is the body of POST /api/plans/{id}/approve.
type ApproveRequest struct {
	Hash  string `json:"hash"`
	Typed string `json:"typed,omitempty"`
}

// storedPlan is a plans row.
type storedPlan struct {
	id, hash, session, actorID, action, target, state string
	params                                            json.RawMessage
	tier                                              Tier
	doc                                               Plan
	created, expires                                  time.Time
	approved                                          bool
}

func (e *Engine) loadPlan(ctx context.Context, id string) (*storedPlan, error) {
	var p storedPlan
	var params, tier, doc string
	var created, expires int64
	var approved sql.NullInt64
	err := e.st.Reader().QueryRowContext(ctx, `SELECT id, hash, session_id, actor_id, action, target, params, tier, state, document, created_at, expires_at, approved_at
		FROM plans WHERE id = ?`, id).
		Scan(&p.id, &p.hash, &p.session, &p.actorID, &p.action, &p.target, &params, &tier, &p.state, &doc, &created, &expires, &approved)
	if err != nil {
		return nil, err
	}
	p.params = json.RawMessage(params)
	if p.tier, err = ParseTier(tier); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(doc), &p.doc); err != nil {
		return nil, err
	}
	p.created, p.expires, p.approved = time.UnixMilli(created), time.UnixMilli(expires), approved.Valid
	return &p, nil
}

// Approve runs a plan: same session, the plan's hash, a fresh step-up where
// the tier needs one, and an unchanged target. With an idempotency key, a
// repeated approval returns the task the first one started. Refusals are
// *Refusal and are audited.
func (e *Engine) Approve(ctx context.Context, a Actor, planID string, req ApproveRequest, idemKey string) (Task, error) {
	if idemKey != "" {
		if t, ok, err := e.idempotent(ctx, a, planID, idemKey); err != nil || ok {
			return t, err
		}
	}
	p, err := e.loadPlan(ctx, planID)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, e.refuse(ctx, a, "unknown", "", nil, nil, "", &Refusal{Status: http.StatusNotFound, Code: "not_found", Message: "There's no such plan."})
	} else if err != nil {
		return Task{}, err
	}
	d, _ := e.reg.Get(p.action)
	refuse := func(r *Refusal) (Task, error) {
		return Task{}, e.refuse(ctx, a, p.action, p.target, p.params, d, p.tier.String(), r)
	}
	now := e.now()
	switch {
	case p.session == "" || p.session != a.Session || p.actorID != a.ID:
		return refuse(&Refusal{Status: http.StatusForbidden, Code: "forbidden", Message: "Only the session that made this plan can approve it."})
	case p.approved:
		return refuse(&Refusal{Status: http.StatusGone, Code: "plan_expired", Message: "This plan has already run. Plan it again."})
	case !now.Before(p.expires):
		return refuse(&Refusal{Status: http.StatusGone, Code: "plan_expired", Message: "This plan has expired. Plan it again."})
	case req.Hash != p.hash:
		return refuse(&Refusal{Status: http.StatusConflict, Code: "plan_changed", Message: "This isn't the plan that was shown. Plan it again."})
	case d == nil:
		return refuse(&Refusal{Status: http.StatusConflict, Code: "unavailable", Message: "This action isn't available any more."})
	}
	t, err := e.resolve(ctx, d, p.target)
	if err != nil || t.State != p.state {
		return refuse(&Refusal{Status: http.StatusConflict, Code: "plan_changed", Message: "The target changed since this was planned. Plan it again."})
	}
	r := Request{Action: d.ID, Target: t, Params: p.params, Actor: a, Plan: p.id}
	if d.Preconditions != nil {
		if err := d.Preconditions(ctx, r); err != nil {
			return refuse(refusalOf(err, ""))
		}
	}
	if p.tier >= T3 && strings.TrimSpace(req.Typed) != p.doc.ConfirmText {
		return refuse(&Refusal{Status: http.StatusUnprocessableEntity, Code: "confirm_mismatch", Message: fmt.Sprintf("Type %q to confirm.", p.doc.ConfirmText)})
	}
	if err := e.policy.Authorize(WithDef(ctx, d), a, d.scope(), t.ID, p.tier); err != nil {
		return refuse(forbidden(err))
	}
	// T4 always asks again: the grant must have begun after the plan.
	if p.tier == T4 && !a.StepUpUntil.Add(-StepUpTTL).After(p.created) {
		return refuse(forbidden(ErrStepUpRequired))
	}

	task := Task{
		ID: "t_" + strings.ToLower(rand.Text()), Plan: p.id, Action: d.ID, Label: p.doc.Label, Target: t.ID,
		Actor: TaskActor{Kind: a.Kind, Name: a.Name}, State: StateQueued, Cancellable: d.Cancellable,
	}
	r.Task = task.ID
	if !e.lock(t.ID, task.ID) {
		return refuse(&Refusal{Status: http.StatusConflict, Code: "locked", Message: "Something else is already running on this. Try again when it's done."})
	}
	created := now.UTC().Truncate(time.Millisecond)
	doc, _ := json.Marshal(task)
	err = e.st.WriteTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE plans SET approved_at = ? WHERE id = ? AND approved_at IS NULL`, created.UnixMilli(), p.id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return errSpent
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO tasks (id, plan_id, actor_id, state, document, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			task.ID, p.id, a.ID, task.State, string(doc), created.UnixMilli()); err != nil {
			return err
		}
		if idemKey != "" {
			_, err = tx.ExecContext(ctx, `INSERT INTO idempotency_keys (actor_id, key, plan_id, task_id, created_at) VALUES (?, ?, ?, ?, ?)`,
				a.ID, idemKey, p.id, task.ID, created.UnixMilli())
		}
		return err
	})
	if err != nil {
		e.unlock(t.ID, task.ID)
		if errors.Is(err, errSpent) {
			return refuse(&Refusal{Status: http.StatusGone, Code: "plan_expired", Message: "This plan has already run. Plan it again."})
		}
		return Task{}, fmt.Errorf("action: start task: %w", err)
	}
	if p.tier == T4 && e.consume != nil && a.Session != "" {
		// The grant is spent: the next T4 asks again, even within its five minutes.
		if err := e.consume(ctx, a.Session); err != nil && e.log != nil {
			e.log.Warn("action: ending the step-up grant after a T4 failed", "err", err)
		}
	}
	if !e.start(d, r, task, p.tier, p.doc.Undo, created) {
		e.unlock(t.ID, task.ID)
		return Task{}, errors.New("action: the engine is shutting down")
	}
	return task, nil
}

var errSpent = errors.New("action: plan already approved")

// idempotent returns the task an earlier approval with the same key
// started, if there was one.
func (e *Engine) idempotent(ctx context.Context, a Actor, planID, key string) (Task, bool, error) {
	var plan, taskID string
	err := e.st.Reader().QueryRowContext(ctx, `SELECT plan_id, task_id FROM idempotency_keys WHERE actor_id = ? AND key = ? AND created_at > ?`,
		a.ID, key, e.now().Add(-idempotencyRetention).UnixMilli()).Scan(&plan, &taskID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return Task{}, false, nil
	case err != nil:
		return Task{}, false, err
	case plan != planID:
		return Task{}, false, &Refusal{Status: http.StatusUnprocessableEntity, Code: "invalid", Message: "That idempotency key was used for another plan."}
	}
	t, err := e.task(ctx, taskID)
	return t, err == nil, err
}
