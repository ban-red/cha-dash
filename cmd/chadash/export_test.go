package main

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/store"
	"dash.cha.sh/internal/vault"
)

const (
	cliSonarrKey   = "FAKE-cli-sonarr-key-00112233445566"
	cliTraefikPass = "FAKE-cli-traefik-password-xyz"
	cliPassphrase  = "the cli test passphrase"
)

// seedDataDir makes a hub data directory with a board, two instances whose
// secrets are in the vault, and a user who must never be exported.
func seedDataDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	st, err := store.Open(ctx, filepath.Join(dir, "chadash.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	v, err := vault.Open(ctx, st, vault.KeyConfig{Path: filepath.Join(dir, "secret.key"), Generate: true}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := st.Writer().ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO boards (id, slug, title, position, created_at, updated_at) VALUES ('b_home', 'home', 'Home', 0, 1, 1)`)
	exec(`INSERT INTO layouts (board_id, revision, format_version, document, created_at) VALUES ('b_home', 1, 1, '{"version":1,"columns":12,"sections":[]}', 1)`)
	for _, in := range []struct{ id, integ, name, field, value, cfg string }{
		{"i_sonarr", "sonarr", "Sonarr", "api_key", cliSonarrKey, `"url":"https://sonarr.home.arpa"`},
		{"i_traefik", "traefik", "Traefik", "password", cliTraefikPass, `"url":"http://traefik.home.arpa:8080","username":"admin"`},
	} {
		name := "instance/" + in.id + "/" + in.field
		if err := v.Put(ctx, name, []byte(in.value)); err != nil {
			t.Fatal(err)
		}
		exec(`INSERT INTO integration_instances (id, integration, name, config, enabled, created_at, updated_at) VALUES (?, ?, ?, ?, 1, 1, 1)`,
			in.id, in.integ, in.name, `{`+in.cfg+`,"`+in.field+`":"`+integration.SecretRef(name)+`"}`)
	}
	exec(`INSERT INTO users (id, username, password_hash, role, created_at, updated_at) VALUES ('u_1', 'FAKE-cli-owner', 'x', 'owner', 1, 1)`)
	return dir
}

func noLeak(t *testing.T, what, s string) {
	t.Helper()
	for _, v := range []string{cliSonarrKey, cliTraefikPass, cliPassphrase, "FAKE-cli-owner"} {
		if strings.Contains(s, v) {
			t.Fatalf("%s holds %q", what, v)
		}
	}
}

func noPrompt(t *testing.T) {
	t.Helper()
	old := promptPassphrase
	promptPassphrase = func(io.Writer, bool) (string, error) { return "", errNoTerminal }
	t.Cleanup(func() { promptPassphrase = old })
}

func TestExportCommand(t *testing.T) {
	noPrompt(t)
	src := seedDataDir(t)
	env := map[string]string{"CHADASH_DATA_DIR": src}

	code, stdout, stderr := runCmd(t, env, "export")
	if code != 0 || !strings.HasPrefix(stdout, "# Cha Dash Home file") || !strings.Contains(stdout, "api_key: !secret instance/i_sonarr/api_key") {
		t.Fatalf("export: %d\n%s\n%s", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "Exported 1 board, 2 instances and 0 settings to standard output: 2 secrets as !secret placeholders.") {
		t.Fatalf("summary: %s", stderr)
	}
	noLeak(t, "export", stdout+stderr)

	// --with-secrets needs a passphrase: from the environment, or a terminal.
	if code, _, stderr := runCmd(t, env, "export", "--with-secrets"); code != 1 || !strings.Contains(stderr, "CHADASH_EXPORT_PASSPHRASE") {
		t.Fatalf("no passphrase: %d %s", code, stderr)
	}
	env["CHADASH_EXPORT_PASSPHRASE"] = "short"
	if code, _, stderr := runCmd(t, env, "export", "--with-secrets"); code != 1 || !strings.Contains(stderr, "at least 12 characters") {
		t.Fatalf("short passphrase: %d %s", code, stderr)
	}
	env["CHADASH_EXPORT_PASSPHRASE"] = cliPassphrase
	out := filepath.Join(t.TempDir(), "home.yaml")
	code, stdout, stderr = runCmd(t, env, "export", "--with-secrets", "--out", out)
	if code != 0 || stdout != "" || !strings.Contains(stderr, "2 secrets as !secret placeholders, 2 of them in the encrypted bundle") {
		t.Fatalf("export --with-secrets: %d %q %s", code, stdout, stderr)
	}
	fi, err := os.Stat(out)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("the file should be 0600: %v %v", fi, err)
	}
	data, _ := os.ReadFile(out)
	if !strings.Contains(string(data), "\nsecrets_bundle:\n") {
		t.Fatalf("no bundle:\n%s", data)
	}
	noLeak(t, "export --with-secrets", string(data)+stderr)

	// Without the key: placeholders still work, the bundle can't.
	if err := os.Rename(filepath.Join(src, "secret.key"), filepath.Join(src, "elsewhere.key")); err != nil {
		t.Fatal(err)
	}
	if code, stdout, stderr := runCmd(t, map[string]string{"CHADASH_DATA_DIR": src}, "export"); code != 0 || !strings.Contains(stdout, "!secret instance/i_traefik/password") || !strings.Contains(stderr, "vault key isn't there") {
		t.Fatalf("export without the key: %d %s", code, stderr)
	}
	if code, _, stderr := runCmd(t, env, "export", "--with-secrets"); code != 1 || !strings.Contains(stderr, "can't be read") {
		t.Fatalf("bundle without the key: %d %s", code, stderr)
	}
	if code, _, stderr := runCmd(t, map[string]string{"CHADASH_DATA_DIR": filepath.Join(t.TempDir(), "nothing")}, "export"); code != 1 || !strings.Contains(stderr, "no hub database") {
		t.Fatalf("missing data dir: %d %s", code, stderr)
	}
	if code, _, _ := runCmd(t, env, "export", "stray"); code != 2 {
		t.Fatalf("stray argument: %d", code)
	}
	if code, _, stderr := runCmd(t, nil, "export", "-h"); code != 0 || !strings.Contains(stderr, "--with-secrets") {
		t.Fatalf("export -h: %d %s", code, stderr)
	}
}

func TestImportCommand(t *testing.T) {
	noPrompt(t)
	src := seedDataDir(t)
	file := filepath.Join(t.TempDir(), "home.yaml")
	env := map[string]string{"CHADASH_DATA_DIR": src, "CHADASH_EXPORT_PASSPHRASE": cliPassphrase}
	if code, _, stderr := runCmd(t, env, "export", "--with-secrets", "--out", file); code != 0 {
		t.Fatalf("export: %s", stderr)
	}

	// A fresh data directory, before the hub's first boot.
	dst := filepath.Join(t.TempDir(), "fresh")
	env = map[string]string{"CHADASH_DATA_DIR": dst, "CHADASH_EXPORT_PASSPHRASE": cliPassphrase}
	code, stdout, stderr := runCmd(t, env, "import", file, "--dry-run") // flags after the file work too
	if code != 0 {
		t.Fatalf("dry run: %d %s", code, stderr)
	}
	for _, want := range []string{
		"create  board     Home (b_home)\n",
		"create  layout    Home (b_home): 0 sections, 0 items\n",
		"create  instance  Sonarr (sonarr, i_sonarr)\n          + /config/url: \"https://sonarr.home.arpa\"\n          secret api_key: create from the bundle\n",
		"Dry run: 4 to create, 0 to update, 0 unchanged, 0 stale, 2 secrets from the bundle. Nothing was changed.",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("dry run lacks %q:\n%s", want, stdout)
		}
	}
	noLeak(t, "dry run", stdout+stderr)
	if n := countRows(t, dst, "integration_instances"); n != 0 {
		t.Fatalf("a dry run wrote %d instances", n)
	}

	code, stdout, stderr = runCmd(t, env, "import", file)
	if code != 0 || !strings.Contains(stdout, "Imported: 4 created, 0 updated, 0 unchanged, 0 stale, 2 secrets from the bundle.") {
		t.Fatalf("import: %d\n%s\n%s", code, stdout, stderr)
	}
	noLeak(t, "import", stdout+stderr)
	if got := vaultSecret(t, dst, "instance/i_sonarr/api_key"); got != cliSonarrKey {
		t.Fatal("the bundle didn't restore the key")
	}
	if n := countRows(t, dst, "users"); n != 0 {
		t.Fatal("an import created users")
	}

	// Again: nothing changes. --json gives the API's shape.
	code, stdout, _ = runCmd(t, env, "import", "--json", file)
	var applied struct {
		Changed bool `json:"changed"`
		Plan    struct {
			Summary map[string]int `json:"summary"`
		} `json:"plan"`
	}
	if code != 0 || json.Unmarshal([]byte(stdout), &applied) != nil || applied.Changed || applied.Plan.Summary["unchanged"] != 4 {
		t.Fatalf("re-import: %d %s", code, stdout)
	}
	if code, stdout, _ := runCmd(t, env, "import", file); code != 0 || !strings.Contains(stdout, "Nothing to change: the hub already matches the file (4 unchanged, 0 stale).") {
		t.Fatalf("re-import text: %s", stdout)
	}

	// A wrong passphrase fails cleanly, and writes nothing.
	other := filepath.Join(t.TempDir(), "other")
	env = map[string]string{"CHADASH_DATA_DIR": other, "CHADASH_EXPORT_PASSPHRASE": "not the right passphrase"}
	if code, _, stderr := runCmd(t, env, "import", file); code != 1 || !strings.Contains(stderr, "the passphrase is wrong, or the secrets bundle was changed") {
		t.Fatalf("wrong passphrase: %d %s", code, stderr)
	}
	noLeak(t, "wrong passphrase", stderr)
	if _, err := os.Stat(other); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a failed import into a fresh directory left files behind: %v", err)
	}
	// No passphrase and no terminal: say how, or skip the secrets.
	env = map[string]string{"CHADASH_DATA_DIR": other}
	if code, _, stderr := runCmd(t, env, "import", file); code != 1 || !strings.Contains(stderr, "--skip-secrets") {
		t.Fatalf("no passphrase: %d %s", code, stderr)
	}
	code, stdout, _ = runCmd(t, env, "import", "--skip-secrets", file)
	if code != 0 || !strings.Contains(stdout, "Secrets needed: enter them in Settings → Integrations.") ||
		!strings.Contains(stdout, "  Sonarr (sonarr): api_key (in the file's bundle: import with its passphrase to restore it)") {
		t.Fatalf("--skip-secrets: %d %s", code, stdout)
	}

	// Usage and file errors.
	if code, _, _ := runCmd(t, env, "import"); code != 2 {
		t.Fatalf("no file: %d", code)
	}
	if code, _, _ := runCmd(t, env, "import", file, file); code != 2 {
		t.Fatalf("two files: %d", code)
	}
	bad := filepath.Join(t.TempDir(), "bad.yaml")
	os.WriteFile(bad, []byte("apiVersion: chadash/v1alpha1\nkind: Home\ninstances:\n- {id: i_x, integration: sonarr, name: X, secrets: {api_key: "+cliSonarrKey+"}}\n"), 0o600)
	code, _, stderr = runCmd(t, env, "import", bad)
	if code != 1 || !strings.Contains(stderr, "line 4, instances[0].secrets.api_key: must be a placeholder") {
		t.Fatalf("plaintext secret: %d %s", code, stderr)
	}
	noLeak(t, "the parse error", stderr)
	if code, _, stderr := runCmd(t, env, "import", filepath.Join(t.TempDir(), "missing.yaml")); code != 1 || !strings.Contains(stderr, "no such file") {
		t.Fatalf("missing file: %d %s", code, stderr)
	}
}

func TestPassphraseFromTheTerminal(t *testing.T) {
	var asked []bool
	old := promptPassphrase
	promptPassphrase = func(_ io.Writer, confirm bool) (string, error) {
		asked = append(asked, confirm)
		return cliPassphrase, nil
	}
	t.Cleanup(func() { promptPassphrase = old })
	src := seedDataDir(t)
	file := filepath.Join(t.TempDir(), "home.yaml")
	if code, _, stderr := runCmd(t, map[string]string{"CHADASH_DATA_DIR": src}, "export", "--with-secrets", "--out", file); code != 0 {
		t.Fatal(stderr)
	}
	if code, _, stderr := runCmd(t, map[string]string{"CHADASH_DATA_DIR": filepath.Join(t.TempDir(), "d")}, "import", file); code != 0 {
		t.Fatal(stderr)
	}
	// Export asks twice (confirm), import once.
	if len(asked) != 2 || !asked[0] || asked[1] {
		t.Fatalf("prompts = %v", asked)
	}
	// The env var wins over the prompt.
	promptPassphrase = func(io.Writer, bool) (string, error) { return "", errors.New("should not ask") }
	if code, _, stderr := runCmd(t, map[string]string{"CHADASH_DATA_DIR": src, "CHADASH_EXPORT_PASSPHRASE": cliPassphrase}, "export", "--with-secrets"); code != 0 {
		t.Fatal(stderr)
	}
}

func countRows(t *testing.T, dir, table string) int {
	t.Helper()
	st, err := store.Open(t.Context(), filepath.Join(dir, "chadash.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var n int
	if err := st.Reader().QueryRowContext(t.Context(), `SELECT count(*) FROM `+table).Scan(&n); err != nil {
		return 0 // not migrated: nothing written
	}
	return n
}

func vaultSecret(t *testing.T, dir, name string) string {
	t.Helper()
	st, err := store.Open(t.Context(), filepath.Join(dir, "chadash.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	v, err := vault.Open(t.Context(), st, vault.KeyConfig{Path: filepath.Join(dir, "secret.key")}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	b, err := v.Get(t.Context(), name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
