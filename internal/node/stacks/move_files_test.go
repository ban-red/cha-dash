package stacks

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"dash.cha.sh/internal/stackproto"
)

// TestMoveReadsOnlyRegularFiles: the compose file and the .env beside it
// are copied only when they are regular files. A symlink (here to a file
// the node shouldn't hand out) or a FIFO refuses the move, at once, and
// leaves nothing in the root.
func TestMoveReadsOnlyRegularFiles(t *testing.T) {
	n := newTestNode(t)
	refused := func(what, stack, file string) {
		t.Helper()
		done := make(chan error, 1)
		go func() {
			done <- n.call("stack.move", stackproto.MoveRequest{Stack: stack, FromPath: file, DryRun: true}, nil)
		}()
		var err error
		select {
		case err = <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: the move hung", what)
		}
		if e := stackproto.ErrorFrom(err); e == nil || e.Code != stackproto.CodeUnadoptable {
			t.Fatalf("%s: %v", what, err)
		}
		if _, err := os.Stat(filepath.Join(n.root, stack)); !os.IsNotExist(err) {
			t.Fatalf("%s: the refusal left %s", what, filepath.Join(n.root, stack))
		}
	}
	secret := filepath.Join(t.TempDir(), "identity")
	if err := os.WriteFile(secret, []byte("KEY=not-for-stacks\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	const app = "services:\n  app:\n    image: nginx:1.28\n"

	dir, file := otherTool(t, n, "linked", app)
	replace := func(path string, make func(string) error) {
		t.Helper()
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := make(path); err != nil {
			t.Fatal(err)
		}
	}
	replace(filepath.Join(dir, ".env"), func(p string) error { return os.Symlink(secret, p) })
	refused("a symlinked .env", "linked", file)

	dir, file = otherTool(t, n, "piped", app)
	replace(filepath.Join(dir, ".env"), func(p string) error { return syscall.Mkfifo(p, 0o600) })
	refused("a FIFO .env", "piped", file)

	// The compose file itself, as a symlink to a real compose file.
	dir, file = otherTool(t, n, "aliased", app)
	real := filepath.Join(dir, "real.yml")
	if err := os.Rename(file, real); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, file); err != nil {
		t.Fatal(err)
	}
	refused("a symlinked compose file", "aliased", file)
}

// TestDotEnvSkipsAFIFO: a FIFO named .env in a stack's directory fails the
// load at once instead of hanging it.
func TestDotEnvSkipsAFIFO(t *testing.T) {
	dir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, ".env"), 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := dotEnv(dir); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a FIFO read as a .env")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reading a FIFO .env hung")
	}
}

// TestMoveStagesOutsideTheStacks: a move's preview is staged under
// .chadash, which the listings skip, and what a node that died mid-preview
// left there is removed when the node starts.
func TestMoveStagesOutsideTheStacks(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, ".chadash", "move-123", "app")
	if err := os.MkdirAll(left, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(left, "compose.yaml"), []byte("services:\n  app:\n    image: nginx:1.28\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	n := newTestNode(t, func(o *Options) { o.Root = root })
	if _, err := os.Stat(filepath.Dir(left)); !os.IsNotExist(err) {
		t.Fatalf("the leftover staging directory is still there: %v", err)
	}

	_, file := otherTool(t, n, "app", movedApp)
	var dry stackproto.MoveResult
	n.must("stack.move", stackproto.MoveRequest{Stack: "app", FromPath: file, DryRun: true}, &dry)
	if dry.Path != filepath.Join(root, "app") || len(dry.Plan.Services) != 1 {
		t.Fatalf("dry run %+v", dry)
	}
	staged := func() []string {
		m, _ := filepath.Glob(filepath.Join(root, ".chadash", "move-*"))
		return m
	}
	if s := staged(); len(s) != 0 {
		t.Fatalf("a dry run left %v", s)
	}
	if _, err := os.Stat(filepath.Join(root, "app")); !os.IsNotExist(err) {
		t.Fatalf("a dry run made %s: %v", filepath.Join(root, "app"), err)
	}
	var res stackproto.MoveResult
	n.must("stack.move", stackproto.MoveRequest{Stack: "app", FromPath: file}, &res)
	if _, err := os.Stat(filepath.Join(root, "app", "compose.yaml")); err != nil || res.Commit == "" {
		t.Fatalf("the move %+v: %v", res, err)
	}
	if _, err := os.Stat(filepath.Join(root, "app", ".env")); err != nil {
		t.Fatalf("the .env didn't move: %v", err)
	}
	if s := staged(); len(s) != 0 {
		t.Fatalf("a move left %v", s)
	}
}
