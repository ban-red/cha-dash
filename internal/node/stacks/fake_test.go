package stacks

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"dash.cha.sh/internal/node"
	"dash.cha.sh/internal/node/stacks/stackstest"
	"dash.cha.sh/internal/stackproto"
)

// startOutside pretends a project was started by the CLI, outside Cha
// Dash: its containers carry the hashes of content as loaded from file.
func startOutside(t *testing.T, d *stackstest.Docker, name, file, content string) {
	t.Helper()
	s := stackRef{name: name, dir: filepath.Dir(file), file: filepath.Base(file)}
	p, err := load(context.Background(), s, content, loadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Up(context.Background(), p, 0, func(Event) {}); err != nil {
		t.Fatal(err)
	}
	d.Reset()
}

var digestOf = stackstest.Digest

// ── a test node ──────────────────────────────────────────────────────────

type testNode struct {
	t      *testing.T
	m      *Module
	docker *stackstest.Docker
	root   string
	clock  time.Time
	mu     sync.Mutex
	// progress collects every progress message any verb streamed.
	progress []string
}

func newTestNode(t *testing.T, opts ...func(*Options)) *testNode {
	t.Helper()
	n := &testNode{t: t, docker: stackstest.New(), root: t.TempDir(), clock: time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)}
	o := Options{
		Root: n.root, Docker: n.docker, Soak: 20 * time.Second, RollbackSoak: 5 * time.Second, Poll: 2 * time.Second,
		Now: func() time.Time {
			n.mu.Lock()
			defer n.mu.Unlock()
			return n.clock
		},
		Sleep: func(_ context.Context, d time.Duration) error {
			n.mu.Lock()
			defer n.mu.Unlock()
			n.clock = n.clock.Add(d)
			return nil
		},
	}
	for _, f := range opts {
		f(&o)
	}
	m, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	n.m = m
	return n
}

// call runs a verb the way the node runtime would: JSON in, JSON out.
func (n *testNode) call(name string, req, resp any) error {
	n.t.Helper()
	i := slices.IndexFunc(n.m.Verbs(), func(v node.Verb) bool { return v.Name == name })
	if i < 0 {
		n.t.Fatalf("no verb %s", name)
	}
	raw, err := json.Marshal(req)
	if err != nil {
		n.t.Fatal(err)
	}
	out, err := n.m.Verbs()[i].Handle(context.Background(), raw, func(p json.RawMessage) {
		n.mu.Lock()
		n.progress = append(n.progress, string(p))
		n.mu.Unlock()
	})
	if err != nil {
		return err
	}
	if resp != nil {
		if err := json.Unmarshal(out, resp); err != nil {
			n.t.Fatal(err)
		}
	}
	return nil
}

func (n *testNode) must(name string, req, resp any) {
	n.t.Helper()
	if err := n.call(name, req, resp); err != nil {
		n.t.Fatalf("%s: %v", name, err)
	}
}

func (n *testNode) deploy(stack, content string, secrets map[string]string) stackproto.DeployResult {
	n.t.Helper()
	var res stackproto.DeployResult
	n.must("stack.deploy", map[string]any{"stack": stack, "content": content, "secrets": secrets, "by": "sam", "via": "web", "ref": "t_test"}, &res)
	return res
}

func (n *testNode) history(stack string) []string {
	n.t.Helper()
	var st stackproto.Stack
	n.must("stack.get", map[string]string{"stack": stack}, &st)
	var out []string
	for _, c := range st.History {
		out = append(out, c.Kind+" "+c.Subject)
	}
	return out
}
