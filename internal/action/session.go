package action

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"dash.cha.sh/internal/auth"
	"dash.cha.sh/internal/httpx"
	"dash.cha.sh/internal/realtime"
)

// Sessions turns the signed-in session (auth.Require's identity) into an
// Actor.
type Sessions struct {
	Proxies httpx.Proxies
	// StepUpUntil reads the session's step-up grant. It's an adapter so the
	// engine doesn't wait for the step-up work in internal/auth: nil means
	// no session ever has a grant, so OwnerOnly refuses T3 with
	// step_up_required. Wire it to the session's grant when that lands.
	StepUpUntil func(auth.Identity) time.Time
	// Lookup finds the session's identity in a context; nil means
	// auth.FromContext. Tests replace it.
	Lookup func(context.Context) (auth.Identity, bool)
}

// FromContext is the actor behind ctx's session, without a client IP (the
// realtime connection's context has no request).
func (s Sessions) FromContext(ctx context.Context) (Actor, bool) {
	lookup := s.Lookup
	if lookup == nil {
		lookup = auth.FromContext
	}
	id, ok := lookup(ctx)
	if !ok {
		return Actor{}, false
	}
	a := Actor{Kind: KindUser, ID: id.User.ID, Name: id.User.Username, Role: id.User.Role, Via: "web", Session: id.Session.ID}
	if s.StepUpUntil != nil {
		a.StepUpUntil = s.StepUpUntil(id)
	}
	return a, true
}

// FromRequest is the actor behind r's session, with the client IP.
func (s Sessions) FromRequest(r *http.Request) (Actor, bool) {
	a, ok := s.FromContext(r.Context())
	if ok {
		if ip := s.Proxies.ClientIP(r); ip.IsValid() {
			a.IP = ip.String()
		}
	}
	return a, ok
}

// TasksSource serves the tasks topic, {"tasks": {id: Task}}: running tasks
// and the last day's, at most MaxTasks. A viewer sees their own tasks, or
// everyone's when the policy grants ScopeTasksAll (Admin and Owner).
func (e *Engine) TasksSource() realtime.Source {
	return realtime.ViewerDocSource{Doc: e.tasks, Topic: TasksTopic, Coll: tasksColl, Visible: func(ctx context.Context) func(string, json.RawMessage) bool {
		a, ok := e.sessions.FromContext(ctx)
		if !ok {
			return func(string, json.RawMessage) bool { return false }
		}
		if e.policy.Authorize(ctx, a, ScopeTasksAll, "", T0) == nil {
			return func(string, json.RawMessage) bool { return true }
		}
		return func(id string, _ json.RawMessage) bool { return e.ownerOf(id) == a.ID }
	}}
}

// TasksTopic is the tasks topic's name.
const TasksTopic = "tasks"

func (e *Engine) ownerOf(task string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if lt, ok := e.live[task]; ok {
		return lt.actorID
	}
	return ""
}
