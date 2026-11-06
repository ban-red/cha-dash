package docker

import (
	"encoding/base64"
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
	mu          sync.Mutex
	containers  map[string]*fakeContainer
	images      map[string]string         // tag → image ID
	imageCfg    map[string]map[string]any // image ID → its Config, as inspect gives it
	repoDigests map[string][]string       // image ID → its RepoDigests
	descriptors map[string]string         // image ID → its descriptor's digest, as the containerd image store gives it
	registry    map[string]string         // tag → its digest, "401" (needs a login) or "" (no such tag)
	asked       []string                  // the tags the fake registry was asked about (fakeRegistry)
	inspected   []string                  // the tags Docker's /distribution was asked about
	pullAuth    map[string]string         // ref → the X-Registry-Auth its pull sent
	headers     []http.Header             // what each registry HEAD sent
	crash       map[string]bool           // images whose containers restart at once on start
	exits       map[string]bool           // images whose containers exit at once on start (restart policy no)
	nextID      int
	calls       []string // "POST /containers/x/start", for asserting what the module did
	failCreate  bool
	busyVols    []string // volumes DELETE /volumes refuses as in use
	logs        []fakeLog
	tty         bool
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
	Cmd                      []string
	Healthcheck              map[string]any
	Health                   string // healthy, unhealthy, starting; "" without a healthcheck
	RestartCount             int
}

func (c *fakeContainer) inspect() map[string]any {
	return map[string]any{
		"Id": c.ID, "Name": "/" + c.Name, "Image": c.ImageID,
		"State":        c.state(),
		"RestartCount": c.RestartCount,
		"Config":       map[string]any{"Image": c.Image, "Env": c.Env, "Labels": c.Labels, "Hostname": c.Hostname, "Tty": c.Tty, "Cmd": c.Cmd, "Healthcheck": c.Healthcheck},
		"HostConfig":   map[string]any{"NetworkMode": "bridge", "RestartPolicy": map[string]any{"Name": "unless-stopped"}},
		"Mounts":       c.Mounts, "NetworkSettings": map[string]any{"Networks": c.Networks},
	}
}

// imageID is the image a reference names: by tag, or by tag@digest as
// Docker resolves a pinned reference to the image pulled for it.
func (f *fakeDocker) imageID(ref string) string {
	if id, ok := f.images[ref]; ok {
		return id
	}
	if tag, _, ok := strings.Cut(ref, "@"); ok {
		return f.images[tag]
	}
	return ""
}

func (c *fakeContainer) state() map[string]any {
	// Docker reports a restarting container as running too.
	st := map[string]any{"Status": c.State, "Running": c.State == "running" || c.State == "restarting", "Restarting": c.State == "restarting"}
	if c.Health != "" {
		st["Health"] = map[string]any{"Status": c.Health}
	}
	return st
}

func newFakeDocker(t *testing.T) (*fakeDocker, string) {
	t.Helper()
	f := &fakeDocker{containers: map[string]*fakeContainer{}, images: map[string]string{}, imageCfg: map[string]map[string]any{}, repoDigests: map[string][]string{}, registry: map[string]string{}, crash: map[string]bool{}, exits: map[string]bool{}}
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
			Image       string
			Env         []string
			Labels      map[string]string
			Hostname    string
			Cmd         []string
			Healthcheck map[string]any
			HostConfig  struct {
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
		c := &fakeContainer{ID: fmt.Sprintf("%064x", f.nextID+0xdef000), Name: name, Image: body.Image, ImageID: f.imageID(body.Image), State: "created", Env: body.Env, Labels: body.Labels, Hostname: body.Hostname, Cmd: body.Cmd, Healthcheck: body.Healthcheck}
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
			if f.crash[c.Image] {
				c.State, c.RestartCount = "restarting", c.RestartCount+1
			}
			if f.exits[c.Image] {
				c.State = "exited"
			}
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
		if f.pullAuth == nil {
			f.pullAuth = map[string]string{}
		}
		f.pullAuth[ref] = r.Header.Get("X-Registry-Auth")
		if strings.Contains(ref, "missing") {
			enc.Encode(map[string]any{"errorDetail": map[string]string{"message": "manifest unknown"}, "error": "manifest unknown"})
			return
		}
		// A private image pulls only with a login, as a registry with
		// htpasswd answers.
		if strings.Contains(ref, "private") && r.Header.Get("X-Registry-Auth") == "" {
			jsonOut(w, 500, map[string]string{"message": "Head \"https://" + ref + "\": no basic auth credentials"})
			return
		}
		enc.Encode(map[string]any{"status": "Pulling from library/" + ref, "id": "latest"})
		enc.Encode(map[string]any{"status": "Downloading", "id": "abc", "progressDetail": map[string]int{"current": 5, "total": 10}})
		enc.Encode(map[string]any{"status": "Status: Downloaded newer image for " + ref})
		f.images[strings.TrimPrefix(strings.TrimPrefix(ref, "docker.io/library/"), "docker.io/")] = "sha256:" + strings.Repeat("2", 64)
	case len(parts) >= 3 && parts[0] == "images" && parts[len(parts)-1] == "json":
		ref := strings.Join(parts[1:len(parts)-1], "/")
		id, ok := f.images[ref]
		_, hasCfg := f.imageCfg[ref]
		_, hasDigests := f.repoDigests[ref]
		if !ok && (hasCfg || hasDigests) {
			id, ok = ref, true // by ID, as a container's Image names it
		}
		if !ok {
			jsonOut(w, 404, map[string]string{"message": "No such image: " + ref})
			return
		}
		out := map[string]any{"Id": id, "Config": f.imageCfg[id], "RepoDigests": f.repoDigests[id]}
		if d := f.descriptors[id]; d != "" {
			out["Descriptor"] = map[string]any{"mediaType": "application/vnd.oci.image.index.v1+json", "digest": d, "size": 1}
		}
		jsonOut(w, 200, out)
	case strings.HasPrefix(path, "/distribution/") && strings.HasSuffix(path, "/json"):
		ref := strings.TrimSuffix(strings.TrimPrefix(path, "/distribution/"), "/json")
		f.inspected = append(f.inspected, ref)
		d := f.registry[ref]
		if login, ok := strings.CutPrefix(d, "login:"); ok {
			// A private tag: its digest when the lookup carries a login.
			d = "401"
			if r.Header.Get("X-Registry-Auth") != "" {
				d = login
			}
		}
		switch d {
		case "":
			jsonOut(w, 404, map[string]string{"message": "manifest unknown"})
		case "401":
			jsonOut(w, 401, map[string]string{"message": "unauthorized: authentication required"})
		default:
			jsonOut(w, 200, map[string]any{"Descriptor": map[string]any{"mediaType": "application/vnd.oci.image.index.v1+json", "digest": d, "size": 1}, "Platforms": []any{}})
		}
	case path == "/events":
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.Encode(map[string]any{"Type": "container", "Action": "start", "Actor": map[string]any{"ID": "abc", "Attributes": map[string]string{"name": "sonarr", "image": "sonarr", "secret.label": "hunter2"}}, "time": 1})
		enc.Encode(map[string]any{"Type": "container", "Action": "die", "Actor": map[string]any{"ID": "abc"}, "time": 2})
	default:
		jsonOut(w, 404, map[string]string{"message": "page not found"})
	}
}

// The fake registry's one login (fakeRegistry, and a pull of a "private"
// image).
const (
	fakeUser     = "robot"
	fakePassword = "fake-pw-not-real-0000"
)

// fakeRegistry serves the registry API for every domain under
// <url>/<domain>, answering HEADs from f.registry: a digest, "401" (a
// token is refused: a login) or "" (no such tag). docker.io wants an
// anonymous token first, as Docker Hub does. unreachable is a domain whose
// registry the node can't reach directly, so Docker is asked instead.
// It returns the function the module's registry field takes.
func (f *fakeDocker) fakeRegistry(t *testing.T, unreachable string) func(string) string {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.URL.Path == "/token" {
			if r.Header.Get("Authorization") == "Basic "+base64.StdEncoding.EncodeToString([]byte(fakeUser+":"+fakePassword)) {
				jsonOut(w, 200, map[string]string{"token": "robot"})
				return
			}
			if r.URL.Query().Get("scope") == "" || strings.Contains(r.URL.Query().Get("scope"), "private") {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			jsonOut(w, 200, map[string]string{"token": "anon"})
			return
		}
		domain, rest, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
		repo, tag, ok := strings.Cut(strings.TrimPrefix(rest, "v2/"), "/manifests/")
		if r.Method != http.MethodHead || !ok {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		ref := domain + "/" + repo + ":" + tag
		authed := r.Header.Get("Authorization") == "Bearer anon" || r.Header.Get("Authorization") == "Bearer robot"
		challenge := `Bearer realm="` + srv.URL + `/token",service="registry.example",scope="repository:` + repo + `:pull"`
		if domain == "docker.io" && !authed {
			w.Header().Set("WWW-Authenticate", challenge)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.asked = append(f.asked, ref)
		f.headers = append(f.headers, r.Header.Clone())
		d := f.registry[ref]
		if login, ok := strings.CutPrefix(d, "login:"); ok {
			// A private tag: its digest for the robot's token only.
			d = "401"
			if r.Header.Get("Authorization") == "Bearer robot" {
				d = login
			}
		}
		switch d {
		case "":
			w.WriteHeader(http.StatusNotFound)
		case "401":
			w.Header().Set("WWW-Authenticate", strings.Replace(challenge, repo, "private/"+repo, 1))
			w.WriteHeader(http.StatusUnauthorized)
		case "429":
			w.WriteHeader(http.StatusTooManyRequests)
		default:
			w.Header().Set("Docker-Content-Digest", d)
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(srv.Close)
	return func(domain string) string {
		if domain == unreachable {
			return "http://127.0.0.1:1"
		}
		return srv.URL + "/" + domain
	}
}
