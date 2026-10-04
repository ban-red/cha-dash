// Command traefik is spike 4.3: read-only capture of a Traefik API, a
// sanitizer that turns the capture into public fixtures, and an analyzer that
// maps routers → services → backends → containers → apps from the fixtures.
//
//	traefik capture  --env ../../.env.local --raw <private dir> --pins <private file>
//	traefik sanitize --raw <private dir> --state <private file> --out ../../testdata/integrations/traefik
//	traefik analyze  --fixtures ../../testdata/integrations/traefik [--docker ../../testdata/integrations/docker/jupiter]
//
// Raw captures and the sanitizer state hold real values: keep them outside the
// repo (or in the git-ignored raw/). Only analyze's output is safe to print.
package main

import (
	"flag"
	"fmt"
	"os"
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
		env := fs.String("env", "", "env file with TRAEFIK_URL, TRAEFIK_USERNAME, TRAEFIK_PASSWORD")
		raw := fs.String("raw", "raw", "directory for the raw (unsanitized) capture")
		pins := fs.String("pins", "tofu-pins.json", "TOFU pin file (git-ignored)")
		fs.Parse(args)
		err = capture(*env, *raw, *pins)
	case "sanitize":
		raw := fs.String("raw", "raw", "raw capture directory")
		state := fs.String("state", "state.json", "sanitizer state (real→fake map; private)")
		out := fs.String("out", "", "fixture output directory")
		leak := fs.String("leak", "", "write the real-value list for the leak check here (private)")
		env := fs.String("env", "", "env file; its values join the leak list")
		fs.Parse(args)
		err = sanitize(*raw, *state, *out, *leak, *env)
	case "leakcheck":
		values := fs.String("values", "", "private leak list (JSON) written by sanitize")
		fs.Parse(args)
		err = leakcheck(*values, fs.Args())
	case "analyze":
		fx := fs.String("fixtures", "", "sanitized Traefik fixture directory")
		docker := fs.String("docker", "", "sanitized Docker fixture directory for the backend host (optional)")
		fs.Parse(args)
		err = analyze(*fx, *docker)
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: traefik capture|sanitize|analyze [flags]")
	os.Exit(2)
}
