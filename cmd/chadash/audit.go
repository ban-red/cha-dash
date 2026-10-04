package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"dash.cha.sh/internal/audit"
	"dash.cha.sh/internal/config"
	"dash.cha.sh/internal/store"
)

// auditCmd works on the audit log offline:
//
//	chadash audit verify
//
// verify walks the hash chain from the first event and reports the first
// break; it exits 0 when the chain holds and 1 when it doesn't. It reads the
// database directly, so it works whether or not the hub is running.
func auditCmd(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "verify" {
		if len(args) > 0 && (args[0] == "-h" || args[0] == "-help" || args[0] == "--help") {
			fmt.Fprintln(stdout, "Usage: chadash audit verify [flags]")
			return 0
		}
		fmt.Fprintln(stderr, "Usage: chadash audit verify [flags]")
		return 2
	}
	fs := flag.NewFlagSet("chadash audit verify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	loader := config.Bind(fs, getenv)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `Usage: chadash audit verify [flags]

Walks the audit log's hash chain, oldest event first, and checks that every
event's hash matches its contents and names the event before it. Reports the
first break and exits 1, or exits 0 when the whole chain holds.

Flags:
`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	cfg, err := loader.Config()
	if err != nil {
		fmt.Fprintf(stderr, "chadash audit: %v\n", err)
		return 2
	}
	fail := func(err error) int {
		fmt.Fprintf(stderr, "chadash audit: %v\n", err)
		return 1
	}
	if _, err := os.Stat(cfg.DBPath()); err != nil {
		return fail(fmt.Errorf("there's no hub database at %s (set --data-dir or CHADASH_DATA_DIR)", cfg.DBPath()))
	}
	st, err := store.Open(ctx, cfg.DBPath())
	if err != nil {
		return fail(err)
	}
	defer st.Close()
	// Migrating creates an empty audit_events table on a hub that predates
	// it, which then verifies as an empty chain.
	if _, err := st.Migrate(ctx); err != nil {
		return fail(err)
	}
	rep, err := audit.New(st).Verify(ctx)
	if err != nil {
		return fail(err)
	}
	if rep.OK {
		fmt.Fprintf(stdout, "The audit log holds. Events checked: %d.\n", rep.Checked)
		return 0
	}
	b := rep.FirstBreak
	id := ""
	if b.ID != "" {
		id = " (" + b.ID + ")"
	}
	fmt.Fprintf(stdout, "The audit log is broken at event %d%s: %s.\nEvents before it that check out: %d.\n", b.Seq, id, b.Reason, rep.Checked)
	return 1
}
