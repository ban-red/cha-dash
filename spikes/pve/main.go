// Command pve-spike is throwaway code for spike 4.1: Proxmox VE multi-instance
// and cluster, read-only, against the maintainer's lab. It captures GET
// responses from every instance in an env file, sanitizes them into the public
// fixtures under testdata/integrations/proxmox/, and checks the fixtures for
// leaks of any real value it saw.
//
// Not product code. What it teaches goes into README.md and the v0.1 Proxmox
// integration, which is written fresh.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"time"
)

const usage = `pve-spike · Cha Dash spike 4.1 (throwaway, read-only)

usage: pve-spike [flags] <command>

commands:
  instances   list instance nicknames from the env file (never URLs or tokens)
  capture     GET every endpoint on every instance into -raw (git-ignored)
  tlscheck    GET /version on every instance against the pinned fingerprints (no -tofu)
  sanitize    turn -raw into sanitized fixtures under -out
  leakcheck   scan -out and this spike for every real value seen in -env and -raw
  report      print tables from the sanitized fixtures (instances, privileges, polling cost, backups)

flags:
`

func main() {
	envPath := flag.String("env", "", "env file with PVE_INSTANCES and per-instance URL/token (read at runtime, never printed)")
	rawDir := flag.String("raw", "raw", "directory for raw captures and the TOFU pin store (keep it git-ignored)")
	outDir := flag.String("out", "../../testdata/integrations/proxmox", "sanitized fixtures directory")
	tofu := flag.Bool("tofu", false, "trust an instance's certificate on first use when no pin exists yet")
	delay := flag.Duration("delay", 250*time.Millisecond, "minimum gap between requests to one instance")
	timeout := flag.Duration("timeout", 15*time.Second, "per-request timeout")
	only := flag.String("only", "", "capture just this instance")
	debug := flag.Bool("debug", false, "print each request (Authorization redacted)")
	flag.Usage = func() { fmt.Fprint(os.Stderr, usage); flag.PrintDefaults() }
	flag.Parse()
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	needEnv := func() []Instance {
		if *envPath == "" {
			fatal(fmt.Errorf("-env is required"))
		}
		ins, err := loadInstances(*envPath)
		if err != nil {
			fatal(err)
		}
		return ins
	}

	switch flag.Arg(0) {
	case "instances":
		for _, in := range needEnv() {
			fmt.Printf("%-12s url:set token:set realm:%s\n", in.Name, in.Realm())
		}
	case "capture":
		ins := needEnv()
		if err := os.MkdirAll(*rawDir, 0o700); err != nil {
			fatal(err)
		}
		store, err := openTOFU(filepath.Join(*rawDir, "tofu.json"))
		if err != nil {
			fatal(err)
		}
		failed := 0
		for _, in := range ins {
			if *only != "" && in.Name != *only {
				continue
			}
			meta, err := capture(ctx, in, store, *tofu, *rawDir, *delay, *timeout, *debug)
			if meta != nil {
				if werr := writeJSON(filepath.Join(*rawDir, in.Name, "_meta.json"), meta); werr != nil {
					fatal(werr)
				}
				fmt.Fprintf(os.Stderr, "[%s] tls mode=%s · %d requests · %.0f ms wall\n", in.Name, meta.TLS.Mode, len(meta.Requests), meta.WallMS)
			}
			if err != nil {
				failed++
				fmt.Fprintln(os.Stderr, "error:", err)
			}
		}
		if err := store.save(); err != nil {
			fatal(err)
		}
		if failed > 0 {
			os.Exit(2)
		}
	case "tlscheck":
		// One GET /version per instance against the pins from an earlier capture,
		// without -tofu: shows the pin is compared on later runs.
		store, err := openTOFU(filepath.Join(*rawDir, "tofu.json"))
		if err != nil {
			fatal(err)
		}
		for _, in := range needEnv() {
			c := newClient(in, store, *tofu, 0, *timeout)
			r := c.Get(ctx, "/version")
			st := fmt.Sprint(r.Status)
			if r.Err != "" {
				st = "ERR " + r.Err
			}
			fmt.Printf("%-12s tls=%-12s status=%s %.0f ms\n", in.Name, c.TLS.Mode, st, r.LatencyMS)
		}
		if err := store.save(); err != nil {
			fatal(err)
		}
	case "sanitize":
		if err := sanitizeAll(needEnv(), *rawDir, *outDir); err != nil {
			fatal(err)
		}
	case "leakcheck":
		n, err := leakcheckCmd(needEnv(), *rawDir, []string{*outDir, "."})
		if err != nil {
			fatal(err)
		}
		if n > 0 {
			os.Exit(3)
		}
	case "report":
		if err := report(*outDir, os.Stdout); err != nil {
			fatal(err)
		}
	default:
		flag.Usage()
		os.Exit(1)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "pve-spike:", err)
	os.Exit(1)
}
