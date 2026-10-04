package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"dash.cha.sh/internal/buildinfo"
	"dash.cha.sh/internal/config"
	"dash.cha.sh/internal/export"
	"dash.cha.sh/internal/store"
	"dash.cha.sh/internal/vault"
)

// passphraseEnv supplies the secrets bundle's passphrase to export and
// import without a prompt.
const passphraseEnv = "CHADASH_EXPORT_PASSPHRASE"

// maxPassphraseLine bounds a passphrase typed at the prompt.
const maxPassphraseLine = export.MaxPassphrase

var errNoTerminal = errors.New("standard input isn't a terminal")

// promptPassphrase asks for the passphrase on the terminal, with echo off;
// confirm asks twice. Tests replace it.
var promptPassphrase = func(stderr io.Writer, confirm bool) (string, error) {
	fd := int(os.Stdin.Fd())
	if !isTerminal(fd) {
		return "", errNoTerminal
	}
	ask := func(prompt string) (string, error) {
		fmt.Fprint(stderr, prompt)
		p, err := readSecretLine(fd)
		fmt.Fprintln(stderr)
		return p, err
	}
	p, err := ask("Passphrase for the secrets bundle: ")
	if err != nil || !confirm {
		return p, err
	}
	again, err := ask("The same passphrase again: ")
	if err != nil {
		return "", err
	}
	if again != p {
		return "", errors.New("the two passphrases differ")
	}
	return p, nil
}

// exportCmd writes the hub's configuration as a Home file:
//
//	chadash export [--out file] [--with-secrets]
//
// It reads the database directly, so it works whether or not the hub is
// running (inside the container: docker exec -it chadash chadash export …).
func exportCmd(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("chadash export", flag.ContinueOnError)
	fs.SetOutput(stderr)
	loader := config.Bind(fs, getenv)
	out := fs.String("out", "", "write the Home file to this path (created 0600) instead of standard output")
	withSecrets := fs.Bool("with-secrets", false, "add every secret, encrypted with a passphrase from "+passphraseEnv+" or the terminal")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `Usage: chadash export [--out file] [--with-secrets] [flags]

Writes boards, layouts, integration instances and carried settings as one
YAML file (apiVersion chadash/v1alpha1, kind Home). Secrets are never written
in plain text: each one is a "!secret <name>" placeholder. --with-secrets adds
an encrypted bundle of their values (scrypt and XChaCha20-Poly1305), so the
file restores a whole hub. Users, sessions, passkeys and the audit log are
never exported.

Flags:
`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "chadash export: unexpected argument %q (use --out to name the file)\n", fs.Arg(0))
		return 2
	}
	cfg, err := loader.Config()
	if err != nil {
		fmt.Fprintf(stderr, "chadash export: %v\n", err)
		return 2
	}
	fail := func(err error) int {
		fmt.Fprintf(stderr, "chadash export: %v\n", err)
		return 1
	}

	passphrase := ""
	if *withSecrets {
		if passphrase, err = passphraseFor(getenv, stderr, true); err != nil {
			return fail(err)
		}
		if err := export.CheckPassphrase(passphrase); err != nil {
			return fail(fmt.Errorf("the passphrase needs at least %d characters", export.MinPassphrase))
		}
	}

	st, v, err := openHub(ctx, cfg, cfg.Logger(stderr), false)
	if err != nil {
		return fail(err)
	}
	defer st.Close()
	if v == nil {
		if *withSecrets {
			return fail(errors.New("the vault key isn't there, so the secrets can't be read; export without --with-secrets for placeholders only"))
		}
		fmt.Fprintln(stderr, "chadash export: the vault key isn't there; the file has placeholders only, which is all an export without --with-secrets has anyway.")
	}
	res, err := export.New(st, v, export.Options{HubVersion: buildinfo.Version}).Export(ctx, passphrase)
	if err != nil {
		return fail(err)
	}
	if *out == "" {
		if _, err := stdout.Write(res.Data); err != nil {
			return fail(err)
		}
	} else if err := writePrivate(*out, res.Data); err != nil {
		return fail(err)
	}
	for _, w := range res.Warnings {
		fmt.Fprintf(stderr, "warning: %s\n", w.Message)
	}
	where := "standard output"
	if *out != "" {
		where = *out
	}
	secrets := fmt.Sprintf("%s as !secret placeholders", plural(res.Secrets, "secret"))
	if *withSecrets {
		secrets += fmt.Sprintf(", %d of them in the encrypted bundle", res.Bundled)
	}
	fmt.Fprintf(stderr, "Exported %s, %s and %s to %s: %s.\n",
		plural(res.Boards, "board"), plural(res.Instances, "instance"), plural(res.Settings, "setting"), where, secrets)
	return 0
}

// importCmd applies a Home file, or with --dry-run shows what it would do:
//
//	chadash import file [--dry-run] [--json] [--skip-secrets]
func importCmd(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("chadash import", flag.ContinueOnError)
	fs.SetOutput(stderr)
	loader := config.Bind(fs, getenv)
	dry := fs.Bool("dry-run", false, "show what importing would change, and change nothing")
	asJSON := fs.Bool("json", false, "print the plan as JSON (the API's shape) instead of text")
	skip := fs.Bool("skip-secrets", false, "import without opening the file's secrets bundle; its secrets are then listed as needed")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `Usage: chadash import file [--dry-run] [--json] [--skip-secrets] [flags]

Imports a Home file (from chadash export) in one transaction. Objects match by
ID; what's equal is left alone, so importing an export of the same hub changes
nothing. Boards and instances the hub has that the file doesn't are reported
as stale and kept. Secrets come from the file's encrypted bundle (passphrase
from `+passphraseEnv+` or the terminal) or from this hub's vault; any other
secret is listed as needed, and its instance waits for it with a Repair.

Flags:
`)
		fs.PrintDefaults()
	}
	// Flags may come before or after the file.
	var files []string
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return 0
			}
			return 2
		}
		if fs.NArg() == 0 {
			break
		}
		files = append(files, fs.Arg(0))
		args = fs.Args()[1:]
	}
	if len(files) != 1 {
		fmt.Fprintln(stderr, "chadash import: name exactly one Home file")
		fs.Usage()
		return 2
	}
	cfg, err := loader.Config()
	if err != nil {
		fmt.Fprintf(stderr, "chadash import: %v\n", err)
		return 2
	}
	fail := func(err error) int {
		fmt.Fprintf(stderr, "chadash import: %v\n", err)
		return 1
	}

	data, err := readLimited(files[0], export.MaxFileBytes)
	if err != nil {
		return fail(err)
	}
	d, err := export.Parse(data)
	if err != nil {
		return fail(fmt.Errorf("%s: %w", files[0], err))
	}
	passphrase := ""
	if d.Bundle != nil && !*skip {
		if passphrase, err = passphraseFor(getenv, stderr, false); err != nil {
			if errors.Is(err, errNoTerminal) {
				return fail(fmt.Errorf("%s has an encrypted secrets bundle: set %s, run in a terminal, or pass --skip-secrets to import without it", files[0], passphraseEnv))
			}
			return fail(err)
		}
		// Check it before touching the data directory, so a typo on a fresh
		// one leaves nothing behind.
		if _, err := d.Bundle.Open(passphrase); err != nil {
			return fail(err)
		}
	}

	st, v, err := openHub(ctx, cfg, cfg.Logger(stderr), true)
	if err != nil {
		return fail(err)
	}
	defer st.Close()
	svc := export.New(st, v, export.Options{HubVersion: buildinfo.Version})

	var plan *export.Plan
	var result any
	if *dry {
		if plan, err = svc.Plan(ctx, d, passphrase); err != nil {
			return fail(err)
		}
		result = map[string]*export.Plan{"plan": plan}
	} else {
		applied, err := svc.Apply(ctx, d, passphrase, "")
		if err != nil {
			return fail(err)
		}
		plan, result = applied.Plan, applied
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(result); err != nil {
			return fail(err)
		}
		return 0
	}
	printPlan(stdout, plan, *dry)
	return 0
}

// passphraseFor takes the passphrase from the environment, else the terminal.
func passphraseFor(getenv func(string) string, stderr io.Writer, confirm bool) (string, error) {
	if p := getenv(passphraseEnv); p != "" {
		return p, nil
	}
	p, err := promptPassphrase(stderr, confirm)
	if errors.Is(err, errNoTerminal) {
		return "", fmt.Errorf("%w: set %s, or run this in a terminal (docker exec -it …)", err, passphraseEnv)
	}
	return p, err
}

// openHub opens the database and the vault as the hub would. create is for
// import, which may run on a fresh data directory before the first boot:
// it creates the directory, the database and (at the default path) the
// vault key. Without create, a missing key returns a nil vault.
func openHub(ctx context.Context, cfg config.Config, log *slog.Logger, create bool) (*store.Store, *vault.Vault, error) {
	if create {
		if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
			return nil, nil, fmt.Errorf("data dir: %w", err)
		}
	} else if _, err := os.Stat(cfg.DBPath()); err != nil {
		return nil, nil, fmt.Errorf("there's no hub database at %s (set --data-dir or CHADASH_DATA_DIR)", cfg.DBPath())
	}
	st, err := store.Open(ctx, cfg.DBPath())
	if err != nil {
		return nil, nil, err
	}
	if _, err := st.Migrate(ctx); err != nil {
		st.Close()
		return nil, nil, err
	}
	keyPath, explicit := cfg.KeyFile()
	if !create {
		if _, err := os.Stat(keyPath); errors.Is(err, os.ErrNotExist) {
			return st, nil, nil
		}
	}
	v, err := vault.Open(ctx, st, vault.KeyConfig{Path: keyPath, Generate: create && !explicit}, log)
	if err != nil {
		st.Close()
		return nil, nil, err
	}
	return st, v, nil
}

// writePrivate writes data to path, mode 0600, through a temporary file in
// the same directory so a reader never sees half a file.
func writePrivate(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".chadash-export-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, werr := f.Write(data)
	cerr := f.Close()
	if err := errors.Join(werr, cerr, os.Chmod(tmp, 0o600)); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func readLimited(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s is larger than %d MiB; that isn't a Home file", path, limit>>20)
	}
	return data, nil
}

// printPlan writes a plan for people: what changes, field by field, then
// the secrets still needed and the totals. Unchanged objects are counted,
// not listed.
func printPlan(w io.Writer, p *export.Plan, dry bool) {
	for _, c := range p.Changes {
		if c.Action == "unchanged" {
			continue
		}
		label := c.Name
		if label == "" {
			label = c.ID
		}
		var tags []string
		if c.Integration != "" {
			tags = append(tags, c.Integration)
		}
		if c.ID != label {
			tags = append(tags, c.ID)
		}
		line := fmt.Sprintf("%-7s %-9s %s", c.Action, c.Kind, label)
		if len(tags) > 0 {
			line += " (" + strings.Join(tags, ", ") + ")"
		}
		switch {
		case c.Action == "stale":
			line += ": not in the file; kept"
		case c.FileID != "":
			line += ", matched by its slug to the file's " + c.FileID
		case c.Detail != "":
			line += ": " + c.Detail
		}
		fmt.Fprintln(w, line)
		for i, f := range c.Fields {
			if i == 10 {
				fmt.Fprintf(w, "          … and %d more\n", len(c.Fields)-i)
				break
			}
			fmt.Fprintln(w, "          "+fieldLine(f))
		}
		if c.Truncated {
			fmt.Fprintln(w, "          … more changes than listed")
		}
		for _, s := range c.Secrets {
			if s.Action == "unchanged" {
				continue
			}
			from := ""
			switch s.Source {
			case "bundle":
				from = " from the bundle"
			case "vault":
				from = " from this hub's vault"
			}
			fmt.Fprintf(w, "          secret %s: %s%s\n", s.Field, s.Action, from)
		}
	}
	for _, wn := range p.Warnings {
		fmt.Fprintf(w, "warning: %s\n", wn.Message)
	}
	if len(p.SecretsNeeded) > 0 {
		fmt.Fprintln(w, "\nSecrets needed: enter them in Settings → Integrations. Until then each instance shows a Repair.")
		for _, n := range p.SecretsNeeded {
			note := ""
			if n.InBundle {
				note = " (in the file's bundle: import with its passphrase to restore it)"
			}
			fmt.Fprintf(w, "  %s (%s): %s%s\n", n.Name, n.Integration, n.Field, note)
		}
	}
	s := p.Summary
	restored := ""
	if s.SecretsRestored > 0 {
		restored = fmt.Sprintf(", %s from the bundle", plural(s.SecretsRestored, "secret"))
	}
	fmt.Fprintln(w)
	switch {
	case dry:
		fmt.Fprintf(w, "Dry run: %d to create, %d to update, %d unchanged, %d stale%s. Nothing was changed.\n",
			s.Create, s.Update, s.Unchanged, s.Stale, restored)
	case !p.Changed():
		fmt.Fprintf(w, "Nothing to change: the hub already matches the file (%d unchanged, %d stale).\n", s.Unchanged, s.Stale)
	default:
		fmt.Fprintf(w, "Imported: %d created, %d updated, %d unchanged, %d stale%s. A running hub starts changed instances within a minute.\n",
			s.Create, s.Update, s.Unchanged, s.Stale, restored)
	}
}

func fieldLine(f export.FieldChange) string {
	switch f.Op {
	case export.OpAdd:
		return fmt.Sprintf("+ %s: %s", f.Path, short(f.To))
	case export.OpRemove:
		return fmt.Sprintf("- %s (was %s)", f.Path, short(f.From))
	case export.OpReorder:
		return fmt.Sprintf("~ %s: reordered", f.Path)
	default:
		return fmt.Sprintf("~ %s: %s → %s", f.Path, short(f.From), short(f.To))
	}
}

// short is a value in JSON, cut to one readable line.
func short(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "?"
	}
	s := string(b)
	if utf8.RuneCountInString(s) > 60 {
		r := []rune(s)
		s = string(r[:57]) + "…"
	}
	return s
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}
