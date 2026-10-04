package homepage

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"dash.cha.sh/internal/importer"
	"dash.cha.sh/internal/store"
	"dash.cha.sh/internal/vault"
)

const fixtureDir = "../../../testdata/importers/homepage/dogfood"

func readFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(fixtureDir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func dogfood(t *testing.T) Input {
	t.Helper()
	return Input{Services: readFixture(t, "services.yaml"), Docker: readFixture(t, "docker.yaml"), Settings: readFixture(t, "settings.yaml")}
}

func build(t *testing.T, in Input) (*importer.Result, importer.Secrets) {
	t.Helper()
	res, secrets, err := Build(in)
	if err != nil {
		t.Fatal(err)
	}
	return res, secrets
}

type hub struct {
	st    *store.Store
	vault *vault.Vault
	svc   *importer.Service
	logs  *syncBuffer
	srv   *httptest.Server
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// newHub opens a fresh database and vault and serves the import routes with
// a debug-level logger whose output the test can inspect.
func newHub(t *testing.T) *hub {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(t.Context(), filepath.Join(dir, "chadash.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	logs := &syncBuffer{}
	log := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	v, err := vault.Open(t.Context(), st, vault.KeyConfig{Path: filepath.Join(dir, "secret.key"), Generate: true}, log)
	if err != nil {
		t.Fatal(err)
	}
	h := &hub{st: st, vault: v, svc: importer.New(st, v), logs: logs}
	mux := http.NewServeMux()
	Register(mux, func(h http.Handler) http.Handler { return h }, h.svc, log)
	h.srv = httptest.NewServer(mux)
	t.Cleanup(h.srv.Close)
	return h
}

func (h *hub) post(t *testing.T, query string, body []byte) (int, []byte) {
	t.Helper()
	resp, err := http.Post(h.srv.URL+"/api/import/homepage"+query, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func (h *hub) get(t *testing.T) (int, []byte) {
	t.Helper()
	resp, err := http.Get(h.srv.URL + "/api/import/homepage")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// fixtureSecretRE finds credential values in the fixture's YAML text,
// commented-out ones included, independently of the importer.
var fixtureSecretRE = regexp.MustCompile(`(?m)^[\s#]*(?:key|password):\s*([^\s#]+)`)

// fixtureSecrets returns every credential value written in the dogfood
// files: keys and passwords (active or commented out), OPNsense's API key
// (which Homepage calls username), and the placeholder weather API keys.
// None of them may ever appear in a plan, a response, a log or a config row.
func fixtureSecrets(t *testing.T) []string {
	t.Helper()
	services := readFixture(t, "services.yaml")
	var out []string
	for _, m := range fixtureSecretRE.FindAllStringSubmatch(services, -1) {
		v := strings.Trim(m[1], `"'`)
		if v != "" && !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	for _, line := range strings.Split(services, "\n") {
		if strings.Contains(line, "username: FAKEopnsense") {
			out = append(out, strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "username:")))
		}
	}
	out = append(out, "openweathermapapikey", "weatherapiapikey")
	if len(out) < 20 {
		t.Fatalf("the leak oracle found only %d values: %v", len(out), out)
	}
	return out
}

func assertNoLeak(t *testing.T, what, text string, secrets []string) {
	t.Helper()
	for i, s := range secrets {
		if strings.Contains(text, s) {
			t.Errorf("%s contains fixture secret #%d", what, i)
		}
	}
}
