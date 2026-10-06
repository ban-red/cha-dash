package adguard

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

// Actions (v0.2): pause protection for five minutes (T1, undo: resume) and
// resume it (T1), both marked household. AdGuard Home's users all have
// full access, so they use the instance's own sign-in; there's no
// separate control credential.

// Action IDs.
const (
	ActPause  = "adguard.app.pause"
	ActResume = "adguard.app.resume"
)

// PauseMillis is how long a pause lasts (POST /control/protection takes
// milliseconds); AdGuard Home turns protection back on by itself.
const PauseMillis = 5 * 60 * 1000

// Actions implements integration.Actor.
func (in *instance) Actions() []action.Def {
	name := in.cfg.Name
	if name == "" {
		name = "AdGuard Home"
	}
	resolve := act.App(in.rid, name)
	return []action.Def{
		{
			ID: ActPause, Title: "Pause protection in", Targets: []string{"app"}, Tier: action.T1, Household: true,
			Resolve: resolve, Preconditions: in.preconditions, Verify: in.verify,
			Label: func(r action.Request) string { return "Pause protection in " + r.Target.Name + " for 5 minutes" },
			Steps: func(r action.Request) []action.Step {
				return act.Steps("Ask "+r.Target.Name+" to stop filtering for 5 minutes", "Check that protection is off")
			},
			Undo: act.UndoBy(ActResume, "Resume protection in"),
		},
		{
			ID: ActResume, Title: "Resume protection in", Targets: []string{"app"}, Tier: action.T1, Household: true,
			Resolve: resolve, Preconditions: in.preconditions, Verify: in.verify,
			Steps: func(r action.Request) []action.Step {
				return act.Steps("Ask "+r.Target.Name+" to filter again", "Check that protection is on")
			},
		},
	}
}

func (in *instance) preconditions(ctx context.Context, req action.Request) error {
	s, err := in.readStatus(ctx)
	if err != nil {
		return err
	}
	switch {
	case req.Action == ActPause && !s.ProtectionEnabled:
		return action.Precondition("Protection in " + req.Target.Name + " is already off.")
	case req.Action == ActResume && s.ProtectionEnabled:
		return action.Precondition("Protection in " + req.Target.Name + " is already on.")
	}
	return nil
}

// Execute implements integration.Actor: POST /control/protection.
func (in *instance) Execute(ctx context.Context, req action.Request, run *action.TaskRun) error {
	body, step := map[string]any{"enabled": true}, "Ask "+req.Target.Name+" to filter again"
	if req.Action == ActPause {
		body, step = map[string]any{"enabled": false, "duration": PauseMillis}, "Ask "+req.Target.Name+" to stop filtering for 5 minutes"
	}
	run.Step(step)
	b, _ := json.Marshal(body)
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, in.base+"/control/protection", bytes.NewReader(b))
	if err != nil {
		return err
	}
	hr.Header.Set("Content-Type", "application/json")
	if in.user != "" {
		hr.SetBasicAuth(in.user, in.pass)
	}
	resp, err := in.d.HTTP.Do(hr)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return action.Fail("auth", "AdGuard Home refused the username or password.")
	case resp.StatusCode == http.StatusNotFound:
		return action.Fail("unsupported", "AdGuard Home doesn't know POST /control/protection; it needs v0.107 or later.")
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return fmt.Errorf("AdGuard Home answered POST /control/protection with %s", resp.Status)
	}
	run.Logf("AdGuard Home accepted protection enabled=%v", body["enabled"])
	return nil
}

// verify reads the status back: protection off after a pause, on after a
// resume.
func (in *instance) verify(ctx context.Context, req action.Request, run *action.TaskRun) error {
	want, step := true, "Check that protection is on"
	if req.Action == ActPause {
		want, step = false, "Check that protection is off"
	}
	return act.Until(ctx, run, step, func(ctx context.Context) (bool, error) {
		s, err := in.readStatus(ctx)
		if err != nil {
			return false, err
		}
		if s.ProtectionEnabled != want {
			if want {
				return false, fmt.Errorf("AdGuard Home reports protection off")
			}
			return false, fmt.Errorf("AdGuard Home reports protection on")
		}
		return true, nil
	})
}
