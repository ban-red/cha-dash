package proxmox

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/integration/integrationtest"
)

const (
	actFixtures   = "../../testdata/integrations/proxmox/actions"
	readTok       = "chadash@pve!dash"
	controlTok    = "chadash-control@pve!control"
	controlSecret = "00000000-fake-4000-8000-proxmoxctl00"
)

func init() {
	taskPoll, statePoll, verifyTimeout = time.Millisecond, time.Millisecond, 300*time.Millisecond
}

// fakePVE is a Proxmox VE that keeps guests' state from the synthetic
// fixtures and changes it when a task it started finishes (on the second
// read of its status), as the API documentation describes. Writes need the
// control token; reads take either.
type fakePVE struct {
	*httptest.Server
	writes integrationtest.Writes

	mu      sync.Mutex
	guests  map[string]*fakeGuest // "qemu/201"
	tasks   map[string]*fakeTask  // UPID → task
	n       int
	deny    map[string]string // write path suffix ("status/start") → the privilege PVE says is missing
	failing map[string]string // write path suffix → the task's exit status
	inert   bool              // tasks succeed but change nothing (a verify that fails)
	writeBy []string          // the token ID of each write
	readBy  map[string]bool   // token IDs that read
}

type fakeGuest struct {
	Node, Type, Name, Status, Lock string
	VMID                           int
	Template                       int
	snaps                          []string
}

type fakeTask struct {
	node   string
	polls  int
	exit   string
	log    []string
	effect func()
}

func newFakePVE(t *testing.T) *fakePVE {
	t.Helper()
	f := &fakePVE{guests: map[string]*fakeGuest{}, tasks: map[string]*fakeTask{}, deny: map[string]string{},
		failing: map[string]string{}, readBy: map[string]bool{}}
	var res struct {
		Data []struct {
			Type, Node, Name, Status, Lock string
			VMID, Template                 int
		} `json:"data"`
	}
	readJSON(t, "cluster-resources.json", &res)
	for _, r := range res.Data {
		if r.Type == "qemu" || r.Type == "lxc" {
			f.guests[r.Type+"/"+strconv.Itoa(r.VMID)] = &fakeGuest{Node: r.Node, Type: r.Type, Name: r.Name, Status: r.Status, Lock: r.Lock, VMID: r.VMID, Template: r.Template}
		}
	}
	f.guests["qemu/201"].snaps = []string{"before-driver"}
	f.Server = httptest.NewTLSServer(f.writes.Wrap(http.HandlerFunc(f.serve)))
	t.Cleanup(f.Close)
	return f
}

func readJSON(t *testing.T, name string, dst any) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(actFixtures, name))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, dst); err != nil {
		t.Fatal(err)
	}
}

func (f *fakePVE) reply(w http.ResponseWriter, status int, reason string, data any) {
	w.Header().Set("Content-Type", "application/json")
	if reason != "" {
		// PVE puts the error in the status line; Go's server can't, so the
		// body's message carries it (reason() reads either).
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(map[string]any{"data": nil, "message": reason})
		return
	}
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{"data": data})
}

func (f *fakePVE) serve(w http.ResponseWriter, r *http.Request) {
	tok := ""
	switch r.Header.Get("Authorization") {
	case "PVEAPIToken=" + readTok + "=" + fakeSecret:
		tok = readTok
	case "PVEAPIToken=" + controlTok + "=" + controlSecret:
		tok = controlTok
	default:
		f.reply(w, http.StatusUnauthorized, "authentication failure", nil)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api2/json")
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Method == http.MethodGet {
		f.readBy[tok] = true
		f.get(w, r, path)
		return
	}
	f.writeBy = append(f.writeBy, tok)
	if tok != controlTok {
		f.reply(w, http.StatusForbidden, "Permission check failed (/vms, VM.PowerMgmt)", nil)
		return
	}
	for suffix, priv := range f.deny {
		if strings.HasSuffix(path, suffix) {
			f.reply(w, http.StatusForbidden, "Permission check failed (/vms, "+priv+")", nil)
			return
		}
	}
	f.write(w, r, path)
}

func (f *fakePVE) get(w http.ResponseWriter, r *http.Request, path string) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	switch {
	case path == "/cluster/resources":
		if r.URL.Query().Get("type") != "vm" {
			f.reply(w, http.StatusBadRequest, "type: value 'x' does not have a value in the enumeration", nil)
			return
		}
		var out []map[string]any
		for _, k := range slices.Sorted(mapKeys(f.guests)) {
			g := f.guests[k]
			row := map[string]any{"id": k, "type": g.Type, "node": g.Node, "vmid": g.VMID, "name": g.Name, "status": g.Status, "template": g.Template}
			if g.Lock != "" {
				row["lock"] = g.Lock
			}
			out = append(out, row)
		}
		f.reply(w, 200, "", out)
	case path == "/cluster/backup":
		var jobs struct {
			Data any `json:"data"`
		}
		b, _ := os.ReadFile(filepath.Join(actFixtures, "backup-jobs.json"))
		json.Unmarshal(b, &jobs)
		f.reply(w, 200, "", jobs.Data)
	case len(parts) == 6 && parts[0] == "nodes" && parts[4] == "status" && parts[5] == "current":
		g := f.guests[parts[2]+"/"+parts[3]]
		if g == nil {
			f.reply(w, 500, "Configuration file does not exist", nil)
			return
		}
		f.reply(w, 200, "", map[string]any{"status": g.Status, "lock": g.Lock, "name": g.Name, "vmid": g.VMID})
	case len(parts) == 5 && parts[0] == "nodes" && parts[4] == "snapshot":
		g := f.guests[parts[2]+"/"+parts[3]]
		out := []map[string]any{}
		for _, s := range g.snaps {
			out = append(out, map[string]any{"name": s, "snaptime": 1759400000})
		}
		out = append(out, map[string]any{"name": "current", "description": "You are here!"})
		f.reply(w, 200, "", out)
	case len(parts) == 5 && parts[0] == "nodes" && parts[2] == "tasks":
		upid, _ := url.PathUnescape(parts[3])
		t := f.tasks[upid]
		if t == nil {
			f.reply(w, 404, "no such task", nil)
			return
		}
		switch parts[4] {
		case "status":
			t.polls++
			if t.polls < 2 {
				f.reply(w, 200, "", map[string]any{"status": "running", "upid": upid})
				return
			}
			if t.polls == 2 && t.effect != nil && t.exit == "OK" && !f.inert {
				t.effect()
			}
			f.reply(w, 200, "", map[string]any{"status": "stopped", "exitstatus": t.exit, "upid": upid})
		case "log":
			start, _ := strconv.Atoi(r.URL.Query().Get("start"))
			out := []map[string]any{}
			for i := start; i < len(t.log); i++ {
				out = append(out, map[string]any{"n": i + 1, "t": t.log[i]})
			}
			f.reply(w, 200, "", out)
		}
	default:
		f.reply(w, 501, "Method 'GET "+path+"' not implemented", nil)
	}
}

func mapKeys[V any](m map[string]V) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

func (f *fakePVE) write(w http.ResponseWriter, r *http.Request, path string) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	_ = r.ParseForm()
	if len(parts) < 3 || parts[0] != "nodes" {
		f.reply(w, 501, "not implemented", nil)
		return
	}
	node := parts[1]
	start := func(kind, id string, effect func(), log ...string) {
		f.n++
		upid := fmt.Sprintf("UPID:%s:%08X:0012F00D:6702A1B2:%s:%s:%s:", node, 0x10000+f.n, kind, id, controlTok)
		exit := "OK"
		for suffix, e := range f.failing {
			if strings.HasSuffix(path, suffix) {
				exit = e
			}
		}
		if exit != "OK" {
			log = append(log, exit)
		} else {
			log = append(log, "TASK OK")
		}
		f.tasks[upid] = &fakeTask{node: node, exit: exit, log: log, effect: effect}
		f.reply(w, 200, "", upid)
	}
	if parts[2] == "vzdump" {
		var lines struct {
			Data []struct{ T string } `json:"data"`
		}
		b, _ := os.ReadFile(filepath.Join(actFixtures, "vzdump-log.json"))
		json.Unmarshal(b, &lines)
		var log []string
		for _, l := range lines.Data[:len(lines.Data)-1] {
			log = append(log, l.T)
		}
		start("vzdump", r.PostForm.Get("vmid"), nil, log...)
		return
	}
	if len(parts) < 5 {
		f.reply(w, 501, "not implemented", nil)
		return
	}
	g := f.guests[parts[2]+"/"+parts[3]]
	if g == nil {
		f.reply(w, 500, "Configuration file does not exist", nil)
		return
	}
	id := strconv.Itoa(g.VMID)
	switch {
	case parts[4] == "status" && len(parts) == 6:
		next := map[string]string{"start": "running", "shutdown": "stopped", "stop": "stopped", "reboot": "running"}[parts[5]]
		start(map[string]string{"qemu": "qm", "lxc": "vz"}[g.Type]+parts[5], id, func() { g.Status = next }, "starting "+parts[5])
	case parts[4] == "snapshot" && len(parts) == 5 && r.Method == http.MethodPost:
		name := r.PostForm.Get("snapname")
		start(g.Type+"snapshot", id, func() { g.snaps = append(g.snaps, name) }, "snapshotting '"+name+"'")
	case parts[4] == "snapshot" && len(parts) == 7 && parts[6] == "rollback":
		start(g.Type+"rollback", id, nil, "rolling back to '"+parts[5]+"'")
	case parts[4] == "snapshot" && len(parts) == 6 && r.Method == http.MethodDelete:
		name := parts[5]
		start(g.Type+"delsnapshot", id, func() { g.snaps = slices.DeleteFunc(g.snaps, func(s string) bool { return s == name }) }, "deleting '"+name+"'")
	default:
		f.reply(w, 501, "not implemented", nil)
	}
}

func (f *fakePVE) guest(key string) fakeGuest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return *f.guests[key]
}

func (f *fakePVE) set(fn func(f *fakePVE)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

// actionsOn starts a harness and the instance's actions against f, with the
// control token unless readOnly.
func actionsOn(t *testing.T, f *fakePVE, readOnly bool, extra map[string]any) (*integrationtest.Harness, *integrationtest.Actions) {
	t.Helper()
	h := integrationtest.New(t)
	cfg := map[string]any{"url": f.URL, "tls": map[string]any{"mode": "insecure"}, "token_id": readTok}
	secrets := map[string]string{"token_secret": fakeSecret}
	if !readOnly {
		cfg["control_token_id"] = controlTok
		secrets["control_token_secret"] = controlSecret
	}
	maps.Copy(cfg, extra)
	return h, h.Actions(Integration{}, cfg, secrets)
}

func gid(local string) string { return "proxmox:i_test/" + local }

func wantTask(t *testing.T, task action.Task, state string) {
	t.Helper()
	if task.State != state {
		t.Fatalf("task %s = %s (error %+v), want %s; log %v", task.Action, task.State, task.Error, state, task.Log)
	}
}

func wantEvent(t *testing.T, acts *integrationtest.Actions, act, target, tier, result string) {
	t.Helper()
	ev := acts.LastEvent()
	if ev.Action != act || ev.Target != target || ev.Tier != tier || ev.Result != result {
		t.Fatalf("audit event = %+v, want %s on %s at %s: %s", ev, act, target, tier, result)
	}
}

func form(kv ...string) url.Values {
	v := url.Values{}
	for i := 0; i < len(kv); i += 2 {
		v.Add(kv[i], kv[i+1])
	}
	return v
}

func TestActionsRegistered(t *testing.T) {
	f := newFakePVE(t)
	_, acts := actionsOn(t, f, false, nil)
	want := []string{actBackup, actReboot, actShutdown, actSnapCreate, actSnapDelete, actSnapRollback, actStart, actStop}
	if got := acts.IDs(); !slices.Equal(got, want) {
		t.Fatalf("ids = %v, want %v", got, want)
	}
	tiers := map[string]action.Tier{actStart: action.T1, actShutdown: action.T1, actReboot: action.T2, actStop: action.T2,
		actSnapCreate: action.T1, actSnapRollback: action.T3, actSnapDelete: action.T3, actBackup: action.T1}
	for id, tier := range tiers {
		d, _ := acts.Registry.Get(id)
		if d.Tier != tier {
			t.Errorf("%s tier = %s, want %s", id, d.Tier, tier)
		}
	}
}

func TestStartAndShutdown(t *testing.T) {
	f := newFakePVE(t)
	_, acts := actionsOn(t, f, false, nil)

	p, err := acts.Plan(actStart, gid("lxc/202"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.Tier != action.T1 || p.Confirm != "undo" || p.Label != "Start build" || p.Undo == nil || p.Undo.Action != actShutdown ||
		len(p.Steps) != 2 || p.Steps[0].Label != "Ask io to start build" || p.Steps[1].Label != "Wait for it to report running" {
		t.Fatalf("plan = %+v", p)
	}
	task, err := acts.Approve(p)
	if err != nil {
		t.Fatal(err)
	}
	done := acts.Wait(task.ID)
	wantTask(t, done, action.StateSucceeded)
	if done.Undo == nil || done.Undo.Action != actShutdown || !slices.Contains(done.Log, "TASK OK") {
		t.Fatalf("task = %+v", done)
	}
	f.writes.Expect(t, integrationtest.Write{Method: "POST", Path: "/api2/json/nodes/io/lxc/202/status/start"})
	if g := f.guest("lxc/202"); g.Status != "running" {
		t.Fatalf("guest = %+v", g)
	}
	wantEvent(t, acts, actStart, gid("lxc/202"), "T1", "ok")

	// The undo: a clean shutdown with PVE's own timeout.
	done = acts.Run(actShutdown, gid("lxc/202"), nil)
	wantTask(t, done, action.StateSucceeded)
	f.writes.Expect(t, integrationtest.Write{Method: "POST", Path: "/api2/json/nodes/io/lxc/202/status/shutdown", Form: form("timeout", "180")})
	if g := f.guest("lxc/202"); g.Status != "stopped" {
		t.Fatalf("guest = %+v", g)
	}
	wantEvent(t, acts, actShutdown, gid("lxc/202"), "T1", "ok")
	// Every write used the control token; Verify read with the read one.
	if slices.ContainsFunc(f.writeBy, func(s string) bool { return s != controlTok }) || !f.readBy[readTok] {
		t.Fatalf("writes by %v, reads by %v", f.writeBy, f.readBy)
	}
}

func TestRebootAndHardStop(t *testing.T) {
	f := newFakePVE(t)
	h, acts := actionsOn(t, f, false, nil)
	// Plex runs on win-gaming: the blast radius comes from the graph.
	if _, err := h.Set.Graph.Update(func(tx *graph.Tx) error {
		for _, r := range []graph.Resource{{ID: graph.RID(gid("qemu/201")), Kind: "vm", Name: "win-gaming"}, {ID: "app:plex", Kind: "app", Name: "Plex"}} {
			if err := tx.Upsert(r); err != nil {
				return err
			}
		}
		return tx.Relate(graph.Relation{From: "app:plex", Type: "runs_on", To: graph.RID(gid("qemu/201"))})
	}); err != nil {
		t.Fatal(err)
	}
	p, err := acts.Plan(actReboot, gid("qemu/201"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.Tier != action.T2 || p.Confirm != "hold" || len(p.Impact) != 1 || p.Impact[0].Resource != "app:plex" || p.Undo != nil {
		t.Fatalf("plan = %+v", p)
	}
	task, err := acts.Approve(p)
	if err != nil {
		t.Fatal(err)
	}
	wantTask(t, acts.Wait(task.ID), action.StateSucceeded)
	f.writes.Expect(t, integrationtest.Write{Method: "POST", Path: "/api2/json/nodes/io/qemu/201/status/reboot", Form: form("timeout", "180")})
	wantEvent(t, acts, actReboot, gid("qemu/201"), "T2", "ok")

	wantTask(t, acts.Run(actStop, gid("qemu/201"), nil), action.StateSucceeded)
	f.writes.Expect(t, integrationtest.Write{Method: "POST", Path: "/api2/json/nodes/io/qemu/201/status/stop"})
	if g := f.guest("qemu/201"); g.Status != "stopped" {
		t.Fatalf("guest = %+v", g)
	}
	wantEvent(t, acts, actStop, gid("qemu/201"), "T2", "ok")
}

func TestPreconditions(t *testing.T) {
	f := newFakePVE(t)
	_, acts := actionsOn(t, f, false, nil)
	for _, c := range []struct {
		act, target string
		params      any
		want        string
	}{
		{actStart, "qemu/201", nil, "win-gaming is already running."},
		{actShutdown, "lxc/202", nil, "build is already stopped."},
		{actStop, "lxc/202", nil, "build is already stopped."},
		{actReboot, "lxc/202", nil, "build isn't running, so it can't reboot. Start it instead."},
		{actReboot, "qemu/203", nil, "media is locked (backup), so Cha Dash won't touch it until that's done."},
		{actBackup, "qemu/203", nil, "media is being backed up already."},
		{actBackup, "lxc/202", nil, "Choose where to back up build: no backup job covers it to take a storage from."},
		{actSnapCreate, "qemu/201", map[string]any{"name": "before-driver"}, "win-gaming already has a snapshot called before-driver."},
		{actSnapRollback, "qemu/201", map[string]any{"name": "nope"}, "win-gaming has no snapshot called nope."},
		{actSnapDelete, "lxc/202", map[string]any{"name": "nope"}, "build has no snapshot called nope."},
	} {
		ref := acts.Refused(c.act, gid(c.target), c.params)
		if ref.Code != "precondition" || ref.Status != 409 || ref.Message != c.want {
			t.Errorf("%s on %s: refusal = %+v, want %q", c.act, c.target, ref, c.want)
		}
	}
	// Unknown targets, templates and invalid params.
	if ref := acts.Refused(actStart, gid("qemu/9000"), nil); ref.Code != "not_found" {
		t.Errorf("template: %+v", ref)
	}
	if ref := acts.Refused(actStart, "proxmox:i_other/qemu/201", nil); ref.Code != "not_found" {
		t.Errorf("another instance's guest: %+v", ref)
	}
	if ref := acts.Refused(actSnapCreate, gid("qemu/201"), map[string]any{"name": "has space"}); ref.Code != "invalid" {
		t.Errorf("bad snapshot name: %+v", ref)
	}
	// Every refusal is on record.
	if ev := acts.LastEvent(); ev.Result != "refused" || ev.Action != actSnapCreate {
		t.Errorf("last event = %+v", ev)
	}
	if len(f.writes.All()) != 0 {
		t.Errorf("a refusal wrote: %+v", f.writes.All())
	}
}

func TestSnapshots(t *testing.T) {
	f := newFakePVE(t)
	_, acts := actionsOn(t, f, false, nil)
	params := map[string]any{"name": "pre-upgrade", "description": "Before 25H2", "vmstate": true}
	p, err := acts.Plan(actSnapCreate, gid("qemu/201"), params)
	if err != nil {
		t.Fatal(err)
	}
	if p.Tier != action.T1 || p.Label != "Snapshot win-gaming as pre-upgrade" || p.Undo == nil || p.Undo.Action != actSnapDelete || string(p.Undo.Params) != `{"name":"pre-upgrade"}` {
		t.Fatalf("plan = %+v", p)
	}
	task, err := acts.Approve(p)
	if err != nil {
		t.Fatal(err)
	}
	wantTask(t, acts.Wait(task.ID), action.StateSucceeded)
	f.writes.Expect(t, integrationtest.Write{Method: "POST", Path: "/api2/json/nodes/io/qemu/201/snapshot",
		Form: form("snapname", "pre-upgrade", "description", "Before 25H2", "vmstate", "1")})
	wantEvent(t, acts, actSnapCreate, gid("qemu/201"), "T1", "ok")

	// Rollback and delete are T3: typed name and step-up.
	p, err = acts.Plan(actSnapRollback, gid("qemu/201"), map[string]any{"name": "pre-upgrade"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Tier != action.T3 || p.Confirm != "type" || p.ConfirmText != "win-gaming" {
		t.Fatalf("rollback plan = %+v", p)
	}
	task, err = acts.Approve(p)
	if err != nil {
		t.Fatal(err)
	}
	wantTask(t, acts.Wait(task.ID), action.StateSucceeded)
	// It was running, so it's started again after the rollback.
	f.writes.Expect(t, integrationtest.Write{Method: "POST", Path: "/api2/json/nodes/io/qemu/201/snapshot/pre-upgrade/rollback", Form: form("start", "1")})
	wantEvent(t, acts, actSnapRollback, gid("qemu/201"), "T3", "ok")

	wantTask(t, acts.Run(actSnapDelete, gid("qemu/201"), map[string]any{"name": "pre-upgrade"}), action.StateSucceeded)
	f.writes.Expect(t, integrationtest.Write{Method: "DELETE", Path: "/api2/json/nodes/io/qemu/201/snapshot/pre-upgrade"})
	if g := f.guest("qemu/201"); !slices.Equal(g.snaps, []string{"before-driver"}) {
		t.Fatalf("snapshots = %v", g.snaps)
	}
	wantEvent(t, acts, actSnapDelete, gid("qemu/201"), "T3", "ok")
}

func TestBackupNow(t *testing.T) {
	f := newFakePVE(t)
	_, acts := actionsOn(t, f, false, nil)
	done := acts.Run(actBackup, gid("qemu/201"), nil)
	wantTask(t, done, action.StateSucceeded)
	// The storage comes from the job that covers it; progress from the log.
	f.writes.Expect(t, integrationtest.Write{Method: "POST", Path: "/api2/json/nodes/io/vzdump",
		Form: form("vmid", "201", "storage", "pbs-main", "mode", "snapshot")})
	if done.Progress == nil || *done.Progress != 1 || !slices.Contains(done.Log, "INFO: Finished Backup of VM 201 (00:01:53)") {
		t.Fatalf("task = %+v", done)
	}
	wantEvent(t, acts, actBackup, gid("qemu/201"), "T1", "ok")

	// An explicit storage and mode.
	wantTask(t, acts.Run(actBackup, gid("lxc/202"), map[string]any{"storage": "local", "mode": "stop"}), action.StateSucceeded)
	f.writes.Expect(t, integrationtest.Write{Method: "POST", Path: "/api2/json/nodes/io/vzdump",
		Form: form("vmid", "202", "storage", "local", "mode", "stop")})
}

func TestUnavailableWithoutControlToken(t *testing.T) {
	f := newFakePVE(t)
	h, acts := actionsOn(t, f, true, nil)
	ref := acts.Refused(actReboot, gid("qemu/201"), nil)
	if ref.Code != "unavailable" || ref.Status != 409 || ref.Recipe != h.ID ||
		ref.Message != "Add a control token to Test to let Cha Dash start, stop, snapshot and back up its guests. The recipe's control variant makes one." {
		t.Fatalf("refusal = %+v", ref)
	}
	if ev := acts.LastEvent(); ev.Result != "refused" || ev.Error == nil || ev.Error.Code != "unavailable" {
		t.Fatalf("event = %+v", ev)
	}
	if av := acts.Bridge.Availability(actReboot, gid("qemu/201")); !av.Known || av.PlanReady || av.Recipe != h.ID {
		t.Fatalf("availability = %+v", av)
	}
	if len(f.writes.All()) != 0 {
		t.Fatalf("wrote without a control token: %+v", f.writes.All())
	}
}

func TestSelfAwareT4(t *testing.T) {
	f := newFakePVE(t)
	h, acts := actionsOn(t, f, false, map[string]any{"hub_vmid": 105})
	// The hub_vmid hint: rebooting or stopping the hub's guest is T4, and
	// asks for a fresh step-up; a snapshot of it isn't.
	for _, act := range []string{actReboot, actStop, actShutdown} {
		p, err := acts.Plan(act, gid("lxc/105"), nil)
		if err != nil {
			t.Fatal(err)
		}
		if p.Tier != action.T4 || p.StepUp != "fresh" || p.Confirm != "type" || p.ConfirmText != "chadash" {
			t.Errorf("%s on the hub's guest: plan = %+v", act, p)
		}
	}
	if p, err := acts.Plan(actSnapCreate, gid("lxc/105"), map[string]any{"name": "safe"}); err != nil || p.Tier != action.T1 {
		t.Errorf("snapshot of the hub's guest: %+v, %v", p, err)
	}

	// The router, from the graph: OPNsense runs on qemu/100 (the
	// correlator's runs_on).
	if p, _ := acts.Plan(actReboot, gid("qemu/100"), nil); p == nil || p.Tier != action.T2 {
		t.Fatalf("before the graph knows: %+v", p)
	}
	if _, err := h.Set.Graph.Update(func(tx *graph.Tx) error {
		for _, r := range []graph.Resource{{ID: graph.RID(gid("qemu/100")), Kind: "vm", Name: "opnsense"}, {ID: "net:opnsense", Kind: "router", Name: "OPNsense"},
			{ID: graph.RID(gid("qemu/201")), Kind: "vm", Name: "win-gaming"}, {ID: integration.HubResource, Kind: "app", Name: "Cha Dash"}} {
			if err := tx.Upsert(r); err != nil {
				return err
			}
		}
		if err := tx.Relate(graph.Relation{From: "net:opnsense", Type: "runs_on", To: graph.RID(gid("qemu/100"))}); err != nil {
			return err
		}
		return tx.Relate(graph.Relation{From: integration.HubResource, Type: "runs_on", To: graph.RID(gid("qemu/201"))})
	}); err != nil {
		t.Fatal(err)
	}
	p, err := acts.Plan(actReboot, gid("qemu/100"), nil)
	if err != nil || p.Tier != action.T4 || p.StepUp != "fresh" {
		t.Fatalf("rebooting the router: %+v, %v", p, err)
	}
	if !slices.ContainsFunc(p.Impact, func(i action.Impact) bool { return i.Resource == "net:opnsense" }) {
		t.Errorf("the router isn't in the blast radius: %+v", p.Impact)
	}
	// The hub's guest as the graph knows it (no hint for 201).
	if p, err := acts.Plan(actStop, gid("qemu/201"), nil); err != nil || p.Tier != action.T4 {
		t.Errorf("stopping the guest the graph says runs the hub: %+v, %v", p, err)
	}
	// A T4 runs like any other once approved (the policy here allows T4).
	wantTask(t, acts.Run(actReboot, gid("qemu/100"), nil), action.StateSucceeded)
	wantEvent(t, acts, actReboot, gid("qemu/100"), "T4", "ok")
}

func TestActionFailures(t *testing.T) {
	f := newFakePVE(t)
	_, acts := actionsOn(t, f, false, nil)

	// PVE's task fails: the task fails with its exit status.
	f.set(func(f *fakePVE) { f.failing["status/start"] = "start failed: QEMU exited with code 1" })
	done := acts.Run(actStart, gid("lxc/202"), nil)
	wantTask(t, done, action.StateFailed)
	if done.Error.Code != "upstream" || done.Error.Message != "Proxmox VE couldn't start build: start failed: QEMU exited with code 1." {
		t.Fatalf("error = %+v", done.Error)
	}
	wantEvent(t, acts, actStart, gid("lxc/202"), "T1", "failed")
	f.writes.Take()

	// The control token lacks a privilege.
	f.set(func(f *fakePVE) { f.deny["/vzdump"] = "VM.Backup" })
	done = acts.Run(actBackup, gid("qemu/201"), nil)
	wantTask(t, done, action.StateFailed)
	if done.Error.Code != "forbidden" || !strings.Contains(done.Error.Message, "VM.Backup and Datastore.AllocateSpace") || !strings.Contains(done.Error.Message, "control variant") {
		t.Fatalf("error = %+v", done.Error)
	}
	f.writes.Take()

	// PVE says OK but the guest never gets there: verify fails the task.
	f.set(func(f *fakePVE) { f.inert, f.failing = true, map[string]string{} })
	done = acts.Run(actStart, gid("lxc/202"), nil)
	wantTask(t, done, action.StateFailed)
	if done.Error.Code != "verify_failed" || done.Error.Message != "build still reports stopped." {
		t.Fatalf("error = %+v", done.Error)
	}
	wantEvent(t, acts, actStart, gid("lxc/202"), "T1", "failed")

	// No secret anywhere in the record.
	b, _ := json.Marshal(acts.Events())
	for _, s := range []string{fakeSecret, controlSecret} {
		if strings.Contains(string(b), s) || strings.Contains(fmt.Sprint(done.Log), s) {
			t.Fatalf("a secret reached the audit log or a task log")
		}
	}
}

// TestPlanChanged: the guest changed between plan and approve.
func TestPlanChanged(t *testing.T) {
	f := newFakePVE(t)
	_, acts := actionsOn(t, f, false, nil)
	p, err := acts.Plan(actStart, gid("lxc/202"), nil)
	if err != nil {
		t.Fatal(err)
	}
	f.set(func(f *fakePVE) { f.guests["lxc/202"].Status = "running" })
	if _, err := acts.Approve(p); err == nil || !strings.Contains(err.Error(), "plan_changed") {
		t.Fatalf("approve = %v", err)
	}
}

// TestHubGuestFromAddresses: Sync relates the hub to the guest whose MAC is
// one of this machine's.
func TestHubGuestFromAddresses(t *testing.T) {
	prev := localAddrs
	t.Cleanup(func() { localAddrs = prev })
	localAddrs = func() ([]string, []string) { return []string{"192.0.2.44"}, []string{"BC:24:11:00:00:01"} }
	_, h, in := connect(t, "pluto")
	if _, err := h.Set.Graph.Update(func(tx *graph.Tx) error {
		return tx.Upsert(graph.Resource{ID: integration.HubResource, Kind: "app", Name: "Cha Dash"})
	}); err != nil {
		t.Fatal(err)
	}
	syncOK(t, in, integrationtest.Watched)
	if !hasRel(h, integration.HubResource, "runs_on", pid("lxc/100")) {
		t.Fatalf("no chadash:hub runs_on lxc/100: %+v", h.Graph().Relations)
	}
	// Nothing of this machine's in the fleet: no relation.
	localAddrs = func() ([]string, []string) { return []string{"192.0.2.44"}, []string{"02:00:00:00:00:01"} }
	in.at = map[string]time.Time{}
	syncOK(t, in, integrationtest.Watched)
	if hasRel(h, integration.HubResource, "runs_on", pid("lxc/100")) {
		t.Fatal("the relation stayed")
	}
}

// TestGuestErrorOffersHardStop: the internal-error incident's alternative is
// the engine's hard stop.
func TestGuestErrorOffersHardStop(t *testing.T) {
	_, h, in := connect(t, "jupiter-nas")
	syncOK(t, in, integrationtest.Watched)
	inc, ok := h.Incident("guest.101")
	if !ok || len(inc.Alternatives) != 1 || inc.Alternatives[0].ID != actStop || inc.Alternatives[0].Tier != "T2" || inc.Alternatives[0].Target != pid("qemu/101") {
		t.Fatalf("incident = %+v", inc)
	}
}

func TestControlRecipe(t *testing.T) {
	r, ok := integration.Default.Recipe(ID)
	if !ok || r.Control == nil {
		t.Fatal("no control recipe")
	}
	c := r.Control
	if c.Access != integration.RecipeScoped || c.Fills["control_token_id"] != "{{user}}!{{token}}" {
		t.Fatalf("control recipe = %+v", c)
	}
	for _, priv := range []string{"VM.PowerMgmt", "VM.Snapshot", "VM.Snapshot.Rollback", "VM.Backup", "Datastore.AllocateSpace"} {
		if !slices.Contains(c.Grants, priv) {
			t.Errorf("the control recipe doesn't grant %s", priv)
		}
	}
	code := ""
	for _, s := range c.Steps {
		for _, b := range s.Blocks {
			code += b.Code + "\n"
		}
	}
	if !strings.Contains(code, "pveum role add {{role}}") || !strings.Contains(code, "VM.Snapshot.Rollback") || !strings.Contains(code, "--privsep 1") {
		t.Fatalf("commands = %s", code)
	}
}
