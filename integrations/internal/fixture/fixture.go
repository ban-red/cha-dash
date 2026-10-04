// Package fixture serves recorded (or synthetic) API responses from
// testdata/integrations/<id>/ for the integrations' contract tests.
package fixture

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Server is an httptest server answering "METHOD /path" from fixture files.
type Server struct {
	*httptest.Server
	dir string

	mu       sync.Mutex
	routes   map[string]Route
	requests []*http.Request
}

// Route is one answer: a file under the fixture directory (or Body), with a
// status (default 200) and a content type (default application/json).
type Route struct {
	File        string
	Body        string
	Status      int
	ContentType string
	Header      http.Header
}

// Dir is testdata/integrations/<id>, found from the test's working directory
// (integrations/<id>).
func Dir(t testing.TB, id string) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "testdata", "integrations", id))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("fixture directory: %v", err)
	}
	return dir
}

// New starts a server for the fixtures in dir. auth decides whether a
// request may pass (nil: all do); one that may not gets 401. Unknown routes
// get 404.
func New(t testing.TB, dir string, auth func(*http.Request) bool, routes map[string]Route) *Server {
	t.Helper()
	s := &Server{dir: dir, routes: map[string]Route{}}
	for k, v := range routes {
		s.routes[k] = v
	}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm() // so recorded POSTs keep their form
		s.mu.Lock()
		s.requests = append(s.requests, r.Clone(r.Context()))
		route, ok := s.routes[r.Method+" "+r.URL.Path]
		for k := range r.URL.Query() { // "GET /path?param" wins when param is present
			if qr, qok := s.routes[r.Method+" "+r.URL.Path+"?"+k]; qok {
				route, ok = qr, true
			}
		}
		s.mu.Unlock()
		if auth != nil && !auth(r) {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		if !ok {
			http.NotFound(w, r)
			return
		}
		body := []byte(route.Body)
		if route.File != "" {
			b, err := os.ReadFile(filepath.Join(s.dir, route.File))
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			body = b
		}
		for k, vs := range route.Header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		ct := route.ContentType
		if ct == "" {
			ct = "application/json; charset=utf-8"
		}
		w.Header().Set("Content-Type", ct)
		if route.Status != 0 {
			w.WriteHeader(route.Status)
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(s.Close)
	return s
}

// Set replaces or adds a route.
func (s *Server) Set(key string, r Route) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.routes[key] = r
}

// Requests returns the requests seen so far whose path ends with suffix ("" for all).
func (s *Server) Requests(suffix string) []*http.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*http.Request
	for _, r := range s.requests {
		if strings.HasSuffix(r.URL.Path, suffix) {
			out = append(out, r)
		}
	}
	return out
}

// Reset forgets the requests seen.
func (s *Server) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = nil
}
