package pbs

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/integration/integrationtest"
)

const (
	fixtures   = "../../testdata/integrations/pbs"
	fixtureTok = "api@pbs!chadash"
	fakeSecret = "00000000-fake-4000-8000-pbstest00000"
	capturedAt = "2026-10-04T01:30:00Z"
)

var stores = []string{"mars", "mini-g5", "pluto.lab.example", "pve.lab.example", "store-2"}

// replay serves the synthetic fixture by path and query; anything else is a
// 404 and is recorded.
type replay struct {
	*httptest.Server
	dir string

	mu       sync.Mutex
	entries  []indexEntry
	override map[string]reply
	seen     []string
	unknown  []string
}

type indexEntry struct {
	Path   string `json:"path"`
	File   string `json:"file"`
	Status int    `json:"status"`
}

type reply struct {
	status int
	body   string
}

func newReplay(t *testing.T) *replay {
	t.Helper()
	r := &replay{dir: filepath.Join(fixtures, "jupiter"), override: map[string]reply{}}
	b, err := os.ReadFile(filepath.Join(r.dir, "_index.json"))
	if err != nil {
		t.Fatal(err)
	}
	var idx struct {
		Synthetic bool         `json:"synthetic"`
		Requests  []indexEntry `json:"requests"`
	}
	if err := json.Unmarshal(b, &idx); err != nil {
		t.Fatal(err)
	}
	if !idx.Synthetic {
		t.Fatal("the PBS fixture must say it's synthetic")
	}
	r.entries = idx.Requests
	r.Server = httptest.NewTLSServer(http.HandlerFunc(r.serve))
	t.Cleanup(r.Close)
	return r
}

func (r *replay) serve(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	path := strings.TrimPrefix(req.URL.Path, "/api2/json")
	// /version answers anybody, as PBS's does.
	if path != "/version" && req.Header.Get("Authorization") != "PBSAPIToken="+fixtureTok+":"+fakeSecret {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"data":null,"message":"authentication failed - invalid credentials"}`)
		return
	}
	r.mu.Lock()
	r.seen = append(r.seen, path)
	o, haveO := r.override[path]
	r.mu.Unlock()
	if haveO {
		w.WriteHeader(o.status)
		fmt.Fprint(w, o.body)
		return
	}
	for _, e := range r.entries {
		ep, eq, _ := strings.Cut(e.Path, "?")
		want, _ := url.ParseQuery(eq)
		got := req.URL.Query()
		if ep != path || !(reflect.DeepEqual(want, got) || len(want) == 0 && len(got) == 0) {
			continue
		}
		body, err := os.ReadFile(filepath.Join(r.dir, e.File))
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.WriteHeader(e.Status)
		w.Write(body)
		return
	}
	r.mu.Lock()
	r.unknown = append(r.unknown, req.URL.RequestURI())
	r.mu.Unlock()
	w.WriteHeader(http.StatusNotFound)
	fmt.Fprint(w, `{"data":null,"message":"Path not found."}`)
}

func (r *replay) set(path string, status int, body string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.override[path] = reply{status, body}
}

func (r *replay) take() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.seen
	r.seen = nil
	return s
}

func (r *replay) unknownPaths() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.unknown)
}

// edit rewrites one fixture response's data array.
func (r *replay) edit(t *testing.T, path, file string, mutate func([]map[string]any)) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(r.dir, file))
	if err != nil {
		t.Fatal(err)
	}
	var env struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(b, &env); err != nil {
		t.Fatal(err)
	}
	mutate(env.Data)
	nb, _ := json.Marshal(env)
	r.set(path, 200, string(nb))
}

func connect(t *testing.T) (*replay, *integrationtest.Harness, *instance) {
	t.Helper()
	srv := newReplay(t)
	h := integrationtest.New(t)
	at, _ := time.Parse(time.RFC3339, capturedAt)
	h.Advance(at.Sub(h.Now()))
	in := h.Connect(Integration{}, map[string]any{"url": srv.URL, "tls": map[string]any{"mode": "tofu"}, "token_id": fixtureTok},
		map[string]string{"token_secret": fakeSecret})
	return srv, h, in.(*instance)
}

func syncOK(t *testing.T, in *instance, want integration.Interest) {
	t.Helper()
	if err := in.Sync(t.Context(), want); err != nil {
		t.Fatalf("sync: %v", err)
	}
}

func res(t *testing.T, h *integrationtest.Harness, local string) graph.Resource {
	t.Helper()
	r, ok := h.Resource("pbs:i_test/" + local)
	if !ok {
		t.Fatalf("no resource %s; have %v", local, h.Graph().Resources)
	}
	return r
}

func value(t *testing.T, h *integrationtest.Harness, local, key string) float64 {
	t.Helper()
	s := h.Series("pbs:i_test/"+local, key)
	if s == nil {
		t.Fatalf("no series %s#%s", local, key)
	}
	v, ok := s["value"].(float64)
	if !ok {
		return math.NaN()
	}
	return v
}

func warningCodes(ws []integration.Warning) []string {
	var out []string
	for _, w := range ws {
		out = append(out, w.Code)
	}
	return out
}

func TestRegistered(t *testing.T) {
	_, m, ok := integration.Default.Get(ID)
	if !ok || m.Name != "Proxmox Backup Server" || m.Poll.Interval != liveInterval {
		t.Fatalf("manifest = %+v", m)
	}
	if got := m.SecretFields(); !slices.Equal(got, []string{"token_secret"}) {
		t.Fatalf("secret fields = %v", got)
	}
}

// TestValidate: the shape the Homepage importer writes for a
// proxmoxbackupserver widget (url, tls, token_id, token_secret).
func TestValidate(t *testing.T) {
	for _, cfg := range []map[string]any{
		{"url": "https://jupiter.lab.example:8007", "tls": map[string]any{"mode": "tofu"}, "token_id": "api@pbs!homepage"},
		{"url": "https://pbs.lab.example:8007/", "tls": map[string]any{"mode": "verify"}, "token_id": "root@pam!homepage"},
	} {
		if errs := integrationtest.Validate(Integration{}, cfg, map[string]string{"token_secret": fakeSecret}); len(errs) > 0 {
			t.Errorf("%v: %v", cfg, errs)
		}
	}
	for name, cfg := range map[string]map[string]any{
		"bare user":   {"url": "https://pbs.lab.example:8007", "token_id": "api@pbs"},
		"no url":      {"token_id": "api@pbs!dash"},
		"ftp":         {"url": "ftp://pbs.lab.example", "token_id": "api@pbs!dash"},
		"unknown key": {"url": "https://pbs.lab.example:8007", "token_id": "api@pbs!dash", "datastore": "main"},
	} {
		if errs := integrationtest.Validate(Integration{}, cfg, map[string]string{"token_secret": fakeSecret}); len(errs) == 0 {
			t.Errorf("%s validated", name)
		}
	}
}

func TestTestReadsGrants(t *testing.T) {
	srv, _, in := connect(t)
	r := in.Test(t.Context())
	if !r.OK || r.Version != "4.0.14" || len(r.Grants) != 2 {
		t.Fatalf("test = %+v", r)
	}
	for _, g := range r.Grants {
		if g.CanWrite() || !strings.Contains(g.Detail, "/datastore") {
			t.Errorf("grant %+v", g)
		}
	}
	if len(r.Warnings) != 0 {
		t.Errorf("warnings for an Audit token: %v", r.Warnings)
	}
	if u := srv.unknownPaths(); len(u) > 0 {
		t.Errorf("uncaptured paths: %v", u)
	}
	// A DatastoreAudit-only token: tasks and the node can't be read.
	srv.set("/access/permissions", 200, `{"data":{"/datastore":{"Datastore.Audit":true}}}`)
	r = in.Test(t.Context())
	if codes := warningCodes(r.Warnings); !slices.Equal(codes, []string{"tasks_hidden", "node_status_hidden"}) {
		t.Fatalf("warnings = %v", codes)
	}
	// Write privileges are marked.
	srv.set("/access/permissions", 200, `{"data":{"/":{"Sys.Audit":true,"Datastore.Audit":true,"Datastore.Modify":true,"Datastore.Read":true}}}`)
	r = in.Test(t.Context())
	byName := map[string]integration.Grant{}
	for _, g := range r.Grants {
		byName[g.Name] = g
	}
	if !byName["Datastore.Modify"].Write || byName["Datastore.Read"].Write || byName["Sys.Audit"].Write {
		t.Fatalf("grants = %+v", r.Grants)
	}
}

func TestAuthRejected(t *testing.T) {
	srv := newReplay(t)
	h := integrationtest.New(t)
	in := h.Connect(Integration{}, map[string]any{"url": srv.URL, "tls": map[string]any{"mode": "tofu"}, "token_id": fixtureTok},
		map[string]string{"token_secret": "wrong"})
	// /version answers anybody; the permissions call must catch it.
	err := in.Sync(t.Context(), integrationtest.Watched)
	if code, _, _ := integration.Classify(err); code != integration.CodeAuth {
		t.Fatalf("sync error %v → %s", err, code)
	}
	if r := in.Test(t.Context()); r.OK || r.ErrorCode != integration.CodeAuth {
		t.Fatalf("test = %+v", r)
	}
}

func TestUnsupportedVersion(t *testing.T) {
	srv, _, in := connect(t)
	srv.set("/version", 200, `{"data":{"version":"2.4","release":"7","repoid":"0000000000000000"}}`)
	err := in.Sync(t.Context(), integrationtest.Watched)
	if code, _, msg := integration.Classify(err); code != integration.CodeUnsupportedVersion || !strings.Contains(msg, "2.4.7") {
		t.Fatalf("sync error %v → %s", err, code)
	}
}

func TestFixture(t *testing.T) {
	srv, h, in := connect(t)
	syncOK(t, in, integrationtest.Watched)
	if u := srv.unknownPaths(); len(u) > 0 {
		t.Fatalf("uncaptured paths: %v", u)
	}
	app := res(t, h, "app")
	if app.Kind != "app" || app.Attrs["type"] != "pbs" || app.Name != "Test" || app.Attrs["version"] != "4.0.14" || app.Health != "attention" ||
		app.Attrs["health.reason"] != "1 task failed in the last 24 h" {
		t.Fatalf("app = %+v", app)
	}
	if v := value(t, h, "app", "tasks.failed_24h"); v != 1 { // the GC with warnings isn't a failure
		t.Errorf("tasks.failed_24h = %v", v)
	}
	if v := value(t, h, "app", "mem.pct"); v != 62.2 {
		t.Errorf("mem.pct = %v", v)
	}
	for _, s := range stores {
		ds := res(t, h, "datastore/"+s)
		if ds.Kind != "backup_target" || ds.Health != "attention" || ds.Attrs["capacity_tb"] != 45.98 || ds.Attrs["full_in_days"] != float64(20) {
			t.Errorf("%s = %+v", s, ds)
		}
		if v := value(t, h, "datastore/"+s, "used.pct"); v != 91.9 {
			t.Errorf("%s used.pct = %v", s, v)
		}
		found := false
		for _, r := range h.Graph().Relations {
			found = found || string(r.From) == "pbs:i_test/datastore/"+s && r.Type == "part_of" && string(r.To) == "pbs:i_test/app"
		}
		if !found {
			t.Errorf("%s isn't part of the app", s)
		}
	}
	mars := res(t, h, "datastore/mars")
	if mars.Attrs["prune_job"] != "disabled" || mars.Attrs["retention"] != "keep-daily 7 · weekly 4 · monthly 6" ||
		mars.Attrs["gc_last"] != "2026-09-14T05:00:00Z" || mars.Attrs["gc_schedule"] != "none" {
		t.Errorf("mars = %v", mars.Attrs)
	}
	if res(t, h, "datastore/mini-g5").Attrs["prune_job"] != "none" {
		t.Error("mini-g5 has no prune job")
	}
	s2 := res(t, h, "datastore/store-2")
	if !strings.Contains(s2.Attrs["health.reason"].(string), "verification failed") || s2.Attrs["verify_state"] == nil {
		t.Errorf("store-2 = %v", s2.Attrs)
	}
	if res(t, h, "datastore/pve.lab.example").Attrs["gc_state"] != "WARNINGS: 2" {
		t.Error("the GC with warnings isn't recorded")
	}

	// Five datastores on one filesystem are one problem.
	inc, ok := h.Incident("filling.mars")
	if !ok || inc.Scenario != "pbs-datastore-filling" || inc.Facts["used_pct"] != float64(92) || len(inc.Facts["stores"].([]any)) != 5 ||
		!strings.Contains(inc.Summary, "share one filesystem") || !strings.Contains(inc.Summary, "about 3 weeks") {
		t.Fatalf("filling incident = %+v", inc)
	}
	var causes []string
	for _, c := range inc.Causes {
		causes = append(causes, c.Text)
	}
	all := strings.Join(causes, "\n")
	if !strings.Contains(all, "prune-mars on mars is disabled") || !strings.Contains(all, "mini-g5 and pve.lab.example have no prune job") ||
		!strings.Contains(all, "Garbage collection on mars last ran") {
		t.Errorf("causes = %s", all)
	}
	for _, s := range stores[1:] {
		if _, ok := h.Incident("filling." + s); ok {
			t.Errorf("a second filling incident for %s", s)
		}
	}
	tk, ok := h.Incident("tasks.failed")
	if !ok || tk.Title != "Verification of store-2 failed" || tk.Target != "pbs:i_test/datastore/store-2" || tk.Facts["failed"] != float64(1) {
		t.Fatalf("tasks incident = %+v", tk)
	}
	if len(in.warnings()) != 0 {
		t.Errorf("warnings = %v", in.warnings())
	}
}

// TestPollPlan: usage and failed tasks every sync; jobs every 5 minutes;
// the node only while watched; version and permissions hourly.
func TestPollPlan(t *testing.T) {
	srv, h, in := connect(t)
	syncOK(t, in, integrationtest.Watched)
	first := srv.take()
	if len(first) != 9 {
		t.Errorf("first sync read %v", first)
	}
	step := func(d time.Duration, want integration.Interest, expect ...string) {
		t.Helper()
		h.Advance(d)
		syncOK(t, in, want)
		got := srv.take()
		slices.Sort(got)
		slices.Sort(expect)
		if !slices.Equal(got, expect) {
			t.Errorf("after %v: read %v, want %v", d, got, expect)
		}
	}
	// The task query moves with the clock, so after the first sync the
	// replay's fixed "since" no longer matches; serve the same body.
	b, _ := os.ReadFile(filepath.Join(fixtures, "jupiter/nodes/localhost/tasks.json"))
	srv.set("/nodes/localhost/tasks", 200, string(b))
	step(time.Minute, integrationtest.Watched, "/status/datastore-usage", "/nodes/localhost/tasks")
	step(5*time.Minute, integration.Interest{}, "/status/datastore-usage", "/nodes/localhost/tasks",
		"/admin/datastore", "/admin/gc", "/admin/prune", "/admin/verify")
	step(55*time.Minute, integration.Interest{}, "/version", "/access/permissions", "/status/datastore-usage", "/nodes/localhost/tasks",
		"/admin/datastore", "/admin/gc", "/admin/prune", "/admin/verify")
}

// TestLeastPrivilege: a DatastoreAudit-only token sees the datastores but
// not tasks or the node. Failed tasks are unknown, not zero, and the
// instance says why.
func TestLeastPrivilege(t *testing.T) {
	srv, h, in := connect(t)
	srv.set("/access/permissions", 200, `{"data":{"/datastore":{"Datastore.Audit":true}}}`)
	syncOK(t, in, integrationtest.Watched)
	for _, p := range srv.take() {
		if p == "/nodes/localhost/tasks" || p == "/nodes/localhost/status" {
			t.Errorf("asked for %s without the privilege", p)
		}
	}
	if v := value(t, h, "app", "tasks.failed_24h"); !math.IsNaN(v) {
		t.Errorf("tasks.failed_24h = %v, want unknown", v)
	}
	if !slices.Contains(warningCodes(in.warnings()), "tasks_hidden") {
		t.Errorf("warnings = %v", in.warnings())
	}
	if h.Series("pbs:i_test/app", "cpu.pct") != nil {
		t.Error("cpu.pct without the node status")
	}
	res(t, h, "datastore/mars")
}

// TestDatastoreStates: an error is down with its own incident; maintenance
// is maintenance; when usage drops the filling incident resolves.
func TestDatastoreStates(t *testing.T) {
	srv, h, in := connect(t)
	syncOK(t, in, integrationtest.Watched)
	srv.edit(t, "/status/datastore-usage", "status/datastore-usage.json", func(d []map[string]any) {
		for _, e := range d {
			switch e["store"] {
			case "store-2":
				for _, k := range []string{"total", "used", "avail", "history", "gc-status", "estimated-full-date"} {
					delete(e, k)
				}
				e["error"] = "datastore 'store-2' is not mounted"
			default:
				e["used"] = 20000000000000.0
				e["estimated-full-date"] = 0
			}
		}
	})
	srv.edit(t, "/admin/datastore", "admin/datastore.json", func(d []map[string]any) {
		for _, e := range d {
			if e["store"] == "mars" {
				e["maintenance"] = "type=read-only,message=moving"
			}
		}
	})
	b, _ := os.ReadFile(filepath.Join(fixtures, "jupiter/nodes/localhost/tasks.json"))
	srv.set("/nodes/localhost/tasks", 200, string(b))
	h.Advance(5 * time.Minute)
	syncOK(t, in, integrationtest.Watched)
	s2 := res(t, h, "datastore/store-2")
	if s2.Health != "down" || s2.State != "error" || !math.IsNaN(value(t, h, "datastore/store-2", "used.pct")) {
		t.Fatalf("store-2 = %+v", s2)
	}
	if inc, ok := h.Incident("datastore.store-2.error"); !ok || inc.Severity != integration.SeverityDown {
		t.Fatalf("error incident = %+v", inc)
	}
	if m := res(t, h, "datastore/mars"); m.Health != "maintenance" || m.Attrs["maintenance"] != "read-only" {
		t.Fatalf("mars = %+v", m)
	}
	if inc, _ := h.Incident("filling.mars"); inc.Status != integration.StatusResolved {
		t.Fatalf("filling incident = %+v", inc)
	}
	if v := value(t, h, "datastore/mini-g5", "used.pct"); v != 43.5 {
		t.Errorf("used.pct = %v", v)
	}
}

func TestNoSecretInRequests(t *testing.T) {
	srv, _, in := connect(t)
	syncOK(t, in, integrationtest.Watched)
	in.Test(t.Context())
	for _, p := range srv.take() {
		if strings.Contains(p, fakeSecret) {
			t.Fatalf("the secret is in %s", p)
		}
	}
}
