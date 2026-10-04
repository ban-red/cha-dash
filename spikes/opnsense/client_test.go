package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The router is production: anything but GET/HEAD must never leave the box.
func TestClientIsReadOnly(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			t.Errorf("server saw %s", r.Method)
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()
	c, err := NewClient(srv.URL, "k", "s", "", 0, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{"POST", "PUT", "PATCH", "DELETE", "OPTIONS"} {
		r := c.Do(m, "/api/core/firmware/reboot", 1024)
		if r.Status != 0 || !strings.Contains(r.Error, "read-only") {
			t.Errorf("%s: status %d err %q, want refusal", m, r.Status, r.Error)
		}
	}
	if hits != 0 {
		t.Fatalf("%d non-GET requests reached the server", hits)
	}
	if r := c.Do("GET", "/api/x", 1024); r.Status != 200 {
		t.Fatalf("GET: %+v", r)
	}
	if r := c.Do("HEAD", "/api/x", 1024); r.Status != 200 {
		t.Fatalf("HEAD: %+v", r)
	}
}

func TestTransportRefusesOtherOrigins(t *testing.T) {
	tr := &readOnlyTransport{base: http.DefaultTransport, scheme: "http", host: "router.lab.example"}
	req, _ := http.NewRequest("GET", "http://elsewhere.lab.example/api", nil)
	if _, err := tr.RoundTrip(req); err == nil || errors.Is(err, errNotReadOnly) {
		t.Fatalf("want origin refusal, got %v", err)
	}
}

func TestScrubRemovesHost(t *testing.T) {
	c, _ := NewClient("http://192.0.2.7:8080", "", "", "", 0, time.Second)
	got := c.scrub(`Get "http://192.0.2.7:8080/api/x": dial tcp 192.0.2.7:8080: connect: refused`)
	if strings.Contains(got, "192.0.2.7") {
		t.Fatalf("scrub left the host: %s", got)
	}
}
