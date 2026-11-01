package updates

import (
	"context"
	crand "crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/auth"
	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/livedoc"
	"dash.cha.sh/internal/msg"
)

// The rollout action (docs/plan/14-v0.4.md, "Rollouts"): updates.rollout
// on RolloutTarget, with params
//
//	{"targets": [resource IDs], "hash": "sha256:…", "window": "mw_…", "rollout": "rol_…"}
//
// targets empty is "Update all". hash, when given, is the plan the person
// looked at (POST /api/updates/rollouts/preview): planning and approving
// refuse a different one. window schedules it into a maintenance window
// instead of running it: approving stores it, as the person who approved,
// with the step-up that took (decision 5). rollout runs one that was
// scheduled or is waiting for a person.
//
// It runs as one task: each step's snapshot, then its update, as nested
// actions (action.TaskRun.Nested), each audited. The first step that fails
// stops it (decision 4): that update has rolled back on its own, the rest
// don't start, and the Inbox says which ran, which rolled back and which
// didn't start.

// RolloutTarget is updates.rollout's target: not a resource, the Update
// Center itself.
const RolloutTarget = "chadash:updates"

// ActionSource names the Update Center's actions in the engine's registry.
const ActionSource = "updates"

// rolloutTimeout bounds a whole rollout: each update may take up to 20
// minutes, and a snapshot 5.
const rolloutTimeout = 6 * time.Hour

// ViaSchedule is the Via of a scheduled rollout's actor.
const ViaSchedule = "schedule"

// Scheduler runs a plan for a person who isn't here (action.Engine).
type Scheduler interface {
	RunScheduled(ctx context.Context, a action.Actor, req action.PlanRequest) (action.Task, *action.Approval, error)
}

// RolloutParams are updates.rollout's params.
type RolloutParams struct {
	Targets []string `json:"targets,omitempty"`
	Hash    string   `json:"hash,omitempty"`
	Window  string   `json:"window,omitempty"`
	Rollout string   `json:"rollout,omitempty"`
}

func rolloutSchema() action.Schema {
	id := &integration.Schema{Type: "string", Pattern: `^[a-z]+:.{1,500}$`}
	targets := integration.Array("Containers", id).Describe("Empty: every available update.")
	maxTargets := 500
	targets.MaxItems = &maxTargets
	return integration.Params(integration.Object(
		integration.Prop("targets", targets),
		integration.Prop("hash", &integration.Schema{Type: "string", Title: "Plan hash", Pattern: `^sha256:[0-9a-f]{64}$`}),
		integration.Prop("window", &integration.Schema{Type: "string", Title: "Maintenance window", Pattern: `^mw_[a-z0-9]{1,64}$`}),
		integration.Prop("rollout", &integration.Schema{Type: "string", Title: "Rollout", Pattern: `^rol_[a-z0-9]{1,64}$`}),
	))
}

// Actions is the Update Center's actions, for the engine's registry
// (Registry.Register with ActionSource).
func (s *Service) Actions() []action.Def {
	return []action.Def{{
		ID: ActRollout, Title: "Update", Tier: action.T2, Async: true, Timeout: rolloutTimeout,
		Params: rolloutSchema(),
		Resolve: func(_ context.Context, target string) (action.Target, error) {
			if target != RolloutTarget {
				return action.Target{}, action.ErrNotFound
			}
			return action.Target{ID: RolloutTarget, Kind: "updates", Name: msg.New("updates.target_name").Text()}, nil
		},
		TierOf: func(ctx context.Context, r action.Request) action.Tier {
			p, err := s.requestPlan(ctx, r)
			if err != nil {
				return action.T2
			}
			return max(p.plan.Tier, action.T2)
		},
		Preconditions: s.rolloutPreconditions,
		Label:         s.rolloutLabel,
		Steps:         s.rolloutSteps,
		BlastRadius:   s.rolloutImpact,
		Execute:       s.executeRollout,
	}}
}

// requested is a request's plan: fresh, for its targets or its stored
// rollout's.
type requested struct {
	params  RolloutParams
	plan    RolloutPlan
	stored  *Rollout          // params.Rollout's row
	window  MaintenanceWindow // params.Window's
	targets []string
}

func (s *Service) requestPlan(ctx context.Context, r action.Request) (requested, error) {
	var q requested
	if err := r.Decode(&q.params); err != nil {
		return q, err
	}
	q.targets = q.params.Targets
	if q.params.Rollout != "" {
		ro, err := s.Rollout(ctx, q.params.Rollout)
		if err != nil {
			return q, err
		}
		q.stored = &ro
		q.targets = nil
		for _, st := range ro.Steps {
			q.targets = append(q.targets, st.Target)
		}
	}
	if q.params.Window != "" {
		w, err := s.Window(ctx, q.params.Window)
		if err != nil {
			return q, err
		}
		q.window = w
	}
	var err error
	q.plan, err = s.Plan(ctx, q.targets)
	return q, err
}

func (s *Service) rolloutPreconditions(ctx context.Context, r action.Request) error {
	q, err := s.requestPlan(ctx, r)
	switch {
	case errors.Is(err, ErrNoWindow):
		return action.Precondition(msg.New("updates.no_window").Text())
	case errors.Is(err, ErrNoRollout):
		return action.Precondition(msg.New("updates.no_rollout").Text())
	case err != nil:
		return err
	}
	p := q.params
	switch {
	case p.Rollout != "" && (p.Window != "" || len(p.Targets) > 0 || p.Hash != ""):
		return action.Precondition(msg.New("updates.rollout_alone").Text())
	case p.Rollout != "" && q.stored.State != RolloutScheduled && q.stored.State != RolloutWaiting:
		return action.Precondition(msg.New("updates.rollout_not_pending").Text())
	case p.Rollout != "" && r.Actor.Via == ViaSchedule && q.plan.Hash != q.stored.PlanHash:
		// Decision 5: a changed plan waits for a person.
		return action.Precondition(msg.New("updates.plan_changed_scheduled").Text())
	case p.Hash != "" && p.Hash != q.plan.Hash:
		return action.Precondition(msg.New("updates.plan_changed").Text())
	case len(q.plan.Runnable()) == 0:
		return action.Precondition(msg.New("updates.nothing_to_update").Text())
	}
	if p.Window != "" {
		if r.Actor.Kind != action.KindUser {
			return action.Precondition(msg.New("updates.schedule_person").Text())
		}
		if _, _, ok := q.window.Occurrence(s.now()); !ok {
			return action.Precondition(msg.New("updates.window_over", "window", q.window.Name).Text())
		}
		_, doc := s.g.Snapshot(nil)
		for _, st := range q.plan.Runnable() {
			if !q.window.covers(doc, st.Target) {
				return action.Precondition(msg.New("updates.window_misses", "name", st.Name, "window", q.window.Name).Text())
			}
		}
	}
	return nil
}

func (s *Service) rolloutLabel(r action.Request) string {
	var p RolloutParams
	_ = r.Decode(&p)
	n := len(p.Targets)
	switch {
	case p.Rollout != "":
		return msg.New("updates.label_run").Text()
	case p.Window != "" && n == 0:
		return msg.New("updates.label_schedule_all").Text()
	case p.Window != "":
		return msg.New("updates.label_schedule", "count", n).Text()
	case n == 0:
		return msg.New("updates.label_all").Text()
	case n == 1:
		return msg.New("updates.label_one").Text()
	}
	return msg.New("updates.label_some", "count", n).Text()
}

func (s *Service) rolloutSteps(r action.Request) []action.Step {
	q, err := s.requestPlan(context.Background(), r)
	if err != nil {
		return nil
	}
	_, doc := s.g.Snapshot(nil)
	var out []action.Step
	if q.params.Window != "" {
		start, _, _ := q.window.Occurrence(s.now())
		out = append(out, action.Step{Label: msg.New("updates.step_scheduled", "window", q.window.Name, "at", start.Format(time.RFC3339)).Text()})
	}
	if !q.plan.Ordered {
		out = append(out, action.Step{Label: msg.New("updates.step_unordered").Text()})
	}
	snapped := map[string]bool{}
	for _, st := range q.plan.Steps {
		out = append(out, action.Step{Label: stepLabel(doc, st, snapped)})
	}
	out = append(out, action.Step{Label: msg.New("updates.step_stop").Text()})
	return out
}

// stepLabel says what one step will do, snapshot first.
func stepLabel(doc graph.Doc, st RolloutStep, snapped map[string]bool) string {
	where := st.Name
	if st.Host != "" {
		where = msg.New("updates.on_host", "name", st.Name, "host", st.Host).Text()
	}
	if st.State == StepSkipped {
		return msg.New("updates.step_skip", "name", where, "reason", st.Reason).Text()
	}
	switch st.Snapshot {
	case SnapshotGit:
		return msg.New("updates.step_git", "name", where).Text()
	case SnapshotPVE:
		guest := st.Guest
		if g := doc.Resources[graph.RID(st.Guest)]; g != nil && g.Name != "" {
			guest = g.Name
		}
		if snapped[st.Guest] {
			return msg.New("updates.step_pve_again", "name", where, "guest", guest).Text()
		}
		snapped[st.Guest] = true
		return msg.New("updates.step_pve", "name", where, "guest", guest).Text()
	}
	return msg.New("updates.step_none", "name", where).Text()
}

func (s *Service) rolloutImpact(ctx context.Context, r action.Request) []action.Impact {
	q, err := s.requestPlan(ctx, r)
	if err != nil {
		return []action.Impact{}
	}
	out := []action.Impact{}
	seen := map[string]bool{}
	for _, st := range q.plan.Runnable() {
		if !seen[st.Target] {
			seen[st.Target] = true
			out = append(out, action.Impact{Resource: st.Target, Via: "update", Depth: 0})
		}
		for _, im := range action.GraphImpact(s.g, st.Target) {
			if !seen[im.Resource] {
				seen[im.Resource] = true
				out = append(out, im)
			}
		}
	}
	return out
}

// ── running ──────────────────────────────────────────────────────────────

func (s *Service) executeRollout(ctx context.Context, r action.Request, run *action.TaskRun) error {
	q, err := s.requestPlan(ctx, r)
	if err != nil {
		return err
	}
	by := r.Actor.ID
	if r.Actor.Kind != action.KindUser {
		by = r.Actor.User
	}
	now := s.now()
	if q.params.Window != "" {
		start, _, _ := q.window.Occurrence(now)
		ro := Rollout{ID: newRolloutID(), CreatedBy: by, PlanHash: q.plan.Hash, State: RolloutScheduled, WindowID: q.window.ID,
			ScheduledFor: start, Steps: q.plan.Steps, CreatedAt: now}
		if err := s.insertRollout(ctx, ro); err != nil {
			return err
		}
		run.Logf("%s", msg.New("updates.log_scheduled", "window", q.window.Name, "at", start.Format(time.RFC3339)).Text())
		return nil
	}
	var ro Rollout
	if q.stored != nil {
		ro = *q.stored
		ro.Steps, ro.PlanHash = q.plan.Steps, q.plan.Hash
		ok, err := s.claim(ctx, ro)
		if err != nil {
			return err
		}
		if !ok {
			return action.Fail("precondition", msg.New("updates.rollout_not_pending").Text())
		}
		s.resolveItem(waitingItemID(ro.ID))
	} else {
		ro = Rollout{ID: newRolloutID(), CreatedBy: by, PlanHash: q.plan.Hash, State: RolloutRunning, Steps: q.plan.Steps, CreatedAt: now, StartedAt: now}
		if err := s.insertRollout(ctx, ro); err != nil {
			return err
		}
	}
	ro.State, ro.StartedAt = RolloutRunning, now
	return s.runSteps(ctx, r.Actor, &ro, run)
}

// runSteps runs a rollout's steps in order, stopping at the first that
// fails, and records each step's state as it goes.
func (s *Service) runSteps(ctx context.Context, actor action.Actor, ro *Rollout, run *action.TaskRun) error {
	s.stopMu.Lock()
	s.runningHere[ro.ID] = true
	s.stopMu.Unlock()
	var order []int
	for i, st := range ro.Steps {
		if st.State != StepSkipped {
			order = append(order, i)
		}
	}
	snapped := map[string]bool{}
	var failed *action.Error
	stoppedBy, stopped := "", false
	defer s.stopped(ro.ID)
	for n, i := range order {
		st := &ro.Steps[i]
		if failed != nil {
			st.State = StepNotStarted
			continue
		}
		if !stopped {
			stoppedBy, stopped = s.stopRequested(ro.ID)
			if stopped {
				run.Logf("%s", cancelledBy(stoppedBy).Text())
			}
		}
		if stopped {
			st.State, st.Reason = StepNotStarted, cancelledBy(stoppedBy).Text()
			continue
		}
		run.Step(msg.New("updates.progress", "n", n+1, "count", len(order), "name", st.Name).Text())
		run.Progress(float64(n) / float64(len(order)))
		st.State = StepRunning
		s.saveSteps(ctx, ro)
		if st.Snapshot == SnapshotPVE && !snapped[st.Guest] {
			name := "cd-" + strings.TrimPrefix(ro.ID, "rol_")
			name = name[:min(len(name), 40)]
			params, _ := json.Marshal(map[string]string{"name": name, "description": msg.New("updates.snapshot_description").Text()})
			run.Logf("%s", msg.New("updates.log_snapshot", "guest", st.Guest, "snapshot", name).Text())
			if err := run.Nested(ctx, actor, snapshotAction, st.Guest, params); err != nil {
				failed = asFail(err)
				st.State, st.Reason = StepNotStarted, msg.New("updates.snapshot_failed", "reason", failed.Message).Text()
				continue
			}
			snapped[st.Guest] = true
		}
		run.Logf("%s", msg.New("updates.log_update", "name", st.Name).Text())
		if err := run.Nested(ctx, actor, st.Action, st.Target, nil); err != nil {
			failed = asFail(err)
			st.State, st.Reason = StepRolledBack, failed.Message
			if failed.Code == "rolled_back" {
				// Remember the digest, so the next Update all doesn't try
				// the same broken image again.
				if err := s.Reject(context.WithoutCancel(ctx), st.Target, st.To, failed.Message, ro.ID); err != nil {
					s.log.Warn("updates: remember a rolled-back digest", "target", st.Target, "err", err)
				}
			}
			continue
		}
		st.State = StepSucceeded
	}
	run.Progress(1)
	// The last step a nested update reported ("Rolled back and healthy")
	// isn't the rollout's: end on what happened to each.
	run.Step(summary(*ro))
	ro.FinishedAt = s.now()
	ro.State = RolloutSucceeded
	switch {
	case failed != nil:
		ro.State = RolloutFailed
	case stopped || ctx.Err() != nil:
		ro.State = RolloutCancelled
	}
	s.saveSteps(context.WithoutCancel(ctx), ro)
	switch {
	case failed != nil:
		s.failedItem(*ro)
		return action.Fail("rolled_back", summary(*ro))
	case stopped:
		// The task reads cancelled, like one stopped from the task sheet,
		// but the step that was running finished first.
		sum := msg.New("updates.sum_cancelled")
		if stoppedBy != "" {
			sum = msg.New("updates.sum_cancelled_by", "by", stoppedBy)
		}
		return action.Fail(action.CodeCancelled, strings.TrimSpace(sum.Text()+" "+summary(*ro)))
	}
	return nil
}

// cancelledBy says who stopped a rollout.
func cancelledBy(by string) msg.M {
	if by == "" {
		return msg.New("updates.cancelled")
	}
	return msg.New("updates.cancelled_by", "by", by)
}

// stopRequested says whether someone asked a running rollout to stop, and
// who.
func (s *Service) stopRequested(id string) (string, bool) {
	s.stopMu.Lock()
	defer s.stopMu.Unlock()
	by, ok := s.stopping[id]
	return by, ok
}

// stopped forgets a stop request once its rollout has ended.
func (s *Service) stopped(id string) {
	s.stopMu.Lock()
	defer s.stopMu.Unlock()
	delete(s.stopping, id)
	delete(s.runningHere, id)
}

func asFail(err error) *action.Error {
	var e *action.Error
	if errors.As(err, &e) {
		return e
	}
	return action.Fail("failed", err.Error())
}

// summary says which steps ran, which rolled back and which didn't start.
func summary(ro Rollout) string {
	var ran, back, not []string
	for _, st := range ro.Steps {
		switch st.State {
		case StepSucceeded:
			ran = append(ran, st.Name)
		case StepRolledBack:
			back = append(back, st.Name)
		case StepNotStarted:
			not = append(not, st.Name)
		}
	}
	parts := []string{}
	if len(ran) > 0 {
		parts = append(parts, msg.New("updates.sum_ran", "names", joinNames(ran)).Text())
	}
	if len(back) > 0 {
		parts = append(parts, msg.New("updates.sum_back", "names", joinNames(back)).Text())
	}
	if len(not) > 0 {
		parts = append(parts, msg.New("updates.sum_not", "names", joinNames(not)).Text())
	}
	return strings.Join(parts, " ")
}

// ── storage ──────────────────────────────────────────────────────────────

// ErrNoRollout is a rollout that doesn't exist.
var ErrNoRollout = errors.New("updates: no such rollout")

func newRolloutID() string { return "rol_" + strings.ToLower(crand.Text()) }

func (s *Service) insertRollout(ctx context.Context, ro Rollout) error {
	steps, _ := json.Marshal(ro.Steps)
	var by, window any
	if ro.CreatedBy != "" {
		by = ro.CreatedBy
	}
	if ro.WindowID != "" {
		window = ro.WindowID
	}
	_, err := s.st.Writer().ExecContext(ctx, `INSERT INTO rollouts (id, created_by, plan_hash, state, window_id, scheduled_for, steps, created_at, started_at, finished_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, ro.ID, by, ro.PlanHash, string(ro.State), window, millis(ro.ScheduledFor), string(steps),
		ro.CreatedAt.UnixMilli(), millis(ro.StartedAt), millis(ro.FinishedAt))
	if err != nil {
		return fmt.Errorf("updates: save rollout: %w", err)
	}
	return nil
}

// claim starts a scheduled or waiting rollout, once: false when it isn't
// either any more.
func (s *Service) claim(ctx context.Context, ro Rollout) (bool, error) {
	steps, _ := json.Marshal(ro.Steps)
	res, err := s.st.Writer().ExecContext(ctx, `UPDATE rollouts SET state = 'running', plan_hash = ?, steps = ?, started_at = ? WHERE id = ? AND state IN ('scheduled', 'waiting')`,
		ro.PlanHash, string(steps), s.now().UnixMilli(), ro.ID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func (s *Service) saveSteps(ctx context.Context, ro *Rollout) {
	steps, _ := json.Marshal(ro.Steps)
	if _, err := s.st.Writer().ExecContext(ctx, `UPDATE rollouts SET state = ?, steps = ?, finished_at = ? WHERE id = ?`,
		string(ro.State), string(steps), millis(ro.FinishedAt), ro.ID); err != nil {
		s.log.Error("updates: save rollout", "rollout", ro.ID, "err", err)
	}
}

func millis(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func fromMillis(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

const rolloutCols = `r.id, COALESCE(r.created_by, ''), COALESCE(u.username, ''), r.plan_hash, r.state, COALESCE(r.window_id, ''), r.scheduled_for, r.steps, r.created_at, r.started_at, r.finished_at`

func scanRollout(row interface{ Scan(...any) error }) (Rollout, string, error) {
	var ro Rollout
	var userID, steps, state string
	var sched, created, started, finished int64
	if err := row.Scan(&ro.ID, &userID, &ro.CreatedBy, &ro.PlanHash, &state, &ro.WindowID, &sched, &steps, &created, &started, &finished); err != nil {
		return ro, "", err
	}
	ro.State = RolloutState(state)
	_ = json.Unmarshal([]byte(steps), &ro.Steps)
	if ro.Steps == nil {
		ro.Steps = []RolloutStep{}
	}
	ro.ScheduledFor, ro.CreatedAt, ro.StartedAt, ro.FinishedAt = fromMillis(sched), fromMillis(created), fromMillis(started), fromMillis(finished)
	return ro, userID, nil
}

// Rollout reads one rollout. CreatedBy is the username.
func (s *Service) Rollout(ctx context.Context, id string) (Rollout, error) {
	ro, _, err := scanRollout(s.st.Reader().QueryRowContext(ctx, `SELECT `+rolloutCols+` FROM rollouts r LEFT JOIN users u ON u.id = r.created_by WHERE r.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return ro, ErrNoRollout
	}
	if err == nil {
		one := []Rollout{ro}
		s.cancelling(one)
		ro = one[0]
	}
	return ro, err
}

// Rollouts lists the rollouts still to run and those of the last 30 days,
// newest first, at most 50.
func (s *Service) Rollouts(ctx context.Context) ([]Rollout, error) {
	rows, err := s.st.Reader().QueryContext(ctx, `SELECT `+rolloutCols+` FROM rollouts r LEFT JOIN users u ON u.id = r.created_by
		WHERE r.state IN ('scheduled', 'waiting', 'running') OR r.created_at >= ? ORDER BY r.created_at DESC, r.id LIMIT 50`,
		s.now().Add(-30*24*time.Hour).UnixMilli())
	if err != nil {
		return nil, fmt.Errorf("updates: read rollouts: %w", err)
	}
	defer rows.Close()
	out := []Rollout{}
	for rows.Next() {
		ro, _, err := scanRollout(rows)
		if err != nil {
			return nil, fmt.Errorf("updates: read rollouts: %w", err)
		}
		out = append(out, ro)
	}
	s.cancelling(out)
	return out, rows.Err()
}

// CancelRollout cancels a scheduled or waiting rollout at once. A running
// one stops before its next step: the step in progress finishes, or rolls
// back on its own, and the rest don't start; Rollout.Cancelling says so
// until it has. by is who asked, by name.
func (s *Service) CancelRollout(ctx context.Context, id, by string) (Rollout, error) {
	res, err := s.st.Writer().ExecContext(ctx, `UPDATE rollouts SET state = 'cancelled', finished_at = ? WHERE id = ? AND state IN ('scheduled', 'waiting')`,
		s.now().UnixMilli(), id)
	if err != nil {
		return Rollout{}, err
	}
	ro, err := s.Rollout(ctx, id)
	if err != nil {
		return ro, err
	}
	if n, _ := res.RowsAffected(); n == 1 {
		s.resolveItem(waitingItemID(id))
		return ro, nil
	}
	if ro.State != RolloutRunning {
		return ro, errNotPending
	}
	s.stopMu.Lock()
	defer s.stopMu.Unlock()
	if !s.runningHere[id] {
		// Its task isn't in this hub (it stopped mid-rollout): the next
		// start ends it (RecoverRollouts).
		return ro, errNotPending
	}
	if _, ok := s.stopping[id]; ok {
		return ro, errStopping
	}
	s.stopping[id] = by
	ro.Cancelling = true
	return ro, nil
}

var (
	errNotPending = errors.New("updates: the rollout isn't scheduled, waiting or running")
	errStopping   = errors.New("updates: the rollout is already stopping")
)

// cancelling marks the rollouts someone asked to stop.
func (s *Service) cancelling(ros []Rollout) {
	s.stopMu.Lock()
	defer s.stopMu.Unlock()
	for i := range ros {
		if _, ok := s.stopping[ros[i].ID]; ok && ros[i].State == RolloutRunning {
			ros[i].Cancelling = true
		}
	}
}

// ── the scheduler ────────────────────────────────────────────────────────

// RunDue starts the scheduled rollouts whose window is open, as the
// person who scheduled them, when their plan's hash still matches; one
// whose plan changed, or whose person can't run it any more, waits for a
// person, with an Inbox item. A window that closed before its rollout
// could start moves it to its next opening (weekly) or makes it wait.
func (s *Service) RunDue(ctx context.Context) {
	rows, err := s.st.Reader().QueryContext(ctx, `SELECT `+rolloutCols+` FROM rollouts r LEFT JOIN users u ON u.id = r.created_by
		WHERE r.state = 'scheduled' AND r.scheduled_for <= ? ORDER BY r.scheduled_for, r.id`, s.now().UnixMilli())
	if err != nil {
		s.log.Warn("updates: read scheduled rollouts", "err", err)
		return
	}
	type due struct {
		ro   Rollout
		user string
	}
	var list []due
	for rows.Next() {
		ro, user, err := scanRollout(rows)
		if err != nil {
			rows.Close()
			s.log.Warn("updates: read scheduled rollouts", "err", err)
			return
		}
		list = append(list, due{ro, user})
	}
	rows.Close()
	now := s.now()
	for _, d := range list {
		if t, ok := s.launched[d.ro.ID]; ok && now.Sub(t) < 10*time.Minute {
			continue // started; its task claims it
		}
		s.runDue(ctx, d.ro, d.user, now)
	}
	for id, t := range s.launched {
		if now.Sub(t) >= 10*time.Minute {
			delete(s.launched, id)
		}
	}
}

func (s *Service) runDue(ctx context.Context, ro Rollout, userID string, now time.Time) {
	var w *MaintenanceWindow
	if ro.WindowID != "" {
		got, err := s.Window(ctx, ro.WindowID)
		if err != nil {
			s.wait(ctx, ro, msg.New("updates.wait_no_window"))
			return
		}
		w = &got
	}
	if w != nil && !w.Open(now) {
		start, _, ok := w.Occurrence(now)
		if !ok {
			s.wait(ctx, ro, msg.New("updates.wait_window_closed", "window", w.Name))
			return
		}
		if start.After(now) {
			if _, err := s.st.Writer().ExecContext(ctx, `UPDATE rollouts SET scheduled_for = ? WHERE id = ? AND state = 'scheduled'`, start.UnixMilli(), ro.ID); err != nil {
				s.log.Warn("updates: reschedule rollout", "err", err)
			}
		}
		return
	}
	var targets []string
	for _, st := range ro.Steps {
		targets = append(targets, st.Target)
	}
	p, err := s.Plan(ctx, targets)
	if err != nil {
		s.log.Warn("updates: plan a scheduled rollout", "err", err)
		return
	}
	if p.Hash != ro.PlanHash {
		s.wait(ctx, ro, msg.New("updates.wait_changed"))
		return
	}
	if s.Engine == nil || s.users == nil || userID == "" {
		s.wait(ctx, ro, msg.New("updates.wait_no_person"))
		return
	}
	u, err := s.users(ctx, userID)
	if err != nil || u.Disabled {
		if err != nil && !errors.Is(err, auth.ErrUserNotFound) {
			s.log.Warn("updates: read a rollout's person", "err", err)
			return
		}
		s.wait(ctx, ro, msg.New("updates.wait_no_person"))
		return
	}
	actor := action.Actor{Kind: action.KindUser, ID: u.ID, Name: u.Username, Role: u.Role, Via: ViaSchedule}
	params, _ := json.Marshal(RolloutParams{Rollout: ro.ID})
	s.launched[ro.ID] = now
	_, apv, err := s.Engine.RunScheduled(ctx, actor, action.PlanRequest{Action: ActRollout, Target: RolloutTarget, Params: params})
	switch {
	case err != nil:
		delete(s.launched, ro.ID)
		var ref *action.Refusal
		if errors.As(err, &ref) {
			s.wait(ctx, ro, msg.New("updates.wait_refused", "reason", ref.Message))
			return
		}
		s.log.Warn("updates: run a scheduled rollout", "rollout", ro.ID, "err", err)
	case apv != nil:
		s.wait(ctx, ro, msg.New("updates.wait_approval"))
	}
}

// wait makes a scheduled rollout wait for a person, with an Inbox item
// saying why.
func (s *Service) wait(ctx context.Context, ro Rollout, why msg.M) {
	res, err := s.st.Writer().ExecContext(ctx, `UPDATE rollouts SET state = 'waiting' WHERE id = ? AND state = 'scheduled'`, ro.ID)
	if err != nil {
		s.log.Warn("updates: rollout waits", "err", err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return
	}
	s.putItem(item{
		ID: waitingItemID(ro.ID), Scenario: "rollout-waiting", Class: "problem", Severity: "info", Status: "open", Phase: "open",
		Title:   msg.New("updates.waiting_title").Text(),
		Summary: why.Text(),
		Target:  integration.HubResource,
		Facts:   map[string]any{"rollout": ro.ID},
		Causes:  []any{}, BlastRadius: []any{},
		Suggested:    map[string]any{"id": ReviewAction, "label": msg.New("updates.review").Text(), "tier": "T0", "target": integration.HubResource},
		ResolvesWhen: msg.New("updates.waiting_resolves").Text(),
	})
}

// RecoverRollouts ends the rollouts a stopped hub left running: their task
// was interrupted, so the step that was running is unknown and the rest
// didn't start.
func (s *Service) RecoverRollouts(ctx context.Context) {
	rows, err := s.st.Reader().QueryContext(ctx, `SELECT `+rolloutCols+` FROM rollouts r LEFT JOIN users u ON u.id = r.created_by WHERE r.state = 'running'`)
	if err != nil {
		s.log.Warn("updates: read running rollouts", "err", err)
		return
	}
	var list []Rollout
	for rows.Next() {
		if ro, _, err := scanRollout(rows); err == nil {
			list = append(list, ro)
		}
	}
	rows.Close()
	for _, ro := range list {
		for i := range ro.Steps {
			switch ro.Steps[i].State {
			case StepRunning:
				ro.Steps[i].State, ro.Steps[i].Reason = StepRolledBack, msg.New("updates.interrupted").Text()
			case StepPending:
				ro.Steps[i].State = StepNotStarted
			}
		}
		ro.State, ro.FinishedAt = RolloutFailed, s.now()
		s.saveSteps(ctx, &ro)
	}
}

// ── Inbox items ──────────────────────────────────────────────────────────

func waitingItemID(id string) string { return "rollout.waiting." + id }
func failedItemID(id string) string  { return "rollout.failed." + id }

// failedItem tells the Inbox a rollout stopped: which ran, which rolled
// back, which didn't start.
func (s *Service) failedItem(ro Rollout) {
	var back string
	for _, st := range ro.Steps {
		if st.State == StepRolledBack {
			back = st.Name
			break
		}
	}
	title := msg.New("updates.failed_title").Text()
	if back != "" {
		title = msg.New("updates.failed_title_name", "name", back).Text()
	}
	s.putItem(item{
		ID: failedItemID(ro.ID), Scenario: "rollout-failed", Class: "problem", Severity: "attention", Status: "open", Phase: "open",
		Title: title, Summary: summary(ro), Target: integration.HubResource,
		Facts:  map[string]any{"rollout": ro.ID},
		Causes: []any{}, BlastRadius: []any{},
		Suggested:    map[string]any{"id": ReviewAction, "label": msg.New("updates.review").Text(), "tier": "T0", "target": integration.HubResource},
		ResolvesWhen: msg.New("updates.failed_resolves").Text(),
	})
}

func (s *Service) putItem(in item) {
	if s.inbox == nil {
		return
	}
	now := s.now().UTC()
	in.OpenedAt, in.UpdatedAt = now, now
	_, _ = s.inbox.Update(func(tx *livedoc.Tx) error {
		tx.Put(incidentsColl, in.ID, in)
		return nil
	})
}

// resolveItem resolves an open item, if there is one.
func (s *Service) resolveItem(id string) {
	if s.inbox == nil {
		return
	}
	now := s.now().UTC()
	_, _ = s.inbox.Update(func(tx *livedoc.Tx) error {
		var cur item
		if raw := tx.Item(incidentsColl, id); raw != nil && json.Unmarshal(raw, &cur) == nil && cur.Status == "open" {
			cur.Status, cur.Phase, cur.UpdatedAt, cur.ResolvedAt, cur.ResolvedBy = "resolved", "resolved", now, &now, "system"
			tx.Put(incidentsColl, id, cur)
		}
		return nil
	})
}

// settleItems resolves rollout items that are done with: a failed
// rollout's once a later rollout succeeded or a day has passed; a waiting
// one's once its rollout isn't waiting.
func (s *Service) settleItems(ctx context.Context) {
	if s.inbox == nil {
		return
	}
	_, doc := s.inbox.Snapshot()
	coll, _ := doc[incidentsColl].(map[string]json.RawMessage)
	var ids []string
	for id := range coll {
		if strings.HasPrefix(id, "rollout.") {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	if len(ids) == 0 {
		return
	}
	ros, err := s.Rollouts(ctx)
	if err != nil {
		return
	}
	state := map[string]Rollout{}
	var lastOK time.Time
	for _, ro := range ros {
		state[ro.ID] = ro
		if ro.State == RolloutSucceeded && ro.FinishedAt.After(lastOK) {
			lastOK = ro.FinishedAt
		}
	}
	now := s.now()
	for _, id := range ids {
		var it item
		if json.Unmarshal(coll[id], &it) != nil || it.Status != "open" {
			continue
		}
		switch {
		case strings.HasPrefix(id, "rollout.waiting."):
			ro, ok := state[strings.TrimPrefix(id, "rollout.waiting.")]
			if !ok || ro.State != RolloutWaiting {
				s.resolveItem(id)
			}
		case strings.HasPrefix(id, "rollout.failed."):
			if lastOK.After(it.OpenedAt) || now.Sub(it.OpenedAt) >= 24*time.Hour {
				s.resolveItem(id)
			}
		}
	}
}
