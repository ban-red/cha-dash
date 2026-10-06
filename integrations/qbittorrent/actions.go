package qbittorrent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"dash.cha.sh/integrations/internal/act"
	"dash.cha.sh/internal/action"
)

// Actions (v0.2): pause and resume every torrent (T1, each the other's
// undo). qBittorrent has one WebUI account and no scopes, so they use the
// instance's own credential; there's no separate control credential to
// ask for.

// Action IDs.
const (
	ActPause  = "qbittorrent.app.pause"
	ActResume = "qbittorrent.app.resume"
)

// verbs are the Web API's names for pausing and resuming, and for the
// torrents/info filters that find what's left: qBittorrent 5 (Web API
// 2.11) renamed pause/resume to stop/start.
type verbs struct{ pause, resume, paused, running string }

var (
	verbsV4 = verbs{pause: "pause", resume: "resume", paused: "paused", running: "resumed"}
	verbsV5 = verbs{pause: "stop", resume: "start", paused: "stopped", running: "running"}
)

// Actions implements integration.Actor.
func (in *instance) Actions() []action.Def {
	name := in.cfg.Name
	if name == "" {
		name = "qBittorrent"
	}
	resolve := act.App(in.appID(), name)
	pause := action.Def{
		ID: ActPause, Title: "Pause all torrents in", Targets: []string{"app"}, Tier: action.T1,
		Resolve: resolve, Preconditions: in.preconditions, Verify: in.verify,
		Label: func(r action.Request) string { return "Pause all torrents in " + r.Target.Name },
		Steps: func(r action.Request) []action.Step {
			return act.Steps("Ask "+r.Target.Name+" to pause every torrent", "Check that none is still running")
		},
		Undo: act.UndoBy(ActResume, "Resume all torrents in"),
	}
	resume := pause
	resume.ID, resume.Title = ActResume, "Resume all torrents in"
	resume.Label = func(r action.Request) string { return "Resume all torrents in " + r.Target.Name }
	resume.Steps = func(r action.Request) []action.Step {
		return act.Steps("Ask "+r.Target.Name+" to resume every torrent", "Check that none is still paused")
	}
	resume.Undo = act.UndoBy(ActPause, "Pause all torrents in")
	return []action.Def{pause, resume}
}

// rid is the app resource's ID.
func (in *instance) appID() string {
	key := in.cfg.InstanceID
	if key == "" {
		key = "test"
	}
	return ID + ":" + key + "/app"
}

// verbs reads the Web API version: 2.11 and later stop and start.
func (in *instance) verbs(ctx context.Context) (verbs, error) {
	var api string
	if err := in.get(ctx, "/api/v2/app/webapiVersion", nil, &api); err != nil {
		return verbs{}, err
	}
	var major, minor int
	if _, err := fmt.Sscanf(api, "%d.%d", &major, &minor); err == nil && (major > 2 || (major == 2 && minor >= 11)) {
		return verbsV5, nil
	}
	return verbsV4, nil
}

// any reports whether a torrent matches a torrents/info filter.
func (in *instance) any(ctx context.Context, filter string) (bool, error) {
	var ts []struct {
		Hash string `json:"hash"`
	}
	if err := in.get(ctx, "/api/v2/torrents/info", url.Values{"filter": {filter}, "limit": {"1"}}, &ts); err != nil {
		return false, err
	}
	return len(ts) > 0, nil
}

func (in *instance) preconditions(ctx context.Context, req action.Request) error {
	v, err := in.verbs(ctx)
	if err != nil {
		return err
	}
	if req.Action == ActPause {
		if running, err := in.any(ctx, v.running); err != nil {
			return err
		} else if !running {
			return action.Precondition("No torrent in " + req.Target.Name + " is running.")
		}
		return nil
	}
	if paused, err := in.any(ctx, v.paused); err != nil {
		return err
	} else if !paused {
		return action.Precondition("No torrent in " + req.Target.Name + " is paused.")
	}
	return nil
}

// Execute implements integration.Actor: one POST with hashes=all.
func (in *instance) Execute(ctx context.Context, req action.Request, run *action.TaskRun) error {
	v, err := in.verbs(ctx)
	if err != nil {
		return err
	}
	verb, words := v.pause, "pause"
	if req.Action == ActResume {
		verb, words = v.resume, "resume"
	}
	run.Step("Ask " + req.Target.Name + " to " + words + " every torrent")
	if err := in.post(ctx, "/api/v2/torrents/"+verb, url.Values{"hashes": {"all"}}); err != nil {
		return err
	}
	run.Logf("qBittorrent accepted torrents/%s for all torrents", verb)
	return nil
}

// verify waits until no torrent is running (after a pause) or paused
// (after a resume).
func (in *instance) verify(ctx context.Context, req action.Request, run *action.TaskRun) error {
	step, left := "Check that none is still running", "running"
	if req.Action == ActResume {
		step, left = "Check that none is still paused", "paused"
	}
	return act.Until(ctx, run, step, func(ctx context.Context) (bool, error) {
		v, err := in.verbs(ctx)
		if err != nil {
			return false, err
		}
		filter := v.running
		if req.Action == ActResume {
			filter = v.paused
		}
		some, err := in.any(ctx, filter)
		if err != nil {
			return false, err
		}
		if some {
			return false, errors.New("a torrent is still " + left)
		}
		return true, nil
	})
}

// post sends a form to path.
func (in *instance) post(ctx context.Context, path string, form url.Values) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, in.base+path, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := in.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusNotFound:
		return action.Fail("unsupported", "qBittorrent doesn't know POST "+path+": its Web API changed.")
	case http.StatusForbidden, http.StatusUnauthorized:
		return action.Fail("auth", "qBittorrent refused the sign-in.")
	}
	return fmt.Errorf("qBittorrent answered POST %s with %s", path, resp.Status)
}
