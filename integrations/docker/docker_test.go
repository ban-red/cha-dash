package docker

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/integration/integrationtest"
)

func TestRegistered(t *testing.T) {
	_, m, ok := integration.Default.Get(ID)
	if !ok || m.Name != "Docker" || len(m.SecretFields()) != 0 || m.Poll.Interval != watchedInterval {
		t.Fatalf("manifest = %+v", m)
	}
	if mode := m.ConfigSchema.Property("tls").Property("mode"); mode.Default != nil {
		t.Fatal("tls has no default mode: its presence switches the client to https")
	}
}

func TestValidate(t *testing.T) {
	// The shapes the Homepage importer writes (expected-plan.json and its
	// variations), plus a TLS endpoint.
	for _, cfg := range []map[string]any{
		{"endpoint": "tcp://dockerproxy:2375"},
		{"endpoint": "tcp://10.0.11.120:2375"},
		{"endpoint": "unix:///var/run/docker.sock"},
		{"endpoint": "tcp://docker.lab.example:2376", "tls": map[string]any{"mode": "tofu"}},
		{"endpoint": "tcp://docker.lab.example"},
	} {
		if errs := integrationtest.Validate(Integration{}, cfg, nil); len(errs) != 0 {
			t.Errorf("%v: %v", cfg, errs)
		}
	}
	for cfg, path := range map[string]string{
		`{}`: "/endpoint",
		`{"endpoint": "http://10.0.11.120:2375"}`:                                "/endpoint",
		`{"endpoint": "unix://var/run/docker.sock"}`:                             "/endpoint",
		`{"endpoint": "tcp://host:2375/v1.54"}`:                                  "/endpoint",
		`{"endpoint": "unix:///var/run/docker.sock", "tls": {"mode": "verify"}}`: "/tls",
		`{"endpoint": "tcp://host:2375", "secret": "x"}`:                         "/secret",
	} {
		var m map[string]any
		if err := json.Unmarshal([]byte(cfg), &m); err != nil {
			t.Fatal(err)
		}
		errs := integrationtest.Validate(Integration{}, m, nil)
		if len(errs) == 0 || errs[0].Path != path {
			t.Errorf("%s: %v, want an error at %s", cfg, errs, path)
		}
	}
}

func syncOK(t *testing.T, inst integration.Instance) {
	t.Helper()
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
}

func resource(t *testing.T, h *integrationtest.Harness, id string) graph.Resource {
	t.Helper()
	r, ok := h.Resource(id)
	if !ok {
		t.Fatalf("no resource %s", id)
	}
	return r
}

func strs(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

func links(r graph.Resource) map[string]string {
	out := map[string]string{}
	if ls, ok := r.Attrs["links"].([]any); ok {
		for _, l := range ls {
			m := l.(map[string]any)
			out[m["title"].(string)] = m["url"].(string)
		}
	}
	return out
}

func TestSyncThroughTecnativa(t *testing.T) {
	f := newFake(t, tecnativa)
	h := integrationtest.New(t)
	inst := h.Connect(Integration{}, map[string]any{"endpoint": f.serve()}, nil)
	syncOK(t, inst)

	eng := resource(t, h, "docker:i_test/engine")
	if eng.Kind != "engine" || eng.Name != "Test" || eng.State != "running" || eng.Health != integration.HealthOK ||
		eng.Attrs["access"] != "broad-proxy" || eng.Attrs["via"] != "socket-proxy" || eng.Attrs["version"] != "29.5.3" ||
		eng.Attrs["api_version"] != "1.54" || eng.Attrs["containers"] != 74.0 || eng.Attrs["running"] != 44.0 ||
		eng.Attrs["manager"] != "unraid" || eng.Attrs["ip"] != "127.0.0.1" || eng.Attrs["hostname"] != nil {
		t.Fatalf("engine = %+v", eng)
	}
	if caps := strs(eng.Attrs["caps"]); !slices.Equal(caps, []string{"containers"}) {
		t.Fatalf("engine caps = %v", caps)
	}
	g := h.Graph()
	n := 0
	for _, r := range g.Resources {
		if r.Kind == "container" {
			n++
		}
	}
	if n != 74 {
		t.Fatalf("%d containers, want 74", n)
	}

	sonarr := resource(t, h, "docker:i_test/container/sonarr")
	if sonarr.Kind != "container" || sonarr.State != "running" || sonarr.Health != integration.HealthOK ||
		sonarr.Attrs["container_id"] != "aa2bd00fa7c9" || sonarr.Attrs["image"] != "binhex/arch-sonarr" ||
		sonarr.Attrs["restart_policy"] != "no" || sonarr.Attrs["restart_count"] != 0.0 || sonarr.Attrs["health_check"] != "healthy" ||
		sonarr.Attrs["desired"] != nil || sonarr.Attrs["network_mode"] != "bridge" || sonarr.Attrs["started_at"] != "2026-09-05T22:11:52Z" {
		t.Fatalf("sonarr = %+v", sonarr)
	}
	if p := strs(sonarr.Attrs["ports"]); !slices.Equal(p, []string{"8989/tcp", "9897/tcp"}) {
		t.Fatalf("ports = %v", p)
	}
	if p := strs(sonarr.Attrs["published"]); !slices.Equal(p, []string{"9897->9897/tcp", "18989->8989/tcp"}) {
		t.Fatalf("published = %v", p)
	}
	if ips := strs(sonarr.Attrs["ips"]); !slices.Equal(ips, []string{"10.101.0.24"}) {
		t.Fatalf("ips = %v", ips)
	}
	// Only allowlisted labels; the image's OCI labels stay behind.
	if sonarr.Labels["net.unraid.docker.managed"] != "dockerman" || len(sonarr.Labels) != 3 {
		t.Fatalf("labels = %v", sonarr.Labels)
	}
	// Unraid's web UI template, filled with the engine's address and the
	// host port bound to 8989.
	if l := links(sonarr); l["Web UI"] != "http://127.0.0.1:18989/" {
		t.Fatalf("links = %v", l)
	}
	plex := resource(t, h, "docker:i_test/container/Plex-Current")
	if l := links(plex); l["Web UI"] != "http://127.0.0.1:32400/web/index.html" || plex.Attrs["network_mode"] != "host" {
		t.Fatalf("host network = %+v", plex)
	}

	// Label discovery: homepage.* labels ride along, minus the secrets.
	immich := resource(t, h, "docker:i_test/container/immich_server")
	want := map[string]string{
		"homepage.group": "Apps", "homepage.name": "Immich", "homepage.href": "https://photos.family.example",
		"homepage.icon": "immich.png", "homepage.widget.type": "immich", "homepage.widget.url": "http://10.0.11.120:2283",
		"homepage.widget.version": "2", "com.docker.compose.project": "immich", "com.docker.compose.service": "immich-server",
	}
	for k, v := range want {
		if immich.Labels[k] != v {
			t.Errorf("immich label %s = %q, want %q", k, immich.Labels[k], v)
		}
	}
	if _, ok := immich.Labels["homepage.widget.key"]; ok {
		t.Fatal("a widget key reached the graph")
	}
	if l := links(immich); l["Immich"] != "https://photos.family.example" {
		t.Fatalf("immich links = %v", l)
	}
	pbs := resource(t, h, "docker:i_test/container/proxmox-backup-server")
	if _, ok := pbs.Labels["homepage.widget.password"]; ok || pbs.Labels["homepage.widget.username"] != "admin" || pbs.Labels["homepage.group"] != "Backup" {
		t.Fatalf("pbs labels = %v", pbs.Labels)
	}

	// Exited containers: the restart policy decides whether that's a problem.
	llama := resource(t, h, "docker:i_test/container/llama-swap")
	if llama.State != "exited" || llama.Health != integration.HealthDown || llama.Attrs["health.reason"] != "OOM-killed" ||
		llama.Attrs["oom_killed"] != true || llama.Attrs["exit_code"] != 0.0 || llama.Attrs["desired"] != "running" || llama.Attrs["health_check"] != nil {
		t.Fatalf("llama-swap = %+v", llama)
	}
	whisper := resource(t, h, "docker:i_test/container/ai-whisperasr-1")
	if whisper.Health != integration.HealthAttention || whisper.Attrs["restart_count"] != 1.0 {
		t.Fatalf("ai-whisperasr-1 = %+v", whisper)
	}
	pingvin := resource(t, h, "docker:i_test/container/pingvin-share")
	if pingvin.Health != integration.HealthOK || pingvin.Attrs["health.reason"] != "stopped (exited 137)" || pingvin.Attrs["health_check"] != nil {
		t.Fatalf("pingvin-share (stale unhealthy, restart policy no) = %+v", pingvin)
	}
	if bl := resource(t, h, "docker:i_test/container/BookLore"); bl.State != "created" || bl.Health != integration.HealthOK {
		t.Fatalf("created = %+v", bl)
	}

	// Stacks from the compose project label.
	st := resource(t, h, "docker:i_test/stack/immich")
	if st.Kind != "stack" || st.State != "running" || st.Health != integration.HealthOK || st.Attrs["containers"] != 4.0 ||
		st.Attrs["managed_by"] != "composeman" || st.Attrs["working_dir"] != "/boot/config/plugins/compose.manager/projects/Immich" {
		t.Fatalf("immich stack = %+v", st)
	}
	if ai := resource(t, h, "docker:i_test/stack/ai"); ai.State != "stopped" || ai.Health != integration.HealthDown ||
		!strings.HasPrefix(ai.Attrs["health.reason"].(string), "llama-swap: ") {
		t.Fatalf("ai stack = %+v", ai)
	}
	for _, rel := range []string{
		"integration:i_test monitors docker:i_test/engine",
		"docker:i_test/container/sonarr runs_on docker:i_test/engine",
		"docker:i_test/container/immich_server part_of docker:i_test/stack/immich",
		"docker:i_test/stack/immich runs_on docker:i_test/engine",
	} {
		if g.Relations[rel] == nil {
			t.Errorf("missing relation %s", rel)
		}
	}

	// Hints: the coarse proxy (reads only, so info), and the secrets in labels.
	hint, ok := h.Incident(hintEndpoint)
	if !ok || hint.Class != "problem" || hint.Severity != "info" || hint.Category != integration.CategorySecurity || hint.Target != "docker:i_test/engine" ||
		!strings.Contains(hint.Summary, "every container's environment, logs and files") {
		t.Fatalf("endpoint hint = %+v", hint)
	}
	ls, ok := h.Incident(hintLabelSecrets)
	if !ok || ls.Severity != "attention" || ls.Category != integration.CategorySecurity || !strings.HasPrefix(ls.Summary, "immich_server, mealie and proxmox-backup-server carry") {
		t.Fatalf("label secrets hint = %+v", ls)
	}

	// Nothing secret reaches the graph or the inbox: no environment, no
	// label secrets, no other labels.
	all, _ := json.Marshal(g)
	inbox, _ := json.Marshal([]any{hint, ls})
	for _, s := range []string{"REDACTED", "OSzeOQuaiQ90iky2dyVGvWyAMhc7DvsZ2WtKiWAgc", "16303553-5814-2fe9", "pyNzdTqzPiZMCdA8", "org.opencontainers", "maintainer", "redacted@example.com"} {
		if strings.Contains(string(all), s) || strings.Contains(string(inbox), s) {
			t.Errorf("%q was published", s)
		}
	}

	// Every container was inspected once; nothing was refused beyond the
	// probe; nothing but GETs (the fake fails the test otherwise).
	if n := len(f.requests(`^GET /v1\.54/containers/[0-9a-f]{64}/json$`)); n != 74 {
		t.Fatalf("%d inspects, want 74", n)
	}
	// A sync with nothing changed inspects nothing.
	syncOK(t, inst)
	if n := len(f.requests(`^GET /v1\.54/containers/[0-9a-f]{64}/json$`)); n != 74 {
		t.Fatalf("%d inspects after an idle sync, want still 74", n)
	}
	// A restart between polls moves the summary's Status: re-inspected.
	c, ins := f.container("sonarr")
	f.update(func() {
		c["Status"] = "Up 5 seconds (healthy)"
		ins["RestartCount"] = 1.0
	})
	syncOK(t, inst)
	if n := len(f.requests(`^GET /v1\.54/containers/[0-9a-f]{64}/json$`)); n != 75 {
		t.Fatalf("%d inspects, want 75", n)
	}
	if r := resource(t, h, "docker:i_test/container/sonarr"); r.Attrs["restart_count"] != 1.0 {
		t.Fatalf("restart not seen: %+v", r.Attrs)
	}
	// The secrets move out of the labels: that hint resolves itself.
	f.update(func() {
		for _, c := range f.list {
			labels := c["Labels"].(map[string]any)
			delete(labels, "homepage.widget.key")
			delete(labels, "homepage.widget.password")
		}
	})
	syncOK(t, inst)
	if ls, _ := h.Incident(hintLabelSecrets); ls.Status != "resolved" || ls.ResolvedBy != "auto" {
		t.Fatalf("label secrets hint = %+v", ls)
	}
	if hint, _ := h.Incident(hintEndpoint); hint.Status != "open" {
		t.Fatalf("endpoint hint = %+v", hint)
	}
}

func TestTestThroughTecnativa(t *testing.T) {
	f := newFake(t, tecnativa)
	h := integrationtest.New(t)
	res := h.Connect(Integration{}, map[string]any{"endpoint": f.serve()}, nil).Test(t.Context())
	if !res.OK || res.Version != "29.5.3" {
		t.Fatalf("test = %+v", res)
	}
	names := map[string]integration.Grant{}
	for _, g := range res.Grants {
		names[g.Name] = g
	}
	for _, n := range []string{"containers", "inspect", "events", "stats", "logs and files"} {
		if g, ok := names[n]; !ok {
			t.Errorf("missing grant %s: %+v", n, res.Grants)
		} else if g.Access != integration.AccessRead || g.Write {
			t.Errorf("grant %s = %q (write %v), want read", n, g.Access, g.Write) // the web shows the access level
		}
	}
	for _, n := range []string{"info", "images", "engine", "writes"} {
		if _, ok := names[n]; ok {
			t.Errorf("grant %s isn't allowed by this proxy", n)
		}
	}
	codes := map[string]integration.Warning{}
	for _, w := range res.Warnings {
		codes[w.Code] = w
	}
	if codes["docker_broad_proxy"].Severity != integration.WarnWarning || !strings.Contains(codes["capability_missing"].Message, "info") ||
		strings.Contains(codes["capability_missing"].Message, "events") {
		t.Fatalf("warnings = %+v", res.Warnings)
	}
	if _, ok := h.Resource("docker:i_test/engine"); ok {
		t.Fatal("Test must not emit facts")
	}
}

func TestAnchoredProxy(t *testing.T) {
	f := newFake(t, anchored)
	h := integrationtest.New(t)
	inst := h.Connect(Integration{}, map[string]any{"endpoint": f.serve()}, nil)
	syncOK(t, inst)
	eng := resource(t, h, "docker:i_test/engine")
	if eng.Attrs["access"] != "filtered-proxy" || eng.Attrs["hostname"] != "jupiter" || eng.Attrs["cores"] != 12.0 || eng.Attrs["mem_gb"] != 64.0 {
		t.Fatalf("engine = %+v", eng.Attrs)
	}
	if _, ok := h.Incident(hintEndpoint); ok {
		t.Fatal("a path-anchored proxy earns no hint")
	}
	res := h.Connect(Integration{}, map[string]any{"endpoint": f.serve()}, nil).Test(t.Context())
	for _, g := range res.Grants {
		if g.CanWrite() || g.Name == "logs and files" {
			t.Fatalf("grant %+v", g)
		}
	}
	if len(res.Warnings) != 0 {
		t.Fatalf("warnings = %+v", res.Warnings)
	}
}

func TestRawAPI(t *testing.T) {
	f := newFake(t, raw)
	h := integrationtest.New(t)
	ep := f.serve()
	syncOK(t, h.Connect(Integration{}, map[string]any{"endpoint": ep}, nil))
	if eng := resource(t, h, "docker:i_test/engine"); eng.Attrs["access"] != "raw" || eng.Attrs["via"] != "tcp" {
		t.Fatalf("engine = %+v", eng.Attrs)
	}
	if hint, _ := h.Incident(hintEndpoint); !strings.Contains(hint.Summary, "root on the host") || hint.Severity != "attention" {
		t.Fatalf("raw API hint = %+v", hint)
	}
	res := h.Connect(Integration{}, map[string]any{"endpoint": ep}, nil).Test(t.Context())
	write := integration.WriteGrantWarning(res.Grants)
	if write == nil || !strings.Contains(write.Message, "engine") || res.Warnings[0].Code != "docker_raw_api" || res.Warnings[0].Severity != integration.WarnCritical {
		t.Fatalf("raw API test = %+v", res)
	}
}

func TestUnixSocket(t *testing.T) {
	f := newFake(t, raw)
	dir, err := os.MkdirTemp("", "cd") // short: macOS caps socket paths at 104 bytes
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "docker.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: f}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })

	h := integrationtest.New(t)
	syncOK(t, h.Connect(Integration{}, map[string]any{"endpoint": "unix://" + sock}, nil))
	eng := resource(t, h, "docker:i_test/engine")
	if eng.Attrs["via"] != "socket" || eng.Attrs["endpoint"] != "unix://"+sock || eng.Attrs["host"] != nil || eng.Attrs["containers"] != 74.0 {
		t.Fatalf("engine = %+v", eng.Attrs)
	}
	// No address for Unraid's template: no web UI link.
	if l := links(resource(t, h, "docker:i_test/container/sonarr")); l["Web UI"] != "" {
		t.Fatalf("links = %v", l)
	}
	if hint, _ := h.Incident(hintEndpoint); !strings.Contains(hint.Title, "full Docker access") {
		t.Fatalf("socket hint = %+v", hint)
	}
	res := h.Connect(Integration{}, map[string]any{"endpoint": "unix://" + sock}, nil).Test(t.Context())
	if !res.OK || integration.WriteGrantWarning(res.Grants) == nil || res.Warnings[0].Code != "docker_raw_socket" {
		t.Fatalf("socket test = %+v", res)
	}
}

func TestProxyWrites(t *testing.T) {
	f := newFake(t, tecnativa)
	ep := f.serve()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(ep, "tcp://"))
	// The proxy container publishes the port this endpoint uses, so the
	// hub can read its own switches (the capture redacted their values).
	proxy, ins := f.container("dockersocket")
	setEnv := func(post string) {
		f.update(func() {
			for _, p := range proxy["Ports"].([]any) {
				p.(map[string]any)["PublicPort"] = json.Number(port)
			}
			ins["Config"].(map[string]any)["Env"] = []any{"CONTAINERS=1", "POST=" + post, "ALLOW_RESTARTS=0", "SECRET_THING=hunter2"}
		})
	}
	grant := func() *integration.Grant {
		t.Helper()
		h := integrationtest.New(t)
		res := h.Connect(Integration{}, map[string]any{"endpoint": ep}, nil).Test(t.Context())
		for _, g := range res.Grants {
			if g.Name == "writes" {
				return &g
			}
		}
		return nil
	}
	if g := grant(); g != nil {
		t.Fatalf("redacted switches can't say: %+v", g)
	}
	setEnv("1")
	if g := grant(); g == nil || !g.CanWrite() || g.Detail != "the proxy allows writes (POST=1)" {
		t.Fatalf("POST=1: %+v", g)
	}
	setEnv("0")
	if g := grant(); g != nil {
		t.Fatalf("POST=0: %+v", g)
	}
	// The instance depends on its proxy.
	h := integrationtest.New(t)
	syncOK(t, h.Connect(Integration{}, map[string]any{"endpoint": ep}, nil))
	if h.Graph().Relations["integration:i_test depends_on docker:i_test/container/dockersocket"] == nil {
		t.Fatal("the instance should depend on its socket proxy")
	}
}

func TestProxyWritesWollomatic(t *testing.T) {
	var ins inspect
	if err := json.Unmarshal([]byte(`{"Config":{"Cmd":["-loglevel=info","-allowGET=/v1\\..{1,2}/(version|containers/.*)","-allowPOST=/v1\\..{1,2}/containers/.*/restart","--token=hunter2"],"Env":["SP_ALLOWFROM=10.0.0.0/8","PASSWORD=hunter2"]}}`), &ins); err != nil {
		t.Fatal(err)
	}
	if len(ins.Config.Cmd) != 1 || len(ins.Config.Env) != 0 {
		t.Fatalf("only write allowlists are kept: %v %v", ins.Config.Cmd, ins.Config.Env)
	}
	if w := proxyWrites("wollomatic/socket-proxy:1", ins); !w.known || !w.yes || w.why != "the proxy allows POST requests (-allowPOST)" {
		t.Fatalf("writes = %+v", w)
	}
	ins.Config.Cmd = nil
	if w := proxyWrites("wollomatic/socket-proxy:1", ins); !w.known || w.yes {
		t.Fatalf("GET only = %+v", w)
	}
}

func TestEvents(t *testing.T) {
	f := newFake(t, tecnativa)
	h := integrationtest.New(t)
	inst := h.Connect(Integration{}, map[string]any{"endpoint": f.serve()}, nil)
	sub := inst.(integration.Subscriber)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- sub.Subscribe(ctx, h.Emitter) }()

	waitFor := func(what string, cond func() bool) {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); !cond(); time.Sleep(5 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
		}
	}
	lists := func() int { return len(f.requests(`^GET /v1\.54/containers/json\?all=1$`)) }
	<-f.subscribed
	// Connecting re-lists everything (no since= replay), then goes live.
	waitFor("the first list", func() bool { _, ok := h.Resource("docker:i_test/container/sonarr"); return ok })
	waitFor("the live interval", func() bool { return inst.(integration.Intervaler).Interval() == liveInterval })
	if lists() != 1 {
		t.Fatalf("%d lists", lists())
	}

	// The recorded events are all health-check execs: the engine's filter
	// drops them, so nothing is re-read.
	var recorded []json.RawMessage
	readJSON(t, filepath.Join(fixtures, "events-live.json"), &recorded)
	for _, e := range recorded {
		f.events <- string(e)
	}
	// sonarr dies: the event names it and it alone is re-inspected.
	c, ins := f.container("sonarr")
	id := c["Id"].(string)
	f.update(func() {
		c["State"], c["Status"] = "exited", "Exited (1) 1 second ago"
		st := ins["State"].(map[string]any)
		st["Status"], st["Running"], st["ExitCode"], st["FinishedAt"] = "exited", false, 1.0, "2026-10-03T14:04:00Z"
	})
	f.events <- `{"Type":"container","Action":"die","Actor":{"ID":"` + id + `","Attributes":{"exitCode":"1","homepage.widget.key":"x"}},"time":1791079700}`
	waitFor("sonarr to exit", func() bool {
		r, _ := h.Resource("docker:i_test/container/sonarr")
		return r.State == "exited"
	})
	if r := resource(t, h, "docker:i_test/container/sonarr"); r.Attrs["exit_code"] != 1.0 || r.Attrs["finished_at"] != "2026-10-03T14:04:00Z" {
		t.Fatalf("exited sonarr = %+v", r.Attrs)
	}
	if n := len(f.requests(`^GET /v1\.54/containers/` + id + `/json$`)); n != 2 {
		t.Fatalf("sonarr inspected %d times, want 2", n)
	}
	if lists() != 2 {
		t.Fatalf("%d lists, want 2 (connect, the die event)", lists())
	}

	// The proxy cuts the stream: Subscribe ends with an error so the
	// runtime reconnects, and the reconnect re-lists.
	f.cut <- struct{}{}
	if err := <-done; err == nil || ctx.Err() != nil {
		t.Fatalf("a cut stream should be an error: %v", err)
	}
	if inst.(integration.Intervaler).Interval() != watchedInterval {
		t.Fatal("not live any more")
	}
	go func() { done <- sub.Subscribe(ctx, h.Emitter) }()
	<-f.subscribed
	waitFor("the re-list", func() bool { return lists() == 3 })
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("cancelled: %v", err)
	}
	if strings.Contains(h.Logs(), "homepage.widget.key") {
		t.Fatal("events are never logged")
	}
}

func TestEventsRefused(t *testing.T) {
	f := newFake(t, anchored)
	f.deny["/events"] = true
	h := integrationtest.New(t)
	inst := h.Connect(Integration{}, map[string]any{"endpoint": f.serve()}, nil)
	err := inst.(integration.Subscriber).Subscribe(t.Context(), h.Emitter)
	if !isDenied(err) {
		t.Fatalf("a refused stream is a capability not granted: %v", err)
	}
	// Polling carries on.
	syncOK(t, inst)
	res := h.Connect(Integration{}, map[string]any{"endpoint": f.serve()}, nil).Test(t.Context())
	if !res.OK || !strings.Contains(res.Warnings[0].Message, "events (live updates; Cha Dash polls instead)") {
		t.Fatalf("test = %+v", res)
	}
}

func TestRepairs(t *testing.T) {
	f := newFake(t, anchored)
	f.deny["/containers/json"] = true
	h := integrationtest.New(t)
	err := h.Connect(Integration{}, map[string]any{"endpoint": f.serve()}, nil).Sync(t.Context(), integrationtest.Watched)
	var re *integration.RepairError
	if !errors.As(err, &re) || re.Code != CodeCapability || re.Field != "endpoint" {
		t.Fatalf("list refused: %v", err)
	}
	if code, class, _ := integration.Classify(err); code != CodeCapability || class != integration.NeedsRepair {
		t.Fatalf("classified %s %v", code, class)
	}

	old := newFake(t, raw)
	old.api = "1.30"
	err = h.Connect(Integration{}, map[string]any{"endpoint": old.serve()}, nil).Sync(t.Context(), integrationtest.Watched)
	if code, _, _ := integration.Classify(err); code != integration.CodeUnsupportedVersion {
		t.Fatalf("API 1.30: %v", err)
	}

	web := http.NewServeMux()
	web.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	srv := &http.Server{Handler: web}
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	res := h.Connect(Integration{}, map[string]any{"endpoint": "tcp://" + ln.Addr().String()}, nil).Test(t.Context())
	if res.OK || res.ErrorCode != integration.CodeInvalidConfig || !strings.Contains(res.ErrorMessage, "doesn't look like a Docker API") {
		t.Fatalf("not docker: %+v", res)
	}
}

func TestPickAPI(t *testing.T) {
	for _, c := range []struct{ server, min, want string }{
		{"1.54", "1.40", "1.54"}, {"1.60", "1.40", "1.54"}, {"1.60", "1.58", "1.58"}, {"1.41", "", "1.41"},
	} {
		if got, err := pickAPI(c.server, c.min); err != nil || got != c.want {
			t.Errorf("pickAPI(%s, %s) = %s %v, want %s", c.server, c.min, got, err, c.want)
		}
	}
}

func TestPublicLabels(t *testing.T) {
	got, secret := publicLabels(map[string]string{
		"homepage.name": "Sonarr", "homepage.widget.key": "k", "homepage.widget.apiKey": "k", "homepage.widget.password": "p",
		"homepage.widget.username": "admin", "homepage.href": "https://user:pw@sonarr.lab.example", "chadash.group": "Media",
		"traefik.http.middlewares.auth.basicauth.users": "admin:$2a$12$x", "org.opencontainers.image.title": "x",
		"net.unraid.docker.managed": "dockerman", "com.docker.compose.project": "media",
	})
	want := map[string]string{"homepage.name": "Sonarr", "homepage.widget.username": "admin", "chadash.group": "Media",
		"net.unraid.docker.managed": "dockerman", "com.docker.compose.project": "media"}
	if len(got) != len(want) {
		t.Fatalf("labels = %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("labels = %v", got)
		}
	}
	if !slices.Equal(secret, []string{"homepage.href", "homepage.widget.apiKey", "homepage.widget.key", "homepage.widget.password"}) {
		t.Fatalf("secret = %v", secret)
	}
}

// TestStatsWhileWatched: a container's cpu.pct and mem.mib come only while
// someone watches it closely, the syncs speed up meanwhile without
// re-listing each time, and the series go when the watch ends.
func TestStatsWhileWatched(t *testing.T) {
	f := newFake(t, tecnativa)
	h := integrationtest.New(t)
	inst := h.Connect(Integration{}, map[string]any{"endpoint": f.serve()}, nil)
	syncOK(t, inst)
	var name, id string
	for _, c := range f.list {
		if c["State"] == "running" {
			name, id = strings.TrimPrefix(c["Names"].([]any)[0].(string), "/"), c["Id"].(string)
			break
		}
	}
	rid := "docker:i_test/container/" + name
	if h.Series(rid, "cpu.pct") != nil || len(f.requests("/stats")) != 1 { // the probe's
		t.Fatal("no stats while nobody watches closely")
	}
	iv := inst.(integration.InterestIntervaler)
	if iv.IntervalFor(integrationtest.Watched) != watchedInterval {
		t.Fatal("the usual interval while nobody watches closely")
	}

	want := integrationtest.Watched
	want.Resources = []string{"docker:i_other/container/" + name, rid}
	if iv.IntervalFor(want) != statsInterval {
		t.Fatalf("interval = %v", iv.IntervalFor(want))
	}
	lists := len(f.requests("/containers/json"))
	h.Advance(statsInterval)
	if err := inst.Sync(t.Context(), want); err != nil {
		t.Fatal(err)
	}
	if len(f.requests("/containers/json")) != lists {
		t.Fatal("a stats sync between refreshes doesn't list again")
	}
	if cpu := h.Series(rid, "cpu.pct"); cpu == nil || cpu["value"] != nil {
		t.Fatalf("the first reading has nothing to measure against: %v", cpu)
	}
	if mem := h.Series(rid, "mem.mib"); mem == nil || mem["value"] != 200.0 || mem["max"] != 1024.0 {
		t.Fatalf("mem.mib = %v", mem)
	}
	h.Advance(statsInterval)
	if err := inst.Sync(t.Context(), want); err != nil {
		t.Fatal(err)
	}
	if cpu := h.Series(rid, "cpu.pct"); cpu["value"] != 20.0 {
		t.Fatalf("cpu.pct = %v", cpu)
	}
	if n := len(f.requests("/containers/" + id + "/stats")); n != 2 {
		t.Fatalf("%d stats requests for the container", n)
	}
	h.Advance(watchedInterval)
	if err := inst.Sync(t.Context(), want); err != nil {
		t.Fatal(err)
	}
	if len(f.requests("/containers/json")) != lists+1 {
		t.Fatal("the list keeps its own pace")
	}

	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	if h.Series(rid, "cpu.pct") != nil || h.Series(rid, "mem.mib") != nil {
		t.Fatal("the series go when the watch ends")
	}
}
