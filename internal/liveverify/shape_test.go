package liveverify

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const fixtures = "../../testdata/integrations"

func TestShapeOf(t *testing.T) {
	s := ShapeOf([]byte(`{"data":[{"vmid":100,"name":"a","tags":null},{"vmid":101,"lock":"backup"}],
		"perms":{"/":{"Sys.Audit":1},"/vms":{"VM.Audit":1}}}`))
	for path, typ := range map[string]string{"data": "array", "data[].vmid": "number", "data[].name": "string",
		"data[].lock": "string", "data[].tags": "null", "perms.*": "object", "perms.*.*": "number"} {
		if !s[path][typ] {
			t.Errorf("%s isn't %s: %v", path, typ, s[path])
		}
	}
	if _, ok := s["perms./vms"]; ok {
		t.Error("keys that are data (a path) must merge into .*")
	}
	if ShapeOf([]byte("not json")) != nil {
		t.Error("not JSON has no shape")
	}
}

func TestCompare(t *testing.T) {
	rec := ShapeOf([]byte(`{"data":[{"vmid":1,"mem":5,"cpu":{"n":2,"model":"x"},"note":null}]}`))
	live := ShapeOf([]byte(`{"data":[{"vmid":1,"mem":"5","note":"now set","new":{"a":1}}]}`))
	d := Compare(rec, live)
	if !slices.Equal(d.Removed, []string{"data[].cpu"}) {
		t.Errorf("removed = %v, want only the outermost of the subtree", d.Removed)
	}
	if !slices.Equal(d.Changed, []string{"data[].mem: number → string"}) {
		t.Errorf("changed = %v", d.Changed)
	}
	if !slices.Equal(d.Added, []string{"data[].new"}) {
		t.Errorf("added = %v", d.Added)
	}
	if d.Empty() {
		t.Error("a removed field is drift")
	}
	if !Compare(rec, rec).Empty() || len(Compare(rec, rec).Added) != 0 {
		t.Error("a recording matches itself")
	}
	// An object whose keys share nothing with the recording's is a map by
	// name (PVE's tokens, by token name): compared as one, not as gone.
	rec = ShapeOf([]byte(`{"data":{"tokens":{"chadash":{"expire":0,"privsep":1}}}}`))
	if d := Compare(rec, ShapeOf([]byte(`{"data":{"tokens":{"dash-ro":{"expire":0,"privsep":1},"other":{"expire":5}}}}`))); !d.Empty() || len(d.Added) != 0 {
		t.Errorf("a map by name = %+v", d)
	}
	if d := Compare(rec, ShapeOf([]byte(`{"data":{"tokens":{"dash-ro":{"expire":"never"}}}}`))); !slices.Equal(d.Removed, []string{"data.tokens.*.privsep"}) || len(d.Changed) != 1 {
		t.Errorf("a map by name still compares its values: %+v", d)
	}
	// A field that is null now, or only ever was, is optional.
	if d := Compare(ShapeOf([]byte(`{"a":1,"b":null}`)), ShapeOf([]byte(`{"a":null}`))); !d.Empty() {
		t.Errorf("nulls = %+v", d)
	}
}

func TestRoutes(t *testing.T) {
	for in, want := range map[string]string{
		"/api2/json/nodes/pluto/qemu/103/config":                                "/nodes/{}/qemu/{}/config",
		"/api2/json/nodes/pluto/tasks/UPID:pluto:003D:vzdump::root@pam:/status": "/nodes/{}/tasks/{}/status",
		"/api2/json/cluster/resources":                                          "/cluster/resources",
		"/api2/json/nodes":                                                      "/nodes",
	} {
		if got := pveRoute(in); got != want {
			t.Errorf("pveRoute(%s) = %s, want %s", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"/v1.54/containers/json":                      "/containers/json",
		"/v1.54/containers/0123456789abcdef0123/json": "/containers/{}/json",
		"/v1.54/containers/plex/json":                 "/containers/{}/json",
		"/version":                                    "/version",
	} {
		if got := dockerRoute(in); got != want {
			t.Errorf("dockerRoute(%s) = %s, want %s", in, got, want)
		}
	}
}

// pveServer answers like a captured PVE instance, through edit.
func pveServer(t *testing.T, name string, edit func(path string, body []byte) []byte) (*httptest.Server, []string) {
	t.Helper()
	dir := filepath.Join(fixtures, "proxmox", name)
	b, err := os.ReadFile(filepath.Join(dir, "_index.json"))
	if err != nil {
		t.Fatal(err)
	}
	var idx struct {
		Requests []struct {
			Path, File string
			Status     int
		} `json:"requests"`
	}
	if err := json.Unmarshal(b, &idx); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	var paths []string
	for _, r := range idx.Requests {
		if r.Status == 200 {
			p, _, _ := strings.Cut(r.Path, "?")
			files[p] = r.File
			paths = append(paths, p)
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/api2/json")
		body, err := os.ReadFile(filepath.Join(dir, files[p]))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json;charset=UTF-8")
		_, _ = w.Write(edit(p, body))
	}))
	t.Cleanup(srv.Close)
	return srv, paths
}

func ask(t *testing.T, c *Capture, base string, paths []string) {
	t.Helper()
	cl := &http.Client{Transport: c}
	for _, p := range paths {
		resp, err := cl.Get(base + "/api2/json" + p)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
}

// Each recording, asked again through the capture, matches itself: the
// comparison finds no drift where there is none.
func TestARecordingMatchesItself(t *testing.T) {
	for _, name := range []string{"pluto", "minig5", "mars", "io", "jupiter-nas"} {
		rec, err := loadPVE(fixtures, name)
		if err != nil {
			t.Fatal(err)
		}
		srv, paths := pveServer(t, name, func(_ string, b []byte) []byte { return b })
		c := &Capture{Next: http.DefaultTransport, Route: pveRoute}
		ask(t, c, srv.URL, paths)
		res := c.Against(rec.Routes)
		if len(res.Routes) != 0 || res.Unseen != 0 || res.Missing != 0 || res.Compared == 0 {
			t.Errorf("%s against itself: %+v", name, res)
		}
	}
	if rec, err := loadDocker(fixtures, "jupiter"); err != nil || rec.Routes["/containers/{}/json"] == nil || rec.Version == "" {
		t.Fatalf("docker/jupiter: %v %+v", err, rec.Version)
	}
	if rec, _ := loadPVE(fixtures, "jupiter-nas"); rec.Version != "8.4.21" {
		t.Errorf("jupiter-nas was recorded on %q", rec.Version)
	}
}

// A field the upstream dropped, and one whose type changed, are drift; the
// summary names them from the recording.
func TestDriftIsFound(t *testing.T) {
	rec, err := loadPVE(fixtures, "pluto")
	if err != nil {
		t.Fatal(err)
	}
	srv, paths := pveServer(t, "pluto", func(p string, b []byte) []byte {
		if p != "/cluster/resources" {
			return b
		}
		var env struct {
			Data []map[string]any `json:"data"`
		}
		_ = json.Unmarshal(b, &env)
		for _, r := range env.Data {
			delete(r, "maxmem")
			if _, ok := r["uptime"]; ok {
				r["uptime"] = "a long time"
			}
		}
		nb, _ := json.Marshal(env)
		return nb
	})
	c := &Capture{Next: http.DefaultTransport, Route: pveRoute}
	ask(t, c, srv.URL, paths)
	d, ok := c.Against(rec.Routes).Routes["/cluster/resources"]
	if !ok || !slices.Contains(d.Removed, "data[].maxmem") || len(d.Changed) != 1 || !strings.HasPrefix(d.Changed[0], "data[].uptime: number → string") {
		t.Fatalf("drift = %+v", d)
	}
}
