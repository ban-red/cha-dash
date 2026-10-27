package unraid

import (
	"encoding/json"
	"errors"
	"io"
	"maps"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/integration/integrationtest"
)

// The spike's capture of the dogfood server (Unraid 7.3.2, API 4.37.2).
const fixtures = "../../testdata/integrations/unraid/jupiter"

const apiKey = "fake-unraid-key"

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(fixtures, name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// server is a fake Unraid API that answers each operation with the
// captured response. The captures answer the spike's queries, which asked
// for more fields than the integration does; that's harmless.
type server struct {
	*httptest.Server
	t *testing.T

	mu       sync.Mutex
	api      string            // replaces the captured API version when set
	ops      []string          // operations received, in order
	override map[string][]byte // operation → body to answer instead
	// handlers answer an operation from its variables, before override
	// (actions_test.go: mutations, and reads that follow them).
	handlers map[string]func(vars map[string]any) []byte
}

var files = map[string]string{
	"ChadashVersion": "info.json",
	"ChadashInfo":    "info.json",
	"ChadashNetwork": "network.json",
	"ChadashMetrics": "metrics.json",
	"ChadashArray":   "array.json",
	"ChadashDocker":  "docker.json",
	"ChadashShares":  "shares.json",
	"ChadashVMs":     "vms.json",
	"ChadashMe":      "me.json",
}

// rootField is the first field a GraphQL document selects.
var rootField = regexp.MustCompile(`^\s*(?:query|mutation)\s+\w+\s*(?:\([^)]*\))?\s*\{\s*(\w+)`)

func newServer(t *testing.T) *server {
	s := &server{t: t, api: "4.37.5+0123abcd", override: map[string][]byte{}, handlers: map[string]func(map[string]any) []byte{}}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/graphql" || r.Header.Get("Content-Type") != "application/json" {
			http.NotFound(w, r)
			return
		}
		var req struct {
			Query         string         `json:"query"`
			OperationName string         `json:"operationName"`
			Variables     map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if m := rootField.FindStringSubmatch(req.Query); m == nil || m[1] == "disks" {
			t.Errorf("%s reads %v: never query disks", req.OperationName, m)
		}
		s.mu.Lock()
		s.ops = append(s.ops, req.OperationName)
		body, overridden := s.override[req.OperationName]
		handler := s.handlers[req.OperationName]
		api := s.api
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("x-api-key") != apiKey {
			io.WriteString(w, `{"data":null,"errors":[{"message":"API key validation failed","extensions":{"code":"UNAUTHENTICATED"}}]}`)
			return
		}
		if handler != nil && !overridden {
			body, overridden = handler(req.Variables), true
		}
		if !overridden {
			name, ok := files[req.OperationName]
			if !ok {
				t.Errorf("unexpected operation %q", req.OperationName)
				http.NotFound(w, r)
				return
			}
			body = fixture(t, name)
			if name == "info.json" && api != "" {
				body = []byte(strings.Replace(string(body), `"4.37.2+a00c99a5"`, `"`+api+`"`, 1))
			}
		}
		w.Write(body)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *server) took() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	ops := s.ops
	s.ops = nil
	return ops
}

func (s *server) setAPI(v string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.api = v
}

func (s *server) set(op string, body []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.override[op] = body
}

// forbidden is the captured FORBIDDEN answer (a read of vars), as if op had
// been refused.
func forbidden(t *testing.T) []byte { return fixture(t, "scope/Vars.json") }

func connect(t *testing.T, h *integrationtest.Harness, srv *server) *instance {
	t.Helper()
	// localhost rather than 127.0.0.1, so the LAN address comes from the
	// network interfaces read.
	url := strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)
	return h.Connect(Integration{}, map[string]any{"url": url, "tls": map[string]any{"mode": "verify"}},
		map[string]string{"api_key": apiKey}).(*instance)
}

func TestRegistered(t *testing.T) {
	_, m, ok := integration.Default.Get(ID)
	if !ok || m.Name != "Unraid" || !slices.Equal(m.SecretFields(), []string{"api_key"}) || m.Poll.Interval != time.Minute {
		t.Fatalf("manifest = %+v", m)
	}
}

func TestValidate(t *testing.T) {
	// The importer's shapes (testdata/importers/homepage): url, tls, and
	// the key moved to the vault.
	for _, cfg := range []map[string]any{
		{"url": "http://10.0.11.120", "tls": map[string]any{"mode": "verify"}},
		{"url": "https://jupiter.lab.example/", "tls": map[string]any{"mode": "tofu"}},
		{"url": "https://10.0.11.120/graphql"},
	} {
		if errs := integrationtest.Validate(Integration{}, cfg, map[string]string{"api_key": "k"}); len(errs) != 0 {
			t.Errorf("%v: %v", cfg, errs)
		}
	}
	for name, c := range map[string]struct {
		cfg     map[string]any
		secrets map[string]string
		path    string
	}{
		"no key":     {map[string]any{"url": "http://10.0.11.120"}, nil, "/api_key"},
		"no url":     {map[string]any{}, map[string]string{"api_key": "k"}, "/url"},
		"ftp":        {map[string]any{"url": "ftp://10.0.11.120"}, map[string]string{"api_key": "k"}, "/url"},
		"pin no pin": {map[string]any{"url": "https://a", "tls": map[string]any{"mode": "pin"}}, map[string]string{"api_key": "k"}, "/tls"},
		"unknown":    {map[string]any{"url": "https://a", "pool1": "cache"}, map[string]string{"api_key": "k"}, "/pool1"},
	} {
		errs := integrationtest.Validate(Integration{}, c.cfg, c.secrets)
		if len(errs) == 0 || errs[0].Path != c.path {
			t.Errorf("%s: %v, want an error at %s", name, errs, c.path)
		}
	}
}

func TestNoQueryReadsDisks(t *testing.T) {
	for _, q := range append(append([]string(nil), queries...), mutations...) {
		m := rootField.FindStringSubmatch(q)
		if m == nil || m[1] == "disks" {
			t.Errorf("root field of %q = %v", q, m)
		}
		// The disks resolver's fields; array devices have none of them.
		for _, f := range []string{"smartStatus", "serialNum", "temperature", "firmwareRevision"} {
			if strings.Contains(q, f) {
				t.Errorf("%q asks for %s", q, f)
			}
		}
	}
}

func TestSync(t *testing.T) {
	srv := newServer(t)
	h := integrationtest.New(t)
	in := connect(t, h, srv)
	if err := in.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	if ops := srv.took(); !slices.Equal(ops, []string{"ChadashInfo", "ChadashNetwork", "ChadashArray", "ChadashMetrics", "ChadashShares", "ChadashDocker", "ChadashVMs"}) {
		t.Fatalf("ops = %v", ops)
	}
	get := func(id string) map[string]any {
		t.Helper()
		r, ok := h.Resource("unraid:i_test/" + id)
		if !ok {
			t.Fatalf("no %s", id)
		}
		if r.Source != "integration:i_test" {
			t.Fatalf("%s source = %s", id, r.Source)
		}
		out := map[string]any{"kind": string(r.Kind), "name": r.Name, "state": r.State, "health": string(r.Health)}
		maps.Copy(out, r.Attrs)
		return out
	}
	value := func(id, key string) any {
		t.Helper()
		s := h.Series("unraid:i_test/"+id, key)
		if s == nil {
			t.Fatalf("no series %s#%s", id, key)
		}
		return s["value"]
	}
	want := func(what string, got map[string]any, kv map[string]any) {
		t.Helper()
		for k, v := range kv {
			if g, ok := got[k]; !ok || !equal(g, v) {
				t.Errorf("%s %s = %v, want %v", what, k, got[k], v)
			}
		}
	}

	host := get("host")
	want("host", host, map[string]any{"kind": "host", "name": "Jupiter", "state": "online", "health": "ok",
		"platform": "unraid", "version": "7.3.2", "api_version": "4.37.5", "ip": "10.0.11.120",
		"cores": 14, "threads": 20, "mem_gb": 62.6, "boot_at": "2026-09-05T22:06:33Z"})
	if r, _ := h.Resource("unraid:i_test/host"); r.Labels["chadash.role"] != "nas" {
		t.Errorf("host labels = %v", r.Labels)
	}
	if v := value("host", "cpu.pct"); v != 10.5 {
		t.Errorf("cpu.pct = %v", v)
	}
	if v := value("host", "mem.pct"); v != 63.6 {
		t.Errorf("mem.pct = %v", v)
	}

	want("array", get("array"), map[string]any{"kind": "array", "state": "online", "health": "ok", "size_tb": 92.0,
		"disks": 7, "parity_disks": 1, "parity": "valid", "parity_check": "completed", "parity_checked_at": "2026-09-08T22:31:42Z"})
	if v := value("array", "used.pct"); v != 88.2 {
		t.Errorf("array used.pct = %v", v)
	}

	want("disk1", get("disk/disk1"), map[string]any{"kind": "disk", "state": "online", "health": "ok", "role": "data",
		"serial": "3EMCY8MG", "model": "WDC WD160EDGZ-11B2DA0", "device": "sdg", "fs": "xfs", "size_tb": 16.0, "rotational": true})
	if v := value("disk/disk1", "temp.c"); v != 45.0 {
		t.Errorf("disk1 temp.c = %v", v)
	}
	if v := value("disk/disk1", "used.pct"); v != 88.7 {
		t.Errorf("disk1 used.pct = %v", v)
	}
	// Spun down: idle, and the temperature is unknown, not 0 or stale.
	want("disk3", get("disk/disk3"), map[string]any{"state": "idle", "health": "ok"})
	if v := value("disk/disk3", "temp.c"); v != nil {
		t.Errorf("a spun-down disk's temp.c = %v", v)
	}
	want("parity", get("disk/parity"), map[string]any{"role": "parity", "serial": "5KBNMTCF", "state": "idle"})
	if h.Series("unraid:i_test/disk/parity", "used.pct") != nil {
		t.Error("parity has no file system to fill")
	}

	// Five pools from seven devices.
	var pools []string
	for id, r := range h.Graph().Resources {
		if r.Kind == "pool" {
			pools = append(pools, string(id))
		}
	}
	slices.Sort(pools)
	if !slices.Equal(pools, []string{"unraid:i_test/pool/cache_appnvme", "unraid:i_test/pool/cache_array", "unraid:i_test/pool/cache_disks",
		"unraid:i_test/pool/cache_mx_beta", "unraid:i_test/pool/cache_nvme_beta"}) {
		t.Fatalf("pools = %v", pools)
	}
	want("pool", get("pool/cache_appnvme"), map[string]any{"state": "online", "health": "ok", "fs": "zfs", "devices": 2, "media": "nvme", "size_tb": 1.93})
	want("pool", get("pool/cache_array"), map[string]any{"devices": 2, "media": "ssd"})
	if v := value("pool/cache_appnvme", "temp.c"); v != 49.0 {
		t.Errorf("pool temp.c = %v, want its hottest device's", v)
	}
	if v := value("pool/cache_appnvme", "used.pct"); v != 34.2 {
		t.Errorf("pool used.pct = %v", v)
	}
	if v := value("pool/cache_disks", "temp.c"); v != nil {
		t.Errorf("a spun-down pool's temp.c = %v", v)
	}
	want("pool device", get("disk/cache_appnvme2"), map[string]any{"role": "pool", "pool": "cache_appnvme", "serial": "5291S317486A"})
	flash := get("disk/flash")
	want("flash", flash, map[string]any{"role": "boot", "fs": "vfat", "transport": "usb"})
	if _, ok := flash["serial"]; ok {
		t.Error("the flash's serial ties to the license and mustn't be published")
	}
	if v := value("disk/flash", "used.pct"); v != 10.6 {
		t.Errorf("flash used.pct = %v", v)
	}
	if h.Series("unraid:i_test/disk/flash", "temp.c") != nil {
		t.Error("the flash has no temperature")
	}

	// Shares: on the array when they name array disks.
	want("share", get("share/backup"), map[string]any{"kind": "share", "state": "online"})
	rels := h.Graph().Relations
	for _, r := range []string{
		"integration:i_test monitors unraid:i_test/host",
		"unraid:i_test/array part_of unraid:i_test/host",
		"unraid:i_test/disk/disk1 part_of unraid:i_test/array",
		"unraid:i_test/disk/parity part_of unraid:i_test/array",
		"unraid:i_test/pool/cache_appnvme part_of unraid:i_test/host",
		"unraid:i_test/disk/cache_appnvme2 part_of unraid:i_test/pool/cache_appnvme",
		"unraid:i_test/disk/flash part_of unraid:i_test/host",
		"unraid:i_test/share/backup part_of unraid:i_test/array",
		"unraid:i_test/share/media part_of unraid:i_test/array",
		"unraid:i_test/share/appdata part_of unraid:i_test/host",
		"unraid:i_test/container/Plex-Current runs_on unraid:i_test/host",
		"unraid:i_test/container/Plex-Current stores_on unraid:i_test/share/appdata",
	} {
		if rels[r] == nil {
			t.Errorf("missing relation %s", r)
		}
	}

	// Containers: autostart is the desired state.
	plex := get("container/Plex-Current")
	want("plex", plex, map[string]any{"kind": "container", "state": "running", "health": "ok", "autostart": true,
		"autostart_order": 25, "manager": "dockerman", "desired": "running"})
	if links, _ := plex["links"].([]any); len(links) != 1 {
		t.Errorf("plex links = %v", plex["links"])
	}
	var composeRunning, total int
	for _, r := range h.Graph().Resources {
		if r.Kind != "container" {
			continue
		}
		total++
		if r.Attrs["manager"] == "composeman" && r.State == "running" {
			composeRunning++
			if _, ok := r.Attrs["desired"]; ok {
				t.Errorf("%s: Compose Manager containers aren't on Unraid's list; desired is Docker's to say", r.ID)
			}
		}
	}
	if total != 74 || composeRunning != 5 {
		t.Fatalf("%d containers, %d running under Compose Manager", total, composeRunning)
	}
	// Labels carry secrets (homepage.widget.key); none reach the graph.
	for _, r := range h.Graph().Resources {
		if r.Kind == "container" && (len(r.Labels) > 0 || r.Attrs["labels"] != nil) {
			t.Fatalf("%s carries labels", r.ID)
		}
	}
}

// The containers join the Docker integration's by Docker's container ID.
func TestContainersJoinDockerByID(t *testing.T) {
	srv := newServer(t)
	h := integrationtest.New(t)
	if err := connect(t, h, srv).Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	var docker []struct {
		ID    string   `json:"Id"`
		Names []string `json:"Names"`
	}
	b, err := os.ReadFile("../../testdata/integrations/docker/jupiter/containers.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &docker); err != nil {
		t.Fatal(err)
	}
	byID := map[string]string{}
	for _, r := range h.Graph().Resources {
		if r.Kind == "container" {
			byID[r.Attrs["container_id"].(string)] = r.Name
		}
	}
	joined := 0
	for _, c := range docker {
		if name, ok := byID[c.ID]; ok {
			joined++
			if "/"+name != c.Names[0] {
				t.Errorf("%s: Unraid calls it %s, Docker %s", c.ID, name, c.Names[0])
			}
		}
	}
	if joined != len(docker) || joined != 74 {
		t.Fatalf("joined %d of %d", joined, len(docker))
	}
}

// TestHubOnUnraid: when the hub's own container ID is one Unraid lists,
// chadash:hub runs_on that container (best effort; one match only).
func TestHubOnUnraid(t *testing.T) {
	var docker []struct {
		ID    string   `json:"Id"`
		Names []string `json:"Names"`
	}
	b, err := os.ReadFile("../../testdata/integrations/docker/jupiter/containers.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &docker); err != nil {
		t.Fatal(err)
	}
	prev := integration.HubContainerID
	t.Cleanup(func() { integration.HubContainerID = prev })
	integration.HubContainerID = func() string { return docker[5].ID[:12] }

	srv := newServer(t)
	h := integrationtest.New(t)
	if _, err := h.Set.Graph.Update(func(tx *graph.Tx) error {
		return tx.Upsert(graph.Resource{ID: integration.HubResource, Kind: "app", Name: "Cha Dash"})
	}); err != nil {
		t.Fatal(err)
	}
	if err := connect(t, h, srv).Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	var on []string
	for _, r := range h.Graph().Relations {
		if string(r.From) == integration.HubResource && r.Type == "runs_on" {
			on = append(on, string(r.To))
		}
	}
	if len(on) != 1 || !strings.HasSuffix(on[0], "/container"+docker[5].Names[0]) {
		t.Fatalf("chadash:hub runs_on %v, want %s", on, docker[5].Names[0])
	}
}

func TestSyncSchedule(t *testing.T) {
	srv := newServer(t)
	h := integrationtest.New(t)
	in := connect(t, h, srv)
	sync := func(want integration.Interest) []string {
		t.Helper()
		if err := in.Sync(t.Context(), want); err != nil {
			t.Fatal(err)
		}
		return srv.took()
	}
	idle := integration.Interest{}
	sync(idle)
	h.Advance(5 * time.Minute)
	// Idle: array and metrics, plus the shares every five minutes.
	if ops := sync(idle); !slices.Equal(ops, []string{"ChadashArray", "ChadashMetrics", "ChadashShares"}) {
		t.Fatalf("idle = %v", ops)
	}
	h.Advance(time.Minute)
	// Watched: containers and VMs every sync.
	if ops := sync(integrationtest.Watched); !slices.Equal(ops, []string{"ChadashArray", "ChadashMetrics", "ChadashDocker", "ChadashVMs"}) {
		t.Fatalf("watched = %v", ops)
	}
	h.Advance(time.Minute)
	// Watching only hosts: no containers or VMs.
	if ops := sync(integration.Interest{Watched: true, Kinds: []string{"host"}}); !slices.Equal(ops, []string{"ChadashArray", "ChadashMetrics"}) {
		t.Fatalf("hosts only = %v", ops)
	}
	h.Advance(time.Hour)
	if ops := sync(idle); !slices.Equal(ops, []string{"ChadashInfo", "ChadashNetwork", "ChadashArray", "ChadashMetrics", "ChadashShares", "ChadashDocker", "ChadashVMs"}) {
		t.Fatalf("an hour later = %v", ops)
	}
}

func TestOldAPI(t *testing.T) {
	srv := newServer(t)
	srv.setAPI("") // the capture: API 4.37.2
	h := integrationtest.New(t)
	in := connect(t, h, srv)
	err := in.Sync(t.Context(), integrationtest.Watched)
	code, class, msg := integration.Classify(err)
	if code != integration.CodeUnsupportedVersion || class != integration.NeedsRepair ||
		!strings.Contains(msg, "version 4.37.2") || !strings.Contains(msg, "4.37.4 or later") || !strings.Contains(msg, "Update Unraid") {
		t.Fatalf("old API: %s %v %s", code, class, msg)
	}
	if _, ok := h.Resource("unraid:i_test/host"); ok {
		t.Fatal("nothing is read from an API with the authorization bug")
	}
	if ops := srv.took(); !slices.Equal(ops, []string{"ChadashInfo"}) {
		t.Fatalf("ops = %v", ops)
	}
	// The version is read again at the next retry, not cached for an hour,
	// so updating Unraid clears the Repair on its own.
	h.Advance(5 * time.Minute)
	srv.setAPI("4.37.4")
	if err := in.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatalf("after the update: %v", err)
	}

	srv.setAPI("")
	res := connect(t, integrationtest.New(t), srv).Test(t.Context())
	if res.OK || res.ErrorCode != integration.CodeUnsupportedVersion || res.Version != "7.3.2 (API 4.37.2)" || len(res.Grants) != 8 {
		t.Fatalf("test on the old API = %+v", res)
	}
}

func TestTestGrants(t *testing.T) {
	srv := newServer(t)
	h := integrationtest.New(t)
	res := connect(t, h, srv).Test(t.Context())
	// The consent key: exactly the eight read grants the recipe asks for.
	if !res.OK || res.Version != "7.3.2 (API 4.37.5)" || len(res.Grants) != 8 || len(res.Warnings) != 0 {
		t.Fatalf("consent key = %+v", res)
	}
	for _, g := range res.Grants {
		if g.CanWrite() || g.Access != integration.AccessRead || g.Detail != "read_any" {
			t.Errorf("grant %+v", g)
		}
	}
	if w := integration.WriteGrantWarning(res.Grants); w != nil {
		t.Fatalf("a read-only key got %v", w)
	}
	if _, ok := h.Resource("unraid:i_test/host"); ok {
		t.Fatal("Test must not emit facts")
	}

	// The spike's first key (capture.json's granted list): DISK:CREATE_ANY
	// instead of a read, plus eight extra reads. Built here in me's shape.
	var capture struct {
		Granted []string `json:"granted"`
	}
	if err := json.Unmarshal(fixture(t, "capture.json"), &capture); err != nil {
		t.Fatal(err)
	}
	type perm struct {
		Resource string   `json:"resource"`
		Actions  []string `json:"actions"`
	}
	var perms []perm
	for _, g := range capture.Granted {
		res, action, _ := strings.Cut(g, ":")
		perms = append(perms, perm{strings.ToUpper(res), []string{strings.ToUpper(action)}})
	}
	me := func(roles []string, perms []perm) []byte {
		b, _ := json.Marshal(map[string]any{"data": map[string]any{"me": map[string]any{"name": "dash", "roles": roles, "permissions": perms}}})
		return b
	}
	srv.set("ChadashMe", me([]string{}, perms))
	res = connect(t, h, srv).Test(t.Context())
	if !res.OK || len(res.Grants) != 16 {
		t.Fatalf("first key = %+v", res)
	}
	w := integration.WriteGrantWarning(res.Grants)
	if w == nil || !strings.Contains(w.Message, "(disk)") {
		t.Fatalf("the write grant isn't flagged: %v (%+v)", w, res.Grants)
	}

	// Roles: admin is a write grant and covers every read.
	srv.set("ChadashMe", me([]string{"ADMIN"}, nil))
	res = connect(t, h, srv).Test(t.Context())
	if !res.OK || len(res.Grants) != 1 || res.Grants[0].Access != integration.AccessAdmin || !res.Grants[0].Write {
		t.Fatalf("admin = %+v", res)
	}

	// No array read: the test fails; no Docker read: a warning.
	srv.set("ChadashMe", me(nil, []perm{{"INFO", []string{"READ_ANY"}}, {"SHARE", []string{"READ_ANY"}}}))
	res = connect(t, h, srv).Test(t.Context())
	if res.OK || res.ErrorCode != CodeMissingPermission || !strings.Contains(res.ErrorMessage, "ARRAY: Read") ||
		len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0].Message, "DOCKER: Read") {
		t.Fatalf("missing reads = %+v", res)
	}

	// A key without ME: the grants are unknown, which isn't a failure.
	srv.set("ChadashMe", forbidden(t))
	res = connect(t, h, srv).Test(t.Context())
	if !res.OK || len(res.Grants) != 0 || len(res.Warnings) != 1 || res.Warnings[0].Code != "grants_unknown" {
		t.Fatalf("no ME = %+v", res)
	}
}

func TestRejectedKey(t *testing.T) {
	srv := newServer(t)
	h := integrationtest.New(t)
	url := strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)
	in := h.Connect(Integration{}, map[string]any{"url": url}, map[string]string{"api_key": "wrong"})
	err := in.Sync(t.Context(), integrationtest.Watched)
	if code, class, _ := integration.Classify(err); code != integration.CodeAuth || class != integration.NeedsRepair || integration.FieldOf(err) != "api_key" {
		t.Fatalf("rejected key: %v (%s)", err, code)
	}
	if res := in.Test(t.Context()); res.OK || res.ErrorCode != integration.CodeAuth {
		t.Fatalf("test = %+v", res)
	}
}

func TestMissingPermissions(t *testing.T) {
	srv := newServer(t)
	h := integrationtest.New(t)
	in := connect(t, h, srv)
	// Docker refused: everything else still shows, with a warning.
	srv.set("ChadashDocker", forbidden(t))
	if err := in.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.Resource("unraid:i_test/array"); !ok {
		t.Fatal("no array")
	}
	if w, ok := in.warnings[partContainers]; !ok || w.Code != CodeMissingPermission || !strings.Contains(w.Message, "DOCKER: Read") {
		t.Fatalf("warnings = %v", in.warnings)
	}
	// Granted again: the warning goes.
	srv.set("ChadashDocker", fixture(t, "docker.json"))
	h.Advance(time.Minute)
	if err := in.Sync(t.Context(), integrationtest.Watched); err != nil || len(in.warnings) != 0 {
		t.Fatalf("%v, warnings = %v", err, in.warnings)
	}
	// The array refused: a Repair that says what to grant.
	srv.set("ChadashArray", forbidden(t))
	err := in.Sync(t.Context(), integrationtest.Watched)
	code, class, msg := integration.Classify(err)
	if code != CodeMissingPermission || class != integration.NeedsRepair || !strings.Contains(msg, "ARRAY: Read") || integration.FieldOf(err) != "api_key" {
		t.Fatalf("array refused: %s %v %s", code, class, msg)
	}
}

func TestNotUnraid(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	h := integrationtest.New(t)
	in := h.Connect(Integration{}, map[string]any{"url": srv.URL}, map[string]string{"api_key": apiKey})
	err := in.Sync(t.Context(), integrationtest.Watched)
	var re *integration.RepairError
	if !errors.As(err, &re) || re.Code != integration.CodeAPIChanged || re.Field != "url" || !strings.Contains(re.Message, "Unraid Connect plugin") {
		t.Fatalf("no API: %v", err)
	}
}

func TestVersions(t *testing.T) {
	for s, want := range map[string]bool{
		"4.37.2+a00c99a5": false, "4.37.3": false, "4.37.4-rc.1": false, "4.36.9": false, "3.99.99": false,
		"4.37.4": true, "4.37.5": true, "v4.38.0": true, "5.0.0+x": true,
	} {
		if got := checkVersion(s) == nil; got != want {
			t.Errorf("%s supported = %v", s, got)
		}
	}
	for _, s := range []string{"", "4.37", "four"} {
		if code, _, _ := integration.Classify(checkVersion(s)); code != integration.CodeUnsupportedVersion {
			t.Errorf("%q: %s", s, code)
		}
	}
}

func TestDiskIDs(t *testing.T) {
	for id, want := range map[string][2]string{
		"d7d4935f60aa13d7cf136dfc57d9e07c2ff1359da5bc1f93dc7bbc18511b76b8:WDC_WD181KFGX-68AFPN0_5KBNMTCF": {"WDC WD181KFGX-68AFPN0", "5KBNMTCF"},
		"d7d4935f60aa13d7cf136dfc57d9e07c2ff1359da5bc1f93dc7bbc18511b76b8:CT2000P3PSSD8_5291S317486A":     {"CT2000P3PSSD8", "5291S317486A"},
		"Samsung_Flash_Drive_8231230990224417-0:0":                                                        {"Samsung Flash Drive", "8231230990224417"},
	} {
		if m, s := modelSerial(id); m != want[0] || s != want[1] {
			t.Errorf("%s = %q %q", id, m, s)
		}
	}
}

func TestPickIP(t *testing.T) {
	var r struct {
		Data struct {
			Info struct {
				Interfaces []netInterface `json:"networkInterfaces"`
			} `json:"info"`
		} `json:"data"`
	}
	if err := json.Unmarshal(fixture(t, "network.json"), &r); err != nil {
		t.Fatal(err)
	}
	if ip := pickIP(r.Data.Info.Interfaces); ip != "10.0.11.120" {
		t.Fatalf("ip = %s", ip)
	}
	if ip := pickIP([]netInterface{{Name: "docker0", IPAddress: "10.101.0.1", Operstate: "up"}, {Name: "eno1", IPAddress: "10.0.11.5", Operstate: "up"}}); ip != "10.0.11.5" {
		t.Fatalf("ip = %s", ip)
	}
}

// equal compares a decoded JSON value with a Go literal.
func equal(got, want any) bool {
	switch w := want.(type) {
	case int:
		g, ok := got.(float64)
		return ok && g == float64(w)
	case float64:
		g, ok := got.(float64)
		return ok && math.Abs(g-w) < 1e-9
	}
	return got == want
}

// In control mode the key's Docker and VM update grants are what the
// actions use, so they aren't a "write grant" warning; in read-only mode
// they are, and any other write grant always is.
func TestControlModeExpectsItsWriteGrants(t *testing.T) {
	m := Integration{}.Manifest()
	grants := []integration.Grant{
		{Name: "docker", Access: integration.AccessReadWrite, Write: true},
		{Name: "vms", Access: integration.AccessReadWrite, Write: true},
		{Name: "array", Access: integration.AccessRead},
	}
	control := integration.Config{Values: map[string]any{"mode": ModeControl}}
	readOnly := integration.Config{Values: map[string]any{"mode": ModeReadOnly}}
	if w := integration.WriteGrantWarning(grants, m.ExpectedWrites(control)...); w != nil {
		t.Fatalf("control mode flags its own grants: %s", w.Message)
	}
	if w := integration.WriteGrantWarning(grants, m.ExpectedWrites(readOnly)...); w == nil {
		t.Fatal("read-only mode doesn't flag Docker and VM update grants")
	}
	extra := append(grants, integration.Grant{Name: "disk", Access: integration.AccessReadWrite, Write: true})
	if w := integration.WriteGrantWarning(extra, m.ExpectedWrites(control)...); w == nil || !strings.Contains(w.Message, "(disk)") {
		t.Fatalf("control mode hides another write grant: %v", w)
	}
	if _, ok := m.ControlConfig(readOnly); ok {
		t.Fatal("read-only mode can act")
	}
}
