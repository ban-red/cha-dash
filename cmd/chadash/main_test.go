package main

import (
	"bytes"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dash.cha.sh/internal/config"
)

func runCmd(t *testing.T, env map[string]string, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(t.Context(), args, func(k string) string { return env[k] }, &out, &errb)
	return code, out.String(), errb.String()
}

func TestCommands(t *testing.T) {
	if code, _, stderr := runCmd(t, nil); code != 2 || !strings.Contains(stderr, "Usage") {
		t.Fatalf("no args: %d %q", code, stderr)
	}
	if code, stdout, _ := runCmd(t, nil, "version"); code != 0 || !strings.HasPrefix(stdout, "chadash dev (commit dev") {
		t.Fatalf("version: %d %q", code, stdout)
	}
	if code, stdout, _ := runCmd(t, nil, "help"); code != 0 || !strings.Contains(stdout, "healthcheck") {
		t.Fatalf("help: %d %q", code, stdout)
	}
	if code, _, stderr := runCmd(t, nil, "frobnicate"); code != 2 || !strings.Contains(stderr, "unknown command") {
		t.Fatalf("unknown: %d %q", code, stderr)
	}
	if code, _, stderr := runCmd(t, nil, "serve", "-h"); code != 0 || !strings.Contains(stderr, "CHADASH_DATA_DIR") {
		t.Fatalf("serve -h: %d %q", code, stderr)
	}
	// Bad configuration fails before anything starts.
	if code, _, stderr := runCmd(t, map[string]string{"CHADASH_LOG_LEVEL": "loud"}, "serve"); code != 2 || !strings.Contains(stderr, "log level") {
		t.Fatalf("serve with bad env: %d %q", code, stderr)
	}
}

func TestHealthcheck(t *testing.T) {
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
	}))
	defer srv.Close()
	env := map[string]string{"CHADASH_LISTEN": srv.Listener.Addr().String()}

	if code, _, stderr := runCmd(t, env, "healthcheck"); code != 0 {
		t.Fatalf("healthy: %d %q", code, stderr)
	}
	status = http.StatusServiceUnavailable
	if code, _, _ := runCmd(t, env, "healthcheck"); code != 1 {
		t.Fatalf("unhealthy: %d", code)
	}
	// The flag wins over the environment.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	closed := ln.Addr().String()
	ln.Close()
	if code, _, _ := runCmd(t, env, "healthcheck", "--listen", closed, "--timeout", "1s"); code != 1 {
		t.Fatalf("nothing listening: %d", code)
	}
	if code, _, _ := runCmd(t, env, "healthcheck", "--bogus"); code != 1 {
		t.Fatalf("bad flag should report unhealthy (1), got %d", code)
	}
}

func TestHealthURL(t *testing.T) {
	for in, want := range map[string]string{
		":3696":          "http://127.0.0.1:3696/healthz",
		"0.0.0.0:80":     "http://127.0.0.1:80/healthz",
		"[::]:3696":      "http://[::1]:3696/healthz",
		"10.1.2.3:3696":  "http://10.1.2.3:3696/healthz",
		"localhost:9000": "http://localhost:9000/healthz",
	} {
		if got := healthURL(in); got != want {
			t.Errorf("healthURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func testConfig(t *testing.T, dataDir string) config.Config {
	t.Helper()
	return config.Config{Listen: "127.0.0.1:0", DataDir: dataDir, LogFormat: "text", History: "standard"}
}

func TestNewAppFirstBoot(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data") // doesn't exist yet
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))

	a, err := newApp(t.Context(), testConfig(t, dataDir), log)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(a.handler)
	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("healthz: %v %v", resp, err)
	}
	resp.Body.Close()
	srv.Close()
	if _, ok := a.graph.Get("chadash:hub"); !ok {
		t.Fatal("the hub should know about itself")
	}
	if err := a.close(); err != nil {
		t.Fatal(err)
	}

	for path, want := range map[string]os.FileMode{
		dataDir:                              0o700,
		filepath.Join(dataDir, "secret.key"): 0o600,
		filepath.Join(dataDir, "chadash.db"): 0o600,
	} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != want {
			t.Errorf("%s mode = %o, want %o", path, got, want)
		}
	}
	for _, want := range []string{"applied migration", "generated vault key", "first run: no owner account yet"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("logs lack %q:\n%s", want, logs.String())
		}
	}

	// A second boot reuses the key.
	key, _ := os.ReadFile(filepath.Join(dataDir, "secret.key"))
	a, err = newApp(t.Context(), testConfig(t, dataDir), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	a.close()
	if again, _ := os.ReadFile(filepath.Join(dataDir, "secret.key")); !bytes.Equal(key, again) {
		t.Fatal("vault key changed across restarts")
	}
}

func TestNewAppDemo(t *testing.T) {
	cfg := testConfig(t, t.TempDir())
	cfg.Demo, cfg.DemoAt, cfg.DemoSeed = true, config.DefaultDemoAt, 1
	var logs bytes.Buffer
	a, err := newApp(t.Context(), cfg, slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()
	if a.demo == nil {
		t.Fatal("demo mode without a driver")
	}
	if _, ok := a.graph.Get("app:chadash"); !ok {
		t.Fatal("the Demo Home isn't in the graph")
	}
	if _, ok := a.graph.Get("chadash:hub"); ok {
		t.Fatal("in demo mode the Demo Home's app:chadash stands for the hub")
	}
	// Demo mode is still a normal hub: first-run setup applies.
	for _, want := range []string{"demo mode", "sim_time=2026-10-03T14:05:00Z", "first run: no owner account yet"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("logs lack %q:\n%s", want, logs.String())
		}
	}
	srv := httptest.NewServer(a.handler)
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/setup")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("setup status: %v %v", resp, err)
	}
	resp.Body.Close()
}

func TestNewAppExplicitKeyMissing(t *testing.T) {
	cfg := testConfig(t, t.TempDir())
	cfg.SecretKeyFile = filepath.Join(t.TempDir(), "missing.key")
	if _, err := newApp(t.Context(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil))); err == nil {
		t.Fatal("a missing CHADASH_SECRET_KEY_FILE must stop the hub")
	}
}

func TestHistoryIsRecordedOutsideDemoMode(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, tc := range []struct {
		name string
		edit func(*config.Config)
		want bool
	}{
		{"standard", func(*config.Config) {}, true},
		{"off", func(c *config.Config) { c.History = "off" }, false},
		{"demo", func(c *config.Config) { c.Demo = true }, false},
		{"own dir", func(c *config.Config) { c.HistoryDir = filepath.Join(c.DataDir, "ssd") }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig(t, t.TempDir())
			tc.edit(&cfg)
			a, err := newApp(t.Context(), cfg, log)
			if err != nil {
				t.Fatal(err)
			}
			_, statErr := os.Stat(cfg.HistoryPath())
			if (a.history != nil) != tc.want || (statErr == nil) != tc.want {
				t.Errorf("recorder %v, history.db stat %v; want recording %v", a.history != nil, statErr, tc.want)
			}
			if err := a.close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
