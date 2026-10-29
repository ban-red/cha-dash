package docker

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
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

// fakeDocker is a small Docker Engine API: enough of it for the module's
// verbs, with containers held in memory. It listens on a Unix socket, as
// the real socket does.
type fakeDocker struct {
	mu         sync.Mutex
	containers map[string]*fakeContainer
	images     map[string]string // tag → image ID
	nextID     int
	calls      []string // "POST /containers/x/start", for asserting what the module did
	failCreate bool
	busyVols   []string // volumes DELETE /volumes refuses as in use
	logs       []fakeLog
	tty        bool
}

type fakeLog struct {
	stream byte
	text   string
}

type fakeContainer struct {
	ID, Name, Image, ImageID string
	State                    string
	Env                      []string
	Labels                   map[string]string
	Mounts                   []map[string]any
	Networks                 map[string]any
	Hostname                 string
	Tty                      bool
}

func (c *fakeContainer) inspect() map[string]any {
	return map[string]any{
		"Id": c.ID, "Name": "/" + c.Name, "Image": c.ImageID,
		"State":      map[string]any{"Status": c.State, "Running": c.State == "running"},
		"Config":     map[string]any{"Image": c.Image, "Env": c.Env, "Labels": c.Labels, "Hostname": c.Hostname, "Tty": c.Tty},
		"HostConfig": map[string]any{"NetworkMode": "bridge", "RestartPolicy": map[string]any{"Name": "unless-stopped"}},
		"Mounts":     c.Mounts, "NetworkSettings": map[string]any{"Networks": c.Networks},
	}
}

func newFakeDocker(t *testing.T) (*fakeDocker, string) {
	t.Helper()
	f := &fakeDocker{containers: map[string]*fakeContainer{}, images: map[string]string{}}
	// Unix socket paths are short on macOS: keep it out of the long TempDir.
	dir, err := os.MkdirTemp("", "cdd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(f.serve))
	srv.Listener.Close()
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)
	return f, "unix://" + sock
}

func (f *fakeDocker) add(c *fakeContainer) *fakeContainer {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c.ID == "" {
		f.nextID++
		c.ID = fmt.Sprintf("%064x", f.nextID+0xabc000)
	}
	if c.State == "" {
		c.State = "running"
	}
	if c.ImageID == "" {
		c.ImageID = "sha256:" + strings.Repeat("1", 64)
	}
	f.containers[c.ID] = c
	return c
}

func (f *fakeDocker) find(ref string) *fakeContainer {
	for _, c := range f.containers {
		if c.Name == ref || strings.HasPrefix(c.ID, ref) {
			return c
		}
	}
	return nil
}

func (f *fakeDocker) byName(name string) *fakeContainer {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.containers {
		if c.Name == name {
			return c
		}
	}
	return nil
}

var versionPrefix = regexp.MustCompile(`^/v1\.[0-9]+`)

func jsonOut(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func notFound(w http.ResponseWriter, what string) {
	jsonOut(w, http.StatusNotFound, map[string]string{"message": "No such container: " + what})
}

func (f *fakeDocker) serve(w http.ResponseWriter, r *http.Request) {
	path := versionPrefix.ReplaceAllString(r.URL.Path, "")
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		f.calls = append(f.calls, r.Method+" "+path)
	}
	w.Header().Set("Api-Version", "1.54")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	switch {
	case path == "/_ping":
		w.Write([]byte("OK"))
	case path == "/version":
		jsonOut(w, 200, map[string]any{"Version": "29.0.0", "ApiVersion": "1.54", "MinAPIVersion": "1.24", "Os": "linux", "Arch": "amd64"})
	case path == "/info":
		jsonOut(w, 200, map[string]any{"Name": "jupiter", "OperatingSystem": "Debian", "NCPU": 4, "MemTotal": int64(8 << 30), "HttpProxy": "http://user:hunter2@proxy.example"})
	case path == "/containers/json":
		var out []map[string]any
		for _, c := range f.containers {
			if c.State != "running" && r.URL.Query().Get("all") != "1" {
				continue
			}
			out = append(out, map[string]any{"Id": c.ID, "Names": []string{"/" + c.Name}, "Image": c.Image, "Labels": c.Labels, "State": c.State, "Status": "Up 3 minutes"})
		}
		jsonOut(w, 200, out)
	case path == "/containers/create" && r.Method == http.MethodPost:
		if f.failCreate {
			jsonOut(w, 500, map[string]string{"message": "create failed on purpose"})
			return
		}
		var body struct {
			Image      string
			Env        []string
			Labels     map[string]string
			Hostname   string
			HostConfig struct {
				Mounts []struct{ Source, Target string }
			}
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		name := r.URL.Query().Get("name")
		if f.find(name) != nil {
			jsonOut(w, 409, map[string]string{"message": "Conflict. The container name is already in use"})
			return
		}
		f.nextID++
		c := &fakeContainer{ID: fmt.Sprintf("%064x", f.nextID+0xdef000), Name: name, Image: body.Image, ImageID: f.images[body.Image], State: "created", Env: body.Env, Labels: body.Labels, Hostname: body.Hostname}
		for _, m := range body.HostConfig.Mounts {
			c.Mounts = append(c.Mounts, map[string]any{"Type": "volume", "Name": m.Source, "Destination": m.Target, "RW": true})
		}
		f.containers[c.ID] = c
		jsonOut(w, 201, map[string]any{"Id": c.ID, "Warnings": []string{}})
	case len(parts) >= 2 && parts[0] == "containers":
		c := f.find(parts[1])
		if c == nil {
			notFound(w, parts[1])
			return
		}
		verb := ""
		if len(parts) > 2 {
			verb = parts[2]
		}
		switch {
		case verb == "json":
			jsonOut(w, 200, c.inspect())
		case verb == "start":
			if c.State == "running" {
				w.WriteHeader(304)
				return
			}
			c.State = "running"
			w.WriteHeader(204)
		case verb == "stop":
			c.State = "exited"
			w.WriteHeader(204)
		case verb == "restart":
			c.State = "running"
			w.WriteHeader(204)
		case verb == "rename":
			c.Name = r.URL.Query().Get("name")
			w.WriteHeader(204)
		case verb == "" && r.Method == http.MethodDelete:
			if c.State == "running" && r.URL.Query().Get("force") != "1" {
				jsonOut(w, 409, map[string]string{"message": "cannot remove a running container"})
				return
			}
			delete(f.containers, c.ID)
			w.WriteHeader(204)
		case verb == "stats":
			jsonOut(w, 200, map[string]any{"cpu_stats": map[string]any{"online_cpus": 4}, "memory_stats": map[string]any{"usage": 1024}})
		case verb == "logs":
			w.Header().Set("Content-Type", "application/vnd.docker.multiplexed-stream")
			for _, l := range f.logs {
				if f.tty {
					w.Write([]byte(l.text))
					continue
				}
				hdr := make([]byte, 8)
				hdr[0] = l.stream
				binary.BigEndian.PutUint32(hdr[4:], uint32(len(l.text)))
				w.Write(hdr)
				w.Write([]byte(l.text))
			}
		default:
			jsonOut(w, 404, map[string]string{"message": "page not found"})
		}
	case len(parts) == 2 && parts[0] == "volumes" && r.Method == http.MethodDelete:
		if slices.Contains(f.busyVols, parts[1]) {
			jsonOut(w, 409, map[string]string{"message": "remove " + parts[1] + ": volume is in use"})
			return
		}
		w.WriteHeader(204)
	case path == "/images/create" && r.Method == http.MethodPost:
		ref := r.URL.Query().Get("fromImage")
		if tag := r.URL.Query().Get("tag"); tag != "" {
			ref += ":" + tag
		}
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		if strings.Contains(ref, "missing") {
			enc.Encode(map[string]any{"errorDetail": map[string]string{"message": "manifest unknown"}, "error": "manifest unknown"})
			return
		}
		enc.Encode(map[string]any{"status": "Pulling from library/" + ref, "id": "latest"})
		enc.Encode(map[string]any{"status": "Downloading", "id": "abc", "progressDetail": map[string]int{"current": 5, "total": 10}})
		enc.Encode(map[string]any{"status": "Status: Downloaded newer image for " + ref})
		f.images[strings.TrimPrefix(ref, "docker.io/library/")] = "sha256:" + strings.Repeat("2", 64)
	case len(parts) >= 3 && parts[0] == "images" && parts[len(parts)-1] == "json":
		ref := strings.Join(parts[1:len(parts)-1], "/")
		id, ok := f.images[ref]
		if !ok {
			jsonOut(w, 404, map[string]string{"message": "No such image: " + ref})
			return
		}
		jsonOut(w, 200, map[string]any{"Id": id})
	case path == "/events":
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.Encode(map[string]any{"Type": "container", "Action": "start", "Actor": map[string]any{"ID": "abc", "Attributes": map[string]string{"name": "sonarr", "image": "sonarr", "secret.label": "hunter2"}}, "time": 1})
		enc.Encode(map[string]any{"Type": "container", "Action": "die", "Actor": map[string]any{"ID": "abc"}, "time": 2})
	default:
		jsonOut(w, 404, map[string]string{"message": "page not found"})
	}
}
