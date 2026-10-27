package proxmox

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
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/integration"
)

// Guest actions, per docs/plan/08-v0.2.md's Tiers table. Each runs as a PVE
// task (a UPID) with the control token: Execute starts it, follows its log
// into the task's log and progress, and fails when PVE says it failed;
// Verify reads the result back with the read token.
//
// | Action                          | Tier | Undo                    | Control token needs              |
// |---------------------------------|------|-------------------------|----------------------------------|
// | proxmox.guest.start             | T1   | shutdown                | VM.PowerMgmt                     |
// | proxmox.guest.shutdown          | T1   | start                   | VM.PowerMgmt                     |
// | proxmox.guest.reboot            | T2   | —                       | VM.PowerMgmt                     |
// | proxmox.guest.stop (hard stop)  | T2   | —                       | VM.PowerMgmt                     |
// | proxmox.guest.snapshot.create   | T1   | snapshot.delete (T3)    | VM.Snapshot                      |
// | proxmox.guest.snapshot.rollback | T3   | —                       | VM.Snapshot, VM.Snapshot.Rollback |
// | proxmox.guest.snapshot.delete   | T3   | —                       | VM.Snapshot                      |
// | proxmox.guest.backup (vzdump)   | T1   | —                       | VM.Backup, Datastore.AllocateSpace |
//
// Shutdown, reboot, hard stop and rollback are Disrupts: the runtime makes
// them T4 when the guest runs the hub or the router (see runtime.Actions),
// and TierFor does the same for the hub_vmid hint.
const (
	actStart        = "proxmox.guest.start"
	actShutdown     = "proxmox.guest.shutdown"
	actReboot       = "proxmox.guest.reboot"
	actStop         = "proxmox.guest.stop"
	actSnapCreate   = "proxmox.guest.snapshot.create"
	actSnapRollback = "proxmox.guest.snapshot.rollback"
	actSnapDelete   = "proxmox.guest.snapshot.delete"
	actBackup       = "proxmox.guest.backup"
)

// guestKinds are the resource kinds the guest actions apply to.
var guestKinds = []string{"vm", "lxc"}

// Waiting on PVE. Variables, so tests can make them short.
var (
	taskPoll  = time.Second     // between reads of a running task
	statePoll = 2 * time.Second // between reads of a guest's state in Verify
	// verifyTimeout bounds waiting for a guest to report its new state.
	verifyTimeout = 3 * time.Minute
)

// guestTimeout gives PVE's own timeout for a clean shutdown or reboot.
const guestTimeout = 180 // seconds

// snapNameRE is a snapshot name PVE accepts (pve-configid): a letter, then
// letters, digits, - and _.
const snapNameRE = `^[A-Za-z][A-Za-z0-9_-]{1,39}$`

func snapParams(withExtras bool) action.Schema {
	name := &integration.Schema{Type: "string", Title: "Snapshot name", Pattern: snapNameRE,
		Description: "A letter, then letters, digits, - or _ (2–40 characters)."}
	props := []integration.Property{integration.Prop("name", name)}
	if withExtras {
		desc := integration.String("Description")
		maxDesc := 200
		desc.MaxLength = &maxDesc
		props = append(props,
			integration.Prop("description", desc),
			integration.Prop("vmstate", integration.Boolean("Include the VM's memory").Describe("VMs only: the snapshot can then resume where it was.")),
		)
	}
	return integration.Params(integration.Object(props...).Require("name"))
}

func backupParams() action.Schema {
	storage := &integration.Schema{Type: "string", Title: "Storage", Pattern: `^[A-Za-z][A-Za-z0-9_.-]{0,99}$`,
		Description: "Where the backup goes. Empty: the storage of a backup job that covers the guest."}
	mode := integration.Enum("Mode", "snapshot", "suspend", "stop").WithDefault("snapshot")
	return integration.Params(integration.Object(integration.Prop("storage", storage), integration.Prop("mode", mode)))
}

// Actions implements integration.Actor.
func (in *instance) Actions() []action.Def {
	guest := func(id, title string, tier action.Tier) action.Def {
		return action.Def{ID: id, Title: title, Targets: guestKinds, Tier: tier, Async: true,
			Resolve: in.resolveGuest, Timeout: 5 * time.Minute, VerifyTimeout: verifyTimeout}
	}
	start := guest(actStart, "Start", action.T1)
	start.Preconditions = in.when(func(name string, s guestStatus) string {
		switch {
		case s.Status == "running":
			return name + " is already running."
		case s.Status != "stopped":
			return fmt.Sprintf("%s is %s, so it can't be started.", name, s.Status)
		}
		return ""
	})
	start.Steps = in.steps("Ask {node} to start {name}", "Wait for it to report running")
	start.Undo = undo(actShutdown, "Shut down")
	start.Execute = in.power("start", nil)
	start.Verify = in.waitState("running")

	shutdown := guest(actShutdown, "Shut down", action.T1)
	shutdown.Disrupts, shutdown.TierFor = true, in.tierFor
	shutdown.Preconditions = in.when(running("shut down"))
	shutdown.Steps = in.steps("Ask {name} to shut down cleanly (on {node})", "Wait for it to report stopped")
	shutdown.Undo = undo(actStart, "Start")
	shutdown.Execute = in.power("shutdown", url.Values{"timeout": {strconv.Itoa(guestTimeout)}})
	shutdown.Verify = in.waitState("stopped")

	reboot := guest(actReboot, "Reboot", action.T2)
	reboot.Disrupts, reboot.TierFor = true, in.tierFor
	reboot.Preconditions = in.when(func(name string, s guestStatus) string {
		if s.Status != "running" {
			return name + " isn't running, so it can't reboot. Start it instead."
		}
		return ""
	})
	reboot.Steps = in.steps("Ask {node} to reboot {name}", "Wait for it to report running")
	reboot.Execute = in.power("reboot", url.Values{"timeout": {strconv.Itoa(guestTimeout)}})
	reboot.Verify = in.waitState("running")

	stop := guest(actStop, "Hard stop", action.T2)
	stop.Disrupts, stop.TierFor = true, in.tierFor
	stop.Preconditions = in.when(running("stop"))
	stop.Steps = in.steps("Stop {name} on {node} at once, without shutting it down", "Wait for it to report stopped")
	stop.Execute = in.power("stop", nil)
	stop.Verify = in.waitState("stopped")

	snap := guest(actSnapCreate, "Snapshot", action.T1)
	snap.Params = snapParams(true)
	snap.Label = func(r action.Request) string { return fmt.Sprintf("Snapshot %s as %s", r.Target.Name, snapName(r)) }
	snap.Preconditions = in.snapshotCheck(false)
	snap.Steps = func(r action.Request) []action.Step {
		return []action.Step{{Label: fmt.Sprintf("Take snapshot %s of %s", snapName(r), r.Target.Name)}, {Label: "Check the snapshot is listed"}}
	}
	snap.Undo = func(r action.Request) *action.Undo {
		p, _ := json.Marshal(map[string]string{"name": snapName(r)})
		return &action.Undo{Action: actSnapDelete, Params: p, Label: "Delete snapshot " + snapName(r)}
	}
	snap.Execute = in.snapshotCreate
	snap.Verify = in.snapshotListed(true)

	rollback := guest(actSnapRollback, "Roll back", action.T3)
	rollback.Params = snapParams(false)
	rollback.Disrupts, rollback.TierFor = true, in.tierFor
	rollback.Label = func(r action.Request) string { return fmt.Sprintf("Roll %s back to %s", r.Target.Name, snapName(r)) }
	rollback.Preconditions = in.snapshotCheck(true)
	rollback.Steps = func(r action.Request) []action.Step {
		return []action.Step{{Label: fmt.Sprintf("Roll %s back to snapshot %s; what changed since is lost", r.Target.Name, snapName(r))}, {Label: "Check Proxmox VE finished the rollback"}}
	}
	rollback.Execute = in.snapshotTask(http.MethodPost, "rollback", "roll back to")
	rollback.Verify = in.taskOK

	del := guest(actSnapDelete, "Delete snapshot", action.T3)
	del.Params = snapParams(false)
	del.Label = func(r action.Request) string {
		return fmt.Sprintf("Delete snapshot %s of %s", snapName(r), r.Target.Name)
	}
	del.Preconditions = in.snapshotCheck(true)
	del.Steps = func(r action.Request) []action.Step {
		return []action.Step{{Label: fmt.Sprintf("Delete snapshot %s of %s", snapName(r), r.Target.Name)}, {Label: "Check it's gone"}}
	}
	del.Execute = in.snapshotTask(http.MethodDelete, "", "delete")
	del.Verify = in.snapshotListed(false)

	backup := guest(actBackup, "Back up now", action.T1)
	backup.Params = backupParams()
	backup.Label = func(r action.Request) string { return "Back up " + r.Target.Name + " now" }
	backup.Preconditions = in.backupCheck
	backup.Steps = func(r action.Request) []action.Step {
		return []action.Step{{Label: "Back up " + r.Target.Name + " with vzdump"}, {Label: "Check the backup task finished OK"}}
	}
	backup.Timeout = action.AsyncTimeout
	backup.Execute = in.vzdump
	backup.Verify = in.taskOK

	return []action.Def{start, shutdown, reboot, stop, snap, rollback, del, backup}
}

// Execute implements integration.Actor. Every Def carries its own Execute;
// this routes by ID for a caller that holds only the Actor.
func (in *instance) Execute(ctx context.Context, req action.Request, run *action.TaskRun) error {
	for _, d := range in.Actions() {
		if d.ID == req.Action {
			return d.Execute(ctx, req, run)
		}
	}
	return action.Fail("unavailable", "Proxmox VE has no action called "+req.Action+".")
}

// ── targets ──────────────────────────────────────────────────────────────

// guestTarget is a resolved guest: its node now, its type and VMID.
type guestTarget struct {
	node, typ string
	vmid      int
}

func (g guestTarget) String() string { return g.node + "/" + g.typ + "/" + strconv.Itoa(g.vmid) }

func (g guestTarget) path(segs ...string) string {
	return p(append([]string{"nodes", g.node, g.typ, strconv.Itoa(g.vmid)}, segs...)...)
}

func parseRef(s string) (guestTarget, bool) {
	parts := strings.Split(s, "/")
	if len(parts) != 3 {
		return guestTarget{}, false
	}
	vmid, err := strconv.Atoi(parts[2])
	return guestTarget{node: parts[0], typ: parts[1], vmid: vmid}, err == nil && (parts[1] == "qemu" || parts[1] == "lxc")
}

func refOf(r action.Request) guestTarget {
	g, _ := parseRef(r.Target.Ref)
	return g
}

// resolveGuest finds a guest by its resource ID (proxmox:<instance>/qemu/201)
// in /cluster/resources, as of now: the VMID is cluster-wide and the node
// moves with a migration. The state an approval must find unchanged is its
// kind, name, node and status.
func (in *instance) resolveGuest(ctx context.Context, target string) (action.Target, error) {
	local, ok := strings.CutPrefix(target, ID+":"+in.key+"/")
	if !ok {
		return action.Target{}, action.ErrNotFound
	}
	typ, num, _ := strings.Cut(local, "/")
	vmid, err := strconv.Atoi(num)
	if err != nil || (typ != "qemu" && typ != "lxc") {
		return action.Target{}, action.ErrNotFound
	}
	// Every guest action resolves the same way: while the hub lists a
	// guest's actions, they share one read (action.Memo).
	res, err := action.Memo(ctx, ID+"/"+in.key+"/cluster/resources/vm", func(ctx context.Context) ([]resource, error) {
		var res []resource
		err := in.get(ctx, "/cluster/resources", url.Values{"type": {"vm"}}, &res)
		return res, err
	})
	if err != nil {
		return action.Target{}, readRefusal(err)
	}
	for _, r := range res {
		if r.Type != typ || int(r.VMID) != vmid || r.Template.bool() {
			continue
		}
		kind := "vm"
		if typ == "lxc" {
			kind = "lxc"
		}
		name := cmp.Or(r.Name, strconv.Itoa(vmid))
		sum := sha256.Sum256([]byte(kind + "\x00" + name + "\x00" + r.Node + "\x00" + r.Status))
		return action.Target{ID: target, Kind: kind, Name: name, State: hex.EncodeToString(sum[:8]),
			Ref: guestTarget{node: r.Node, typ: typ, vmid: vmid}.String()}, nil
	}
	return action.Target{}, action.ErrNotFound
}

// readRefusal is a failed read while planning, as a sentence.
func readRefusal(err error) error {
	_, _, msg := integration.Classify(err)
	return action.Unavailable("Cha Dash couldn't read Proxmox VE just now: "+msg+".", "")
}

// tierFor is T4 for the guest the hub_vmid hint names: the hub would take
// itself down. (The runtime adds the graph's view: the hub's guest when the
// hint is unset, and the router.)
func (in *instance) tierFor(t action.Target) action.Tier {
	if g, ok := parseRef(t.Ref); ok && in.hubVMID != 0 && g.vmid == in.hubVMID {
		return action.T4
	}
	return action.T0
}

// ── preconditions ───────────────────────────────────────────────────────

// guestStatus is GET /nodes/{node}/{type}/{vmid}/status/current.
type guestStatus struct {
	Status    string `json:"status"` // running, stopped
	Lock      string `json:"lock"`
	QMPStatus string `json:"qmpstatus"` // qemu: running, paused, …
}

// While the hub lists a guest's actions, they share one read (action.Memo).
func (in *instance) current(ctx context.Context, g guestTarget) (guestStatus, error) {
	return action.Memo(ctx, ID+"/"+in.key+"/status/"+g.String(), func(ctx context.Context) (guestStatus, error) {
		var s guestStatus
		err := in.get(ctx, g.path("status", "current"), nil, &s)
		return s, err
	})
}

// when refuses with check's sentence, or when the guest is locked (a
// backup, a migration, a snapshot running).
func (in *instance) when(check func(name string, s guestStatus) string) func(context.Context, action.Request) error {
	return func(ctx context.Context, r action.Request) error {
		s, err := in.current(ctx, refOf(r))
		if err != nil {
			return readRefusal(err)
		}
		if s.Lock != "" {
			return action.Precondition(fmt.Sprintf("%s is locked (%s), so Cha Dash won't touch it until that's done.", r.Target.Name, s.Lock))
		}
		if msg := check(r.Target.Name, s); msg != "" {
			return action.Precondition(msg)
		}
		return nil
	}
}

func running(verb string) func(string, guestStatus) string {
	return func(name string, s guestStatus) string {
		if s.Status == "stopped" {
			return name + " is already stopped."
		}
		if s.Status != "running" {
			return fmt.Sprintf("%s is %s, so it can't be told to %s.", name, s.Status, verb)
		}
		return ""
	}
}

// snapshot is an entry of GET …/snapshot. "current" is the live state, not
// a snapshot.
type snapshot struct {
	Name string `json:"name"`
}

func (in *instance) snapshots(ctx context.Context, g guestTarget) ([]string, error) {
	var ss []snapshot
	if err := in.get(ctx, g.path("snapshot"), nil, &ss); err != nil {
		return nil, err
	}
	var out []string
	for _, s := range ss {
		if s.Name != "current" {
			out = append(out, s.Name)
		}
	}
	return out, nil
}

func snapName(r action.Request) string {
	var p struct {
		Name string `json:"name"`
	}
	_ = r.Decode(&p)
	return p.Name
}

// snapshotCheck refuses a snapshot action on a locked guest, and one whose
// snapshot exists (create) or doesn't (rollback, delete).
func (in *instance) snapshotCheck(mustExist bool) func(context.Context, action.Request) error {
	return func(ctx context.Context, r action.Request) error {
		g := refOf(r)
		s, err := in.current(ctx, g)
		if err != nil {
			return readRefusal(err)
		}
		if s.Lock != "" {
			return action.Precondition(fmt.Sprintf("%s is locked (%s), so Cha Dash won't touch it until that's done.", r.Target.Name, s.Lock))
		}
		names, err := in.snapshots(ctx, g)
		if err != nil {
			return readRefusal(err)
		}
		name := snapName(r)
		has := slices.Contains(names, name)
		switch {
		case mustExist && !has:
			return action.Precondition(fmt.Sprintf("%s has no snapshot called %s.", r.Target.Name, name))
		case !mustExist && has:
			return action.Precondition(fmt.Sprintf("%s already has a snapshot called %s.", r.Target.Name, name))
		}
		return nil
	}
}

type backupArgs struct {
	Storage string `json:"storage"`
	Mode    string `json:"mode"`
}

// backupStorage is where a backup of g goes: the param, else the storage of
// an enabled backup job that covers it.
func (in *instance) backupStorage(ctx context.Context, g guestTarget, r action.Request) (string, error) {
	var a backupArgs
	_ = r.Decode(&a)
	if a.Storage != "" {
		return a.Storage, nil
	}
	var jobs []backupJob
	if err := in.get(ctx, "/cluster/backup", nil, &jobs); err != nil {
		return "", readRefusal(err)
	}
	slices.SortFunc(jobs, func(a, b backupJob) int { return strings.Compare(a.ID, b.ID) })
	for _, j := range jobs {
		if j.enabled() && (j.Type == "" || j.Type == "vzdump") && covers(j, g) && j.Storage != "" {
			return j.Storage, nil
		}
	}
	return "", action.Precondition(fmt.Sprintf("Choose where to back up %s: no backup job covers it to take a storage from.", r.Target.Name))
}

// covers reports whether a job selects g: by VMID, or all guests (on its
// node, if it names one) minus the excluded. Pool selections can't be
// resolved without pool rights, so they don't count.
func covers(j backupJob, g guestTarget) bool {
	id := strconv.Itoa(g.vmid)
	list := func(s string) []string {
		return strings.FieldsFunc(s, func(c rune) bool { return c == ',' || c == ' ' })
	}
	switch {
	case j.VMID != "":
		return slices.Contains(list(j.VMID), id)
	case j.All.bool():
		return (j.Node == "" || j.Node == g.node) && !slices.Contains(list(j.Exclude), id)
	}
	return false
}

func (in *instance) backupCheck(ctx context.Context, r action.Request) error {
	g := refOf(r)
	s, err := in.current(ctx, g)
	if err != nil {
		return readRefusal(err)
	}
	if s.Lock == "backup" {
		return action.Precondition(r.Target.Name + " is being backed up already.")
	}
	if s.Lock != "" {
		return action.Precondition(fmt.Sprintf("%s is locked (%s), so Cha Dash won't back it up until that's done.", r.Target.Name, s.Lock))
	}
	_, err = in.backupStorage(ctx, g, r)
	return err
}

// ── plan text ───────────────────────────────────────────────────────────

// steps renders step labels with the guest's {name} and {node}.
func (in *instance) steps(labels ...string) func(action.Request) []action.Step {
	return func(r action.Request) []action.Step {
		fill := strings.NewReplacer("{name}", r.Target.Name, "{node}", refOf(r).node)
		out := make([]action.Step, len(labels))
		for i, l := range labels {
			out[i] = action.Step{Label: fill.Replace(l)}
		}
		return out
	}
}

// undo offers the inverse power action.
func undo(id, title string) func(action.Request) *action.Undo {
	return func(r action.Request) *action.Undo {
		return &action.Undo{Action: id, Params: json.RawMessage("{}"), Label: title + " " + r.Target.Name}
	}
}

// ── execute ─────────────────────────────────────────────────────────────

// upids remembers the PVE task each Cha Dash task started, so Verify (on the
// read instance) can check how it ended. Keyed by the Cha Dash task ID.
var upids sync.Map // task ID → upidRef

type upidRef struct{ node, upid string }

// write sends a POST or DELETE with a form body under /api2/json and
// returns the data: the UPID of the task it started.
func (in *instance) write(ctx context.Context, method, path string, form url.Values) (string, error) {
	var body io.Reader
	if len(form) > 0 {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, in.base+"/api2/json"+path, body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", in.auth)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
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
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(b, &env); err != nil {
		return "", fmt.Errorf("%s %s: the answer isn't PVE's JSON", method, path)
	}
	var upid string
	if err := json.Unmarshal(env.Data, &upid); err != nil || !strings.HasPrefix(upid, "UPID:") {
		return "", fmt.Errorf("%s %s: PVE didn't start a task", method, path)
	}
	return upid, nil
}

// writeFail turns a failed write into the sentence the task shows. priv is
// what the control token needs for it.
func writeFail(err error, what, priv string) error {
	if ae, ok := errors.AsType[*apiError](err); ok {
		switch ae.Code {
		case http.StatusUnauthorized:
			return action.Fail("auth", "Proxmox VE rejected the control token. Enter it again in the instance's settings.")
		case http.StatusForbidden:
			msg := "Proxmox VE refused the control token"
			if ae.Reason != "" {
				msg += " (" + ae.Reason + ")"
			}
			return action.Fail("forbidden", fmt.Sprintf("%s. It needs %s to %s; the recipe's control variant grants it.", msg, priv, what))
		}
		if ae.Reason != "" {
			return action.Fail("upstream", fmt.Sprintf("Proxmox VE couldn't %s: %s.", what, ae.Reason))
		}
	}
	_, _, msg := integration.Classify(err)
	return action.Fail("upstream", fmt.Sprintf("Proxmox VE couldn't %s: %s.", what, msg))
}

// taskStatus is GET /nodes/{node}/tasks/{upid}/status.
type taskStatus struct {
	Status     string `json:"status"` // running, stopped
	ExitStatus string `json:"exitstatus"`
}

func taskOK(exit string) bool { return exit == "OK" || strings.HasPrefix(exit, "WARNINGS") }

// progressRE finds a vzdump progress line: "INFO:  42% (4.2 GiB of 10.0 GiB) …".
var progressRE = regexp.MustCompile(`^INFO:\s+(\d{1,3})% \(`)

// follow waits for a PVE task to end, copying its log into run's log (and a
// vzdump's percentage into its progress), and fails with PVE's exit status.
func (in *instance) follow(ctx context.Context, run *action.TaskRun, node, upid, what string) error {
	next := 0
	for {
		var lines []logLine
		if err := in.get(ctx, p("nodes", node, "tasks", upid, "log"), url.Values{"start": {strconv.Itoa(next)}, "limit": {"500"}}, &lines); err == nil {
			for _, l := range lines {
				if l.N <= next {
					continue
				}
				next = l.N
				if l.T == "" || l.T == "no content" {
					continue
				}
				run.Logf("%s", l.T)
				if m := progressRE.FindStringSubmatch(l.T); m != nil {
					if pct, err := strconv.Atoi(m[1]); err == nil {
						run.Progress(float64(pct) / 100)
					}
				}
			}
		} else if ctx.Err() != nil {
			return ctx.Err()
		}
		var st taskStatus
		if err := in.get(ctx, p("nodes", node, "tasks", upid, "status"), nil, &st); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// A read that failed once isn't the task failing: try again.
		} else if st.Status == "stopped" {
			if taskOK(st.ExitStatus) {
				return nil
			}
			return action.Fail("upstream", fmt.Sprintf("Proxmox VE couldn't %s: %s.", what, cmp.Or(st.ExitStatus, "the task failed")))
		}
		if err := sleep(ctx, taskPoll); err != nil {
			return err
		}
	}
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// task starts a PVE task and follows it to its end.
func (in *instance) task(ctx context.Context, run *action.TaskRun, r action.Request, node, method, path string, form url.Values, what, priv string) error {
	upid, err := in.write(ctx, method, path, form)
	if err != nil {
		return writeFail(err, what, priv)
	}
	upids.Store(r.Task, upidRef{node: node, upid: upid})
	if err := in.follow(ctx, run, node, upid, what); err != nil {
		upids.Delete(r.Task) // no Verify follows a failure
		return err
	}
	return nil
}

// power runs status/<verb> on the guest.
func (in *instance) power(verb string, form url.Values) func(context.Context, action.Request, *action.TaskRun) error {
	return func(ctx context.Context, r action.Request, run *action.TaskRun) error {
		g := refOf(r)
		run.Step(fmt.Sprintf("Asking %s to %s %s", g.node, verb, r.Target.Name))
		return in.task(ctx, run, r, g.node, http.MethodPost, g.path("status", verb), form, verb+" "+r.Target.Name, "VM.PowerMgmt")
	}
}

func (in *instance) snapshotCreate(ctx context.Context, r action.Request, run *action.TaskRun) error {
	g := refOf(r)
	var a struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		VMState     bool   `json:"vmstate"`
	}
	_ = r.Decode(&a)
	form := url.Values{"snapname": {a.Name}}
	if a.Description != "" {
		form.Set("description", a.Description)
	}
	if a.VMState && g.typ == "qemu" {
		form.Set("vmstate", "1")
	}
	run.Step("Taking snapshot " + a.Name)
	return in.task(ctx, run, r, g.node, http.MethodPost, g.path("snapshot"), form, "snapshot "+r.Target.Name, "VM.Snapshot")
}

// snapshotTask runs a task on one snapshot: rollback (POST …/rollback) or
// delete (DELETE …/snapshot/{name}).
func (in *instance) snapshotTask(method, sub, verb string) func(context.Context, action.Request, *action.TaskRun) error {
	return func(ctx context.Context, r action.Request, run *action.TaskRun) error {
		g := refOf(r)
		name := snapName(r)
		segs := []string{"snapshot", name}
		priv := "VM.Snapshot"
		if sub != "" {
			segs = append(segs, sub)
			priv = "VM.Snapshot and VM.Snapshot.Rollback"
		}
		run.Step(fmt.Sprintf("Asking %s to %s snapshot %s", g.node, verb, name))
		return in.task(ctx, run, r, g.node, method, g.path(segs...), nil, verb+" snapshot "+name+" of "+r.Target.Name, priv)
	}
}

func (in *instance) vzdump(ctx context.Context, r action.Request, run *action.TaskRun) error {
	g := refOf(r)
	storage, err := in.backupStorage(ctx, g, r)
	if err != nil {
		return action.Fail("precondition", err.Error())
	}
	var a backupArgs
	_ = r.Decode(&a)
	form := url.Values{"vmid": {strconv.Itoa(g.vmid)}, "storage": {storage}, "mode": {cmp.Or(a.Mode, "snapshot")}}
	run.Step(fmt.Sprintf("Backing up %s to %s", r.Target.Name, storage))
	run.Progress(0)
	return in.task(ctx, run, r, g.node, http.MethodPost, p("nodes", g.node, "vzdump"), form, "back up "+r.Target.Name, "VM.Backup and Datastore.AllocateSpace")
}

// ── verify ──────────────────────────────────────────────────────────────

// waitState waits until the guest reports want.
func (in *instance) waitState(want string) func(context.Context, action.Request, *action.TaskRun) error {
	return func(ctx context.Context, r action.Request, run *action.TaskRun) error {
		defer upids.Delete(r.Task)
		run.Step("Waiting for " + r.Target.Name + " to report " + want)
		g := refOf(r)
		last := ""
		for {
			s, err := in.current(ctx, g)
			if err == nil {
				if s.Status == want {
					return nil
				}
				last = s.Status
			}
			if err := sleep(ctx, statePoll); err != nil {
				if last != "" {
					return action.Fail("verify_failed", fmt.Sprintf("%s still reports %s.", r.Target.Name, last))
				}
				return action.Fail("verify_failed", fmt.Sprintf("Cha Dash couldn't read %s's state to check it.", r.Target.Name))
			}
		}
	}
}

// snapshotListed checks the snapshot is listed (or gone).
func (in *instance) snapshotListed(want bool) func(context.Context, action.Request, *action.TaskRun) error {
	return func(ctx context.Context, r action.Request, run *action.TaskRun) error {
		defer upids.Delete(r.Task)
		names, err := in.snapshots(ctx, refOf(r))
		if err != nil {
			return readRefusalFail(err)
		}
		name := snapName(r)
		switch has := slices.Contains(names, name); {
		case want && !has:
			return action.Fail("verify_failed", fmt.Sprintf("Proxmox VE doesn't list snapshot %s of %s.", name, r.Target.Name))
		case !want && has:
			return action.Fail("verify_failed", fmt.Sprintf("Proxmox VE still lists snapshot %s of %s.", name, r.Target.Name))
		}
		return nil
	}
}

func readRefusalFail(err error) error {
	_, _, msg := integration.Classify(err)
	return action.Fail("verify_failed", "Cha Dash couldn't read the result back: "+msg+".")
}

// taskOK reads the status of the task Execute started, with the read
// token, and checks it ended OK.
func (in *instance) taskOK(ctx context.Context, r action.Request, run *action.TaskRun) error {
	v, ok := upids.LoadAndDelete(r.Task)
	if !ok {
		return action.Fail("verify_failed", "Cha Dash lost track of the Proxmox VE task.")
	}
	u := v.(upidRef)
	var st taskStatus
	if err := in.get(ctx, p("nodes", u.node, "tasks", u.upid, "status"), nil, &st); err != nil {
		return readRefusalFail(err)
	}
	if st.Status != "stopped" || !taskOK(st.ExitStatus) {
		return action.Fail("verify_failed", fmt.Sprintf("Proxmox VE reports the task as %s.", cmp.Or(st.ExitStatus, st.Status)))
	}
	return nil
}

var _ integration.Actor = (*instance)(nil)
