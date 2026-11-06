package docker

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"dash.cha.sh/internal/node"
	"dash.cha.sh/internal/nodeproto"
)

// container.update: pull the tag, recreate on its digest, hold it to the
// gate, and put the previous image back when it fails.
func TestContainerUpdate(t *testing.T) {
	f, host := newFakeDocker(t)
	p, err := node.ParsePolicy([]byte("docker: { protect: [vault] }\n"))
	if err != nil {
		t.Fatal(err)
	}
	m, err := New(Options{Host: host, Policy: p})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }
	m.sleep = func(context.Context, time.Duration) error { now = now.Add(time.Second); return nil }
	d, err := node.NewDispatcher(p, m)
	if err != nil {
		t.Fatal(err)
	}
	// Every pull in the fake gives the image digest("2"); its RepoDigests
	// say what the registry called it.
	f.repoDigests[digest("2")] = []string{"example/app@" + digest("9"), "example/bad@" + digest("8")}
	f.repoDigests[digest("a")] = []string{"example/app@" + digest("1")}
	f.repoDigests[digest("b")] = []string{"example/bad@" + digest("3")}
	f.add(&fakeContainer{Name: "app", Image: "example/app:1", ImageID: digest("a")})
	f.add(&fakeContainer{Name: "bad", Image: "example/bad:1", ImageID: digest("b")})
	f.add(&fakeContainer{Name: "vault", Image: "example/vault:1"})

	update := func(name string) (map[string]string, []string) {
		t.Helper()
		res, items, err := call(t, d, "container.update", map[string]any{"id": name})
		if err != nil {
			t.Fatalf("update %s: %v", name, err)
		}
		var out map[string]string
		_ = json.Unmarshal(res, &out)
		var steps []string
		for _, it := range items {
			var s struct{ Step string }
			if json.Unmarshal(it, &s) == nil && s.Step != "" {
				steps = append(steps, s.Step)
			}
		}
		return out, steps
	}

	out, steps := update("app")
	pinned := "example/app:1@" + digest("9")
	if out["outcome"] != "deployed" || out["current"] != pinned || out["previous"] != "example/app:1@"+digest("1") {
		t.Fatalf("app: %v", out)
	}
	if c := f.byName("app"); c.Image != pinned || c.State != "running" {
		t.Fatalf("app now %+v", c)
	}
	// A container with no healthcheck passes only after running for settle.
	if !strings.Contains(strings.Join(steps, "\n"), "Health gate") || !strings.Contains(strings.Join(steps, "\n"), "Updated and healthy") {
		t.Fatalf("steps %q", steps)
	}

	// Again: the tag still points at what it runs.
	if out, _ := update("app"); out["outcome"] != "unchanged" {
		t.Fatalf("app again: %v", out)
	}

	// An image that restarts at once fails the gate and is rolled back.
	f.crash["example/bad:1@"+digest("8")] = true
	out, _ = update("bad")
	prev := "example/bad:1@" + digest("3")
	if out["outcome"] != "rolled_back" || out["current"] != prev || !strings.Contains(out["reason"], "bad is restarting") || !strings.Contains(out["reason"], "Rolled back to "+prev) {
		t.Fatalf("bad: %v", out)
	}
	if c := f.byName("bad"); c.Image != prev || c.State != "running" {
		t.Fatalf("bad now %+v", c)
	}
	// One that exits at once, with no restart policy to bring it back: the
	// rollback still starts the previous image.
	f.crash["example/bad:1@"+digest("8")] = false
	f.exits["example/bad:1@"+digest("8")] = true
	f.add(&fakeContainer{Name: "flaky", Image: "example/bad:1", ImageID: digest("b")})
	if out, _ = update("flaky"); out["outcome"] != "rolled_back" || !strings.Contains(out["reason"], "flaky exited") {
		t.Fatalf("flaky: %v", out)
	}
	if c := f.byName("flaky"); c.Image != prev || c.State != "running" {
		t.Fatalf("flaky after an exit %+v", c)
	}

	// A stopped container isn't updated: the gate couldn't tell it works.
	f.add(&fakeContainer{Name: "idle", Image: "example/app:1", ImageID: digest("a"), State: "exited"})
	if _, _, err := call(t, d, "container.update", map[string]any{"id": "idle"}); nodeproto.CodeOf(err) != nodeproto.CodeConflict {
		t.Fatalf("stopped = %v", err)
	}

	// Protected containers and refused policies.
	if _, _, err := call(t, d, "container.update", map[string]any{"id": "vault"}); nodeproto.CodeOf(err) != nodeproto.CodeForbidden {
		t.Fatalf("protected = %v", err)
	}
	ro, _ := node.ParsePolicy([]byte("docker: { allow: [read] }\n"))
	d2, _ := node.NewDispatcher(ro, m)
	if _, _, err := call(t, d2, "container.update", map[string]any{"id": "app"}); nodeproto.CodeOf(err) != nodeproto.CodeForbidden {
		t.Fatalf("without docker.update = %v", err)
	}
}

// A rollback finds the image it puts back on either image store. The
// classic store still names it repo@digest; the containerd store drops that
// name once the tag moves, so the node pulls it again by digest (its layers
// are local). Engine tests run both stores in CI; this is the same at layer 1.
func TestContainerUpdateRollsBackOnEitherStore(t *testing.T) {
	for _, containerd := range []bool{false, true} {
		f, host := newFakeDocker(t)
		f.containerdStore = containerd
		p, err := node.ParsePolicy([]byte("{}\n"))
		if err != nil {
			t.Fatal(err)
		}
		m, err := New(Options{Host: host, Policy: p})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { m.Close() })
		now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
		m.now = func() time.Time { return now }
		m.sleep = func(context.Context, time.Duration) error { now = now.Add(time.Second); return nil }
		d, err := node.NewDispatcher(p, m)
		if err != nil {
			t.Fatal(err)
		}
		f.repoDigests[digest("2")] = []string{"example/bad@" + digest("8")}
		f.repoDigests[digest("b")] = []string{"example/bad@" + digest("3")}
		f.add(&fakeContainer{Name: "bad", Image: "example/bad:1", ImageID: digest("b")})
		f.crash["example/bad:1@"+digest("8")] = true

		res, _, err := call(t, d, "container.update", map[string]any{"id": "bad"})
		var out map[string]string
		_ = json.Unmarshal(res, &out)
		prev := "example/bad:1@" + digest("3")
		if err != nil || out["outcome"] != "rolled_back" || out["current"] != prev {
			t.Fatalf("containerd %v: %v %v", containerd, out, err)
		}
		if c := f.byName("bad"); c.Image != prev || c.ImageID != digest("b") || c.State != "running" {
			t.Fatalf("containerd %v: bad now %+v", containerd, c)
		}
		again := slices.Contains(f.pulled, "docker.io/example/bad@"+digest("3"))
		if again != containerd {
			t.Errorf("containerd %v: pulled %q", containerd, f.pulled)
		}
	}
}
