package action

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Task states.
const (
	StateQueued    = "queued"
	StateRunning   = "running"
	StateVerifying = "verifying"
	StateSucceeded = "succeeded"
	StateFailed    = "failed"
	StateCancelled = "cancelled"
)

// Log tail bounds.
const (
	LogLines   = 50
	LogLineMax = 500 // bytes
)

// TaskActor is who started a task, as tasks show it.
type TaskActor struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// Task is one approved plan running, or finished: the API's and the tasks
// topic's shape.
type Task struct {
	ID         string     `json:"id"`
	Plan       string     `json:"plan"`
	Action     string     `json:"action"`
	Label      string     `json:"label"`
	Target     string     `json:"target"`
	Actor      TaskActor  `json:"actor"`
	State      string     `json:"state"`
	Step       string     `json:"step,omitempty"`
	Progress   *float64   `json:"progress,omitempty"` // 0–1
	Log        []string   `json:"log,omitempty"`      // the last LogLines lines
	StartedAt  *time.Time `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
	Error      *Error     `json:"error"`
	Undo       *Undo      `json:"undo"`  // once it has succeeded, the plan's undo
	Audit      *string    `json:"audit"` // the audit event, once finished
}

// Finished reports whether the task has ended.
func (t *Task) Finished() bool {
	switch t.State {
	case StateSucceeded, StateFailed, StateCancelled:
		return true
	}
	return false
}

func (t *Task) clone() Task {
	c := *t
	c.Log = append([]string(nil), t.Log...)
	if t.Progress != nil {
		p := *t.Progress
		c.Progress = &p
	}
	return c
}

// TaskRun is a running task's handle: Execute and Verify report through
// it. Its methods are safe for concurrent use and cheap; the tasks topic
// coalesces what they publish.
type TaskRun struct {
	e  *Engine
	id string
}

// ID is the task's ID.
func (r *TaskRun) ID() string { return r.id }

// Step names the step now running ("Wait for it to report running").
func (r *TaskRun) Step(label string) {
	r.e.updateTask(r.id, true, func(t *Task) { t.Step = clip(label, 200) })
}

// Progress sets how far along the task is, from 0 to 1.
func (r *TaskRun) Progress(f float64) {
	f = min(max(f, 0), 1)
	r.e.updateTask(r.id, false, func(t *Task) { t.Progress = &f })
}

// Logf adds a line to the task's log. Only the last LogLines lines are
// kept, each at most LogLineMax bytes. Never log a secret.
func (r *TaskRun) Logf(format string, args ...any) {
	line := clip(fmt.Sprintf(format, args...), LogLineMax)
	r.e.updateTask(r.id, false, func(t *Task) {
		t.Log = append(t.Log, line)
		if n := len(t.Log); n > LogLines {
			t.Log = append(t.Log[:0], t.Log[n-LogLines:]...)
		}
	})
}

// clip shortens s to at most n bytes on a rune boundary, on one line.
func clip(s string, n int) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r", " "), "\n", " ")
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
