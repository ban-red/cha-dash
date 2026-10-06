package export

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"dash.cha.sh/internal/boards"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/store"
	"dash.cha.sh/internal/vault"
)

func TestMain(m *testing.M) {
	scryptLogN = 10 // fast bundles; TestBundleDefaultWorkFactor uses the real one
	os.Exit(m.Run())
}

// Fake integrations with the shapes v0.1's have: a URL, TLS and a required
// key (like Sonarr), and an optional username and password (like Traefik).
type keyed struct{}

func (keyed) Manifest() integration.Manifest {
	return integration.Manifest{ID: "keyed", Name: "Keyed", Icon: "keyed", Version: "0.1.0", Tier: integration.Bronze,
		ConfigSchema: integration.Object(integration.URLProp(), integration.TLSProp(),
			integration.Prop("label", integration.String("Label")),
			integration.Prop("api_key", integration.Secret("API key"))).Require("url", "api_key")}
}

func (keyed) Connect(context.Context, integration.Config, integration.Deps) (integration.Instance, error) {
	return nil, fmt.Errorf("not in these tests")
}

type pair struct{}

func (pair) Manifest() integration.Manifest {
	return integration.Manifest{ID: "pair", Name: "Pair", Icon: "pair", Version: "0.1.0", Tier: integration.Bronze,
		ConfigSchema: integration.Object(integration.URLProp(), integration.TLSProp(),
			integration.Prop("username", integration.String("Username")),
			integration.Prop("password", integration.Secret("Password"))).Require("url")}
}

func (pair) Connect(context.Context, integration.Config, integration.Deps) (integration.Instance, error) {
	return nil, fmt.Errorf("not in these tests")
}

func registry(t *testing.T) *integration.Registry {
	t.Helper()
	reg := integration.NewRegistry()
	for _, i := range []integration.Integration{keyed{}, pair{}} {
		if err := reg.Register(i); err != nil {
			t.Fatal(err)
		}
	}
	return reg
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

type hub struct {
	st    *store.Store
	vault *vault.Vault
	svc   *Service
	logs  *lockedBuffer
	log   *slog.Logger
}

var fixedNow = time.Date(2026, 10, 4, 9, 30, 0, 0, time.UTC)

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
	key := make([]byte, 32)
	rand.Read(key)
	v, err := vault.New(st, key)
	if err != nil {
		t.Fatal(err)
	}
	logs := &lockedBuffer{}
	return &hub{st: st, vault: v, logs: logs, log: slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		svc: New(st, v, Options{Registry: registry(t), HubVersion: "0.1.0-test", Now: func() time.Time { return fixedNow }})}
}

func (h *hub) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	if _, err := h.st.Writer().ExecContext(t.Context(), query, args...); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

func (h *hub) addBoard(t *testing.T, id, slug, name string, position int64, layout string, tiles ...string) {
	t.Helper()
	h.exec(t, `INSERT INTO boards (id, slug, title, position, created_at, updated_at) VALUES (?, ?, ?, ?, 1, 1)`, id, slug, name, position)
	if layout != "" {
		var td json.RawMessage
		if len(tiles) > 0 {
			td = json.RawMessage(tiles[0])
		}
		if _, err := boards.New(h.st).SaveLayout(t.Context(), id, 0, 1, json.RawMessage(layout), td, ""); err != nil {
			t.Fatal(err)
		}
	}
}

func (h *hub) addInstance(t *testing.T, id, integ, name string, enabled bool, config map[string]any, secrets map[string]string) {
	t.Helper()
	cfg := map[string]any{}
	for k, v := range config {
		cfg[k] = v
	}
	for field, value := range secrets {
		name := "instance/" + id + "/" + field
		if err := h.vault.Put(t.Context(), name, []byte(value)); err != nil {
			t.Fatal(err)
		}
		cfg[field] = integration.SecretRef(name)
	}
	b, _ := json.Marshal(cfg)
	h.exec(t, `INSERT INTO integration_instances (id, integration, name, config, enabled, created_at, updated_at) VALUES (?, ?, ?, ?, ?, 1, 1)`,
		id, integ, name, string(b), boolInt(enabled))
}

// The fixture's secret values. Tests scan every output for them.
var secretValues = map[string]string{
	"sonarr":  "FAKE-sonarr-api-key-0123456789abcdef",
	"radarr":  "FAKE-radarr-api-key-fedcba9876543210",
	"traefik": "FAKE-traefik-password-correct-horse",
	"legacy":  "FAKE-legacy-token-for-a-gone-integration",
}

const passphrase = "a long enough test passphrase"

const homeLayout = `{"version":1,"columns":12,"sections":[` +
	`{"id":"media","title":"Media","span":8,"items":[` +
	`{"id":"service@homepage:media/sonarr","type":"service","size":"small","supportedSizes":["small","wide"],"x":0,"y":0},` +
	`{"id":"service@homepage:media/radarr","type":"service","size":"wide","supportedSizes":["small","wide"],"x":2,"y":0}]},` +
	`{"id":"net","span":4,"items":[{"id":"router@opnsense:i_x/router","type":"router","size":"large","supportedSizes":["large"],"x":0,"y":0}]}]}`

// homeTiles is b_home's tiles document: what the layout's items can't hold.
const homeTiles = `{"service@homepage:media/sonarr":{"name":"Sonarr","href":"https://sonarr.home.arpa","bind":{"instance":"i_sonarr"},` +
	`"origin":{"source":"homepage","file":"services.yaml","group":"Media","entry":"Sonarr"}},` +
	`"service@homepage:media/radarr":{"name":"Radarr","href":"https://radarr.home.arpa","bind":{"sources":[{"instance":"i_radarr","capability":"media.calendar"}]},` +
	`"options":{"unmonitored":true},"origin":{"source":"homepage","file":"services.yaml","group":"Media","entry":"Radarr"}}}`

// record is the Homepage import record: what an import keeps beyond the board.
const record = `{"source":"homepage","board_id":"b_home","instances":["i_sonarr"],"links":[],"discovery":[]}`

// seed fills a hub with what a lived-in home has, plus everything that must
// never leave it: users, sessions, passkeys, audit, the client id.
func seed(t *testing.T, h *hub) {
	t.Helper()
	h.addBoard(t, "b_home", "home", "Home", 0, homeLayout, homeTiles)
	h.addBoard(t, "b_lab", "lab", "Lab", 1, `{"version":1,"columns":12,"sections":[]}`)
	h.addBoard(t, "b_empty", "empty", "Never saved", 2, "")
	h.addInstance(t, "i_sonarr", "keyed", "Sonarr", true,
		map[string]any{"url": "https://sonarr.home.arpa", "tls": map[string]any{"mode": "tofu", "pin": strings.Repeat("ab", 32)}, "label": "TV"},
		map[string]string{"api_key": secretValues["sonarr"]})
	h.addInstance(t, "i_radarr", "keyed", "Radarr", false,
		map[string]any{"url": "https://radarr.home.arpa:7878", "tls": map[string]any{"mode": "verify"}},
		map[string]string{"api_key": secretValues["radarr"]})
	h.addInstance(t, "i_traefik", "pair", "Traefik", true,
		map[string]any{"url": "http://traefik.home.arpa:8080", "username": "admin"},
		map[string]string{"password": secretValues["traefik"]})
	h.addInstance(t, "i_legacy", "gone", "Legacy", true,
		map[string]any{"url": "https://legacy.home.arpa", "port": 8443},
		map[string]string{"token": secretValues["legacy"]})
	h.exec(t, `INSERT INTO settings (key, value, updated_at) VALUES ('import/homepage', ?, 1)`, record)
	h.exec(t, `INSERT INTO settings (key, value, updated_at) VALUES ('integrations/client_id', '"FAKE-client-id-never-exported"', 1)`)
	h.exec(t, `INSERT INTO users (id, username, password_hash, role, created_at, updated_at) VALUES ('u_owner', 'FAKE-owner-name', '$argon2id$FAKE-hash', 'owner', 1, 1)`)
	h.exec(t, `INSERT INTO sessions (id, token_hash, user_id, method, created_at, expires_at, last_seen_at) VALUES ('s_one', X'00112233', 'u_owner', 'password', 1, 2, 1)`)
	h.exec(t, `INSERT INTO audit (at, actor, action, result) VALUES (1, '{"user":"FAKE-audit-actor"}', 'FAKE-audit-action', 'ok')`)
	h.exec(t, `INSERT INTO settings (key, value, updated_at) VALUES ('onboarding', '{"step":"FAKE-onboarding-state"}', 1)`)
	h.exec(t, `INSERT INTO correlation_decisions (id, from_id, rel, to_id, verdict, rule, from_instance, to_instance, created_at)
		VALUES ('cd_1', 'keyed:i_sonarr/app', 'same_as', 'keyed:i_radarr/app', 'reject', 'FAKE-correlation-rule', 'i_sonarr', 'i_radarr', 1)`)
}

// mustNotLeak fails if text holds any fixture secret, the passphrase, or
// anything that must never be exported.
func mustNotLeak(t *testing.T, what, text string) {
	t.Helper()
	for k, v := range secretValues {
		if strings.Contains(text, v) {
			t.Fatalf("%s holds the %s secret value", what, k)
		}
	}
	for _, s := range []string{passphrase, "FAKE-client-id-never-exported", "FAKE-owner-name", "FAKE-hash", "FAKE-audit",
		"FAKE-onboarding-state", "cd_1"} { // decisions travel since v0.2, their row IDs never
		if strings.Contains(text, s) {
			t.Fatalf("%s holds %q", what, s)
		}
	}
}

// snapshot is every row an import may touch, timestamps included.
func snapshot(t *testing.T, st *store.Store) string {
	t.Helper()
	var sb strings.Builder
	for _, q := range []string{
		`SELECT id, slug, title, position, created_at, updated_at FROM boards ORDER BY id`,
		`SELECT board_id, revision, format_version, document, tiles FROM layouts ORDER BY board_id, revision`,
		`SELECT id, integration, name, config, enabled, created_at, updated_at FROM integration_instances ORDER BY id`,
		`SELECT name, hex(ciphertext), updated_at FROM vault_secrets ORDER BY name`,
		`SELECT key, value, updated_at FROM settings ORDER BY key`,
	} {
		rows, err := st.Reader().QueryContext(t.Context(), q)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			fmt.Fprintln(&sb, vals...)
		}
		rows.Close()
	}
	return sb.String()
}

func actions(p *Plan) map[string]string {
	m := map[string]string{}
	for _, c := range p.Changes {
		m[c.Kind+":"+c.ID] = c.Action
	}
	return m
}

func change(t *testing.T, p *Plan, kind, id string) Change {
	t.Helper()
	for _, c := range p.Changes {
		if c.Kind == kind && c.ID == id {
			return c
		}
	}
	t.Fatalf("no %s %s change in %+v", kind, id, p.Changes)
	return Change{}
}

func parse(t *testing.T, data []byte) *Document {
	t.Helper()
	d, err := Parse(data)
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, data)
	}
	return d
}

func jsonOf(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// withoutMeta drops the lines that differ between two exports of equal
// hubs: the time of the export.
func withoutMeta(data []byte) string {
	var keep []string
	for _, l := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(l, "  exported_at:") {
			keep = append(keep, l)
		}
	}
	return strings.Join(keep, "\n")
}

func vaultValue(t *testing.T, v *vault.Vault, name string) string {
	t.Helper()
	b, err := v.Get(t.Context(), name)
	if err != nil {
		t.Fatalf("vault %s: %v", name, err)
	}
	return string(b)
}

func rowCount(t *testing.T, st *store.Store, table string) int {
	t.Helper()
	var n int
	if err := st.Reader().QueryRowContext(t.Context(), `SELECT count(*) FROM `+table).Scan(&n); err != nil && err != sql.ErrNoRows {
		t.Fatal(err)
	}
	return n
}
