package config

import (
	"bytes"
	"flag"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func load(t *testing.T, env map[string]string, args ...string) (Config, error) {
	t.Helper()
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	l := Bind(fs, func(k string) string { return env[k] })
	if err := fs.Parse(args); err != nil {
		t.Fatalf("parse: %v", err)
	}
	return l.Config()
}

func TestDefaults(t *testing.T) {
	c, err := load(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":3696" || c.DataDir != "/data" || c.LogLevel != slog.LevelInfo || c.LogFormat != "text" {
		t.Fatalf("unexpected defaults: %+v", c)
	}
	if p, explicit := c.KeyFile(); p != filepath.Join("/data", "secret.key") || explicit {
		t.Fatalf("KeyFile() = %q, %v", p, explicit)
	}
	if c.DBPath() != filepath.Join("/data", "chadash.db") {
		t.Fatalf("DBPath() = %q", c.DBPath())
	}
}

func TestEnvThenFlagsWin(t *testing.T) {
	env := map[string]string{
		"CHADASH_LISTEN":          "127.0.0.1:9000",
		"CHADASH_DATA_DIR":        "/srv/chadash",
		"CHADASH_LOG_LEVEL":       "debug",
		"CHADASH_LOG_FORMAT":      "json",
		"CHADASH_SECRET_KEY_FILE": "/run/secrets/key",
		"CHADASH_TRUSTED_PROXIES": "10.0.0.0/8, 192.0.2.7",
		"CHADASH_ALLOWED_ORIGINS": "https://Dash.Example.com",
	}
	c, err := load(t, env)
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != "127.0.0.1:9000" || c.DataDir != "/srv/chadash" || c.LogLevel != slog.LevelDebug || c.LogFormat != "json" {
		t.Fatalf("env not applied: %+v", c)
	}
	if p, explicit := c.KeyFile(); p != "/run/secrets/key" || !explicit {
		t.Fatalf("KeyFile() = %q, %v", p, explicit)
	}
	if len(c.TrustedProxies) != 2 || c.TrustedProxies[1].String() != "192.0.2.7/32" {
		t.Fatalf("proxies = %v", c.TrustedProxies)
	}
	if len(c.AllowedOrigins) != 1 || c.AllowedOrigins[0] != "https://dash.example.com" {
		t.Fatalf("origins = %v", c.AllowedOrigins)
	}

	c, err = load(t, env, "--listen", ":4000", "--log-level", "warn")
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":4000" || c.LogLevel != slog.LevelWarn {
		t.Fatalf("flags should win over env: %+v", c)
	}
	if c.DataDir != "/srv/chadash" {
		t.Fatalf("unset flag should keep env value, got %q", c.DataDir)
	}
}

func TestInvalidValues(t *testing.T) {
	_, err := load(t, map[string]string{
		"CHADASH_LISTEN":          "nope",
		"CHADASH_LOG_LEVEL":       "loud",
		"CHADASH_LOG_FORMAT":      "xml",
		"CHADASH_TRUSTED_PROXIES": "not-an-ip",
		"CHADASH_ALLOWED_ORIGINS": "ftp://x, https://x/path",
		"CHADASH_PUBLIC_URL":      "dash.example.com",
	})
	if err == nil {
		t.Fatal("expected errors")
	}
	for _, want := range []string{"listen address", "log level", "log format", "trusted proxy", "ftp://x", "https://x/path", "public URL"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestPublicURL(t *testing.T) {
	c, err := load(t, nil)
	if err != nil || c.PublicURL != "" {
		t.Fatalf("default public URL = %q, %v", c.PublicURL, err)
	}

	// Normalised, and trusted like an allowed origin (once).
	c, err = load(t, map[string]string{
		"CHADASH_PUBLIC_URL":      "https://Dash.Lab.Example/",
		"CHADASH_ALLOWED_ORIGINS": "https://other.example",
	})
	if err != nil {
		t.Fatal(err)
	}
	if c.PublicURL != "https://dash.lab.example" {
		t.Fatalf("public URL = %q", c.PublicURL)
	}
	if !slices.Equal(c.AllowedOrigins, []string{"https://other.example", "https://dash.lab.example"}) {
		t.Fatalf("allowed origins = %v", c.AllowedOrigins)
	}
	c, err = load(t, map[string]string{"CHADASH_ALLOWED_ORIGINS": "http://localhost:5173"}, "--public-url", "http://localhost:5173")
	if err != nil || c.PublicURL != "http://localhost:5173" || len(c.AllowedOrigins) != 1 {
		t.Fatalf("flag public URL = %q %v, %v", c.PublicURL, c.AllowedOrigins, err)
	}

	// Plain http on the LAN is accepted here; passkeys explain at use that they need HTTPS.
	if c, err := load(t, map[string]string{"CHADASH_PUBLIC_URL": "http://192.168.1.5:3696"}); err != nil || c.PublicURL != "http://192.168.1.5:3696" {
		t.Fatalf("http public URL = %q, %v", c.PublicURL, err)
	}
	for _, bad := range []string{"https://dash.example.com/sub", "ftp://dash.example.com", "https://user@dash.example.com", "https://"} {
		if _, err := load(t, map[string]string{"CHADASH_PUBLIC_URL": bad}); err == nil || !strings.Contains(err.Error(), "public URL") {
			t.Errorf("public URL %q: err = %v", bad, err)
		}
	}
}

func TestDemo(t *testing.T) {
	c, err := load(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Demo || c.DemoWallClock || !c.DemoLocal || c.DemoAt != DefaultDemoAt || c.DemoSeed != 1 {
		t.Fatalf("demo defaults: %+v", c)
	}

	c, err = load(t, map[string]string{"CHADASH_DEMO": "1", "CHADASH_DEMO_AT": "now", "CHADASH_DEMO_SEED": "42"})
	if err != nil {
		t.Fatal(err)
	}
	if !c.Demo || !c.DemoWallClock || c.DemoSeed != 42 {
		t.Fatalf("demo from env: %+v", c)
	}

	c, err = load(t, map[string]string{"CHADASH_DEMO": "false"}, "--demo", "--demo-at", "09:30:15", "--demo-seed", "-3")
	if err != nil {
		t.Fatal(err)
	}
	if !c.Demo || c.DemoWallClock || c.DemoLocal || c.DemoAt != 9*time.Hour+30*time.Minute+15*time.Second || c.DemoSeed != -3 {
		t.Fatalf("demo flags should win: %+v", c)
	}

	_, err = load(t, map[string]string{"CHADASH_DEMO": "sure", "CHADASH_DEMO_AT": "24:00", "CHADASH_DEMO_SEED": "one"})
	for _, want := range []string{"CHADASH_DEMO", "demo start", "demo seed"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("error %v does not mention %q", err, want)
		}
	}
}

func TestLoggerFormat(t *testing.T) {
	var buf bytes.Buffer
	Config{LogFormat: "json", LogLevel: slog.LevelInfo}.Logger(&buf).Info("hi", "k", "v")
	if !strings.HasPrefix(buf.String(), "{") {
		t.Fatalf("want JSON, got %q", buf.String())
	}
	buf.Reset()
	Config{LogFormat: "text", LogLevel: slog.LevelWarn}.Logger(&buf).Info("hidden")
	if buf.Len() != 0 {
		t.Fatalf("info should be filtered at warn level, got %q", buf.String())
	}
}

func TestNodePairingFile(t *testing.T) {
	c, err := load(t, nil)
	if err != nil || c.NodePairingFile != "" {
		t.Fatalf("default: %q %v", c.NodePairingFile, err)
	}
	c, err = load(t, map[string]string{"CHADASH_NODE_PAIRING_FILE": "/pairing//token"})
	if err != nil || c.NodePairingFile != "/pairing/token" {
		t.Fatalf("env: %q %v", c.NodePairingFile, err)
	}
	c, err = load(t, nil, "--node-pairing-file", "/run/pair")
	if err != nil || c.NodePairingFile != "/run/pair" {
		t.Fatalf("flag: %q %v", c.NodePairingFile, err)
	}
	if _, err := load(t, map[string]string{"CHADASH_NODE_PAIRING_FILE": "pairing/token"}); err == nil {
		t.Fatal("a relative path was accepted")
	}
}

func TestNodePairingFrom(t *testing.T) {
	c, err := load(t, nil)
	if err != nil || c.NodePairingFrom != nil {
		t.Fatalf("default: %v %v", c.NodePairingFrom, err)
	}
	c, err = load(t, map[string]string{"CHADASH_NODE_PAIRING_FROM": "172.18.0.0/16, 10.0.0.5"})
	if err != nil || len(c.NodePairingFrom) != 2 || c.NodePairingFrom[0].String() != "172.18.0.0/16" || c.NodePairingFrom[1].String() != "10.0.0.5/32" {
		t.Fatalf("env: %v %v", c.NodePairingFrom, err)
	}
	c, err = load(t, map[string]string{"CHADASH_NODE_PAIRING_FROM": "any"})
	if err != nil || len(c.NodePairingFrom) != 2 || c.NodePairingFrom[0].Bits() != 0 {
		t.Fatalf("any: %v %v", c.NodePairingFrom, err)
	}
	if _, err := load(t, map[string]string{"CHADASH_NODE_PAIRING_FROM": "lan"}); err == nil {
		t.Fatal("a bad range was accepted")
	}
}

func TestHistory(t *testing.T) {
	c, err := load(t, nil)
	if err != nil || c.History != "standard" || c.HistoryPath() != filepath.Join("/data", "history.db") {
		t.Fatalf("defaults: %q at %q (%v)", c.History, c.HistoryPath(), err)
	}
	c, err = load(t, map[string]string{"CHADASH_HISTORY": "Long-Term", "CHADASH_HISTORY_DIR": "/ssd/chadash/"})
	if err != nil || c.History != "long-term" || c.HistoryPath() != filepath.Join("/ssd/chadash", "history.db") {
		t.Fatalf("set: %q at %q (%v)", c.History, c.HistoryPath(), err)
	}
	for _, env := range []map[string]string{{"CHADASH_HISTORY": "forever"}, {"CHADASH_HISTORY_DIR": "relative/dir"}} {
		if _, err := load(t, env); err == nil {
			t.Errorf("%v: no error", env)
		}
	}
}
