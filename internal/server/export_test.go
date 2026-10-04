package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"dash.cha.sh/internal/vault"
)

func TestExportRoutes(t *testing.T) {
	e := newEnvWith(t, func(d *Deps) {
		v, err := vault.New(d.Store, make([]byte, 32))
		if err != nil {
			t.Fatal(err)
		}
		d.Vault = v
	})
	anon := &http.Client{}
	for _, r := range []struct{ method, path string }{
		{"GET", "/api/export"}, {"POST", "/api/export"}, {"POST", "/api/import/chadash"},
	} {
		if resp := e.request(t, anon, r.method, r.path, map[string]any{}, nil); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s %s without a session: %d", r.method, r.path, resp.StatusCode)
		}
	}

	c := signIn(t, e)
	if resp := e.request(t, c, "GET", "/api/boards", nil, nil); resp.StatusCode != 200 {
		t.Fatalf("boards: %d", resp.StatusCode)
	}
	resp := e.request(t, c, "GET", "/api/export", nil, nil)
	file, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/yaml; charset=utf-8" ||
		resp.Header.Get("Cache-Control") != "no-store" || !strings.Contains(string(file), "slug: home") {
		t.Fatalf("export: %d %v\n%s", resp.StatusCode, resp.Header, file)
	}
	// The owner's account is never in it.
	if strings.Contains(string(file), "owner") || strings.Contains(string(file), "correct horse") {
		t.Fatalf("the export holds the account:\n%s", file)
	}
	resp = e.request(t, c, "POST", "/api/import/chadash?dry_run=1", map[string]string{"yaml": string(file)}, nil)
	var body struct {
		Plan struct {
			Summary map[string]int `json:"summary"`
		} `json:"plan"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&body) != nil || body.Plan.Summary["unchanged"] != 1 || body.Plan.Summary["create"] != 0 {
		t.Fatalf("dry run: %d %+v", resp.StatusCode, body)
	}
}
