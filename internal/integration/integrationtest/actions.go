package integrationtest

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/audit"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/runtime"
	"dash.cha.sh/internal/store"
)

// Actions runs an instance's actions the way the hub does: the runtime
// connects a read instance and, with a control credential, a control
// instance (runtime.Actions), registers their Defs with a real engine over
// a temporary database, and every attempt goes through plan, approve,
// execute, verify and the audit log. The engine's graph is the harness's,
// so a test that synced first gets the blast radius and the self-aware T4
// from what the instance published.
//
//	acts := h.Actions(proxmox.Integration{}, config, secrets)
//	task := acts.Run("proxmox.guest.start", "proxmox:"+h.ID+"/qemu/201", nil)
//	if task.State != action.StateSucceeded { … }
//	writes.Expect(t, integrationtest.Write{Method: "POST", Path: "/api2/json/nodes/io/qemu/201/status/start"})
//
// The actor is an owner with a fresh step-up grant, and the policy allows
// everything (T4 included): tiers are read from the plan, and refusals
// come from the Defs (preconditions, unavailable) and the engine.
type Actions struct {
	h *Harness
	// Engine, Registry, Bridge and Audit are the parts, for tests that need
	// more than the helpers.
	Engine   *action.Engine
	Registry *action.Registry
	Bridge   *runtime.Actions
	Audit    *audit.Log
	Actor    action.Actor
}

// Actions validates config (failing the test on any problem), attaches the
// instance's actions and starts an engine. The engine closes when the test
// ends.
func (h *Harness) Actions(integ integration.Integration, config map[string]any, secrets map[string]string) *Actions {
	h.t.Helper()
	if errs := Validate(integ, config, secrets); len(errs) > 0 {
		h.t.Fatalf("invalid config: %v", errs)
	}
	m := integ.Manifest()
	if err := integration.ValidateManifest(m); err != nil {
		h.t.Fatal(err)
	}
	st, err := store.Open(h.t.Context(), filepath.Join(h.t.TempDir(), "actions.db"))
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(h.t.Context()); err != nil {
		h.t.Fatal(err)
	}
	a := &Actions{h: h, Registry: action.NewRegistry(), Audit: audit.New(st)}
	a.Audit.SetClock(h.Now)
	a.Bridge = runtime.NewActions(a.Registry, h.Set.Graph, nil)
	cfg := integration.Config{InstanceID: h.ID, Name: "Test", Values: merged(config, secrets)}
	ok, err := a.Bridge.Attach(h.t.Context(), runtime.ActorSpec{
		Instance: h.ID, Name: "Test", Integration: integ, Manifest: m, Config: cfg,
		Deps: func(c integration.Config) (integration.Deps, func()) {
			d, _, release := runtime.NewDeps(runtime.DepsOptions{
				Config: c, Emit: quiet{self: runtime.SelfID(h.ID)}, Now: h.Now, RootCAs: h.RootCAs,
				Log: slog.New(slog.NewTextHandler(&lockedWriter{h: h}, &slog.HandlerOptions{Level: slog.LevelDebug})),
			})
			return d, release
		},
	})
	if err != nil {
		h.t.Fatalf("attaching actions: %v", err)
	}
	if !ok {
		h.t.Fatalf("%s isn't an integration.Actor", m.ID)
	}
	h.t.Cleanup(func() { a.Bridge.Detach(h.ID) })
	a.Engine, err = action.New(h.t.Context(), action.Options{
		Store: st, Audit: a.Audit, Registry: a.Registry, Policy: action.AllowAll{}, Graph: h.Set.Graph, Now: h.Now,
	})
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(a.Engine.Close)
	a.Actor = action.Actor{Kind: action.KindUser, ID: "u_test", Name: "sam", Role: "owner", Via: "web", Session: "s_test"}
	return a
}

// IDs lists the registered actions.
func (a *Actions) IDs() []string { return a.Registry.IDs() }

// Plan plans an action. params is anything that marshals to a JSON object,
// or nil.
func (a *Actions) Plan(actionID, target string, params any) (*action.Plan, error) {
	a.h.t.Helper()
	var raw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			a.h.t.Fatal(err)
		}
		raw = b
	}
	return a.Engine.Plan(a.h.t.Context(), a.actor(), action.PlanRequest{Action: actionID, Target: target, Params: raw})
}

// actor is the owner with a step-up grant that began just now, which
// satisfies T3 and T4.
func (a *Actions) actor() action.Actor {
	act := a.Actor
	act.StepUpUntil = a.h.Now().Add(action.StepUpTTL + time.Second)
	return act
}

// Refused plans an action that must be refused and returns the refusal.
func (a *Actions) Refused(actionID, target string, params any) *action.Refusal {
	a.h.t.Helper()
	p, err := a.Plan(actionID, target, params)
	var ref *action.Refusal
	if !errors.As(err, &ref) {
		a.h.t.Fatalf("plan %s on %s: want a refusal, got %+v, %v", actionID, target, p, err)
	}
	return ref
}

// Refusal is Refused under the name integrations/internal/act/acttest uses.
func (a *Actions) Refusal(actionID, target string, params any) *action.Refusal {
	a.h.t.Helper()
	return a.Refused(actionID, target, params)
}

// Succeeded runs an action and fails the test unless its task succeeded.
func (a *Actions) Succeeded(actionID, target string, params any) action.Task {
	a.h.t.Helper()
	t := a.Run(actionID, target, params)
	if t.State != action.StateSucceeded {
		a.h.t.Fatalf("%s on %s: %s, error %+v, log %v", actionID, target, t.State, t.Error, t.Log)
	}
	return t
}

// Approve approves a plan, typing its confirm text, and returns the task as
// it started.
func (a *Actions) Approve(p *action.Plan) (action.Task, error) {
	return a.Engine.Approve(a.h.t.Context(), a.actor(), p.ID, action.ApproveRequest{Hash: p.Hash, Typed: p.ConfirmText}, "")
}

// WaitTimeout bounds Wait.
const WaitTimeout = 20 * time.Second

// Wait returns the task once it has finished, failing the test after
// WaitTimeout.
func (a *Actions) Wait(id string) action.Task {
	a.h.t.Helper()
	deadline := time.Now().Add(WaitTimeout)
	for {
		t, err := a.Engine.Task(a.h.t.Context(), a.actor(), id)
		if err != nil {
			a.h.t.Fatal(err)
		}
		if t.Finished() {
			return t
		}
		if time.Now().After(deadline) {
			a.h.t.Fatalf("task %s (%s) didn't finish within %s: %+v", id, t.Action, WaitTimeout, t)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Run plans, approves and waits for an action, failing the test on a
// refusal, and returns the finished task (which may have failed).
func (a *Actions) Run(actionID, target string, params any) action.Task {
	a.h.t.Helper()
	p, err := a.Plan(actionID, target, params)
	if err != nil {
		a.h.t.Fatalf("plan %s on %s: %v", actionID, target, err)
	}
	task, err := a.Approve(p)
	if err != nil {
		a.h.t.Fatalf("approve %s on %s: %v", actionID, target, err)
	}
	return a.Wait(task.ID)
}

// Events returns the audit log, oldest first.
func (a *Actions) Events() []audit.Event {
	a.h.t.Helper()
	page, err := a.Audit.Query(a.h.t.Context(), audit.Filter{Limit: audit.MaxLimit})
	if err != nil {
		a.h.t.Fatal(err)
	}
	ev := page.Events
	slices.Reverse(ev)
	return ev
}

// LastEvent is the newest audit event.
func (a *Actions) LastEvent() audit.Event {
	a.h.t.Helper()
	ev := a.Events()
	if len(ev) == 0 {
		a.h.t.Fatal("the audit log is empty")
	}
	return ev[len(ev)-1]
}

// quiet is the emitter action instances get: they publish nothing.
type quiet struct{ self string }

func (q quiet) Self() string                          { return q.self }
func (quiet) Replace(string, integration.Facts) error { return nil }
func (quiet) Raise(integration.Incident) error        { return nil }
func (quiet) Resolve(string, string) error            { return nil }
func (quiet) SetVersion(string)                       {}
func (quiet) SetWarnings([]integration.Warning)       {}

var _ integration.Emitter = quiet{}

// Write is one request an upstream fake received that changes something.
// In an expectation, Method and Path must match; Form, JSON and Body are
// compared only when set.
type Write struct {
	Method string
	Path   string // the URL path, unescaped
	Query  url.Values
	Body   string
	// Form is the body decoded, for application/x-www-form-urlencoded.
	Form url.Values
	// JSON is the body decoded, for application/json.
	JSON any
}

// Writes records the requests a fake upstream receives that may change
// something (every method but GET, HEAD and OPTIONS), so a contract test
// can check an action asked for exactly what it should: method, path and
// body. Headers aren't kept, so credentials never reach test output.
type Writes struct {
	mu   sync.Mutex
	reqs []Write
}

// Wrap returns next, recording writes on their way to it.
func (w *Writes) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(rw, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		rec := Write{Method: r.Method, Path: r.URL.Path, Query: r.URL.Query(), Body: string(body)}
		ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		switch ct {
		case "application/x-www-form-urlencoded":
			rec.Form, _ = url.ParseQuery(string(body))
		case "application/json":
			_ = json.Unmarshal(body, &rec.JSON)
		}
		w.mu.Lock()
		w.reqs = append(w.reqs, rec)
		w.mu.Unlock()
		next.ServeHTTP(rw, r)
	})
}

// All returns the writes so far.
func (w *Writes) All() []Write {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.reqs)
}

// Take returns the writes so far and forgets them.
func (w *Writes) Take() []Write {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := w.reqs
	w.reqs = nil
	return out
}

// Expect checks the writes so far are exactly want, in order, and forgets
// them.
func (w *Writes) Expect(t testing.TB, want ...Write) {
	t.Helper()
	got := w.Take()
	if len(got) != len(want) {
		t.Errorf("writes: got %d, want %d:\n got  %s\n want %s", len(got), len(want), describe(got), describe(want))
		return
	}
	for i := range want {
		g, e := got[i], want[i]
		ok := g.Method == e.Method && g.Path == e.Path &&
			(e.Form == nil || reflect.DeepEqual(g.Form, e.Form)) &&
			(e.JSON == nil || jsonEqual(g.JSON, e.JSON)) &&
			(e.Body == "" || g.Body == e.Body) &&
			(e.Query == nil || reflect.DeepEqual(g.Query, e.Query))
		if !ok {
			t.Errorf("write %d: got %s, want %s", i, describe([]Write{g}), describe([]Write{e}))
		}
	}
}

func jsonEqual(a, b any) bool {
	na, _ := json.Marshal(a)
	nb, _ := json.Marshal(b)
	var x, y any
	_ = json.Unmarshal(na, &x)
	_ = json.Unmarshal(nb, &y)
	return reflect.DeepEqual(x, y)
}

func describe(ws []Write) string {
	b, _ := json.Marshal(ws)
	return string(b)
}
