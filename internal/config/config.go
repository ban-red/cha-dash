// Package config loads hub settings from CHADASH_* environment variables and
// command-line flags. Precedence is flag > env > built-in default, which is
// implemented by using the env value as each flag's default.
//
// Nothing is required: `docker compose up -d` with no environment works.
package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultListen  = ":3696"
	DefaultDataDir = "/data"

	// keyFileName is where the vault key lives when CHADASH_SECRET_KEY_FILE is unset.
	keyFileName = "secret.key"
	dbFileName  = "chadash.db"
)

// Config is the validated hub configuration.
type Config struct {
	Listen  string // host:port to listen on
	DataDir string // the one persistent volume: SQLite, vault key, layout history

	// SecretKeyFile is an explicit vault key path. Empty means <DataDir>/secret.key,
	// generated on first boot.
	SecretKeyFile string

	LogLevel  slog.Level
	LogFormat string // "text" or "json"

	// TrustedProxies are the reverse proxies whose X-Forwarded-For and
	// X-Forwarded-Proto headers are believed. Empty means none are.
	TrustedProxies []netip.Prefix

	// AllowedOrigins are extra browser origins (scheme://host[:port]) accepted for
	// state-changing requests and WebSockets, for proxies that rewrite Host.
	// Same-origin requests are always allowed.
	AllowedOrigins []string

	// Demo runs the Demo Home simulator in place of a real home.
	Demo bool
	// DemoWallClock makes the simulated clock the real one
	// (CHADASH_DEMO_AT=now). Otherwise the demo starts at DemoAt, a time of
	// day on the simulator's own day, and runs in real time from there.
	DemoWallClock bool
	DemoAt        time.Duration
	DemoSeed      int64
}

// DefaultDemoAt is when the demo day starts: 14:05, the moment all six of
// the default script's incidents are open (sim.DefaultAllOpen).
const DefaultDemoAt = 14*time.Hour + 5*time.Minute

// KeyFile returns the vault key path and whether the operator set it explicitly.
// The vault only auto-generates a key at the default path.
func (c Config) KeyFile() (path string, explicit bool) {
	if c.SecretKeyFile != "" {
		return c.SecretKeyFile, true
	}
	return filepath.Join(c.DataDir, keyFileName), false
}

// DBPath is the SQLite database file inside the data directory.
func (c Config) DBPath() string { return filepath.Join(c.DataDir, dbFileName) }

// Logger builds the process logger described by the config.
func (c Config) Logger(w io.Writer) *slog.Logger {
	opts := &slog.HandlerOptions{Level: c.LogLevel}
	if c.LogFormat == "json" {
		return slog.New(slog.NewJSONHandler(w, opts))
	}
	return slog.New(slog.NewTextHandler(w, opts))
}

// Loader binds the shared flags to a FlagSet. Call Config after fs.Parse.
type Loader struct {
	listen, dataDir, keyFile, level, format, proxies, origins string
	demo                                                      bool
	demoAt, demoSeed                                          string
	envErrs                                                   []error // env values a flag default can't hold
}

// Bind registers the config flags on fs, defaulting each from the environment.
func Bind(fs *flag.FlagSet, getenv func(string) string) *Loader {
	env := func(key, def string) string {
		if v := strings.TrimSpace(getenv(key)); v != "" {
			return v
		}
		return def
	}
	l := &Loader{}
	fs.StringVar(&l.listen, "listen", env("CHADASH_LISTEN", DefaultListen), "address to listen on (env CHADASH_LISTEN)")
	fs.StringVar(&l.dataDir, "data-dir", env("CHADASH_DATA_DIR", DefaultDataDir), "data directory (env CHADASH_DATA_DIR)")
	fs.StringVar(&l.keyFile, "secret-key-file", env("CHADASH_SECRET_KEY_FILE", ""), "vault key file; default <data-dir>/secret.key, generated on first boot (env CHADASH_SECRET_KEY_FILE)")
	fs.StringVar(&l.level, "log-level", env("CHADASH_LOG_LEVEL", "info"), "debug, info, warn or error (env CHADASH_LOG_LEVEL)")
	fs.StringVar(&l.format, "log-format", env("CHADASH_LOG_FORMAT", "text"), "text or json (env CHADASH_LOG_FORMAT)")
	fs.StringVar(&l.proxies, "trusted-proxies", env("CHADASH_TRUSTED_PROXIES", ""), "comma-separated proxy IPs/CIDRs whose X-Forwarded-* headers are trusted (env CHADASH_TRUSTED_PROXIES)")
	fs.StringVar(&l.origins, "allowed-origins", env("CHADASH_ALLOWED_ORIGINS", ""), "comma-separated extra origins, e.g. https://dash.example.com (env CHADASH_ALLOWED_ORIGINS)")

	demo := false
	if v := env("CHADASH_DEMO", ""); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			l.envErrs = append(l.envErrs, fmt.Errorf("CHADASH_DEMO %q: want 1, true, 0 or false", v))
		}
		demo = b
	}
	fs.BoolVar(&l.demo, "demo", demo, "run the Demo Home simulator instead of a real home (env CHADASH_DEMO)")
	fs.StringVar(&l.demoAt, "demo-at", env("CHADASH_DEMO_AT", "14:05"), "demo start: a time of day (HH:MM[:SS], UTC) on the demo's day, or now to follow the real clock (env CHADASH_DEMO_AT)")
	fs.StringVar(&l.demoSeed, "demo-seed", env("CHADASH_DEMO_SEED", "1"), "demo simulator seed (env CHADASH_DEMO_SEED)")
	return l
}

// Config validates the parsed values.
func (l *Loader) Config() (Config, error) {
	errs := append([]error(nil), l.envErrs...)
	c := Config{
		Listen:        l.listen,
		DataDir:       filepath.Clean(l.dataDir),
		SecretKeyFile: l.keyFile,
		LogFormat:     strings.ToLower(l.format),
		Demo:          l.demo,
	}

	// The demo settings are checked even when demo mode is off, so a typo
	// shows up before someone turns it on.
	if strings.EqualFold(strings.TrimSpace(l.demoAt), "now") {
		c.DemoWallClock = true
	} else if at, err := parseTimeOfDay(l.demoAt); err != nil {
		errs = append(errs, fmt.Errorf("demo start %q: want HH:MM, HH:MM:SS or now", l.demoAt))
	} else {
		c.DemoAt = at
	}
	if seed, err := strconv.ParseInt(strings.TrimSpace(l.demoSeed), 10, 64); err != nil {
		errs = append(errs, fmt.Errorf("demo seed %q: want an integer", l.demoSeed))
	} else {
		c.DemoSeed = seed
	}

	if err := checkListen(l.listen); err != nil {
		errs = append(errs, err)
	}
	if strings.TrimSpace(l.dataDir) == "" {
		errs = append(errs, errors.New("data dir must not be empty"))
	}
	if err := c.LogLevel.UnmarshalText([]byte(l.level)); err != nil {
		errs = append(errs, fmt.Errorf("log level %q: want debug, info, warn or error", l.level))
	}
	if c.LogFormat != "text" && c.LogFormat != "json" {
		errs = append(errs, fmt.Errorf("log format %q: want text or json", l.format))
	}
	for _, s := range splitList(l.proxies) {
		p, err := parsePrefix(s)
		if err != nil {
			errs = append(errs, fmt.Errorf("trusted proxy %q: %w", s, err))
			continue
		}
		c.TrustedProxies = append(c.TrustedProxies, p)
	}
	for _, s := range splitList(l.origins) {
		o, err := parseOrigin(s)
		if err != nil {
			errs = append(errs, fmt.Errorf("allowed origin %q: %w", s, err))
			continue
		}
		c.AllowedOrigins = append(c.AllowedOrigins, o)
	}
	return c, errors.Join(errs...)
}

func checkListen(addr string) error {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("listen address %q: %w", addr, err)
	}
	if n, err := strconv.Atoi(port); err != nil || n < 0 || n > 65535 {
		return fmt.Errorf("listen address %q: bad port", addr)
	}
	return nil
}

// parseTimeOfDay reads HH:MM or HH:MM:SS as an offset from midnight.
func parseTimeOfDay(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	layout := "15:04"
	if strings.Count(s, ":") == 2 {
		layout = "15:04:05"
	}
	t, err := time.Parse(layout, s)
	if err != nil {
		return 0, err
	}
	return time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute + time.Duration(t.Second())*time.Second, nil
}

func splitList(s string) []string {
	var out []string
	for part := range strings.SplitSeq(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// parsePrefix accepts a CIDR or a bare address (treated as a single host).
func parsePrefix(s string) (netip.Prefix, error) {
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		return p.Masked(), err
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, err
	}
	a = a.Unmap()
	return netip.PrefixFrom(a, a.BitLen()), nil
}

// parseOrigin normalises an origin to scheme://host[:port].
func parseOrigin(s string) (string, error) {
	u, err := url.Parse(s)
	if err != nil {
		return "", err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", errors.New("scheme must be http or https")
	}
	if u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("want scheme://host[:port] with nothing after it")
	}
	return u.Scheme + "://" + strings.ToLower(u.Host), nil
}
