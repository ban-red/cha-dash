//go:build engine

package testengine

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// The harness does what it says: each canary behaves as its kind, a new
// version moves the tag, and a registry with a login refuses a pull
// without one.
func TestEngineCanaries(t *testing.T) {
	e := New(t)
	reg := e.Registry()

	ref := reg.Ref("canary", "1")
	v1 := e.Push(reg, ref, Healthy, "1")
	id := e.Start("healthy", ref, nil)
	e.WaitHealthy(id, 30*time.Second)

	v2 := e.Push(reg, ref, Healthy, "2")
	if v1 == v2 || !strings.HasPrefix(v1, reg.Host+"/canary@sha256:") {
		t.Fatalf("a new version should move the tag: %s, %s", v1, v2)
	}

	exits := e.Start("exits", e.Push(reg, reg.Ref("exits", "1"), ExitsOne, "1"), nil)
	ctx, cancel := e.Ctx()
	defer cancel()
	w := e.Cli.ContainerWait(ctx, exits, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
	select {
	case r := <-w.Result:
		if r.StatusCode != 1 {
			t.Errorf("exits-one exited %d", r.StatusCode)
		}
	case err := <-w.Error:
		t.Fatal(err)
	}

	never := e.Start("never", e.Push(reg, reg.Ref("never", "1"), NeverHealthy, "1"), nil)
	deadline := time.Now().Add(30 * time.Second)
	for {
		c := e.Inspect(never)
		if c.State.Health != nil && c.State.Health.Status == container.Unhealthy {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("never-healthy should turn unhealthy: %+v", c.State.Health)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func TestEngineRegistryWithLogin(t *testing.T) {
	e := New(t)
	reg := e.Registry(WithLogin("tester", "not-a-real-password"))
	ref := reg.Ref("private", "1")
	pinned := e.Push(reg, ref, Healthy, "1")

	// Drop the local copy, then pull it back: with the login, and without.
	ctx, cancel := e.Ctx()
	defer cancel()
	if _, err := e.Cli.ImageRemove(ctx, pinned, client.ImageRemoveOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	if r, err := e.Cli.ImagePull(ctx, ref, client.ImagePullOptions{}); err == nil {
		if err = r.Wait(ctx); err == nil {
			t.Fatal("a pull without the login should be refused")
		}
	}
	r, err := e.Cli.ImagePull(ctx, ref, client.ImagePullOptions{RegistryAuth: reg.Auth()})
	if err == nil {
		err = r.Wait(ctx)
	}
	if err != nil {
		t.Fatalf("a pull with the login: %v", err)
	}
}

// TestNoLeaks runs after the suite (bun run test:engine): nothing any run
// started may be left behind.
func TestNoLeaks(t *testing.T) {
	if os.Getenv("CHADASH_ENGINE_LEAKCHECK") != "1" {
		t.Skip("set CHADASH_ENGINE_LEAKCHECK=1 to check for leftovers")
	}
	cli, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	leaks, err := Leaks(ctx, cli)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range leaks {
		t.Error("left behind: " + l)
	}
}
