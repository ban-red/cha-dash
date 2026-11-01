package pvehook

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"dash.cha.sh/internal/audit"
	"dash.cha.sh/internal/authz"
	"dash.cha.sh/internal/runtime"
	"dash.cha.sh/internal/store"
	"dash.cha.sh/internal/vault"
)

// fakeInstances: proxmox-wren (Proxmox, running), proxmox-off (Proxmox,
// stopped) and docker-magpie (not Proxmox).
type fakeInstances struct {
	mu        sync.Mutex
	refreshed []string
}

func (f *fakeInstances) Get(id string) (runtime.InstanceView, error) {
	switch id {
	case "proxmox-wren", "proxmox-off":
		return runtime.InstanceView{ID: id, Integration: "proxmox"}, nil
	case "docker-magpie":
		return runtime.InstanceView{ID: id, Integration: "docker"}, nil
	}
	return runtime.InstanceView{}, runtime.ErrNotFound
}

func (f *fakeInstances) Refresh(id string) error {
	if id == "proxmox-off" {
		return runtime.ErrNotRunning
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refreshed = append(f.refreshed, id)
	return nil
}

func (f *fakeInstances) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.refreshed)
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type env struct {
	t     *testing.T
	s     *Service
	inst  *fakeInstances
	log   *audit.Log
	mux   *http.ServeMux
	clock *clock
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "chadash.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	v, err := vault.New(st, bytes.Repeat([]byte{3}, 32))
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, inst: &fakeInstances{}, log: audit.New(st), mux: http.NewServeMux(), clock: &clock{t: time.Date(2026, 10, 30, 2, 0, 0, 0, time.UTC)}}
	e.s = New(Options{Store: st, Vault: v, Instances: e.inst, Now: e.clock.Now})
	e.s.Audit = &authz.Recorder{Log: e.log}
	e.s.Register(e.mux, func(h http.Handler) http.Handler { return h })
	return e
}

func (e *env) call(method, path, secret string, body []byte) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, bytes.NewReader(body))
	r.RemoteAddr = "10.20.30.11:40000"
	if secret != "" {
		r.Header.Set(Header, secret)
	}
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, r)
	return w
}

func (e *env) rotate(id string) string {
	e.t.Helper()
	w := e.call("POST", "/api/integrations/instances/"+id+"/pve-hook", "", nil)
	var v View
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &v) != nil || len(v.Secret) != 64 || !v.Set {
		e.t.Fatalf("rotate %s = %d %s", id, w.Code, w.Body)
	}
	return v.Secret
}

func (e *env) refusals() []audit.Event {
	e.t.Helper()
	page, err := e.log.Query(e.t.Context(), audit.Filter{Action: ActionRefused})
	if err != nil {
		e.t.Fatal(err)
	}
	return page.Events
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "integrations", "proxmox", "webhook", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestHookAcceptsTheSecretAndOnlySyncs(t *testing.T) {
	e := newEnv(t)
	secret := e.rotate("proxmox-wren")
	body := fixture(t, "backup-failed.json")

	w := e.call("POST", "/api/hooks/pve/proxmox-wren", secret, body)
	if w.Code != 202 || strings.TrimSpace(w.Body.String()) != `{"status":"queued"}` || e.inst.count() != 1 {
		t.Fatalf("notification = %d %s, %d syncs", w.Code, w.Body, e.inst.count())
	}
	// Nothing of the body comes back, anywhere.
	if strings.Contains(w.Body.String(), "script") || strings.Contains(w.Body.String(), "wren.lab.example") {
		t.Fatal("the body was echoed")
	}
	// A burst (a backup night): one sync per 15 s.
	w = e.call("POST", "/api/hooks/pve/proxmox-wren", secret, fixture(t, "fence.txt"))
	if w.Code != 202 || !strings.Contains(w.Body.String(), `"coalesced"`) || e.inst.count() != 1 {
		t.Fatalf("second notification = %d %s, %d syncs", w.Code, w.Body, e.inst.count())
	}
	e.clock.Advance(16 * time.Second)
	if w := e.call("POST", "/api/hooks/pve/proxmox-wren", secret, body); !strings.Contains(w.Body.String(), `"queued"`) || e.inst.count() != 2 {
		t.Fatalf("after 16 s = %s, %d syncs", w.Body, e.inst.count())
	}
	// Last used is recorded.
	w = e.call("GET", "/api/integrations/instances/proxmox-wren/pve-hook", "", nil)
	if !strings.Contains(w.Body.String(), `"last_used_at":`) || strings.Contains(w.Body.String(), secret) || !strings.Contains(w.Body.String(), `"path":"/api/hooks/pve/proxmox-wren"`) {
		t.Fatalf("GET = %s", w.Body)
	}
	// A stopped instance: accepted, nothing to sync.
	off := e.rotate("proxmox-off")
	if w := e.call("POST", "/api/hooks/pve/proxmox-off", off, body); w.Code != 202 || !strings.Contains(w.Body.String(), `"not_running"`) {
		t.Fatalf("stopped instance = %d %s", w.Code, w.Body)
	}
	if len(e.refusals()) != 0 {
		t.Fatal("an accepted notification was audited as refused")
	}
}

func TestHookRefusesAndAudits(t *testing.T) {
	e := newEnv(t)
	body := fixture(t, "backup-failed.json")
	// No secret set yet.
	if w := e.call("POST", "/api/hooks/pve/proxmox-wren", "anything", body); w.Code != 401 {
		t.Fatalf("no secret set = %d", w.Code)
	}
	if ev := e.refusals(); len(ev) != 1 || !strings.Contains(string(ev[0].Params), "no secret is set") || ev[0].Target != "integration:proxmox-wren" || ev[0].Actor.IP != "10.20.30.11" {
		t.Fatalf("audit = %+v", ev)
	}
	secret := e.rotate("proxmox-wren") // a new secret starts a new window
	for _, c := range []struct{ name, secret, reason string }{
		{"missing", "", "the secret header is missing"},
		{"wrong", strings.Repeat("0", 64), "the secret is wrong"},
		{"almost", secret[:63] + "x", "the secret is wrong"},
	} {
		w := e.call("POST", "/api/hooks/pve/proxmox-wren", c.secret, body)
		if w.Code != 401 || !strings.Contains(w.Body.String(), `"code":"unauthorized"`) {
			t.Fatalf("%s secret = %d %s", c.name, w.Code, w.Body)
		}
	}
	ev := e.refusals()
	if len(ev) != 2 || !strings.Contains(string(ev[0].Params), "the secret header is missing") || ev[0].Result != audit.Refused {
		t.Fatalf("one audit per window after the rotation, the first reason: %+v", ev)
	}
	if strings.Contains(string(ev[0].Params), secret[:10]) {
		t.Fatal("the audit holds the secret")
	}
	e.clock.Advance(11 * time.Minute)
	e.call("POST", "/api/hooks/pve/proxmox-wren", "", body)
	if ev := e.refusals(); len(ev) != 2+1 || !strings.Contains(string(ev[0].Params), `"refusals":4`) {
		t.Fatalf("next window = %+v", ev)
	}
	if e.inst.count() != 0 {
		t.Fatal("a refused notification synced")
	}
	// Unknown and non-Proxmox instances: the same 401, not audited.
	for _, id := range []string{"proxmox-nope", "docker-magpie", "bad..id", "-dash"} {
		if w := e.call("POST", "/api/hooks/pve/"+id, secret, body); w.Code != 401 && w.Code != 404 {
			t.Fatalf("%s = %d", id, w.Code)
		}
	}
	if len(e.refusals()) != 3 {
		t.Fatal("an unknown instance was audited")
	}
	// Too large.
	if w := e.call("POST", "/api/hooks/pve/proxmox-wren", secret, bytes.Repeat([]byte("a"), maxBody+1)); w.Code != 413 {
		t.Fatalf("too large = %d", w.Code)
	}
	// A made-up instance answers a large body the same: no telling which exist.
	if w := e.call("POST", "/api/hooks/pve/proxmox-nope", secret, bytes.Repeat([]byte("a"), maxBody+1)); w.Code != 413 {
		t.Fatalf("too large, unknown instance = %d", w.Code)
	}
}

// Wrong secrets from many addresses don't use up what PVE may send.
func TestWrongSecretsDontUseUpTheInstance(t *testing.T) {
	e := newEnv(t)
	secret := e.rotate("proxmox-wren")
	for i := range 40 {
		r := httptest.NewRequest("POST", "/api/hooks/pve/proxmox-wren", nil)
		r.RemoteAddr = fmt.Sprintf("10.20.31.%d:40000", i+1)
		r.Header.Set(Header, "wrong")
		w := httptest.NewRecorder()
		e.mux.ServeHTTP(w, r)
		if w.Code != 401 {
			t.Fatalf("wrong secret %d = %d", i, w.Code)
		}
	}
	if w := e.call("POST", "/api/hooks/pve/proxmox-wren", secret, nil); w.Code != 202 {
		t.Fatalf("PVE itself = %d", w.Code)
	}
}

func TestHookRateLimits(t *testing.T) {
	e := newEnv(t)
	secret := e.rotate("proxmox-wren")
	limited := false
	for range 40 {
		if w := e.call("POST", "/api/hooks/pve/proxmox-wren", secret, nil); w.Code == 429 {
			limited = w.Header().Get("Retry-After") != ""
			break
		}
	}
	if !limited {
		t.Fatal("40 requests at once weren't limited")
	}
}

func TestSecretRoutes(t *testing.T) {
	e := newEnv(t)
	if w := e.call("GET", "/api/integrations/instances/proxmox-wren/pve-hook", "", nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"set":false`) || !strings.Contains(w.Body.String(), `"header":"X-Chadash-Webhook-Secret"`) {
		t.Fatalf("GET before = %d %s", w.Code, w.Body)
	}
	first := e.rotate("proxmox-wren")
	second := e.rotate("proxmox-wren")
	if first == second {
		t.Fatal("rotating kept the secret")
	}
	if w := e.call("POST", "/api/hooks/pve/proxmox-wren", first, nil); w.Code != 401 {
		t.Fatalf("the old secret after a rotation = %d", w.Code)
	}
	page, _ := e.log.Query(t.Context(), audit.Filter{Action: "settings.integration.pve_hook.rotate"})
	if len(page.Events) != 2 || !strings.Contains(string(page.Events[0].Params), `"replaced":true`) || strings.Contains(string(page.Events[0].Params), second) {
		t.Fatalf("rotate audit = %+v", page.Events)
	}
	if w := e.call("DELETE", "/api/integrations/instances/proxmox-wren/pve-hook", "", nil); w.Code != 204 {
		t.Fatalf("DELETE = %d %s", w.Code, w.Body)
	}
	page, _ = e.log.Query(t.Context(), audit.Filter{Action: "settings.integration.pve_hook.delete"})
	if len(page.Events) != 1 || page.Events[0].Target != "integration:proxmox-wren" {
		t.Fatalf("delete audit = %+v", page.Events)
	}
	if w := e.call("DELETE", "/api/integrations/instances/proxmox-wren/pve-hook", "", nil); w.Code != 404 {
		t.Fatalf("DELETE again = %d", w.Code)
	}
	if w := e.call("POST", "/api/hooks/pve/proxmox-wren", second, nil); w.Code != 401 {
		t.Fatalf("after DELETE = %d", w.Code)
	}
	if w := e.call("POST", "/api/integrations/instances/docker-magpie/pve-hook", "", nil); w.Code != 422 {
		t.Fatalf("not Proxmox = %d", w.Code)
	}
	if w := e.call("GET", "/api/integrations/instances/nope/pve-hook", "", nil); w.Code != 404 {
		t.Fatalf("unknown = %d", w.Code)
	}
	_ = io.Discard
}
