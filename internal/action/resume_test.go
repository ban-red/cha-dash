package action

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"dash.cha.sh/internal/audit"
)

// waitRunning waits until a task's row says it's running, as it would when
// the hub died mid-task.
func (e *env) waitRunning(id string) {
	e.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var state string
		_ = e.st.Reader().QueryRow(`SELECT state FROM tasks WHERE id = ?`, id).Scan(&state)
		if state == StateRunning {
			return
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("task %s never ran", id)
		}
		time.Sleep(time.Millisecond)
	}
}

// A task whose Def resumes is finished through Resume after a restart, with
// its audit record, its undo and its lock; one whose Def doesn't is still
// interrupted. A restart while Resume runs leaves it to the next start.
func TestRestartResumesTasks(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	slow := func(ctx context.Context, _ Request, _ *TaskRun) error {
		select {
		case <-block:
		case <-ctx.Done():
		}
		return nil
	}
	release := make(chan struct{})
	var mu sync.Mutex
	var handed []Task
	update := Def{ID: "x.update", Title: "Update", Tier: T1, Targets: []string{"app"}, Execute: slow, Resumes: true,
		Undo: func(Request) *Undo { return &Undo{Action: "x.update", Params: []byte(`{}`), Label: "Go back"} },
		Resume: func(ctx context.Context, task Task, run *TaskRun) error {
			mu.Lock()
			handed = append(handed, task)
			mu.Unlock()
			run.Step("Ask the node how it ended")
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}}
	var calls int
	rollback := Def{ID: "x.selfupdate", Title: "Update itself", Tier: T1, Targets: []string{"vm"}, Execute: slow, Resumes: true,
		Resume: func(ctx context.Context, _ Task, _ *TaskRun) error {
			mu.Lock()
			calls++
			n := calls
			mu.Unlock()
			if n == 1 { // the hub stops again while it asks
				<-ctx.Done()
				return ctx.Err()
			}
			return Fail("rolled_back", "It failed its health check and rolled back.")
		}}
	plain := Def{ID: "x.slow", Tier: T1, Targets: []string{"host"}, Execute: slow}
	e := newEnv(t, OwnerOnly{}, update, rollback, plain)

	start := func(action, target string) Task {
		t.Helper()
		task, err := e.approve(owner, e.mustPlan(owner, action, target), "", "")
		if err != nil {
			t.Fatal(err)
		}
		e.waitRunning(task.ID)
		return task
	}
	up := start("x.update", "app:plex")
	self := start("x.selfupdate", "proxmox:i_x/qemu/201")
	other := start("x.slow", "proxmox:i_x/node/io")

	e.start() // a second engine on the same database: the restart

	// Not resumable: interrupted at once, as before.
	got, err := e.e.Task(t.Context(), owner, other.ID)
	if err != nil || got.State != StateFailed || got.Error.Code != "interrupted" || got.Audit == nil {
		t.Fatalf("plain task after restart: %+v, %v", got, err)
	}
	// Resuming: running, holding its target, until Resume answers.
	if got, _ := e.e.Task(t.Context(), owner, up.ID); got.State != StateRunning || got.Finished() {
		t.Fatalf("resuming task: %+v", got)
	}
	_, err = e.approve(owner, e.mustPlan(owner, "x.update", "app:plex"), "", "")
	refusal(t, err, "locked")
	close(release)
	done := e.wait(up.ID)
	if done.State != StateSucceeded || done.Error != nil || done.Undo == nil || done.Step != "Ask the node how it ended" {
		t.Fatalf("resumed task: %+v", done)
	}
	mu.Lock()
	if len(handed) != 1 || handed[0].ID != up.ID || handed[0].Target != "app:plex" || handed[0].StartedAt == nil {
		t.Fatalf("Resume was handed %+v", handed)
	}
	mu.Unlock()
	var ev audit.Event
	for _, x := range e.events() {
		if x.Task == up.ID {
			ev = x
		}
	}
	if ev.Result != audit.OK || ev.Action != "x.update" || ev.Actor.ID != owner.ID || *done.Audit != ev.ID {
		t.Fatalf("audit = %+v", ev)
	}
	var state string
	var finished *int64
	if err := e.st.Reader().QueryRow(`SELECT state, finished_at FROM tasks WHERE id = ?`, up.ID).Scan(&state, &finished); err != nil || state != StateSucceeded || finished == nil {
		t.Fatalf("stored: %s %v %v", state, finished, err)
	}
	// Its lock is free again.
	if _, err := e.approve(owner, e.mustPlan(owner, "x.update", "app:plex"), "", ""); err != nil {
		t.Fatalf("after it finished: %v", err)
	}

	// The hub stops while Resume asks: the task stays running for the next
	// start, which resumes it again.
	e.e.Close()
	if err := e.st.Reader().QueryRow(`SELECT state, finished_at FROM tasks WHERE id = ?`, self.ID).Scan(&state, &finished); err != nil || state != StateRunning || finished != nil {
		t.Fatalf("after a second stop: %s %v %v", state, finished, err)
	}
	e.start()
	done = e.wait(self.ID)
	if done.State != StateFailed || done.Error == nil || done.Error.Code != "rolled_back" || done.Undo != nil {
		t.Fatalf("resumed again: %+v", done)
	}
	if rep, _ := e.audit.Verify(t.Context()); !rep.OK {
		t.Fatalf("audit chain: %+v", rep)
	}
}

// A task that never started (still queued) isn't resumed: whether it ran
// at all isn't the node's to say.
func TestRestartDoesNotResumeQueuedTasks(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	resumed := false
	d := Def{ID: "x.update", Tier: T1, Targets: []string{"app"}, Resumes: true,
		Execute: func(ctx context.Context, _ Request, _ *TaskRun) error {
			select {
			case <-block:
			case <-ctx.Done():
			}
			return nil
		},
		Resume: func(context.Context, Task, *TaskRun) error { resumed = true; return nil }}
	e := newEnv(t, OwnerOnly{}, d)
	task, err := e.approve(owner, e.mustPlan(owner, "x.update", "app:plex"), "", "")
	if err != nil {
		t.Fatal(err)
	}
	e.waitRunning(task.ID)
	if _, err := e.st.Writer().Exec(`UPDATE tasks SET state = ?, document = json_remove(json_set(document, '$.state', ?), '$.started_at') WHERE id = ?`,
		StateQueued, StateQueued, task.ID); err != nil {
		t.Fatal(err)
	}
	e.start()
	got, err := e.e.Task(t.Context(), owner, task.ID)
	if err != nil || got.State != StateFailed || got.Error.Code != "interrupted" || resumed {
		t.Fatalf("queued task after restart: %+v, %v (resumed %v)", got, err, resumed)
	}
}

func TestRegisterRefusesResumesWithoutResume(t *testing.T) {
	err := NewRegistry().Register("test", Def{ID: "x.update", Tier: T1, Resumes: true,
		Execute: func(context.Context, Request, *TaskRun) error { return nil }})
	if err == nil || !strings.Contains(err.Error(), "no Resume") {
		t.Fatalf("Register = %v", err)
	}
}

// A plan made with RequireApproval waits for a person whoever made it: a
// person in a session, or a sync, whose kind the policy never makes wait.
func TestRequireApprovalWaits(t *testing.T) {
	var calls []string
	e, _ := newTokenEnv(t, reboot(&calls))
	sync := Actor{Kind: KindSync, ID: "sync_media", Name: "media (git)", Role: "admin", User: "u_owner", UserName: "sam"}
	for _, a := range []Actor{owner, sync} {
		p, err := e.e.Plan(t.Context(), a, PlanRequest{Action: "proxmox.guest.reboot", Target: "proxmox:i_x/qemu/201", RequireApproval: true})
		if err != nil {
			t.Fatalf("%s: plan: %v", a.Kind, err)
		}
		if !p.RequireApproval {
			t.Fatalf("%s: plan %+v", a.Kind, p)
		}
		task, apv, err := e.e.ApproveOrSubmit(t.Context(), a, p.ID, ApproveRequest{Hash: p.Hash}, "")
		if err != nil || apv == nil || task.ID != "" || apv.State != ApprovalAwaiting || !apv.Plan.RequireApproval {
			t.Fatalf("%s: submit: %+v %+v %v", a.Kind, task, apv, err)
		}
		if apv.Originator.Kind != a.Kind || apv.Originator.ID != a.ID {
			t.Fatalf("%s: originator %+v", a.Kind, apv.Originator)
		}
		it := e.item(apv.ID)
		if s, _ := it["summary"].(string); a.Kind == KindSync && !strings.HasPrefix(s, "Planned by a git sync") {
			t.Fatalf("summary %q", s)
		}
		e.clock.Advance(time.Second)
		task, err = e.e.Grant(t.Context(), stepUpAt(owner, e.clock.Now()), apv.ID, ApproveRequest{Hash: p.Hash})
		if err != nil {
			t.Fatalf("%s: grant: %v", a.Kind, err)
		}
		if done := e.wait(task.ID); done.State != StateSucceeded || done.Actor.Kind != a.Kind || done.ApprovedBy == nil {
			t.Fatalf("%s: task %+v", a.Kind, done)
		}
	}
	if len(calls) != 4 {
		t.Fatalf("calls %v", calls)
	}
	// Without it, the same plans run when their maker approves them.
	for _, a := range []Actor{owner, sync} {
		p, err := e.e.Plan(t.Context(), a, PlanRequest{Action: "proxmox.guest.reboot", Target: "proxmox:i_x/qemu/201"})
		if err != nil {
			t.Fatal(err)
		}
		task, err := e.approve(a, p, "", "")
		var aw *AwaitingApproval
		if errors.As(err, &aw) || err != nil {
			t.Fatalf("%s: %v", a.Kind, err)
		}
		e.wait(task.ID)
	}
	// A sync's task is audited as the sync, for its owner.
	var last audit.Event
	for _, ev := range e.events() {
		if ev.Action == "proxmox.guest.reboot" && ev.Actor.Kind == KindSync {
			last = ev
		}
	}
	if last.Actor.ID != "sync_media" || last.Actor.OnBehalfOf != "u_owner" || last.Actor.Session != "" {
		t.Fatalf("audit actor %+v", last.Actor)
	}
	// Another sync can't approve this one's plan; it binds to sync:<id>.
	p, err := e.e.Plan(t.Context(), sync, PlanRequest{Action: "proxmox.guest.reboot", Target: "proxmox:i_x/qemu/201"})
	if err != nil {
		t.Fatal(err)
	}
	if sync.binding() != "sync:sync_media" {
		t.Fatalf("binding %q", sync.binding())
	}
	other := sync
	other.ID = "sync_other"
	refusal(t, func() error { _, err := e.approve(other, p, "", ""); return err }(), "forbidden")
}

// The hub shutting down under a task that resumes (Cha Dash updating
// itself stops the hub that runs the task) leaves it running, not
// interrupted, and the next start finishes it through Resume. A task that
// doesn't resume is still interrupted by the same shutdown.
func TestShutdownDuringExecuteResumes(t *testing.T) {
	waitCtx := func(ctx context.Context, _ Request, _ *TaskRun) error {
		<-ctx.Done()
		return ctx.Err()
	}
	self := Def{ID: "x.selfupdate", Title: "Update itself", Tier: T1, Targets: []string{"vm"}, Execute: waitCtx, Resumes: true,
		Resume: func(context.Context, Task, *TaskRun) error { return nil }}
	plain := Def{ID: "x.slow", Tier: T1, Targets: []string{"host"}, Execute: waitCtx}
	e := newEnv(t, OwnerOnly{}, self, plain)
	start := func(action, target string) Task {
		t.Helper()
		task, err := e.approve(owner, e.mustPlan(owner, action, target), "", "")
		if err != nil {
			t.Fatal(err)
		}
		e.waitRunning(task.ID)
		return task
	}
	up := start("x.selfupdate", "proxmox:i_x/qemu/201")
	other := start("x.slow", "proxmox:i_x/node/io")
	e.e.Close()

	var state string
	var finished *int64
	if err := e.st.Reader().QueryRow(`SELECT state, finished_at FROM tasks WHERE id = ?`, up.ID).Scan(&state, &finished); err != nil || state != StateRunning || finished != nil {
		t.Fatalf("after the shutdown: %s %v %v", state, finished, err)
	}
	if err := e.st.Reader().QueryRow(`SELECT state FROM tasks WHERE id = ?`, other.ID).Scan(&state); err != nil || state != StateFailed {
		t.Fatalf("the plain task: %s %v", state, err)
	}
	e.start()
	if done := e.wait(up.ID); done.State != StateSucceeded || done.Audit == nil {
		t.Fatalf("resumed: %+v", done)
	}
	if rep, _ := e.audit.Verify(t.Context()); !rep.OK {
		t.Fatalf("audit chain: %+v", rep)
	}
}
