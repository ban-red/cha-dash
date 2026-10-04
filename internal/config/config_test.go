package config

import (
	"bytes"
	"flag"
	"io"
	"log/slog"
	"path/filepath"
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
	})
	if err == nil {
		t.Fatal("expected errors")
	}
	for _, want := range []string{"listen address", "log level", "log format", "trusted proxy", "ftp://x", "https://x/path"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestDemo(t *testing.T) {
	c, err := load(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Demo || c.DemoWallClock || c.DemoAt != DefaultDemoAt || c.DemoSeed != 1 {
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
	if !c.Demo || c.DemoWallClock || c.DemoAt != 9*time.Hour+30*time.Minute+15*time.Second || c.DemoSeed != -3 {
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
