//go:build engine

package docker

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"dash.cha.sh/internal/node"
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
