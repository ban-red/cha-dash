package liveverify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// Recorded is a recording's shapes, by route: the request's path with its
// variable parts as placeholders ("/nodes/{}/qemu/{}/config").
type Recorded map[string]Shape

// Recording is one instance's recorded answers, and the version they're of.
type Recording struct {
	Routes  Recorded
	Version string
}

// Routes knows how to name an integration's routes and where its
// recordings are. An integration with none has no comparison yet.
type Routes struct {
	// Route turns a request path (no query) into its route; "" skips it.
	Route func(path string) string
	// Load reads the recording for the instance with this name (the
	// part of the label after the slash, or "").
	Load func(root, name string) (Recording, error)
}

// ByIntegration lists the integrations with recordings to compare.
var ByIntegration = map[string]Routes{
	"proxmox": {Route: pveRoute, Load: loadPVE},
	"docker":  {Route: dockerRoute, Load: loadDocker},
}

// pveVar are path segments whose next segment is a name or an ID.
var pveVar = map[string]bool{"nodes": true, "qemu": true, "lxc": true, "storage": true, "tasks": true, "users": true, "token": true}

func pveRoute(path string) string {
	path = strings.TrimPrefix(path, "/api2/json")
	segs := strings.Split(strings.Trim(path, "/"), "/")
	for i := 1; i < len(segs); i++ {
		if pveVar[segs[i-1]] && segs[i] != "" {
			segs[i] = "{}"
		}
	}
	return "/" + strings.Join(segs, "/")
}

// loadPVE reads testdata/integrations/proxmox/<name>/_index.json: every
// request, with the file that holds its answer.
func loadPVE(root, name string) (Recording, error) {
	dir := filepath.Join(root, "proxmox", name)
	b, err := os.ReadFile(filepath.Join(dir, "_index.json"))
	if err != nil {
		return Recording{}, err
	}
	var idx struct {
		Requests []struct {
			Path   string `json:"path"`
			File   string `json:"file"`
			Status int    `json:"status"`
		} `json:"requests"`
	}
	if err := json.Unmarshal(b, &idx); err != nil {
		return Recording{}, fmt.Errorf("%s: %w", name, err)
	}
	rec := Recorded{}
	for _, r := range idx.Requests {
		if r.Status != http.StatusOK {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, r.File))
		if err != nil {
			return Recording{}, err
		}
		p, _, _ := strings.Cut(r.Path, "?")
		rec.add(pveRoute(p), body)
	}
	return Recording{Routes: rec, Version: versionIn(filepath.Join(dir, "version.json"), "data", "version")}, nil
}

// versionIn reads a version string from a recorded JSON answer, by keys.
func versionIn(file string, keys ...string) string {
	b, err := os.ReadFile(file)
	if err != nil {
		return ""
	}
	var v any
	if json.Unmarshal(b, &v) != nil {
		return ""
	}
	for _, k := range keys {
		m, _ := v.(map[string]any)
		v = m[k]
	}
	s, _ := v.(string)
	return s
}

var (
	dockerVersionRE = regexp.MustCompile(`^/v[0-9.]+/`)
	hexIDRE         = regexp.MustCompile(`^[0-9a-f]{12,64}$`)
)

func dockerRoute(path string) string {
	path = dockerVersionRE.ReplaceAllString(path, "/")
	segs := strings.Split(strings.Trim(path, "/"), "/")
	for i := 1; i < len(segs); i++ {
		if segs[i-1] == "containers" && segs[i] != "json" || hexIDRE.MatchString(segs[i]) {
			segs[i] = "{}"
		}
	}
	return "/" + strings.Join(segs, "/")
}

// loadDocker reads testdata/integrations/docker/<name>: the version, the
// container list, and one inspect per container.
func loadDocker(root, name string) (Recording, error) {
	dir := filepath.Join(root, "docker", name)
	rec := Recorded{}
	for file, route := range map[string]string{"version.json": "/version", "containers.json": "/containers/json"} {
		b, err := os.ReadFile(filepath.Join(dir, file))
		if err != nil {
			return Recording{}, err
		}
		rec.add(route, b)
	}
	inspects, _ := filepath.Glob(filepath.Join(dir, "inspect", "*.json"))
	for _, f := range inspects {
		b, err := os.ReadFile(f)
		if err != nil {
			return Recording{}, err
		}
		rec.add("/containers/{}/json", b)
	}
	return Recording{Routes: rec, Version: versionIn(filepath.Join(dir, "version.json"), "Version")}, nil
}

func (r Recorded) add(route string, body []byte) {
	s := ShapeOf(body)
	if s == nil {
		return
	}
	if r[route] == nil {
		r[route] = Shape{}
	}
	r[route].Merge(s)
}

// Capture is a transport that keeps the shape of every JSON answer to a
// GET, by route. Bodies are read whole and handed on unchanged; streams
// (event streams, chunked logs) aren't JSON documents and are passed by.
type Capture struct {
	Next  http.RoundTripper
	Route func(path string) string

	mu   sync.Mutex
	live Recorded
}

// maxCapture bounds one body read for its shape.
const maxCapture = 32 << 20

// RoundTrip implements http.RoundTripper, through Next.
func (c *Capture) RoundTrip(req *http.Request) (*http.Response, error) {
	return c.through(c.Next, req)
}

// Wrap returns a transport that records into c through next, for each of
// an instance's HTTP clients (integrationtest.Harness.Wrap).
func (c *Capture) Wrap(next http.RoundTripper) http.RoundTripper {
	return roundTripper(func(req *http.Request) (*http.Response, error) { return c.through(next, req) })
}

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func (c *Capture) through(next http.RoundTripper, req *http.Request) (*http.Response, error) {
	resp, err := next.RoundTrip(req)
	if err != nil || req.Method != http.MethodGet || resp.StatusCode != http.StatusOK ||
		!strings.Contains(resp.Header.Get("Content-Type"), "json") || resp.ContentLength > maxCapture {
		return resp, err
	}
	route := c.Route(req.URL.Path)
	if route == "" {
		return resp, nil
	}
	body, rerr := io.ReadAll(io.LimitReader(resp.Body, maxCapture+1))
	resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(body))
	if rerr != nil || len(body) > maxCapture {
		return resp, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.live == nil {
		c.live = Recorded{}
	}
	c.live.add(route, body)
	return resp, nil
}

// Result compares what was captured with rec, by route. Routes only one
// side has are counted, not compared.
type Result struct {
	Routes   map[string]Drift `json:"routes"`             // compared routes with drift
	Compared int              `json:"compared"`           // routes both sides have
	Unseen   int              `json:"unrecorded_live"`    // live routes with no recording
	Missing  int              `json:"recorded_not_asked"` // recorded routes not asked this run
}

// Against compares the capture with rec.
func (c *Capture) Against(rec Recorded) Result {
	c.mu.Lock()
	defer c.mu.Unlock()
	res := Result{Routes: map[string]Drift{}}
	for route, live := range c.live {
		want, ok := rec[route]
		if !ok {
			res.Unseen++
			continue
		}
		res.Compared++
		if d := Compare(want, live); !d.Empty() || len(d.Added) > 0 {
			res.Routes[route] = d
		}
	}
	for route := range rec {
		if _, ok := c.live[route]; !ok {
			res.Missing++
		}
	}
	return res
}
