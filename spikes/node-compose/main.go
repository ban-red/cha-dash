// Command node-spike is throwaway code for spike 4.6: can chadash-node own compose stacks with
// the Compose SDK and go-git? It runs on a Docker host as root and walks one stack through
// adopt → diff → deploy → health gate → rollback, with a git-versioned stacks root as history.
//
// Not product code. What it teaches goes into the spike notes and ADRs; the node is written fresh.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"
)

const usage = `node-spike · Cha Dash spike 4.6 (throwaway)

usage: node-spike [flags] <command> [args]

commands:
  scan                      list compose projects and whether each can be adopted in place
  adopt   <stack>           start owning a stack: commit its compose file to the stacks repo
  diff    <stack> <file>    what deploying <file> would change: text, config hash, and a dry run
  deploy  <stack> <file>    dry-run preflight, commit, up --wait, soak; roll back if the gate fails
  history <stack>           the stack's deploys and rollbacks, from git

flags:
`

// Exit codes the scenario runner checks.
const (
	exitRolledBack = 2 // the change failed its health gate and the previous version is running again
	exitStuck      = 3 // the rollback failed too; a human has to look
	exitBlocked    = 4 // the dry run refused the change, so nothing was touched
)

func main() {
	root := flag.String("root", "/opt/stacks", "stacks root (a git repository once a stack is adopted)")
	wait := flag.Duration("wait", 90*time.Second, "health gate: how long up --wait may take to reach running/healthy")
	soak := flag.Duration("soak", 20*time.Second, "after the gate, how long every container must stay running and not unhealthy")
	flag.Usage = func() { fmt.Fprint(os.Stderr, usage); flag.PrintDefaults() }
	flag.Parse()
	if flag.NArg() == 0 {
		flag.Usage()
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	n, err := newNode(*root, *wait, *soak)
	if err != nil {
		fatal(err)
	}

	args := flag.Args()
	need := func(k int) {
		if len(args) != k+1 {
			flag.Usage()
			os.Exit(1)
		}
	}
	switch args[0] {
	case "scan":
		need(0)
		err = n.scan(ctx)
	case "adopt":
		need(1)
		err = n.adopt(ctx, args[1])
	case "diff":
		need(2)
		_, err = n.diff(ctx, args[1], args[2])
	case "deploy":
		need(2)
		err = n.deploy(ctx, args[1], args[2])
	case "history":
		need(1)
		err = n.history(ctx, args[1])
	default:
		flag.Usage()
		os.Exit(1)
	}

	if rss := peakRSS(); rss != "" {
		fmt.Fprintln(os.Stderr, "· peak RSS", rss)
	}
	switch {
	case err == nil:
	case errors.Is(err, errRolledBack):
		fmt.Fprintln(os.Stderr, "↩", err)
		os.Exit(exitRolledBack)
	case errors.Is(err, errStuck):
		fmt.Fprintln(os.Stderr, "■", err)
		os.Exit(exitStuck)
	case errors.Is(err, errBlocked):
		fmt.Fprintln(os.Stderr, "■", err)
		os.Exit(exitBlocked)
	default:
		fatal(err)
	}
}

// peakRSS is the process's high-water memory mark (Linux only); it feeds the hardware floor (ADR 16).
func peakRSS() string {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return ""
	}
	for line := range strings.Lines(string(b)) {
		if v, ok := strings.CutPrefix(line, "VmHWM:"); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "■", err)
	os.Exit(1)
}
