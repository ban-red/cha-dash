package stacks

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"dash.cha.sh/internal/node"
	"dash.cha.sh/internal/stackproto"
)

// twoServices is a stack with two tags: web moves, db stays.
const twoServices = `# the media stack
services:
  web:
    image: nginx:1.27
    healthcheck:
      test: ["CMD", "true"]
  db:
    image: postgres:17
`

func (n *testNode) check(name string) map[string]stackproto.ImageMove {
	n.t.Helper()
	var res stackproto.UpdateCheck
	n.must(stackproto.VerbUpdateCheck, stackproto.UpdateCheckRequest{Name: name}, &res)
	out := map[string]stackproto.ImageMove{}
	for _, mv := range res.Services {
		out[mv.Service] = mv
	}
	return out
}

func (n *testNode) update(req stackproto.UpdateRequest) stackproto.DeployResult {
	n.t.Helper()
	if req.By == "" {
		req.By, req.Via, req.Ref = "sam", "web", "t_test"
	}
	var res stackproto.DeployResult
	n.must(stackproto.VerbUpdate, req, &res)
	return res
}

// containerIDs is each service's container ID.
func (n *testNode) containerIDs(name string) map[string]string {
	n.t.Helper()
	cs, _ := n.docker.Containers(n.t.Context(), name)
	out := map[string]string{}
	for _, c := range cs {
		out[c.Service] = c.ID
	}
	return out
}

// A tag that moves: the check says so, the update pulls the tags,
// recreates only the service that moved, and commits with no file change;
// a second update has nothing to do; a rollback to the commit before the
// update brings the old digest back.
func TestUpdate(t *testing.T) {
	n := newTestNode(t)
	if res := n.deploy("media", twoServices, nil); res.Outcome != stackproto.OutcomeDeployed {
		t.Fatalf("deploy: %+v", res)
	}
	before := headCommit(t, n.root, 0)
	oldWeb := digestOf("nginx:1.27")

	got := n.check("media")
	if mv := got["web"]; mv.Moved || mv.Current == "" || mv.Current != mv.Latest || mv.Image != "nginx:1.27" {
		t.Fatalf("web before a push: %+v", mv)
	}
	if res := n.update(stackproto.UpdateRequest{Name: "media"}); res.Outcome != stackproto.OutcomeUnchanged || res.Commit != "" {
		t.Fatalf("nothing moved: %+v", res)
	}
	if headCommit(t, n.root, 0).Hash != before.Hash {
		t.Fatal("an update with nothing moved must not commit")
	}

	newWeb := n.docker.Push("nginx:1.27")
	n.docker.Reset()
	got = n.check("media")
	if mv := got["web"]; !mv.Moved || mv.Current != digestPart(oldWeb) || mv.Latest != digestPart(newWeb) {
		t.Fatalf("web after a push: %+v", mv)
	}
	if mv := got["db"]; mv.Moved || mv.Reason != "" {
		t.Errorf("db: %+v", mv)
	}
	if n.docker.Called("pull ") != 0 || n.docker.Called("up ") != 0 {
		t.Error("the check must pull and change nothing")
	}

	ids := n.containerIDs("media")
	n.docker.Reset()
	res := n.update(stackproto.UpdateRequest{Name: "media", ExpectHead: before.Hash.String(), Expect: map[string]string{"web": digestPart(newWeb)}})
	if res.Outcome != stackproto.OutcomeDeployed || res.Commit == "" {
		t.Fatalf("update: %+v", res)
	}
	if len(res.Plan.Services) != 1 || res.Plan.Services[0].Name != "web" || res.Plan.Services[0].From != digestPart(oldWeb) || res.Plan.Services[0].To != digestPart(newWeb) || res.Plan.Services[0].Action != stackproto.ActionRecreate || res.Plan.Changed || res.Plan.RecreateAll {
		t.Fatalf("the plan lists web alone, old and new digest: %+v", res.Plan)
	}
	if n.docker.Called("pull media db,web") != 1 {
		t.Errorf("the update pulls both tags: %d", n.docker.Called("pull media db,web"))
	}
	after := n.containerIDs("media")
	if after["web"] == ids["web"] || after["db"] != ids["db"] {
		t.Errorf("only web is recreated: %v → %v", ids, after)
	}
	refs, _ := n.docker.ImageRefs(t.Context(), "media")
	if refs["web"] != newWeb {
		t.Errorf("web runs %s, want %s", refs["web"], newWeb)
	}
	c := headCommit(t, n.root, 0)
	parent := headCommit(t, n.root, 1)
	if c.TreeHash != parent.TreeHash {
		t.Error("an update changes no file")
	}
	subject, _, _ := strings.Cut(c.Message, "\n")
	want := "Update media: web " + shortDigest(oldWeb) + " → " + shortDigest(newWeb) + " · by sam via web · t_test"
	if subject != want {
		t.Errorf("subject %q, want %q", subject, want)
	}
	if trailer(c.Message, trailerKind) != stackproto.KindUpdate || !slices.Equal(mapsKeys(previousImages(c.Message)), []string{"web"}) || previousImages(c.Message)["web"] != oldWeb {
		t.Errorf("trailers: %s", c.Message)
	}
	if h := n.history("media"); !strings.HasPrefix(h[0], "update Update media") {
		t.Errorf("history: %q", h)
	}

	// Again: nothing moved.
	if res := n.update(stackproto.UpdateRequest{Name: "media"}); res.Outcome != stackproto.OutcomeUnchanged || headCommit(t, n.root, 0).Hash != c.Hash {
		t.Fatalf("a second update: %+v", res)
	}

	// A rollback to the commit before the update runs the old digest, the
	// file pinned; db is left alone.
	ids = n.containerIDs("media")
	var rb stackproto.DeployResult
	n.must(stackproto.VerbRollback, stackproto.RollbackRequest{Stack: "media", Commit: before.Hash.String(), By: "sam"}, &rb)
	if rb.Outcome != stackproto.OutcomeDeployed {
		t.Fatalf("rollback: %+v", rb)
	}
	if got := n.docker.Running("media")["web"]; got != "nginx:1.27@"+digestPart(oldWeb) {
		t.Errorf("after the rollback web runs %q, want the old digest", got)
	}
	if after := n.containerIDs("media"); after["db"] != ids["db"] {
		t.Error("the rollback recreated db, which the update didn't touch")
	}
}

// A new image that fails the gate rolls the stack back to the digest it
// ran, pinned, as a rollback commit after the update's.
func TestUpdateRollsBack(t *testing.T) {
	n := newTestNode(t)
	n.deploy("media", twoServices, nil)
	oldWeb := digestOf("nginx:1.27")
	n.docker.Push("nginx:1.27", "unhealthy")
	res := n.update(stackproto.UpdateRequest{Name: "media"})
	if res.Outcome != stackproto.OutcomeRolledBack || res.RollbackCommit == "" || !strings.Contains(res.Reason, "unhealthy") {
		t.Fatalf("%+v", res)
	}
	if got := n.docker.Running("media")["web"]; got != "nginx:1.27@"+digestPart(oldWeb) {
		t.Errorf("after the rollback web runs %q", got)
	}
	if h := n.history("media"); !strings.HasPrefix(h[0], "rollback Roll back media") || !strings.Contains(h[0], "pinned web") || !strings.HasPrefix(h[1], "update Update media") {
		t.Errorf("history: %q", h)
	}
	disk, _ := os.ReadFile(filepath.Join(n.root, "media", "compose.yaml"))
	if !strings.Contains(string(disk), "nginx:1.27@"+digestPart(oldWeb)) || !strings.Contains(string(disk), "postgres:17\n") {
		t.Errorf("the file after the rollback pins web alone:\n%s", disk)
	}
}

// Services limits an update, Hold keeps a service on its image; what
// can't move says why in the check.
func TestUpdateServicesHoldAndReasons(t *testing.T) {
	n := newTestNode(t)
	content := twoServices + `  cache:
    image: redis@sha256:` + strings.Repeat("c", 64) + `
`
	n.deploy("media", content, nil)
	n.docker.Push("nginx:1.27")
	n.docker.Push("postgres:17")
	ids := n.containerIDs("media")

	res := n.update(stackproto.UpdateRequest{Name: "media", Services: []string{"web", "db"}, Hold: []string{"db"}})
	if res.Outcome != stackproto.OutcomeDeployed || len(res.Plan.Services) != 1 || res.Plan.Services[0].Name != "web" {
		t.Fatalf("%+v", res)
	}
	if after := n.containerIDs("media"); after["db"] != ids["db"] || after["cache"] != ids["cache"] {
		t.Errorf("held and pinned services are left alone: %v → %v", ids, after)
	}
	got := n.check("media")
	if mv := got["cache"]; mv.Moved || !strings.Contains(mv.Reason, "pinned by digest") {
		t.Errorf("cache: %+v", mv)
	}
	if mv := got["db"]; !mv.Moved {
		t.Errorf("db's tag moved and it still runs the old one: %+v", mv)
	}
	if err := n.call(stackproto.VerbUpdate, stackproto.UpdateRequest{Name: "media", Services: []string{"nope"}}, nil); codeOf(err) != stackproto.CodeInvalid {
		t.Errorf("an unknown service: %v", err)
	}

	// Built locally: no registry digest.
	n2 := newTestNode(t)
	n2.deploy("media", twoServices, nil)
	n2.docker.NoRepoDigests = true
	n2.docker.Reset()
	if mv := n2.check("media")["web"]; mv.Moved || !strings.Contains(mv.Reason, "built or loaded on this host") {
		t.Errorf("a local image: %+v", mv)
	}
	if res := n2.update(stackproto.UpdateRequest{Name: "media"}); res.Outcome != stackproto.OutcomeUnchanged || n2.docker.Called("pull ") != 0 {
		t.Errorf("a stack of local images: %+v", res)
	}
}

// A registry that wants a login the node hasn't got: the check says so in
// a sentence, and the update is blocked before anything changes.
func TestUpdateNeedsLogin(t *testing.T) {
	n := newTestNode(t)
	content := strings.Replace(twoServices, "nginx:1.27", "ghcr.io/acme/web:1", 1)
	n.deploy("media", content, nil)
	n.docker.LoginNeeded = []string{"ghcr.io"}
	n.docker.Reset()
	mv := n.check("media")["web"]
	if mv.Moved || mv.Reason != "ghcr.io needs a login, and this node has none for it." || mv.Login != stackproto.LoginNeeded {
		t.Fatalf("check: %+v", mv)
	}
	before := headCommit(t, n.root, 0).Hash
	res := n.update(stackproto.UpdateRequest{Name: "media"})
	if res.Outcome != stackproto.OutcomeBlocked || !strings.Contains(res.Reason, "ghcr.io needs a login, and this node has none for it.") {
		t.Fatalf("update: %+v", res)
	}
	if headCommit(t, n.root, 0).Hash != before || n.docker.Called("up ") != 0 {
		t.Error("a blocked update must not commit or recreate")
	}
	// A pull that fails another way is blocked too.
	n.docker.LoginNeeded = nil
	n.docker.FailPull = errors.New("toomanyrequests: rate limit")
	if res := n.update(stackproto.UpdateRequest{Name: "media"}); res.Outcome != stackproto.OutcomeBlocked || !strings.Contains(res.Reason, "Pulling failed") {
		t.Fatalf("a failed pull: %+v", res)
	}
}

// The file loads with the secrets the request carries, and their values
// stay out of what the update returns and streams.
func TestUpdateSecrets(t *testing.T) {
	n := newTestNode(t)
	content := twoServices + "    environment:\n      POSTGRES_PASSWORD: ${secret:db_pw}\n"
	secret := map[string]string{"db_pw": "s3cret-value-1234"}
	if res := n.deploy("media", content, secret); res.Outcome != stackproto.OutcomeDeployed {
		t.Fatalf("%+v", res)
	}
	n.docker.Push("postgres:17")
	if err := n.call(stackproto.VerbUpdate, stackproto.UpdateRequest{Name: "media"}, nil); codeOf(err) != stackproto.CodeInvalid {
		t.Errorf("an update without the secret: %v", err)
	}
	n.progress = nil
	res := n.update(stackproto.UpdateRequest{Name: "media", Secrets: secret})
	if res.Outcome != stackproto.OutcomeDeployed || res.Plan.Services[0].Name != "db" {
		t.Fatalf("%+v", res)
	}
	if got := n.docker.LastEnv()["db"]["POSTGRES_PASSWORD"]; got != "s3cret-value-1234" {
		t.Errorf("db's env: %q", got)
	}
	for _, p := range n.progress {
		if strings.Contains(p, "s3cret-value-1234") {
			t.Fatalf("progress carries the secret: %s", p)
		}
	}
}

// Both verbs refuse what a deploy refuses: a bad name, not managed, Cha
// Dash's own stack, a file edited outside Cha Dash; the update a stale
// head, busy, a protected container and a stack with nothing running.
func TestUpdateRefusals(t *testing.T) {
	protect := false
	n := newTestNode(t, func(o *Options) {
		o.Protected = []string{"chadash"}
		o.ProtectContainer = func(name, _ string) bool { return protect && name == "media-web-1" }
	})
	n.deploy("media", twoServices, nil)
	writeRootStack(t, n, "notes", "services:\n  app:\n    image: nginx:1\n")
	n.must("stack.adopt", stackproto.AdoptRequest{Stack: "notes"}, nil) // managed, never up
	writeRootStack(t, n, "loose", "services:\n  app:\n    image: nginx:1\n")
	n.docker.AddContainer("chadash", Container{Name: "chadash", Service: "chadash", Image: "ghcr.io/ban-red/chadash:0.4.0", State: "running"})
	n.docker.Push("nginx:1.27")
	n.docker.Reset()

	call := func(verb, name, head string) error {
		if verb == stackproto.VerbUpdateCheck {
			return n.call(verb, stackproto.UpdateCheckRequest{Name: name}, nil)
		}
		return n.call(verb, stackproto.UpdateRequest{Name: name, ExpectHead: head}, nil)
	}
	for _, v := range []string{stackproto.VerbUpdateCheck, stackproto.VerbUpdate} {
		for name, want := range map[string]string{
			"nothing-here": stackproto.CodeNotFound,
			"loose":        stackproto.CodeNotManaged,
			"chadash":      stackproto.CodeProtected,
			"Bad Name":     stackproto.CodeInvalid,
		} {
			if err := call(v, name, ""); codeOf(err) != want {
				t.Errorf("%s %s: %v, want %s", v, name, err, want)
			}
		}
	}
	if err := call(stackproto.VerbUpdate, "media", strings.Repeat("a", 40)); codeOf(err) != stackproto.CodeConflict {
		t.Errorf("a stale head: %v", err)
	}
	unlock, err := n.m.lock("media")
	if err != nil {
		t.Fatal(err)
	}
	if err := call(stackproto.VerbUpdate, "media", ""); codeOf(err) != stackproto.CodeBusy {
		t.Errorf("busy: %v", err)
	}
	unlock()
	protect = true
	if err := call(stackproto.VerbUpdate, "media", ""); codeOf(err) != stackproto.CodeProtected || !strings.Contains(err.Error(), "media-web-1") {
		t.Errorf("a protected container: %v", err)
	}
	protect = false
	if err := call(stackproto.VerbUpdate, "notes", ""); codeOf(err) != stackproto.CodeNotDeployed {
		t.Errorf("a stack never up: %v", err)
	}
	if n.docker.Called("up ") != 0 || n.docker.Called("pull ") != 1 {
		t.Errorf("only the protected one got as far as the pull: up %d, pull %d", n.docker.Called("up "), n.docker.Called("pull "))
	}
	// An edited file: the update would run the committed one.
	if err := os.WriteFile(filepath.Join(n.root, "media", "compose.yaml"), []byte(twoServices+"# by hand\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{stackproto.VerbUpdateCheck, stackproto.VerbUpdate} {
		if err := call(v, "media", ""); codeOf(err) != stackproto.CodeDrift {
			t.Errorf("%s on a drifted file: %v", v, err)
		}
	}
}

// Both verbs need stacks.deploy: without it the dispatcher refuses them
// before the module runs.
func TestUpdatePolicy(t *testing.T) {
	n := newTestNode(t)
	n.deploy("media", twoServices, nil)
	n.docker.Reset()
	dispatch := func(allow []string, verb string) error {
		p := node.DefaultPolicy()
		p.Stacks.Allow = allow
		d, err := node.NewDispatcher(p, n.m)
		if err != nil {
			t.Fatal(err)
		}
		_, err = d.Call(t.Context(), verb, json.RawMessage(`{"name":"media"}`), nil)
		return err
	}
	for _, v := range []string{stackproto.VerbUpdateCheck, stackproto.VerbUpdate} {
		if err := dispatch([]string{"read", "adopt", "sync", "remove", "selfupdate"}, v); err == nil || !strings.Contains(err.Error(), "doesn't allow stacks.deploy") {
			t.Errorf("%s without stacks.deploy: %v", v, err)
		}
		if err := dispatch(node.DefaultPolicy().Stacks.Allow, v); err != nil {
			t.Errorf("%s by default: %v", v, err)
		}
	}
	if len(n.docker.Asked()) != 2 || n.docker.Called("pull ") != 1 { // one check of two tags, one update
		t.Errorf("only the allowed calls reached the registry and Docker: asked %v, pulls %d", n.docker.Asked(), n.docker.Called("pull "))
	}
}
