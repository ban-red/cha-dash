package docker

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/audit"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/integration/integrationtest"
	"dash.cha.sh/internal/store"
)

// nodeErr is an error with a code, as the hub and the node report them.
type nodeErr struct{ code, msg string }

func (e *nodeErr) Error() string     { return e.msg }
func (e *nodeErr) ErrorCode() string { return e.code }

// fakeNode plays chadash-node in front of the fake engine: the read verbs
// become the engine's GETs (env redacted, as the node's default policy
// does), and the write verbs change a state override and are recorded.
type fakeNode struct {
	t    *testing.T
	base string // the fake engine's URL

	mu        sync.Mutex
	calls     []string
	state     map[string]string // container name → state after a write
	deny      map[string]string // permission → the node's reason
	offline   bool
	callsSeen chan string
	hubID     string // what engine.self names as the hub's container
}

func newFakeNode(t *testing.T, f *fakeDocker) *fakeNode {
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return &fakeNode{t: t, base: srv.URL, state: map[string]string{}, deny: map[string]string{}, callsSeen: make(chan string, 64)}
}

func (n *fakeNode) get(ctx context.Context, path string) ([]byte, int, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, n.base+path, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return b, resp.StatusCode, err
}

// idOf finds a container's full ID by name or ID prefix.
func (n *fakeNode) idOf(ctx context.Context, ref string) (string, string) {
	b, _, _ := n.get(ctx, "/v1.54/containers/json?all=1")
	var list []summary
	_ = json.Unmarshal(b, &list)
	for _, s := range list {
		if s.name() == ref || strings.HasPrefix(s.ID, ref) {
			return s.ID, s.name()
		}
	}
	return "", ""
}

func (n *fakeNode) Call(ctx context.Context, node, verb string, req any, progress func(json.RawMessage)) (json.RawMessage, error) {
	n.mu.Lock()
	n.calls = append(n.calls, verb)
	offline := n.offline
	n.mu.Unlock()
	select {
	case n.callsSeen <- verb:
	default:
	}
	if node != "local" {
		n.t.Errorf("called node %q", node)
	}
	if offline {
		return nil, &nodeErr{"not_connected", "the node isn't connected to the hub (local)"}
	}
	var p struct {
		ID         string              `json:"id"`
		All        bool                `json:"all"`
		Permission string              `json:"permission"`
		Filters    map[string][]string `json:"filters"`
		Since      string              `json:"since"`
		Until      string              `json:"until"`
		Volumes    bool                `json:"volumes"`
	}
	b, _ := json.Marshal(req)
	_ = json.Unmarshal(b, &p)
	switch verb {
	case "engine.version":
		b, _, err := n.get(ctx, "/version")
		return b, err
	case "engine.info":
		b, _, err := n.get(ctx, "/v1.54/info")
		return b, err
	case "container.list":
		b, _, err := n.get(ctx, "/v1.54/containers/json?all=1")
		return b, err
	case "container.inspect":
		id, name := n.idOf(ctx, p.ID)
		if id == "" {
			return nil, &nodeErr{"not_found", "No such container: " + p.ID}
		}
		b, _, err := n.get(ctx, "/v1.54/containers/"+id+"/json")
		var doc map[string]any
		_ = json.Unmarshal(b, &doc)
		if cfg, ok := doc["Config"].(map[string]any); ok {
			if env, ok := cfg["Env"].([]any); ok {
				for i, kv := range env {
					k, _, _ := strings.Cut(kv.(string), "=")
					env[i] = k + "=[redacted]"
				}
			}
		}
		n.mu.Lock()
		if st, ok := n.state[name]; ok {
			doc["State"].(map[string]any)["Status"] = st
			doc["State"].(map[string]any)["Running"] = st == "running"
		}
		if st := n.state[name]; st == "removed" {
			n.mu.Unlock()
			return nil, &nodeErr{"not_found", "No such container: " + p.ID}
		}
		n.mu.Unlock()
		out, _ := json.Marshal(doc)
		return out, err
	case "container.stats":
		return nil, &nodeErr{"not_found", "No such container"}
	case "engine.self":
		n.mu.Lock()
		id := n.hubID
		n.mu.Unlock()
		if id == "" {
			return json.RawMessage(`{"project":"chadash"}`), nil
		}
		return json.Marshal(map[string]any{"project": "chadash", "hub": map[string]string{"id": id, "name": "chadash", "service": "chadash"}})
	case "engine.events":
		q := url.Values{}
		if p.Filters != nil {
			fb, _ := json.Marshal(p.Filters)
			q.Set("filters", string(fb))
		}
		if p.Since != "" {
			q.Set("since", p.Since)
		}
		if p.Until != "" {
			q.Set("until", p.Until)
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, n.base+"/v1.54/events?"+q.Encode(), nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			progress(json.RawMessage(sc.Text()))
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return json.RawMessage(`{"events":0}`), nil
	case "container.can":
		n.mu.Lock()
		defer n.mu.Unlock()
		if why, ok := n.deny[p.Permission]; ok {
			return json.Marshal(map[string]any{"allowed": false, "reason": why})
		}
		return json.RawMessage(`{"allowed":true,"reason":""}`), nil
	case "container.start", "container.stop", "container.restart", "container.recreate", "container.pull", "container.remove", "container.logs":
		_, name := n.idOf(ctx, p.ID)
		n.mu.Lock()
		defer n.mu.Unlock()
		switch verb {
		case "container.start", "container.restart", "container.recreate":
			n.state[name] = "running"
		case "container.stop":
			n.state[name] = "exited"
		case "container.remove":
			n.state[name] = "removed"
			return json.RawMessage(`{"kept":["a"],"removed":[]}`), nil
		case "container.logs":
			progress(json.RawMessage(`{"stream":"stdout","line":"hello from the container"}`))
			return json.RawMessage(`{"lines":1}`), nil
		case "container.pull":
			progress(json.RawMessage(`{"status":"Pulling","id":"latest"}`))
			return json.RawMessage(`{"image":"x:1","changed":true}`), nil
		}
		return json.RawMessage(`{}`), nil
	}
	return nil, &nodeErr{"not_found", "no verb " + verb}
}

func (n *fakeNode) verbs() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return slices.Clone(n.calls)
}

// TestNodeTransportSameResources syncs the same engine through a raw
// endpoint and through the node, and checks every container and stack
// comes out the same, with the same IDs.
func TestNodeTransportSameResources(t *testing.T) {
	f := newFake(t, raw)
	direct := integrationtest.New(t)
	syncOK(t, direct.Connect(Integration{}, map[string]any{"endpoint": f.serve()}, nil))

	viaNode := integrationtest.New(t)
	viaNode.Nodes = newFakeNode(t, f)
	inst := viaNode.Connect(Integration{}, map[string]any{"endpoint": "node://local"}, nil)
	syncOK(t, inst)

	a, b := direct.Graph(), viaNode.Graph()
	n := 0
	for id, r := range a.Resources {
		if r.Kind != "container" && r.Kind != "stack" {
			continue
		}
		n++
		o, ok := b.Resources[id]
		if !ok {
			t.Errorf("%s is missing through the node", id)
			continue
		}
		// Like unix://, the node has no host address to fill Unraid's web UI
		// template with; that's the one expected difference.
		delete(r.Attrs, "links")
		delete(o.Attrs, "links")
		ra, _ := json.Marshal(r)
		rb, _ := json.Marshal(o)
		if string(ra) != string(rb) {
			t.Errorf("%s differs:\n direct %s\n node   %s", id, ra, rb)
		}
	}
	if n < 74 {
		t.Fatalf("compared %d resources", n)
	}
	for k := range a.Relations {
		if _, ok := b.Relations[k]; !ok && !strings.Contains(k, "/engine") {
			t.Errorf("relation %s is missing through the node", k)
		}
	}
	eng := resource(t, viaNode, "docker:i_test/engine")
	if eng.Attrs["via"] != "node" || eng.Attrs["access"] != "node" || eng.Attrs["node"] != "local" || eng.Attrs["host"] != nil || eng.Attrs["hostname"] != "jupiter" {
		t.Fatalf("engine = %+v", eng.Attrs)
	}
	// Test names the node, and the probe found inspect, events and info.
	res := inst.Test(t.Context())
	if !res.OK {
		t.Fatalf("test: %+v", res)
	}
	var grants []string
	for _, g := range res.Grants {
		grants = append(grants, g.Name)
		if g.Write {
			t.Errorf("the node transport reported a write grant: %+v", g)
		}
	}
	for _, want := range []string{"containers", "node", "inspect", "events", "info"} {
		if !slices.Contains(grants, want) {
			t.Errorf("grants %v lack %s", grants, want)
		}
	}
	for _, w := range res.Warnings {
		if strings.Contains(w.Code, "raw") || strings.Contains(w.Code, "broad") {
			t.Errorf("warning %+v", w)
		}
	}
}

func TestNodeTransportEvents(t *testing.T) {
	f := newFake(t, raw)
	h := integrationtest.New(t)
	h.Nodes = newFakeNode(t, f)
	inst := h.Connect(Integration{}, map[string]any{"endpoint": "node://local"}, nil)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- inst.(integration.Subscriber).Subscribe(ctx, nil) }()
	select {
	case <-f.subscribed:
	case <-time.After(15 * time.Second): // generous: -race on a busy machine
		t.Fatal("no event stream through the node")
	}
	eventually := func(what string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out: %s", what)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	eventually("the first refresh", func() bool { _, ok := h.Resource("docker:i_test/container/sonarr"); return ok })
	s, ins := f.container("sonarr")
	f.update(func() {
		s["State"], s["Status"] = "exited", "Exited (0) 1 second ago"
		ins["State"].(map[string]any)["Status"] = "exited"
	})
	f.events <- `{"Type":"container","Action":"die","Actor":{"ID":"` + s["Id"].(string) + `"}}`
	eventually("the event's refresh", func() bool {
		r, _ := h.Resource("docker:i_test/container/sonarr")
		return r.State == "exited"
	})
	cancel()
	<-done
}

func TestNodeEndpointConfig(t *testing.T) {
	for cfg, ok := range map[string]bool{
		`{"endpoint": "node://local"}`:                            true,
		`{"endpoint": "node://n_abc123"}`:                         true,
		`{"endpoint": "node://"}`:                                 false,
		`{"endpoint": "node://bad name"}`:                         false,
		`{"endpoint": "node://local", "tls": {"mode": "verify"}}`: false,
		`{"endpoint": "node://../etc"}`:                           false,
	} {
		var m map[string]any
		json.Unmarshal([]byte(cfg), &m)
		errs := integrationtest.Validate(Integration{}, m, nil)
		if (len(errs) == 0) != ok {
			t.Errorf("%s: %v", cfg, errs)
		}
	}
	// A hub without nodes can't connect a node endpoint.
	_, err := Integration{}.Connect(t.Context(), integration.Config{Values: map[string]any{"endpoint": "node://local"}}, integration.Deps{})
	if err == nil || !strings.Contains(err.Error(), "chadash-node") {
		t.Fatalf("connect without nodes = %v", err)
	}
	// Only node instances have actions.
	f := newFake(t, raw)
	h := integrationtest.New(t)
	tcp := h.Connect(Integration{}, map[string]any{"endpoint": f.serve()}, nil)
	if defs := tcp.(integration.Actor).Actions(); len(defs) != 0 {
		t.Fatalf("a socket endpoint offers %d actions", len(defs))
	}
}

// actionEnv runs the instance's actions on a real engine.
type actionEnv struct {
	t    *testing.T
	eng  *action.Engine
	node *fakeNode
	h    *integrationtest.Harness
}

var owner = action.Actor{Kind: action.KindUser, ID: "u_1", Name: "owner", Role: "owner", Via: "web", Session: "s_1"}

func newActionEnv(t *testing.T) *actionEnv {
	t.Helper()
	f := newFake(t, raw)
	h := integrationtest.New(t)
	n := newFakeNode(t, f)
	h.Nodes = n
	inst := h.Connect(Integration{}, map[string]any{"endpoint": "node://local"}, nil)
	syncOK(t, inst)
	st, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "chadash.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	reg := action.NewRegistry()
	if err := reg.Register("docker:i_test", integration.ActionDefs(inst.(integration.Actor))...); err != nil {
		t.Fatal(err)
	}
	eng, err := action.New(t.Context(), action.Options{Store: st, Audit: audit.New(st), Registry: reg, Policy: action.AllowAll{}, Graph: h.Set.Graph})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(eng.Close)
	return &actionEnv{t: t, eng: eng, node: n, h: h}
}

func (e *actionEnv) plan(act, target string, params string) (*action.Plan, error) {
	req := action.PlanRequest{Action: act, Target: target}
	if params != "" {
		req.Params = json.RawMessage(params)
	}
	return e.eng.Plan(e.t.Context(), owner, req)
}

func (e *actionEnv) run(p *action.Plan, typed string) action.Task {
	e.t.Helper()
	task, err := e.eng.Approve(e.t.Context(), owner, p.ID, action.ApproveRequest{Hash: p.Hash, Typed: typed}, "")
	if err != nil {
		e.t.Fatalf("approve %s: %v", p.Action, err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for !task.Finished() {
		if time.Now().After(deadline) {
			e.t.Fatalf("task %s never finished: %+v", p.Action, task)
		}
		time.Sleep(20 * time.Millisecond)
		task, _ = e.eng.Task(e.t.Context(), owner, task.ID)
	}
	return task
}

func TestDockerActionsThroughTheNode(t *testing.T) {
	e := newActionEnv(t)
	sonarr := "docker:i_test/container/sonarr"

	// Start on a running container is refused up front.
	if _, err := e.plan("docker.container.start", sonarr, ""); !isRefusal(err, "precondition") {
		t.Fatalf("start a running container = %v", err)
	}
	// Stop: T1 with an undo, verified through the node.
	p, err := e.plan("docker.container.stop", sonarr, "")
	if err != nil {
		t.Fatal(err)
	}
	if p.Tier != action.T1 || p.Confirm != "undo" || p.Undo == nil || p.Undo.Action != "docker.container.start" || len(p.Steps) != 2 || !strings.Contains(p.Steps[0].Label, "chadash-node local") {
		t.Fatalf("plan %+v", p)
	}
	if task := e.run(p, ""); task.State != action.StateSucceeded {
		t.Fatalf("stop: %+v", task)
	}
	if v := e.node.verbs(); !slices.Contains(v, "container.stop") || !slices.Contains(v, "container.can") {
		t.Fatalf("verbs %v", v)
	}
	// Restart and recreate are T2 with the graph's blast radius.
	p, err = e.plan("docker.container.restart", sonarr, "")
	if err != nil || p.Tier != action.T2 || p.Confirm != "hold" {
		t.Fatalf("restart plan %+v %v", p, err)
	}
	if task := e.run(p, ""); task.State != action.StateSucceeded {
		t.Fatalf("restart: %+v", task)
	}
	// Recreating immich's database lists immich_server, which depends on it
	// in compose.
	p, err = e.plan("docker.container.recreate", "docker:i_test/container/immich_postgres", "")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(p.Impact, func(im action.Impact) bool {
		return im.Resource == "docker:i_test/container/immich_server" && im.Via == "depends_on"
	}) {
		t.Errorf("recreate impact %+v lacks immich_server", p.Impact)
	}
	if task := e.run(p, ""); task.State != action.StateSucceeded {
		t.Fatalf("recreate: %+v", task)
	}
	// Pull and logs report on the task.
	p, _ = e.plan("docker.container.pull", sonarr, "")
	if task := e.run(p, ""); task.State != action.StateSucceeded || !slices.ContainsFunc(task.Log, func(l string) bool { return strings.Contains(l, "Recreate sonarr") }) {
		t.Fatalf("pull: %+v", task)
	}
	p, _ = e.plan("docker.container.logs", sonarr, "")
	if p.Tier != action.T0 || p.Confirm != "none" {
		t.Fatalf("logs plan %+v", p)
	}
	if task := e.run(p, ""); task.State != action.StateSucceeded || !slices.Contains(task.Log, "hello from the container") {
		t.Fatalf("logs: %+v", task)
	}

	// The node's policy refusing remove makes it unavailable, with its sentence.
	e.node.mu.Lock()
	e.node.deny["docker.remove"] = "this node's policy doesn't allow docker.remove"
	e.node.mu.Unlock()
	e.node.mu.Lock()
	e.node.state["sonarr"] = "exited"
	e.node.mu.Unlock()
	if _, err := e.plan("docker.container.remove", sonarr, ""); !isRefusal(err, "unavailable") || !strings.Contains(err.Error(), "docker.remove") {
		t.Fatalf("remove under a policy without it = %v", err)
	}
	e.node.mu.Lock()
	delete(e.node.deny, "docker.remove")
	e.node.mu.Unlock()
	p, err = e.plan("docker.container.remove", sonarr, `{"volumes": false}`)
	if err != nil {
		t.Fatal(err)
	}
	if p.Tier != action.T3 || p.Confirm != "type" || p.ConfirmText != "sonarr" {
		t.Fatalf("remove plan %+v", p)
	}
	if !slices.ContainsFunc(p.Steps, func(s action.Step) bool {
		return strings.HasPrefix(s.Label, "Keep the volume") || strings.Contains(s.Label, "can't be undone")
	}) {
		t.Fatalf("remove steps %+v", p.Steps)
	}
	if task := e.run(p, "sonarr"); task.State != action.StateSucceeded {
		t.Fatalf("remove: %+v", task)
	}

	// A node that isn't connected: unavailable, never a socket fallback.
	e.node.mu.Lock()
	e.node.offline = true
	e.node.mu.Unlock()
	if _, err := e.plan("docker.container.restart", "docker:i_test/container/plex", ""); !isRefusal(err, "unavailable") {
		t.Fatalf("offline node = %v", err)
	}
	// Another instance's container isn't ours.
	e.node.mu.Lock()
	e.node.offline = false
	e.node.mu.Unlock()
	if _, err := e.plan("docker.container.restart", "docker:i_other/container/sonarr", ""); !isRefusal(err, "not_found") {
		t.Fatalf("another instance's target = %v", err)
	}
}

func isRefusal(err error, code string) bool {
	r, ok := err.(*action.Refusal)
	return ok && r.Code == code
}
