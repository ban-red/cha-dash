package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"dash.cha.sh/internal/spec"
	"dash.cha.sh/internal/spec/spectest"
)

// maxSpec bounds a spec file the commands read: the hub takes 64 KiB.
const maxSpec = 1 << 20

// specCmd checks a spec for its author, with no hub (internal/spec/spectest):
//
//	chadash spec lint <spec.yaml>...           the schema and the load-time rules
//	chadash spec test <spec.yaml> <fixtures/>  run it against recorded answers
//	chadash spec migrate <spec.yaml>...        rewrite a v1alpha1 spec as v1beta1
//	chadash spec new [-dir <folder>] <id>      start a spec that passes spec test
//
// They exit 1 when a check fails and 2 on a usage error.
func specCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	usage := func(w io.Writer) {
		fmt.Fprintf(w, `Usage: chadash spec lint <spec.yaml>...
       chadash spec test <spec.yaml> <fixtures/>
       chadash spec migrate <spec.yaml>...
       chadash spec new [-dir <folder>] <id>

lint checks each spec against the published schema (%[1]s, or %[2]s,
which still loads) and the rules the hub applies when it loads one; for a
%[2]s spec it also says what %[1]s changes. test runs a spec against the
answers in a fixtures folder (<request>.json, or a fixtures.yaml) on a local
fake upstream, prints the resources and metrics it publishes, and checks
what every spec must publish. migrate rewrites %[2]s specs as %[1]s in
place, keeping comments and layout. new writes <id>/spec.yaml, its
fixtures/ and a recipe.md and README.md to fill in, the community
repository's layout, which spec test passes as it is. None needs a hub.
`, spec.APIVersion, spec.V1Alpha1)
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
				notes := spec.MigrationNotes(b)
				if len(notes) == 0 {
					fmt.Fprintf(stdout, "%s: ok\n", path)
					continue
				}
				fmt.Fprintf(stdout, "%s: ok, in %s, which loads through v0.5.x. What %s changes:\n", path, spec.V1Alpha1, spec.V1Beta1)
				for _, n := range notes {
					fmt.Fprintf(stdout, "  %s\n", n)
				}
				fmt.Fprintf(stdout, "  chadash spec migrate %s rewrites it.\n", path)
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
	case "migrate":
		if len(args) < 2 {
			usage(stderr)
			return 2
		}
		code := 0
		for _, path := range args[1:] {
			if err := migrateFile(path, stdout); err != nil {
				fmt.Fprintf(stderr, "chadash spec migrate: %s: %v\n", path, err)
				code = 1
			}
		}
		return code
	case "new":
		fl := flag.NewFlagSet("spec new", flag.ContinueOnError)
		fl.SetOutput(stderr)
		dir := fl.String("dir", ".", "the folder to write <id>/ in")
		if err := fl.Parse(args[1:]); err != nil || fl.NArg() != 1 {
			if err == nil {
				usage(stderr)
			}
			return 2
		}
		files, err := newSpec(*dir, fl.Arg(0), time.Now())
		if err != nil {
			fmt.Fprintf(stderr, "chadash spec new: %v\n", err)
			return 1
		}
		id := fl.Arg(0)
		for _, f := range files {
			fmt.Fprintf(stdout, "wrote %s\n", f)
		}
		at := filepath.Join(*dir, id)
		fmt.Fprintf(stdout, "\nchadash spec test %s %s passes as it is. Fill in the TODOs, record your\ninstance's answers into fixtures/ with personal data taken out, and run it again.\n",
			filepath.Join(at, "spec.yaml"), filepath.Join(at, "fixtures"))
		return 0
	default:
		fmt.Fprintf(stderr, "chadash spec: unknown command %q\n\n", args[0])
		usage(stderr)
		return 2
	}
}

// migrateFile rewrites one spec file as the current version, in place.
func migrateFile(path string, stdout io.Writer) error {
	b, err := readLimited(path, maxSpec)
	if err != nil {
		return err
	}
	out, changes, err := spec.Migrate(b)
	if err != nil {
		return err
	}
	if changes == nil {
		fmt.Fprintf(stdout, "%s: already %s\n", path, spec.APIVersion)
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	tmp := path + ".migrate"
	if err := os.WriteFile(tmp, out, info.Mode().Perm()); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	fmt.Fprintf(stdout, "%s: migrated to %s\n", path, spec.APIVersion)
	for _, c := range changes {
		fmt.Fprintf(stdout, "  %s\n", c)
	}
	return nil
}

// specID is what the hub takes as an integration's ID.
var specID = regexp.MustCompile(`^[a-z][a-z0-9]{1,31}$`)

// newSpec writes a spec to start from under dir/id, refusing an ID the hub
// wouldn't take and a folder that has any of the files already. It returns
// the files it wrote.
func newSpec(dir, id string, now time.Time) ([]string, error) {
	if !specID.MatchString(id) {
		return nil, fmt.Errorf("%q isn't a spec ID: 2 to 32 lowercase letters and digits, starting with a letter", id)
	}
	name := strings.ToUpper(id[:1]) + id[1:]
	root := filepath.Join(dir, id)
	files := []struct{ path, body string }{
		{filepath.Join(root, "spec.yaml"), strings.NewReplacer("{{id}}", id, "{{name}}", name, "{{version}}", spec.APIVersion).Replace(newSpecYAML)},
		{filepath.Join(root, "fixtures", "fixtures.yaml"), strings.NewReplacer("{{id}}", id, "{{now}}", now.UTC().Truncate(time.Minute).Format(time.RFC3339)).Replace(newFixturesYAML)},
		{filepath.Join(root, "fixtures", "status.json"), newStatusJSON},
		{filepath.Join(root, "recipe.md"), strings.ReplaceAll(newRecipe, "{{name}}", name)},
		{filepath.Join(root, "README.md"), strings.NewReplacer("{{id}}", id, "{{name}}", name).Replace(newREADME)},
	}
	for _, f := range files {
		if _, err := os.Stat(f.path); err == nil {
			return nil, fmt.Errorf("%s exists already; nothing was written", f.path)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}
	var wrote []string
	for _, f := range files {
		if err := os.MkdirAll(filepath.Dir(f.path), 0o755); err != nil {
			return wrote, err
		}
		// O_EXCL: a file that appeared since the check isn't overwritten.
		w, err := os.OpenFile(f.path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return wrote, err
		}
		if _, err := io.WriteString(w, f.body); err != nil {
			w.Close()
			return wrote, err
		}
		if err := w.Close(); err != nil {
			return wrote, err
		}
		wrote = append(wrote, f.path)
	}
	return wrote, nil
}

const newSpecYAML = `# {{name}}: TODO say in a line what this spec reads.
#
# Format {{version}}; Cha Dash's docs/specs.md is the guide. Check it as you
# go, with no hub:
#   chadash spec lint {{id}}/spec.yaml
#   chadash spec test {{id}}/spec.yaml {{id}}/fixtures
# Editors take the schema from a first line like
#   # yaml-language-server: $schema=https://<your hub>/api/specs/schema/v1beta1.json
apiVersion: {{version}}
kind: Integration
meta:
  id: {{id}}
  name: {{name}}  # TODO the app's name as people write it
  icon: {{id}}
  version: 0.1.0
  tier: bronze
  category: monitoring  # media, automation, downloads, hosts, network, monitoring, home or files

# url and tls are always there. Secrets go only into headers, queries and
# bodies; a default is the value when the owner leaves a field unset.
config:
  api_key: { type: secret, title: API key, required: true, description: "TODO where the app shows it" }
  errors_warn: { type: integer, title: Errors before attention, default: 1, min: 1, max: 10000 }

source:
  headers:
    X-Api-Key: "{{ secret.api_key }}"  # TODO the header the app reads its key from
  timeout: 10s

every: 60s

# Run in order, each under the instance's URL. extract names values by
# gjson path into the answer; a missing one is null.
requests:
  status:
    path: /api/status  # TODO a cheap endpoint that answers JSON
    extract:
      version: version
      errors: errors

version: status.version

resources:
  - id: app
    kind: app
    name: instance.name
    state: '"running"'
    health: '(status.errors ?? 0) >= config.errors_warn ? "attention" : "ok"'
    reason: '(status.errors ?? 0) >= config.errors_warn ? string(status.errors) + " errors" : ""'
    attrs:
      type: '"{{id}}"'
      version: status.version
    links:
      - { title: {{name}}, url: config.url }

metrics:
  - { resource: app, key: errors, value: status.errors, unit: count, max: 10000 }
`

const newFixturesYAML = `# The answers chadash spec test serves for spec.yaml, by request name:
#   chadash spec test {{id}}/spec.yaml {{id}}/fixtures
# Replace status.json with your instance's answer, with names, addresses and
# tokens taken out. A request without an answer gets a 404.
secrets: { api_key: fake-api-key }
answers:
  status: status.json
# now() is this moment in spec test, so time-based health doesn't depend on
# the day you run it; leave it out for the wall clock.
now: {{now}}
`

const newStatusJSON = `{"version": "1.0.0", "errors": 0}
`

const newRecipe = `---
title: TODO the credential, for example a read-only API key
summary: TODO say in a sentence what the credential can and can't do in {{name}}.
access: read-only
---

## Make the key

TODO the steps in {{name}}, then copy the key into **API key**.

## Notes

- TODO anything the owner should know: what the key can change, how to revoke it.
`

const newREADME = `# {{id}}

TODO what {{name}} is and what this spec shows of it.

- spec.yaml: the spec. chadash spec lint {{id}}/spec.yaml checks it.
- fixtures/: recorded answers, with personal data taken out. chadash spec test {{id}}/spec.yaml {{id}}/fixtures runs the spec against them.
- recipe.md: how to make a credential with only the access the spec needs.

Tested against {{name}} TODO version.
`
