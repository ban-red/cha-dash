package main

import (
	"bytes"
	"encoding/json"
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
	"dash.cha.sh/internal/config"
)

// System tests (layer 4 in docs/plan/17-test-automation.md), in process: the
// whole hub from newApp, with test automation and the test clock. Nothing is
// built: the hub is this test binary. The tests that need a node and a real
// Docker Engine are in system_test.go (-tags engine); the browser half of
// layer 4 is web/e2e. This half needs no browser, only the HTTP API a script
// uses.

// system is one hub.
type system struct {
	t   *testing.T
	srv *httptest.Server
	dir string       // the hub's data directory
	c   *http.Client // signed in as the owner with the automation key, stepped up
}

// newHub is the whole hub from newApp, with test automation and the test
// clock, its owner set up and signed in through the automation key.
func newHub(t *testing.T) (*system, config.Config) {
	t.Helper()
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
	s := &system{t: t, srv: srv, dir: dir}

	// The owner, then a session from the automation key: stepped up, as a
	// script's would be on a test hub.
	jar, _ := cookiejar.New(nil)
	setup := &http.Client{Jar: jar}
	if code, body := s.do(setup, "POST", "/api/setup", map[string]string{"username": "owner", "password": "correct horse battery staple"}, nil); code != http.StatusCreated {
		t.Fatalf("setup: %d %s", code, body)
	}
	jar2, _ := cookiejar.New(nil)
	s.c = &http.Client{Jar: jar2}
	if code, body := s.do(s.c, "POST", "/api/auth/automation", nil, map[string]string{auth.AutomationHeader: s.automationKey()}); code != http.StatusOK {
		t.Fatalf("automation sign-in: %d %s", code, body)
	}
	return s, cfg
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
