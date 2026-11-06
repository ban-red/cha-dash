package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"dash.cha.sh/internal/pvehook"
)

// fakePVE serves a captured Proxmox VE instance from testdata: each request
// in its _index.json, matched by path and then query. hide drops tasks from
// the task lists, as if they hadn't run yet.
type fakePVE struct {
	*httptest.Server
	dir     string
	entries []struct {
		Path   string `json:"path"`
		File   string `json:"file"`
		Status int    `json:"status"`
	}

	mu   sync.Mutex
	hide []string // UPIDs
}

func newFakePVE(t *testing.T, inst string) *fakePVE {
	t.Helper()
	f := &fakePVE{dir: filepath.Join("..", "..", "testdata", "integrations", "proxmox", inst)}
	b, err := os.ReadFile(filepath.Join(f.dir, "_index.json"))
	if err != nil {
		t.Fatal(err)
	}
	var idx struct {
		Requests json.RawMessage `json:"requests"`
	}
	if err := json.Unmarshal(b, &idx); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(idx.Requests, &f.entries); err != nil {
		t.Fatal(err)
	}
	f.Server = httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *fakePVE) setHidden(upids ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hide = upids
}

func (f *fakePVE) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	path := strings.TrimPrefix(r.URL.Path, "/api2/json")
	file, status := "", 0
	// The exact request first; else the same path with another query (a
	// metrics export starts at the hub's now, not the capture's).
	for _, e := range f.entries {
		ep, eq, _ := strings.Cut(e.Path, "?")
		q, _ := url.ParseQuery(eq)
		if ep == path && q.Encode() == r.URL.Query().Encode() {
			file, status = e.File, e.Status
			break
		}
		if ep == path && file == "" && eq != "" && len(r.URL.Query()) > 0 {
			file, status = e.File, e.Status
		}
	}
	if file == "" {
		w.WriteHeader(http.StatusNotImplemented)
		_, _ = w.Write([]byte(`{"data":null}`))
		return
	}
	body, err := os.ReadFile(filepath.Join(f.dir, file))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	f.mu.Lock()
	hide := slices.Clone(f.hide)
	f.mu.Unlock()
	if len(hide) > 0 && (path == "/cluster/tasks" || strings.HasSuffix(path, "/tasks")) {
		var env struct {
			Data []map[string]any `json:"data"`
		}
		if json.Unmarshal(body, &env) == nil {
			env.Data = slices.DeleteFunc(env.Data, func(t map[string]any) bool {
				u, _ := t["upid"].(string)
				return slices.Contains(hide, u)
			})
			body, _ = json.Marshal(env)
		}
	}
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// A backup fails on PVE, and PVE's webhook says so: the hub syncs the
// instance at once and the incident is in the Inbox, without waiting for
// the next poll. 14b's manual test 9, with no lab and no waiting: the
// hub's clock doesn't move, so only the webhook can bring it.
func TestSystemPVEHookOpensTheIncident(t *testing.T) {
	s, _ := newHub(t)
	pve := newFakePVE(t, "pluto")
	// Before the night's run: the scheduled run that failed, and the manual
	// one after it, haven't happened.
	failed := "UPID:pluto:003D7632:06868C45:6AC0C412:vzdump::root@pam:"
	pve.setHidden(failed, "UPID:pluto:003F60DE:06BCCF25:6AC14EFA:vzdump:103:root@pam:")

	code, body := s.do(s.c, "POST", "/api/integrations/instances", map[string]any{
		"integration": "proxmox", "name": "pluto",
		"config":  map[string]any{"url": pve.URL + "/", "tls": map[string]any{"mode": "tofu"}, "token_id": "api@pam!chadash"},
		"secrets": map[string]string{"token_secret": "00000000-fake-4000-8000-proxmoxtest0"},
	}, nil)
	var inst struct {
		ID string `json:"id"`
	}
	if code != http.StatusCreated || json.Unmarshal(body, &inst) != nil || inst.ID == "" {
		t.Fatalf("the Proxmox instance: %d %s", code, body)
	}
	s.waitFor(30*time.Second, "the first sync", func() (bool, string) {
		_, body := s.do(s.c, "GET", "/api/integrations/instances/"+inst.ID, nil, nil)
		var v struct {
			Status string `json:"status"`
			LastOK any    `json:"last_ok"`
		}
		_ = json.Unmarshal(body, &v)
		return v.LastOK != nil && (v.Status == "ok" || v.Status == "degraded"), string(body)
	})
	incident := func() (bool, string) {
		_, body := s.do(s.c, "GET", "/api/inbox", nil, nil)
		var inbox struct {
			Incidents map[string]struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"incidents"`
		}
		if err := json.Unmarshal(body, &inbox); err != nil || inbox.Incidents == nil {
			t.Fatalf("the Inbox: %v %s", err, body)
		}
		for _, i := range inbox.Incidents {
			if strings.HasSuffix(i.ID, ".backup.backup-8ef1991a-dabd") && i.Status == "open" {
				return true, string(body)
			}
		}
		return false, string(body)
	}
	if open, body := incident(); open {
		t.Fatalf("the backup incident is open before the run failed: %s", body)
	}

	// The run fails; PVE's webhook target posts its notification.
	pve.setHidden("UPID:pluto:003F60DE:06BCCF25:6AC14EFA:vzdump:103:root@pam:")
	code, body = s.do(s.c, "POST", "/api/integrations/instances/"+inst.ID+"/pve-hook", nil, nil)
	var hook struct {
		Secret string `json:"secret"`
		Path   string `json:"path"`
	}
	if code != http.StatusOK || json.Unmarshal(body, &hook) != nil || hook.Secret == "" {
		t.Fatalf("the webhook secret: %d", code)
	}
	note, err := os.ReadFile(filepath.Join("..", "..", "testdata", "integrations", "proxmox", "webhook", "backup-failed.json"))
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequestWithContext(t.Context(), "POST", s.srv.URL+hook.Path, bytes.NewReader(note))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(pvehook.Header, hook.Secret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("the notification = %d", resp.StatusCode)
	}
	s.waitFor(15*time.Second, "the backup incident in the Inbox", incident)
}
