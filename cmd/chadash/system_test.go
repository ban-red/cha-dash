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
	s := &system{t: t, e: e, srv: srv}

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
	code, body := s.do(s.c, "POST", "/api/actions/plan", map[string]string{"action": action, "target": target}, nil)
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
