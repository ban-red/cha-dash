# Upgrade fixtures: data directories released versions left behind

`TestUpgradeFromReleasedVersions` (`cmd/chadash/upgrade_test.go`) starts the current hub on each of these. It checks three things:
- the database migrates to the newest schema;
- the owner signs in with the password set under the old version;
- the vault still opens the secret stored then, because the Sonarr instance's API key reaches a fake Sonarr.

| Folder | Made by | Schema |
|---|---|---|
| `0.3.0/` | v0.3.0 | 22 |
| `0.4.0/` | v0.4.0 | 28 |
| `0.5.0/` | v0.5.0 | 31 |

Each folder holds:
- `chadash.sql` and `history.sql`, the databases as `sqlite3 .dump` text. Text, not `.db` files, because the repo ignores `*.db` and `*.key` to keep real data directories out. The test rebuilds the databases from the dumps.
- `vault-key.txt`, the folder's `secret.key`.

Everything here is made up:
- the owner, `owner`, with the password `correct horse battery staple`;
- one Sonarr instance at `http://127.0.0.1:9/` whose API key is `0123456789abcdef0123456789abcdef`;
- a vault key made for this folder and used nowhere else.

## Adding a release

Run that release's own code, not a build: a test in a worktree at its tag.

```sh
git worktree add --detach .claude/worktrees/up-0.5.0 v0.5.0
cp <the generator below> .claude/worktrees/up-0.5.0/cmd/chadash/zz_upgradegen_test.go
mkdir -p /tmp/up-0.5.0
(cd .claude/worktrees/up-0.5.0 && CHADASH_UPGRADE_OUT=/tmp/up-0.5.0 go test -count=1 -run TestGenerateUpgradeData ./cmd/chadash/)
mkdir testdata/upgrade/0.5.0
sqlite3 /tmp/up-0.5.0/chadash.db .dump > testdata/upgrade/0.5.0/chadash.sql
sqlite3 /tmp/up-0.5.0/history.db .dump > testdata/upgrade/0.5.0/history.sql
cp /tmp/up-0.5.0/secret.key testdata/upgrade/0.5.0/vault-key.txt
git worktree remove .claude/worktrees/up-0.5.0
```

Then add the version to the test's list and to the table above.

The generator, as it made 0.3.0 and 0.4.0:

```go
package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"testing"
)

// TestGenerateUpgradeData writes a data directory as this version leaves it:
// the owner, and a Sonarr instance whose API key is in the vault.
func TestGenerateUpgradeData(t *testing.T) {
	out := os.Getenv("CHADASH_UPGRADE_OUT")
	if out == "" {
		t.Skip("set CHADASH_UPGRADE_OUT")
	}
	a, err := newApp(t.Context(), testConfig(t, out), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(a.handler)
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	do := func(method, path string, body any) (int, string) {
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest(method, srv.URL+path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		rb, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(rb)
	}
	if code, body := do("POST", "/api/setup", map[string]string{"username": "owner", "password": "correct horse battery staple"}); code != 201 {
		t.Fatalf("setup: %d %s", code, body)
	}
	if code, body := do("POST", "/api/integrations/instances", map[string]any{
		"integration": "sonarr", "name": "Sonarr", "enabled": false,
		"config":  map[string]any{"url": "http://127.0.0.1:9/"},
		"secrets": map[string]string{"api_key": "0123456789abcdef0123456789abcdef"},
	}); code != 201 {
		t.Fatalf("instance: %d %s", code, body)
	}
	srv.Close()
	a.close()
}
```
