// Command unraid-spike is throwaway code for spike 4.2: read-only plumbing against one Unraid
// server through the Unraid API (GraphQL): introspection, read queries, subscriptions over
// graphql-ws, and the ApiKeyAuthorize consent flow that issues the key.
//
// Safety rails, enforced in code:
//   - every document is parsed before sending; mutations are refused (guard.go);
//   - requests are sequential, spaced, and time out; a subscription socket lives ≤ 60 s;
//   - redirects are never followed, so the key can't be carried to another host;
//   - everything printed goes through the Redactor (key, URL, host, IPs, MACs);
//   - raw responses go only to a capture directory outside the repo (or git-ignored raw/).
//
// Not product code. What it teaches goes into README.md; the integration is written fresh.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"
)

const usage = `unraid-spike · Cha Dash spike 4.2 (throwaway, read-only)

usage: unraid-spike [flags] <command>

commands:
  probe        unauthenticated reachability checks; prints status codes only, never sends a key
  consent-url  write the ApiKeyAuthorize consent URL to -o (the URL holds the server address,
               so it is never printed)
  run          with a key: introspection, read queries, scope checks, one subscription window
               (with -only: just the named reads)
  sanitize     raw captures (-out, optionally -overlay) → fixtures (-fixtures), plus the private
               leak list (-leak)
  analyze      from the fixtures: container identity join with the Docker fixture, desired state

flags:
`

type options struct {
	cmd, envPath, instance         string
	timeout, gap, window           time.Duration
	outDir, consentOut, only       string
	overlayDir                     string
	statePath, wordsPath, leakPath string
	fixturesDir, dockerFixtureDir  string
}

func main() {
	var o options
	fs := flag.NewFlagSet("unraid-spike", flag.ExitOnError)
	fs.StringVar(&o.envPath, "env", "", "env file with UNRAID_<INSTANCE>_URL and _API_KEY (default: nearest .env.local upward)")
	fs.StringVar(&o.instance, "instance", "jupiter", "instance name in the env keys")
	fs.DurationVar(&o.timeout, "timeout", 20*time.Second, "per-request timeout")
	fs.DurationVar(&o.gap, "gap", 500*time.Millisecond, "minimum pause between requests")
	fs.DurationVar(&o.window, "window", maxWindow, "subscription window, capped at 60s")
	fs.StringVar(&o.outDir, "out", "raw", "directory for raw captures (holds real values: keep it outside the repo or in git-ignored raw/)")
	fs.StringVar(&o.only, "only", "", "run: re-run only these reads (comma-separated names, e.g. Disks), appended to the summary in -out")
	fs.StringVar(&o.overlayDir, "overlay", "", "sanitize: raw captures of a later run with another key; its key-dependent reads (disks, me, scope checks, spin state) replace -out's")
	fs.StringVar(&o.consentOut, "o", "", "consent-url: file to write the URL to (required)")
	fs.StringVar(&o.statePath, "state", "", "sanitize: sanitizer state (salt and real→fake maps; private, outside the repo)")
	fs.StringVar(&o.wordsPath, "words", "", "sanitize: private real<TAB>replacement words (personal share names)")
	fs.StringVar(&o.leakPath, "leak", "", "sanitize: where to write the real-value list for the leak check (private)")
	fs.StringVar(&o.fixturesDir, "fixtures", "../../testdata/integrations/unraid", "sanitize/analyze: fixture directory")
	fs.StringVar(&o.dockerFixtureDir, "docker-fixture", "../../testdata/integrations/docker/jupiter", "sanitize/analyze: the Docker fixture for the same host")
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage); fs.PrintDefaults() }
	_ = fs.Parse(os.Args[1:])
	if fs.NArg() != 1 {
		fs.Usage()
		os.Exit(2)
	}
	o.cmd = fs.Arg(0)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	red := NewRedactor()
	if err := run(ctx, red, o); err != nil {
		fmt.Fprintln(os.Stderr, "error:", red.Redact(err.Error()))
		os.Exit(1)
	}
}

func run(ctx context.Context, red *Redactor, o options) error {
	if o.cmd == "analyze" { // works from committed fixtures only; no env needed
		return analyze(o.fixturesDir, o.dockerFixtureDir, os.Stdout)
	}
	if o.envPath == "" {
		wd, err := os.Getwd()
		if err != nil {
			return err
		}
		if o.envPath, err = findEnvFile(wd); err != nil {
			return err
		}
	}
	cfg, err := loadInstance(o.envPath, o.instance)
	red.AddURL(cfg.URL)
	red.Add(cfg.APIKey, "<api-key>")
	if err != nil {
		return err
	}

	switch o.cmd {
	case "probe":
		noKey := cfg
		noKey.APIKey = "" // the probe is unauthenticated by construction
		c, err := newClient(noKey, red, o.timeout, o.gap, nil)
		if err != nil {
			return err
		}
		return runProbe(ctx, c, red, os.Stdout)

	case "consent-url":
		if o.consentOut == "" {
			return errors.New("consent-url needs -o <file>")
		}
		return writeConsentURL(cfg, o.consentOut, os.Stdout)

	case "run":
		if cfg.APIKey == "" {
			return fmt.Errorf("UNRAID_%s_API_KEY is empty: complete the consent flow first", cfg.Name)
		}
		c, err := newClient(cfg, red, o.timeout, o.gap, nil)
		if err != nil {
			return err
		}
		abs, err := filepath.Abs(o.outDir)
		if err != nil {
			return err
		}
		if o.only != "" {
			return runOnly(ctx, c, red, abs, strings.Split(o.only, ","), o.timeout, os.Stdout)
		}
		return runAll(ctx, c, red, abs, o.window, os.Stdout)

	case "sanitize":
		return sanitize(o.outDir, o.overlayDir, o.statePath, o.wordsPath, o.dockerFixtureDir, o.fixturesDir, o.leakPath, cfg, o.envPath, os.Stdout)

	default:
		return fmt.Errorf("unknown command %q", o.cmd)
	}
}
