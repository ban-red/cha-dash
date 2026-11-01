package pbs

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/msg"
)

// Verify now: POST /admin/datastore/{store}/verify, for the whole
// datastore or one snapshot group (backup-type and backup-id), optionally
// in a namespace. T1: PBS reads every chunk and checks it against its
// checksum; nothing changes but each snapshot's verify state. It runs as a
// PBS task under the control token, which needs Datastore.Verify there
// (DatastoreAdmin has it).

const (
	groupPattern = `^(vm|ct|host)/[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$`
	nsPattern    = `^[A-Za-z0-9_][A-Za-z0-9._-]{0,31}(/[A-Za-z0-9_][A-Za-z0-9._-]{0,31}){0,7}$`
)

// verifyPrivs is what the control token needs on the datastore.
var verifyPrivs = []string{"Datastore.Verify"}

type verifyParams struct {
	Group string `json:"group"`
	NS    string `json:"ns"`
}

// verifyResult is the newest verify run from the hub on one datastore,
// shown on it (verify_last, verify_state) until a verify job's is newer.
type verifyResult struct {
	At    time.Time `json:"at"`
	State string    `json:"state"` // PBS's exit status: OK, WARNINGS: n, or the error
	Group string    `json:"group,omitempty"`
}

// verified holds the results by instance and datastore. The control
// instance records them as its task ends, and the read instance, which
// publishes the datastore, as it reads the task back. Each is also kept in
// the instance's state (verifyStateKey), so it survives a restart: the
// read instance loads them at its first sync.
var verified sync.Map

func verifiedKey(instance, store string) string { return instance + "\x00" + store }

// verifyStateKey is the instance state that keeps the last verify run
// from the hub per datastore: {"v": 1, "stores": {store: verifyResult}}.
const (
	verifyStateKey     = "verify"
	verifyStateVersion = 1
)

type verifyState struct {
	V      int                     `json:"v"`
	Stores map[string]verifyResult `json:"stores"`
}

// verifyStateMu serializes reading and rewriting the state, since the
// read and control instances share it.
var verifyStateMu sync.Mutex

// loadVerified fills verified from the instance's state, once; a state
// that can't be read is tried again at the next sync. A result already in
// memory that's newer wins.
func (in *instance) loadVerified(ctx context.Context) {
	if in.verifyLoaded || in.d.State == nil {
		return
	}
	verifyStateMu.Lock()
	b, err := in.d.State.Load(ctx, verifyStateKey)
	verifyStateMu.Unlock()
	if err != nil {
		in.d.Log.Warn("couldn't load the last verify results; trying again at the next sync", "err", err)
		return
	}
	in.verifyLoaded = true
	var st verifyState
	if b == nil {
		return
	}
	if err := json.Unmarshal(b, &st); err != nil || st.V != verifyStateVersion {
		in.d.Log.Warn("the saved verify results are unreadable; they show again after the next verify", "err", err)
		return
	}
	for store, res := range st.Stores {
		key := verifiedKey(in.key, store)
		if v, ok := verified.Load(key); ok && !v.(verifyResult).At.Before(res.At) {
			continue
		}
		verified.Store(key, res)
	}
}

// saveVerified keeps one datastore's result in the instance's state.
func (in *instance) saveVerified(ctx context.Context, store string, res verifyResult) {
	if in.d.State == nil {
		return
	}
	verifyStateMu.Lock()
	defer verifyStateMu.Unlock()
	st := verifyState{V: verifyStateVersion, Stores: map[string]verifyResult{}}
	if b, err := in.d.State.Load(ctx, verifyStateKey); err == nil && b != nil {
		var old verifyState
		if json.Unmarshal(b, &old) == nil && old.V == verifyStateVersion && old.Stores != nil {
			st.Stores = old.Stores
		}
	}
	if len(res.State) > 500 {
		res.State = res.State[:500]
	}
	st.Stores[store] = res
	b, err := json.Marshal(st)
	if err == nil {
		err = in.d.State.Save(ctx, verifyStateKey, b)
	}
	if err != nil {
		in.d.Log.Warn("couldn't keep the verify result", "store", store, "err", err)
	}
}

func (in *instance) verifyDef() action.Def {
	group := &integration.Schema{Type: "string", Title: "Snapshot group", Pattern: groupPattern,
		Description: "Verify one group, such as vm/100 or ct/101. Empty: the whole datastore."}
	ns := &integration.Schema{Type: "string", Title: "Namespace", Pattern: nsPattern,
		Description: "The namespace the group is in. Empty: the root."}
	return action.Def{
		ID: ActVerify, Title: "Verify now", Targets: []string{"backup_target"}, Tier: action.T1, Async: true,
		Params:  integration.Params(integration.Object(integration.Prop("group", group), integration.Prop("ns", ns))),
		Resolve: in.resolveStore,
		Label: func(r action.Request) string {
			if p := verifyParamsOf(r); p.Group != "" {
				return msg.New("pbs.verify_label_group", "group", p.Group, "target", r.Target.Name).Text()
			}
			return msg.New("pbs.verify_label", "target", r.Target.Name).Text()
		},
		Steps: func(r action.Request) []action.Step {
			return []action.Step{
				{Label: msg.New("pbs.verify_step", "what", verifyWhat(verifyParamsOf(r)), "target", r.Target.Name).Text()},
				{Label: msg.New("pbs.verify_step_result").Text()},
			}
		},
		Preconditions: in.verifyPreconditions,
		Permitted:     in.mayVerify,
		Timeout:       action.AsyncTimeout,
		Execute:       in.runVerify,
		Verify:        in.verifyOK,
	}
}

func verifyParamsOf(r action.Request) verifyParams {
	var p verifyParams
	_ = r.Decode(&p)
	return p
}

func verifyWhat(p verifyParams) string {
	if p.Group != "" {
		return p.Group
	}
	return msg.New("pbs.verify_every_group").Text()
}

// verifyPreconditions refuses a datastore that's offline for maintenance;
// read-only maintenance still lets PBS read it.
func (in *instance) verifyPreconditions(ctx context.Context, r action.Request) error {
	var items []datastoreItem
	if err := in.get(ctx, "/admin/datastore", nil, &items); err != nil {
		return readRefusal(err)
	}
	for _, it := range items {
		if it.Store != r.Target.Ref || it.Maintenance == "" {
			continue
		}
		mode, _, _ := strings.Cut(it.Maintenance, ",")
		mode = strings.TrimPrefix(mode, "type=")
		if mode != "read-only" {
			return action.Precondition(msg.New("pbs.verify_maintenance", "target", r.Target.Name, "mode", mode).Text())
		}
	}
	return nil
}

// mayVerify refuses a verify the control token couldn't run, so it shows
// as unavailable when planned (Def.Permitted, on the control instance).
func (in *instance) mayVerify(ctx context.Context, r action.Request) error {
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
	for _, priv := range verifyPrivs {
		if !perms.has(path, priv) {
			return action.Unavailable(msg.New("pbs.verify_lacks", "target", r.Target.Name, "path", path).Text(), in.key)
		}
	}
	return nil
}

func (in *instance) runVerify(ctx context.Context, r action.Request, run *action.TaskRun) error {
	p := verifyParamsOf(r)
	store := r.Target.Ref
	form := url.Values{}
	if p.Group != "" {
		typ, id, _ := strings.Cut(p.Group, "/")
		form.Set("backup-type", typ)
		form.Set("backup-id", id)
	}
	if p.NS != "" {
		form.Set("ns", p.NS)
	}
	run.Step(msg.New("pbs.verify_running", "what", verifyWhat(p), "target", store).Text())
	upid, err := in.write(ctx, http.MethodPost, "/admin/datastore/"+url.PathEscape(store)+"/verify", form)
	if err != nil {
		return verifyFail(err, store)
	}
	upids.Store(r.Task, upid)
	err = in.follow(ctx, run, upid, func(exit string) error {
		in.recordVerify(ctx, store, p.Group, exit)
		return action.Fail("upstream", msg.New("pbs.verify_failed", "target", store, "reason", exit).Text())
	})
	if err != nil {
		upids.Delete(r.Task)
		return err
	}
	in.recordVerify(ctx, store, p.Group, "OK")
	return nil
}

func (in *instance) recordVerify(ctx context.Context, store, group, state string) {
	res := verifyResult{At: in.d.Now().UTC(), State: state, Group: group}
	verified.Store(verifiedKey(in.key, store), res)
	in.saveVerified(context.WithoutCancel(ctx), store, res)
}

func verifyFail(err error, store string) error {
	if ae, ok := errors.AsType[*apiError](err); ok {
		switch ae.Code {
		case http.StatusUnauthorized:
			return action.Fail("auth", "Proxmox Backup Server rejected the control token. Enter it again in the instance's settings.")
		case http.StatusForbidden:
			return action.Fail("forbidden", msg.New("pbs.verify_forbidden", "reason", cmp.Or(ae.Reason, "permission check failed"), "target", store).Text())
		}
		if ae.Reason != "" {
			return action.Fail("upstream", msg.New("pbs.verify_failed", "target", store, "reason", ae.Reason).Text())
		}
	}
	_, _, m := integration.Classify(err)
	return action.Fail("upstream", msg.New("pbs.verify_failed", "target", store, "reason", m).Text())
}

// verifyOK reads the verify task's status back with the read token and
// records the result for the datastore.
func (in *instance) verifyOK(ctx context.Context, r action.Request, run *action.TaskRun) error {
	v, ok := upids.LoadAndDelete(r.Task)
	if !ok {
		return action.Fail("verify_failed", msg.New("pbs.verify_lost").Text())
	}
	var st taskStatus
	if err := in.get(ctx, taskPath(v.(string), "status"), nil, &st); err != nil {
		_, _, m := integration.Classify(err)
		return action.Fail("verify_failed", msg.New("pbs.verify_unreadable", "reason", m).Text())
	}
	p := verifyParamsOf(r)
	if st.Status != "stopped" || !taskOK(st.ExitStatus) {
		status := cmp.Or(st.ExitStatus, st.Status)
		if st.Status == "stopped" {
			in.recordVerify(ctx, r.Target.Ref, p.Group, status)
		}
		return action.Fail("verify_failed", msg.New("pbs.verify_reported", "status", status).Text())
	}
	in.recordVerify(ctx, r.Target.Ref, p.Group, st.ExitStatus)
	return nil
}
