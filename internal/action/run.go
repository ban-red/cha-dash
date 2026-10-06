package action

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"dash.cha.sh/internal/audit"
	"dash.cha.sh/internal/livedoc"
)

// ── tasks ────────────────────────────────────────────────────────────────

// Task returns a task the actor may see: their own, or anyone's with
// ScopeTasksAll. Otherwise it's ErrNotFound, so IDs don't leak.
func (e *Engine) Task(ctx context.Context, a Actor, id string) (Task, error) {
	e.mu.Lock()
	lt, ok := e.live[id]
	var t Task
	var owner string
	if ok {
		t, owner = lt.task.clone(), lt.actorID
	}
	e.mu.Unlock()
	if !ok {
		var err error
		if t, owner, err = e.storedTask(ctx, id); errors.Is(err, sql.ErrNoRows) {
			return Task{}, ErrNotFound
		} else if err != nil {
			return Task{}, err
		}
	}
	if owner != a.ID && e.policy.Authorize(ctx, a, ScopeTasksAll, "", T0) != nil {
		return Task{}, ErrNotFound
	}
	return t, nil
}

// task returns a task without an access check.
func (e *Engine) task(ctx context.Context, id string) (Task, error) {
	e.mu.Lock()
	if lt, ok := e.live[id]; ok {
		t := lt.task.clone()
		e.mu.Unlock()
		return t, nil
	}
	e.mu.Unlock()
	t, _, err := e.storedTask(ctx, id)
	return t, err
}

func (e *Engine) storedTask(ctx context.Context, id string) (Task, string, error) {
	var doc, owner string
	if err := e.st.Reader().QueryRowContext(ctx, `SELECT document, actor_id FROM tasks WHERE id = ?`, id).Scan(&doc, &owner); err != nil {
		return Task{}, "", err
	}
	var t Task
	err := json.Unmarshal([]byte(doc), &t)
	return t, owner, err
}

// Cancel stops a running task: the actor's own, or anyone's with
// ScopeTasksAll, and only when its action is Cancellable. It ends the task's
// context; the task then records itself as cancelled, with its own audit
// record, as it stops. The request is audited as ActionCancel, refusals
// included. A task the actor may not see is ErrNotFound, so IDs don't leak;
// the other refusals are *Refusal: 409 finished, 409 not_cancellable.
func (e *Engine) Cancel(ctx context.Context, a Actor, id string) error {
	params, _ := json.Marshal(map[string]string{"task": clip(id, 100)})
	record := func(target, action string, result string, r *Refusal) {
		entry := audit.Entry{Actor: auditActor(a), Action: ActionCancel, Target: clip(target, 500), Params: params, Tier: T0.String(), Result: result}
		if r != nil {
			entry.Error = &audit.Error{Code: r.Code, Message: r.Message}
		}
		if result == audit.OK {
			entry.Task = id
		}
		if _, err := e.audit.Append(context.WithoutCancel(ctx), entry); err != nil {
			e.log.Error("action: auditing a cancel failed", "task", id, "action", action, "err", err)
		}
	}
	t, err := e.Task(ctx, a, id)
	if errors.Is(err, ErrNotFound) {
		record("", "", audit.Refused, &Refusal{Status: http.StatusNotFound, Code: "not_found", Message: "There's no such task."})
		return err
	} else if err != nil {
		return err
	}
	refuse := func(r *Refusal) error {
		record(t.Target, t.Action, audit.Refused, r)
		return r
	}
	if d, ok := e.reg.Get(t.Action); !ok || !d.Cancellable {
		return refuse(&Refusal{Status: http.StatusConflict, Code: "not_cancellable", Message: fmt.Sprintf("%s can't be stopped once it has started.", t.Label)})
	}
	e.mu.Lock()
	lt, ok := e.live[id]
	running := ok && lt.cancel != nil
	if running {
		lt.cancel()
	}
	e.mu.Unlock()
	if !running {
		return refuse(&Refusal{Status: http.StatusConflict, Code: "finished", Message: fmt.Sprintf("%s has already finished.", t.Label)})
	}
	record(t.Target, t.Action, audit.OK, nil)
	return nil
}

// start runs a task in the background. It reports false when the engine is
// closing.
func (e *Engine) start(d *Def, r Request, task Task, tier Tier, undo *Undo, created time.Time) bool {
	ctx, cancel := context.WithCancel(e.ctx)
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		cancel()
		return false
	}
	e.live[task.ID] = &liveTask{task: task, actorID: r.Actor.ID, created: created, cancel: cancel}
	e.wg.Add(1)
	e.mu.Unlock()
	e.publish(task.ID)
	go func() {
		defer e.wg.Done()
		defer cancel()
		e.run(ctx, d, r, task.ID, tier, undo)
	}()
	return true
}

// run executes, verifies and audits one task.
func (e *Engine) run(ctx context.Context, d *Def, r Request, id string, tier Tier, undo *Undo) {
	run := &TaskRun{e: e, id: id}
	started := e.now().UTC().Truncate(time.Millisecond)
	e.updateTask(id, true, func(t *Task) { t.State, t.StartedAt = StateRunning, &started })

	err := e.call(ctx, d.timeout(), func(ctx context.Context) error { return d.Execute(ctx, r, run) })
	if err == nil && d.Verify != nil {
		e.updateTask(id, true, func(t *Task) { t.State = StateVerifying })
		err = e.call(ctx, d.verifyTimeout(), func(ctx context.Context) error { return d.Verify(ctx, r, run) })
		if err != nil && !isCancel(ctx, err) {
			var ae *Error
			if !errors.As(err, &ae) {
				err = Fail("verify_failed", "It ran, but the check afterwards failed: "+err.Error())
			}
		}
	}

	state, result := StateSucceeded, audit.OK
	var fail *Error
	switch {
	case err == nil:
	case ctx.Err() != nil && e.ctx.Err() != nil:
		state, result, fail = StateFailed, audit.Failed, Fail("interrupted", "The hub shut down while this was running.")
	case ctx.Err() != nil:
		state, result, fail = StateCancelled, audit.Failed, Fail("cancelled", "Cancelled.")
	default:
		state, result, fail = StateFailed, audit.Failed, errorOf(err)
	}
	finished := e.now().UTC().Truncate(time.Millisecond)
	var auditErr *audit.Error
	if fail != nil {
		auditErr = &audit.Error{Code: fail.Code, Message: fail.Message}
	}
	var secrets []string
	if d.Params != nil {
		secrets = d.Params.Secrets()
	}
	// The audit record goes first: a task never shows as finished without
	// one. context.Background, since ctx may be what just ended.
	ev, aerr := e.audit.Append(context.Background(), audit.Entry{
		Actor: auditActor(r.Actor), Action: d.ID, Target: r.Target.ID, Params: r.Params, Secrets: secrets,
		Tier: tier.String(), Result: result, Error: auditErr, Duration: finished.Sub(started), Task: id,
	})
	if aerr != nil {
		e.log.Error("action: auditing a task failed", "task", id, "err", aerr)
	}
	e.finish(id, r.Target.ID, func(t *Task) {
		t.State, t.FinishedAt, t.Error = state, &finished, fail
		if state == StateSucceeded {
			t.Undo = undo
		}
		if aerr == nil {
			t.Audit = &ev.ID
		}
	})
}

// finish records a task's end. The task shows as finished, its lock is
// released and the topic is trimmed in one step, so whoever sees it
// finished can lock the resource again.
func (e *Engine) finish(id, resource string, fn func(t *Task)) {
	e.mu.Lock()
	lt, ok := e.live[id]
	if !ok {
		e.mu.Unlock()
		return
	}
	fn(&lt.task)
	lt.cancel = nil
	if e.locks[resource] == id {
		delete(e.locks, resource)
	}
	t := lt.task.clone()
	drop := e.trimLocked()
	e.mu.Unlock()

	e.save(t)
	e.tasks.Update(func(tx *livedoc.Tx) error {
		if !slices.Contains(drop, id) {
			tx.Put(tasksColl, id, t)
		}
		for _, d := range drop {
			tx.Delete(tasksColl, d)
		}
		return nil
	})
}

// call runs fn with a timeout, turning a panic into an error.
func (e *Engine) call(ctx context.Context, timeout time.Duration, fn func(context.Context) error) (err error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	defer func() {
		if p := recover(); p != nil {
			e.log.Error("action: panic", "panic", p)
			err = Fail("internal", "Cha Dash hit an internal error running this.")
		}
	}()
	err = fn(ctx)
	if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		var ae *Error
		if !errors.As(err, &ae) {
			err = Fail("timeout", fmt.Sprintf("It didn't finish within %s.", timeout))
		}
	}
	return err
}

func isCancel(ctx context.Context, err error) bool {
	return ctx.Err() != nil && errors.Is(err, context.Canceled)
}

func errorOf(err error) *Error {
	var ae *Error
	if errors.As(err, &ae) {
		return ae
	}
	return Fail("failed", err.Error())
}

// updateTask changes a live task and publishes it; persist also writes it
// to the tasks table (state and step changes, not every progress tick).
func (e *Engine) updateTask(id string, persist bool, fn func(t *Task)) {
	e.mu.Lock()
	lt, ok := e.live[id]
	if !ok {
		e.mu.Unlock()
		return
	}
	fn(&lt.task)
	t := lt.task.clone()
	e.mu.Unlock()
	e.publish(id)
	if persist {
		e.save(t)
	}
}

// save writes a task to the tasks table.
func (e *Engine) save(t Task) {
	doc, _ := json.Marshal(t)
	var finished any
	if t.FinishedAt != nil {
		finished = t.FinishedAt.UnixMilli()
	}
	if _, err := e.st.Writer().ExecContext(context.Background(), `UPDATE tasks SET state = ?, document = ?, finished_at = ? WHERE id = ?`,
		t.State, string(doc), finished, t.ID); err != nil {
		e.log.Error("action: saving a task failed", "task", t.ID, "err", err)
	}
}

// publish copies a live task into the topic.
func (e *Engine) publish(id string) {
	e.tasks.Update(func(tx *livedoc.Tx) error {
		e.mu.Lock()
		defer e.mu.Unlock()
		if lt, ok := e.live[id]; ok {
			tx.Put(tasksColl, id, lt.task)
		} else {
			tx.Delete(tasksColl, id)
		}
		return nil
	})
}

// trim keeps the topic to running tasks plus the last RecentTasks, at most
// MaxTasks, dropping the oldest finished ones first.
func (e *Engine) trim() {
	e.mu.Lock()
	drop := e.trimLocked()
	e.mu.Unlock()
	if len(drop) == 0 {
		return
	}
	e.tasks.Update(func(tx *livedoc.Tx) error {
		for _, id := range drop {
			tx.Delete(tasksColl, id)
		}
		return nil
	})
}

// trimLocked forgets the tasks trim drops and returns them. The caller
// holds e.mu and removes them from the topic.
func (e *Engine) trimLocked() []string {
	cutoff := e.now().Add(-RecentTasks)
	var finished []string
	for id, lt := range e.live {
		if lt.task.Finished() {
			finished = append(finished, id)
		}
	}
	slices.SortFunc(finished, func(a, b string) int { return e.live[a].created.Compare(e.live[b].created) })
	var drop []string
	over := len(e.live) - MaxTasks
	for _, id := range finished {
		if over > 0 || e.live[id].created.Before(cutoff) {
			drop = append(drop, id)
			delete(e.live, id)
			over--
		}
	}
	return drop
}

// recover marks tasks a previous run left unfinished as failed, with an
// audit record each: the hub stopped while they ran.
func (e *Engine) recover(ctx context.Context) error {
	rows, err := e.st.Reader().QueryContext(ctx, `SELECT t.id, t.document, p.actor, p.params, p.tier, t.created_at FROM tasks t JOIN plans p ON p.id = t.plan_id
		WHERE t.finished_at IS NULL`)
	if err != nil {
		return fmt.Errorf("action: %w", err)
	}
	type orphan struct {
		id      string
		task    Task
		actor   Actor
		params  json.RawMessage
		tier    string
		created int64
	}
	var orphans []orphan
	for rows.Next() {
		var o orphan
		var doc, actor, params string
		if err := rows.Scan(&o.id, &doc, &actor, &params, &o.tier, &o.created); err != nil {
			rows.Close()
			return err
		}
		if json.Unmarshal([]byte(doc), &o.task) != nil || json.Unmarshal([]byte(actor), &o.actor) != nil {
			continue
		}
		o.params = json.RawMessage(params)
		orphans = append(orphans, o)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, o := range orphans {
		now := e.now().UTC().Truncate(time.Millisecond)
		fail := Fail("interrupted", "The hub stopped while this was running, so whether it finished isn't known.")
		var secrets []string
		if d, ok := e.reg.Get(o.task.Action); ok && d.Params != nil {
			secrets = d.Params.Secrets()
		}
		var dur time.Duration
		if o.task.StartedAt != nil {
			dur = now.Sub(*o.task.StartedAt)
		}
		ev, err := e.audit.Append(ctx, audit.Entry{
			Actor: auditActor(o.actor), Action: o.task.Action, Target: o.task.Target, Params: o.params, Secrets: secrets,
			Tier: o.tier, Result: audit.Failed, Error: &audit.Error{Code: fail.Code, Message: fail.Message}, Duration: dur, Task: o.id,
		})
		if err != nil {
			return err
		}
		o.task.State, o.task.FinishedAt, o.task.Error, o.task.Audit = StateFailed, &now, fail, &ev.ID
		doc, _ := json.Marshal(o.task)
		if _, err := e.st.Writer().ExecContext(ctx, `UPDATE tasks SET state = ?, document = ?, finished_at = ? WHERE id = ?`,
			o.task.State, string(doc), now.UnixMilli(), o.id); err != nil {
			return err
		}
		e.log.Warn("action: a task was interrupted by a restart", "task", o.id, "action", o.task.Action)
	}
	return nil
}

// load fills the topic with the last RecentTasks of tasks.
func (e *Engine) load(ctx context.Context) error {
	rows, err := e.st.Reader().QueryContext(ctx, `SELECT id, actor_id, document, created_at FROM tasks WHERE created_at >= ? ORDER BY created_at DESC LIMIT ?`,
		e.now().Add(-RecentTasks).UnixMilli(), MaxTasks)
	if err != nil {
		return fmt.Errorf("action: %w", err)
	}
	defer rows.Close()
	e.mu.Lock()
	for rows.Next() {
		var id, owner, doc string
		var created int64
		if err := rows.Scan(&id, &owner, &doc, &created); err != nil {
			e.mu.Unlock()
			return err
		}
		var t Task
		if json.Unmarshal([]byte(doc), &t) == nil {
			e.live[id] = &liveTask{task: t, actorID: owner, created: time.UnixMilli(created)}
		}
	}
	ids := make([]string, 0, len(e.live))
	for id := range e.live {
		ids = append(ids, id)
	}
	e.mu.Unlock()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		e.publish(id)
	}
	return nil
}

// prune drops old tasks from the topic and old rows from the working
// tables.
func (e *Engine) prune(ctx context.Context) {
	e.trim()
	now := e.now()
	for _, q := range []struct {
		sql string
		arg int64
	}{
		{`DELETE FROM plans WHERE approved_at IS NULL AND expires_at < ?`, now.Add(-planRetention).UnixMilli()},
		{`DELETE FROM plans WHERE created_at < ? AND id NOT IN (SELECT plan_id FROM tasks WHERE finished_at IS NULL)`, now.Add(-taskRetention).UnixMilli()},
		{`DELETE FROM idempotency_keys WHERE created_at < ?`, now.Add(-idempotencyRetention).UnixMilli()},
	} {
		if _, err := e.st.Writer().ExecContext(ctx, q.sql, q.arg); err != nil && ctx.Err() == nil {
			e.log.Error("action: pruning failed", "err", err)
		}
	}
}
