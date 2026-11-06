//go:build engine

package stacks

import (
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"

	noderegistry "dash.cha.sh/internal/node/registry"
	"dash.cha.sh/internal/stackproto"
	"dash.cha.sh/internal/testengine"
)

// Engine tests for updating a stack (plan 16, wave 32b) and private
// registries: the Compose SDK on throwaway stacks, a throwaway registry
// (with htpasswd for the private one), in a real Docker Engine.

// updateNode is the module on the engine with the node's logins (from a
// Docker config file, filtered by allowed) and a registry checker that
// speaks plain http to the test registry. Every log line goes to logs.
func updateNode(t *testing.T, e *testengine.Engine, root, config string, allowed []string, logs *syncBuffer) *testNode {
	t.Helper()
	log := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	logins := noderegistry.NewLogins(config, allowed, log)
	sdk, err := NewSDK(e.Host)
	if err != nil {
		t.Fatal(err)
	}
	sdk.Logins = logins
	var mu sync.Mutex
	skew := time.Duration(0)
	checker := &noderegistry.Checker{
		Client: noderegistry.Client{Base: func(d string) string { return "http://" + d }, HTTP: noderegistry.NewHTTPClient(), Logins: logins},
		// Each check is a fresh one, past the minute a fresh check keeps.
		Now: func() time.Time {
			mu.Lock()
			defer mu.Unlock()
			skew += 2 * noderegistry.FreshAfter
			return time.Now().Add(skew)
		},
	}
	n := &testNode{t: t, root: root}
	m, err := New(Options{Root: n.root, Docker: sdk, Registry: checker, Logins: logins, Log: log,
		Wait: time.Minute, Soak: 3 * time.Second, RollbackSoak: 2 * time.Second, Poll: 500 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	n.m = m
	return n
}

// syncBuffer is a log sink the module's goroutines can share.
type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func webDBFile(web, db string) string {
	return "# the app stack\nservices:\n  web:\n    image: " + web + "\n  db:\n    image: " + db + "\n"
}

// engineIDs is each service's running container ID.
func engineIDs(t *testing.T, e *testengine.Engine, project string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, id := range projectContainers(t, e, project) {
		c := e.Inspect(id)
		out[c.Config.Labels["com.docker.compose.service"]] = id
	}
	return out
}

func (n *testNode) checkFresh(name string) map[string]stackproto.ImageMove {
	n.t.Helper()
	var res stackproto.UpdateCheck
	n.must(stackproto.VerbUpdateCheck, stackproto.UpdateCheckRequest{Name: name, Fresh: true}, &res)
	out := map[string]stackproto.ImageMove{}
	for _, mv := range res.Services {
		out[mv.Service] = mv
	}
	return out
}

// A stack on a tag: a new image pushed to the tag shows in the check, the
// update recreates only that service on it, a broken one rolls back to the
// digest that ran, and a rollback to the commit before the update brings
// the first digest back.
func TestEngineStackUpdate(t *testing.T) {
	e := testengine.New(t)
	reg := e.Registry()
	web, db := reg.Ref("web", "1"), reg.Ref("db", "1")
	_, d1, _ := strings.Cut(e.Push(reg, web, testengine.Healthy, "1"), "@")
	e.Push(reg, db, testengine.Healthy, "db1")
	var logs syncBuffer
	n := updateNode(t, e, t.TempDir(), "", nil, &logs)
	stack := e.Project("app")
	if res := n.deploy(stack, webDBFile(web, db), nil); res.Outcome != stackproto.OutcomeDeployed {
		t.Fatalf("deploy: %s: %s", res.Outcome, res.Reason)
	}
	deployed := headCommit(t, n.root, 0).Hash.String()

	got := n.checkFresh(stack)
	if mv := got["web"]; mv.Moved || mv.Current != d1 || mv.Latest != d1 {
		t.Fatalf("web before a push: %+v", mv)
	}
	if res := n.update(stackproto.UpdateRequest{Name: stack}); res.Outcome != stackproto.OutcomeUnchanged || headCommit(t, n.root, 0).Hash.String() != deployed {
		t.Fatalf("nothing pushed: %s: %s", res.Outcome, res.Reason)
	}

	_, d2, _ := strings.Cut(e.Push(reg, web, testengine.Healthy, "2"), "@")
	got = n.checkFresh(stack)
	if mv := got["web"]; !mv.Moved || mv.Current != d1 || mv.Latest != d2 {
		t.Fatalf("web after a push: %+v", mv)
	}
	if mv := got["db"]; mv.Moved {
		t.Fatalf("db: %+v", mv)
	}
	ids := engineIDs(t, e, stack)
	res := n.update(stackproto.UpdateRequest{Name: stack})
	if res.Outcome != stackproto.OutcomeDeployed || len(res.Plan.Services) != 1 || res.Plan.Services[0].From != d1 || res.Plan.Services[0].To != d2 {
		t.Fatalf("update: %s: %s %+v", res.Outcome, res.Reason, res.Plan)
	}
	after := engineIDs(t, e, stack)
	if after["db"] != ids["db"] || after["web"] == ids["web"] {
		t.Errorf("only web is recreated: %v → %v", ids, after)
	}
	if mv := n.checkFresh(stack)["web"]; mv.Current != d2 || mv.Moved {
		t.Errorf("web after the update: %+v", mv)
	}

	// A broken image on the tag: rolled back to d2, pinned.
	e.Push(reg, web, testengine.NeverHealthy, "bad")
	ids = engineIDs(t, e, stack)
	res = n.update(stackproto.UpdateRequest{Name: stack})
	if res.Outcome != stackproto.OutcomeRolledBack {
		t.Fatalf("a broken image: %s: %s", res.Outcome, res.Reason)
	}
	if got := running(t, e, stack)["web"]; got != web+"@"+d2 {
		t.Errorf("after the rollback web runs %q, want %s pinned", got, d2)
	}
	if after := engineIDs(t, e, stack); after["db"] != ids["db"] {
		t.Error("the rollback recreated db")
	}

	// Back to before the first update: d1.
	var rb stackproto.DeployResult
	n.must(stackproto.VerbRollback, stackproto.RollbackRequest{Stack: stack, Commit: deployed, By: "sam"}, &rb)
	if rb.Outcome != stackproto.OutcomeDeployed {
		t.Fatalf("rollback: %s: %s", rb.Outcome, rb.Reason)
	}
	if got := running(t, e, stack)["web"]; got != web+"@"+d1 {
		t.Errorf("after the rollback to %s web runs %q, want %s", deployed[:7], got, d1)
	}
}

// A stack on a registry behind htpasswd: with no login the deploy is
// blocked at the dry run, saying so, and a login the policy doesn't list is
// never sent; with the login listed it deploys, the check logs in, and the
// update pulls. The password is in no reply, progress line, error, log
// line or commit.
func TestEnginePrivateRegistry(t *testing.T) {
	const user, password = "robot", "fake-pw-not-real-0000"
	e := testengine.New(t)
	reg := e.Registry(testengine.WithLogin(user, password))
	web, other := reg.Ref("web", "1"), reg.Ref("web", "other")
	_, d1, _ := strings.Cut(e.Push(reg, web, testengine.Healthy, "1"), "@")
	e.Push(reg, other, testengine.Healthy, "other")
	auth := base64.StdEncoding.EncodeToString([]byte(user + ":" + password))
	config := filepath.Join(t.TempDir(), "docker-config.json")
	b, _ := json.Marshal(map[string]any{"auths": map[string]any{reg.Host: map[string]string{"auth": auth}}})
	if err := os.WriteFile(config, b, 0o600); err != nil {
		t.Fatal(err)
	}
	file := "services:\n  web:\n    image: " + web + "\n"
	var seen []string // every reply and error
	keep := func(v any, err error) {
		j, _ := json.Marshal(v)
		seen = append(seen, string(j))
		if err != nil {
			seen = append(seen, err.Error())
		}
	}

	// Listed: it deploys and checks with the login.
	var logs syncBuffer
	root := t.TempDir()
	n := updateNode(t, e, root, config, []string{reg.Host}, &logs)
	stack := e.Project("private")
	res := n.deploy(stack, file, nil)
	keep(res, nil)
	if res.Outcome != stackproto.OutcomeDeployed {
		t.Fatalf("with the login: %s: %s", res.Outcome, res.Reason)
	}
	mv := n.checkFresh(stack)["web"]
	keep(mv, nil)
	if mv.Moved || mv.Latest != d1 || mv.Login != stackproto.LoginUsed {
		t.Fatalf("check with the login: %+v", mv)
	}

	// The config has the login, but images.registries doesn't list it:
	// a new stack on a tag this host hasn't pulled is blocked at the dry
	// run, and the check says the registry needs a login.
	none := updateNode(t, e, root, config, []string{"registry.lab.example"}, &logs)
	want := reg.Host + " needs a login, and this node has none for it."
	res = none.deploy(e.Project("other"), "services:\n  web:\n    image: "+other+"\n", nil)
	keep(res, nil)
	if res.Outcome != stackproto.OutcomeBlocked || !strings.Contains(res.Reason, want) {
		t.Fatalf("without a login: %s: %s", res.Outcome, res.Reason)
	}
	mv = none.checkFresh(stack)["web"]
	keep(mv, nil)
	if mv.Latest != "" || mv.Reason != want || mv.Login != stackproto.LoginNeeded {
		t.Fatalf("check without the login: %+v", mv)
	}

	// A new image: the update pulls it with the login, and can't without.
	_, d2, _ := strings.Cut(e.Push(reg, web, testengine.Healthy, "2"), "@")
	var noneRes stackproto.DeployResult
	err := none.call(stackproto.VerbUpdate, stackproto.UpdateRequest{Name: stack}, &noneRes)
	keep(noneRes, err)
	if err != nil || noneRes.Outcome != stackproto.OutcomeBlocked || !strings.Contains(noneRes.Reason, want) {
		t.Fatalf("an update without the login: %v %s: %s", err, noneRes.Outcome, noneRes.Reason)
	}
	res = n.update(stackproto.UpdateRequest{Name: stack})
	keep(res, nil)
	if res.Outcome != stackproto.OutcomeDeployed || res.Plan.Services[0].To != d2 {
		t.Fatalf("update with the login: %s: %s", res.Outcome, res.Reason)
	}
	var st stackproto.Stack
	n.must(stackproto.VerbGet, stackproto.GetRequest{Stack: stack}, &st)
	keep(st, nil)
	for _, c := range st.History {
		full := commitMessage(t, n.root, c.Commit)
		seen = append(seen, full)
	}

	all := strings.Join(append(append(seen, none.progress...), n.progress...), "\n")
	for _, secret := range []string{password, auth} {
		if strings.Contains(all, secret) {
			t.Errorf("a reply, progress line or commit carries the login")
		}
		if strings.Contains(logs.String(), secret) {
			t.Errorf("a log line carries the login")
		}
	}
	if !strings.Contains(logs.String(), "registry.lab.example") && !strings.Contains(logs.String(), reg.Host) {
		t.Errorf("the log should name which registries the node logs in to: %s", logs.String())
	}
}

// commitMessage is a commit's whole message.
func commitMessage(t *testing.T, root, hash string) string {
	t.Helper()
	r, err := git.PlainOpen(root)
	if err != nil {
		t.Fatal(err)
	}
	c, err := r.CommitObject(plumbing.NewHash(hash))
	if err != nil {
		t.Fatal(err)
	}
	return c.Message
}
