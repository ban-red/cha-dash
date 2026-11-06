package truenas

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"dash.cha.sh/internal/backups"
	"dash.cha.sh/internal/correlate"
	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/integration/integrationtest"
)

const (
	fixtures = "../../testdata/integrations/truenas"
	// fakeKey has the shape of a TrueNAS key, <id>-<64 characters>.
	fakeKey = "7-FAKEtruenasKEY000000000000000000000000000000000000000000000000000"
)

// rpcReply overrides a method's answer: a result, or a JSON-RPC error.
type rpcReply struct {
	result any
	err    map[string]any
}

// fake is TrueNAS's /api/current over TLS, answering from the fixture
// folder: <method>.json is the method's result.
type fake struct {
	*httptest.Server
	t   *testing.T
	dir string

	mu       sync.Mutex
	override map[string]rpcReply
	calls    []string
	params   map[string]json.RawMessage
	logins   int
	conns    []*websocket.Conn
	status   int // answer the upgrade with this status instead
}

func newFake(t *testing.T) *fake {
	t.Helper()
	f := &fake{t: t, dir: filepath.Join(fixtures, "nas"), override: map[string]rpcReply{}, params: map[string]json.RawMessage{}}
	f.Server = httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *fake) set(method string, r rpcReply) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.override[method] = r
}

func (f *fake) took() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.calls
	f.calls = nil
	return c
}

// drop closes every open connection, as a TrueNAS restart would.
func (f *fake) drop() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.conns {
		c.CloseNow()
	}
	f.conns = nil
}

func (f *fake) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	status := f.status
	f.mu.Unlock()
	if status != 0 {
		http.Error(w, http.StatusText(status), status)
		return
	}
	if r.URL.Path != apiPath || r.URL.RawQuery != "" {
		http.NotFound(w, r)
		return
	}
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	c.SetReadLimit(1 << 20)
	f.mu.Lock()
	f.conns = append(f.conns, c)
	f.mu.Unlock()
	ctx := r.Context()
	authed := false
	for {
		_, data, err := c.Read(ctx)
		if err != nil {
			return
		}
		var req struct {
			ID     int               `json:"id"`
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(data, &req); err != nil {
			return
		}
		f.mu.Lock()
		f.calls = append(f.calls, req.Method)
		if len(req.Params) > 0 {
			b, _ := json.Marshal(req.Params)
			f.params[req.Method] = b
		}
		o, haveO := f.override[req.Method]
		f.mu.Unlock()
		var result any
		var rpcErr map[string]any
		switch {
		case req.Method == "auth.login_with_api_key":
			var key string
			if len(req.Params) == 1 {
				_ = json.Unmarshal(req.Params[0], &key)
			}
			authed = key == fakeKey
			f.mu.Lock()
			f.logins++
			f.mu.Unlock()
			result = authed
		case !authed:
			rpcErr = map[string]any{"code": -32001, "message": "Not authenticated", "data": map[string]any{"error": 13, "errname": "ENOTAUTHENTICATED", "reason": "Not authenticated"}}
		case haveO:
			result, rpcErr = o.result, o.err
		default:
			b, err := os.ReadFile(filepath.Join(f.dir, req.Method+".json"))
			if err != nil {
				rpcErr = map[string]any{"code": -32601, "message": "Method not found"}
				break
			}
			result = json.RawMessage(b)
		}
		resp := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		if rpcErr != nil {
			resp["error"] = rpcErr
		} else {
			resp["result"] = result
		}
		// A notification first, which the client must skip.
		_ = c.Write(ctx, websocket.MessageText, []byte(`{"jsonrpc":"2.0","method":"collection_update","params":{"msg":"changed","collection":"alert.list"}}`))
		b, _ := json.Marshal(resp)
		if err := c.Write(ctx, websocket.MessageText, b); err != nil {
			return
		}
	}
}

func connect(t *testing.T) (*fake, *integrationtest.Harness, *instance) {
	t.Helper()
	srv := newFake(t)
	h := integrationtest.New(t)
	in := h.Connect(Integration{}, map[string]any{"url": srv.URL, "tls": map[string]any{"mode": "tofu"}},
		map[string]string{"api_key": fakeKey})
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
	r, ok := h.Resource("truenas:i_test/" + local)
	if !ok {
		var ids []string
		for id := range h.Graph().Resources {
			ids = append(ids, string(id))
		}
		slices.Sort(ids)
		t.Fatalf("no resource %s; have %v", local, ids)
	}
	return r
}

func value(t *testing.T, h *integrationtest.Harness, local, key string) float64 {
	t.Helper()
	s := h.Series("truenas:i_test/"+local, key)
	if s == nil {
		t.Fatalf("no series %s#%s", local, key)
	}
	v, ok := s["value"].(float64)
	if !ok {
		return math.NaN()
	}
	return v
}

func TestValidate(t *testing.T) {
	ok := map[string]any{"url": "https://nas.lab.example", "tls": map[string]any{"mode": "tofu"}}
	if errs := integrationtest.Validate(Integration{}, ok, map[string]string{"api_key": fakeKey}); len(errs) > 0 {
		t.Fatalf("valid config refused: %v", errs)
	}
	for name, c := range map[string]struct {
		cfg     map[string]any
		secrets map[string]string
		path    string
	}{
		"plain http": {map[string]any{"url": "http://nas.lab.example"}, map[string]string{"api_key": fakeKey}, "/url"},
		"ws scheme":  {map[string]any{"url": "wss://nas.lab.example"}, map[string]string{"api_key": fakeKey}, "/url"},
		"no key":     {map[string]any{"url": "https://nas.lab.example"}, nil, ""},
	} {
		errs := integrationtest.Validate(Integration{}, c.cfg, c.secrets)
		if len(errs) == 0 {
			t.Errorf("%s: accepted", name)
			continue
		}
		if c.path != "" && errs[0].Path != c.path {
			t.Errorf("%s: %v", name, errs)
		}
		if c.path == "/url" && !strings.Contains(errs[0].Message, "revokes") {
			t.Errorf("%s: the message doesn't say why: %q", name, errs[0].Message)
		}
	}
}

func TestTest(t *testing.T) {
	srv, _, in := connect(t)
	r := in.Test(t.Context())
	if !r.OK || r.Version != "25.10.0" {
		t.Fatalf("test = %+v", r)
	}
	if len(r.Grants) != 1 || r.Grants[0].Name != "READONLY_ADMIN" || r.Grants[0].CanWrite() {
		t.Fatalf("grants = %+v", r.Grants)
	}
	for _, w := range r.Warnings {
		t.Errorf("unexpected warning %+v", w)
	}

	srv.set("auth.me", rpcReply{result: map[string]any{"pw_name": "admin", "privilege": map[string]any{"roles": []string{"FULL_ADMIN"}, "web_shell": true}}})
	r = in.Test(t.Context())
	if !r.OK || !slices.ContainsFunc(r.Warnings, func(w integration.Warning) bool { return w.Code == "write_grant" }) {
		t.Fatalf("a full admin's key should warn: %+v", r)
	}
}

func TestSync(t *testing.T) {
	srv, h, in := connect(t)
	syncOK(t, in, integrationtest.Watched)
	calls := srv.took()
	for _, m := range []string{"auth.login_with_api_key", "system.info", "pool.query", "pool.dataset.query", "disk.query",
		"app.query", "pool.snapshottask.query", "replication.query", "alert.list", "disk.temperatures"} {
		if !slices.Contains(calls, m) {
			t.Errorf("didn't call %s; called %v", m, calls)
		}
	}
	var dsParams []any
	_ = json.Unmarshal(srv.params["pool.dataset.query"], &dsParams)
	if b, _ := json.Marshal(dsParams); !strings.Contains(string(b), `"flat":true`) || !strings.Contains(string(b), `"retrieve_children":false`) {
		t.Errorf("pool.dataset.query params = %s", b)
	}

	host := res(t, h, "host")
	if host.Kind != "host" || host.Name != "nas01" || host.Attrs["platform"] != "truenas" || host.Attrs["version"] != "25.10.0" ||
		host.Attrs["cores"] != float64(8) && host.Attrs["cores"] != 8 || host.Attrs["ip"] != "127.0.0.1" {
		t.Errorf("host = %+v", host)
	}
	if host.Health != "attention" || reason(host) != "5 active alerts" {
		t.Errorf("host health = %+v", host.Health)
	}
	if v := value(t, h, "host", "alerts.active"); v != 5 {
		t.Errorf("alerts.active = %v", v)
	}

	// Pools.
	tank := res(t, h, "pool/tank")
	if tank.Kind != "pool" || tank.State != "degraded" || tank.Health != "attention" || !strings.Contains(reason(tank), "sdd") {
		t.Errorf("tank = %+v", tank)
	}
	if tank.Attrs["layout"] != "RAIDZ2" || tank.Attrs["scrub_state"] != "finished" {
		t.Errorf("tank attrs = %v", tank.Attrs)
	}
	if v := value(t, h, "pool/tank", "used.pct"); math.Abs(v-71.2) > 0.1 {
		t.Errorf("tank used.pct = %v", v)
	}
	fast := res(t, h, "pool/fast")
	if fast.Health != "ok" || fast.State != "online" || fast.Attrs["layout"] != "MIRROR" {
		t.Errorf("fast = %+v", fast)
	}
	if v := value(t, h, "pool/fast", "used.pct"); math.Abs(v-41.9) > 0.1 {
		t.Errorf("fast used.pct = %v", v)
	}

	// Datasets: the roots, .system and ix-apps aren't published.
	for _, hidden := range []string{"dataset/tank", "dataset/fast", "dataset/tank/.system", "dataset/tank/ix-apps"} {
		if _, ok := h.Resource("truenas:i_test/" + hidden); ok {
			t.Errorf("%s published", hidden)
		}
	}
	media := res(t, h, "dataset/tank/media")
	if media.Kind != "share" || media.Attrs["path"] != "/mnt/tank/media" || media.Attrs["snapshots"] != true {
		t.Errorf("tank/media = %+v", media)
	}
	if v := value(t, h, "dataset/tank/backups", "used.pct"); v != 80 {
		t.Errorf("tank/backups used.pct = %v", v)
	}
	if zv := res(t, h, "dataset/fast/vms/homeassistant"); zv.Kind != "volume" {
		t.Errorf("zvol = %+v", zv)
	}
	if v := res(t, h, "dataset/tank/vault"); v.State != "locked" || v.Health != "unknown" {
		t.Errorf("locked dataset = %+v", v)
	}

	// Disks.
	sdd := res(t, h, "disk/FAKEHDD00004")
	if sdd.Health != "down" || sdd.State != "faulted" || !strings.Contains(reason(sdd), "SMART") ||
		sdd.Attrs["role"] != "data" || sdd.Attrs["pool"] != "tank" {
		t.Errorf("sdd = %+v", sdd)
	}
	if v := value(t, h, "disk/FAKEHDD00004", "temp.c"); v != 44 {
		t.Errorf("sdd temp = %v", v)
	}
	if sda := res(t, h, "disk/FAKEHDD00001"); sda.Health != "ok" || sda.Attrs["smart"] != "ok" || sda.Attrs["rotational"] != true {
		t.Errorf("sda = %+v", sda)
	}
	if boot := res(t, h, "disk/FAKESSD000001"); boot.Attrs["role"] != "boot" {
		t.Errorf("boot disk = %+v", boot)
	}

	// Apps.
	if nc := res(t, h, "app/nextcloud"); nc.Health != "down" || nc.State != "crashed" {
		t.Errorf("nextcloud = %+v", nc)
	}
	if jf := res(t, h, "app/jellyfin"); jf.State != "running" || jf.Attrs["update_available"] != true || jf.Attrs["latest_version"] != "1.2.15" {
		t.Errorf("jellyfin = %+v", jf)
	}
	if st := res(t, h, "app/syncthing"); st.State != "stopped" || st.Health != "ok" {
		t.Errorf("syncthing = %+v", st)
	}

	// Snapshot and replication tasks.
	if s3 := res(t, h, "snapshot_task/3"); s3.Health != "attention" || s3.Attrs["retention"] != "1 month" {
		t.Errorf("snapshot task 3 = %+v", s3)
	}
	r1 := res(t, h, "replication/1")
	if r1.Kind != "backup_target" || !strings.Contains(fmt.Sprint(r1.Attrs["caps"]), "backups.targets") || r1.Attrs["backup.last_status"] != "ok" ||
		r1.Attrs["backup.last_ok"] != "2026-10-03T02:00:00Z" || r1.Attrs["backup.schedule"] != "0 0 * * *" || r1.Attrs["remote"] != "backup.lab.example" {
		t.Errorf("replication 1 = %+v", r1)
	}
	covers, _ := json.Marshal(r1.Attrs["backup.covers"])
	for _, want := range []string{`{"resource":"truenas:i_test/dataset/tank/media"}`, `{"resource":"truenas:i_test/dataset/tank/media/movies"}`,
		`{"resource":"truenas:i_test/dataset/tank/media/tv"}`, `{"host":"127.0.0.1","path":"/mnt/tank/media"}`} {
		if !strings.Contains(string(covers), want) {
			t.Errorf("replication 1 covers %s, missing %s", covers, want)
		}
	}
	r2 := res(t, h, "replication/2")
	if r2.Health != "attention" || r2.Attrs["backup.last_status"] != "failed" || r2.Attrs["backup.last_error"] != "cannot receive incremental stream: out of space." {
		t.Errorf("replication 2 = %+v", r2)
	}
	if _, ok := r2.Attrs["backup.last_ok"]; ok {
		t.Errorf("replication 2 has a last_ok it never had: %v", r2.Attrs["backup.last_ok"])
	}
	if r3 := res(t, h, "replication/3"); r3.Attrs["backup.covers"] != nil || r3.Attrs["direction"] != "pull" {
		t.Errorf("a pull covers nothing here: %+v", r3)
	}
	if r4 := res(t, h, "replication/4"); r4.State != "disabled" || r4.Attrs["enabled"] != false {
		t.Errorf("replication 4 = %+v", r4)
	}

	// Relations.
	rels := h.Graph().Relations
	for _, k := range []string{
		"truenas:i_test/pool/tank part_of truenas:i_test/host",
		"truenas:i_test/dataset/tank/media part_of truenas:i_test/pool/tank",
		"truenas:i_test/disk/FAKEHDD00004 part_of truenas:i_test/pool/tank",
		"truenas:i_test/disk/FAKESSD000001 part_of truenas:i_test/host",
		"truenas:i_test/app/jellyfin runs_on truenas:i_test/host",
		"truenas:i_test/snapshot_task/1 part_of truenas:i_test/dataset/tank/media",
		"integration:i_test monitors truenas:i_test/host",
	} {
		if rels[k] == nil {
			t.Errorf("no relation %s", k)
		}
	}
}

func TestIncidents(t *testing.T) {
	srv, h, in := connect(t)
	syncOK(t, in, integrationtest.Watched)

	pool, ok := h.Incident("pool.tank.health")
	if !ok || pool.Severity != "attention" || pool.Class != "problem" || pool.Target != "truenas:i_test/pool/tank" || pool.Scenario != "truenas-pool-degraded" {
		t.Fatalf("pool incident = %+v", pool)
	}
	if len(pool.Causes) != 1 || pool.Causes[0].Resource != "truenas:i_test/disk/FAKEHDD00004" || !strings.Contains(pool.Causes[0].Text, "41 checksum errors") {
		t.Errorf("pool causes = %+v", pool.Causes)
	}
	repl, ok := h.Incident("replication.2.failed")
	if !ok || repl.Severity != "attention" || repl.Target != "truenas:i_test/replication/2" || !strings.Contains(repl.Summary, "out of space") {
		t.Fatalf("replication incident = %+v", repl)
	}
	if len(repl.BlastRadius) != 1 || repl.BlastRadius[0].Resource != "truenas:i_test/dataset/fast/apps" {
		t.Errorf("blast radius = %+v", repl.BlastRadius)
	}
	if _, ok := h.Incident("snapshot.3.failed"); !ok {
		t.Error("no incident for the failed snapshot task")
	}
	if cert, ok := h.Incident("alert.00000000-0000-4000-8000-000000000004"); !ok || !strings.Contains(cert.Title, "truenas_default") {
		t.Errorf("certificate alert = %+v, %v", cert, ok)
	}
	// TrueNAS's own alerts for what the integration already raises, warnings
	// and dismissed alerts aren't incidents.
	for _, n := range []string{"01", "02", "03", "05", "06"} {
		if _, ok := h.Incident("alert.00000000-0000-4000-8000-0000000000" + n); ok {
			t.Errorf("alert %s became an incident", n)
		}
	}
	if _, ok := h.Incident("replication.1.failed"); ok {
		t.Error("a finished replication raised an incident")
	}

	// The pool is repaired and the replication runs again: both resolve.
	var pools []map[string]any
	b, _ := os.ReadFile(filepath.Join(fixtures, "nas", "pool.query.json"))
	_ = json.Unmarshal(b, &pools)
	pools[0]["status"], pools[0]["healthy"], pools[0]["status_detail"] = "ONLINE", true, nil
	topo := pools[0]["topology"].(map[string]any)["data"].([]any)[0].(map[string]any)
	topo["status"] = "ONLINE"
	for _, c := range topo["children"].([]any) {
		d := c.(map[string]any)
		d["status"] = "ONLINE"
		d["stats"].(map[string]any)["read_errors"], d["stats"].(map[string]any)["checksum_errors"] = 0, 0
	}
	srv.set("pool.query", rpcReply{result: pools})
	var repls []map[string]any
	b, _ = os.ReadFile(filepath.Join(fixtures, "nas", "replication.query.json"))
	_ = json.Unmarshal(b, &repls)
	repls[1]["state"] = map[string]any{"state": "FINISHED", "datetime": map[string]any{"$date": 1791037800000}}
	srv.set("replication.query", rpcReply{result: repls})
	h.Advance(30 * time.Minute)
	syncOK(t, in, integrationtest.Watched)
	for _, id := range []string{"pool.tank.health", "replication.2.failed"} {
		if inc, ok := h.Incident(id); !ok || inc.Status != "resolved" {
			t.Errorf("%s = %+v", id, inc)
		}
	}
	if r2 := res(t, h, "replication/2"); r2.Health != "ok" || r2.Attrs["backup.last_ok"] != "2026-10-03T14:30:00Z" {
		t.Errorf("replication 2 after = %+v", r2)
	}
	if tank := res(t, h, "pool/tank"); tank.Health != "ok" || tank.State != "online" {
		t.Errorf("tank after = %+v", tank)
	}
}

func TestFaultedPoolIsDown(t *testing.T) {
	srv, h, in := connect(t)
	var pools []map[string]any
	b, _ := os.ReadFile(filepath.Join(fixtures, "nas", "pool.query.json"))
	_ = json.Unmarshal(b, &pools)
	pools[1]["status"], pools[1]["healthy"] = "FAULTED", false
	srv.set("pool.query", rpcReply{result: pools})
	syncOK(t, in, integrationtest.Watched)
	if inc, ok := h.Incident("pool.fast.health"); !ok || inc.Severity != "down" {
		t.Fatalf("faulted pool incident = %+v", inc)
	}
	if r := res(t, h, "pool/fast"); r.Health != "down" {
		t.Errorf("fast = %+v", r)
	}
}

// TestBackupCoverage runs the correlator and the coverage map over what the
// integration published: a push replication covers its source datasets.
func TestBackupCoverage(t *testing.T) {
	_, h, in := connect(t)
	syncOK(t, in, integrationtest.Watched)
	doc := h.Graph()
	result := correlate.Correlate(doc, nil)
	have := map[string]bool{}
	for _, l := range result.Links {
		if l.Type == correlate.BackedUpBy {
			have[string(l.From)+" → "+string(l.To)] = true
			doc.Relations[l.Key()] = &graph.Relation{From: l.From, Type: l.Type, To: l.To, Source: correlate.Source}
		}
	}
	for _, want := range []string{
		"truenas:i_test/dataset/tank/media → truenas:i_test/replication/1",
		"truenas:i_test/dataset/tank/media/movies → truenas:i_test/replication/1",
		"truenas:i_test/dataset/fast/apps → truenas:i_test/replication/2",
	} {
		if !have[want] {
			t.Errorf("no link %s; have %v", want, have)
		}
	}
	if have["truenas:i_test/dataset/tank/backups → truenas:i_test/replication/3"] {
		t.Error("a pull covers its local target")
	}
	cov := backups.Compute(doc, nil, h.Now())
	if it := cov["truenas:i_test/dataset/tank/media/movies"]; it.State != backups.Fresh {
		t.Errorf("tank/media/movies coverage = %+v", it)
	}
	if it := cov["truenas:i_test/dataset/fast/apps"]; it.State == backups.Fresh || it.State == backups.Uncovered || len(it.By) != 1 {
		t.Errorf("fast/apps, covered by a failed task without a good run, = %+v", it)
	}
	if it := cov["truenas:i_test/dataset/tank/backups"]; it.State != backups.Uncovered {
		t.Errorf("tank/backups = %+v", it)
	}
}

func TestAuthFailureIsARepair(t *testing.T) {
	srv := newFake(t)
	h := integrationtest.New(t)
	wrong := "7-FAKEwrongKEY0000000000000000000000000000000000000000000000000000"
	in := h.Connect(Integration{}, map[string]any{"url": srv.URL, "tls": map[string]any{"mode": "tofu"}}, map[string]string{"api_key": wrong}).(*instance)
	err := in.Sync(t.Context(), integrationtest.Watched)
	code, class, msg := integration.Classify(err)
	if code != integration.CodeAuth || class != integration.NeedsRepair || integration.FieldOf(err) != "api_key" {
		t.Fatalf("classify = %s %v %q (%v)", code, class, msg, err)
	}
	if r := in.Test(t.Context()); r.OK || r.ErrorCode != integration.CodeAuth {
		t.Fatalf("test = %+v", r)
	}
	for _, s := range []string{err.Error(), msg, h.Logs()} {
		if strings.Contains(s, wrong) || strings.Contains(s, "FAKEwrong") {
			t.Fatalf("the key leaked: %q", s)
		}
	}
}

func TestNoAPIEndpointIsUnsupported(t *testing.T) {
	srv, _, in := connect(t)
	srv.mu.Lock()
	srv.status = http.StatusNotFound
	srv.mu.Unlock()
	err := in.Sync(t.Context(), integrationtest.Watched)
	if code, _, _ := integration.Classify(err); code != integration.CodeUnsupportedVersion {
		t.Fatalf("classify = %s (%v)", code, err)
	}
}

func TestOldVersionIsUnsupported(t *testing.T) {
	srv, _, in := connect(t)
	srv.set("system.info", rpcReply{result: map[string]any{"version": "24.10.2", "hostname": "nas01"}})
	err := in.Sync(t.Context(), integrationtest.Watched)
	if code, _, msg := integration.Classify(err); code != integration.CodeUnsupportedVersion || !strings.Contains(msg, "24.10.2") {
		t.Fatalf("classify = %s %q", code, msg)
	}
}

// TestSessionReused: one login across syncs, and a dropped session is
// dialled again within the sync.
func TestSessionReused(t *testing.T) {
	srv, _, in := connect(t)
	syncOK(t, in, integrationtest.Watched)
	syncOK(t, in, integration.Interest{})
	srv.mu.Lock()
	logins := srv.logins
	srv.mu.Unlock()
	if logins != 1 {
		t.Fatalf("logins = %d, want 1", logins)
	}
	srv.drop()
	syncOK(t, in, integrationtest.Watched)
	srv.mu.Lock()
	logins = srv.logins
	srv.mu.Unlock()
	if logins != 2 {
		t.Fatalf("logins after a drop = %d, want 2", logins)
	}
}

// TestIdleSkipsTemperatures: nobody watching, no smartctl per disk.
func TestIdleSkipsTemperatures(t *testing.T) {
	srv, _, in := connect(t)
	syncOK(t, in, integration.Interest{})
	if slices.Contains(srv.took(), "disk.temperatures") {
		t.Error("read temperatures while nobody watched")
	}
}

func TestSoftParts(t *testing.T) {
	srv, h, in := connect(t)
	denied := map[string]any{"code": -32001, "message": "Not authorized", "data": map[string]any{"error": 13, "errname": "EACCES", "reason": "Not authorized"}}
	srv.set("app.query", rpcReply{err: denied})
	srv.set("alert.list", rpcReply{err: map[string]any{"code": -32001, "message": "boom", "data": map[string]any{"error": 22, "errname": "EINVAL", "reason": "something broke"}}})
	srv.set("disk.temperatures", rpcReply{err: map[string]any{"code": -32601, "message": "Method not found"}})
	syncOK(t, in, integrationtest.Watched)
	if _, ok := h.Resource("truenas:i_test/app/jellyfin"); ok {
		t.Error("apps published though denied")
	}
	codes := map[string]bool{}
	for _, w := range in.warnings() {
		codes[w.Code] = true
	}
	if !codes["apps_hidden"] || !codes["partial_read"] {
		t.Errorf("warnings = %v", codes)
	}
	if _, ok := res(t, h, "disk/FAKEHDD00001").Attrs["smart"]; ok {
		t.Error("SMART said ok without having read the alerts")
	}

	// The pools themselves denied: the key's user lacks the privilege.
	srv.set("pool.query", rpcReply{err: denied})
	err := in.Sync(t.Context(), integrationtest.Watched)
	if code, _, _ := integration.Classify(err); code != integration.CodeAuth || integration.FieldOf(err) != "api_key" {
		t.Fatalf("denied pool.query = %s (%v)", code, err)
	}
}

// TestKeyNeverLeaks: the API key is in no log line, error, warning, state
// or published fact.
func TestKeyNeverLeaks(t *testing.T) {
	srv, h, in := connect(t)
	syncOK(t, in, integrationtest.Watched)
	_ = in.Test(t.Context())
	srv.set("pool.query", rpcReply{err: map[string]any{"code": -32001, "message": "echo " + fakeKey, "data": map[string]any{"error": 22, "errname": "EINVAL", "reason": "echo " + fakeKey}}})
	err := in.Sync(t.Context(), integrationtest.Watched)
	if err == nil {
		t.Fatal("sync succeeded")
	}
	var state []string
	for _, k := range []string{stateKey} {
		b, _ := h.State.Load(t.Context(), k)
		state = append(state, string(b))
	}
	if len(state[0]) == 0 {
		t.Error("the replication state wasn't saved")
	}
	graphJSON, _ := json.Marshal(h.Graph())
	warnJSON, _ := json.Marshal(in.warnings())
	stored, _ := json.Marshal(h.StoredConfig())
	secret := strings.TrimPrefix(fakeKey, "7-")
	for name, s := range map[string]string{"error": err.Error(), "logs": h.Logs(), "state": strings.Join(state, " "),
		"graph": string(graphJSON), "warnings": string(warnJSON), "config": string(stored)} {
		if strings.Contains(s, secret) {
			t.Errorf("the key leaked into the %s", name)
		}
	}
	if !strings.Contains(err.Error(), "[redacted]") {
		t.Errorf("error = %v", err)
	}
}

// TestLastOKSurvivesRestart: zettarepl keeps only the newest run, so the
// last good one comes from the instance's state after a failure.
func TestLastOKSurvivesRestart(t *testing.T) {
	srv, h, in := connect(t)
	syncOK(t, in, integrationtest.Watched)
	var repls []map[string]any
	b, _ := os.ReadFile(filepath.Join(fixtures, "nas", "replication.query.json"))
	_ = json.Unmarshal(b, &repls)
	repls[0]["state"] = map[string]any{"state": "ERROR", "datetime": map[string]any{"$date": 1791036000000}, "error": "connection refused."}
	srv.set("replication.query", rpcReply{result: repls})
	in2 := h.Connect(Integration{}, map[string]any{"url": srv.URL, "tls": map[string]any{"mode": "tofu"}}, map[string]string{"api_key": fakeKey}).(*instance)
	syncOK(t, in2, integrationtest.Watched)
	r1 := res(t, h, "replication/1")
	if r1.Attrs["backup.last_ok"] != "2026-10-03T02:00:00Z" || r1.Attrs["backup.last_status"] != "failed" {
		t.Errorf("replication 1 after a restart and a failure = %v", r1.Attrs)
	}
	inc, ok := h.Incident("replication.1.failed")
	if !ok || inc.Facts["last_ok"] != "2026-10-03T02:00:00Z" {
		t.Errorf("incident = %+v", inc)
	}
}

func TestTimeShapes(t *testing.T) {
	for in, want := range map[string]string{
		`{"$date": 1790992800000}`: "2026-10-03T02:00:00Z",
		`1790992800000`:            "2026-10-03T02:00:00Z",
		`"2026-10-03T02:00:00Z"`:   "2026-10-03T02:00:00Z",
	} {
		var v tnTime
		if err := json.Unmarshal([]byte(in), &v); err != nil || v.UTC().Format(time.RFC3339) != want {
			t.Errorf("%s → %v (%v)", in, v.Time, err)
		}
	}
	var null tnTime
	if err := json.Unmarshal([]byte(`null`), &null); err != nil || !null.IsZero() {
		t.Errorf("null → %v", null.Time)
	}
	var s strSet
	_ = json.Unmarshal([]byte(`{"$set":["A","B"]}`), &s)
	if fmt.Sprint(s) != "[A B]" {
		t.Errorf("$set → %v", s)
	}
}

func reason(r graph.Resource) string {
	s, _ := r.Attrs["health.reason"].(string)
	return s
}
