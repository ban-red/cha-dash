package proxmox

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
	fixtures    = "../../testdata/integrations/proxmox"
	fixtureTok  = "api@pam!chadash" // the token ID the fixtures were captured with
	fakeSecret  = "00000000-fake-4000-8000-proxmoxtest0"
	capturedAt  = "2026-10-04T01:30:00Z"
	unsupported = `{"data":{"release":"7.4","version":"7.4-17","repoid":"0000000000000000"}}`
)

// replay serves a captured PVE instance: every request in its _index.json,
// by path and query, with the captured status. Anything else is a 501 (PVE's
// answer to an unknown path) and is recorded, so a test can insist the
// integration only asks for what the spike captured.
type replay struct {
	*httptest.Server
	dir string

	mu       sync.Mutex
	entries  []indexEntry
	override map[string]reply // path (no query) → reply
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

func newReplay(t *testing.T, inst string) *replay {
	t.Helper()
	r := &replay{dir: filepath.Join(fixtures, inst), override: map[string]reply{}}
	b, err := os.ReadFile(filepath.Join(r.dir, "_index.json"))
	if err != nil {
		t.Fatal(err)
	}
	var idx struct {
		Requests []indexEntry `json:"requests"`
	}
	if err := json.Unmarshal(b, &idx); err != nil {
		t.Fatal(err)
	}
	r.entries = idx.Requests
	r.Server = httptest.NewTLSServer(http.HandlerFunc(r.serve))
	t.Cleanup(r.Close)
	return r
}

func (r *replay) serve(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if req.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if req.Header.Get("Authorization") != "PVEAPIToken="+fixtureTok+"="+fakeSecret {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"data":null}`)
		return
	}
	path := strings.TrimPrefix(req.URL.Path, "/api2/json")
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
		if ep != path || !reflect.DeepEqual(want, req.URL.Query()) && !(len(want) == 0 && len(req.URL.Query()) == 0) {
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
	w.WriteHeader(http.StatusNotImplemented)
	fmt.Fprint(w, `{"data":null}`)
}

func (r *replay) set(path string, status int, body string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.override[path] = reply{status, body}
}

func (r *replay) unset(path string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.override, path)
}

// take returns and clears the paths requested so far.
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

// connect runs a fixture instance through the harness the way the importer
// configures it (TOFU TLS), with the clock at the capture.
func connect(t *testing.T, inst string) (*replay, *integrationtest.Harness, *instance) {
	t.Helper()
	srv := newReplay(t, inst)
	h := integrationtest.New(t)
	at, _ := time.Parse(time.RFC3339, capturedAt)
	h.Advance(at.Sub(h.Now()))
	got := h.Connect(Integration{}, map[string]any{"url": srv.URL + "/", "tls": map[string]any{"mode": "tofu"}, "token_id": fixtureTok},
		map[string]string{"token_secret": fakeSecret})
	return srv, h, got.(*instance)
}

func syncOK(t *testing.T, in *instance, want integration.Interest) {
	t.Helper()
	if err := in.Sync(t.Context(), want); err != nil {
		t.Fatalf("sync: %v", err)
	}
}

func res(t *testing.T, h *integrationtest.Harness, local string) graph.Resource {
	t.Helper()
	r, ok := h.Resource("proxmox:i_test/" + local)
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

func attr(r graph.Resource, k string) any { return r.Attrs[k] }

func value(t *testing.T, h *integrationtest.Harness, local, key string) float64 {
	t.Helper()
	s := h.Series("proxmox:i_test/"+local, key)
	if s == nil {
		t.Fatalf("no series %s#%s", local, key)
	}
	v, ok := s["value"].(float64)
	if !ok {
		return math.NaN()
	}
	return v
}

func hasRel(h *integrationtest.Harness, from, typ, to string) bool {
	for _, r := range h.Graph().Relations {
		if string(r.From) == from && string(r.Type) == typ && string(r.To) == to {
			return true
		}
	}
	return false
}

func pid(local string) string { return "proxmox:i_test/" + local }

func TestRegistered(t *testing.T) {
	_, m, ok := integration.Default.Get(ID)
	if !ok || m.Name != "Proxmox VE" || m.Poll.Interval != liveInterval || m.Poll.Idle != idleInterval {
		t.Fatalf("manifest = %+v", m)
	}
	if got := m.SecretFields(); !slices.Equal(got, []string{"token_secret"}) {
		t.Fatalf("secret fields = %v", got)
	}
}

// TestValidateImporterShape: the configs the Homepage importer writes for
// the dogfood home (testdata/importers/homepage/dogfood/expected-plan.json)
// validate, with the secret reference resolved.
func TestValidateImporterShape(t *testing.T) {
	b, err := os.ReadFile("../../testdata/importers/homepage/dogfood/expected-plan.json")
	if err != nil {
		t.Fatal(err)
	}
	var plan struct {
		Plan struct {
			Instances []struct {
				Integration string         `json:"integration"`
				Config      map[string]any `json:"config"`
			} `json:"instances"`
		} `json:"plan"`
	}
	if err := json.Unmarshal(b, &plan); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, inst := range plan.Plan.Instances {
		if inst.Integration != ID {
			continue
		}
		n++
		cfg := map[string]any{}
		for k, v := range inst.Config {
			if k != "token_secret" {
				cfg[k] = v
			}
		}
		if errs := integrationtest.Validate(Integration{}, cfg, map[string]string{"token_secret": fakeSecret}); len(errs) > 0 {
			t.Errorf("%v: %v", inst.Config["url"], errs)
		}
	}
	if n != 5 {
		t.Fatalf("checked %d proxmox instances, want the dogfood's 5", n)
	}
	base := map[string]any{"url": "https://pve.lab.example:8006", "token_id": "chadash@pve!dash"}
	for name, c := range map[string]struct {
		mut  func(map[string]any)
		path string
	}{
		"token id without a token name": {func(m map[string]any) { m["token_id"] = "chadash@pve" }, "/token_id"},
		"token id without a realm":      {func(m map[string]any) { m["token_id"] = "chadash!dash" }, "/token_id"},
		"no url":                        {func(m map[string]any) { delete(m, "url") }, ""},
		"ftp url":                       {func(m map[string]any) { m["url"] = "ftp://pve.lab.example" }, "/url"},
		"unknown key":                   {func(m map[string]any) { m["node"] = "pluto" }, "/node"},
	} {
		cfg := map[string]any{}
		for k, v := range base {
			cfg[k] = v
		}
		c.mut(cfg)
		errs := integrationtest.Validate(Integration{}, cfg, map[string]string{"token_secret": fakeSecret})
		if len(errs) == 0 || (c.path != "" && errs[0].Path != c.path) {
			t.Errorf("%s: %v, want an error at %q", name, errs, c.path)
		}
	}
	if errs := integrationtest.Validate(Integration{}, base, nil); len(errs) == 0 {
		t.Error("a config without the token secret validated")
	}
}

func TestTestReadsGrants(t *testing.T) {
	for inst, want := range map[string]struct {
		version string
		privs   int
	}{
		"pluto": {"9.0.9", 7}, "minig5": {"8.4.19", 6}, "mars": {"9.1.5", 7}, "io": {"9.2.20", 7}, "jupiter-nas": {"8.4.21", 6},
	} {
		t.Run(inst, func(t *testing.T) {
			srv, _, in := connect(t, inst)
			res := in.Test(t.Context())
			if !res.OK || res.Version != want.version {
				t.Fatalf("test = %+v", res)
			}
			if len(res.Grants) != want.privs {
				t.Fatalf("grants = %+v, want PVEAuditor's %d", res.Grants, want.privs)
			}
			for _, g := range res.Grants {
				if g.CanWrite() || !strings.HasSuffix(g.Name, ".Audit") || !strings.Contains(g.Detail, "/vms") {
					t.Errorf("grant %+v", g)
				}
			}
			codes := warningCodes(res.Warnings)
			if !slices.Contains(codes, "updates_hidden") || !slices.Contains(codes, "backups_from_tasks") || slices.Contains(codes, "privsep_off") {
				t.Errorf("warnings = %v", codes)
			}
			if u := srv.unknownPaths(); len(u) > 0 {
				t.Errorf("asked for uncaptured paths: %v", u)
			}
		})
	}
}

func warningCodes(ws []integration.Warning) []string {
	var out []string
	for _, w := range ws {
		out = append(out, w.Code)
	}
	return out
}

func TestWriteGrant(t *testing.T) {
	srv, _, in := connect(t, "pluto")
	srv.set("/access/permissions", 200, `{"data":{"/":{"Sys.Audit":1,"VM.Audit":1,"Datastore.Audit":1},"/vms":{"VM.Audit":1,"VM.PowerMgmt":1}}}`)
	res := in.Test(t.Context())
	var power integration.Grant
	for _, g := range res.Grants {
		if g.Name == "VM.PowerMgmt" {
			power = g
		}
	}
	if !res.OK || !power.Write || power.Access != integration.AccessWrite || power.Detail != "on /vms" {
		t.Fatalf("grants = %+v", res.Grants)
	}
	// The hourly privilege check warns at sync time too.
	syncOK(t, in, integrationtest.Watched)
	if !slices.Contains(warningCodes(in.warnings()), "write_grant") {
		t.Fatalf("warnings = %v", in.warnings())
	}
}

func TestAuthRejected(t *testing.T) {
	srv := newReplay(t, "pluto")
	h := integrationtest.New(t)
	in := h.Connect(Integration{}, map[string]any{"url": srv.URL, "tls": map[string]any{"mode": "tofu"}, "token_id": fixtureTok},
		map[string]string{"token_secret": "wrong"})
	err := in.Sync(t.Context(), integrationtest.Watched)
	if code, class, _ := integration.Classify(err); code != integration.CodeAuth || class != integration.NeedsRepair {
		t.Fatalf("sync error %v classified %s", err, code)
	}
	if res := in.Test(t.Context()); res.OK || res.ErrorCode != integration.CodeAuth {
		t.Fatalf("test = %+v", res)
	}
	if strings.Contains(err.Error(), "wrong") {
		t.Fatal("the secret leaked into the error")
	}
}

func TestUnsupportedVersion(t *testing.T) {
	srv, _, in := connect(t, "pluto")
	srv.set("/version", 200, unsupported)
	err := in.Sync(t.Context(), integrationtest.Watched)
	if code, class, msg := integration.Classify(err); code != integration.CodeUnsupportedVersion || class != integration.NeedsRepair || !strings.Contains(msg, "7.4-17") {
		t.Fatalf("sync error %v → %s", err, code)
	}
	if res := in.Test(t.Context()); res.OK || res.ErrorCode != integration.CodeUnsupportedVersion {
		t.Fatalf("test = %+v", res)
	}
}

// TestStandalone90: pluto, PVE 9.0, standalone. The router VM's agent gives
// its address; the nightly job to PBS failed because PBS wasn't reachable.
func TestStandalone90(t *testing.T) {
	srv, h, in := connect(t, "pluto")
	syncOK(t, in, integrationtest.Watched)
	if u := srv.unknownPaths(); len(u) > 0 {
		t.Fatalf("asked for uncaptured paths: %v", u)
	}
	if _, ok := h.Resource(pid("cluster")); ok {
		t.Fatal("a standalone node has no cluster resource")
	}
	host := res(t, h, "node/pluto")
	if host.Kind != "host" || host.State != "online" || attr(host, "platform") != "pve" || attr(host, "pve_version") != "9.0.9" || attr(host, "ip") != "10.0.11.180" {
		t.Fatalf("host = %+v", host)
	}
	if v := value(t, h, "node/pluto", "cpu.pct"); v != 7.5 {
		t.Errorf("cpu.pct = %v", v)
	}
	if v := value(t, h, "node/pluto", "mem.pct"); v != 81.1 { // (total − available) / total on 9.x
		t.Errorf("mem.pct = %v", v)
	}
	if h.Series(pid("node/pluto"), "temp.c") != nil {
		t.Error("PVE's API has no temperatures; temp.c must not be invented")
	}
	if !hasRel(h, "integration:i_test", "monitors", pid("node/pluto")) {
		t.Error("the instance doesn't monitor its host")
	}

	vm := res(t, h, "qemu/103")
	if vm.Kind != "vm" || vm.Name != "OPNsense" || vm.State != "running" || attr(vm, "ip") != "10.0.11.1" || attr(vm, "mac") != "BC:24:11:00:00:02" ||
		attr(vm, "desired") != "running" || attr(vm, "vmid") != float64(103) {
		t.Fatalf("vm = %+v", vm)
	}
	if !hasRel(h, pid("qemu/103"), "runs_on", pid("node/pluto")) || !hasRel(h, pid("qemu/103"), "stores_on", pid("node/pluto/storage/local-lvm")) {
		t.Error("the VM's relations are missing")
	}
	ct := res(t, h, "lxc/100")
	if ct.Kind != "lxc" || attr(ct, "ip") != "10.0.11.187" || h.Series(pid("lxc/100"), "disk.pct") == nil {
		t.Fatalf("lxc = %+v", ct)
	}

	pbs := res(t, h, "storage/pbs-unraid-pluto")
	if attr(pbs, "pbs.server") != "10.0.11.120" || attr(pbs, "shared") != true || pbs.Health != "attention" {
		t.Fatalf("pbs storage = %+v", pbs)
	}
	if v := value(t, h, "storage/pbs-unraid-pluto", "used.pct"); v != 91.9 {
		t.Errorf("used.pct = %v", v)
	}
	if !hasRel(h, pid("node/pluto/storage/local-lvm"), "part_of", pid("node/pluto")) {
		t.Error("local storage isn't part of its host")
	}

	job := res(t, h, "backup/backup-8ef1991a-dabd")
	if job.Kind != "backup_job" || job.Name != "daily 03:00" || attr(job, "last_run.status") != "failed" || attr(job, "last_run.failed") != float64(2) ||
		job.Health != "attention" || !strings.Contains(attr(job, "health.reason").(string), "No route to host") {
		t.Fatalf("job = %+v", job)
	}
	if attr(job, "last_ok") != "2026-10-02T09:00:59Z" {
		t.Errorf("last_ok = %v", attr(job, "last_ok"))
	}
	if !hasRel(h, pid("qemu/103"), "backed_up_by", pid("backup/backup-8ef1991a-dabd")) || !hasRel(h, pid("backup/backup-8ef1991a-dabd"), "stores_on", pid("storage/pbs-unraid-pluto")) {
		t.Error("backup relations are missing")
	}
	if v := value(t, h, "backup/backup-8ef1991a-dabd", "tasks.failed_24h"); v != 1 {
		t.Errorf("tasks.failed_24h = %v", v)
	}
	if attr(vm, "backup.last_status") != "failed" || attr(vm, "backup.last_ok") != "2026-10-02T09:00:59Z" || attr(vm, "backup.covered") != true {
		t.Errorf("vm backup attrs = %v", vm.Attrs)
	}
	inc, ok := h.Incident("backup.backup-8ef1991a-dabd")
	if !ok || inc.Scenario != "backup-target-unreachable" || inc.Facts["endpoint"] != "10.0.11.120:8007" || inc.Facts["reason"] != "No route to host" ||
		inc.Facts["failed_runs"] != float64(6) || inc.Facts["runs"] != float64(12) || len(inc.Facts["failed_at"].([]any)) != 6 {
		t.Fatalf("incident = %+v", inc)
	}
	if inc.Causes[0].Resource != pid("storage/pbs-unraid-pluto") || inc.OpenedAt.Format(time.RFC3339) != "2026-10-03T09:00:02Z" {
		t.Errorf("incident causes = %+v, opened %v", inc.Causes, inc.OpenedAt)
	}
	// The manual backup to usb at 12:52 isn't a run of the job.
	if attr(job, "last_run.start") != "2026-10-03T09:00:02Z" {
		t.Errorf("last_run.start = %v", attr(job, "last_run.start"))
	}
	if len(in.warnings()) != 0 {
		t.Errorf("warnings with a PVEAuditor token: %v", in.warnings())
	}
	if tls, _ := h.StoredConfig()["tls"].(map[string]any); tls == nil || tls["pin"] == nil {
		t.Error("the TOFU pin wasn't recorded")
	}
}

// TestStandalone84: minig5 on PVE 8.4. No memory.available, the agent isn't
// readable (not even asked), a weekly job, apt-get update failing.
func TestStandalone84(t *testing.T) {
	srv, h, in := connect(t, "minig5")
	syncOK(t, in, integrationtest.Watched)
	if u := srv.unknownPaths(); len(u) > 0 {
		t.Fatalf("asked for uncaptured paths: %v", u)
	}
	if in.ad.memAvailable || in.ad.agentIPs {
		t.Fatalf("8.4 adapter = %+v", in.ad)
	}
	if v := value(t, h, "node/minig5", "mem.pct"); v != 65.9 {
		t.Errorf("mem.pct = %v", v)
	}
	vm := res(t, h, "qemu/100")
	if vm.Name != "haos" || attr(vm, "ip") != nil || attr(vm, "mac") != "BC:24:11:00:00:10" {
		t.Errorf("vm = %+v", vm)
	}
	if attr(res(t, h, "lxc/200"), "ip") != "10.0.11.202" {
		t.Error("LXC addresses work on 8.4")
	}
	job := res(t, h, "backup/backup-d22893c3-d0bb")
	if job.Name != "weekly sun 05:00" || attr(job, "last_run.start") != "2026-09-27T11:00:07Z" || attr(job, "last_run.status") != "failed" {
		t.Fatalf("job = %+v", job)
	}
	if inc, ok := h.Incident("backup.backup-d22893c3-d0bb"); !ok || inc.Facts["endpoint"] != "jupiter.lab.example:8007" {
		t.Fatalf("incident = %+v", inc)
	}
	if inc, ok := h.Incident("hint.aptupdate.minig5"); !ok || inc.Severity != integration.SeverityInfo || inc.Facts["failures"] != float64(4) {
		t.Fatalf("apt hint = %+v", inc)
	}
	if inc, ok := h.Incident("hint.not-backed-up"); !ok || inc.Title != "ct-200 on minig5 isn't backed up" {
		t.Fatalf("coverage hint = %+v", inc)
	}
	if attr(res(t, h, "lxc/200"), "backup.covered") != false {
		t.Error("ct-200 is in no job")
	}
}

// TestCluster: mars, a 2-node PVE 9.1 cluster with an HA guest on Ceph.
func TestCluster(t *testing.T) {
	srv, h, in := connect(t, "mars")
	syncOK(t, in, integrationtest.Watched)
	if u := srv.unknownPaths(); len(u) > 0 {
		t.Fatalf("asked for uncaptured paths: %v", u)
	}
	c := res(t, h, "cluster")
	if c.Kind != "cluster" || c.Name != "mars" || attr(c, "nodes") != float64(2) || attr(c, "quorate") != true || c.Health != "ok" {
		t.Fatalf("cluster = %+v", c)
	}
	for _, n := range []string{"mars-1", "mars-2"} {
		if !hasRel(h, pid("node/"+n), "part_of", pid("cluster")) {
			t.Errorf("%s isn't part of the cluster", n)
		}
	}
	if !hasRel(h, pid("lxc/101"), "runs_on", pid("node/mars-1")) || !hasRel(h, pid("lxc/3696"), "runs_on", pid("node/mars-2")) {
		t.Error("guests don't run on their nodes")
	}
	ct := res(t, h, "lxc/101")
	if ct.Name != "pvedocker" || attr(ct, "hastate") != "started" || attr(ct, "ip") != "10.0.11.201" || attr(ct, "cluster") != "mars" {
		t.Fatalf("lxc = %+v", ct)
	}
	// Shared storage appears once per node in /cluster/resources but is one resource.
	vd := res(t, h, "storage/vm-disks")
	if attr(vd, "nodes") == nil || len(attr(vd, "nodes").([]any)) != 2 || !hasRel(h, pid("storage/vm-disks"), "part_of", pid("cluster")) {
		t.Fatalf("shared storage = %+v", vd)
	}
	if !hasRel(h, pid("lxc/101"), "stores_on", pid("storage/vm-disks")) {
		t.Error("the Ceph-backed guest doesn't store on vm-disks")
	}
	res(t, h, "node/mars-1/storage/local-lvm")
	res(t, h, "node/mars-2/storage/local-lvm")
	if inc, ok := h.Incident("backup.backup-b2d4cc5b-11c0"); !ok || inc.Facts["endpoint"] != "10.0.11.120:8007" || !strings.Contains(inc.Title, "mars") {
		t.Fatalf("incident = %+v", inc)
	}
	if inc, ok := h.Incident("hint.not-backed-up"); !ok || inc.Target != pid("cluster") {
		t.Fatalf("coverage hint = %+v", inc)
	}
}

// TestPassthroughConflict: io's nightly job fails every night, because
// backing up the stopped VM 100 has to start it and VM 200 holds the GPU.
func TestPassthroughConflict(t *testing.T) {
	srv, h, in := connect(t, "io")
	syncOK(t, in, integrationtest.Watched)
	if u := srv.unknownPaths(); len(u) > 0 {
		t.Fatalf("asked for uncaptured paths: %v", u)
	}
	job := res(t, h, "backup/backup-77532a36-6067")
	if attr(job, "last_run.ok") != float64(1) || attr(job, "last_run.failed") != float64(1) || attr(job, "last_ok") != nil {
		t.Fatalf("job = %+v", job)
	}
	inc, ok := h.Incident("backup.backup-77532a36-6067")
	if !ok || inc.Scenario != "backup-passthrough-conflict" || inc.Target != pid("qemu/100") || inc.Facts["in_use_by"] != float64(200) ||
		inc.Facts["pci_device"] != "0000:01:00.0" || inc.Facts["failed_runs"] != float64(10) {
		t.Fatalf("incident = %+v", inc)
	}
	if !strings.Contains(inc.Causes[0].Text, "iolinux") || inc.Causes[1].Resource != pid("qemu/200") {
		t.Errorf("causes = %+v", inc.Causes)
	}
	vm100, vm200 := res(t, h, "qemu/100"), res(t, h, "qemu/200")
	if attr(vm100, "backup.last_status") != "failed" || attr(vm100, "backup.last_ok") != nil || !strings.Contains(attr(vm100, "backup.last_error").(string), "PCI device") {
		t.Errorf("vm 100 = %v", vm100.Attrs)
	}
	if attr(vm200, "backup.last_status") != "ok" || attr(vm200, "backup.last_ok") == nil || attr(vm200, "ip") != "10.0.11.243" {
		t.Errorf("vm 200 = %v", vm200.Attrs)
	}
	if !slices.Equal(anys(attr(vm100, "hostpci")), []any{"0000:01:00", "0000:08:00"}) {
		t.Errorf("hostpci = %v", attr(vm100, "hostpci"))
	}
	// 9.x reports a VM's memory host-side, above maxmem; mem.pct stays ≤ 100.
	if v := value(t, h, "qemu/200", "mem.pct"); v != 100 {
		t.Errorf("iolinux mem.pct = %v", v)
	}
	if vm100.State != "stopped" || vm100.Health != "ok" || attr(vm100, "health.reason") != "stopped (expected)" || h.Series(pid("qemu/100"), "cpu.pct") != nil {
		t.Errorf("stopped vm = %+v", vm100)
	}
	if inc, ok := h.Incident("hint.backup-local.backup-77532a36-6067"); !ok || inc.Severity != integration.SeverityInfo {
		t.Fatalf("local-backup hint = %+v", inc)
	}
	if inc, ok := h.Incident("hint.not-backed-up"); !ok || !strings.Contains(inc.Title, "vm-101") {
		t.Fatalf("coverage hint = %+v", inc)
	}
	if v := value(t, h, "storage/jupiter-games", "used.pct"); v != 87.9 {
		t.Errorf("nfs used.pct = %v", v)
	}
	if attr(res(t, h, "storage/jupiter-games"), "nfs.export") != "/mnt/user/games" {
		t.Error("the NFS export is missing")
	}
}

// TestInternalError: jupiter-nas VM 101 is in internal-error, holding most
// of the host's memory; nothing on the host is backed up.
func TestInternalError(t *testing.T) {
	srv, h, in := connect(t, "jupiter-nas")
	syncOK(t, in, integrationtest.Watched)
	if u := srv.unknownPaths(); len(u) > 0 {
		t.Fatalf("asked for uncaptured paths: %v", u)
	}
	vm := res(t, h, "qemu/101")
	if vm.State != "error" || vm.Health != "down" || attr(vm, "pve_status") != "internal-error" {
		t.Fatalf("vm = %+v", vm)
	}
	inc, ok := h.Incident("guest.101")
	if !ok || inc.Severity != integration.SeverityDown || inc.Target != pid("qemu/101") || inc.Facts["mem_gb"] != 21.1 || len(inc.Causes) != 1 || !strings.Contains(inc.Causes[0].Text, "qmreset") {
		t.Fatalf("incident = %+v", inc)
	}
	host := res(t, h, "node/jupiter-nas")
	if host.Health != "attention" || attr(host, "health.reason") != "memory 95%" {
		t.Fatalf("host = %+v", host)
	}
	if inc, ok := h.Incident("hint.not-backed-up"); !ok || !strings.Contains(inc.Summary, "no backup job") || len(inc.BlastRadius) != 2 {
		t.Fatalf("coverage hint = %+v", inc)
	}
	ct := res(t, h, "lxc/100")
	if ct.State != "running" || ct.Health != "ok" || attr(ct, "onboot") != false || attr(ct, "desired") != nil {
		t.Errorf("running ct without onboot = %+v", ct)
	}
}

// TestPollPlan follows the spike's plan: live state every poll, the rest
// when due, addresses only while watched.
func TestPollPlan(t *testing.T) {
	srv, h, in := connect(t, "pluto")
	syncOK(t, in, integrationtest.Watched)
	first := srv.take()
	for _, want := range []string{"/version", "/access/permissions", "/cluster/resources", "/cluster/status", "/cluster/tasks", "/nodes/pluto/status",
		"/storage", "/nodes/pluto/qemu/103/config", "/nodes/pluto/lxc/100/interfaces", "/nodes/pluto/qemu/103/agent/network-get-interfaces",
		"/cluster/backup", "/cluster/backup-info/not-backed-up"} {
		if !slices.Contains(first, want) {
			t.Errorf("first sync didn't read %s", want)
		}
	}
	logs := 0
	for _, s := range first {
		if strings.HasSuffix(s, "/log") {
			logs++
		}
	}
	if logs != 1 {
		t.Errorf("read %d task logs, want the failed run's", logs)
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
	step(15*time.Second, integrationtest.Watched, "/cluster/resources")
	step(45*time.Second, integrationtest.Watched, "/cluster/resources", "/cluster/status", "/cluster/tasks")
	idle := integration.Interest{}
	step(5*time.Minute, idle, "/cluster/resources", "/cluster/status", "/cluster/tasks", "/nodes/pluto/status")
	// At 16 minutes the backup tier is due; the failed run's log is cached.
	step(10*time.Minute, idle, "/cluster/resources", "/cluster/status", "/cluster/tasks", "/nodes/pluto/status",
		"/cluster/backup", "/cluster/backup-info/not-backed-up")
	// At 31 minutes, the idle config pace: configs, but no addresses.
	step(15*time.Minute, idle, "/cluster/resources", "/cluster/status", "/cluster/tasks", "/nodes/pluto/status",
		"/cluster/backup", "/cluster/backup-info/not-backed-up", "/storage", "/nodes/pluto/lxc/100/config", "/nodes/pluto/qemu/103/config")
	// After an hour, the version and the permissions again.
	step(30*time.Minute, idle, "/version", "/access/permissions", "/cluster/resources", "/cluster/status", "/cluster/tasks", "/nodes/pluto/status",
		"/cluster/backup", "/cluster/backup-info/not-backed-up", "/storage", "/nodes/pluto/lxc/100/config", "/nodes/pluto/qemu/103/config")
}

// TestReadOnlyDegrades: a token that can't see backup jobs or tasks still
// syncs everything else, and says what it can't show.
func TestReadOnlyDegrades(t *testing.T) {
	srv, h, in := connect(t, "pluto")
	denied := `{"data":null}`
	srv.set("/cluster/backup", 403, denied)
	srv.set("/cluster/backup-info/not-backed-up", 403, denied)
	srv.set("/cluster/tasks", 403, denied)
	syncOK(t, in, integrationtest.Watched)
	codes := warningCodes(in.warnings())
	if !slices.Contains(codes, "backups_hidden") || !slices.Contains(codes, "tasks_hidden") {
		t.Fatalf("warnings = %v", codes)
	}
	res(t, h, "qemu/103")
	if _, ok := h.Resource(pid("backup/backup-8ef1991a-dabd")); ok {
		t.Fatal("a hidden job was published")
	}
	if _, ok := h.Resource(pid("qemu/103")); !ok || attr(res(t, h, "qemu/103"), "backup.covered") != nil {
		t.Error("coverage is unknown when jobs are hidden")
	}
	// Once the token may read them, the warnings go.
	for _, p := range []string{"/cluster/backup", "/cluster/backup-info/not-backed-up", "/cluster/tasks"} {
		srv.unset(p)
	}
	h.Advance(16 * time.Minute)
	syncOK(t, in, integrationtest.Watched)
	if len(in.warnings()) != 0 {
		t.Fatalf("warnings = %v", in.warnings())
	}
	res(t, h, "backup/backup-8ef1991a-dabd")
}

// TestBackupRecovers: the next scheduled run succeeds, so the job's
// incident resolves itself.
func TestBackupRecovers(t *testing.T) {
	srv, h, in := connect(t, "pluto")
	syncOK(t, in, integrationtest.Watched)
	if _, ok := h.Incident("backup.backup-8ef1991a-dabd"); !ok {
		t.Fatal("no incident to resolve")
	}
	b, err := os.ReadFile(filepath.Join(fixtures, "pluto/cluster/tasks.json"))
	if err != nil {
		t.Fatal(err)
	}
	var tasks struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(b, &tasks); err != nil {
		t.Fatal(err)
	}
	next := map[string]any{"type": "vzdump", "id": "", "node": "pluto", "user": "root@pam", "status": "OK",
		"starttime": 1791104402, "endtime": 1791104470, "upid": "UPID:pluto:00400000:07000000:6AC21990:vzdump::root@pam:"}
	tasks.Data = append([]map[string]any{next}, tasks.Data...)
	nb, _ := json.Marshal(tasks)
	srv.set("/cluster/tasks", 200, string(nb))
	h.Advance(time.Unix(1791104470, 0).Add(5 * time.Minute).Sub(h.Now()))
	syncOK(t, in, integrationtest.Watched)
	inc, _ := h.Incident("backup.backup-8ef1991a-dabd")
	if inc.Status != integration.StatusResolved || inc.ResolvedBy != "auto" {
		t.Fatalf("incident = %+v", inc)
	}
	job := res(t, h, "backup/backup-8ef1991a-dabd")
	if attr(job, "last_run.status") != "ok" || job.Health != "ok" || attr(res(t, h, "qemu/103"), "backup.last_status") != "ok" {
		t.Fatalf("job = %+v", job)
	}
}

func anys(v any) []any { s, _ := v.([]any); return s }

// edit rewrites one captured response's data.
func edit(t *testing.T, srv *replay, path, file string, mutate func(data []map[string]any)) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(srv.dir, file))
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
	srv.set(path, 200, string(nb))
}

// TestNodeOffline: mars-2 drops out of the 2-node cluster, which loses
// quorum. The node is down, the cluster is down, and nothing is asked of
// the node that can't answer.
func TestNodeOffline(t *testing.T) {
	srv, h, in := connect(t, "mars")
	edit(t, srv, "/cluster/status", "cluster/status.json", func(d []map[string]any) {
		for _, e := range d {
			switch e["id"] {
			case "cluster":
				e["quorate"] = 0
			case "node/mars-2":
				e["online"] = 0
			}
		}
	})
	edit(t, srv, "/cluster/resources", "cluster/resources.json", func(d []map[string]any) {
		for _, e := range d {
			if e["id"] == "node/mars-2" {
				e["status"], e["uptime"], e["cpu"], e["mem"] = "offline", 0, 0, 0
			}
		}
	})
	syncOK(t, in, integrationtest.Watched)
	for _, s := range srv.take() {
		if strings.HasPrefix(s, "/nodes/mars-2/") {
			t.Errorf("asked the offline node: %s", s)
		}
	}
	n := res(t, h, "node/mars-2")
	if n.State != "offline" || n.Health != "down" || !math.IsNaN(value(t, h, "node/mars-2", "cpu.pct")) {
		t.Fatalf("node = %+v", n)
	}
	if inc, ok := h.Incident("node.mars-2"); !ok || inc.Severity != integration.SeverityDown {
		t.Fatalf("node incident = %+v", inc)
	}
	c := res(t, h, "cluster")
	if c.Health != "down" || attr(c, "health.reason") != "quorum lost" {
		t.Fatalf("cluster = %+v", c)
	}
	inc, ok := h.Incident("cluster.quorum")
	if !ok || !strings.Contains(inc.Summary, "1 of 2 nodes") {
		t.Fatalf("quorum incident = %+v", inc)
	}
	// The node comes back: both incidents resolve.
	srv.unset("/cluster/status")
	srv.unset("/cluster/resources")
	h.Advance(time.Minute)
	syncOK(t, in, integrationtest.Watched)
	for _, id := range []string{"node.mars-2", "cluster.quorum"} {
		if inc, _ := h.Incident(id); inc.Status != integration.StatusResolved {
			t.Errorf("%s = %+v", id, inc)
		}
	}
}

// TestNoSecretInRequests: the token travels only in the Authorization
// header, never in a path or query.
func TestNoSecretInRequests(t *testing.T) {
	srv, _, in := connect(t, "mars")
	syncOK(t, in, integrationtest.Watched)
	in.Test(t.Context())
	for _, s := range srv.take() {
		if strings.Contains(s, fakeSecret) {
			t.Fatalf("the secret is in %s", s)
		}
	}
}
