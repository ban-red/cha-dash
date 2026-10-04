package integration

import (
	"context"
	"fmt"

	"dash.cha.sh/internal/action"
)

// Actor is an optional interface for an Instance that runs actions (v0.2),
// beside the reads it mirrors (integrations/<id>/actions.go). The runtime
// registers each instance's actions with the engine (wave 8) and calls
// Execute with the instance's HTTP client and secrets, using the control
// credential when the instance has one.
//
// The action types live in internal/action, and this package imports it:
// the engine must not import integration (the runtime, which owns
// instances, sits between them), so the dependency points from the
// framework to the engine and there's no cycle.
type Actor interface {
	// Actions are the instance's action definitions. A Def's Execute may be
	// nil: ActionDefs routes it to Execute below.
	Actions() []action.Def
	// Execute runs one approved action, reporting on run.
	Execute(ctx context.Context, req action.Request, run *action.TaskRun) error
}

// ActionDefs returns a's actions, with Execute filled in where a Def leaves
// it nil.
func ActionDefs(a Actor) []action.Def {
	defs := a.Actions()
	out := make([]action.Def, len(defs))
	for i, d := range defs {
		if d.Execute == nil {
			d.Execute = a.Execute
		}
		out[i] = d
	}
	return out
}

// Params adapts an object schema to an action's params schema. Write-only
// properties are the action's secret params: redacted in the audit log and
// in plans.
func Params(s *Schema) action.Schema {
	if err := s.Check(); err != nil {
		panic(fmt.Sprintf("integration: action params: %v", err))
	}
	return paramsSchema{s}
}

type paramsSchema struct{ s *Schema }

func (p paramsSchema) MarshalJSON() ([]byte, error) { return p.s.MarshalJSON() }

func (p paramsSchema) Validate(params map[string]any) []action.FieldError {
	errs := p.s.Validate(params)
	out := make([]action.FieldError, len(errs))
	for i, e := range errs {
		out[i] = action.FieldError{Path: e.Path, Message: e.Message}
	}
	return out
}

func (p paramsSchema) Secrets() []string {
	var names []string
	for _, prop := range p.s.Properties {
		if prop.Schema.WriteOnly {
			names = append(names, prop.Name)
		}
	}
	return names
}
