package export

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func (h *hub) serve(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	Register(mux, func(h http.Handler) http.Handler { return h }, h.svc, h.log)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func call(t *testing.T, srv *httptest.Server, method, path, contentType string, body any) (int, http.Header, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, srv.URL+path, rd)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, b
}

func errCode(t *testing.T, body []byte) string {
	t.Helper()
	var e struct {
		Error struct{ Code, Message string } `json:"error"`
	}
	if err := json.Unmarshal(body, &e); err != nil {
		t.Fatalf("%s: %v", body, err)
	}
	return e.Error.Code
}

func TestHTTP(t *testing.T) {
	a := newHub(t)
	seed(t, a)
	srv := a.serve(t)
	const js = "application/json"
	var outputs []string

	code, hdr, plain := call(t, srv, "GET", "/api/export", "", nil)
	if code != 200 || hdr.Get("Content-Type") != "application/yaml; charset=utf-8" ||
		hdr.Get("Content-Disposition") != `attachment; filename="chadash-home-2026-10-04.yaml"` || !bytes.HasPrefix(plain, []byte("# Cha Dash Home file")) {
		t.Fatalf("GET /api/export: %d %v\n%s", code, hdr, plain)
	}
	outputs = append(outputs, string(plain))

	if code, _, body := call(t, srv, "POST", "/api/export", js, map[string]string{"passphrase": "short"}); code != 422 || errCode(t, body) != "weak_passphrase" {
		t.Fatalf("weak passphrase: %d %s", code, body)
	}
	if code, _, body := call(t, srv, "POST", "/api/export", "application/x-www-form-urlencoded", map[string]string{"passphrase": passphrase}); code != 415 {
		t.Fatalf("form post: %d %s", code, body)
	}
	code, hdr, full := call(t, srv, "POST", "/api/export", js, map[string]string{"passphrase": passphrase})
	if code != 200 || hdr.Get("Content-Disposition") != `attachment; filename="chadash-home-2026-10-04-with-secrets.yaml"` || !bytes.Contains(full, []byte("\nsecrets_bundle:\n")) {
		t.Fatalf("POST /api/export: %d %v\n%s", code, hdr, full)
	}
	outputs = append(outputs, string(full))

	// A dry run against the same hub: nothing would change.
	code, _, body := call(t, srv, "POST", "/api/import/chadash?dry_run=1", js, map[string]string{"yaml": string(full), "passphrase": passphrase})
	if code != 200 {
		t.Fatalf("dry run: %d %s", code, body)
	}
	outputs = append(outputs, string(body))
	var dry map[string]map[string]json.RawMessage
	if err := json.Unmarshal(body, &dry); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"format", "exported_at", "hub_version", "changes", "secrets_needed", "bundle", "summary", "warnings"} {
		if _, ok := dry["plan"][k]; !ok {
			t.Errorf("plan lacks %s: %s", k, body)
		}
	}
	if string(dry["plan"]["summary"]) != `{"create":0,"update":0,"unchanged":11,"stale":0,"secrets_restored":0,"secrets_needed":0}` ||
		string(dry["plan"]["bundle"]) != `{"present":true,"unlocked":true,"secrets":4}` {
		t.Fatalf("plan = %s", body)
	}
	if !strings.Contains(string(body), `{"kind":"instance","id":"i_sonarr","name":"Sonarr","action":"unchanged","integration":"keyed","secrets":[{"field":"api_key","secret":"instance/i_sonarr/api_key","action":"unchanged","source":"vault"}]}`) {
		t.Fatalf("instance change shape: %s", body)
	}

	// Errors name lines and paths, never values.
	for name, tc := range map[string]struct {
		path string
		body map[string]string
		code int
		err  string
	}{
		"bad dry_run":      {"/api/import/chadash?dry_run=maybe", map[string]string{"yaml": string(plain)}, 400, "invalid"},
		"no yaml":          {"/api/import/chadash?dry_run=1", map[string]string{"yaml": "  "}, 422, "invalid"},
		"plaintext secret": {"/api/import/chadash?dry_run=1", map[string]string{"yaml": head + "instances:\n- {id: i_x, integration: keyed, name: X, secrets: {api_key: '" + secretValues["radarr"] + "'}}\n"}, 422, "invalid_export"},
		"value in config":  {"/api/import/chadash", map[string]string{"yaml": head + "instances:\n- {id: i_x, integration: keyed, name: X, config: {api_key: '" + secretValues["radarr"] + "'}}\n"}, 422, "invalid_export"},
		"wrong passphrase": {"/api/import/chadash", map[string]string{"yaml": string(full), "passphrase": "not " + passphrase}, 422, "wrong_passphrase"},
	} {
		code, _, body := call(t, srv, "POST", tc.path, js, tc.body)
		if code != tc.code || errCode(t, body) != tc.err {
			t.Errorf("%s: %d %s", name, code, body)
		}
		outputs = append(outputs, string(body))
	}

	// Applying the full backup to a fresh hub restores it.
	b := newHub(t)
	code, _, body = call(t, b.serve(t), "POST", "/api/import/chadash", js, map[string]string{"yaml": string(full), "passphrase": passphrase})
	if code != 200 {
		t.Fatalf("apply: %d %s", code, body)
	}
	outputs = append(outputs, string(body))
	var applied struct {
		Plan        Plan     `json:"plan"`
		Changed     bool     `json:"changed"`
		BoardIDs    []string `json:"board_ids"`
		InstanceIDs []string `json:"instance_ids"`
	}
	if err := json.Unmarshal(body, &applied); err != nil {
		t.Fatal(err)
	}
	if !applied.Changed || len(applied.BoardIDs) != 3 || len(applied.InstanceIDs) != 4 || applied.Plan.Summary.SecretsRestored != 4 {
		t.Fatalf("applied = %s", body)
	}
	if got := vaultValue(t, b.vault, "instance/i_traefik/password"); got != secretValues["traefik"] {
		t.Fatal("the bundle didn't restore the password")
	}

	for i, o := range outputs {
		mustNotLeak(t, "response "+string(rune('0'+i)), o)
	}
	mustNotLeak(t, "the logs", a.logs.String()+b.logs.String())
	if !strings.Contains(a.logs.String(), "exported the hub") || !strings.Contains(b.logs.String(), "home file imported") {
		t.Fatalf("logs:\n%s\n%s", a.logs, b.logs)
	}
}
