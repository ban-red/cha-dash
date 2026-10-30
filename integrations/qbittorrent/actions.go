package qbittorrent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"dash.cha.sh/integrations/internal/act"
	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/integration"
)

// Actions (v0.2): pause and resume every torrent (T1, each the other's
// undo). qBittorrent has one WebUI account and no scopes, so they use the
// instance's own credential; there's no separate control credential to
// ask for.
//
// Planning records which torrents the action will change (the running ones
// for a pause, the paused ones for a resume) in the target, so the undo
// names exactly those: undoing a pause leaves torrents someone had paused
// before it alone. Both take an optional hashes param, which is how the
// undo asks for them; without it they act on every torrent.

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

// maxHashes bounds the hashes param, and so what an undo can remember.
const maxHashes = 5000

// hashesParam is both actions' params: the torrents to act on, or all.
type hashesParam struct {
	Hashes []string `json:"hashes"`
}

func hashesOf(r action.Request) []string {
	var p hashesParam
	_ = r.Decode(&p)
	return p.Hashes
}

// Actions implements integration.Actor.
func (in *instance) Actions() []action.Def {
	name := in.cfg.Name
	if name == "" {
		name = "qBittorrent"
	}
	maxN := maxHashes
	hashes := integration.Array("Torrents", &integration.Schema{Type: "string", Pattern: `^[0-9A-Fa-f]{40,64}$`})
	hashes.MaxItems, hashes.Description = &maxN, "The torrents to act on, by hash. Empty: every torrent."
	params := integration.Params(integration.Object(integration.Prop("hashes", hashes)))
	which := func(r action.Request, all string) string {
		if n := len(hashesOf(r)); n > 0 {
			return strconv.Itoa(n) + " torrents"
		}
		return all
	}
	pause := action.Def{
		ID: ActPause, Title: "Pause all torrents in", Targets: []string{"app"}, Tier: action.T1, Params: params,
		Resolve: in.resolveChanging(name, true), Preconditions: in.preconditions, Verify: in.verify,
		Label: func(r action.Request) string { return "Pause " + which(r, "all torrents") + " in " + r.Target.Name },
		Steps: func(r action.Request) []action.Step {
			return act.Steps("Ask "+r.Target.Name+" to pause "+which(r, "every torrent"), "Check that none of them is still running")
		},
		Undo: in.undoBy(ActResume, "Resume"),
	}
	resume := pause
	resume.ID, resume.Title = ActResume, "Resume all torrents in"
	resume.Resolve = in.resolveChanging(name, false)
	resume.Label = func(r action.Request) string { return "Resume " + which(r, "all torrents") + " in " + r.Target.Name }
	resume.Steps = func(r action.Request) []action.Step {
		return act.Steps("Ask "+r.Target.Name+" to resume "+which(r, "every torrent"), "Check that none of them is still paused")
	}
	resume.Undo = in.undoBy(ActPause, "Pause")
	return []action.Def{pause, resume}
}

// resolveChanging resolves the app and records in its Ref the torrents the
// action would change, running ones for a pause and paused ones for a
// resume, sorted; State fingerprints them, so an approval after they
// change is refused.
func (in *instance) resolveChanging(name string, pausing bool) func(context.Context, string) (action.Target, error) {
	app := act.App(in.appID(), name)
	return func(ctx context.Context, target string) (action.Target, error) {
		t, err := app(ctx, target)
		if err != nil {
			return t, err
		}
		v, err := in.verbs(ctx)
		if err != nil {
			return t, err
		}
		filter := v.paused
		if pausing {
			filter = v.running
		}
		hs, err := in.hashes(ctx, filter)
		if err != nil {
			return t, err
		}
		slices.Sort(hs)
		t.Ref, t.State = strings.Join(hs, "|"), act.Fingerprint(hs...)
		return t, nil
	}
}

// undoBy is the other action, on exactly the torrents this one changes:
// the ones planning found in the state it moves them from, narrowed to the
// hashes asked for. None when that's none, or more than the param holds.
func (in *instance) undoBy(id, verb string) func(action.Request) *action.Undo {
	return func(r action.Request) *action.Undo {
		hs := changing(r)
		if len(hs) == 0 || len(hs) > maxHashes {
			return nil
		}
		params, _ := json.Marshal(hashesParam{Hashes: hs})
		return &action.Undo{Action: id, Params: params, Label: fmt.Sprintf("%s the %d torrents it changed in %s", verb, len(hs), r.Target.Name)}
	}
}

// changing is what planning found the action would change, narrowed to its
// hashes param when it has one.
func changing(r action.Request) []string {
	var found []string
	if r.Target.Ref != "" {
		found = strings.Split(r.Target.Ref, "|")
	}
	if only := hashesOf(r); len(only) > 0 {
		found = slices.DeleteFunc(found, func(h string) bool {
			return !slices.ContainsFunc(only, func(o string) bool { return strings.EqualFold(o, h) })
		})
	}
	return found
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

// hashes lists the torrents a torrents/info filter matches.
func (in *instance) hashes(ctx context.Context, filter string) ([]string, error) {
	var ts []struct {
		Hash string `json:"hash"`
	}
	if err := in.get(ctx, "/api/v2/torrents/info", url.Values{"filter": {filter}}, &ts); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(ts))
	for _, t := range ts {
		out = append(out, strings.ToLower(t.Hash))
	}
	return out, nil
}

// any reports whether a torrent matches a torrents/info filter, among only
// when it's given.
func (in *instance) any(ctx context.Context, filter string, only []string) (bool, error) {
	if len(only) == 0 {
		var ts []struct {
			Hash string `json:"hash"`
		}
		if err := in.get(ctx, "/api/v2/torrents/info", url.Values{"filter": {filter}, "limit": {"1"}}, &ts); err != nil {
			return false, err
		}
		return len(ts) > 0, nil
	}
	hs, err := in.hashes(ctx, filter)
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(hs, func(h string) bool {
		return slices.ContainsFunc(only, func(o string) bool { return strings.EqualFold(o, h) })
	}), nil
}

func (in *instance) preconditions(ctx context.Context, req action.Request) error {
	v, err := in.verbs(ctx)
	if err != nil {
		return err
	}
	only := hashesOf(req)
	of := ""
	if len(only) > 0 {
		of = " of those asked for"
	}
	if req.Action == ActPause {
		if running, err := in.any(ctx, v.running, only); err != nil {
			return err
		} else if !running {
			return action.Precondition("No torrent" + of + " in " + req.Target.Name + " is running.")
		}
		return nil
	}
	if paused, err := in.any(ctx, v.paused, only); err != nil {
		return err
	} else if !paused {
		return action.Precondition("No torrent" + of + " in " + req.Target.Name + " is paused.")
	}
	return nil
}

// Execute implements integration.Actor: one POST, with hashes=all or the
// hashes asked for.
func (in *instance) Execute(ctx context.Context, req action.Request, run *action.TaskRun) error {
	v, err := in.verbs(ctx)
	if err != nil {
		return err
	}
	verb, words := v.pause, "pause"
	if req.Action == ActResume {
		verb, words = v.resume, "resume"
	}
	which, what := "all", "all torrents"
	if only := hashesOf(req); len(only) > 0 {
		which, what = strings.Join(only, "|"), strconv.Itoa(len(only))+" torrents"
	}
	run.Step("Ask " + req.Target.Name + " to " + words + " " + what)
	if err := in.post(ctx, "/api/v2/torrents/"+verb, url.Values{"hashes": {which}}); err != nil {
		return err
	}
	run.Logf("qBittorrent accepted torrents/%s for %s", verb, what)
	return nil
}

// verify waits until no torrent it acted on is running (after a pause) or
// paused (after a resume).
func (in *instance) verify(ctx context.Context, req action.Request, run *action.TaskRun) error {
	step, left := "Check that none of them is still running", "running"
	if req.Action == ActResume {
		step, left = "Check that none of them is still paused", "paused"
	}
	only := hashesOf(req)
	return act.Until(ctx, run, step, func(ctx context.Context) (bool, error) {
		v, err := in.verbs(ctx)
		if err != nil {
			return false, err
		}
		filter := v.running
		if req.Action == ActResume {
			filter = v.paused
		}
		some, err := in.any(ctx, filter, only)
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
