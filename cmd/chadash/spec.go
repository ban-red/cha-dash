package main

import (
	"context"
	"fmt"
	"io"

	"dash.cha.sh/internal/spec/spectest"
)

// maxSpec bounds a spec file the commands read: the hub takes 64 KiB.
const maxSpec = 1 << 20

// specCmd checks a spec for its author, with no hub (internal/spec/spectest):
//
//	chadash spec lint <spec.yaml>...           the schema and the load-time rules
//	chadash spec test <spec.yaml> <fixtures/>  run it against recorded answers
//
// Both exit 1 when a check fails and 2 on a usage error.
func specCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	usage := func(w io.Writer) {
		fmt.Fprint(w, `Usage: chadash spec lint <spec.yaml>...
       chadash spec test <spec.yaml> <fixtures/>

lint checks each spec against the published schema (chadash/v1alpha1) and
the rules the hub applies when it loads one. test runs a spec against the
answers in a fixtures folder (<request>.json, or a fixtures.yaml) on a local
fake upstream, prints the resources and metrics it publishes, and checks
what every spec must publish. Neither needs a hub.
`)
	}
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	switch args[0] {
	case "-h", "-help", "--help", "help":
		usage(stdout)
		return 0
	case "lint":
		if len(args) < 2 {
			usage(stderr)
			return 2
		}
		code := 0
		for _, path := range args[1:] {
			b, err := readLimited(path, maxSpec)
			if err != nil {
				fmt.Fprintf(stderr, "chadash spec lint: %v\n", err)
				code = 1
				continue
			}
			problems := spectest.Lint(b)
			if len(problems) == 0 {
				fmt.Fprintf(stdout, "%s: ok\n", path)
				continue
			}
			code = 1
			for _, p := range problems {
				fmt.Fprintf(stdout, "%s: %s\n", path, p)
			}
		}
		return code
	case "test":
		if len(args) != 3 {
			usage(stderr)
			return 2
		}
		b, err := readLimited(args[1], maxSpec)
		if err != nil {
			fmt.Fprintf(stderr, "chadash spec test: %v\n", err)
			return 1
		}
		ok, err := spectest.Run(ctx, b, args[2], stdout)
		if err != nil {
			fmt.Fprintf(stderr, "chadash spec test: %v\n", err)
			return 1
		}
		if !ok {
			return 1
		}
		return 0
	default:
		fmt.Fprintf(stderr, "chadash spec: unknown command %q\n\n", args[0])
		usage(stderr)
		return 2
	}
}
