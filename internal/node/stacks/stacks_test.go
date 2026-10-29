package stacks

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"dash.cha.sh/internal/stackproto"
)

const v1 = `# the media stack
services:
  web:
    image: nginx:1.27   # pinned on purpose
    healthcheck:
      test: ["CMD", "true"]
      interval: 3s
x-chadash:
  soak: 20s
`

var (
	v2Good      = strings.Replace(v1, "nginx:1.27", "nginx:1.28", 1)
	v2Unhealthy = strings.Replace(v1, "nginx:1.27", "nginx-unhealthy:1.28", 1)
	v2Crash     = strings.Replace(v1, "nginx:1.27", "nginx-crashloop:1.28", 1)
	v2Missing   = strings.Replace(v1, "nginx:1.27", "nginx-missing:9.99", 1)
)

func TestValidate(t *testing.T) {
	n := newTestNode(t)
	cases := []struct {
		name, content string
		ok            bool
		want          string // a message that must appear
		line          int
	}{
		{"good", v1, true, "", 0},
		{"yaml", "services:\n  web:\n    image: [nginx\n", false, "doesn't parse", -1},
		{"no image", "services:\n  web:\n    restart: always\n", false, "neither an image nor a build context", 0},
		{"build only", "services:\n  web:\n    build: .\n", false, "builds its image", 2},
		{"bad secret name", "services:\n  web:\n    image: nginx:1\n    environment:\n      A: ${secret:bad name}\n", false, "isn't a valid secret name", 5},
		{"plain secret", "services:\n  db:\n    image: postgres:17\n    environment:\n      POSTGRES_PASSWORD: hunter2hunter2\n", true, "looks like a secret in plain text", 2},
		{"latest", "services:\n  web:\n    image: nginx\n", true, "has no tag", 2},
		{"no healthcheck", "services:\n  web:\n    image: nginx:1\n", true, "has no healthcheck", 2},
		{"meta", "services:\n  web:\n    image: nginx:1\nx-chadash:\n  soak: forever\n  colour: blue\n", true, "should be a duration", 5},
		{"empty", "  \n", false, "empty", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var res stackproto.ValidateResponse
			n.must("stack.validate", stackproto.ValidateRequest{Stack: "media", Content: c.content}, &res)
			if res.OK != c.ok {
				t.Errorf("ok = %v, want %v: %+v", res.OK, c.ok, res.Messages)
			}
			if c.want == "" {
				for _, m := range res.Messages {
					if m.Severity == stackproto.SeverityError {
						t.Errorf("unexpected error: %+v", m)
					}
				}
				return
			}
			i := slices.IndexFunc(res.Messages, func(m stackproto.Message) bool { return strings.Contains(m.Message, c.want) })
			if i < 0 {
				t.Fatalf("no message containing %q in %+v", c.want, res.Messages)
			}
			if c.line == -1 && res.Messages[i].Line == 0 {
				t.Error("a YAML error should carry its line")
			}
			if c.line > 0 && res.Messages[i].Line != c.line {
				t.Errorf("line = %d, want %d", res.Messages[i].Line, c.line)
			}
		})
	}
	var res stackproto.ValidateResponse
	n.must("stack.validate", stackproto.ValidateRequest{Stack: "media", Content: "services:\n  db:\n    image: postgres:17\n    environment:\n      PW: ${secret:db_password}\n      L: $${secret:literal}\n"}, &res)
	if !res.OK || !slices.Equal(res.Secrets, []string{"db_password"}) {
		t.Errorf("secrets = %v (ok %v), want [db_password]; $${…} is a literal", res.Secrets, res.OK)
	}
	if err := n.call("stack.validate", stackproto.ValidateRequest{Stack: "Not A Name", Content: v1}, nil); stackproto.ErrorFrom(err) == nil || stackproto.ErrorFrom(err).Code != stackproto.CodeInvalid {
		t.Errorf("a bad stack name: %v", err)
	}
}

// A secret's value is substituted inside the parsed value: it can't inject
// YAML or be interpolated again.
func TestSecretsSubstituteSafely(t *testing.T) {
	s := stackRef{name: "x", dir: t.TempDir(), file: "compose.yaml"}
	content := "services:\n  a:\n    image: nginx:1\n    environment:\n      PW: ${secret:pw}\n      HOME_DIR: ${HOME_DIR:-/srv}\n"
	evil := "pa$$word ${HOME_DIR}\n    privileged: true"
	p, err := load(context.Background(), s, content, loadOptions{secrets: map[string]string{"pw": evil}})
	if err != nil {
		t.Fatal(err)
	}
	if got := *p.Services["a"].Environment["PW"]; got != evil {
		t.Errorf("PW = %q, want the value verbatim", got)
	}
	if p.Services["a"].Privileged {
		t.Error("the secret's value injected YAML")
	}
	if _, err := load(context.Background(), s, content, loadOptions{secrets: map[string]string{}}); err == nil || !strings.Contains(err.Error(), `"pw" wasn't provided`) {
		t.Errorf("a missing secret: %v", err)
	}
}

func TestPlanFromHashes(t *testing.T) {
	n := newTestNode(t)
	file := filepath.Join(n.root, "media", "compose.yaml")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(v1), 0o644); err != nil {
		t.Fatal(err)
	}
	startOutside(t, n.docker, "media", file, v1)
	n.must("stack.adopt", stackproto.AdoptRequest{Stack: "media"}, nil)
	plan := func(content string) stackproto.Plan {
		var p stackproto.Plan
		n.must("stack.plan", stackproto.PlanRequest{Stack: "media", Content: content}, &p)
		return p
	}
	if p := plan(v1); p.Services[0].Action != stackproto.ActionUnchanged {
		t.Errorf("the running file: %+v; the hash should match the container's label", p.Services)
	}
	p := plan(v2Good)
	if s := p.Services[0]; s.Action != stackproto.ActionRecreate || !s.Pull || p.DowntimeS != downtimePerRecreate {
		t.Errorf("a new tag: %+v, downtime %v", p.Services, p.DowntimeS)
	}
	more := v1 + "" // add a service, keep web
	more = strings.Replace(more, "x-chadash:", "  whoami:\n    image: traefik/whoami:v1.11\nx-chadash:", 1)
	p = plan(more)
	if len(p.Services) != 2 || p.Services[0].Action != stackproto.ActionUnchanged || p.Services[1].Action != stackproto.ActionCreate {
		t.Errorf("adding a service: %+v", p.Services)
	}
	p = plan("services:\n  other:\n    image: nginx:1.27\n")
	actions := map[string]string{}
	for _, s := range p.Services {
		actions[s.Name] = s.Action
	}
	if actions["web"] != stackproto.ActionRemove || actions["other"] != stackproto.ActionCreate {
		t.Errorf("replacing a service: %v", actions)
	}
	// Labels from a Compose version that hashed differently.
	n.docker.SetHash("media", "web", "stamped-by-another-version")
	if p := plan(v1); !p.RecreateAll {
		t.Errorf("foreign hashes should say everything is recreated once: %+v", p)
	}
}

func TestPinImages(t *testing.T) {
	refs := map[string]string{"web": "nginx@sha256:aaaa", "db": "docker.io/library/postgres@sha256:bbbb", "app": "ghcr.io/x/app@sha256:cccc"}
	in := "services:\n  web:\n    image: nginx:1.27   # keep\n  db:\n    image: \"postgres:17\"\n  app:\n    image: ghcr.io/other/app:2\n"
	out, pinned := pinImages(in, refs)
	want := "services:\n  web:\n    image: nginx:1.27@sha256:aaaa   # keep\n  db:\n    image: \"postgres:17@sha256:bbbb\"\n  app:\n    image: ghcr.io/other/app:2\n"
	if out != want {
		t.Errorf("pinned:\n%s\nwant:\n%s", out, want)
	}
	if !slices.Equal(pinned, []string{"web", "db"}) {
		t.Errorf("pinned %v; app is another repository and must stay", pinned)
	}
}

func TestDeployNewStackAndHistory(t *testing.T) {
	n := newTestNode(t)
	res := n.deploy("media", v1, nil)
	if res.Outcome != stackproto.OutcomeDeployed {
		t.Fatalf("outcome %s: %s", res.Outcome, res.Reason)
	}
	if got := n.docker.Running("media")["web"]; got != "nginx:1.27" {
		t.Errorf("running %q", got)
	}
	disk, _ := os.ReadFile(filepath.Join(n.root, "media", "compose.yaml"))
	if string(disk) != v1 {
		t.Error("the file on disk isn't what was deployed")
	}
	res = n.deploy("media", v2Good, nil)
	if res.Outcome != stackproto.OutcomeDeployed || res.Plan.Services[0].Action != stackproto.ActionRecreate {
		t.Fatalf("upgrade: %+v", res)
	}
	if n.docker.Called("pull media web") != 2 {
		t.Error("each change of image should be pulled")
	}
	if res := n.deploy("media", v2Good, nil); res.Outcome != stackproto.OutcomeUnchanged {
		t.Errorf("redeploying the same file: %s", res.Outcome)
	}
	h := n.history("media")
	if len(h) != 2 || !strings.HasPrefix(h[0], "deploy Deploy media: recreate web · by sam via web · t_test") || !strings.HasPrefix(h[1], "deploy Deploy media: create web") {
		t.Errorf("history %q", h)
	}
	var list stackproto.ListResponse
	n.must("stack.list", nil, &list)
	if len(list.Stacks) != 1 || list.Stacks[0].Status != stackproto.StatusRunning || list.Stacks[0].Drift || list.Stacks[0].Head == nil {
		t.Errorf("list %+v", list)
	}
}

// The Done-when scenario: one deliberately bad deploy that rolls back on
// its own. v1 is healthy; v2's healthcheck fails; the gate catches it, and
// the previous file comes back as a new commit, pinned to the digest v1 was
// running, and passes the gate again.
func TestBadDeployRollsBackOnItsOwn(t *testing.T) {
	for _, bad := range []struct{ name, content, reason string }{
		{"unhealthy", v2Unhealthy, "is unhealthy"},
		{"crash loop", v2Crash, "restarted 1× during the soak"},
	} {
		t.Run(bad.name, func(t *testing.T) {
			n := newTestNode(t)
			if res := n.deploy("media", v1, nil); res.Outcome != stackproto.OutcomeDeployed {
				t.Fatalf("v1: %+v", res)
			}
			res := n.deploy("media", bad.content, nil)
			if res.Outcome != stackproto.OutcomeRolledBack {
				t.Fatalf("outcome %s: %s", res.Outcome, res.Reason)
			}
			if !strings.Contains(res.Reason, bad.reason) || !strings.Contains(res.Reason, "Rolled back to") {
				t.Errorf("reason %q", res.Reason)
			}
			pinned := digestOf("nginx:1.27")
			_, digest, _ := strings.Cut(pinned, "@")
			if got := n.docker.Running("media")["web"]; got != "nginx:1.27@"+digest {
				t.Errorf("after the rollback web runs %q, want v1 pinned to its digest", got)
			}
			disk, _ := os.ReadFile(filepath.Join(n.root, "media", "compose.yaml"))
			if !strings.Contains(string(disk), "image: nginx:1.27@"+digest+"   # pinned on purpose") || !strings.Contains(string(disk), "# the media stack") {
				t.Errorf("the restored file should be v1, pinned, comments kept:\n%s", disk)
			}
			h := n.history("media")
			if len(h) != 3 || !strings.HasPrefix(h[0], "rollback Roll back media: ") || !strings.Contains(h[0], "(pinned web)") ||
				!strings.HasPrefix(h[1], "deploy Deploy media: recreate web") {
				t.Errorf("the failed attempt must stay in the history: %q", h)
			}
			// The attempt recorded what was running before it.
			c := headCommit(t, n.root, 1)
			if got := previousImages(c.Message)["web"]; got != pinned {
				t.Errorf("Chadash-Previous-Image = %q, want %q", got, pinned)
			}
			// And the stack is healthy and deployable again.
			if res := n.deploy("media", v2Good, nil); res.Outcome != stackproto.OutcomeDeployed {
				t.Errorf("a good deploy after the rollback: %+v", res)
			}
		})
	}
}

func headCommit(t *testing.T, root string, back int) *object.Commit {
	t.Helper()
	r, err := git.PlainOpen(root)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := r.Head()
	if err != nil {
		t.Fatal(err)
	}
	c, err := r.CommitObject(ref.Hash())
	if err != nil {
		t.Fatal(err)
	}
	for range back {
		if c, err = c.Parent(0); err != nil {
			t.Fatal(err)
		}
	}
	return c
}

func TestBlockedByDryRun(t *testing.T) {
	n := newTestNode(t)
	n.deploy("media", v1, nil)
	before := headCommit(t, n.root, 0).Hash
	ups := n.docker.Called("up ")
	res := n.deploy("media", v2Missing, nil)
	if res.Outcome != stackproto.OutcomeBlocked || !strings.Contains(res.Reason, "manifest unknown") || !strings.Contains(res.Reason, "nothing was changed") {
		t.Fatalf("%+v", res)
	}
	if headCommit(t, n.root, 0).Hash != before || n.docker.Called("up ") != ups {
		t.Error("a blocked deploy must not commit or touch Docker")
	}
	n.docker.Unchecked = true
	if res := n.deploy("media", v2Good, nil); res.Outcome != stackproto.OutcomeBlocked || !strings.Contains(res.Reason, "Couldn't reach the registry") {
		t.Errorf("couldn't check reads differently: %+v", res)
	}
}

func TestStuckWhenTheRollbackFails(t *testing.T) {
	n := newTestNode(t)
	n.deploy("media", v1, nil)
	n.docker.FailUp = 2 // the deploy's up and the rollback's
	res := n.deploy("media", v2Good, nil)
	if res.Outcome != stackproto.OutcomeStuck || !strings.Contains(res.Reason, "the rollback to") || res.RollbackCommit == "" {
		t.Fatalf("%+v", res)
	}
}

func TestNewStackThatFailsIsRemoved(t *testing.T) {
	n := newTestNode(t)
	res := n.deploy("media", v2Unhealthy, nil)
	if res.Outcome != stackproto.OutcomeRolledBack || !strings.Contains(res.Reason, "removed it again") {
		t.Fatalf("%+v", res)
	}
	if n.docker.Called("down media") != 1 {
		t.Error("its containers should go")
	}
	if _, err := os.Stat(filepath.Join(n.root, "media", "compose.yaml")); !os.IsNotExist(err) {
		t.Error("its file should go too; the hub keeps the draft")
	}
}

func TestRefusals(t *testing.T) {
	n := newTestNode(t, func(o *Options) { o.Protected = []string{"chadash"} })
	n.deploy("media", v1, nil)
	code := func(err error) string {
		if e := stackproto.ErrorFrom(err); e != nil {
			return e.Code
		}
		return ""
	}
	// Someone deployed since the plan.
	err := n.call("stack.deploy", stackproto.DeployRequest{Stack: "media", Content: v2Good, ExpectHead: strings.Repeat("a", 40)}, nil)
	if code(err) != stackproto.CodeConflict {
		t.Errorf("stale head: %v", err)
	}
	// Edited outside Cha Dash.
	file := filepath.Join(n.root, "media", "compose.yaml")
	if err := os.WriteFile(file, []byte(v1+"# edited by hand\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := n.call("stack.deploy", stackproto.DeployRequest{Stack: "media", Content: v2Good}, nil); code(err) != stackproto.CodeDrift {
		t.Errorf("drift: %v", err)
	}
	var list stackproto.ListResponse
	n.must("stack.list", nil, &list)
	if !list.Stacks[0].Drift {
		t.Error("the list should show the drift")
	}
	// Accepting the edits commits them, and deploys work again.
	var ad stackproto.AdoptResult
	n.must("stack.adopt", stackproto.AdoptRequest{Stack: "media", By: "sam"}, &ad)
	if ad.Kind != stackproto.KindAccept {
		t.Errorf("adopting a drifted stack accepts the edits: %+v", ad)
	}
	if res := n.deploy("media", v2Good, nil); res.Outcome != stackproto.OutcomeDeployed {
		t.Errorf("after accepting: %+v", res)
	}
	// Cha Dash's own stack: by name, and by image.
	if err := n.call("stack.deploy", stackproto.DeployRequest{Stack: "chadash", Content: v1}, nil); code(err) != stackproto.CodeProtected {
		t.Errorf("protected by name: %v", err)
	}
	own := "services:\n  hub:\n    image: ghcr.io/ban-red/chadash:0.2.0\n"
	if err := n.call("stack.deploy", stackproto.DeployRequest{Stack: "dash", Content: own}, nil); code(err) != stackproto.CodeProtected {
		t.Errorf("protected by image: %v", err)
	}
	// One operation per stack.
	unlock, err := n.m.lock("media")
	if err != nil {
		t.Fatal(err)
	}
	if err := n.call("stack.deploy", stackproto.DeployRequest{Stack: "media", Content: v1}, nil); code(err) != stackproto.CodeBusy {
		t.Errorf("busy: %v", err)
	}
	unlock()
	// A missing secret.
	withSecret := strings.Replace(v1, "    healthcheck:", "    environment:\n      PW: ${secret:pw}\n    healthcheck:", 1)
	if err := n.call("stack.deploy", stackproto.DeployRequest{Stack: "media", Content: withSecret}, nil); code(err) != stackproto.CodeInvalid {
		t.Errorf("a secret that wasn't sent: %v", err)
	}
}

// TestAdoptFromAnotherTool: a project another tool deployed from inside its
// own container (Hawser's /data/stacks) has labels naming a path the node
// can't see. Its file, copied to <root>/<name>/, makes it adoptable there,
// and adopting restarts nothing.
func TestAdoptFromAnotherTool(t *testing.T) {
	n := newTestNode(t)
	content := "services:\n  app:\n    image: nginx:1\n"
	startOutside(t, n.docker, "apps", "/data/stacks/apps/compose.yaml", content)
	detected := func() stackproto.Detected {
		var det stackproto.DetectResponse
		n.must("stack.detect", nil, &det)
		for _, d := range det.Projects {
			if d.Name == "apps" {
				return d
			}
		}
		t.Fatal("apps isn't detected")
		return stackproto.Detected{}
	}
	if d := detected(); d.Adoptable || !strings.Contains(d.Reason, "/data/stacks/apps/compose.yaml") {
		t.Fatalf("before the copy: %+v", d)
	}
	dir := filepath.Join(n.root, "apps")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if d := detected(); !d.Adoptable || d.Path != dir || d.Status != stackproto.StatusRunning || d.Reason != "" {
		t.Fatalf("after the copy: %+v", d)
	}
	ids := n.docker.Running("apps")
	var ad stackproto.AdoptResult
	n.must("stack.adopt", stackproto.AdoptRequest{Stack: "apps"}, &ad)
	if ad.Location == stackproto.LocationExternal || ad.Path != dir {
		t.Errorf("adopt %+v", ad)
	}
	if n.docker.Called("up ") != 0 || n.docker.Called("down ") != 0 || n.docker.Running("apps")["app"] != ids["app"] {
		t.Error("adopting must not restart anything")
	}
	var p stackproto.Plan
	n.must("stack.plan", stackproto.PlanRequest{Stack: "apps", Content: content}, &p)
	if p.Services[0].Action != stackproto.ActionUnchanged {
		t.Errorf("the same file should plan no changes: %+v", p)
	}
}

// Cha Dash's own project with an override (two files) can't be adopted, but
// it's still listed and read as protected, so the hub takes no draft for it.
func TestOwnProjectWithAnOverrideIsProtected(t *testing.T) {
	n := newTestNode(t, func(o *Options) { o.Protected = []string{"chadash"} })
	startOutside(t, n.docker, "chadash", "/root/chadash/compose.yaml", "services:\n  chadash:\n    image: ghcr.io/ban-red/chadash:0.2\n")
	n.docker.SetFiles("chadash", []string{"/root/chadash/compose.yaml", "/root/chadash/compose.override.yaml"})
	var det stackproto.DetectResponse
	n.must("stack.detect", nil, &det)
	i := slices.IndexFunc(det.Projects, func(d stackproto.Detected) bool { return d.Name == "chadash" })
	if i < 0 {
		t.Fatalf("not detected: %+v", det)
	}
	if d := det.Projects[i]; !d.Protected || d.Adoptable || !strings.Contains(d.Reason, "Cha Dash's own stack") {
		t.Errorf("detected %+v", d)
	}
	var st stackproto.Stack
	n.must("stack.get", stackproto.GetRequest{Stack: "chadash"}, &st)
	if !st.Protected || st.ProtectedReason == "" || st.Managed || st.Content != "" || len(st.Containers) != 1 {
		t.Errorf("get %+v", st)
	}
	// A project that isn't protected still isn't found.
	if err := n.call("stack.get", stackproto.GetRequest{Stack: "nothing"}, nil); stackproto.ErrorFrom(err) == nil || stackproto.ErrorFrom(err).Code != stackproto.CodeNotFound {
		t.Errorf("unknown stack: %v", err)
	}
}

func TestAdoptInPlace(t *testing.T) {
	n := newTestNode(t)
	// A Dockge-style directory in the root, never adopted, not running.
	if err := os.MkdirAll(filepath.Join(n.root, "notes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(n.root, "notes", "compose.yaml"), []byte("services:\n  app:\n    image: nginx:1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A project the CLI started elsewhere, with a .env holding a secret.
	elsewhere := t.TempDir()
	extFile := filepath.Join(elsewhere, "docker-compose.yml")
	ext := "services:\n  dns:\n    image: pihole/pihole:2026.10\n    environment:\n      WEBPASSWORD: ${WEBPASSWORD}\n"
	if err := os.WriteFile(extFile, []byte(ext), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(elsewhere, ".env"), []byte("WEBPASSWORD=dotenv-secret-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	startOutside(t, n.docker, "pihole", extFile, ext)
	// One with override files can't be adopted.
	n.docker.AddProject("multi", []string{"/srv/multi/compose.yaml", "/srv/multi/compose.override.yaml"})

	var det stackproto.DetectResponse
	n.must("stack.detect", nil, &det)
	byName := map[string]stackproto.Detected{}
	for _, d := range det.Projects {
		byName[d.Name] = d
	}
	if d := byName["pihole"]; !d.Adoptable || d.Status != stackproto.StatusRunning || d.Path != elsewhere {
		t.Errorf("pihole: %+v", d)
	}
	if d := byName["notes"]; !d.Adoptable || d.Status != stackproto.StatusDown {
		t.Errorf("notes: %+v", d)
	}
	if d := byName["multi"]; d.Adoptable || !strings.Contains(d.Reason, "2 compose files") {
		t.Errorf("multi: %+v", d)
	}
	ids := n.docker.Running("pihole")
	before, _ := os.ReadDir(elsewhere)

	var ad stackproto.AdoptResult
	n.must("stack.adopt", stackproto.AdoptRequest{Stack: "pihole", By: "sam", Via: "web"}, &ad)
	if ad.Location != stackproto.LocationExternal || ad.Path != elsewhere {
		t.Errorf("%+v", ad)
	}
	n.must("stack.adopt", stackproto.AdoptRequest{Stack: "notes"}, nil)
	if n.docker.Called("up ") != 0 || n.docker.Called("down ") != 0 {
		t.Error("adopting must not restart anything")
	}
	after, _ := os.ReadDir(elsewhere)
	if len(after) != len(before) {
		t.Error("adopting in place must not write next to the stack")
	}
	if got := n.docker.Running("pihole"); got["dns"] != ids["dns"] {
		t.Error("the same container keeps running")
	}
	var list stackproto.ListResponse
	n.must("stack.list", nil, &list)
	if len(list.Stacks) != 2 || list.Stacks[0].Name != "notes" || list.Stacks[1].Location != stackproto.LocationExternal {
		t.Errorf("list %+v", list.Stacks)
	}
	// The .env stays out of history, and its values never leave.
	var st stackproto.Stack
	n.must("stack.get", stackproto.GetRequest{Stack: "pihole"}, &st)
	if !st.Env.Present || !slices.Equal(st.Env.Keys, []string{"WEBPASSWORD"}) {
		t.Errorf("env %+v", st.Env)
	}
	assertNowhere(t, n, "dotenv-secret-value")
	// Unchanged after adopting: the hash plan matches the running container.
	var p stackproto.Plan
	n.must("stack.plan", stackproto.PlanRequest{Stack: "pihole", Content: ext}, &p)
	if p.Services[0].Action != stackproto.ActionUnchanged {
		t.Errorf("after adopting in place nothing should need recreating: %+v", p)
	}
	// A deploy of an external stack writes its real file and the shadow.
	ext2 := strings.Replace(ext, "2026.10", "2026.11", 1)
	if res := n.deploy("pihole", ext2, nil); res.Outcome != stackproto.OutcomeDeployed {
		t.Fatalf("%+v", res)
	}
	if b, _ := os.ReadFile(extFile); string(b) != ext2 {
		t.Error("the real file should be deployed in place")
	}
	if st, _ := os.Stat(extFile); st.Mode().Perm() != 0o640 {
		t.Errorf("the file's mode changed to %v", st.Mode().Perm())
	}
	// Un-adopt: the repo forgets it, the files stay.
	n.must("stack.unadopt", stackproto.UnadoptRequest{Stack: "pihole"}, nil)
	n.must("stack.unadopt", stackproto.UnadoptRequest{Stack: "notes"}, nil)
	n.must("stack.list", nil, &list)
	if len(list.Stacks) != 0 {
		t.Errorf("after un-adopting: %+v", list.Stacks)
	}
	if _, err := os.Stat(filepath.Join(n.root, "notes", "compose.yaml")); err != nil {
		t.Error("un-adopting must leave the file")
	}
	if _, err := os.Stat(extFile); err != nil {
		t.Error("un-adopting must leave the external file")
	}
	n.must("stack.detect", nil, &det)
	if len(det.Projects) != 3 {
		t.Errorf("both are detected again: %+v", det.Projects)
	}
}

// Secrets are resolved in memory for Docker and nowhere else: not the
// stacks root, not git, not progress, not results.
func TestSecretsNeverTouchDisk(t *testing.T) {
	n := newTestNode(t)
	const value = "s3cr3t-VALUE-never-on-disk"
	content := strings.Replace(v1, "    healthcheck:", "    environment:\n      DB_PASSWORD: ${secret:db_password}\n    healthcheck:", 1)
	res := n.deploy("media", content, map[string]string{"db_password": value})
	if res.Outcome != stackproto.OutcomeDeployed {
		t.Fatalf("%+v", res)
	}
	if got := n.docker.LastEnv()["web"]["DB_PASSWORD"]; got != value {
		t.Errorf("Docker got %q, want the resolved value", got)
	}
	var p stackproto.Plan
	n.must("stack.plan", stackproto.PlanRequest{Stack: "media", Content: content, Secrets: stackproto.Secrets{"db_password": value}}, &p)
	if p.Services[0].Action != stackproto.ActionUnchanged {
		t.Errorf("the plan hashes with the real value: %+v", p.Services)
	}
	n.must("stack.plan", stackproto.PlanRequest{Stack: "media", Content: content, Secrets: stackproto.Secrets{"db_password": "another"}}, &p)
	if p.Services[0].Action != stackproto.ActionRecreate {
		t.Errorf("a changed secret recreates the service: %+v", p.Services)
	}
	// A failing deploy with the secret, so rollbacks and reasons are covered.
	bad := strings.Replace(content, "nginx:1.27", "nginx-unhealthy:1.28", 1)
	res = n.deploy("media", bad, map[string]string{"db_password": value})
	if res.Outcome != stackproto.OutcomeRolledBack {
		t.Fatalf("%+v", res)
	}
	// A loader error that would echo the value is scrubbed.
	broken := strings.Replace(content, "image: nginx:1.27", "image: nginx:1.27\n    ports: [\"${secret:db_password}\"]", 1)
	err := n.call("stack.deploy", stackproto.DeployRequest{Stack: "media", Content: broken, Secrets: stackproto.Secrets{"db_password": value}}, nil)
	if err == nil || strings.Contains(err.Error(), value) {
		t.Errorf("error %v", err)
	}
	assertNowhere(t, n, value)
	if s := (stackproto.Secrets{"db_password": value}); strings.Contains(s.String(), value) {
		t.Error("Secrets must not print their values")
	}
}

// assertNowhere fails if needle is in any file under the root, any git
// object, or any progress message.
func assertNowhere(t *testing.T, n *testNode, needle string) {
	t.Helper()
	b := []byte(needle)
	sanity := false
	err := filepath.WalkDir(n.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.HasSuffix(path, "compose.yaml") {
			sanity = true
		}
		if bytes.Contains(data, b) {
			t.Errorf("%s holds the secret", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !sanity {
		t.Fatal("the scan saw no compose file; it checked nothing")
	}
	r, err := git.PlainOpen(n.root)
	if err != nil {
		t.Fatal(err)
	}
	objs, err := r.Storer.IterEncodedObjects(plumbing.AnyObject)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	_ = objs.ForEach(func(o plumbing.EncodedObject) error {
		count++
		rd, err := o.Reader()
		if err != nil {
			return err
		}
		defer rd.Close()
		data, _ := io.ReadAll(rd)
		if bytes.Contains(data, b) {
			t.Errorf("git object %s holds the secret", o.Hash())
		}
		return nil
	})
	if count == 0 {
		t.Fatal("no git objects scanned")
	}
	for _, p := range n.progress {
		if strings.Contains(p, needle) {
			t.Errorf("progress %s holds the secret", p)
		}
	}
}

func TestManualRollbackPinsWhatItRan(t *testing.T) {
	n := newTestNode(t)
	n.deploy("media", v1, nil)
	first := headCommit(t, n.root, 0).Hash.String()
	n.deploy("media", v2Good, nil)
	var res stackproto.DeployResult
	n.must("stack.rollback", stackproto.RollbackRequest{Stack: "media", Commit: first[:10], By: "sam"}, &res)
	if res.Outcome != stackproto.OutcomeDeployed {
		t.Fatalf("%+v", res)
	}
	_, digest, _ := strings.Cut(digestOf("nginx:1.27"), "@")
	if got := n.docker.Running("media")["web"]; got != "nginx:1.27@"+digest {
		t.Errorf("running %q, want 1.27 pinned to the digest it last ran", got)
	}
	h := n.history("media")
	if !strings.HasPrefix(h[0], "rollback Roll back media to "+first[:7]+" (pinned web): recreate web") {
		t.Errorf("history %q", h)
	}
	if err := n.call("stack.rollback", stackproto.RollbackRequest{Stack: "media", Commit: "deadbeefdeadbeef"}, nil); stackproto.ErrorFrom(err).Code != stackproto.CodeNotFound {
		t.Errorf("unknown commit: %v", err)
	}
}

func TestErrorFromSurvivesText(t *testing.T) {
	e := stackproto.Errorf(stackproto.CodeDrift, "edited by hand")
	for _, err := range []error{e, errorString("node local: " + e.Error()), errorString(e.Error())} {
		got := stackproto.ErrorFrom(err)
		if got == nil || got.Code != stackproto.CodeDrift || got.Message != "edited by hand" {
			t.Errorf("%v → %+v", err, got)
		}
	}
	if stackproto.ErrorFrom(errorString("dial: refused")) != nil {
		t.Error("an unknown code isn't a stack error")
	}
}

type errorString string

func (e errorString) Error() string { return string(e) }
