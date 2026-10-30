package pbs

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/integration"
)

// actPrune prunes a datastore (or the namespace its prune job covers) by
// the retention policy of its prune job, now: the Demo Home's "Prune per
// policy" (T2). It's POST /admin/datastore/{store}/prune-datastore with
// the job's keep-* options, namespace and depth, run as a PBS task with the
// control token. Pruning only marks snapshots gone; the space comes back
// at the next garbage collection.
const actPrune = "pbs.datastore.prune"

// Waiting on PBS. Variables, so tests can make them short.
var taskPoll = time.Second

const jobIDPattern = `^[A-Za-z0-9_][A-Za-z0-9._-]{0,63}$`

// Actions implements integration.Actor.
func (in *instance) Actions() []action.Def {
	job := &integration.Schema{Type: "string", Title: "Prune job", Pattern: jobIDPattern,
		Description: "Whose retention policy to prune by. Empty: the datastore's prune job (an enabled one first)."}
	return []action.Def{{
		ID: actPrune, Title: "Prune per policy", Targets: []string{"backup_target"}, Tier: action.T2, Async: true,
		Params:  integration.Params(integration.Object(integration.Prop("job", job))),
		Resolve: in.resolveStore,
		Label:   func(r action.Request) string { return "Prune " + r.Target.Name + " per policy" },
		Steps: func(r action.Request) []action.Step {
			return []action.Step{
				{Label: "Remove the snapshots on " + r.Target.Name + " that its prune job's retention policy doesn't keep"},
				{Label: "Check PBS finished the prune; the space comes back at the next garbage collection"},
			}
		},
		Preconditions: func(ctx context.Context, r action.Request) error {
			_, err := in.pruneJobFor(ctx, r, false)
			return err
		},
		Permitted: in.mayPrune,
		Timeout:   action.AsyncTimeout,
		Execute:   in.runPrune,
		Verify:    in.pruneOK,
	}}
}

// Execute implements integration.Actor.
func (in *instance) Execute(ctx context.Context, req action.Request, run *action.TaskRun) error {
	if req.Action != actPrune {
		return action.Fail("unavailable", "Proxmox Backup Server has no action called "+req.Action+".")
	}
	return in.runPrune(ctx, req, run)
}

// resolveStore finds a datastore by its resource ID (pbs:<instance>/datastore/<store>).
func (in *instance) resolveStore(ctx context.Context, target string) (action.Target, error) {
	store, ok := strings.CutPrefix(target, ID+":"+in.key+"/datastore/")
	if !ok || store == "" {
		return action.Target{}, action.ErrNotFound
	}
	var items []datastoreItem
	if err := in.get(ctx, "/admin/datastore", nil, &items); err != nil {
		return action.Target{}, readRefusal(err)
	}
	for _, it := range items {
		if it.Store == store {
			sum := sha256.Sum256([]byte("backup_target\x00" + store + "\x00" + it.Maintenance))
			return action.Target{ID: target, Kind: "backup_target", Name: store, State: hex.EncodeToString(sum[:8]), Ref: store}, nil
		}
	}
	return action.Target{}, action.ErrNotFound
}

// prunePrivs are what the control token needs on the datastore: Audit to
// read its prune job back when it runs, and Modify to prune every group,
// not only the token's own (Datastore.Prune). Only DatastoreAdmin holds
// Modify, which is what the control recipe grants.
var prunePrivs = []string{"Datastore.Audit", "Datastore.Modify"}

// mayPrune refuses a prune the control token couldn't carry out, so it
// shows as unavailable when planned instead of failing once approved. It
// runs on the control instance (Def.Permitted) and reads the token's own
// privileges on the datastore.
func (in *instance) mayPrune(ctx context.Context, r action.Request) error {
	path := "/datastore/" + r.Target.Ref
	perms, err := action.Memo(ctx, ID+"/"+in.key+"/control/permissions"+path, func(ctx context.Context) (privileges, error) {
		var p privileges
		err := in.get(ctx, "/access/permissions", url.Values{"path": {path}}, &p)
		return p, err
	})
	if statusOf(err) == http.StatusUnauthorized {
		return action.Unavailable("Proxmox Backup Server rejected the control token. Enter it again in the instance's settings.", in.key)
	}
	if err != nil {
		return readRefusal(err)
	}
	var lack []string
	for _, priv := range prunePrivs {
		if !perms.has(path, priv) {
			lack = append(lack, priv)
		}
	}
	if len(lack) > 0 {
		return action.Unavailable(fmt.Sprintf("The control token can't prune %s: it lacks %s on %s. Give it the DatastoreAdmin role there, as the control recipe does.",
			r.Target.Name, strings.Join(lack, " and "), path), in.key)
	}
	return nil
}

func readRefusal(err error) error {
	_, _, msg := integration.Classify(err)
	return action.Unavailable("Cha Dash couldn't read Proxmox Backup Server just now: "+msg+".", "")
}

// pruneJobFor is the prune job whose policy applies: the one named, else
// the datastore's (enabled first, then by ID). It refuses a datastore in
// maintenance, one without a job, and a policy that keeps everything.
//
// running is true when the control token reads it to prune. The plan saw
// the job with the read token, so finding none then means the job went
// away since, or the control token can't see it; it never means the
// datastore has none.
func (in *instance) pruneJobFor(ctx context.Context, r action.Request, running bool) (pruneJob, error) {
	store := r.Target.Ref
	var items []datastoreItem
	if err := in.get(ctx, "/admin/datastore", nil, &items); err != nil {
		return pruneJob{}, readRefusal(err)
	}
	for _, it := range items {
		if it.Store == store && it.Maintenance != "" {
			mode, _, _ := strings.Cut(it.Maintenance, ",")
			return pruneJob{}, action.Precondition(fmt.Sprintf("%s is in maintenance (%s), so it can't be pruned now.", store, strings.TrimPrefix(mode, "type=")))
		}
	}
	var jobs []pruneJob
	if err := in.get(ctx, "/admin/prune", nil, &jobs); err != nil {
		if statusOf(err) == http.StatusNotFound || statusOf(err) == http.StatusNotImplemented {
			return pruneJob{}, action.Precondition("This Proxmox Backup Server has no prune jobs (it's older than 2.2), so there's no policy to prune by.")
		}
		return pruneJob{}, readRefusal(err)
	}
	var p struct {
		Job string `json:"job"`
	}
	_ = r.Decode(&p)
	var mine []pruneJob
	for _, j := range jobs {
		if j.Store == store && (p.Job == "" || j.ID == p.Job) {
			mine = append(mine, j)
		}
	}
	slices.SortStableFunc(mine, func(a, b pruneJob) int {
		return cmp.Or(cmp.Compare(a.Disable, b.Disable), strings.Compare(a.ID, b.ID))
	})
	switch {
	case len(mine) == 0 && p.Job != "":
		return pruneJob{}, action.Precondition(fmt.Sprintf("%s has no prune job called %s.", store, p.Job))
	case len(mine) == 0 && running:
		return pruneJob{}, action.Precondition(fmt.Sprintf("The control token sees no prune job on %s. It was removed since this was planned, or the token can't read it: it needs Datastore.Audit on /datastore/%s.", store, store))
	case len(mine) == 0:
		return pruneJob{}, action.Precondition(fmt.Sprintf("%s has no prune job, so there's no retention policy to prune by. Add one in Proxmox Backup Server first.", store))
	case keeps(mine[0].keepOptions).Encode() == "":
		return pruneJob{}, action.Precondition(fmt.Sprintf("The prune job %s keeps everything, so pruning by it would remove nothing.", mine[0].ID))
	}
	return mine[0], nil
}

// keeps is a policy as prune-datastore's form fields.
func keeps(k keepOptions) url.Values {
	v := url.Values{}
	for _, o := range []struct {
		name string
		n    num
	}{{"keep-last", k.Last}, {"keep-hourly", k.Hourly}, {"keep-daily", k.Daily}, {"keep-weekly", k.Weekly}, {"keep-monthly", k.Monthly}, {"keep-yearly", k.Yearly}} {
		if o.n > 0 {
			v.Set(o.name, strconv.Itoa(int(o.n)))
		}
	}
	return v
}

// upids remembers the PBS task each Cha Dash task started, for Verify on
// the read instance. Keyed by the Cha Dash task ID.
var upids sync.Map

func (in *instance) runPrune(ctx context.Context, r action.Request, run *action.TaskRun) error {
	j, err := in.pruneJobFor(ctx, r, true)
	if err != nil {
		if ref, ok := errors.AsType[*action.Refusal](err); ok {
			return action.Fail(ref.Code, ref.Message)
		}
		return err
	}
	form := keeps(j.keepOptions)
	if j.NS != "" {
		form.Set("ns", j.NS)
	}
	if j.MaxDepth != nil {
		form.Set("max-depth", strconv.Itoa(int(*j.MaxDepth)))
	}
	store := r.Target.Ref
	run.Step(fmt.Sprintf("Pruning %s by %s's policy (%s)", store, j.ID, cmp.Or(retention(j.keepOptions), "keep all")))
	upid, err := in.write(ctx, http.MethodPost, "/admin/datastore/"+url.PathEscape(store)+"/prune-datastore", form)
	if err != nil {
		return writeFail(err, store)
	}
	upids.Store(r.Task, upid)
	if err := in.follow(ctx, run, upid, store); err != nil {
		upids.Delete(r.Task)
		return err
	}
	return nil
}

// write sends a form under /api2/json and returns the UPID it started.
func (in *instance) write(ctx context.Context, method, path string, form url.Values) (string, error) {
	req, err := http.NewRequestWithContext(ctx, method, in.base+"/api2/json"+path, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", in.auth)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := in.d.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if err := integration.CheckResponse(resp); err != nil {
		var se *integration.StatusError
		errors.As(err, &se)
		return "", &apiError{StatusError: se, Reason: reason(resp.Status, b)}
	}
	var env struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(b, &env); err != nil || !strings.HasPrefix(env.Data, "UPID:") {
		return "", fmt.Errorf("%s %s: PBS didn't start a task", method, path)
	}
	return env.Data, nil
}

func writeFail(err error, store string) error {
	if ae, ok := errors.AsType[*apiError](err); ok {
		switch ae.Code {
		case http.StatusUnauthorized:
			return action.Fail("auth", "Proxmox Backup Server rejected the control token. Enter it again in the instance's settings.")
		case http.StatusForbidden:
			return action.Fail("forbidden", fmt.Sprintf("Proxmox Backup Server refused the control token (%s). It needs DatastoreAdmin on /datastore/%s to prune it; the recipe's control variant grants it.",
				cmp.Or(ae.Reason, "permission check failed"), store))
		}
		if ae.Reason != "" {
			return action.Fail("upstream", fmt.Sprintf("Proxmox Backup Server couldn't prune %s: %s.", store, ae.Reason))
		}
	}
	_, _, msg := integration.Classify(err)
	return action.Fail("upstream", fmt.Sprintf("Proxmox Backup Server couldn't prune %s: %s.", store, msg))
}

type taskStatus struct {
	Status     string `json:"status"`
	ExitStatus string `json:"exitstatus"`
}

type logLine struct {
	N int    `json:"n"`
	T string `json:"t"`
}

func taskOK(exit string) bool { return exit == "OK" || strings.HasPrefix(exit, "WARNINGS") }

func taskPath(upid, sub string) string {
	return "/nodes/localhost/tasks/" + url.PathEscape(upid) + "/" + sub
}

// follow waits for a PBS task to end, copying its log into run's log.
func (in *instance) follow(ctx context.Context, run *action.TaskRun, upid, store string) error {
	next := 0
	for {
		var lines []logLine
		if err := in.get(ctx, taskPath(upid, "log"), url.Values{"start": {strconv.Itoa(next)}, "limit": {"500"}}, &lines); err == nil {
			for _, l := range lines {
				if l.N <= next {
					continue
				}
				next = l.N
				if l.T != "" {
					run.Logf("%s", l.T)
				}
			}
		} else if ctx.Err() != nil {
			return ctx.Err()
		}
		var st taskStatus
		if err := in.get(ctx, taskPath(upid, "status"), nil, &st); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
		} else if st.Status == "stopped" {
			if taskOK(st.ExitStatus) {
				return nil
			}
			return action.Fail("upstream", fmt.Sprintf("Proxmox Backup Server couldn't prune %s: %s.", store, cmp.Or(st.ExitStatus, "the task failed")))
		}
		t := time.NewTimer(taskPoll)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
}

// pruneOK reads the prune task's status back with the read token.
func (in *instance) pruneOK(ctx context.Context, r action.Request, run *action.TaskRun) error {
	v, ok := upids.LoadAndDelete(r.Task)
	if !ok {
		return action.Fail("verify_failed", "Cha Dash lost track of the prune task.")
	}
	var st taskStatus
	if err := in.get(ctx, taskPath(v.(string), "status"), nil, &st); err != nil {
		_, _, msg := integration.Classify(err)
		return action.Fail("verify_failed", "Cha Dash couldn't read the prune task back: "+msg+".")
	}
	if st.Status != "stopped" || !taskOK(st.ExitStatus) {
		return action.Fail("verify_failed", "Proxmox Backup Server reports the prune task as "+cmp.Or(st.ExitStatus, st.Status)+".")
	}
	return nil
}

var _ integration.Actor = (*instance)(nil)
