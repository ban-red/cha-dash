// Package node is chadash-node's runtime: it dials out to the hub, keeps
// its identity on disk, loads and enforces the local policy file, and
// dispatches the hub's requests to modules (docker, stacks).
//
// The node exposes capability verbs, never a Docker API pass-through
// (ADR-0006): each Verb names the policy permission it needs, and the
// runtime refuses it unless the policy allows that permission. The hub can
// only ask.
package node

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"dash.cha.sh/internal/nodeproto"
)

// Module is one area of the node's verbs: docker, stacks.
type Module interface {
	Name() string
	Verbs() []Verb
}

// Verb is one capability the hub may call.
type Verb struct {
	Name   string // "container.restart", "stack.deploy"
	Policy string // the policy permission it needs, e.g. "docker.lifecycle", "stacks.deploy"
	// Handle serves one call. progress sends a stream item to the caller
	// (a progress step, a log line) before the reply. Return a
	// *nodeproto.Error (Errorf, Forbidden, NotFound) for a failure the hub
	// should tell apart; anything else is "failed" with its text, so keep
	// secrets out of error messages.
	Handle func(ctx context.Context, req json.RawMessage, progress func(json.RawMessage)) (json.RawMessage, error)
}

// Forbidden is the refusal for a protected target or a policy that doesn't
// allow something a verb was asked to do.
func Forbidden(format string, args ...any) error {
	return nodeproto.Errorf(nodeproto.CodeForbidden, format, args...)
}

// NotFound is the refusal for a target that doesn't exist.
func NotFound(format string, args ...any) error {
	return nodeproto.Errorf(nodeproto.CodeNotFound, format, args...)
}

// Invalid is the refusal for a request that doesn't parse.
func Invalid(format string, args ...any) error {
	return nodeproto.Errorf(nodeproto.CodeInvalid, format, args...)
}

// Dispatcher routes verbs to their modules under the policy.
type Dispatcher struct {
	policy Policy
	verbs  map[string]Verb
	order  []string
}

// NewDispatcher checks the modules' verbs: each needs a unique name, a
// known permission and a handler.
func NewDispatcher(p Policy, modules ...Module) (*Dispatcher, error) {
	d := &Dispatcher{policy: p, verbs: map[string]Verb{}}
	for _, m := range modules {
		for _, v := range m.Verbs() {
			switch {
			case v.Name == "" || v.Handle == nil:
				return nil, fmt.Errorf("node: module %s: a verb needs a name and a handler", m.Name())
			case !ValidPermission(v.Policy):
				return nil, fmt.Errorf("node: module %s: %s needs unknown permission %q", m.Name(), v.Name, v.Policy)
			case v.Name == nodeproto.VerbHubPin || v.Name == nodeproto.VerbNodeRotate:
				return nil, fmt.Errorf("node: module %s: %s is a built-in verb", m.Name(), v.Name)
			}
			if _, dup := d.verbs[v.Name]; dup {
				return nil, fmt.Errorf("node: module %s: %s is registered twice", m.Name(), v.Name)
			}
			d.verbs[v.Name] = v
			d.order = append(d.order, v.Name)
		}
	}
	slices.Sort(d.order)
	return d, nil
}

// Policy is the policy the dispatcher enforces.
func (d *Dispatcher) Policy() Policy { return d.policy }

// Info lists the verbs and whether the policy allows each, for the hello.
func (d *Dispatcher) Info() []nodeproto.VerbInfo {
	out := make([]nodeproto.VerbInfo, 0, len(d.order))
	for _, n := range d.order {
		v := d.verbs[n]
		out = append(out, nodeproto.VerbInfo{Name: v.Name, Policy: v.Policy, Allowed: d.policy.Allows(v.Policy)})
	}
	return out
}

// Call runs one verb: unknown verbs are not_found, and verbs whose
// permission the policy doesn't grant are forbidden before their handler
// runs.
func (d *Dispatcher) Call(ctx context.Context, verb string, req json.RawMessage, progress func(json.RawMessage)) (json.RawMessage, error) {
	v, ok := d.verbs[verb]
	if !ok {
		return nil, nodeproto.Errorf(nodeproto.CodeNotFound, "this node has no verb %q", verb)
	}
	if !d.policy.Allows(v.Policy) {
		return nil, nodeproto.Errorf(nodeproto.CodeForbidden, "this node's policy doesn't allow %s (needed by %s)", v.Policy, v.Name)
	}
	if len(req) == 0 {
		req = json.RawMessage("{}")
	}
	if progress == nil {
		progress = func(json.RawMessage) {}
	}
	return v.Handle(ctx, req, progress)
}
