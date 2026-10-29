package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"dash.cha.sh/internal/graph"
)

// fakeSocketProxy answers like the shipped compose.yaml's wollomatic proxy in
// front of an engine with three containers: the anchored GET allowlist,
// text/plain 403 for everything else, and never a write.
type fakeSocketProxy struct {
	t        *testing.T
	mu       sync.Mutex
	requests []string
}

var allowed = regexp.MustCompile(`^/(v1\.[0-9]+/)?(_ping|version|info|events|containers/json|containers/[a-zA-Z0-9][a-zA-Z0-9_.-]*/(json|stats)|images/json)$`)

func (f *fakeSocketProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)
	f.mu.Unlock()
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		f.t.Errorf("onboarding sent %s %s to the socket proxy", r.Method, r.URL.Path)
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	if !allowed.MatchString(r.URL.Path) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	path := regexp.MustCompile(`^/v1\.[0-9]+`).ReplaceAllString(r.URL.Path, "")
	reply := func(v any) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(v)
	}
	containers := []map[string]any{
		{"Id": strings.Repeat("a", 64), "Names": []string{"/sonarr"}, "Image": "lscr.io/linuxserver/sonarr", "State": "running", "Status": "Up 3 hours", "Labels": map[string]string{"com.docker.compose.project": "media"}},
		{"Id": strings.Repeat("b", 64), "Names": []string{"/radarr"}, "Image": "lscr.io/linuxserver/radarr", "State": "running", "Status": "Up 3 hours", "Labels": map[string]string{"com.docker.compose.project": "media"}},
		{"Id": strings.Repeat("c", 64), "Names": []string{"/backup"}, "Image": "restic/restic", "State": "exited", "Status": "Exited (0) 2 hours ago", "Labels": map[string]string{}},
	}
	switch {
	case path == "/_ping":
		w.Header().Set("Api-Version", "1.47")
		w.Write([]byte("OK"))
	case path == "/version":
		reply(map[string]any{"Version": "27.5.1", "ApiVersion": "1.47", "MinAPIVersion": "1.24", "Os": "linux", "Arch": "amd64", "KernelVersion": "6.8.0"})
	case path == "/info":
		reply(map[string]any{"Name": "docker-host", "OperatingSystem": "Debian GNU/Linux 12", "NCPU": 4, "MemTotal": int64(8 << 30)})
	case path == "/containers/json":
		reply(containers)
	case strings.HasSuffix(path, "/json") && strings.HasPrefix(path, "/containers/"):
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/containers/"), "/json")
		for _, c := range containers {
			if c["Id"] == id {
				reply(map[string]any{"RestartCount": 0, "State": map[string]any{"Status": c["State"], "StartedAt": "2026-10-04T09:00:00Z"},
					"HostConfig": map[string]any{"RestartPolicy": map[string]any{"Name": "unless-stopped"}}})
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"message":"No such container"}`))
	case path == "/events":
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK) // a window in the past: ends at once
	default:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"message":"not in the fake"}`))
	}
}

// The v0.1 promise: from a fresh install to a live page with no YAML. Setup,
// then the wizard finds Docker through the compose sidecar's socket proxy,
// one click adds it, and the graph has the containers.
func TestThreeMinutePath(t *testing.T) {
	proxy := &fakeSocketProxy{t: t}
	ps := httptest.NewServer(proxy)
	defer ps.Close()
	was := sidecarEndpoint
	sidecarEndpoint = "tcp://" + strings.TrimPrefix(ps.URL, "http://")
	defer func() { sidecarEndpoint = was }()

	start := time.Now()
	a, err := newApp(t.Context(), testConfig(t, t.TempDir()), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()
	srv := httptest.NewServer(a.handler)
	defer srv.Close()
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	call := func(method, path string, body any, want int) map[string]any {
		t.Helper()
		var r io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			r = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, srv.URL+path, r)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("Origin", srv.URL)
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != want {
			t.Fatalf("%s %s: %d %s", method, path, resp.StatusCode, raw)
		}
		var out map[string]any
		json.Unmarshal(raw, &out)
		return out
	}

	// 1. First run: the owner account.
	if st := call("GET", "/api/setup", nil, 200); st["required"] != true {
		t.Fatalf("setup: %v", st)
	}
	call("POST", "/api/setup", map[string]string{"username": "owner", "password": "correct horse battery staple"}, 201)

	// 2. The wizard opens on its own on a new hub, and remembers its step.
	if st := call("GET", "/api/onboarding", nil, 200); st["status"] != "new" || st["show"] != true {
		t.Fatalf("onboarding on a new hub: %v", st)
	}
	call("PUT", "/api/onboarding", map[string]string{"status": "started", "step": "discover"}, 200)

	// 3. Discover: the sidecar's socket proxy answers.
	res := call("POST", "/api/onboarding/discover", map[string]any{"sidecar": true}, 200)
	found, _ := res["found"].([]any)
	if len(found) != 1 {
		t.Fatalf("discover: %v", res)
	}
	docker := found[0].(map[string]any)
	if docker["integration"] != "docker" || docker["ready"] != true || docker["source"] != "sidecar" || docker["version"] != "27.5.1" {
		t.Fatalf("the sidecar: %v", docker)
	}
	for _, g := range docker["grants"].([]any) {
		if g.(map[string]any)["write"] == true {
			t.Fatalf("a filtered proxy has no write grant: %v", docker["grants"])
		}
	}

	// 4. One click adds it, as discovered.
	created := call("POST", "/api/integrations/instances", map[string]any{
		"integration": "docker", "name": docker["name"], "config": docker["config"], "secrets": map[string]any{},
	}, 201)
	id, _ := created["id"].(string)

	// 5. The page has something to show: the engine and its containers.
	want := []graph.RID{graph.RID("docker:" + id + "/engine"), graph.RID("docker:" + id + "/container/sonarr"), graph.RID("docker:" + id + "/container/radarr"), graph.RID("docker:" + id + "/container/backup")}
	for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		missing := 0
		for _, rid := range want {
			if _, ok := a.graph.Get(rid); !ok {
				missing++
			}
		}
		if missing == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d of %d resources never reached the graph", missing, len(want))
		}
	}
	if r, _ := a.graph.Get(graph.RID("docker:" + id + "/container/sonarr")); r.Kind != "container" || r.State != "running" {
		t.Fatalf("sonarr: %+v", r)
	}
	// Discovery found it already added, and the wizard is done for good.
	res = call("POST", "/api/onboarding/discover", map[string]any{"sidecar": true}, 200)
	if again := res["found"].([]any)[0].(map[string]any); again["added"] != id {
		t.Fatalf("a second discovery should see the instance: %v", again)
	}
	call("PUT", "/api/onboarding", map[string]string{"status": "done"}, 200)
	if st := call("GET", "/api/onboarding", nil, 200); st["show"] != false || st["status"] != "done" {
		t.Fatalf("after the wizard: %v", st)
	}
	if took := time.Since(start); took > 3*time.Minute {
		t.Fatalf("took %s", took)
	}
	proxy.mu.Lock()
	defer proxy.mu.Unlock()
	for _, r := range proxy.requests {
		if !strings.HasPrefix(r, "GET ") && !strings.HasPrefix(r, "HEAD ") {
			t.Fatalf("not a read: %s", r)
		}
	}
}
