package updates

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"dash.cha.sh/integrations/proxmox"
	"dash.cha.sh/integrations/unraid"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/integration/integrationtest"
	"dash.cha.sh/internal/msg"
)

// The host rows end to end (docs/plan/17-test-automation.md, 14b item 5):
// the Proxmox and Unraid integrations read their recorded fixtures, publish
// their hosts, and the Update Center lists what those hosts give. Pluto's
// recording has a PVEAuditor token; testdata/integrations/proxmox/sys-modify
// lays a synthetic token with Sys.Modify and its pending packages over it.

const (
	pveFixtures     = "../../testdata/integrations/proxmox"
	pveToken        = "api@pam!chadash"
	pveSecret       = "00000000-fake-4000-8000-proxmoxtest0"
	unraidFixtures  = "../../testdata/integrations/unraid/jupiter"
	unraidKey       = "fake-unraid-key"
	unraidRecording = "7.3.2" // info.json's versions.core.unraid
)

// fakePVE serves pluto's recording, by path and query as its _index.json
// lists them, with each file under overlay (if set) answering instead. It
// records every path asked for.
type fakePVE struct {
	mu   sync.Mutex
	seen []string
}

func (f *fakePVE) asked() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.seen)
}

func newFakePVE(t *testing.T, overlay string) (*fakePVE, string) {
	t.Helper()
	dir := filepath.Join(pveFixtures, "pluto")
	b, err := os.ReadFile(filepath.Join(dir, "_index.json"))
	if err != nil {
		t.Fatal(err)
	}
	var idx struct {
		Requests []struct {
			Path   string `json:"path"`
			File   string `json:"file"`
			Status int    `json:"status"`
		} `json:"requests"`
	}
	if err := json.Unmarshal(b, &idx); err != nil {
		t.Fatal(err)
	}
	f := &fakePVE{}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "PVEAPIToken="+pveToken+"="+pveSecret {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"data":null}`)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/api2/json")
		f.mu.Lock()
		f.seen = append(f.seen, path)
		f.mu.Unlock()
		if overlay != "" && r.URL.RawQuery == "" {
			if b, err := os.ReadFile(filepath.Join(overlay, filepath.FromSlash(path)+".json")); err == nil {
				w.Write(b)
				return
			}
		}
		for _, e := range idx.Requests {
			p, q, _ := strings.Cut(e.Path, "?")
			if p != path || !sameQuery(q, r.URL.RawQuery) {
				continue
			}
			body, err := os.ReadFile(filepath.Join(dir, e.File))
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			w.WriteHeader(e.Status)
			w.Write(body)
			return
		}
		w.WriteHeader(http.StatusNotImplemented) // PVE's answer to an unknown path
		fmt.Fprint(w, `{"data":null}`)
	}))
	t.Cleanup(srv.Close)
	return f, srv.URL + "/"
}

func sameQuery(a, b string) bool {
	split := func(s string) []string {
		if s == "" {
			return nil
		}
		p := strings.Split(s, "&")
		slices.Sort(p)
		return p
	}
	return slices.Equal(split(a), split(b))
}

// pveHost syncs pluto through the Proxmox integration and copies what it
// published into the Update Center's graph. It returns the instance, to
// test its token.
func (e *env) pveHost(overlay string) (*fakePVE, integration.Instance) {
	e.t.Helper()
	f, url := newFakePVE(e.t, overlay)
	h := integrationtest.New(e.t)
	in := h.Connect(proxmox.Integration{}, map[string]any{"url": url, "tls": map[string]any{"mode": "tofu"}, "token_id": pveToken},
		map[string]string{"token_secret": pveSecret})
	if err := in.Sync(e.t.Context(), integrationtest.Watched); err != nil {
		e.t.Fatalf("proxmox sync: %v", err)
	}
	e.publish(h)
	return f, in
}

// publish copies the harness's hosts into the Update Center's graph.
func (e *env) publish(h *integrationtest.Harness) {
	e.t.Helper()
	n := 0
	for _, r := range h.Graph().Resources {
		if r.Kind == "host" {
			e.upsert(*r)
			n++
		}
	}
	if n == 0 {
		e.t.Fatal("the integration published no host")
	}
}

// TestAptRowsWithSysModify: a token with Sys.Modify reads pluto's pending
// packages, and the Update Center lists them on pluto's apt row with their
// versions, the count, the riskiest move and Proxmox's page.
func TestAptRowsWithSysModify(t *testing.T) {
	e := newEnv(t)
	f, _ := e.pveHost(filepath.Join(pveFixtures, "sys-modify"))
	if !slices.Contains(f.asked(), "/nodes/pluto/apt/update") {
		t.Fatalf("pending updates weren't read: %v", f.asked())
	}
	a, ok := e.list()["pluto"]
	if !ok {
		t.Fatalf("no row for pluto: %v", e.list())
	}
	want := []Package{
		{Name: "libc6", From: "2.41-12", To: "2.41-12+deb13u1", Risk: RiskPatch},
		{Name: "pve-manager", From: "9.0.9", To: "9.0.11", Risk: RiskPatch},
		{Name: "pve-qemu-kvm", From: "10.0.2-4", To: "10.1.2-1", Risk: RiskMinor},
	}
	if a.Kind != KindApt || a.State != integration.UpdateAvailable || a.Latest != "3 packages" || !slices.Equal(a.Packages, want) ||
		a.Risk != RiskMinor || a.Update != nil || a.Why != msg.New("updates.apt_elsewhere").Text() ||
		a.Link == nil || !strings.HasPrefix(a.Link.URL, "https://") {
		t.Fatalf("pluto = %+v", a)
	}
}

// TestAptRowsHiddenFromAnAuditor: pluto's recorded PVEAuditor token never
// asks for pending updates, so the Update Center has no apt row for the
// host; the reason is the instance's updates_hidden warning, naming
// Sys.Modify (the recipe: "Settings → Updates shows no package rows").
func TestAptRowsHiddenFromAnAuditor(t *testing.T) {
	e := newEnv(t)
	f, in := e.pveHost("")
	if slices.Contains(f.asked(), "/nodes/pluto/apt/update") {
		t.Fatal("an auditor token asked for pending updates")
	}
	for _, r := range e.list() {
		if r.Kind == KindApt {
			t.Fatalf("an apt row without Sys.Modify: %+v", r)
		}
	}
	res := in.Test(t.Context())
	var hidden *integration.Warning
	for i, w := range res.Warnings {
		if w.Code == "updates_hidden" {
			hidden = &res.Warnings[i]
		}
	}
	if !res.OK || hidden == nil || !strings.Contains(hidden.Message, "Sys.Modify") || !strings.HasPrefix(hidden.Message, "Pending updates aren't shown") {
		t.Fatalf("test = %+v", res)
	}
}

// TestUnraidOSRowCantCheck: Unraid's recorded host gives an OS row with
// its version that reads "can't check", saying why, with a link to the
// server's own page.
func TestUnraidOSRowCantCheck(t *testing.T) {
	e := newEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		files := map[string]string{
			"ChadashVersion": "info.json", "ChadashInfo": "info.json", "ChadashNetwork": "network.json", "ChadashMetrics": "metrics.json",
			"ChadashArray": "array.json", "ChadashDocker": "docker.json", "ChadashShares": "shares.json", "ChadashMe": "me.json",
		}
		var req struct {
			OperationName string `json:"operationName"`
		}
		if r.Method != http.MethodPost || r.Header.Get("x-api-key") != unraidKey || json.NewDecoder(r.Body).Decode(&req) != nil || files[req.OperationName] == "" {
			http.NotFound(w, r)
			return
		}
		b, err := os.ReadFile(filepath.Join(unraidFixtures, files[req.OperationName]))
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		// An API version the integration accepts, as internal/correlate's test does.
		w.Write([]byte(strings.Replace(string(b), `"4.37.2+a00c99a5"`, `"4.37.5+0123abcd"`, 1)))
	}))
	t.Cleanup(srv.Close)
	h := integrationtest.New(t)
	url := strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)
	in := h.Connect(unraid.Integration{}, map[string]any{"url": url, "tls": map[string]any{"mode": "verify"}}, map[string]string{"api_key": unraidKey})
	if err := in.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatalf("unraid sync: %v", err)
	}
	e.publish(h)
	var osRows []Row
	for _, r := range e.list() {
		if r.Kind == KindOS {
			osRows = append(osRows, r)
		}
	}
	if len(osRows) != 1 {
		t.Fatalf("OS rows = %+v", osRows)
	}
	u := osRows[0]
	if u.Image != "Unraid" || u.Current != unraidRecording || u.State != integration.UpdateCantCheck || u.Risk != RiskUnknown ||
		u.Reason != msg.New("updates.unraid_cant_check").Text() || !strings.Contains(u.Reason, "Update OS") || u.Update != nil ||
		u.Link == nil || !strings.HasPrefix(u.Link.URL, "http://localhost:") {
		t.Fatalf("unraid = %+v", u)
	}
}
