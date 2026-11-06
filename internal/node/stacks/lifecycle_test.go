package stacks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"dash.cha.sh/internal/node"
	"dash.cha.sh/internal/stackproto"
)

// withVolume is v1 with a named volume, which down and remove keep.
var withVolume = strings.Replace(v1, "      interval: 3s\n", "      interval: 3s\n    volumes:\n      - data:/data\nvolumes:\n  data: {}\n", 1)

func (n *testNode) life(verb, name string) stackproto.LifecycleResult {
	n.t.Helper()
	var res stackproto.LifecycleResult
	n.must(verb, stackproto.LifecycleRequest{Name: name, By: "sam", Via: "web"}, &res)
	return res
}

func (n *testNode) summaryOf(name string) stackproto.Summary {
	n.t.Helper()
	var list stackproto.ListResponse
	n.must("stack.list", nil, &list)
	for _, s := range list.Stacks {
		if s.Name == name {
			return s
		}
	}
	n.t.Fatalf("%s isn't listed: %+v", name, list.Stacks)
	return stackproto.Summary{}
}

// Stop keeps the containers and reads as stopped, not drift; start brings
// them back; restart goes through the gate; down removes the containers
// and keeps the volume; start after down has nothing to start.
func TestLifecycleStopStartRestartDown(t *testing.T) {
	n := newTestNode(t)
	if res := n.deploy("media", withVolume, nil); res.Outcome != stackproto.OutcomeDeployed {
		t.Fatalf("%+v", res)
	}
	var st stackproto.Stack
	n.must("stack.get", stackproto.GetRequest{Stack: "media"}, &st)
	if !slices.Equal(st.Volumes, []string{"media_data"}) {
		t.Fatalf("stack.get volumes %v", st.Volumes)
	}
	head := st.Head.Commit
	n.docker.Reset()

	res := n.life("stack.stop", "media")
	if res.Name != "media" || res.Status != stackproto.StatusStopped || len(res.Services) != 1 || res.Services[0].State != "exited" {
		t.Fatalf("stop: %+v", res)
	}
	if s := n.summaryOf("media"); s.Status != stackproto.StatusStopped || s.Drift {
		t.Errorf("a stopped stack is stopped, not drift: %+v", s)
	}
	n.must("stack.get", stackproto.GetRequest{Stack: "media"}, &st)
	if st.Status != stackproto.StatusStopped || st.Drift || len(st.Containers) != 1 {
		t.Errorf("get after stop: %+v", st.Summary)
	}

	if res = n.life("stack.start", "media"); res.Status != stackproto.StatusRunning || res.Services[0].Health != "healthy" {
		t.Fatalf("start: %+v", res)
	}
	if res = n.life("stack.restart", "media"); res.Healthy == nil || !*res.Healthy || len(res.Unhealthy) != 0 || res.Status != stackproto.StatusRunning {
		t.Fatalf("restart: %+v", res)
	}
	if n.docker.Called("up ") != 0 || n.docker.Called("down ") != 0 {
		t.Error("stop, start and restart never recreate")
	}

	res = n.life("stack.down", "media")
	if res.Status != stackproto.StatusDown || len(res.Services) != 0 || !slices.Equal(res.VolumesKept, []string{"media_data"}) || len(res.VolumesRemoved) != 0 {
		t.Fatalf("down: %+v", res)
	}
	if n.docker.Called("rmvolumes") != 0 {
		t.Error("down never removes volumes")
	}
	if s := n.summaryOf("media"); s.Status != stackproto.StatusDown || s.Drift || s.Head.Commit != head {
		t.Errorf("down is a state; the stack stays managed at its commit: %+v", s)
	}
	if err := n.call("stack.start", stackproto.LifecycleRequest{Name: "media"}, nil); codeOf(err) != stackproto.CodeNotDeployed {
		t.Errorf("start after down: %v", err)
	}
	// The hub then deploys the current commit, which brings it back,
	// although the file is the one at HEAD.
	n.must("stack.get", stackproto.GetRequest{Stack: "media"}, &st)
	dep := n.deploy("media", st.Content, nil)
	if dep.Outcome != stackproto.OutcomeDeployed || len(n.docker.Running("media")) != 1 {
		t.Fatalf("deploying the current commit after down: %+v", dep)
	}
	if dep.Plan.Changed || dep.Plan.Services[0].Action != stackproto.ActionCreate {
		t.Errorf("the plan creates web, the file unchanged: %+v", dep.Plan)
	}
	// Once it runs, the same file is unchanged again.
	if res := n.deploy("media", st.Content, nil); res.Outcome != stackproto.OutcomeUnchanged {
		t.Errorf("redeploying a running stack's file: %s", res.Outcome)
	}
}

// Restart's gate reports; it never rolls back.
func TestRestartGateReportsOnly(t *testing.T) {
	n := newTestNode(t)
	bad := "services:\n  web:\n    image: nginx-unhealthy:1\n  db:\n    image: postgres:17\n"
	writeRootStack(t, n, "flaky", bad)
	n.must("stack.adopt", stackproto.AdoptRequest{Stack: "flaky"}, nil)
	startOutside(t, n.docker, "flaky", filepath.Join(n.root, "flaky", "compose.yaml"), bad)
	before := headCommit(t, n.root, 0).Hash

	res := n.life("stack.restart", "flaky")
	if res.Healthy == nil || *res.Healthy || !slices.Equal(res.Unhealthy, []string{"web"}) {
		t.Fatalf("restart of an unhealthy service: %+v", res)
	}
	if headCommit(t, n.root, 0).Hash != before || n.docker.Called("up ") != 0 || n.docker.Called("down ") != 0 {
		t.Error("restart's gate must not commit, recreate or take anything down")
	}
	if got := n.docker.Running("flaky"); got["web"] != "nginx-unhealthy:1" {
		t.Errorf("web runs %q", got["web"])
	}
}

func writeRootStack(t *testing.T, n *testNode, name, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(n.root, name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(n.root, name, "compose.yaml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Remove takes the stack down and its folder out of the root as a commit;
// the volume stays unless asked; a rollback to before the removal brings it
// back.
func TestRemoveAndRestore(t *testing.T) {
	n := newTestNode(t)
	n.deploy("media", withVolume, nil)
	if err := os.WriteFile(filepath.Join(n.root, "media", ".env"), []byte("A=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	deployed := headCommit(t, n.root, 0).Hash.String()

	var res stackproto.LifecycleResult
	n.must("stack.remove", stackproto.RemoveRequest{Name: "media", By: "sam", Via: "web"}, &res)
	if res.Status != stackproto.StatusDown || res.Commit == nil || res.Commit.Kind != stackproto.KindRemove || !strings.Contains(res.Commit.Subject, "by sam via web") {
		t.Fatalf("remove: %+v %+v", res, res.Commit)
	}
	if !slices.Equal(res.VolumesKept, []string{"media_data"}) || len(res.VolumesRemoved) != 0 || res.FilesKept != "" {
		t.Errorf("volumes: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(n.root, "media")); !os.IsNotExist(err) {
		t.Errorf("the folder should go: %v", err)
	}
	if c := headCommit(t, n.root, 0); trailer(c.Message, trailerKind) != stackproto.KindRemove || trailer(c.Message, trailerStack) != "media" {
		t.Errorf("the remove commit: %q", c.Message)
	}
	var list stackproto.ListResponse
	n.must("stack.list", nil, &list)
	if len(list.Stacks) != 0 {
		t.Errorf("a removed stack isn't listed: %+v", list.Stacks)
	}
	if n.docker.Called("down media") != 1 || len(n.docker.Running("media")) != 0 {
		t.Error("remove takes it down first")
	}
	// Removing it again: it's gone.
	if err := n.call("stack.remove", stackproto.RemoveRequest{Name: "media"}, nil); codeOf(err) != stackproto.CodeNotFound {
		t.Errorf("remove twice: %v", err)
	}

	// stack.get reads the version to restore; without At it's not found.
	if err := n.call("stack.get", stackproto.GetRequest{Stack: "media"}, nil); codeOf(err) != stackproto.CodeNotFound {
		t.Errorf("get of a removed stack: %v", err)
	}
	for _, at := range []string{deployed, res.Commit.Commit, deployed[:7]} {
		var old stackproto.Stack
		n.must("stack.get", stackproto.GetRequest{Stack: "media", At: at}, &old)
		if !old.Managed || old.Content != withVolume || old.Name != "media" || len(old.History) < 2 || old.History[0].Kind != stackproto.KindRemove {
			t.Errorf("get at %s of a removed stack: %+v", at[:7], old)
		}
	}
	if err := n.call("stack.get", stackproto.GetRequest{Stack: "media", At: strings.Repeat("0", 40)}, nil); codeOf(err) != stackproto.CodeNotFound {
		t.Errorf("get at an unknown commit: %v", err)
	}

	// Restore: roll back to the commit before the removal.
	var rb stackproto.DeployResult
	n.must("stack.rollback", stackproto.RollbackRequest{Stack: "media", Commit: deployed, By: "sam"}, &rb)
	if rb.Outcome != stackproto.OutcomeDeployed {
		t.Fatalf("restore: %+v", rb)
	}
	if b, err := os.ReadFile(filepath.Join(n.root, "media", "compose.yaml")); err != nil || !strings.Contains(string(b), "# the media stack") {
		t.Errorf("the file should be back: %v %s", err, b)
	}
	if s := n.summaryOf("media"); s.Status != stackproto.StatusRunning || s.Drift {
		t.Errorf("restored: %+v", s)
	}
	if h := n.history("media"); !strings.HasPrefix(h[0], "rollback ") || !strings.HasPrefix(h[1], "remove ") {
		t.Errorf("history keeps the removal: %q", h)
	}

	// With volumes: they go too.
	res = stackproto.LifecycleResult{}
	n.must("stack.remove", stackproto.RemoveRequest{Name: "media", Volumes: true}, &res)
	if !slices.Equal(res.VolumesRemoved, []string{"media_data"}) || len(res.VolumesKept) != 0 {
		t.Errorf("remove with volumes: %+v", res)
	}
	if vs, _ := n.docker.Volumes(t.Context(), "media"); len(vs) != 0 {
		t.Errorf("volumes left: %v", vs)
	}
	// The remove commit stands for the commit before it.
	rb = stackproto.DeployResult{}
	n.must("stack.rollback", stackproto.RollbackRequest{Stack: "media", Commit: res.Commit.Commit}, &rb)
	if rb.Outcome != stackproto.OutcomeDeployed || len(n.docker.Running("media")) != 1 {
		t.Fatalf("restore from the remove commit: %+v", rb)
	}
	if h := n.history("media"); !strings.HasPrefix(h[0], "rollback Restore media from ") {
		t.Errorf("restore subject: %q", h[0])
	}
}

// stack.get at a commit of a removed stack lists its secret references,
// for the hub to send with the restore.
func TestGetRemovedListsSecrets(t *testing.T) {
	n := newTestNode(t)
	withSecret := strings.Replace(v1, "    healthcheck:", "    environment:\n      PW: ${secret:pw}\n    healthcheck:", 1)
	if res := n.deploy("vault", withSecret, map[string]string{"pw": "hunter2-long"}); res.Outcome != stackproto.OutcomeDeployed {
		t.Fatalf("%+v", res)
	}
	var res stackproto.LifecycleResult
	n.must("stack.remove", stackproto.RemoveRequest{Name: "vault"}, &res)
	var old stackproto.Stack
	n.must("stack.get", stackproto.GetRequest{Stack: "vault", At: res.Commit.Commit}, &old)
	if !slices.Equal(old.Secrets, []string{"pw"}) || old.Content != withSecret {
		t.Errorf("secrets %v, content %q", old.Secrets, old.Content)
	}
	var rb stackproto.DeployResult
	n.must("stack.rollback", stackproto.RollbackRequest{Stack: "vault", Commit: res.Commit.Commit, Secrets: stackproto.Secrets{"pw": "hunter2-long"}}, &rb)
	if rb.Outcome != stackproto.OutcomeDeployed {
		t.Errorf("restore with its secret: %+v", rb)
	}
}

// A stack adopted in place: remove takes it down and stops managing it,
// and its file outside the root stays.
func TestRemoveExternalKeepsItsFiles(t *testing.T) {
	n := newTestNode(t)
	elsewhere := t.TempDir()
	extFile := filepath.Join(elsewhere, "compose.yaml")
	ext := "services:\n  dns:\n    image: pihole/pihole:2026.10\n"
	if err := os.WriteFile(extFile, []byte(ext), 0o644); err != nil {
		t.Fatal(err)
	}
	startOutside(t, n.docker, "pihole", extFile, ext)
	n.must("stack.adopt", stackproto.AdoptRequest{Stack: "pihole"}, nil)

	var res stackproto.LifecycleResult
	n.must("stack.remove", stackproto.RemoveRequest{Name: "pihole"}, &res)
	if res.FilesKept != extFile || res.Commit == nil || res.Commit.Kind != stackproto.KindRemove {
		t.Fatalf("remove external: %+v", res)
	}
	if b, err := os.ReadFile(extFile); err != nil || string(b) != ext {
		t.Errorf("the file outside the root must stay: %v", err)
	}
	if _, err := os.Stat(filepath.Join(n.root, shadowDir, "pihole")); !os.IsNotExist(err) {
		t.Errorf("its shadow copy goes: %v", err)
	}
	if len(n.docker.Running("pihole")) != 0 {
		t.Error("its containers go")
	}
	var list stackproto.ListResponse
	n.must("stack.list", nil, &list)
	if len(list.Stacks) != 0 {
		t.Errorf("listed after remove: %+v", list.Stacks)
	}
	// Its files were left in place, so it comes back by adopting, not by a
	// rollback that would put a copy in the root.
	err := n.call("stack.rollback", stackproto.RollbackRequest{Stack: "pihole", Commit: res.Commit.Commit}, nil)
	if codeOf(err) != stackproto.CodeNotFound || !strings.Contains(err.Error(), "Adopt it again") {
		t.Errorf("rollback of a removed external stack: %v", err)
	}
	if _, err := os.Stat(filepath.Join(n.root, "pihole")); !os.IsNotExist(err) {
		t.Error("nothing should be written to the root")
	}
}

// Each verb refuses what a deploy refuses: not managed, a stale head,
// busy, Cha Dash's own stack and a protected container; start with nothing
// to start; remove on a drifted file.
func TestLifecycleRefusals(t *testing.T) {
	protect := false
	n := newTestNode(t, func(o *Options) {
		o.Protected = []string{"chadash"}
		o.ProtectContainer = func(name, _ string) bool { return protect && name == "media-web-1" }
	})
	n.deploy("media", v1, nil)
	writeRootStack(t, n, "notes", "services:\n  app:\n    image: nginx:1\n")
	n.must("stack.adopt", stackproto.AdoptRequest{Stack: "notes"}, nil) // managed, never up
	writeRootStack(t, n, "loose", "services:\n  app:\n    image: nginx:1\n")
	n.docker.AddContainer("chadash", Container{Name: "chadash", Service: "chadash", Image: "ghcr.io/ban-red/chadash:0.4.0", State: "running"})
	n.docker.Reset()

	verbs := []string{stackproto.VerbStop, stackproto.VerbStart, stackproto.VerbRestart, stackproto.VerbDown, stackproto.VerbRemove}
	call := func(verb, name, head string) error {
		if verb == stackproto.VerbRemove {
			return n.call(verb, stackproto.RemoveRequest{Name: name, ExpectHead: head}, nil)
		}
		return n.call(verb, stackproto.LifecycleRequest{Name: name, ExpectHead: head}, nil)
	}
	for _, v := range verbs {
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
		if err := call(v, "media", strings.Repeat("a", 40)); codeOf(err) != stackproto.CodeConflict {
			t.Errorf("%s with a stale head: %v", v, err)
		}
		unlock, err := n.m.lock("media")
		if err != nil {
			t.Fatal(err)
		}
		if err := call(v, "media", ""); codeOf(err) != stackproto.CodeBusy {
			t.Errorf("%s while busy: %v", v, err)
		}
		unlock()
		protect = true
		if err := call(v, "media", ""); codeOf(err) != stackproto.CodeProtected || !strings.Contains(err.Error(), "media-web-1") {
			t.Errorf("%s with a protected container: %v", v, err)
		}
		protect = false
	}
	for _, c := range []string{"stop ", "start ", "restart ", "down ", "rmvolumes"} {
		if n.docker.Called(c) != 0 {
			t.Errorf("a refused verb called Docker: %s", c)
		}
	}
	if err := n.call(stackproto.VerbStart, stackproto.LifecycleRequest{Name: "notes"}, nil); codeOf(err) != stackproto.CodeNotDeployed {
		t.Errorf("start of a stack that was never up: %v", err)
	}
	// The right head is fine.
	head := headCommit(t, n.root, 0).Hash.String()
	var st stackproto.Stack
	n.must("stack.get", stackproto.GetRequest{Stack: "media"}, &st)
	if err := n.call(stackproto.VerbStop, stackproto.LifecycleRequest{Name: "media", ExpectHead: st.Head.Commit}, nil); err != nil {
		t.Errorf("stop at its head: %v", err)
	}
	// Remove would lose edits made outside Cha Dash.
	if err := os.WriteFile(filepath.Join(n.root, "media", "compose.yaml"), []byte(v1+"# by hand\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := call(stackproto.VerbRemove, "media", ""); codeOf(err) != stackproto.CodeDrift {
		t.Errorf("remove of a drifted stack: %v", err)
	}
	if headCommit(t, n.root, 0).Hash.String() != head {
		t.Error("a refused remove must not commit")
	}
}

// The policy: stop, start, restart and down need stacks.deploy; remove
// needs stacks.remove, which is off by default.
func TestLifecyclePolicy(t *testing.T) {
	n := newTestNode(t)
	n.deploy("media", v1, nil)
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
	if node.DefaultPolicy().Allows(stackproto.PermRemove) {
		t.Error("stacks.remove is on by default")
	}
	for _, v := range []string{stackproto.VerbStop, stackproto.VerbStart, stackproto.VerbRestart, stackproto.VerbDown} {
		if err := dispatch([]string{"read", "adopt", "sync", "remove"}, v); err == nil || !strings.Contains(err.Error(), "doesn't allow stacks.deploy") {
			t.Errorf("%s without stacks.deploy: %v", v, err)
		}
	}
	if err := dispatch(node.DefaultPolicy().Stacks.Allow, stackproto.VerbRemove); err == nil || !strings.Contains(err.Error(), "doesn't allow stacks.remove") {
		t.Errorf("remove by default: %v", err)
	}
	if err := dispatch([]string{"read", "deploy"}, stackproto.VerbRemove); err == nil || !strings.Contains(err.Error(), "doesn't allow stacks.remove") {
		t.Errorf("remove with deploy only: %v", err)
	}
	for _, v := range []string{stackproto.VerbStop, stackproto.VerbStart, stackproto.VerbDown} {
		if err := dispatch(node.DefaultPolicy().Stacks.Allow, v); err != nil {
			t.Errorf("%s by default: %v", v, err)
		}
	}
	if err := dispatch([]string{"read", "remove"}, stackproto.VerbRemove); err != nil {
		t.Errorf("remove with stacks.remove: %v", err)
	}
	if _, err := os.Stat(filepath.Join(n.root, "media")); !os.IsNotExist(err) {
		t.Error("the allowed remove should have removed it")
	}
}
