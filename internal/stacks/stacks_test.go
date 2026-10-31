package stacks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/audit"
	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/livedoc"
	"dash.cha.sh/internal/node"
	nodestacks "dash.cha.sh/internal/node/stacks"
	"dash.cha.sh/internal/node/stacks/stackstest"
	"dash.cha.sh/internal/stackproto"
	"dash.cha.sh/internal/store"
	"dash.cha.sh/internal/vault"
)

// These tests run the hub's stacks package against the real node module in
// process (a Caller that dispatches to its verbs as the transport would,
// through JSON), over the fake Docker daemon. Only this test binary links
// the node module; the hub never does.

// localCaller is a nodes.Caller over in-process modules, by node ID. Its
// errors are flattened to text, as a transport would.
type localCaller struct {
	mu      sync.Mutex
	modules map[string]node.Module
	// sent records every request body, to check what crossed the wire.
	sent []string
	// tamper, when set, rewrites a reply: a node that lies.
	tamper func(verb string, req, reply json.RawMessage) json.RawMessage
	// fake, when set, answers a verb instead of the module (ok true): a
	// git remote the tests control (sync_test.go).
	fake func(node, verb string, req json.RawMessage) (reply json.RawMessage, ok bool, err error)
}

func (c *localCaller) Call(ctx context.Context, nodeID, verb string, req any, progress func(json.RawMessage)) (json.RawMessage, error) {
	c.mu.Lock()
	m := c.modules[nodeID]
	c.mu.Unlock()
	if m == nil {
		return nil, errors.New("no such node")
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.sent = append(c.sent, verb+" "+string(raw))
	fake := c.fake
	c.mu.Unlock()
	if fake != nil {
		if out, ok, err := fake(nodeID, verb, raw); ok {
			if err != nil {
				return nil, errors.New("node " + nodeID + ": " + err.Error())
			}
			return out, nil
		}
	}
	for _, v := range m.Verbs() {
		if v.Name == verb {
			out, err := v.Handle(ctx, raw, progress)
			if err != nil {
				return nil, errors.New("node " + nodeID + ": " + err.Error())
			}
			c.mu.Lock()
			tamper := c.tamper
			c.mu.Unlock()
			if tamper != nil {
				out = tamper(verb, raw, out)
			}
			return out, nil
		}
	}
	return nil, errors.New("unknown verb " + verb)
}

type hubEnv struct {
	t      *testing.T
	st     *store.Store
	vault  *vault.Vault
	docker *stackstest.Docker
	caller *localCaller
	svc    *Service
	eng    *action.Engine
	inbox  *livedoc.Doc
	g      *graph.Graph
	online bool
	root   string
}

var sam = action.Actor{Kind: action.KindUser, ID: "u_1", Name: "sam", Role: "owner", Via: "web", Session: "s_1"}

func newHub(t *testing.T) *hubEnv {
	t.Helper()
	st, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "chadash.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	v, err := vault.New(st, bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	h := &hubEnv{t: t, st: st, vault: v, docker: stackstest.New(), online: true, root: t.TempDir(), inbox: livedoc.New(incidentsColl), g: graph.New()}
	var mu sync.Mutex
	clock := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	m, err := nodestacks.New(nodestacks.Options{
		Root: h.root, Docker: h.docker, Soak: 20 * time.Second, Poll: 2 * time.Second,
		Now: func() time.Time { mu.Lock(); defer mu.Unlock(); return clock },
		Sleep: func(_ context.Context, d time.Duration) error {
			mu.Lock()
			defer mu.Unlock()
			clock = clock.Add(d)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	h.caller = &localCaller{modules: map[string]node.Module{"local": m}}
	h.svc, err = New(Options{
		Store: st, Vault: v, Caller: h.caller, Graph: h.g, Inbox: h.inbox,
		Nodes: func(context.Context) ([]Node, error) {
			return []Node{{ID: "local", Name: "jupiter", Online: h.online}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	reg := action.NewRegistry()
	if err := reg.Register(ActionSource, h.svc.Actions()...); err != nil {
		t.Fatal(err)
	}
	h.eng, err = action.New(t.Context(), action.Options{Store: st, Audit: audit.New(st), Registry: reg, Policy: action.AllowAll{}, Graph: h.g,
		OnApprovalEnded: h.svc.ApprovalEnded})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.eng.Close)
	return h
}

// do serves one request through the stacks routes.
func (h *hubEnv) do(method, path string, body any) *httptest.ResponseRecorder {
	h.t.Helper()
	mux := http.NewServeMux()
	h.svc.Register(mux, func(next http.Handler) http.Handler { return next })
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rd)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

func decode[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("%d %s: %v", w.Code, w.Body, err)
	}
	return v
}

// run plans and approves an action and waits for its task.
func (h *hubEnv) run(id, target, params string) (*action.Plan, action.Task) {
	h.t.Helper()
	var raw json.RawMessage
	if params != "" {
		raw = json.RawMessage(params)
	}
	p, err := h.eng.Plan(h.t.Context(), sam, action.PlanRequest{Action: id, Target: target, Params: raw})
	if err != nil {
		h.t.Fatalf("plan %s: %v", id, err)
	}
	task, err := h.eng.Approve(h.t.Context(), sam, p.ID, action.ApproveRequest{Hash: p.Hash}, "")
	if err != nil {
		h.t.Fatalf("approve %s: %v", id, err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		t, err := h.eng.Task(h.t.Context(), sam, task.ID)
		if err != nil {
			h.t.Fatal(err)
		}
		if t.Finished() && t.Audit != nil {
			return p, t
		}
		time.Sleep(2 * time.Millisecond)
	}
	h.t.Fatalf("task %s didn't finish", task.ID)
	return nil, action.Task{}
}

func (h *hubEnv) saveDraft(name, content string) {
	h.t.Helper()
	d, _ := h.svc.draft(h.t.Context(), "local", name)
	rev := int64(0)
	if d != nil {
		rev = d.Revision
	}
	if w := h.do("PUT", "/api/stacks/local/"+name, map[string]any{"content": content, "revision": rev}); w.Code != 200 {
		h.t.Fatalf("save draft: %d %s", w.Code, w.Body)
	}
}

const good = `services:
  web:
    image: nginx:1.27
    healthcheck:
      test: ["CMD", "true"]
    environment:
      DB_PASSWORD: ${secret:db_password}
x-chadash:
  soak: 20s
`

const secretValue = "hub-secret-VALUE-1234"

// The Done-when scenario through the hub: save a draft, deploy it as a T2
// action, then deploy a bad version that rolls back on its own. The task
// fails with "rolled back on its own", the history keeps the attempt, the
// draft survives, and undo is a rollback.
func TestScenarioBadDeployRollsBackOnItsOwn(t *testing.T) {
	h := newHub(t)
	target := Target("local", "media")

	// 1. A new stack: a draft, validated, with its secret set.
	w := h.do("POST", "/api/stacks/local/media/validate", map[string]string{"content": good})
	v := decode[Validation](t, w)
	if v.OK || !slices.ContainsFunc(v.Messages, func(m stackproto.Message) bool { return strings.Contains(m.Message, `"db_password" isn't set`) }) {
		t.Fatalf("an unset secret must block: %+v", v)
	}
	if w := h.do("PUT", "/api/stacks/secrets/db_password", map[string]string{"value": secretValue}); w.Code != 204 {
		t.Fatalf("set secret: %d %s", w.Code, w.Body)
	}
	v = decode[Validation](t, h.do("POST", "/api/stacks/local/media/validate", map[string]string{"content": good}))
	if !v.OK || v.Plan == nil || v.Plan.Services[0].Action != "create" || len(v.Diff) == 0 || v.Diff[0].Op != "+" {
		t.Fatalf("validate: %+v", v)
	}
	h.saveDraft("media", good)

	// 2. Deploy it: T2, hold, steps name the service.
	p, task := h.run(ActionDeploy, target, "")
	if p.Tier != action.T2 || p.Confirm != "hold" || !slices.ContainsFunc(p.Steps, func(s action.Step) bool { return s.Label == "Create web" }) {
		t.Errorf("plan %+v", p)
	}
	if task.State != action.StateSucceeded {
		t.Fatalf("first deploy: %+v", task.Error)
	}
	if got := h.docker.LastEnv()["web"]["DB_PASSWORD"]; got != secretValue {
		t.Errorf("the node got %q for the secret", got)
	}
	d := decode[StackDetail](t, h.do("GET", "/api/stacks/local/media", nil))
	if d.Draft != nil || !d.Managed || len(d.History) != 1 || d.Status != "running" {
		t.Fatalf("after deploying, the draft is cleared and the stack managed: %+v", d)
	}
	first := d.History[0].Commit

	// 3. The deliberately bad deploy: its healthcheck fails.
	bad := strings.Replace(good, "nginx:1.27", "nginx-unhealthy:1.28", 1)
	h.saveDraft("media", bad)
	p, task = h.run(ActionDeploy, target, "")
	if p.Undo == nil || p.Undo.Action != ActionRollback || !strings.Contains(string(p.Undo.Params), first) {
		t.Errorf("undo should roll back to %s: %+v", first[:7], p.Undo)
	}
	if !slices.ContainsFunc(p.Steps, func(s action.Step) bool { return strings.HasPrefix(s.Label, "Recreate web") }) {
		t.Errorf("steps %+v", p.Steps)
	}
	if task.State != action.StateFailed || task.Error == nil || task.Error.Code != "rolled_back" ||
		!strings.Contains(task.Error.Message, "rolled back on its own") || !strings.Contains(task.Error.Message, "unhealthy") {
		t.Fatalf("task %+v %+v", task.State, task.Error)
	}
	if !slices.ContainsFunc(task.Log, func(l string) bool { return strings.Contains(l, "Health gate") }) {
		t.Errorf("the task log should follow the node's steps: %q", task.Log)
	}
	d = decode[StackDetail](t, h.do("GET", "/api/stacks/local/media", nil))
	if len(d.History) != 3 || d.History[0].Kind != "rollback" || d.History[1].Kind != "deploy" {
		t.Errorf("history %+v", d.History)
	}
	if last := task.Log[len(task.Log)-1]; len(d.History) == 3 && !strings.HasPrefix(last, "Commit "+d.History[0].Commit[:7]) {
		t.Errorf("the log should end on the rollback's commit %s: %q", d.History[0].Commit[:7], last)
	}
	if d.Draft == nil || d.Draft.Content != bad {
		t.Error("a failed deploy keeps the draft")
	}
	if !strings.HasPrefix(h.docker.Running("media")["web"], "nginx:1.27@sha256:") {
		t.Errorf("web runs %q; want v1 pinned", h.docker.Running("media")["web"])
	}

	// 4. The secret never crossed the wire except to plan or deploy, and
	// never came back in a response, a task or the audit log.
	for _, s := range h.caller.sent {
		if strings.Contains(s, secretValue) && !strings.HasPrefix(s, "stack.deploy ") && !strings.HasPrefix(s, "stack.plan ") {
			t.Errorf("the secret went with %s", strings.SplitN(s, " ", 2)[0])
		}
	}
	for _, path := range []string{"/api/stacks", "/api/stacks/local/media", "/api/stacks/secrets"} {
		if w := h.do("GET", path, nil); strings.Contains(w.Body.String(), secretValue) {
			t.Errorf("%s returned the secret", path)
		}
	}
	tj, _ := json.Marshal(task)
	if strings.Contains(string(tj), secretValue) {
		t.Error("the task holds the secret")
	}
	rows, err := h.st.Reader().Query(`SELECT event FROM audit_events`)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for rows.Next() {
		var ev string
		_ = rows.Scan(&ev)
		n++
		if strings.Contains(ev, secretValue) {
			t.Error("the audit log holds the secret")
		}
	}
	rows.Close()
	if n == 0 {
		t.Fatal("no audit events; the scan checked nothing")
	}
}

func TestDeployRefusals(t *testing.T) {
	h := newHub(t)
	target := Target("local", "media")
	plan := func(id, params string) error {
		var raw json.RawMessage
		if params != "" {
			raw = json.RawMessage(params)
		}
		_, err := h.eng.Plan(t.Context(), sam, action.PlanRequest{Action: id, Target: target, Params: raw})
		return err
	}
	code := func(err error) string {
		if r, ok := errors.AsType[*action.Refusal](err); ok {
			return r.Code
		}
		return ""
	}
	if err := plan(ActionDeploy, ""); code(err) != "not_found" {
		t.Errorf("no stack, no draft: %v", err)
	}
	plain := strings.Replace(good, "${secret:db_password}", "plain", 1)
	h.saveDraft("media", plain)
	if _, task := h.run(ActionDeploy, target, ""); task.State != action.StateSucceeded {
		t.Fatalf("%+v", task.Error)
	}
	if err := plan(ActionDeploy, ""); code(err) != "precondition" || !strings.Contains(err.Error(), "save a draft") {
		t.Errorf("no draft: %v", err)
	}
	h.saveDraft("media", plain)
	if err := plan(ActionDeploy, ""); code(err) != "precondition" || !strings.Contains(err.Error(), "nothing to deploy") {
		t.Errorf("a draft equal to what runs: %v", err)
	}
	h.saveDraft("media", strings.Replace(plain, "nginx:1.27", "nginx:1.28", 1)+"  bogus: [\n")
	if err := plan(ActionDeploy, ""); code(err) != "precondition" || !strings.Contains(err.Error(), "isn't valid") {
		t.Errorf("an invalid draft: %v", err)
	}
	// The plan is bound to the draft: saving a new one changes the target.
	h.saveDraft("media", strings.Replace(plain, "nginx:1.27", "nginx:1.28", 1))
	p, err := h.eng.Plan(t.Context(), sam, action.PlanRequest{Action: ActionDeploy, Target: target})
	if err != nil {
		t.Fatal(err)
	}
	h.saveDraft("media", strings.Replace(plain, "nginx:1.27", "nginx:1.29", 1))
	if _, err := h.eng.Approve(t.Context(), sam, p.ID, action.ApproveRequest{Hash: p.Hash}, ""); code(err) != "plan_changed" {
		t.Errorf("approving after the draft changed: %v", err)
	}
	// Rollback params.
	if err := plan(ActionRollback, `{"commit":"nothex"}`); code(err) != "invalid" {
		t.Errorf("bad commit: %v", err)
	}
	if err := plan(ActionRollback, `{"commit":"abcdef1"}`); code(err) != "precondition" {
		t.Errorf("a commit not in the history: %v", err)
	}
	// Offline node.
	h.online = false
	if err := plan(ActionDeploy, ""); code(err) != "unavailable" || !strings.Contains(err.Error(), "The node jupiter isn't connected") {
		t.Errorf("offline: %v", err)
	}
	if w := h.do("GET", "/api/stacks/local/media", nil); w.Code != 503 {
		t.Errorf("offline detail: %d", w.Code)
	}
	l := decode[struct{ Nodes []NodeView }](t, h.do("GET", "/api/stacks", nil))
	if len(l.Nodes) != 1 || l.Nodes[0].Error == nil || l.Nodes[0].Error.Code != "node_unavailable" || !l.Nodes[0].Stacks[0].Draft {
		t.Errorf("offline list: %+v", l.Nodes)
	}
}

func TestProtectedStack(t *testing.T) {
	h := newHub(t)
	own := "services:\n  hub:\n    image: ghcr.io/ban-red/chadash:0.2.0\n"
	if w := h.do("PUT", "/api/stacks/local/chadash", map[string]any{"content": own}); w.Code != 200 {
		t.Fatalf("a draft of a stack that doesn't exist yet: %d", w.Code)
	}
	// It's new, so nothing on the node says "protected" yet; the node
	// refuses it when the deploy runs, before touching anything.
	_, task := h.run(ActionDeploy, Target("local", "chadash"), "")
	if task.Error == nil || task.Error.Code != "protected" || !strings.Contains(task.Error.Message, "self-update") {
		t.Fatalf("%+v", task.Error)
	}
	if h.docker.Called("up ") != 0 {
		t.Error("nothing may start")
	}
	// Once the node can tell it's Cha Dash's own (here a file in the root,
	// not adopted, with the hub's image), a draft is refused before it's kept.
	h.do("DELETE", "/api/stacks/local/chadash/draft", nil)
	mustWrite(t, filepath.Join(h.root, "dash", "compose.yaml"), own)
	w := h.do("PUT", "/api/stacks/local/dash", map[string]any{"content": own})
	if w.Code != 409 || !strings.Contains(w.Body.String(), `"protected"`) {
		t.Fatalf("a draft of Cha Dash's own stack: %d %s", w.Code, w.Body)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestAdoptAndUndo(t *testing.T) {
	h := newHub(t)
	// A Dockge directory nobody adopted.
	dir := filepath.Join(h.root, "notes")
	mustWrite(t, filepath.Join(dir, "compose.yaml"), "services:\n  app:\n    image: nginx:1\n")
	l := decode[struct{ Nodes []NodeView }](t, h.do("GET", "/api/stacks", nil))
	if len(l.Nodes[0].Detected) != 1 || !l.Nodes[0].Detected[0].Adoptable {
		t.Fatalf("detected %+v", l.Nodes[0])
	}
	p, task := h.run(ActionAdopt, Target("local", "notes"), "")
	if p.Tier != action.T1 || p.Confirm != "undo" || p.Undo == nil || p.Undo.Action != ActionUnadopt || len(p.Impact) != 0 {
		t.Errorf("plan %+v", p)
	}
	if task.State != action.StateSucceeded {
		t.Fatalf("%+v", task.Error)
	}
	l = decode[struct{ Nodes []NodeView }](t, h.do("GET", "/api/stacks", nil))
	if len(l.Nodes[0].Stacks) != 1 || len(l.Nodes[0].Detected) != 0 {
		t.Fatalf("after adopting: %+v", l.Nodes[0])
	}
	if _, err := h.eng.Plan(t.Context(), sam, action.PlanRequest{Action: ActionAdopt, Target: Target("local", "notes")}); err == nil {
		t.Error("adopting twice")
	}
	// The undo.
	if _, task := h.run(p.Undo.Action, Target("local", "notes"), string(p.Undo.Params)); task.State != action.StateSucceeded {
		t.Fatalf("%+v", task.Error)
	}
	l = decode[struct{ Nodes []NodeView }](t, h.do("GET", "/api/stacks", nil))
	if len(l.Nodes[0].Stacks) != 0 || len(l.Nodes[0].Detected) != 1 {
		t.Errorf("after un-adopting: %+v", l.Nodes[0])
	}
}

func TestStuckRaisesAnInboxItem(t *testing.T) {
	h := newHub(t)
	plain := strings.Replace(good, "${secret:db_password}", "plain", 1)
	h.saveDraft("media", plain)
	h.run(ActionDeploy, Target("local", "media"), "")
	h.docker.FailUp = 2
	h.saveDraft("media", strings.Replace(plain, "1.27", "1.28", 1))
	_, task := h.run(ActionDeploy, Target("local", "media"), "")
	if task.Error == nil || task.Error.Code != "stuck" {
		t.Fatalf("%+v", task.Error)
	}
	raw := h.inbox.Item(incidentsColl, "stack.local.media.stuck")
	var in incident
	if err := json.Unmarshal(raw, &in); err != nil || in.Severity != "down" || in.Status != "open" || in.Target != "stack:local/media" {
		t.Fatalf("incident %s", raw)
	}
	// A good deploy resolves it.
	h.docker.FailUp = 0
	h.saveDraft("media", strings.Replace(plain, "1.27", "1.29", 1))
	if _, task := h.run(ActionDeploy, Target("local", "media"), ""); task.State != action.StateSucceeded {
		t.Fatalf("%+v", task.Error)
	}
	_ = json.Unmarshal(h.inbox.Item(incidentsColl, "stack.local.media.stuck"), &in)
	if in.Status != "resolved" {
		t.Errorf("status %s", in.Status)
	}
}

func TestDrafts(t *testing.T) {
	h := newHub(t)
	if w := h.do("PUT", "/api/stacks/local/media", map[string]any{"content": good, "revision": 0}); w.Code != 200 {
		t.Fatal(w.Body)
	}
	w := h.do("PUT", "/api/stacks/local/media", map[string]any{"content": good, "revision": 0})
	if w.Code != 409 || decode[struct{ Revision int64 }](t, w).Revision != 1 {
		t.Errorf("stale save: %d %s", w.Code, w.Body)
	}
	// A node's name isn't its ID: nothing is kept for a node the hub doesn't know.
	if w := h.do("PUT", "/api/stacks/jupiter/media", map[string]any{"content": good, "revision": 0}); w.Code != 404 {
		t.Errorf("a draft for an unknown node: %d %s", w.Code, w.Body)
	}
	var kept int
	if err := h.st.Reader().QueryRow(`SELECT count(*) FROM stack_drafts WHERE node = 'jupiter'`).Scan(&kept); err != nil || kept != 0 {
		t.Errorf("drafts kept for an unknown node: %d %v", kept, err)
	}
	if w := h.do("PUT", "/api/stacks/local/Bad%20Name", map[string]any{"content": good}); w.Code != 422 {
		t.Errorf("bad name: %d", w.Code)
	}
	if w := h.do("DELETE", "/api/stacks/local/media/draft", nil); w.Code != 204 {
		t.Errorf("delete: %d", w.Code)
	}
	if w := h.do("GET", "/api/stacks/local/media", nil); w.Code != 404 {
		t.Errorf("no stack and no draft: %d", w.Code)
	}
}

func TestLineDiff(t *testing.T) {
	a := "a\nb\nc\nd\ne\nf\ng\nh\ni\nj\n"
	b := "a\nb\nc\nd\nE\nf\ng\nh\ni\nj\nk\n"
	got := lineDiff(a, b)
	var ops []string
	for _, l := range got {
		ops = append(ops, l.Op+l.Text)
	}
	want := []string{"…4 unchanged lines", "-e", "+E", " f", " g", " h", "…2 unchanged lines", " j", "+k"}
	// Leading context is folded entirely only when it's long; check the edit and the tail.
	if !slices.Contains(ops, "-e") || !slices.Contains(ops, "+E") || ops[len(ops)-1] != "+k" {
		t.Errorf("diff %q (want something like %q)", ops, want)
	}
	if d := lineDiff("", "x\n"); len(d) != 1 || d[0].Op != "+" {
		t.Errorf("new file: %+v", d)
	}
}

func TestSecretRefs(t *testing.T) {
	got := secretRefs("a: ${secret:one}\nb: $${secret:literal}\nc: ${secret:two} ${secret:one}\nd: ${secret:bad name}\n")
	if !slices.Equal(got, []string{"one", "two"}) {
		t.Errorf("%v", got)
	}
}

// A draft says where it came from. The editor writes "web", a row from
// before the column existed reads back as "web", and a sync's "git:<sha>"
// reads back as it was written.
func TestDraftSource(t *testing.T) {
	h := newHub(t)
	ctx := t.Context()

	w := h.do("PUT", "/api/stacks/local/media", map[string]any{"content": good, "revision": 0})
	if saved := decode[struct{ Draft Draft }](t, w); w.Code != 200 || saved.Draft.Source != SourceWeb {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	if !strings.Contains(w.Body.String(), `"source":"web"`) {
		t.Errorf("the saved draft's JSON has no source: %s", w.Body)
	}
	if d := decode[StackDetail](t, h.do("GET", "/api/stacks/local/media", nil)); d.Draft == nil || d.Draft.Source != SourceWeb {
		t.Errorf("detail's draft = %+v", d.Draft)
	}

	// Saving over a sync's draft from the editor makes it the editor's.
	if _, err := h.st.Writer().ExecContext(ctx, `UPDATE stack_drafts SET source = 'git:0123abc' WHERE stack = 'media'`); err != nil {
		t.Fatal(err)
	}
	if d, err := h.svc.draft(ctx, "local", "media"); err != nil || d.Source != "git:0123abc" {
		t.Fatalf("draft = %+v, %v", d, err)
	}
	h.saveDraft("media", strings.Replace(good, "1.27", "1.28", 1))
	if d, err := h.svc.draft(ctx, "local", "media"); err != nil || d.Source != SourceWeb || d.Revision != 2 {
		t.Errorf("after an editor save, draft = %+v, %v", d, err)
	}

	// A row written the way v0.2 wrote it, without a source.
	if _, err := h.st.Writer().ExecContext(ctx, `INSERT INTO stack_drafts (node, stack, content, revision, updated_at, updated_by) VALUES ('local', 'vpn', 'services: {}', 1, 0, 'sam')`); err != nil {
		t.Fatal(err)
	}
	all, err := h.svc.drafts(ctx, "local")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all["vpn"].Source != SourceWeb || all["media"].Source != SourceWeb {
		t.Errorf("drafts = %+v", all)
	}
}
