//go:build engine

package docker

import (
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/client"

	"dash.cha.sh/internal/node"
	"dash.cha.sh/internal/node/registry"
	"dash.cha.sh/internal/testengine"
)

// Engine tests: the Docker module against a real Docker Engine and a
// throwaway registry (docs/plan/17-test-automation.md, layer 3). This is
// what v0.4's manual rollout runs checked with a canary on the lab.

func engineModule(t *testing.T, e *testengine.Engine) (*Module, *node.Dispatcher) {
	t.Helper()
	p, err := node.ParsePolicy(nil)
	if err != nil {
		t.Fatal(err)
	}
	m, err := New(Options{Host: e.Host, Policy: p, SelfID: "-"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	// The test registry speaks plain HTTP on loopback.
	m.registry = func(domain string) string { return "http://" + domain }
	d, err := node.NewDispatcher(p, m)
	if err != nil {
		t.Fatal(err)
	}
	return m, d
}

// container.update: unchanged while the tag stays; a broken image of each
// kind rolls back to the digest that ran; a good one goes through.
// image.check sees each move of the tag.
func TestEngineContainerUpdate(t *testing.T) {
	e := testengine.New(t)
	reg := e.Registry()
	ref := reg.Ref("app", "1")
	v1 := e.Push(reg, ref, testengine.Healthy, "1")
	id := e.Start("app", ref, nil)
	e.WaitHealthy(id, 30*time.Second)
	m, d := engineModule(t, e)
	// image.check keeps an answer for a minute; the test moves the
	// module's clock past that between checks.
	var skew time.Duration
	m.now = func() time.Time { return time.Now().Add(skew) }
	name := strings.TrimPrefix(e.Inspect(id).Name, "/")

	update := func() map[string]string {
		t.Helper()
		res, _, err := call(t, d, "container.update", map[string]any{"id": name})
		if err != nil {
			t.Fatalf("update: %v", err)
		}
		var out map[string]string
		_ = json.Unmarshal(res, &out)
		return out
	}
	check := func(fresh bool) string {
		t.Helper()
		res, _, err := call(t, d, "image.check", map[string]any{"ids": []string{name}, "fresh": fresh})
		if err != nil {
			t.Fatalf("image.check: %v", err)
		}
		var out struct {
			Images []struct{ State, Reason string }
		}
		_ = json.Unmarshal(res, &out)
		if len(out.Images) != 1 {
			t.Fatalf("image.check: %s", res)
		}
		return out.Images[0].State
	}

	if got := check(false); got != updateCurrent {
		t.Errorf("image.check before any push: %s", got)
	}
	if out := update(); out["outcome"] != outcomeUnchanged {
		t.Fatalf("nothing pushed: %v", out)
	}

	_, digest1, _ := strings.Cut(v1, "@")
	for i, bad := range []testengine.Canary{testengine.NeverHealthy, testengine.ExitsOne} {
		e.Push(reg, ref, bad, "bad"+string(rune('a'+i)))
		out := update()
		if out["outcome"] != outcomeRolledBack {
			t.Fatalf("%s: %v", bad, out)
		}
		if out["current"] != ref+"@"+digest1 {
			t.Errorf("%s: after the rollback it runs %q, want v1's digest", bad, out["current"])
		}
		c := e.Inspect(name)
		if !c.State.Running || c.Config.Image != ref+"@"+digest1 {
			t.Errorf("%s: the container after the rollback: %s, running %v", bad, c.Config.Image, c.State.Running)
		}
	}

	v2 := e.Push(reg, ref, testengine.Healthy, "2")
	// Without fresh the answer is kept for CheckEvery; Check now (fresh)
	// asks again once it's older than freshAfter.
	skew += 2 * freshAfter
	if got := check(false); got != updateCurrent {
		t.Errorf("image.check without fresh should keep its answer: %s", got)
	}
	if got := check(true); got != updateAvailable {
		t.Errorf("image.check after v2: %s", got)
	}
	_, digest2, _ := strings.Cut(v2, "@")
	if out := update(); out["outcome"] != outcomeDeployed || out["current"] != ref+"@"+digest2 {
		t.Fatalf("v2: %v", out)
	}
	e.WaitHealthy(name, 30*time.Second)
}

// A container from a registry behind htpasswd: image.check and
// container.update log in where images.registries lists the registry, and
// say it needs a login where it doesn't; the password is in no reply,
// item, error or log line.
func TestEnginePrivateRegistry(t *testing.T) {
	const user, password = "robot", "fake-pw-not-real-0000"
	e := testengine.New(t)
	reg := e.Registry(testengine.WithLogin(user, password))
	ref := reg.Ref("app", "1")
	e.Push(reg, ref, testengine.Healthy, "1")
	// The test starts the container itself, with the login.
	ctx, cancel := e.Ctx()
	defer cancel()
	pr, err := e.Cli.ImagePull(ctx, ref, client.ImagePullOptions{RegistryAuth: reg.Auth()})
	if err != nil {
		t.Fatal(err)
	}
	if err := pr.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	id := e.Start("private", ref, nil)
	e.WaitHealthy(id, 30*time.Second)
	name := strings.TrimPrefix(e.Inspect(id).Name, "/")

	auth := base64.StdEncoding.EncodeToString([]byte(user + ":" + password))
	config := filepath.Join(t.TempDir(), "docker-config.json")
	b, _ := json.Marshal(map[string]any{"auths": map[string]any{reg.Host: map[string]string{"auth": auth}}})
	if err := os.WriteFile(config, b, 0o600); err != nil {
		t.Fatal(err)
	}
	var logs strings.Builder
	log := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	var seen strings.Builder
	module := func(allowed ...string) *node.Dispatcher {
		p, err := node.ParsePolicy(nil)
		if err != nil {
			t.Fatal(err)
		}
		m, err := New(Options{Host: e.Host, Policy: p, SelfID: "-", Logins: registry.NewLogins(config, allowed, log), Log: log})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { m.Close() })
		m.registry = func(domain string) string { return "http://" + domain }
		d, err := node.NewDispatcher(p, m)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	run := func(d *node.Dispatcher, verb string, req any) (json.RawMessage, error) {
		t.Helper()
		res, items, err := call(t, d, verb, req)
		seen.Write(res)
		for _, it := range items {
			seen.Write(it)
		}
		if err != nil {
			seen.WriteString(err.Error())
		}
		return res, err
	}
	state := func(res json.RawMessage) (string, string) {
		var out struct {
			Images []struct{ State, Reason string }
		}
		_ = json.Unmarshal(res, &out)
		if len(out.Images) != 1 {
			t.Fatalf("image.check: %s", res)
		}
		return out.Images[0].State, out.Images[0].Reason
	}

	want := reg.Host + " needs a login, and this node has none for it."
	without, with := module("registry.lab.example"), module(reg.Host)
	res, err := run(without, "image.check", map[string]any{"ids": []string{name}, "fresh": true})
	if st, why := state(res); err != nil || st != updateCantCheck || why != want {
		t.Fatalf("image.check without a login: %s %q %v", st, why, err)
	}
	res, err = run(with, "image.check", map[string]any{"ids": []string{name}, "fresh": true})
	if st, why := state(res); err != nil || st != updateCurrent {
		t.Fatalf("image.check with the login: %s %q %v", st, why, err)
	}

	e.Push(reg, ref, testengine.Healthy, "2")
	if _, err := run(without, "container.update", map[string]any{"id": name}); err == nil || err.Error() != want {
		t.Fatalf("container.update without a login: %v", err)
	}
	res, err = run(with, "container.update", map[string]any{"id": name})
	var out map[string]string
	_ = json.Unmarshal(res, &out)
	if err != nil || out["outcome"] != outcomeDeployed {
		t.Fatalf("container.update with the login: %s %v", res, err)
	}
	for _, secret := range []string{password, auth} {
		if strings.Contains(seen.String(), secret) || strings.Contains(logs.String(), secret) {
			t.Error("a reply, item, error or log line carries the login")
		}
	}
}
