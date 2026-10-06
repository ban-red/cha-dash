// Package act is what the integrations' actions share: resolving an
// instance's own app resource, undo between two actions, and waiting for a
// read to show an action's result. Actions are defined in each
// integration's actions.go; see internal/action for the contract.
package act

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"dash.cha.sh/internal/action"
)

// App resolves only the instance's app resource (id, "<integration>:<instance>/app"),
// named name. Any other target is action.ErrNotFound, so actions with the
// same ID from several instances of an integration each answer for their
// own target only.
func App(id, name string) func(context.Context, string) (action.Target, error) {
	return func(_ context.Context, target string) (action.Target, error) {
		if target != id {
			return action.Target{}, action.ErrNotFound
		}
		return action.Target{ID: id, Kind: "app", Name: name}, nil
	}
}

// Fingerprint is a target's State from the values an approval must find
// unchanged.
func Fingerprint(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)[:8])
}

// UndoBy offers the action called id, with no params, on the same target as
// the undo, labelled verb and the target's name ("Stop plex").
func UndoBy(id, verb string) func(action.Request) *action.Undo {
	return func(r action.Request) *action.Undo {
		return &action.Undo{Action: id, Params: json.RawMessage(`{}`), Label: verb + " " + r.Target.Name}
	}
}

// Steps is a plan's steps from labels.
func Steps(labels ...string) []action.Step {
	out := make([]action.Step, len(labels))
	for i, l := range labels {
		out[i] = action.Step{Label: l}
	}
	return out
}

// Every is how often Until reads again. Tests may shorten it.
var Every = 2 * time.Second

// errNotYet is what Until reports when the context ends before check
// passes and check never said why.
var errNotYet = errors.New("the change didn't show in time")

// Until runs check now and then every Every until it reports done, fails
// with a *action.Error, or ctx ends. Other errors are read failures that
// may pass: the last one is returned if time runs out.
func Until(ctx context.Context, run *action.TaskRun, step string, check func(context.Context) (done bool, err error)) error {
	if run != nil && step != "" {
		run.Step(step)
	}
	var last error
	for {
		done, err := check(ctx)
		var fail *action.Error
		switch {
		case done && err == nil:
			return nil
		case errors.As(err, &fail):
			return err
		case err != nil && (ctx.Err() == nil || last == nil):
			// A read cut short by the deadline says less than the one before.
			last = err
		}
		t := time.NewTimer(Every)
		select {
		case <-ctx.Done():
			t.Stop()
			if last != nil {
				return last
			}
			return errNotYet
		case <-t.C:
		}
	}
}
