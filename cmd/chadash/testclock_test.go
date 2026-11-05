package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dash.cha.sh/internal/auth"
)

// The test clock on a whole hub: mounted only with CHADASH_TEST_CLOCK, the
// automation key is its credential, and it moves forward only
// (docs/plan/17-test-automation.md, H2).
func TestTestClock(t *testing.T) {
	clockCall := func(t *testing.T, srv *httptest.Server, method, body, key string) (int, string) {
		t.Helper()
		req, _ := http.NewRequestWithContext(t.Context(), method, srv.URL+"/api/test/clock", strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if key != "" {
			req.Header.Set(auth.AutomationHeader, key)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	start := func(t *testing.T, testClock bool) (*httptest.Server, string) {
		t.Helper()
		dir := t.TempDir()
		cfg := testConfig(t, dir)
		cfg.TestAutomation, cfg.TestClock = true, testClock
		a, err := newApp(t.Context(), cfg, slog.New(slog.DiscardHandler))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { a.close() })
		srv := httptest.NewServer(a.handler)
		t.Cleanup(srv.Close)
		key, err := os.ReadFile(filepath.Join(dir, auth.AutomationKeyFile))
		if err != nil {
			t.Fatal(err)
		}
		return srv, strings.TrimSpace(string(key))
	}

	t.Run("off without the flag", func(t *testing.T) {
		srv, key := start(t, false)
		if code, _ := clockCall(t, srv, "GET", "", key); code != http.StatusNotFound {
			t.Errorf("GET without CHADASH_TEST_CLOCK = %d, want 404", code)
		}
	})

	t.Run("on", func(t *testing.T) {
		srv, key := start(t, true)
		if code, _ := clockCall(t, srv, "GET", "", ""); code != http.StatusUnauthorized {
			t.Errorf("GET without the key = %d", code)
		}
		if code, _ := clockCall(t, srv, "POST", `{"advance":"1h"}`, "not-the-key"); code != http.StatusUnauthorized {
			t.Errorf("POST with a wrong key = %d", code)
		}
		code, body := clockCall(t, srv, "POST", `{"advance":"168h"}`, key)
		if code != http.StatusOK || !strings.Contains(body, `"skew_s":604800`) {
			t.Fatalf("advance a week = %d %s", code, body)
		}
		for _, bad := range []string{`{"advance":"-1h"}`, `{"advance":"soon"}`, `{}`, `{"set":"2020-01-01T00:00:00Z"}`, `{"advance":"1h","set":"2099-01-01T00:00:00Z"}`} {
			if code, body := clockCall(t, srv, "POST", bad, key); code != http.StatusBadRequest || !strings.Contains(body, "only goes forward") {
				t.Errorf("POST %s = %d %s", bad, code, body)
			}
		}
		target := time.Now().Add(30 * 24 * time.Hour).UTC().Format(time.RFC3339)
		if code, body := clockCall(t, srv, "POST", `{"set":"`+target+`"}`, key); code != http.StatusOK || !strings.Contains(body, target[:13]) {
			t.Errorf("set = %d %s", code, body)
		}
		// It keeps running from there.
		if code, body := clockCall(t, srv, "GET", "", key); code != http.StatusOK || !strings.Contains(body, `"now":"`+target[:13]) {
			t.Errorf("GET after set = %d %s", code, body)
		}
	})
}
