package homeassistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"dash.cha.sh/integrations/internal/act"
	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/integration"
)

// Actions (v0.2): run a script (T1, no undo: a script does what it does).
// Only the scripts the owner lists in the instance's config can be run;
// with none listed, the instance has no actions. It calls script.turn_on
// with the instance's token: Home Assistant's tokens carry their user's
// rights and have no scopes, so there's no separate control credential.
// The allow-list is the limit.

// ActRunScript is the action's ID.
const ActRunScript = "homeassistant.script.run"

// Allow-list bounds.
const (
	scriptPattern = `^script\.[a-z0-9_]+$`
	maxScripts    = 50
)

// scriptState is the part of /api/states/<script> the action reads.
type scriptState struct {
	State      string `json:"state"` // on while it runs, off, unavailable
	Attributes struct {
		FriendlyName  string  `json:"friendly_name"`
		LastTriggered *string `json:"last_triggered"`
	} `json:"attributes"`
}

// runs remembers when each task's script last ran before it was started,
// so Verify can see it run again.
type runs struct {
	mu     sync.Mutex
	before map[string]string
}

func (r *runs) set(task, last string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.before == nil {
		r.before = map[string]string{}
	}
	r.before[task] = last
}

func (r *runs) take(task string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	last := r.before[task]
	delete(r.before, task)
	return last
}

// Actions implements integration.Actor: none without an allow-list.
func (in *instance) Actions() []action.Def {
	if len(in.scripts) == 0 {
		return nil
	}
	script := integration.Enum("Script", in.scripts...)
	return []action.Def{{
		ID: ActRunScript, Title: "Run", Targets: []string{"app"}, Tier: action.T1,
		Params:        integration.Params(integration.Object(integration.Prop("script", script)).Require("script")),
		Choices:       func(action.Target) []json.RawMessage { return scriptChoices(in.scripts) },
		Resolve:       act.App(in.appID, in.name),
		Preconditions: in.scriptPreconditions,
		Label: func(r action.Request) string {
			if s := scriptOf(r); s != "" {
				return "Run " + s + " in " + r.Target.Name
			}
			return "Run a script in " + r.Target.Name // the listing, before one is picked
		},
		Steps: func(r action.Request) []action.Step {
			return act.Steps("Ask "+r.Target.Name+" to run "+scriptOf(r), "Check that it ran")
		},
		Verify: in.scriptVerify,
	}}
}

// choices are the listing's offers: one per allowed script.
func scriptChoices(scripts []string) []json.RawMessage {
	out := make([]json.RawMessage, len(scripts))
	for i, s := range scripts {
		out[i], _ = json.Marshal(map[string]string{"script": s})
	}
	return out
}

// scriptOf is the request's script (validated against the allow-list).
func scriptOf(r action.Request) string {
	var p struct {
		Script string `json:"script"`
	}
	_ = r.Decode(&p)
	return p.Script
}

func (in *instance) readScript(ctx context.Context, entity string) (scriptState, error) {
	var s scriptState
	err := in.getJSON(ctx, "/api/states/"+entity, nil, &s)
	if errors.Is(err, errMissing) {
		return s, action.Precondition("Home Assistant has no " + entity + ". Check the scripts listed in this instance's settings.")
	}
	return s, err
}

func (in *instance) scriptPreconditions(ctx context.Context, req action.Request) error {
	entity := scriptOf(req)
	s, err := in.readScript(ctx, entity)
	if err != nil {
		return err
	}
	switch s.State {
	case "on":
		return action.Precondition(entity + " is already running.")
	case "unavailable", "unknown":
		return action.Precondition(entity + " is " + s.State + " in Home Assistant.")
	}
	return nil
}

// Execute implements integration.Actor: POST /api/services/script/turn_on,
// which starts the script without waiting for it to finish.
func (in *instance) Execute(ctx context.Context, req action.Request, run *action.TaskRun) error {
	entity := scriptOf(req)
	s, err := in.readScript(ctx, entity)
	if err != nil {
		return err
	}
	last := ""
	if s.Attributes.LastTriggered != nil {
		last = *s.Attributes.LastTriggered
	}
	in.runs.set(req.Task, last)
	run.Step("Ask " + req.Target.Name + " to run " + entity)
	body, _ := json.Marshal(map[string]string{"entity_id": entity})
	u := in.base.JoinPath("api", "services", "script", "turn_on")
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	hr.Header.Set("Content-Type", "application/json")
	resp, err := in.http.Do(hr)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return action.Fail("auth", "Home Assistant refused the token, or its user may not run scripts.")
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return action.Fail("upstream", fmt.Sprintf("Home Assistant answered script.turn_on with %s: %s", resp.Status, strings.TrimSpace(string(msg))))
	}
	run.Logf("Home Assistant started %s", entity)
	return nil
}

// scriptVerify waits for the script to have run: its last_triggered moved,
// or it's running now.
func (in *instance) scriptVerify(ctx context.Context, req action.Request, run *action.TaskRun) error {
	entity, before := scriptOf(req), in.runs.take(req.Task)
	return act.Until(ctx, run, "Check that it ran", func(ctx context.Context) (bool, error) {
		s, err := in.readScript(ctx, entity)
		if err != nil {
			return false, err
		}
		if s.State == "on" || (s.Attributes.LastTriggered != nil && *s.Attributes.LastTriggered != before) {
			return true, nil
		}
		return false, fmt.Errorf("%s hasn't run", entity)
	})
}
