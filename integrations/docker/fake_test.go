package docker

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
)

// fixtures is the spike 4.4 capture of jupiter's engine through its
// Tecnativa socket proxy.
const fixtures = "../../testdata/integrations/docker/jupiter"

// Proxy modes for the fake.
const (
	tecnativa = "tecnativa" // the capture: CONTAINERS=1 EVENTS=1 PING=1 VERSION=1, everything else 403
	anchored  = "anchored"  // a path-anchored allowlist like the compose preset (synthetic)
	raw       = "raw"       // no proxy at all (synthetic)
)

// fakeDocker serves the recorded engine behind a proxy of the given mode.
// Docker's own answers are emulated where the capture has none (probe
// requests about an unknown container, the swarm endpoints of a raw API).
type fakeDocker struct {
	t    *testing.T
	mode string
	api  string // the engine's API version

	mu       sync.Mutex
	list     []map[string]any
	inspects map[string]map[string]any // by full ID
	paths    []string                  // every request, method and path
	deny     map[string]bool           // paths the proxy refuses whatever its mode

	events     chan string   // raw event lines for the stream
	cut        chan struct{} // ends the open stream, like a proxy timing out
	subscribed chan struct{} // receives once per stream opened
}

func newFake(t *testing.T, mode string) *fakeDocker {
	t.Helper()
	f := &fakeDocker{t: t, mode: mode, api: "1.54", inspects: map[string]map[string]any{}, deny: map[string]bool{},
		events: make(chan string, 64), cut: make(chan struct{}), subscribed: make(chan struct{}, 8)}
	readJSON(t, filepath.Join(fixtures, "containers.json"), &f.list)
	files, _ := filepath.Glob(filepath.Join(fixtures, "inspect", "*.json"))
	for _, p := range files {
		var m map[string]any
		readJSON(t, p, &m)
		f.inspects[m["Id"].(string)] = m
	}
	if len(f.list) != 74 || len(f.inspects) != 74 {
		t.Fatalf("fixtures: %d containers, %d inspects", len(f.list), len(f.inspects))
	}
	return f
}

func readJSON(t *testing.T, path string, dst any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, dst); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

// serve starts the fake on TCP and returns the endpoint config value.
func (f *fakeDocker) serve() string {
	srv := httptest.NewServer(f)
	f.t.Cleanup(srv.Close)
	return "tcp://" + strings.TrimPrefix(srv.URL, "http://")
}

// container returns a container's summary and inspect by name.
func (f *fakeDocker) container(name string) (map[string]any, map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.list {
		if c["Names"].([]any)[0] == "/"+name {
			return c, f.inspects[c["Id"].(string)]
		}
	}
	f.t.Fatalf("no container %s", name)
	return nil, nil
}

// update changes the recorded engine under the lock.
func (f *fakeDocker) update(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn()
}

// requests returns the paths requested so far that match a pattern.
func (f *fakeDocker) requests(pattern string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	re := regexp.MustCompile(pattern)
	var out []string
	for _, p := range f.paths {
		if re.MatchString(p) {
			out = append(out, p)
		}
	}
	return out
}

var versioned = regexp.MustCompile(`^/v1\.\d+(/.*)$`)

func (f *fakeDocker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.paths = append(f.paths, r.Method+" "+r.URL.RequestURI())
	f.mu.Unlock()
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		f.t.Errorf("the integration sent %s %s: it must only read", r.Method, r.URL.Path)
		http.Error(w, "no", http.StatusMethodNotAllowed)
		return
	}
	path := r.URL.Path
	if m := versioned.FindStringSubmatch(path); m != nil {
		path = m[1]
	}
	if !f.allowed(path) {
		if f.mode == tecnativa {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusForbidden)
			io.WriteString(w, "<html><body><h1>403 Forbidden</h1>\nRequest forbidden by administrative rules.\n</body></html>\n")
			return
		}
		http.Error(w, "Forbidden", http.StatusForbidden) // text/plain, like wollomatic
		return
	}
	f.docker(w, r, path)
}

// allowed is the proxy's verdict on a path.
func (f *fakeDocker) allowed(path string) bool {
	if f.deny[path] {
		return false
	}
	switch f.mode {
	case tecnativa: // section prefixes, as probe.json recorded them
		return path == "/_ping" || path == "/version" || strings.HasPrefix(path, "/containers") || path == "/events"
	case anchored:
		for _, re := range anchoredPaths {
			if re.MatchString(path) {
				return true
			}
		}
		return false
	}
	return true
}

var anchoredPaths = []*regexp.Regexp{
	regexp.MustCompile(`^/_ping$`), regexp.MustCompile(`^/version$`), regexp.MustCompile(`^/info$`),
	regexp.MustCompile(`^/events$`), regexp.MustCompile(`^/containers/json$`),
	regexp.MustCompile(`^/containers/[a-zA-Z0-9_.-]+/(json|stats)$`),
}

var containerPath = regexp.MustCompile(`^/containers/([^/]+)/(json|logs|stats)$`)

// docker answers like the engine.
func (f *fakeDocker) docker(w http.ResponseWriter, r *http.Request, path string) {
	q := r.URL.Query()
	switch {
	case path == "/_ping":
		w.Header().Set("Api-Version", f.api)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		io.WriteString(w, "OK")
	case path == "/version":
		var v map[string]any
		readJSON(f.t, filepath.Join(fixtures, "version.json"), &v)
		v["ApiVersion"] = f.api
		writeJSON(w, 200, v)
	case path == "/info":
		writeJSON(w, 200, map[string]any{"Name": "jupiter", "OperatingSystem": "Unraid OS 7.3", "NCPU": 12, "MemTotal": 64 << 30})
	case path == "/containers/json":
		f.mu.Lock()
		defer f.mu.Unlock()
		writeJSON(w, 200, f.list)
	case containerPath.MatchString(path):
		m := containerPath.FindStringSubmatch(path)
		f.mu.Lock()
		ins := f.inspects[m[1]]
		f.mu.Unlock()
		switch {
		case m[2] == "logs" && q.Get("stdout") != "1" && q.Get("stderr") != "1":
			writeJSON(w, 400, map[string]string{"message": "Bad parameters: you must choose at least one stream"})
		case ins == nil:
			writeJSON(w, 404, map[string]string{"message": "No such container: " + m[1]})
		case m[2] == "json":
			f.mu.Lock()
			defer f.mu.Unlock()
			writeJSON(w, 200, ins)
		default:
			f.t.Errorf("unexpected container request %s", r.URL.RequestURI())
			writeJSON(w, 500, map[string]string{"message": "not in the fake"})
		}
	case strings.HasPrefix(path, "/images/"):
		writeJSON(w, 404, map[string]string{"message": "No such image"})
	case path == "/swarm", path == "/secrets", path == "/configs", path == "/nodes":
		writeJSON(w, 503, map[string]string{"message": "This node is not a swarm manager."})
	case path == "/plugins":
		writeJSON(w, 200, []any{})
	case path == "/events":
		f.stream(w, r)
	default:
		writeJSON(w, 404, map[string]string{"message": "page not found"})
	}
}

// stream serves /events: at once for a past window, else live until the
// test cuts it, applying the type and event filters like Docker.
func (f *fakeDocker) stream(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	w.Header().Set("Content-Type", "application/json")
	if q.Get("until") != "" {
		return
	}
	var filters map[string][]string
	if err := json.Unmarshal([]byte(q.Get("filters")), &filters); err != nil || !slices.Equal(filters["type"], []string{"container"}) {
		f.t.Errorf("the event stream must be filtered by the engine: %q", q.Get("filters"))
	}
	w.WriteHeader(200)
	w.(http.Flusher).Flush()
	f.subscribed <- struct{}{}
	for {
		select {
		case <-r.Context().Done():
			return
		case <-f.cut:
			return
		case line := <-f.events:
			var ev struct{ Type, Action string }
			_ = json.Unmarshal([]byte(line), &ev)
			action, _, _ := strings.Cut(ev.Action, ":")
			if ev.Type != "container" || !slices.Contains(filters["event"], action) {
				continue
			}
			io.WriteString(w, line+"\n")
			w.(http.Flusher).Flush()
		}
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
