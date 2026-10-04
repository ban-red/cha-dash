package labkit

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestReadOnlyClientRefusesWrites(t *testing.T) {
	hit := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	c := NewReadOnlyClient(nil, nil, 0, 5*time.Second)

	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch, http.MethodOptions, http.MethodConnect, "PROPFIND"} {
		req, _ := http.NewRequest(m, srv.URL+"/x", nil)
		if _, err := c.Do(req); !errors.Is(err, ErrRefused) {
			t.Errorf("%s: err = %v, want ErrRefused", m, err)
		}
	}
	// A GET with a body is refused too.
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/x", strings.NewReader("{}"))
	if _, err := c.Do(req); !errors.Is(err, ErrRefused) {
		t.Errorf("GET with body: err = %v, want ErrRefused", err)
	}
	if hit != 0 {
		t.Fatalf("server was hit %d times by refused requests", hit)
	}
	for _, m := range []string{http.MethodGet, http.MethodHead} {
		req, _ := http.NewRequest(m, srv.URL+"/x", nil)
		resp, err := c.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", m, err)
		}
		resp.Body.Close()
	}
	if hit != 2 {
		t.Fatalf("GET and HEAD: server hit %d times, want 2", hit)
	}
}

func TestReadOnlyClientGuardAndRedirects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "http://198.51.100.1/elsewhere", http.StatusFound)
			return
		}
		t.Errorf("unexpected request to %s", r.URL.Path)
	}))
	defer srv.Close()
	guard := func(r *http.Request) error {
		if strings.Contains(r.URL.Path, "/exec") {
			return errors.New("exec is never called")
		}
		return nil
	}
	c := NewReadOnlyClient(nil, guard, 0, 5*time.Second)
	if _, err := c.Get(srv.URL + "/containers/abc/exec"); !errors.Is(err, ErrRefused) {
		t.Errorf("guarded path: err = %v, want ErrRefused", err)
	}
	resp, err := c.Get(srv.URL + "/redirect")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Errorf("redirect was followed: status %d", resp.StatusCode)
	}
}
