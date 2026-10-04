// Command chadash is the Cha Dash hub.
//
//	chadash serve         run the hub
//	chadash healthcheck   exit 0 if the local hub answers /healthz (for Docker HEALTHCHECK)
//	chadash version       print the build version
package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"dash.cha.sh/internal/buildinfo"
)

func main() {
	privateUmask() // before anything creates a file
	os.Exit(run(context.Background(), os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	switch args[0] {
	case "serve":
		return serve(ctx, args[1:], getenv, stderr)
	case "healthcheck":
		return healthcheck(ctx, args[1:], getenv, stderr)
	case "version", "--version", "-version":
		fmt.Fprintln(stdout, buildinfo.String())
		return 0
	case "help", "-h", "-help", "--help":
		usage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "chadash: unknown command %q\n\n", args[0])
		usage(stderr)
		return 2
	}
}

func usage(w io.Writer) {
	fmt.Fprint(w, `Usage: chadash <command> [flags]

Commands:
  serve         run the hub
  healthcheck   check the local hub's /healthz; exit 0 if healthy, 1 if not
  version       print the build version

Run "chadash <command> -h" for a command's flags. Every flag can also be set
with a CHADASH_* environment variable; flags win.
`)
}
