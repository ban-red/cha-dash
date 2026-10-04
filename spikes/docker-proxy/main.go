// Command docker-proxy is spike 4.4: read-only capture of Docker engines
// through socket proxies (list, inspect, events, images, networks, volumes),
// a proxy-vs-raw probe, a sanitizer that turns the capture into public
// fixtures, and an analyzer that runs on the fixtures.
//
//	docker-proxy capture  --env ../../.env.local --raw <private dir>
//	docker-proxy sanitize --raw <private dir> --state <private file> --out ../../testdata/integrations/docker
//	docker-proxy analyze  --fixtures ../../testdata/integrations/docker
//
// Raw captures and the sanitizer state hold real values: keep them outside the
// repo (or in the git-ignored raw/). Only analyze's output is safe to print.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	cmd, args := os.Args[1], os.Args[2:]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	var err error
	switch cmd {
	case "capture":
		env := fs.String("env", "", "env file with DOCKER_ENDPOINTS and DOCKER_<NAME>_URL")
		raw := fs.String("raw", "raw", "directory for the raw (unsanitized) capture")
		window := fs.Duration("events", 55*time.Second, "live events window (max 60s)")
		only := fs.String("only", "", "capture just this endpoint name")
		fs.Parse(args)
		err = capture(*env, *raw, *window, *only)
	case "sanitize":
		raw := fs.String("raw", "raw", "raw capture directory")
		state := fs.String("state", "state.json", "sanitizer state (real→fake map; private)")
		out := fs.String("out", "", "fixture output directory")
		leak := fs.String("leak", "", "append the real-value list for the leak check here (private)")
		env := fs.String("env", "", "env file; its values join the leak list")
		fs.Parse(args)
		err = sanitize(*raw, *state, *out, *leak, *env)
	case "analyze":
		fx := fs.String("fixtures", "", "sanitized Docker fixture directory")
		fs.Parse(args)
		err = analyze(*fx)
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: docker-proxy capture|sanitize|analyze [flags]")
	os.Exit(2)
}

func logf(format string, a ...any) { fmt.Fprintf(os.Stderr, format+"\n", a...) }
