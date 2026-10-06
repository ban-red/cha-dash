package docker

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"

	"dash.cha.sh/internal/node"
	"dash.cha.sh/internal/nodeproto"
)

// setup runs the module behind a dispatcher with policy, against a fake
// Docker holding a few containers.
func setup(t *testing.T, policyYAML string) (*fakeDocker, *node.Dispatcher) {
	t.Helper()
	f, host := newFakeDocker(t)
	p, err := node.ParsePolicy([]byte(policyYAML))
	if err != nil {
		t.Fatal(err)
	}
	m, err := New(Options{Host: host, Policy: p, SelfID: "self00000000"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	d, err := node.NewDispatcher(p, m)
	if err != nil {
		t.Fatal(err)
	}
	f.add(&fakeContainer{Name: "sonarr", Image: "sonarr:4", Env: []string{"API_KEY=hunter2", "TZ=UTC"},
		Labels: map[string]string{"com.docker.compose.service": "sonarr"},
		Mounts: []map[string]any{
			{"Type": "volume", "Name": "sonarr_config", "Destination": "/config", "RW": true},
			{"Type": "volume", "Name": strings.Repeat("a", 64), "Destination": "/cache", "RW": true},
			{"Type": "bind", "Source": "/srv/tv", "Destination": "/tv", "RW": true},
		},
		Networks: map[string]any{"media": map[string]any{"Aliases": []string{"sonarr"}, "IPAddress": "192.0.2.5"}}})
	f.add(&fakeContainer{Name: "chadash-chadash-1", Image: "ghcr.io/ban-red/chadash:0.2", Labels: map[string]string{"com.docker.compose.service": "chadash"}})
	f.add(&fakeContainer{Name: "vaultwarden", Image: "vaultwarden/server", State: "exited"})
	f.add(&fakeContainer{ID: "self00000000" + strings.Repeat("0", 52), Name: "weird-name", Image: "ghcr.io/ban-red/chadash-node:0.2"})
	return f, d
}

func call(t *testing.T, d *node.Dispatcher, verb string, req any) (json.RawMessage, []json.RawMessage, error) {
	t.Helper()
	b, _ := json.Marshal(req)
	var items []json.RawMessage
	res, err := d.Call(t.Context(), verb, b, func(x json.RawMessage) { items = append(items, x) })
	return res, items, err
}

func TestReads(t *testing.T) {
	_, d := setup(t, "")
	res, _, err := call(t, d, "engine.version", nil)
	if err != nil || !strings.Contains(string(res), `"ApiVersion":"1.54"`) {
		t.Fatalf("version: %s %v", res, err)
	}
	res, _, err = call(t, d, "engine.info", nil)
	if err != nil || !strings.Contains(string(res), `"Name":"jupiter"`) || strings.Contains(string(res), "hunter2") {
		t.Fatalf("info: %s %v", res, err)
	}
	res, _, err = call(t, d, "container.list", map[string]bool{"all": true})
	var list []container.Summary
	if err != nil || json.Unmarshal(res, &list) != nil || len(list) != 4 {
		t.Fatalf("list: %s %v", res, err)
	}
	res, _, err = call(t, d, "container.inspect", map[string]string{"id": "sonarr"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(res), "hunter2") || !strings.Contains(string(res), `"API_KEY=[redacted]"`) || !strings.Contains(string(res), `"/config"`) {
		t.Fatalf("inspect leaks or loses data: %s", res)
	}
	if _, _, err := call(t, d, "container.inspect", map[string]string{"id": "nope"}); nodeproto.CodeOf(err) != nodeproto.CodeNotFound {
		t.Fatalf("missing container = %v", err)
	}
	if _, _, err := call(t, d, "container.inspect", map[string]string{"id": "../../etc"}); nodeproto.CodeOf(err) != nodeproto.CodeInvalid {
		t.Fatalf("path in id = %v", err)
	}
	res, _, err = call(t, d, "container.stats", map[string]string{"id": "sonarr"})
	if err != nil || !strings.Contains(string(res), "online_cpus") {
		t.Fatalf("stats: %s %v", res, err)
	}
	res, items, err := call(t, d, "engine.events", map[string]any{"filters": map[string][]string{"type": {"container"}}, "since": "1", "until": "2"})
	if err != nil || len(items) != 2 || !strings.Contains(string(res), `"events":2`) {
		t.Fatalf("events: %s %v %d", res, err, len(items))
	}
	if strings.Contains(string(items[0]), "hunter2") || !strings.Contains(string(items[0]), `"name":"sonarr"`) {
		t.Fatalf("event attributes: %s", items[0])
	}
}

func TestRedactEnvOff(t *testing.T) {
	_, d := setup(t, "docker: { redact_env: false }\n")
	res, _, err := call(t, d, "container.inspect", map[string]string{"id": "sonarr"})
	if err != nil || !strings.Contains(string(res), "API_KEY=hunter2") {
		t.Fatalf("inspect: %s %v", res, err)
	}
}

func TestLogs(t *testing.T) {
	f, d := setup(t, "")
	f.logs = []fakeLog{{1, "first line\nsecond "}, {2, "an error\n"}, {1, "line\n"}, {1, "no newline at the end"}}
	res, items, err := call(t, d, "container.logs", map[string]any{"id": "sonarr", "tail": "50"})
	if err != nil || !strings.Contains(string(res), `"lines":4`) {
		t.Fatalf("logs: %s %v", res, err)
	}
	var got []string
	for _, it := range items {
		var l logLine
		json.Unmarshal(it, &l)
		got = append(got, l.Stream+":"+l.Line)
	}
	want := []string{"stdout:first line", "stderr:an error", "stdout:second line", "stdout:no newline at the end"}
	if !slices.Equal(got, want) {
		t.Fatalf("lines = %q, want %q", got, want)
	}
	f.tty = true
	f.byName("sonarr").Tty = true
	f.logs = []fakeLog{{0, "tty one\ntty two\n"}}
	_, items, err = call(t, d, "container.logs", map[string]any{"id": "sonarr"})
	if err != nil || len(items) != 2 {
		t.Fatalf("tty logs: %v %d", err, len(items))
	}
	for _, bad := range []map[string]any{{"id": "sonarr", "tail": "-1"}, {"id": "sonarr", "tail": "lots"}, {"id": "sonarr", "since": "yesterday"}} {
		if _, _, err := call(t, d, "container.logs", bad); nodeproto.CodeOf(err) != nodeproto.CodeInvalid {
			t.Errorf("%v = %v", bad, err)
		}
	}
	// Logs need docker.logs.
	_, d = setup(t, "docker: { allow: [read] }\n")
	if _, _, err := call(t, d, "container.logs", map[string]any{"id": "sonarr"}); nodeproto.CodeOf(err) != nodeproto.CodeForbidden {
		t.Fatalf("logs without the grant = %v", err)
	}
}

func TestLifecycle(t *testing.T) {
	f, d := setup(t, "")
	res, _, err := call(t, d, "container.stop", map[string]any{"id": "sonarr", "timeout": 5})
	if err != nil || !strings.Contains(string(res), `"state":"exited"`) {
		t.Fatalf("stop: %s %v", res, err)
	}
	if _, _, err := call(t, d, "container.stop", map[string]any{"id": "sonarr"}); nodeproto.CodeOf(err) != nodeproto.CodeConflict {
		t.Fatalf("stop twice = %v", err)
	}
	res, _, err = call(t, d, "container.start", map[string]any{"id": "sonarr"})
	if err != nil || !strings.Contains(string(res), `"state":"running"`) {
		t.Fatalf("start: %s %v", res, err)
	}
	if _, _, err := call(t, d, "container.start", map[string]any{"id": "sonarr"}); nodeproto.CodeOf(err) != nodeproto.CodeConflict {
		t.Fatalf("start twice = %v", err)
	}
	if _, _, err := call(t, d, "container.restart", map[string]any{"id": "sonarr"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := call(t, d, "container.stop", map[string]any{"id": "sonarr", "timeout": 9999}); nodeproto.CodeOf(err) != nodeproto.CodeInvalid {
		t.Fatalf("huge timeout = %v", err)
	}
	if n := len(f.calls); n != 3 {
		t.Fatalf("calls %v", f.calls)
	}
}

// TestProtected checks every write verb refuses the hub, the node itself
// (by its ID, whatever its name) and docker.protect, before Docker sees a
// write.
func TestProtected(t *testing.T) {
	f, d := setup(t, "docker: { allow: [read, logs, lifecycle, update, remove], protect: [vaultwarden] }\n")
	writes := []string{"container.start", "container.stop", "container.restart", "container.pull", "container.recreate", "container.remove"}
	for _, verb := range writes {
		for _, target := range []string{"chadash-chadash-1", "vaultwarden", "weird-name", "self00000000"} {
			_, _, err := call(t, d, verb, map[string]any{"id": target, "force": true})
			if nodeproto.CodeOf(err) != nodeproto.CodeForbidden {
				t.Errorf("%s %s = %v, want forbidden", verb, target, err)
			}
		}
	}
	if len(f.calls) != 0 {
		t.Fatalf("Docker saw writes: %v", f.calls)
	}
	// Reads of protected containers are fine.
	if _, _, err := call(t, d, "container.inspect", map[string]any{"id": "vaultwarden"}); err != nil {
		t.Fatal(err)
	}
	// container.can answers before the hub plans.
	for _, c := range []struct {
		id, perm string
		ok       bool
	}{
		{"vaultwarden", "docker.lifecycle", false},
		{"vaultwarden", "docker.logs", true},
		{"sonarr", "docker.lifecycle", true},
		{"sonarr", "docker.exec", false},
		{"chadash-chadash-1", "docker.update", false},
	} {
		res, _, err := call(t, d, "container.can", map[string]string{"id": c.id, "permission": c.perm})
		var out struct {
			Allowed bool
			Reason  string
		}
		json.Unmarshal(res, &out)
		if err != nil || out.Allowed != c.ok || (!c.ok && out.Reason == "") {
			t.Errorf("can %s %s = %s %v", c.id, c.perm, res, err)
		}
	}
	if _, _, err := call(t, d, "container.can", map[string]string{"id": "nope", "permission": "docker.lifecycle"}); nodeproto.CodeOf(err) != nodeproto.CodeNotFound {
		t.Errorf("can on a missing container = %v", err)
	}
}

func TestPullAndRecreate(t *testing.T) {
	f, d := setup(t, "")
	res, items, err := call(t, d, "container.pull", map[string]any{"id": "sonarr"})
	if err != nil || len(items) != 3 || !strings.Contains(string(res), `"changed":true`) {
		t.Fatalf("pull: %s %v %d", res, err, len(items))
	}
	old := f.byName("sonarr")
	res, items, err = call(t, d, "container.recreate", map[string]any{"id": "sonarr"})
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]string
	json.Unmarshal(res, &out)
	if out["state"] != "running" || out["old_id"] != old.ID || out["id"] == old.ID || len(items) != 4 {
		t.Fatalf("recreate: %s items %d", res, len(items))
	}
	nw := f.byName("sonarr")
	if nw == nil || nw.ID == old.ID || nw.ImageID != "sha256:"+strings.Repeat("2", 64) {
		t.Fatalf("new container %+v", nw)
	}
	if !slices.Equal(nw.Env, old.Env) || nw.Labels["com.docker.compose.service"] != "sonarr" {
		t.Fatal("the config didn't carry over")
	}
	// The anonymous volume is mounted by name, so its data survives.
	var anon bool
	for _, m := range nw.Mounts {
		anon = anon || m["Name"] == strings.Repeat("a", 64) && m["Destination"] == "/cache"
	}
	if !anon {
		t.Fatalf("anonymous volume lost: %v", nw.Mounts)
	}
	if f.find(old.ID) != nil {
		t.Fatal("the old container is still there")
	}

	// A failed create puts the old container back, running, under its name.
	f.failCreate = true
	before := f.byName("sonarr").ID
	if _, _, err := call(t, d, "container.recreate", map[string]any{"id": "sonarr"}); err == nil {
		t.Fatal("recreate succeeded")
	}
	c := f.byName("sonarr")
	if c == nil || c.ID != before || c.State != "running" {
		t.Fatalf("not rolled back: %+v", c)
	}
	f.add(&fakeContainer{Name: "broken", Image: "missing:1"})
	if _, _, err := call(t, d, "container.pull", map[string]any{"id": "broken"}); err == nil || !strings.Contains(err.Error(), "manifest unknown") {
		t.Fatalf("pull of a missing tag = %v", err)
	}
}

func TestRemove(t *testing.T) {
	f, d := setup(t, "docker: { allow: [read, lifecycle, remove] }\n")
	if _, _, err := call(t, d, "container.remove", map[string]any{"id": "sonarr"}); nodeproto.CodeOf(err) != nodeproto.CodeConflict {
		t.Fatalf("remove running = %v", err)
	}
	res, _, err := call(t, d, "container.remove", map[string]any{"id": "sonarr", "force": true, "volumes": true})
	if err != nil {
		t.Fatal(err)
	}
	var out struct{ Kept, Removed []string }
	json.Unmarshal(res, &out)
	if !slices.Equal(out.Kept, []string{"sonarr_config"}) || !slices.Equal(out.Removed, []string{strings.Repeat("a", 64)}) {
		t.Fatalf("volumes %s", res)
	}
	if f.byName("sonarr") != nil {
		t.Fatal("still there")
	}
	// The default policy has no remove.
	_, d = setup(t, "")
	if _, _, err := call(t, d, "container.remove", map[string]any{"id": "sonarr"}); nodeproto.CodeOf(err) != nodeproto.CodeForbidden {
		t.Fatalf("default remove = %v", err)
	}
}

func TestVolumePlan(t *testing.T) {
	anon := strings.Repeat("b", 64)
	mounts := []container.MountPoint{
		{Type: mount.TypeVolume, Name: "db_data"}, {Type: mount.TypeVolume, Name: anon}, {Type: mount.TypeBind, Source: "/srv"},
	}
	kept, removed := VolumePlan(mounts, false)
	if !slices.Equal(kept, []string{"db_data", anon}) || removed != nil {
		t.Fatalf("without volumes: %v %v", kept, removed)
	}
	kept, removed = VolumePlan(mounts, true)
	if !slices.Equal(kept, []string{"db_data"}) || !slices.Equal(removed, []string{anon}) {
		t.Fatalf("with volumes: %v %v", kept, removed)
	}
}

func TestSelfProject(t *testing.T) {
	f, host := newFakeDocker(t)
	f.add(&fakeContainer{ID: "abcdef123456" + strings.Repeat("0", 52), Name: "chadash-chadash-node-1", Labels: map[string]string{"com.docker.compose.project": "chadash"}})
	m, _ := New(Options{Host: host, Policy: node.DefaultPolicy(), SelfID: "abcdef123456"})
	defer m.Close()
	if p, err := m.SelfProject(t.Context()); err != nil || p != "chadash" {
		t.Fatalf("self project = %q %v", p, err)
	}
	other, _ := New(Options{Host: host, Policy: node.DefaultPolicy(), SelfID: "999999999999"})
	defer other.Close()
	if p, err := other.SelfProject(t.Context()); err != nil || p != "" {
		t.Fatalf("unknown self = %q %v", p, err)
	}
}

// TestSelfInfo: engine.self names the node's container and the hub's in
// the same compose project, by service, else by image; anything else in
// another project is never taken for the hub.
func TestSelfInfo(t *testing.T) {
	f, host := newFakeDocker(t)
	proj := func(p, svc string) map[string]string {
		return map[string]string{"com.docker.compose.project": p, "com.docker.compose.service": svc}
	}
	f.add(&fakeContainer{ID: "abcdef123456" + strings.Repeat("0", 52), Name: "chadash-chadash-node-1", Image: "ghcr.io/ban-red/chadash-node:0.2", Labels: proj("chadash", "chadash-node")})
	hub := f.add(&fakeContainer{ID: "fedcba654321" + strings.Repeat("1", 52), Name: "chadash-chadash-1", Image: "ghcr.io/ban-red/chadash:0.2", Labels: proj("chadash", "chadash")})
	f.add(&fakeContainer{Name: "other-chadash-1", Image: "ghcr.io/ban-red/chadash:0.1", Labels: proj("other", "chadash")})
	m, _ := New(Options{Host: host, Policy: node.DefaultPolicy(), SelfID: "abcdef123456"})
	defer m.Close()
	d, err := node.NewDispatcher(node.DefaultPolicy(), m)
	if err != nil {
		t.Fatal(err)
	}
	res, _, err := call(t, d, "engine.self", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	var s Self
	_ = json.Unmarshal(res, &s)
	if s.Project != "chadash" || s.Node == nil || s.Node.Name != "chadash-chadash-node-1" || s.Hub == nil || s.Hub.ID != hub.ID || s.Hub.Name != "chadash-chadash-1" || s.Hub.Service != "chadash" {
		t.Fatalf("self = %s", res)
	}

	// A renamed service: the image tells.
	hub.Labels = proj("chadash", "dash")
	if s, err := m.SelfInfo(t.Context()); err != nil || s.Hub == nil || s.Hub.ID != hub.ID {
		t.Fatalf("by image: %+v %v", s.Hub, err)
	}
	// Two candidates: none.
	f.add(&fakeContainer{Name: "chadash-dash-2", Image: "chadash:dev", Labels: proj("chadash", "dash2")})
	if s, err := m.SelfInfo(t.Context()); err != nil || s.Hub != nil {
		t.Fatalf("ambiguous: %+v %v", s.Hub, err)
	}
	// Not in a container: nothing.
	other, _ := New(Options{Host: host, Policy: node.DefaultPolicy(), SelfID: "999999999999"})
	defer other.Close()
	if s, err := other.SelfInfo(t.Context()); err != nil || s.Node != nil || s.Hub != nil {
		t.Fatalf("unknown self = %+v %v", s, err)
	}
	if imageRepo("ghcr.io/ban-red/chadash-node@sha256:abc") != "chadash-node" || imageRepo("chadash") != "chadash" || imageRepo("localhost:5000/chadash:1") != "chadash" {
		t.Fatal("imageRepo")
	}
}

func TestDockerUnreachable(t *testing.T) {
	p := node.DefaultPolicy()
	m, err := New(Options{Host: "unix:///nonexistent/docker.sock", Policy: p})
	if err != nil {
		t.Fatal(err)
	}
	d, _ := node.NewDispatcher(p, m)
	_, _, err = call(t, d, "container.list", nil)
	if nodeproto.CodeOf(err) != nodeproto.CodeUnavailable {
		t.Fatalf("unreachable = %v", err)
	}
}
