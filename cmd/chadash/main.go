// Command chadash is the Cha Dash hub.
//
//	chadash serve         run the hub
//	chadash export        write the hub's configuration as a Home file (secrets as placeholders, or encrypted)
//	chadash import        apply a Home file, or preview it with --dry-run
//	chadash audit verify  walk the audit log's hash chain and report the first break
//	chadash bans          list addresses banned after wrong passwords; bans clear <ip> lifts one
//	chadash spec          lint, test, migrate or start an integration spec, with no hub
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
	case "export":
		return exportCmd(ctx, args[1:], getenv, stdout, stderr)
	case "import":
		return importCmd(ctx, args[1:], getenv, stdout, stderr)
	case "audit":
		return auditCmd(ctx, args[1:], getenv, stdout, stderr)
	case "bans":
		return bansCmd(ctx, args[1:], getenv, stdout, stderr)
	case "spec":
		return specCmd(ctx, args[1:], stdout, stderr)
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
  export        write boards, instances and settings as a Home file (YAML);
                secrets as placeholders, or encrypted with --with-secrets
  import        apply a Home file; --dry-run shows the changes first
  audit verify  check the audit log's hash chain; exit 1 at the first break
  bans          list addresses banned after wrong passwords; bans clear <ip>
                lifts one (works while the hub is stopped too)
  spec lint     check integration specs against the schema and load rules
  spec test     run a spec against recorded answers and print what it publishes
  spec migrate  rewrite v1alpha1 specs as v1beta1, keeping comments
  spec new      start a spec, its fixtures and a recipe that pass spec test
  healthcheck   check the local hub's /healthz; exit 0 if healthy, 1 if not
  version       print the build version

Run "chadash <command> -h" for a command's flags. Every flag can also be set
with a CHADASH_* environment variable; flags win.
`)
}
