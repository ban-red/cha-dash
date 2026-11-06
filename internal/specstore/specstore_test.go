package specstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/audit"
	"dash.cha.sh/internal/authz"
	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/integration/integrationtest"
	"dash.cha.sh/internal/spec"
	"dash.cha.sh/internal/store"
	"dash.cha.sh/specs"
)

type env struct {
	t         *testing.T
	st        *store.Store
	reg       *integration.Registry
	s         *Service
	instances map[string]int
	rebound   []string
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
	if _, err := st.Writer().Exec(`INSERT INTO users (id, username, role, created_at, updated_at) VALUES ('u_owner', 'owner', 'owner', 0, 0)`); err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, st: st, instances: map[string]int{}}
	e.restart()
	return e
}

// restart is a hub start: a fresh registry with the bundled Linkding, and
// the installed specs loaded into it.
func (e *env) restart() {
	e.t.Helper()
	e.reg = integration.NewRegistry()
	b, _ := fs.ReadFile(specs.FS, "linkding.yaml")
	sp, err := spec.Parse(b)
	if err != nil {
		e.t.Fatal(err)
	}
	if err := e.reg.Register(sp); err != nil {
		e.t.Fatal(err)
	}
	e.s = New(Options{Store: e.st, Registry: e.reg, Now: func() time.Time { return time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC) }})
	if err := e.s.Load(e.t.Context()); err != nil {
		e.t.Fatal(err)
	}
	e.s.SetRuntime(func(id string) int { return e.instances[id] }, func(id string) int { e.rebound = append(e.rebound, id); return 1 })
}

// community is Seerr's spec under another ID, as a community spec would
// arrive: two actions, T1 and T2.
func community(t *testing.T) string {
	b, _ := fs.ReadFile(specs.FS, "seerr.yaml")
	return strings.ReplaceAll(string(b), "seerr", "myseerr")
}

func TestInstall(t *testing.T) {
	e := newEnv(t)
	src := community(t)

	_, pv, err := e.s.Check(t.Context(), src, false)
	if err != nil {
		t.Fatal(err)
	}
	if pv.Summary.ID != "myseerr" || len(pv.Summary.Requests) != 7 || len(pv.Summary.Actions) != 2 || pv.Replacing != nil {
		t.Fatalf("preview %+v", pv)
	}
	if a := pv.Summary.Actions[0]; a.ID != "myseerr.request.approve" || a.Tier != "T1" || a.Method != "POST" || !strings.Contains(a.Path, "/approve") {
		t.Fatalf("action %+v", a)
	}

	v, _, err := e.s.Install(t.Context(), src, "github.com/ban-red/cha-dash-specs", "u_owner", false)
	if err != nil {
		t.Fatal(err)
	}
	if v.ID != "myseerr" || v.ActionsOn || v.InstalledBy != "owner" || len(v.SHA256) != 64 || v.Source != "github.com/ban-red/cha-dash-specs" {
		t.Fatalf("installed %+v", v)
	}
	_, m, ok := e.reg.Get("myseerr")
	if !ok || !m.Installed || m.Engine != integration.EngineSpec {
		t.Fatalf("registered %+v %v", m, ok)
	}

	// A bundled ID, a broken spec and a second install are refused.
	b, _ := fs.ReadFile(specs.FS, "linkding.yaml")
	for what, c := range map[string]struct {
		yaml string
		code string
	}{
		"bundled":  {string(b), "bundled"},
		"invalid":  {strings.Replace(src, "path: /api/v1/status", "path: api/v1/status", 1), "invalid_spec"},
		"again":    {src, "exists"},
		"too long": {src + strings.Repeat("#", MaxYAML), "too_large"},
	} {
		_, _, err := e.s.Install(t.Context(), c.yaml, "", "u_owner", false)
		if p, ok := err.(*Problem); !ok || p.Code != c.code {
			t.Errorf("%s: %v", what, err)
		}
	}

	// It survives a restart, actions still off.
	e.restart()
	if _, m, ok := e.reg.Get("myseerr"); !ok || !m.Installed || e.s.ActionsOn("myseerr") {
		t.Fatal("not registered after a restart")
	}
}

// An installed spec's actions refuse until the owner turns them on; a
// replacement with other actions turns them off again.
func TestActionsOff(t *testing.T) {
	e := newEnv(t)
	src := community(t)
	v0, _, err := e.s.Install(t.Context(), src, "", "u_owner", false)
	if err != nil {
		t.Fatal(err)
	}
	integ, _, _ := e.reg.Get("myseerr")
	h := integrationtest.New(t)
	inst := h.Connect(integ, map[string]any{"url": "http://seerr.lab.example"}, map[string]string{"api_key": "fake-key"})
	actor, ok := inst.(integration.Actor)
	if !ok {
		t.Fatal("not an actor")
	}
	defs := integration.ActionDefs(actor)
	if len(defs) != 2 {
		t.Fatalf("defs %d", len(defs))
	}
	req := action.Request{Action: defs[0].ID, Target: action.Target{ID: "myseerr:i_test/request/1"}}
	err = defs[0].Preconditions(t.Context(), req)
	if r, ok := err.(*action.Refusal); !ok || r.Code != "unavailable" || !strings.Contains(r.Message, "actions stay off until the owner turns them on") {
		t.Fatalf("off: %v", err)
	}
	if err := defs[0].Execute(t.Context(), req, nil); err == nil || !strings.Contains(err.Error(), "turned this spec's actions off") {
		t.Fatalf("execute while off: %v", err)
	}

	if _, err := e.s.SetActions(t.Context(), "myseerr", true, v0.SHA256); err != nil {
		t.Fatal(err)
	}
	if err := defs[0].Preconditions(t.Context(), req); err != nil && strings.Contains(err.Error(), "stay off") {
		t.Fatalf("on: %v", err)
	}

	// The same actions: still on after a replace, and its instances restart.
	newer := strings.Replace(src, "version: 0.", "version: 9.", 1)
	if newer == src {
		t.Fatal("no version to bump")
	}
	v, replaced, err := e.s.Install(t.Context(), newer, "", "u_owner", true)
	if err != nil || !replaced || !v.ActionsOn || len(e.rebound) != 1 {
		t.Fatalf("replace with the same actions: %+v %v %v", v, err, e.rebound)
	}
	// Turning them on names the version reviewed: the one before is stale.
	if _, err := e.s.SetActions(t.Context(), "myseerr", true, v0.SHA256); err == nil || err.(*Problem).Code != "changed" {
		t.Fatalf("on with a stale sha256: %v", err)
	}
	// The same method, path and tier but another verify: off again.
	body := strings.Replace(newer, `+ " declined."'`, `+ " declined, as asked."'`, 1)
	if body == newer {
		t.Fatal("no decline verify message")
	}
	if v, _, err := e.s.Install(t.Context(), body, "", "u_owner", true); err != nil || v.ActionsOn {
		t.Fatalf("replace with a changed write: %+v %v", v, err)
	}
	if _, err := e.s.SetActions(t.Context(), "myseerr", true, v.SHA256); err == nil {
		t.Fatal("on with the replaced version's sha256")
	}
	cur, _ := e.s.List(t.Context())
	if _, err := e.s.SetActions(t.Context(), "myseerr", true, cur[0].SHA256); err != nil {
		t.Fatal(err)
	}
	// Other actions: off again.
	other := strings.Replace(newer, "tier: T1", "tier: T2", 1)
	if v, _, err := e.s.Install(t.Context(), other, "", "u_owner", true); err != nil || v.ActionsOn {
		t.Fatalf("replace with other actions: %+v %v", v, err)
	}
}

// A read that POSTs can change things whatever it's called: an installed
// spec's waits for its actions, and changing one turns them off again.
func TestPostReadsWaitForActions(t *testing.T) {
	e := newEnv(t)
	posting := strings.Replace(community(t), "requests:\n", "requests:\n  poke:\n    method: POST\n    path: /api/v1/settings/jobs/run\n    body: '{\"all\": true}'\n    extract:\n      ok: ok\n", 1)
	v, _, err := e.s.Install(t.Context(), posting, "", "u_owner", false)
	if err != nil {
		t.Fatal(err)
	}
	integ, _, _ := e.reg.Get("myseerr")
	h := integrationtest.New(t)
	inst := h.Connect(integ, map[string]any{"url": "http://127.0.0.1:1"}, map[string]string{"api_key": "fake-key"})
	err = inst.Sync(t.Context(), integrationtest.Watched)
	if re, ok := errors.AsType[*integration.RepairError](err); !ok || re.Message != spec.WritesOff {
		t.Fatalf("sync while off: %v", err)
	}
	if _, err := e.s.SetActions(t.Context(), "myseerr", true, v.SHA256); err != nil {
		t.Fatal(err)
	}
	// On: the POST goes out (and finds nothing listening).
	if re, ok := errors.AsType[*integration.RepairError](inst.Sync(t.Context(), integrationtest.Watched)); ok && re.Message == spec.WritesOff {
		t.Fatal("still gated with the actions on")
	}
	// Another body: off again.
	changed := strings.Replace(posting, `{"all": true}`, `{"all": false}`, 1)
	if v, _, err := e.s.Install(t.Context(), changed, "", "u_owner", true); err != nil || v.ActionsOn {
		t.Fatalf("replace with another POST: %+v %v", v, err)
	}
	// So do new source headers, which actions send too.
	if _, err := e.s.SetActions(t.Context(), "myseerr", true, func() string { l, _ := e.s.List(t.Context()); return l[0].SHA256 }()); err != nil {
		t.Fatal(err)
	}
	headers := strings.Replace(changed, `X-Api-Key: "{{ secret.api_key }}"`, "X-Api-Key: \"{{ secret.api_key }}\"\n    X-Forwarded-User: admin", 1)
	if v, _, err := e.s.Install(t.Context(), headers, "", "u_owner", true); err != nil || v.ActionsOn {
		t.Fatalf("replace with other headers: %+v %v", v, err)
	}
}

func TestRemove(t *testing.T) {
	e := newEnv(t)
	if _, _, err := e.s.Install(t.Context(), community(t), "", "", false); err != nil {
		t.Fatal(err)
	}
	e.instances["myseerr"] = 1
	if _, err := e.s.Remove(t.Context(), "myseerr"); err == nil || err.(*Problem).Code != "in_use" {
		t.Fatalf("remove with an instance: %v", err)
	}
	e.instances["myseerr"] = 0
	if _, err := e.s.Remove(t.Context(), "myseerr"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := e.reg.Get("myseerr"); ok {
		t.Fatal("still registered")
	}
	if _, err := e.s.Remove(t.Context(), "myseerr"); err != ErrNotFound {
		t.Fatalf("remove twice: %v", err)
	}
}

// The routes, with a step-up where they ask for one, and their audit
// records (server.TestAuditedRoutesRecord names this test).
func TestRoutes(t *testing.T) {
	e := newEnv(t)
	log := audit.New(e.st)
	e.s.Audit = &authz.Recorder{Log: log}
	owner := authz.Actor{Kind: authz.KindUser, ID: "u_owner", Name: "owner", Role: "owner", Session: "s_1"}
	e.s.actorOf = func(*http.Request) (authz.Actor, bool) { return owner, true }
	mux := http.NewServeMux()
	e.s.Register(mux, func(h http.Handler) http.Handler { return h })
	do := func(method, path string, body any) (int, string) {
		b, _ := json.Marshal(body)
		r := httptest.NewRequestWithContext(context.Background(), method, path, strings.NewReader(string(b)))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
	src := community(t)

	if code, body := do("POST", "/api/specs/installed/preview", map[string]any{"yaml": src}); code != 200 || !strings.Contains(body, `"myseerr.request.decline"`) {
		t.Fatalf("preview %d %s", code, body)
	}
	if code, body := do("POST", "/api/specs/installed", map[string]any{"yaml": src}); code != 403 || !strings.Contains(body, "step_up_required") {
		t.Fatalf("install without a step-up %d %s", code, body)
	}
	owner.StepUpUntil = time.Now().Add(time.Minute)
	code, body := do("POST", "/api/specs/installed", map[string]any{"yaml": src, "source": "a paste"})
	var made Installed
	if code != 200 || json.Unmarshal([]byte(body), &made) != nil || made.ActionsOn {
		t.Fatalf("install %d %s", code, body)
	}
	if code, body := do("POST", "/api/specs/installed/preview", map[string]any{"yaml": src}); code != 409 || !strings.Contains(body, `"exists"`) || !strings.Contains(body, `"replacing"`) {
		t.Fatalf("preview of an installed one %d %s", code, body)
	}
	if code, body := do("PUT", "/api/specs/installed/myseerr/actions", map[string]any{"on": true}); code != 409 || !strings.Contains(body, `"changed"`) {
		t.Fatalf("actions on without naming the version %d %s", code, body)
	}
	if code, body := do("PUT", "/api/specs/installed/myseerr/actions", map[string]any{"on": true, "sha256": made.SHA256}); code != 200 || !strings.Contains(body, `"actions_on":true`) {
		t.Fatalf("actions on %d %s", code, body)
	}
	if code, body := do("GET", "/api/specs/installed", nil); code != 200 || !strings.Contains(body, `"id":"myseerr"`) {
		t.Fatalf("list %d %s", code, body)
	}
	e.instances["myseerr"] = 2
	if code, body := do("DELETE", "/api/specs/installed/myseerr", nil); code != 409 || !strings.Contains(body, "2 instance(s)") {
		t.Fatalf("remove in use %d %s", code, body)
	}
	e.instances["myseerr"] = 0
	if code, body := do("DELETE", "/api/specs/installed/myseerr", nil); code != 200 {
		t.Fatalf("remove %d %s", code, body)
	}

	page, err := log.Query(t.Context(), audit.Filter{Action: "settings.specs."})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, ev := range page.Events {
		got = append(got, ev.Action+" "+ev.Result)
	}
	want := []string{"settings.specs.remove ok", "settings.specs.actions ok", "settings.specs.install ok", "settings.specs.install refused"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("audit %q", got)
	}
}

// A spec installed under an ID the hub later bundles: it doesn't load, says
// why, and neither replacing nor removing it touches the bundled one.
func TestBundledLater(t *testing.T) {
	e := newEnv(t)
	b, _ := fs.ReadFile(specs.FS, "linkding.yaml")
	src := strings.ReplaceAll(string(b), "linkding", "mylinks")
	if _, _, err := e.s.Install(t.Context(), src, "", "", false); err != nil {
		t.Fatal(err)
	}
	// The next hub bundles mylinks.
	e.restart()
	e.reg.Unregister("mylinks")
	bundled, err := spec.Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.reg.Register(bundled); err != nil {
		t.Fatal(err)
	}
	// Loaded again on that hub: refused, with the reason listed.
	e.s = New(Options{Store: e.st, Registry: e.reg})
	if err := e.s.Load(t.Context()); err != nil {
		t.Fatal(err)
	}
	list, _ := e.s.List(t.Context())
	if len(list) != 1 || !strings.Contains(list[0].Error, "now includes an integration called") {
		t.Fatalf("list %+v", list)
	}
	if _, m, _ := e.reg.Get("mylinks"); m.Installed {
		t.Fatal("the installed spec replaced the bundled one")
	}
	if _, _, err := e.s.Install(t.Context(), src, "", "", true); err == nil || err.(*Problem).Code != "bundled" {
		t.Fatalf("replace over a bundled ID: %v", err)
	}
	if _, err := e.s.Remove(t.Context(), "mylinks"); err != nil {
		t.Fatal(err)
	}
	if _, m, ok := e.reg.Get("mylinks"); !ok || m.Installed {
		t.Fatal("removing the row unregistered the bundled integration")
	}
}

// An installed v1alpha1 spec, stored by an older hub, migrates to v1beta1
// at start and keeps working: the row holds the migrated YAML, its actions
// keep their switch, and its instance syncs the same before and after.
func TestInstalledV1Alpha1Migrates(t *testing.T) {
	e := newEnv(t)
	b, err := os.ReadFile(filepath.Join("..", "spec", "testdata", "v1alpha1", "seerr.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	old := strings.ReplaceAll(string(b), "seerr", "myseerr")
	if !strings.Contains(old, "apiVersion: chadash/v1alpha1") || !strings.Contains(old, "(config.pending_warn ?? 20)") {
		t.Fatal("not the v1alpha1 Seerr")
	}
	sum := sha256.Sum256([]byte(old))
	if _, err := e.st.Writer().Exec(`INSERT INTO installed_specs (id, spec_id, yaml, sha256, source, actions_on, installed_by, installed_at, updated_at)
		VALUES ('isp_old', 'myseerr', ?, ?, '', 1, 'u_owner', 1, 1)`, old, hex.EncodeToString(sum[:])); err != nil {
		t.Fatal(err)
	}

	// Seerr's synthetic fixtures; 3 pending is under the default 20.
	dir := filepath.Join("..", "..", "specs", "testdata", "seerr")
	mux := http.NewServeMux()
	for route, file := range map[string]string{"GET /api/v1/status": "status.json", "GET /api/v1/request/count": "request_count.json", "GET /api/v1/request": "request_pending.json"} {
		body, err := os.ReadFile(filepath.Join(dir, file))
		if err != nil {
			t.Fatal(err)
		}
		mux.HandleFunc(route, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write(body)
		})
	}
	srv := httptest.NewServer(mux)
	defer srv.Close()
	h := integrationtest.New(t)
	syncWith := func(integ integration.Integration, want string) graph.Resource {
		t.Helper()
		inst := h.Connect(integ, map[string]any{"url": srv.URL}, map[string]string{"api_key": "fake-key"})
		if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
			t.Fatalf("%s: %v", want, err)
		}
		r, ok := h.Resource("myseerr:i_test/app")
		if !ok || r.Health != integration.HealthOK || r.Attrs["type"] != "myseerr" {
			t.Fatalf("%s: app %+v", want, r)
		}
		return r
	}
	syncOnce := func(want string) graph.Resource {
		t.Helper()
		integ, m, ok := e.reg.Get("myseerr")
		if !ok || !m.Installed {
			t.Fatal("myseerr isn't registered")
		}
		if v := integ.(installed).APIVersion(); v != want {
			t.Fatalf("registered as %s, want %s", v, want)
		}
		return syncWith(integ, want)
	}
	// What the v1alpha1 spec publishes, as the older hub ran it.
	sp, err := spec.Parse([]byte(old))
	if err != nil {
		t.Fatal(err)
	}
	before := syncWith(sp, spec.V1Alpha1)

	// The hub starts: Load migrates the row.
	e.restart()
	list, err := e.s.List(t.Context())
	if err != nil || len(list) != 1 {
		t.Fatal(list, err)
	}
	v := list[0]
	if !strings.Contains(v.YAML, "apiVersion: chadash/v1beta1") || strings.Contains(v.YAML, "?? 20") || v.Error != "" || !v.ActionsOn || !v.UpdatedAt.After(v.InstalledAt) {
		t.Fatalf("after start: %+v", v)
	}
	now := sha256.Sum256([]byte(v.YAML))
	if v.SHA256 != hex.EncodeToString(now[:]) || v.SHA256 == hex.EncodeToString(sum[:]) || !strings.Contains(v.YAML, "# ") {
		t.Fatalf("the row's sha256 and comments: %s", v.SHA256)
	}
	after := syncOnce(spec.V1Beta1)
	if after.State != before.State || after.Health != before.Health || !reflect.DeepEqual(after.Attrs, before.Attrs) {
		t.Fatalf("v1alpha1 published %+v, v1beta1 %+v", before, after)
	}

	// Another start finds nothing to migrate and runs the same.
	e.restart()
	if again, _ := e.s.List(t.Context()); again[0].SHA256 != v.SHA256 || !again[0].ActionsOn {
		t.Fatalf("migrated twice: %+v", again[0])
	}
	if r := syncOnce(spec.V1Beta1); r.Attrs["health.reason"] != after.Attrs["health.reason"] || r.State != after.State {
		t.Fatalf("%+v, then %+v", after, r)
	}

	// Installing a v1alpha1 spec stores it as v1beta1.
	other := strings.ReplaceAll(old, "myseerr", "myseerr2")
	got, _, err := e.s.Install(t.Context(), other, "", "u_owner", false)
	if err != nil || !strings.Contains(got.YAML, "apiVersion: chadash/v1beta1") || strings.Contains(got.YAML, "?? 20") {
		t.Fatalf("install v1alpha1: %v %.300s", err, got.YAML)
	}
}
