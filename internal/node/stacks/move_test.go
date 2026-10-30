package stacks

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"dash.cha.sh/internal/stackproto"
)

// movedApp is a compose file another tool wrote, with every kind of
// relative path a move rewrites, and an env_file naming the .env that moves
// with it (which stays relative).
const movedApp = `services:
  app:
    image: nginx:1.28
    volumes:
      - ./data:/data
      - "./conf:/etc/app:ro"
      - cache:/cache
      - type: bind
        source: ../shared
        target: /shared
    env_file: .env
    build:
      context: ./src
configs:
  app:
    file: ./app.conf
volumes:
  cache:
`

// otherTool starts a project the way another tool would: its file and .env
// in a directory outside the root, its containers running.
func otherTool(t *testing.T, n *testNode, name, content string) (dir, file string) {
	t.Helper()
	dir = filepath.Join(t.TempDir(), "srv", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	file = filepath.Join(dir, "docker-compose.yml")
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("TZ=Europe/Example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	startOutside(t, n.docker, name, file, content)
	return dir, file
}

func detectedNamed(t *testing.T, n *testNode, name string) stackproto.Detected {
	t.Helper()
	var det stackproto.DetectResponse
	n.must("stack.detect", nil, &det)
	i := slices.IndexFunc(det.Projects, func(d stackproto.Detected) bool { return d.Name == name })
	if i < 0 {
		t.Fatalf("%s isn't detected: %+v", name, det.Projects)
	}
	return det.Projects[i]
}

// Moving a project another tool runs: relative paths made absolute and
// listed with their lines, the file and its .env in the root, an adopt
// commit with the trailer, and nothing run on Docker. The old file stays.
func TestMoveFromAnotherTool(t *testing.T) {
	n := newTestNode(t)
	dir, file := otherTool(t, n, "app", movedApp)
	before := n.docker.Running("app")
	if d := detectedNamed(t, n, "app"); !d.Moveable || d.MoveRefusal != "" {
		t.Fatalf("detected %+v", d)
	}

	// A dry run says the same and leaves nothing behind.
	var dry stackproto.MoveResult
	n.must("stack.move", stackproto.MoveRequest{Stack: "app", FromPath: file, DryRun: true}, &dry)
	if dry.Commit != "" || len(dry.Rewrites) != 5 {
		t.Fatalf("dry run %+v", dry)
	}
	if _, err := os.Stat(filepath.Join(n.root, "app")); !os.IsNotExist(err) {
		t.Fatalf("a dry run left %s: %v", filepath.Join(n.root, "app"), err)
	}

	var res stackproto.MoveResult
	n.must("stack.move", stackproto.MoveRequest{Stack: "app", FromPath: file, By: "sam", Via: "web"}, &res)
	parent := filepath.Dir(dir)
	want := []stackproto.Rewrite{
		{Line: 5, From: "./data:/data", To: dir + "/data:/data"},
		{Line: 6, From: "./conf:/etc/app:ro", To: dir + "/conf:/etc/app:ro"},
		{Line: 9, From: "../shared", To: parent + "/shared"},
		{Line: 13, From: "./src", To: dir + "/src"},
		{Line: 16, From: "./app.conf", To: dir + "/app.conf"},
	}
	if !slices.Equal(res.Rewrites, want) {
		t.Fatalf("rewrites\n got %+v\nwant %+v", res.Rewrites, want)
	}
	if res.Commit == "" || res.Path != filepath.Join(n.root, "app") {
		t.Fatalf("result %+v", res)
	}
	// The first deploy recreates it: the file now lives elsewhere.
	if len(res.Plan.Services) != 1 || res.Plan.Services[0].Action != stackproto.ActionRecreate {
		t.Fatalf("plan %+v", res.Plan)
	}
	moved, err := os.ReadFile(filepath.Join(n.root, "app", "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range want {
		if !strings.Contains(string(moved), w.To) {
			t.Errorf("the moved file lacks %s:\n%s", w.To, moved)
		}
	}
	if !strings.Contains(string(moved), `- "`+dir+`/conf:/etc/app:ro"`) || !strings.Contains(string(moved), "env_file: .env") || !strings.Contains(string(moved), "- cache:/cache") {
		t.Errorf("quoting, the .env and named volumes should stay as written:\n%s", moved)
	}
	if env, err := os.ReadFile(filepath.Join(n.root, "app", ".env")); err != nil || string(env) != "TZ=Europe/Example\n" {
		t.Errorf(".env %q %v", env, err)
	}
	if old, _ := os.ReadFile(file); string(old) != movedApp {
		t.Error("the old file changed")
	}
	if n.docker.Called("up ") != 0 || n.docker.Called("down ") != 0 || n.docker.Called("pull") != 0 || n.docker.Called("dryrun") != 0 {
		t.Error("moving must not run anything")
	}
	if !maps.Equal(n.docker.Running("app"), before) {
		t.Error("moving must not restart anything")
	}

	// It's managed in the root now, with the trailer, and the stack says
	// where the old copy is.
	var st stackproto.Stack
	n.must("stack.get", stackproto.GetRequest{Stack: "app"}, &st)
	if !st.Managed || st.Location != stackproto.LocationRoot || st.MovedFrom != file || st.History[0].Kind != stackproto.KindAdopt {
		t.Fatalf("after the move: %+v", st)
	}
	c, err := n.m.repo.commitByHash(res.Commit)
	if err != nil {
		t.Fatal(err)
	}
	if trailer(c.Message, trailerMovedFrom) != file || trailer(c.Message, trailerKind) != stackproto.KindAdopt || !strings.HasPrefix(c.Message, "Move app into the stacks root from "+dir+" · by sam via web") {
		t.Fatalf("commit message %q", c.Message)
	}
	if strings.Contains(c.Message, "Europe") {
		t.Error("the .env went into git")
	}
	// Un-adopting keeps both copies, and the containers.
	n.must("stack.unadopt", stackproto.UnadoptRequest{Stack: "app"}, nil)
	for _, f := range []string{file, filepath.Join(n.root, "app", "compose.yaml")} {
		if _, err := os.Stat(f); err != nil {
			t.Errorf("un-adopting removed %s: %v", f, err)
		}
	}
	if !maps.Equal(n.docker.Running("app"), before) {
		t.Error("un-adopting must not restart anything")
	}
}

// A stack adopted in place moves into the root: its registration and its
// shadow copy go, and the old file stays where it is.
func TestMoveFromInPlace(t *testing.T) {
	n := newTestNode(t)
	content := "services:\n  dns:\n    image: pihole/pihole:2026.10\n    volumes:\n      - ./etc:/etc/pihole\n"
	dir, file := otherTool(t, n, "pihole", content)
	n.must("stack.adopt", stackproto.AdoptRequest{Stack: "pihole"}, nil)
	shadow := filepath.Join(n.root, shadowDir, "pihole", "docker-compose.yml")
	if _, err := os.Stat(shadow); err != nil {
		t.Fatal(err)
	}
	var res stackproto.MoveResult
	n.must("stack.move", stackproto.MoveRequest{Stack: "pihole", FromPath: dir}, &res)
	if len(res.Rewrites) != 1 || res.Rewrites[0].To != dir+"/etc:/etc/pihole" {
		t.Fatalf("rewrites %+v", res.Rewrites)
	}
	reg, err := n.m.readRegistry()
	if err != nil || len(reg.Stacks) != 0 {
		t.Fatalf("registry %+v %v", reg, err)
	}
	if _, err := os.Stat(shadow); !os.IsNotExist(err) {
		t.Errorf("the shadow copy stayed: %v", err)
	}
	tracked, _ := n.m.repo.tracked()
	if tracked[shadowDir+"/pihole/docker-compose.yml"] || !tracked["pihole/compose.yaml"] {
		t.Errorf("tracked %v", tracked)
	}
	var st stackproto.Stack
	n.must("stack.get", stackproto.GetRequest{Stack: "pihole"}, &st)
	if st.Location != stackproto.LocationRoot || st.MovedFrom != file {
		t.Fatalf("stack %+v", st)
	}
	// The history goes on: the adopt in place, then the move.
	if len(st.History) != 2 || st.History[1].Subject != "Adopt pihole in place at "+dir {
		t.Fatalf("history %+v", st.History)
	}
	// Once the old file is gone, the stack stops mentioning it.
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	var after stackproto.Stack
	n.must("stack.get", stackproto.GetRequest{Stack: "pihole"}, &after)
	if after.MovedFrom != "" {
		t.Errorf("moved_from %q after the old file went", after.MovedFrom)
	}
}

// Content the hub supplies: absolute paths move, relative ones refuse and
// name their line, since where it came from isn't known.
func TestMoveSuppliedContent(t *testing.T) {
	n := newTestNode(t)
	relative := "services:\n  web:\n    image: nginx:1.28\n    volumes:\n      - ./html:/usr/share/nginx/html\n"
	err := n.call("stack.move", stackproto.MoveRequest{Stack: "web", Content: relative}, nil)
	if e := stackproto.ErrorFrom(err); e == nil || e.Code != stackproto.CodeInvalid || !strings.Contains(e.Message, "line 5 (the bind source ./html)") {
		t.Fatalf("relative supplied content: %v", err)
	}
	if _, err := os.Stat(filepath.Join(n.root, "web")); !os.IsNotExist(err) {
		t.Fatal("a refused move left its directory")
	}
	absolute := "services:\n  web:\n    image: nginx:1.28\n    env_file: .env\n    volumes:\n      - /srv/web/html:/usr/share/nginx/html\n"
	var res stackproto.MoveResult
	n.must("stack.move", stackproto.MoveRequest{Stack: "web", Content: absolute, Env: "TZ=UTC\n"}, &res)
	if len(res.Rewrites) != 0 || res.Commit == "" || res.Plan.Services[0].Action != stackproto.ActionCreate {
		t.Fatalf("result %+v", res)
	}
	if env, _ := os.ReadFile(filepath.Join(n.root, "web", ".env")); string(env) != "TZ=UTC\n" {
		t.Errorf(".env %q", env)
	}
	c, _ := n.m.repo.commitByHash(res.Commit)
	if trailer(c.Message, trailerMovedFrom) != movedFromSupplied {
		t.Errorf("message %q", c.Message)
	}
	var st stackproto.Stack
	n.must("stack.get", stackproto.GetRequest{Stack: "web"}, &st)
	if st.MovedFrom != "" || !st.Managed {
		t.Errorf("stack %+v", st)
	}
}

func TestMoveRefusals(t *testing.T) {
	n := newTestNode(t)
	_, file := otherTool(t, n, "app", "services:\n  app:\n    image: nginx:1.28\n")
	code := func(req stackproto.MoveRequest) (string, string) {
		t.Helper()
		err := n.call("stack.move", req, nil)
		e := stackproto.ErrorFrom(err)
		if e == nil {
			t.Fatalf("%+v: %v", req, err)
		}
		return e.Code, e.Message
	}
	// Exactly one of from_path and content.
	if c, _ := code(stackproto.MoveRequest{Stack: "app"}); c != stackproto.CodeInvalid {
		t.Errorf("neither: %s", c)
	}
	if c, _ := code(stackproto.MoveRequest{Stack: "app", FromPath: file, Content: "services: {}\n"}); c != stackproto.CodeInvalid {
		t.Errorf("both: %s", c)
	}
	// from_path must be where the project runs from.
	if c, m := code(stackproto.MoveRequest{Stack: "app", FromPath: "/etc/passwd"}); c != stackproto.CodeInvalid || !strings.Contains(m, file) {
		t.Errorf("another path: %s %s", c, m)
	}
	// A root directory that already exists.
	if err := os.MkdirAll(filepath.Join(n.root, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	if c, _ := code(stackproto.MoveRequest{Stack: "app", FromPath: file}); c != stackproto.CodeExists {
		t.Errorf("existing directory: %s", c)
	}
	if d := detectedNamed(t, n, "app"); d.Moveable || !strings.Contains(d.MoveRefusal, "already exists") {
		t.Errorf("detected %+v", d)
	}
	if err := os.Remove(filepath.Join(n.root, "app")); err != nil {
		t.Fatal(err)
	}
	// A stack already managed in the root.
	n.deploy("media", "services:\n  web:\n    image: nginx:1.28\n", nil)
	if c, _ := code(stackproto.MoveRequest{Stack: "media", Content: "services:\n  web:\n    image: nginx:1.28\n"}); c != stackproto.CodeExists {
		t.Errorf("managed: %s", c)
	}
	// Cha Dash's own project.
	_, hub := otherTool(t, n, "chadash", "services:\n  chadash:\n    image: ghcr.io/ban-red/chadash:0.2.0\n")
	if c, _ := code(stackproto.MoveRequest{Stack: "chadash", FromPath: hub}); c != stackproto.CodeProtected {
		t.Errorf("protected: %s", c)
	}
	if d := detectedNamed(t, n, "chadash"); d.Moveable {
		t.Errorf("detected %+v", d)
	}
	// What the guard refuses, and nothing left behind.
	sock := "services:\n  agent:\n    image: portainer/agent:2\n    volumes:\n      - /var/run/docker.sock:/var/run/docker.sock\n"
	if c, m := code(stackproto.MoveRequest{Stack: "agent", Content: sock}); c != stackproto.CodeInvalid || !strings.Contains(m, "docker.sock") {
		t.Errorf("guard: %s %s", c, m)
	}
	if _, err := os.Stat(filepath.Join(n.root, "agent")); !os.IsNotExist(err) {
		t.Error("a refused move left its directory")
	}
	// An env_file outside the stack's directory, once made absolute, is
	// outside it: the guard refuses it.
	_, extra := otherTool(t, n, "extra", "services:\n  app:\n    image: nginx:1.28\n    env_file: ./.env\n")
	if err := os.WriteFile(extra, []byte("services:\n  app:\n    image: nginx:1.28\n    env_file: ./app.env\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(extra), "app.env"), []byte("A=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if c, m := code(stackproto.MoveRequest{Stack: "extra", FromPath: extra}); c != stackproto.CodeInvalid || !strings.Contains(m, "env_file") {
		t.Errorf("env_file elsewhere: %s %s", c, m)
	}
	if n.docker.Called("up extra") != 0 {
		t.Error("ran something")
	}
}
