package arr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"dash.cha.sh/integrations/internal/act"
	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/integration"
)

// Actions (v0.2): "search for missing" on Sonarr, Radarr and Lidarr (T1,
// no undo: a search only looks). It's the app's own command
// (MissingEpisodeSearch, MissingMoviesSearch, MissingAlbumSearch), sent with
// the instance's API key: the *arrs have one key with full access, so
// there's no separate control credential to ask for.

// Command is a /command resource: what POST /command answers and GET
// /command/{id} reads.
type Command struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"` // queued, started, completed, failed, aborted, cancelled, orphaned
}

// commandFailed are the statuses of a command that didn't do its job.
var commandFailed = map[string]bool{"failed": true, "aborted": true, "cancelled": true, "orphaned": true}

// Post sends body as JSON to path (under the API path) and reads the
// answer into dst. Errors are as Get's, as action errors where the UI can
// show them as they are.
func (c *Client) Post(ctx context.Context, path string, body, dst any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+c.App.APIPath+path, bytes.NewReader(b))
	if err != nil {
		return &integration.ConfigError{Field: "url", Message: "the URL can't be used: " + err.Error()}
	}
	req.Header.Set("X-Api-Key", c.key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return action.Fail("auth", c.App.Name+" rejected the API key.")
	case http.StatusNotFound:
		return action.Fail("unsupported", fmt.Sprintf("%s doesn't know POST %s%s; check it's %s %d or later.", c.App.Name, c.App.APIPath, path, c.App.Name, c.App.MinMajor))
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%s answered POST %s%s with %s: %s", c.App.Name, c.App.APIPath, path, resp.Status, strings.TrimSpace(string(msg)))
	}
	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		return fmt.Errorf("%s's answer to POST %s%s isn't JSON Cha Dash understands: %v", c.App.Name, c.App.APIPath, path, err)
	}
	return nil
}

// SearchMissingID is a media *arr's search action: sonarr.app.search-missing.
func (m MediaApp) SearchMissingID() string { return m.ID + ".app.search-missing" }

// commands remembers the command each task started, for Verify.
type commands struct {
	mu     sync.Mutex
	byTask map[string]int
}

func (c *commands) set(task string, id int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.byTask == nil {
		c.byTask = map[string]int{}
	}
	c.byTask[task] = id
}

func (c *commands) take(task string) (int, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	id, ok := c.byTask[task]
	delete(c.byTask, task)
	return id, ok
}

// Actions implements integration.Actor: none for an app without a search
// command.
func (in *media) Actions() []action.Def {
	if in.m.SearchCommand == "" {
		return nil
	}
	name := in.cfg.Name
	if name == "" {
		name = in.m.Name
	}
	what := "missing " + in.m.MissingNoun
	return []action.Def{{
		ID: in.m.SearchMissingID(), Title: "Search for " + what + " in", Targets: []string{"app"}, Tier: action.T1,
		Resolve:       act.App(in.m.ID+":"+InstanceKey(in.cfg)+"/app", name),
		Preconditions: in.searchPreconditions,
		Label:         func(r action.Request) string { return "Search for " + what + " in " + r.Target.Name },
		Steps: func(r action.Request) []action.Step {
			return act.Steps("Ask "+r.Target.Name+" to search for every monitored "+strings.TrimSuffix(in.m.MissingNoun, "s")+" that's missing",
				"Check that "+r.Target.Name+" took the search on")
		},
		Verify: in.searchVerify,
	}}
}

// running reports whether the search command is already queued or
// running.
func (in *media) running(ctx context.Context) (bool, error) {
	var cmds []Command
	if err := in.c.Get(ctx, "/command", nil, &cmds); err != nil {
		return false, err
	}
	for _, c := range cmds {
		if strings.EqualFold(c.Name, in.m.SearchCommand) && (c.Status == "queued" || c.Status == "started") {
			return true, nil
		}
	}
	return false, nil
}

// SearchMissingMax is how many missing items search missing takes on at
// once: below it, one search; at it or above, the app's own Missing page,
// where someone picks what to search for, so one click doesn't send every
// indexer hundreds of queries and queue a pile of downloads.
const SearchMissingMax = 10

func (in *media) searchPreconditions(ctx context.Context, req action.Request) error {
	n, found, err := in.c.Total(ctx, "/wanted/missing", url.Values{"monitored": {"true"}})
	if err != nil {
		return err
	}
	if found && n == 0 {
		return action.Precondition("Nothing monitored is missing in " + req.Target.Name + ".")
	}
	if found && n >= SearchMissingMax {
		return action.UnavailableInstead(
			fmt.Sprintf("%d monitored %s are missing in %s. Cha Dash searches for fewer than %d at once; pick them in %s's Missing page instead.",
				n, in.m.MissingNoun, req.Target.Name, SearchMissingMax, in.m.Name),
			action.Link{Title: "Open Missing in " + req.Target.Name, URL: strings.TrimSuffix(in.c.URL(), "/") + "/wanted/missing"})
	}
	busy, err := in.running(ctx)
	if err != nil {
		return err
	}
	if busy {
		return action.Precondition(req.Target.Name + " is already searching for missing " + in.m.MissingNoun + ".")
	}
	return nil
}

// Execute implements integration.Actor: POST /command.
func (in *media) Execute(ctx context.Context, req action.Request, run *action.TaskRun) error {
	run.Step("Ask " + req.Target.Name + " to search for missing " + in.m.MissingNoun)
	var cmd Command
	if err := in.c.Post(ctx, "/command", map[string]any{"name": in.m.SearchCommand}, &cmd); err != nil {
		return err
	}
	if cmd.ID == 0 {
		return action.Fail("failed", in.m.Name+" didn't queue the search.")
	}
	in.cmds.set(req.Task, cmd.ID)
	run.Logf("%s queued command %d (%s)", in.m.Name, cmd.ID, cmd.Name)
	return nil
}

// searchVerify reads the command back: queued, started or completed is
// taken on; failed, aborted or cancelled isn't. The search itself can run
// for a long time and isn't waited for.
func (in *media) searchVerify(ctx context.Context, req action.Request, run *action.TaskRun) error {
	id, ok := in.cmds.take(req.Task)
	if !ok {
		return errors.New("the search's command ID was lost")
	}
	return act.Until(ctx, run, "Check that "+req.Target.Name+" took the search on", func(ctx context.Context) (bool, error) {
		var cmd Command
		if err := in.c.Get(ctx, fmt.Sprintf("/command/%d", id), nil, &cmd); err != nil {
			return false, err
		}
		if commandFailed[cmd.Status] {
			return false, action.Fail("search_failed", fmt.Sprintf("%s's search ended as %s.", in.m.Name, cmd.Status))
		}
		return true, nil
	})
}
