//go:build engine

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dash.cha.sh/internal/auth"
	"dash.cha.sh/internal/node"
	nodedocker "dash.cha.sh/internal/node/docker"
	nodestacks "dash.cha.sh/internal/node/stacks"
	"dash.cha.sh/internal/nodeproto"
	"dash.cha.sh/internal/testengine"
)

// System tests (layer 4 in docs/plan/17-test-automation.md), in process: the
// whole hub from newApp, with test automation and the test clock, and a
// real node beside it, its Docker and stacks modules on a real Docker
// Engine, paired through the hub's local pairing. Nothing is built: the
// hub and the node are this test binary. The browser half of layer 4 is
// web/e2e; this half needs no browser, only the HTTP API a script uses.

// system is one hub and one node.
type system struct {
	t      *testing.T
	e      *testengine.Engine
	srv    *httptest.Server
	dir    string       // the hub's data directory
	c      *http.Client // signed in as the owner with the automation key, stepped up
	nodeID string
}

func newSystem(t *testing.T) *system {
	t.Helper()
	e := testengine.New(t)
	dir := t.TempDir()
	cfg := testConfig(t, dir)
	cfg.TestAutomation, cfg.TestClock = true, true
	cfg.NodePairingFile = filepath.Join(dir, "pairing")
	cfg.NodePairingFrom = []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128")}
	a, err := newApp(t.Context(), cfg, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.close() })
	srv := httptest.NewServer(a.handler)
	t.Cleanup(srv.Close)
	s := &system{t: t, e: e, srv: srv, dir: dir}

	// The owner, then a session from the automation key: stepped up, as a
	// script's would be on a test hub.
	jar, _ := cookiejar.New(nil)
	setup := &http.Client{Jar: jar}
	if code, body := s.do(setup, "POST", "/api/setup", map[string]string{"username": "owner", "password": "correct horse battery staple"}, nil); code != http.StatusCreated {
		t.Fatalf("setup: %d %s", code, body)
	}
	key, err := os.ReadFile(filepath.Join(dir, auth.AutomationKeyFile))
	if err != nil {
		t.Fatal(err)
	}
	jar2, _ := cookiejar.New(nil)
	s.c = &http.Client{Jar: jar2}
	if code, body := s.do(s.c, "POST", "/api/auth/automation", nil, map[string]string{auth.AutomationHeader: strings.TrimSpace(string(key))}); code != http.StatusOK {
		t.Fatalf("automation sign-in: %d %s", code, body)
	}

	// The node: the real modules, on the engine, with a short health gate.
	policy := node.DefaultPolicy()
	policy.Stacks.Root = t.TempDir()
	dm, err := nodedocker.New(nodedocker.Options{Host: e.Host, Policy: policy, SelfID: "-"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dm.Close() })
	sdk, err := nodestacks.NewSDK(e.Host)
	if err != nil {
		t.Fatal(err)
	}
	sm, err := nodestacks.New(nodestacks.Options{Root: policy.Stacks.Root, Docker: sdk, ProtectContainer: policy.Protected,
		Wait: time.Minute, Soak: 3 * time.Second, RollbackSoak: 2 * time.Second, Poll: 500 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	disp, err := node.NewDispatcher(policy, dm, sm)
	if err != nil {
		t.Fatal(err)
	}
	ident, err := node.OpenIdentity(filepath.Join(t.TempDir(), "identity"))
	if err != nil {
		t.Fatal(err)
	}
	tok, err := os.ReadFile(cfg.NodePairingFile)
	if err != nil {
		t.Fatal(err)
	}
	nc, err := node.NewClient(node.Config{HubURL: "ws" + strings.TrimPrefix(srv.URL, "http") + nodeproto.Path, Name: "engine",
		Identity: ident, Dispatcher: disp, Pairing: strings.TrimSpace(string(tok))})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = nc.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	deadline := time.Now().Add(20 * time.Second)
	for {
		var list struct {
			Nodes []struct {
				ID        string `json:"id"`
				Name      string `json:"name"`
				Connected bool   `json:"connected"`
			} `json:"nodes"`
		}
		code, body := s.do(s.c, "GET", "/api/nodes", nil, nil)
		if code == http.StatusOK && json.Unmarshal(body, &list) == nil {
			for _, n := range list.Nodes {
				if n.Name == "engine" && n.Connected {
					s.nodeID = n.ID
				}
			}
		}
		if s.nodeID != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the node never connected: %d %s", code, body)
		}
		time.Sleep(100 * time.Millisecond)
	}
	return s
}

// do sends a JSON request and returns the status and body.
func (s *system) do(c *http.Client, method, path string, body any, headers map[string]string) (int, []byte) {
	s.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(s.t.Context(), method, s.srv.URL+path, r)
	if err != nil {
		s.t.Fatal(err)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// run plans an action, approves it by its hash, and waits for its task.
func (s *system) run(action, target string) map[string]any {
	s.t.Helper()
	return s.runWith(action, target, nil)
}

// runWith is run with params.
func (s *system) runWith(action, target string, params any) map[string]any {
	s.t.Helper()
	req := map[string]any{"action": action, "target": target}
	if params != nil {
		req["params"] = params
	}
	code, body := s.do(s.c, "POST", "/api/actions/plan", req, nil)
	var plan struct {
		ID   string `json:"id"`
		Hash string `json:"hash"`
	}
	if code != http.StatusOK || json.Unmarshal(body, &plan) != nil || plan.ID == "" {
		s.t.Fatalf("plan %s on %s: %d %s", action, target, code, body)
	}
	code, body = s.do(s.c, "POST", "/api/plans/"+plan.ID+"/approve", map[string]string{"hash": plan.Hash}, nil)
	var ok struct {
		Task struct {
			ID string `json:"id"`
		} `json:"task"`
	}
	if code != http.StatusAccepted || json.Unmarshal(body, &ok) != nil || ok.Task.ID == "" {
		s.t.Fatalf("approve %s: %d %s", action, code, body)
	}
	deadline := time.Now().Add(3 * time.Minute)
	for {
		code, body = s.do(s.c, "GET", "/api/tasks/"+ok.Task.ID, nil, nil)
		var task map[string]any
		if code == http.StatusOK && json.Unmarshal(body, &task) == nil {
			if st, _ := task["state"].(string); st == "succeeded" || st == "failed" {
				return task
			}
		}
		if time.Now().After(deadline) {
			s.t.Fatalf("task %s didn't finish: %d %s", ok.Task.ID, code, body)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// saveDraft saves a stack's compose file as its draft.
func (s *system) saveDraft(stack, content string) {
	s.t.Helper()
	rev := int64(0)
	if code, body := s.do(s.c, "GET", "/api/stacks/"+s.nodeID+"/"+stack, nil, nil); code == http.StatusOK {
		var cur struct {
			Draft *struct {
				Revision int64 `json:"revision"`
			} `json:"draft"`
		}
		if json.Unmarshal(body, &cur) == nil && cur.Draft != nil {
			rev = cur.Draft.Revision
		}
	}
	if code, body := s.do(s.c, "PUT", "/api/stacks/"+s.nodeID+"/"+stack, map[string]any{"content": content, "revision": rev}, nil); code != http.StatusOK {
		s.t.Fatalf("save the draft of %s: %d %s", stack, code, body)
	}
}

// A stack deployed through the hub's API reaches the node and the engine;
// a broken version of it rolls back on its own, and the hub's task says so.
func TestSystemStackDeployAndRollBack(t *testing.T) {
	s := newSystem(t)
	reg := s.e.Registry()
	v1 := s.e.Push(reg, reg.Ref("app", "1"), testengine.Healthy, "1")
	s.e.Push(reg, reg.Ref("app", "bad"), testengine.NeverHealthy, "1")
	stack := s.e.Project("app")
	target := "stack:" + s.nodeID + "/" + stack

	s.saveDraft(stack, fmt.Sprintf("services:\n  web:\n    image: %s\n", reg.Ref("app", "1")))
	if task := s.run("stack.deploy", target); task["state"] != "succeeded" {
		t.Fatalf("deploy v1: %v", task)
	}

	s.saveDraft(stack, fmt.Sprintf("services:\n  web:\n    image: %s\n", reg.Ref("app", "bad")))
	task := s.run("stack.deploy", target)
	if task["state"] != "failed" || !strings.Contains(fmt.Sprint(task["error"]), "rolled_back") {
		t.Fatalf("a deploy that can't pass the gate should fail as rolled back: %v", task)
	}
	_, digest, _ := strings.Cut(v1, "@")
	code, body := s.do(s.c, "GET", "/api/stacks/"+s.nodeID+"/"+stack, nil, nil)
	if code != http.StatusOK || !strings.Contains(string(body), "@"+digest) {
		t.Fatalf("after the rollback the stack should be v1, pinned: %d %s", code, body)
	}
}

// automationKey is the test hub's key, for the routes that take it.
func (s *system) automationKey() string {
	s.t.Helper()
	key, err := os.ReadFile(filepath.Join(s.dir, auth.AutomationKeyFile))
	if err != nil {
		s.t.Fatal(err)
	}
	return strings.TrimSpace(string(key))
}

// waitFor polls f until it says done, or fails the test with what it last said.
func (s *system) waitFor(within time.Duration, what string, f func() (bool, string)) {
	s.t.Helper()
	deadline := time.Now().Add(within)
	for {
		done, last := f()
		if done {
			return
		}
		if time.Now().After(deadline) {
			s.t.Fatalf("%s within %s: %s", what, within, last)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// A container update scheduled into a maintenance window, end to end: the
// hub's Docker instance reads the engine through the node, Check now finds
// the tag moved in the registry, a rollout is scheduled into a window an
// hour away, and nothing runs until the test clock reaches it; then the
// node updates the real container through its health gate. 14b's manual
// test 1, with no lab, no person and no waiting.
func TestSystemRolloutRunsInItsWindow(t *testing.T) {
	s := newSystem(t)
	reg := s.e.Registry()
	ref := reg.Ref("app", "1")
	s.e.Push(reg, ref, testengine.Healthy, "1")
	id := s.e.Start("app", ref, nil)
	s.e.WaitHealthy(id, 30*time.Second)
	name := strings.TrimPrefix(s.e.Inspect(id).Name, "/")

	if code, body := s.do(s.c, "POST", "/api/integrations/instances", map[string]any{
		"integration": "docker", "name": "engine", "config": map[string]any{"endpoint": "node://engine"},
	}, nil); code != http.StatusCreated {
		t.Fatalf("the Docker instance: %d %s", code, body)
	}

	// The tag moves; Check now finds it.
	v2 := s.e.Push(reg, ref, testengine.Healthy, "2")
	_, digest2, _ := strings.Cut(v2, "@")
	var row struct {
		Resource, Name, Engine, State string
	}
	s.waitFor(90*time.Second, "an available update for "+name, func() (bool, string) {
		s.do(s.c, "POST", "/api/updates/check", nil, nil)
		code, body := s.do(s.c, "GET", "/api/updates", nil, nil)
		var list struct {
			Updates []struct {
				Resource string `json:"resource"`
				Name     string `json:"name"`
				Engine   string `json:"engine"`
				State    string `json:"state"`
				Reason   string `json:"reason"`
			} `json:"updates"`
		}
		_ = json.Unmarshal(body, &list)
		for _, u := range list.Updates {
			if u.Name == name {
				row.Resource, row.Name, row.Engine, row.State = u.Resource, u.Name, u.Engine, u.State
				return u.State == "available", fmt.Sprintf("%s: %s", u.State, u.Reason)
			}
		}
		return false, fmt.Sprintf("%d, %s not listed: %s", code, name, body)
	})

	// A window an hour from now on the hub's clock, and a rollout into it.
	var clk struct {
		Now time.Time `json:"now"`
	}
	key := map[string]string{auth.AutomationHeader: s.automationKey()}
	_, body := s.do(s.c, "GET", "/api/test/clock", nil, key)
	if err := json.Unmarshal(body, &clk); err != nil || clk.Now.IsZero() {
		t.Fatalf("the test clock: %s", body)
	}
	code, body := s.do(s.c, "POST", "/api/windows", map[string]any{"name": "Tonight", "hosts": []string{row.Engine},
		"starts_at": clk.Now.Add(time.Hour), "ends_at": clk.Now.Add(2 * time.Hour)}, nil)
	var w struct {
		ID string `json:"id"`
	}
	if code != http.StatusCreated && code != http.StatusOK || json.Unmarshal(body, &w) != nil || w.ID == "" {
		t.Fatalf("the window: %d %s", code, body)
	}
	if task := s.runWith("updates.rollout", "chadash:updates", map[string]any{"targets": []string{row.Resource}, "window": w.ID}); task["state"] != "succeeded" {
		t.Fatalf("scheduling the rollout: %v", task)
	}
	rollout := func() (string, string) {
		_, body := s.do(s.c, "GET", "/api/updates/rollouts", nil, nil)
		var list struct {
			Rollouts []struct {
				ID    string `json:"id"`
				State string `json:"state"`
			} `json:"rollouts"`
		}
		_ = json.Unmarshal(body, &list)
		if len(list.Rollouts) == 0 {
			return "", string(body)
		}
		return list.Rollouts[0].State, string(body)
	}
	if st, body := rollout(); st != "scheduled" {
		t.Fatalf("the rollout should wait for its window: %s", body)
	}
	time.Sleep(time.Second)
	if c := s.e.Inspect(name); strings.Contains(c.Config.Image, digest2) {
		t.Fatal("the container was updated before its window")
	}

	// The clock reaches the window: the rollout runs, and the container
	// runs v2.
	if code, body := s.do(s.c, "POST", "/api/test/clock", map[string]string{"advance": "61m"}, key); code != http.StatusOK {
		t.Fatalf("moving the clock: %d %s", code, body)
	}
	s.waitFor(3*time.Minute, "the rollout to succeed in its window", func() (bool, string) {
		st, body := rollout()
		return st == "succeeded", body
	})
	if c := s.e.Inspect(name); !c.State.Running || c.Config.Image != ref+"@"+digest2 {
		t.Fatalf("after the rollout the container runs %s (running %v), want v2", c.Config.Image, c.State.Running)
	}
}
