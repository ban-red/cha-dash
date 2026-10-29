package unraid

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"dash.cha.sh/integrations/internal/act/acttest"
	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/integration/integrationtest"
)

// The fixture's VMs, by libvirt UUID.
const (
	vmUbuntu  = "85d7761b-c3d9-9180-6755-741350d64937" // jupiter_ubuntu, RUNNING
	vmWindows = "0d255241-7175-d003-6916-caff9055e309" // Windows 10, SHUTOFF
)

// lab is the fake server's mutable state for actions: container and VM
// states that mutations change, and the mutations received.
type lab struct {
	t   *testing.T
	srv *server

	mu         sync.Mutex
	containers map[string]string // name → state, over the fixture's
	vms        map[string]string // UUID → state
	mutations  []string          // "<op> <id>"
	// lag is how many reads a change takes to show (Until's polling).
	lag     int
	pending map[string]int
	// stuck: mutations succeed, but nothing changes.
	stuck bool
}

// perms is me's shape for a key with these grants.
type perm struct {
	Resource string   `json:"resource"`
	Actions  []string `json:"actions"`
}

func meBody(t *testing.T, roles []string, perms ...perm) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{"data": map[string]any{"me": map[string]any{"name": "Cha Dash", "roles": roles, "permissions": perms}}})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// controlKey is what the control recipe asks for.
var controlKey = []perm{
	{"INFO", []string{"READ_ANY"}}, {"ARRAY", []string{"READ_ANY"}}, {"SHARE", []string{"READ_ANY"}}, {"ME", []string{"READ_ANY"}},
	{"DOCKER", []string{"READ_ANY", "UPDATE_ANY"}}, {"VMS", []string{"READ_ANY", "UPDATE_ANY"}},
}

func newLab(t *testing.T) *lab {
	l := &lab{t: t, srv: newServer(t), containers: map[string]string{}, vms: map[string]string{}, pending: map[string]int{}}
	l.srv.set("ChadashMe", meBody(t, nil, controlKey...))
	l.srv.handlers["ChadashDocker"] = l.docker
	l.srv.handlers["ChadashVMs"] = l.vmList
	for _, op := range []string{"ChadashContainerStart", "ChadashContainerStop", "ChadashContainerUpdate", "ChadashVMStart", "ChadashVMStop",
		"ChadashContainerRestart", "ChadashContainerPause", "ChadashContainerUnpause", "ChadashContainerRemove",
		"ChadashVMPause", "ChadashVMResume", "ChadashVMReboot", "ChadashVMForceStop"} {
		l.srv.handlers[op] = l.mutation(op)
	}
	return l
}

// listed answers a fixture's list (data.<root>.<list>) with states from
// the lab, keyed by key(item).
func (l *lab) listed(file, root, list, prefix string, states map[string]string, key func(map[string]any) string) []byte {
	var doc map[string]map[string]map[string]any
	if err := json.Unmarshal(fixture(l.t, file), &doc); err != nil {
		l.t.Error(err)
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	items, _ := doc["data"][root][list].([]any)
	kept := items[:0]
	for _, it := range items {
		m := it.(map[string]any)
		k := key(m)
		if st, ok := states[k]; ok && l.shown(prefix+k) {
			if st == "GONE" {
				continue
			}
			m["state"] = st
		}
		kept = append(kept, it)
	}
	doc["data"][root][list] = kept
	b, _ := json.Marshal(doc)
	return b
}

// docker answers the fixture with the lab's container states.
func (l *lab) docker(map[string]any) []byte {
	return l.listed("docker.json", "docker", "containers", "c/", l.containers, func(m map[string]any) string {
		return strings.TrimPrefix(m["names"].([]any)[0].(string), "/")
	})
}

func (l *lab) vmList(map[string]any) []byte {
	return l.listed("vms.json", "vms", "domains", "v/", l.vms, func(m map[string]any) string { return localID(m["id"].(string)) })
}

// shown reports whether a change to key has shown yet, counting down its
// lag. Called with l.mu held.
func (l *lab) shown(key string) bool {
	if n := l.pending[key]; n > 0 {
		l.pending[key] = n - 1
		return false
	}
	return true
}

// mutation applies a mutation to the lab's state, looking the target up by
// its PrefixedID in the fixtures.
func (l *lab) mutation(op string) func(map[string]any) []byte {
	return func(vars map[string]any) []byte {
		id, _ := vars["id"].(string)
		l.mu.Lock()
		defer l.mu.Unlock()
		l.mutations = append(l.mutations, op+" "+id)
		if strings.HasPrefix(op, "ChadashVM") {
			st := map[string]string{"ChadashVMStart": "RUNNING", "ChadashVMStop": "SHUTOFF", "ChadashVMPause": "PAUSED", "ChadashVMResume": "RUNNING",
				"ChadashVMReboot": "RUNNING", "ChadashVMForceStop": "SHUTOFF"}[op]
			if !l.stuck {
				l.vms[localID(id)] = st
				l.pending["v/"+localID(id)] = l.lag
			}
			field := strings.ToLower(strings.TrimPrefix(op, "ChadashVM"))
			if field == "forcestop" {
				field = "forceStop"
			}
			return []byte(`{"data":{"vm":{"` + field + `":true}}}`)
		}
		var doc struct {
			Data struct {
				Docker struct {
					Containers []container `json:"containers"`
				} `json:"docker"`
			} `json:"data"`
		}
		_ = json.Unmarshal(fixture(l.t, "docker.json"), &doc)
		for _, c := range doc.Data.Docker.Containers {
			if c.ID != id {
				continue
			}
			name := strings.TrimPrefix(c.Names[0], "/")
			st := map[string]string{"ChadashContainerStart": "RUNNING", "ChadashContainerStop": "EXITED", "ChadashContainerUpdate": "RUNNING",
				"ChadashContainerRestart": "RUNNING", "ChadashContainerPause": "PAUSED", "ChadashContainerUnpause": "RUNNING", "ChadashContainerRemove": "GONE"}[op]
			if !l.stuck {
				l.containers[name] = st
				l.pending["c/"+name] = l.lag
			}
			field := map[string]string{"ChadashContainerStart": "start", "ChadashContainerStop": "stop", "ChadashContainerUpdate": "updateContainer",
				"ChadashContainerRestart": "restart", "ChadashContainerPause": "pause", "ChadashContainerUnpause": "unpause", "ChadashContainerRemove": "removeContainer"}[op]
			if op == "ChadashContainerRemove" {
				return []byte(`{"data":{"docker":{"removeContainer":true}}}`)
			}
			return []byte(`{"data":{"docker":{"` + field + `":{"id":"` + id + `","state":"` + st + `"}}}}`)
		}
		return []byte(`{"data":null,"errors":[{"message":"Container not found","extensions":{"code":"INTERNAL_SERVER_ERROR"}}]}`)
	}
}

func (l *lab) took() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	m := l.mutations
	l.mutations = nil
	return m
}

func connectMode(t *testing.T, h *integrationtest.Harness, srv *server, mode string) *instance {
	t.Helper()
	cfg := map[string]any{"url": srv.URL}
	if mode != "" {
		cfg["mode"] = mode
	}
	return h.Connect(Integration{}, cfg, map[string]string{"api_key": apiKey}).(*instance)
}

// controlled is a synced controllable instance with its actions in an
// engine over the harness's graph.
func controlled(t *testing.T, l *lab) (*instance, *acttest.Env) {
	t.Helper()
	h := integrationtest.New(t)
	in := connectMode(t, h, l.srv, ModeControl)
	if err := in.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	return in, acttest.New(t, h.Set.Graph, integration.ActionDefs(in)...)
}

func TestModes(t *testing.T) {
	ok := []map[string]any{{"url": "http://10.0.11.120", "mode": "read_only"}, {"url": "http://10.0.11.120", "mode": "control"}, {"url": "http://10.0.11.120"}}
	for _, cfg := range ok {
		if errs := integrationtest.Validate(Integration{}, cfg, map[string]string{"api_key": "k"}); len(errs) != 0 {
			t.Errorf("%v: %v", cfg, errs)
		}
	}
	if errs := integrationtest.Validate(Integration{}, map[string]any{"url": "http://10.0.11.120", "mode": "admin"}, map[string]string{"api_key": "k"}); len(errs) == 0 || errs[0].Path != "/mode" {
		t.Fatalf("mode admin: %v", errs)
	}
	// The form shows the choice with titles, read-only first and default.
	_, m, _ := integration.Default.Get(ID)
	b, _ := json.Marshal(m.ConfigSchema.Property("mode"))
	if !strings.Contains(string(b), `"oneOf":[{"const":"read_only","title":"Read-only"},{"const":"control","title":"Control: start, stop and update containers and VMs"}],"default":"read_only"`) {
		t.Fatalf("mode = %s", b)
	}

	// Read-only, said or by default: no actions at all.
	srv := newServer(t)
	for _, mode := range []string{"", ModeReadOnly} {
		in := connectMode(t, integrationtest.New(t), srv, mode)
		if defs := integration.ActionDefs(in); len(defs) != 0 {
			t.Fatalf("mode %q has actions: %v", mode, defs)
		}
	}
	in := connectMode(t, integrationtest.New(t), srv, ModeControl)
	var ids []string
	for _, d := range integration.ActionDefs(in) {
		ids = append(ids, d.ID+"="+d.Tier.String())
	}
	if want := []string{"unraid.container.start=T1", "unraid.container.stop=T1", "unraid.container.update=T2", "unraid.container.restart=T2",
		"unraid.container.pause=T1", "unraid.container.unpause=T1", "unraid.container.remove=T3", "unraid.vm.start=T1", "unraid.vm.stop=T1",
		"unraid.vm.pause=T1", "unraid.vm.resume=T1", "unraid.vm.reboot=T2", "unraid.vm.force_stop=T3"}; !slices.Equal(ids, want) {
		t.Fatalf("actions = %v", ids)
	}
}

func TestContainerStartStop(t *testing.T) {
	l := newLab(t)
	l.lag = 2 // the change shows on the third read: Verify polls
	_, env := controlled(t, l)
	target := "unraid:i_test/container/Grafana"

	task, plan := env.Succeeded(ActContainerStart, target, nil)
	if plan.Label != "Start Grafana" || plan.Tier != action.T1 || plan.Confirm != "undo" ||
		plan.Undo == nil || plan.Undo.Action != ActContainerStop || plan.Undo.Label != "Stop Grafana" {
		t.Fatalf("plan = %+v", plan)
	}
	if len(plan.Steps) != 2 || plan.Steps[0].Label != "Ask Unraid to start Grafana" || plan.Steps[1].Label != "Wait for it to report running" {
		t.Fatalf("steps = %+v", plan.Steps)
	}
	if m := l.took(); len(m) != 1 || !strings.HasPrefix(m[0], "ChadashContainerStart d7d4935f") {
		t.Fatalf("mutations = %v (the PrefixedID, not the name)", m)
	}
	if task.Undo == nil || task.Undo.Action != ActContainerStop {
		t.Fatalf("task = %+v", task)
	}

	// Now running: starting again is refused with a sentence, and nothing is sent.
	if r := env.Refusal(ActContainerStart, target, nil); r.Code != "precondition" || r.Message != "Grafana is already running." {
		t.Fatalf("refusal = %+v", r)
	}
	// The undo.
	env.Succeeded(ActContainerStop, target, nil)
	if r := env.Refusal(ActContainerStop, target, nil); r.Code != "precondition" || r.Message != "Grafana is already stopped." {
		t.Fatalf("refusal = %+v", r)
	}
	if m := l.took(); len(m) != 1 || !strings.HasPrefix(m[0], "ChadashContainerStop ") {
		t.Fatalf("mutations = %v", m)
	}
	// Audited with the target and no params.
	for _, e := range env.Events() {
		if !strings.HasPrefix(e.Action, "unraid.container.") || e.Target != target || (string(e.Params) != "{}" && e.Params != nil) {
			t.Fatalf("event = %+v", e)
		}
	}

	// Unknown names, other instances' targets and other kinds aren't found.
	for _, tg := range []string{"unraid:i_test/container/nope", "unraid:i_other/container/Grafana", "unraid:i_test/vm/" + vmWindows, "docker:i_test/container/Grafana"} {
		if r := env.Refusal(ActContainerStart, tg, nil); r.Code != "not_found" {
			t.Errorf("%s: %+v", tg, r)
		}
	}
}

func TestContainerUpdate(t *testing.T) {
	l := newLab(t)
	_, env := controlled(t, l)
	_, plan := env.Succeeded(ActContainerUpdate, "unraid:i_test/container/tautulli", nil)
	if plan.Tier != action.T2 || plan.Confirm != "hold" || plan.Undo != nil {
		t.Fatalf("plan = %+v", plan)
	}
	if m := l.took(); len(m) != 1 || !strings.HasPrefix(m[0], "ChadashContainerUpdate ") {
		t.Fatalf("mutations = %v", m)
	}
	// Compose Manager's containers aren't Unraid's to update.
	if r := env.Refusal(ActContainerUpdate, "unraid:i_test/container/mealie", nil); r.Code != "precondition" || !strings.Contains(r.Message, "Compose Manager") {
		t.Fatalf("refusal = %+v", r)
	}
}

func TestVMStartStop(t *testing.T) {
	l := newLab(t)
	l.lag = 1
	_, env := controlled(t, l)
	win := "unraid:i_test/vm/" + vmWindows
	_, plan := env.Succeeded(ActVMStart, win, nil)
	if plan.Label != "Start Windows 10" || plan.Undo == nil || plan.Undo.Action != ActVMStop || plan.Undo.Label != "Shut down Windows 10" {
		t.Fatalf("plan = %+v", plan)
	}
	if r := env.Refusal(ActVMStart, win, nil); r.Code != "precondition" || r.Message != "Windows 10 is already running." {
		t.Fatalf("refusal = %+v", r)
	}
	env.Succeeded(ActVMStop, win, nil)
	if m := l.took(); len(m) != 2 || !strings.HasPrefix(m[0], "ChadashVMStart d7d4935f") || !strings.HasSuffix(m[0], vmWindows) || !strings.HasPrefix(m[1], "ChadashVMStop ") {
		t.Fatalf("mutations = %v", m)
	}
	// A VM that's shutting down.
	l.mu.Lock()
	l.vms[vmUbuntu] = "SHUTDOWN"
	l.mu.Unlock()
	if r := env.Refusal(ActVMStop, "unraid:i_test/vm/"+vmUbuntu, nil); r.Code != "precondition" || r.Message != "jupiter_ubuntu is already shutting down." {
		t.Fatalf("refusal = %+v", r)
	}
}

func TestContainerPauseRestartRemove(t *testing.T) {
	l := newLab(t)
	_, env := controlled(t, l)
	tautulli := "unraid:i_test/container/tautulli" // running
	grafana := "unraid:i_test/container/Grafana"   // stopped

	_, plan := env.Succeeded(ActContainerPause, tautulli, nil)
	if plan.Tier != action.T1 || plan.Undo == nil || plan.Undo.Action != ActContainerUnpause || plan.Undo.Label != "Unpause tautulli" {
		t.Fatalf("plan = %+v", plan)
	}
	if r := env.Refusal(ActContainerPause, tautulli, nil); r.Code != "precondition" || r.Message != "tautulli is already paused." {
		t.Fatalf("refusal = %+v", r)
	}
	env.Succeeded(ActContainerUnpause, tautulli, nil)
	if r := env.Refusal(ActContainerUnpause, tautulli, nil); r.Code != "precondition" || r.Message != "tautulli isn't paused." {
		t.Fatalf("refusal = %+v", r)
	}
	if r := env.Refusal(ActContainerPause, grafana, nil); r.Code != "precondition" || !strings.Contains(r.Message, "isn't running") {
		t.Fatalf("refusal = %+v", r)
	}
	_, plan = env.Succeeded(ActContainerRestart, tautulli, nil)
	if plan.Tier != action.T2 || plan.Undo != nil {
		t.Fatalf("plan = %+v", plan)
	}
	if m := l.took(); len(m) != 3 || !strings.HasPrefix(m[0], "ChadashContainerPause ") || !strings.HasPrefix(m[1], "ChadashContainerUnpause ") || !strings.HasPrefix(m[2], "ChadashContainerRestart ") {
		t.Fatalf("mutations = %v", m)
	}
}

// Remove needs Docker: Delete, which the control key doesn't have: it's the
// "full control" consent link's. It also needs the container stopped.
func TestContainerRemove(t *testing.T) {
	l := newLab(t)
	in, env := controlled(t, l)
	grafana := "unraid:i_test/container/Grafana"

	r := env.Refusal(ActContainerRemove, grafana, nil)
	if r.Code != "unavailable" || !strings.Contains(r.Message, "Docker: Delete (docker:delete_any)") || !strings.Contains(r.Message, "remove Docker containers") {
		t.Fatalf("refusal = %+v", r)
	}
	if m := l.took(); len(m) != 0 {
		t.Fatalf("sent %v", m)
	}

	l.srv.set("ChadashMe", meBody(t, nil, append(append([]perm(nil), controlKey[:4]...),
		perm{"DOCKER", []string{"READ_ANY", "UPDATE_ANY", "DELETE_ANY"}}, perm{"VMS", []string{"READ_ANY", "UPDATE_ANY"}})...))
	in.actMu.Lock()
	in.meAt = time.Time{}
	in.actMu.Unlock()

	if r := env.Refusal(ActContainerRemove, "unraid:i_test/container/tautulli", nil); r.Code != "precondition" || !strings.Contains(r.Message, "Stop it first") {
		t.Fatalf("refusal = %+v", r)
	}
	_, plan := env.Succeeded(ActContainerRemove, grafana, nil)
	if plan.Tier != action.T3 || plan.Confirm != "type" || plan.ConfirmText != "Grafana" || plan.Undo != nil {
		t.Fatalf("plan = %+v", plan)
	}
	if m := l.took(); len(m) != 1 || !strings.HasPrefix(m[0], "ChadashContainerRemove ") {
		t.Fatalf("mutations = %v", m)
	}
	// Gone from the list, and no mutation asked for the image to go.
	if r := env.Refusal(ActContainerRemove, grafana, nil); r.Code != "not_found" {
		t.Fatalf("refusal = %+v", r)
	}
	if strings.Contains(mContainerRemove, "withImage") {
		t.Fatal("remove must keep the image")
	}
}

func TestVMPauseResumeRebootForceStop(t *testing.T) {
	l := newLab(t)
	_, env := controlled(t, l)
	ubuntu, win := "unraid:i_test/vm/"+vmUbuntu, "unraid:i_test/vm/"+vmWindows // running, stopped

	_, plan := env.Succeeded(ActVMPause, ubuntu, nil)
	if plan.Undo == nil || plan.Undo.Action != ActVMResume || plan.Undo.Label != "Resume jupiter_ubuntu" {
		t.Fatalf("plan = %+v", plan)
	}
	if r := env.Refusal(ActVMPause, ubuntu, nil); r.Code != "precondition" || r.Message != "jupiter_ubuntu is already paused." {
		t.Fatalf("refusal = %+v", r)
	}
	if r := env.Refusal(ActVMReboot, ubuntu, nil); r.Code != "precondition" || !strings.Contains(r.Message, "isn't running") {
		t.Fatalf("refusal = %+v", r)
	}
	env.Succeeded(ActVMResume, ubuntu, nil)
	if r := env.Refusal(ActVMResume, ubuntu, nil); r.Code != "precondition" || r.Message != "jupiter_ubuntu isn't paused." {
		t.Fatalf("refusal = %+v", r)
	}
	_, plan = env.Succeeded(ActVMReboot, ubuntu, nil)
	if plan.Tier != action.T2 {
		t.Fatalf("plan = %+v", plan)
	}
	if r := env.Refusal(ActVMForceStop, win, nil); r.Code != "precondition" || r.Message != "Windows 10 is already shut down." {
		t.Fatalf("refusal = %+v", r)
	}
	_, plan = env.Succeeded(ActVMForceStop, ubuntu, nil)
	if plan.Tier != action.T3 || plan.Confirm != "type" || plan.ConfirmText != "jupiter_ubuntu" {
		t.Fatalf("plan = %+v", plan)
	}
	if m := l.took(); len(m) != 4 || !strings.HasPrefix(m[3], "ChadashVMForceStop ") {
		t.Fatalf("mutations = %v", m)
	}
}

// A controllable instance whose key lacks a write scope shows the actions,
// and each refuses as unavailable, naming the scope and how to add it.
func TestMissingWriteScope(t *testing.T) {
	l := newLab(t)
	// A read-only key (the v0.1 recipe, plus VMs).
	l.srv.set("ChadashMe", meBody(t, nil, perm{"INFO", []string{"READ_ANY"}}, perm{"ARRAY", []string{"READ_ANY"}},
		perm{"DOCKER", []string{"READ_ANY"}}, perm{"VMS", []string{"READ_ANY"}}, perm{"SHARE", []string{"READ_ANY"}}, perm{"ME", []string{"READ_ANY"}}))
	in, env := controlled(t, l)
	if n := len(integration.ActionDefs(in)); n != 13 {
		t.Fatalf("%d actions; a controllable instance shows them all", n)
	}
	r := env.Refusal(ActContainerStop, "unraid:i_test/container/tautulli", nil)
	if r.Code != "unavailable" || r.Status != 409 || r.Recipe != "i_test" ||
		!strings.Contains(r.Message, "Docker: Update (docker:update_any)") || !strings.Contains(r.Message, "consent link") ||
		!strings.Contains(r.Message, "Settings → Management Access → API Keys") {
		t.Fatalf("refusal = %+v", r)
	}
	if r := env.Refusal(ActVMStart, "unraid:i_test/vm/"+vmWindows, nil); r.Code != "unavailable" || !strings.Contains(r.Message, "VMs: Update (vms:update_any)") {
		t.Fatalf("refusal = %+v", r)
	}
	if m := l.took(); len(m) != 0 {
		t.Fatalf("sent %v", m)
	}

	// Docker only: VMs stay unavailable, containers work.
	l.srv.set("ChadashMe", meBody(t, nil, perm{"DOCKER", []string{"READ_ANY", "UPDATE_ANY"}}, perm{"VMS", []string{"READ_ANY"}}))
	in.actMu.Lock()
	in.meAt = time.Time{}
	in.actMu.Unlock()
	env.Succeeded(ActContainerStop, "unraid:i_test/container/tautulli", nil)
	if r := env.Refusal(ActVMStart, "unraid:i_test/vm/"+vmWindows, nil); r.Code != "unavailable" {
		t.Fatalf("refusal = %+v", r)
	}

	// The admin role can do everything.
	if !(meData{Roles: []string{"ADMIN"}}).can("VMS", "UPDATE") || (meData{Roles: []string{"VIEWER"}}).can("VMS", "UPDATE") {
		t.Fatal("roles")
	}
	if !(meData{Permissions: []struct {
		Resource string   `json:"resource"`
		Actions  []string `json:"actions"`
	}{{"vms", []string{"update:any"}}}}).can("VMS", "UPDATE") {
		t.Fatal("lowercase actions")
	}
}

// A key that can't read ME: the grants are unknown, so the mutation's own
// refusal says what's missing.
func TestUnknownGrants(t *testing.T) {
	l := newLab(t)
	l.srv.set("ChadashMe", forbidden(t))
	l.srv.set("ChadashContainerStart", forbidden(t))
	_, env := controlled(t, l)
	task, _ := env.Run(ActContainerStart, "unraid:i_test/container/Grafana", nil)
	if task.State != action.StateFailed || task.Error == nil || task.Error.Code != "missing_permission" || !strings.Contains(task.Error.Message, "Docker: Update") {
		t.Fatalf("task = %+v", task)
	}
	// Refused Docker reads: unavailable, naming the read scope.
	l.srv.set("ChadashDocker", forbidden(t))
	if r := env.Refusal(ActContainerStart, "unraid:i_test/container/Grafana", nil); r.Code != "unavailable" || !strings.Contains(r.Message, "DOCKER: Read") {
		t.Fatalf("refusal = %+v", r)
	}
}

func TestMutationRejectedByOldAPI(t *testing.T) {
	l := newLab(t)
	l.srv.set("ChadashContainerUpdate", []byte(`{"errors":[{"message":"Cannot query field \"updateContainer\" on type \"DockerMutations\".","extensions":{"code":"GRAPHQL_VALIDATION_FAILED"}}]}`))
	_, env := controlled(t, l)
	task, _ := env.Run(ActContainerUpdate, "unraid:i_test/container/tautulli", nil)
	if task.State != action.StateFailed || task.Error.Code != "unsupported" || !strings.Contains(task.Error.Message, "(4.37.5)") {
		t.Fatalf("task = %+v", task)
	}
}

// Verify reads until the change shows, and fails when it never does.
func TestVerifyTimesOut(t *testing.T) {
	l := newLab(t)
	l.stuck = true
	in, env := controlled(t, l)
	_ = env // speeds Until up
	var start action.Def
	for _, d := range integration.ActionDefs(in) {
		if d.ID == ActContainerStart {
			start = d
		}
	}
	tg, err := start.Resolve(t.Context(), "unraid:i_test/container/Grafana")
	if err != nil {
		t.Fatal(err)
	}
	req := action.Request{Action: ActContainerStart, Target: tg, Params: json.RawMessage(`{}`)}
	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	err = start.Verify(ctx, req, nil)
	if err == nil || err.Error() != "Grafana reports exited, not running" {
		t.Fatalf("verify = %v", err)
	}
}

func TestTestComparesMode(t *testing.T) {
	srv := newServer(t)
	// The consent key of v0.1 (read-only) on a controllable instance:
	// passes, with a warning per missing write scope.
	res := connectMode(t, integrationtest.New(t), srv, ModeControl).Test(t.Context())
	if !res.OK || len(res.Warnings) != 2 {
		t.Fatalf("control, read-only key = %+v", res)
	}
	for i, want := range []string{"DOCKER: Update", "VMS: Update"} {
		if w := res.Warnings[i]; w.Code != CodeMissingPermission || !strings.Contains(w.Message, want) || !strings.Contains(w.Message, "consent link") {
			t.Errorf("warning %d = %+v", i, w)
		}
	}
	// The control key: no warnings from the integration; its update grants
	// are reported as writes.
	srv.set("ChadashMe", meBody(t, nil, controlKey...))
	res = connectMode(t, integrationtest.New(t), srv, ModeControl).Test(t.Context())
	if !res.OK || len(res.Warnings) != 0 {
		t.Fatalf("control key = %+v", res)
	}
	writes := 0
	for _, g := range res.Grants {
		if g.CanWrite() {
			writes++
		}
	}
	if writes != 2 {
		t.Fatalf("grants = %+v", res.Grants)
	}
	// Read-only: the VMs read is optional, so a key without it is fine.
	srv.set("ChadashMe", meBody(t, nil, controlKey[:4]...))
	res = connectMode(t, integrationtest.New(t), srv, ModeReadOnly).Test(t.Context())
	if !res.OK || len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0].Message, "DOCKER: Read") {
		t.Fatalf("read-only = %+v", res)
	}
}

// The recipe's consent link follows the mode: each choice asks for exactly
// its scopes, and the control set is the read set plus the write scopes
// the actions check for.
func TestRecipeScopes(t *testing.T) {
	r, ok := integration.Default.Recipe(ID)
	if !ok {
		t.Fatal("no recipe")
	}
	var scopes integration.RecipeParam
	for _, p := range r.Params {
		if p.Name == "scopes" {
			scopes = p
		}
	}
	if scopes.From != "config.mode" || len(scopes.Options) != 2 {
		t.Fatalf("scopes = %+v", scopes)
	}
	byMode := map[string][]string{}
	for _, o := range scopes.Options {
		byMode[o.When] = strings.Split(o.Value, ",")
	}
	read := byMode[ModeReadOnly]
	if scopes.Default != strings.Join(read, ",") || !slices.Contains(read, "vms:read_any") || slices.ContainsFunc(read, func(s string) bool { return !strings.HasSuffix(s, ":read_any") }) {
		t.Fatalf("read-only scopes = %v (default %q)", read, scopes.Default)
	}
	want := slices.Clone(read)
	for _, s := range controlScopes {
		want = append(want, s.scope)
	}
	got := slices.Clone(byMode[ModeControl])
	slices.Sort(want)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("control scopes = %v, want %v", got, want)
	}
}

func TestVMs(t *testing.T) {
	srv := newServer(t)
	h := integrationtest.New(t)
	in := connect(t, h, srv)
	if err := in.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	ubuntu, ok := h.Resource("unraid:i_test/vm/" + vmUbuntu)
	win, ok2 := h.Resource("unraid:i_test/vm/" + vmWindows)
	if !ok || !ok2 || ubuntu.Kind != "vm" || ubuntu.Name != "jupiter_ubuntu" || ubuntu.State != "running" || win.Name != "Windows 10" || win.State != "stopped" {
		t.Fatalf("vms = %+v %+v", ubuntu, win)
	}
	runsOn := false
	for _, r := range h.Graph().Relations {
		if string(r.From) == "unraid:i_test/vm/"+vmUbuntu && r.Type == "runs_on" && string(r.To) == "unraid:i_test/host" {
			runsOn = true
		}
	}
	if !runsOn {
		t.Fatal("the VM doesn't run on the host")
	}

	// Refused on a read-only instance: quiet. On a controllable one: a warning.
	srv.set("ChadashVMs", forbidden(t))
	h.Advance(time.Hour)
	if err := in.Sync(t.Context(), integrationtest.Watched); err != nil || len(in.warnings) != 0 {
		t.Fatalf("%v, warnings = %v", err, in.warnings)
	}
	ctl := connectMode(t, integrationtest.New(t), srv, ModeControl)
	if err := ctl.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	if w, ok := ctl.warnings[partVMs]; !ok || !strings.Contains(w.Message, "VMS: Read") {
		t.Fatalf("warnings = %v", ctl.warnings)
	}
	// Any other failure (the VM manager is off): logged only.
	srv.set("ChadashVMs", []byte(`{"data":null,"errors":[{"message":"Failed to connect to libvirt","extensions":{"code":"INTERNAL_SERVER_ERROR"}}]}`))
	h.Advance(time.Hour)
	if err := ctl.Sync(t.Context(), integrationtest.Watched); err != nil || len(ctl.warnings) != 0 {
		t.Fatalf("%v, warnings = %v", err, ctl.warnings)
	}
}
