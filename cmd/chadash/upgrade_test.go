package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"dash.cha.sh/internal/store"
)

// upgradeKey is the API key the old hubs stored in the vault
// (testdata/upgrade/README.md).
const upgradeKey = "0123456789abcdef0123456789abcdef"

// oldDataDir rebuilds a data directory a released version left behind,
// from testdata/upgrade/<version>: its databases from their dumps, and its
// vault key.
func oldDataDir(t *testing.T, version string) string {
	t.Helper()
	src := filepath.Join("..", "..", "testdata", "upgrade", version)
	dir := t.TempDir()
	for _, name := range []string{"chadash", "history"} {
		dump, err := os.ReadFile(filepath.Join(src, name+".sql"))
		if err != nil {
			t.Fatal(err)
		}
		db, err := store.Open(t.Context(), filepath.Join(dir, name+".db")) // opens, doesn't migrate
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Writer().ExecContext(t.Context(), string(dump)); err != nil {
			t.Fatalf("%s %s.sql: %v", version, name, err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}
	key, err := os.ReadFile(filepath.Join(src, "vault-key.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secret.key"), key, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// The current hub opens what 0.3.0 and 0.4.0 left: it migrates the
// database to the newest schema (0.4.0's may be it already), the owner signs in with the same
// password, and the vault still opens the secret stored under the old
// version (the instance's API key reaches its server).
func TestUpgradeFromReleasedVersions(t *testing.T) {
	fresh, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "fresh.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fresh.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	newest, err := fresh.SchemaVersion(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	fresh.Close()

	for _, version := range []string{"0.3.0", "0.4.0"} {
		t.Run(version, func(t *testing.T) {
			dir := oldDataDir(t, version)
			before := schemaVersion(t, filepath.Join(dir, "chadash.db"))
			if before > newest {
				t.Fatalf("%s's database is at %d, past this build's %d", version, before, newest)
			}

			a, err := newApp(t.Context(), testConfig(t, dir), slog.New(slog.DiscardHandler))
			if err != nil {
				t.Fatalf("the hub won't open %s's data: %v", version, err)
			}
			t.Cleanup(func() { a.close() })
			srv := httptest.NewServer(a.handler)
			t.Cleanup(srv.Close)
			s := &system{t: t, srv: srv, dir: dir}
			if v := schemaVersion(t, filepath.Join(dir, "chadash.db")); v != newest {
				t.Fatalf("migrated from %d to %d, want %d", before, v, newest)
			}

			// The owner signs in with the password set under the old version.
			jar, _ := cookiejar.New(nil)
			s.c = &http.Client{Jar: jar}
			if code, body := s.do(s.c, "POST", "/api/auth/login", map[string]string{"username": "owner", "password": "correct horse battery staple"}, nil); code != http.StatusOK {
				t.Fatalf("sign-in: %d %s", code, body)
			}
			code, body := s.do(s.c, "GET", "/api/integrations/instances", nil, nil)
			var list []struct {
				ID          string `json:"id"`
				Integration string `json:"integration"`
				Secrets     map[string]struct {
					Set bool `json:"set"`
				} `json:"secrets"`
			}
			if code != http.StatusOK || json.Unmarshal(body, &list) != nil || len(list) != 1 || list[0].Integration != "sonarr" || !list[0].Secrets["api_key"].Set {
				t.Fatalf("instances: %d %s", code, body)
			}

			// The key, decrypted with the old vault key, reaches Sonarr.
			var mu sync.Mutex
			var got []string
			sonarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				got = append(got, r.Header.Get("X-Api-Key"))
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"appName":"Sonarr","version":"4.0.15.2941"}`))
			}))
			t.Cleanup(sonarr.Close)
			if code, body := s.do(s.c, "POST", "/api/auth/step-up", map[string]string{"password": "correct horse battery staple"}, nil); code != http.StatusOK {
				t.Fatalf("step-up: %d %s", code, body)
			}
			if code, body := s.do(s.c, "PUT", "/api/integrations/instances/"+list[0].ID, map[string]any{"config": map[string]any{"url": sonarr.URL + "/"}}, nil); code != http.StatusOK {
				t.Fatalf("pointing it at the fake Sonarr: %d %s", code, body)
			}
			if code, body := s.do(s.c, "POST", "/api/integrations/instances/"+list[0].ID+"/test", nil, nil); code != http.StatusOK {
				t.Fatalf("test: %d %s", code, body)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(got) == 0 || strings.Join(got, ",") != strings.Repeat(upgradeKey+",", len(got)-1)+upgradeKey {
				t.Fatalf("Sonarr got X-Api-Key %q, want the key stored under %s", got, version)
			}
		})
	}
}

func schemaVersion(t *testing.T, path string) int64 {
	t.Helper()
	st, err := store.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	v, err := st.SchemaVersion(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return v
}
