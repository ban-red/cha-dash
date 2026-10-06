// Package acttest runs an integration's actions through the real action
// engine, for contract tests: plan (resolve, preconditions, blast radius,
// undo), approve, execute and verify, with the audit record to read back.
//
// It's a stand-in until internal/integration/integrationtest grows the
// same helper (wave 8, track 8A); the calls are meant to move there as
// they are.
//
//	env := acttest.New(t, h.Set.Graph, integration.ActionDefs(inst.(integration.Actor))...)
//	task, plan := env.Run("qbittorrent.app.pause", "qbittorrent:i_test/app", nil)
package acttest

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"dash.cha.sh/integrations/internal/act"
	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/audit"
	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/store"
)

// Owner is who acts in the tests.
var Owner = action.Actor{Kind: action.KindUser, ID: "u_owner", Name: "sam", Role: "owner", Via: "web", Session: "s_1"}

// Env is an engine with the defs registered.
type Env struct {
	t      *testing.T
	Engine *action.Engine
	Audit  *audit.Log
}

// New starts an engine over g (nil for none) with defs registered, under a
// policy that allows everything. Until's polling is shortened for the test.
func New(t *testing.T, g *graph.Graph, defs ...action.Def) *Env {
	t.Helper()
	every := act.Every
	act.Every = 10 * time.Millisecond
	t.Cleanup(func() { act.Every = every })
	st, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "chadash.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	reg := action.NewRegistry()
	if err := reg.Register("test", defs...); err != nil {
		t.Fatal(err)
	}
	log := audit.New(st)
	e, err := action.New(t.Context(), action.Options{Store: st, Audit: log, Registry: reg, Policy: action.AllowAll{}, Graph: g})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Close)
	return &Env{t: t, Engine: e, Audit: log}
}

// Plan plans id on target with params (nil for none).
func (e *Env) Plan(id, target string, params any) (*action.Plan, error) {
	e.t.Helper()
	var raw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			e.t.Fatal(err)
		}
		raw = b
	}
	return e.Engine.Plan(e.t.Context(), Owner, action.PlanRequest{Action: id, Target: target, Params: raw})
}

// Refusal plans id and returns the refusal, failing the test if it was
// planned.
func (e *Env) Refusal(id, target string, params any) *action.Refusal {
	e.t.Helper()
	p, err := e.Plan(id, target, params)
	var r *action.Refusal
	if !errors.As(err, &r) {
		e.t.Fatalf("%s on %s: planned %+v, %v; want a refusal", id, target, p, err)
	}
	return r
}

// Run plans and approves id, waits for the task to finish and returns it
// with the plan. A refusal fails the test.
func (e *Env) Run(id, target string, params any) (action.Task, *action.Plan) {
	e.t.Helper()
	p, err := e.Plan(id, target, params)
	if err != nil {
		e.t.Fatalf("plan %s on %s: %v", id, target, err)
	}
	task, err := e.Engine.Approve(e.t.Context(), Owner, p.ID, action.ApproveRequest{Hash: p.Hash, Typed: p.ConfirmText}, "")
	if err != nil {
		e.t.Fatalf("approve %s on %s: %v", id, target, err)
	}
	ctx, cancel := context.WithTimeout(e.t.Context(), 30*time.Second)
	defer cancel()
	for !task.Finished() {
		select {
		case <-ctx.Done():
			e.t.Fatalf("%s on %s didn't finish: %+v", id, target, task)
		case <-time.After(5 * time.Millisecond):
		}
		if task, err = e.Engine.Task(ctx, Owner, task.ID); err != nil {
			e.t.Fatal(err)
		}
	}
	return task, p
}

// Succeeded runs id and fails the test unless the task succeeded.
func (e *Env) Succeeded(id, target string, params any) (action.Task, *action.Plan) {
	e.t.Helper()
	task, p := e.Run(id, target, params)
	if task.State != action.StateSucceeded {
		e.t.Fatalf("%s on %s: %s, %+v (log %q)", id, target, task.State, task.Error, task.Log)
	}
	return task, p
}

// Short is a context for calling a Verify that should give up: half a
// second, with Until's polling shortened.
func Short(t *testing.T) (context.Context, context.CancelFunc) {
	every := act.Every
	act.Every = 10 * time.Millisecond
	t.Cleanup(func() { act.Every = every })
	return context.WithTimeout(t.Context(), 500*time.Millisecond)
}

// Events is the audit log, newest first.
func (e *Env) Events() []audit.Event {
	e.t.Helper()
	page, err := e.Audit.Query(e.t.Context(), audit.Filter{Limit: 100})
	if err != nil {
		e.t.Fatal(err)
	}
	return page.Events
}
