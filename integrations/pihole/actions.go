package pihole

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"dash.cha.sh/integrations/internal/act"
	"dash.cha.sh/internal/action"
)

// Actions (v0.2): pause blocking for five minutes (T1, undo: resume) and
// resume it (T1). Both are marked household, so the Household role may run
// them. Pi-hole has one administrator and no scopes, so they use the
// instance's own password; there's no separate control credential.

// Action IDs.
const (
	ActPause  = "pihole.app.pause"
	ActResume = "pihole.app.resume"
)

// PauseSeconds is how long a pause lasts; Pi-hole turns blocking back on
// by itself afterwards.
const PauseSeconds = 300

// Actions implements integration.Actor.
func (in *instance) Actions() []action.Def {
	name := in.cfg.Name
	if name == "" {
		name = "Pi-hole"
	}
	resolve := act.App(in.rid, name)
	return []action.Def{
		{
			ID: ActPause, Title: "Pause blocking in", Targets: []string{"app"}, Tier: action.T1, Household: true,
			Resolve: resolve, Preconditions: in.preconditions, Verify: in.verify,
			Label: func(r action.Request) string { return "Pause blocking in " + r.Target.Name + " for 5 minutes" },
			Steps: func(r action.Request) []action.Step {
				return act.Steps("Ask "+r.Target.Name+" to stop blocking for 5 minutes", "Check that blocking is off")
			},
			Undo: act.UndoBy(ActResume, "Resume blocking in"),
		},
		{
			ID: ActResume, Title: "Resume blocking in", Targets: []string{"app"}, Tier: action.T1, Household: true,
			Resolve: resolve, Preconditions: in.preconditions, Verify: in.verify,
			Steps: func(r action.Request) []action.Step {
				return act.Steps("Ask "+r.Target.Name+" to block again", "Check that blocking is on")
			},
		},
	}
}

func (in *instance) blocking(ctx context.Context) (blocking, error) {
	var b blocking
	err := in.get(ctx, "/api/dns/blocking", &b)
	return b, err
}

func (in *instance) preconditions(ctx context.Context, req action.Request) error {
	b, err := in.blocking(ctx)
	if err != nil {
		return err
	}
	switch {
	case req.Action == ActPause && b.Blocking == "disabled" && b.Timer != nil && *b.Timer > 0:
		return action.Precondition(fmt.Sprintf("Blocking in %s is already paused for another %s.", req.Target.Name, minutes(*b.Timer)))
	case req.Action == ActPause && b.Blocking == "disabled":
		return action.Precondition("Blocking in " + req.Target.Name + " is already off.")
	case req.Action == ActResume && b.Blocking == "enabled":
		return action.Precondition("Blocking in " + req.Target.Name + " is already on.")
	}
	return nil
}

// Execute implements integration.Actor: POST /api/dns/blocking.
func (in *instance) Execute(ctx context.Context, req action.Request, run *action.TaskRun) error {
	body, step := map[string]any{"blocking": true, "timer": nil}, "Ask "+req.Target.Name+" to block again"
	if req.Action == ActPause {
		body, step = map[string]any{"blocking": false, "timer": PauseSeconds}, "Ask "+req.Target.Name+" to stop blocking for 5 minutes"
	}
	run.Step(step)
	b, _ := json.Marshal(body)
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, in.base+"/api/dns/blocking", bytes.NewReader(b))
	if err != nil {
		return err
	}
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("Accept", "application/json")
	resp, err := in.http.Do(hr)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return action.Fail("auth", "Pi-hole refused the password.")
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return action.Fail("upstream", fmt.Sprintf("Pi-hole answered POST /api/dns/blocking with %s", resp.Status))
	}
	run.Logf("Pi-hole accepted blocking=%v", body["blocking"])
	return nil
}

// verify reads the blocking state back: disabled after a pause, enabled
// after a resume.
func (in *instance) verify(ctx context.Context, req action.Request, run *action.TaskRun) error {
	want, step := "enabled", "Check that blocking is on"
	if req.Action == ActPause {
		want, step = "disabled", "Check that blocking is off"
	}
	return act.Until(ctx, run, step, func(ctx context.Context) (bool, error) {
		b, err := in.blocking(ctx)
		if err != nil {
			return false, err
		}
		if b.Blocking != want {
			return false, fmt.Errorf("blocking is %s on Pi-hole", cmp(b.Blocking, "unknown"))
		}
		return true, nil
	})
}
