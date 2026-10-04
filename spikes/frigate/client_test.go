package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClientIsReadOnly(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("\xff\xd8\xff fake jpeg bytes"))
	}))
	defer srv.Close()
	c, err := NewClient(srv.URL, 0, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{"POST", "PUT", "PATCH", "DELETE"} {
		if r := c.Do(m, "/api/restart", 1024); r.Status != 0 || !strings.Contains(r.Error, "read-only") {
			t.Errorf("%s not refused: %+v", m, r)
		}
	}
	if hits != 0 {
		t.Fatalf("%d non-GET requests reached the server", hits)
	}
	// Media is measured, never kept.
	r := c.Do("GET", "/api/cam/latest.jpg", 1<<20)
	if r.Body != nil || r.Bytes == 0 {
		t.Fatalf("media body kept or not counted: %+v", r)
	}
	m := c.Measure("GET", "/api/cam/latest.jpg", 1<<20)
	if m.Body != nil || m.Bytes == 0 || m.ContentType != "image/jpeg" {
		t.Fatalf("measure: %+v", m)
	}
}

func TestScrubRemovesImagesAndCredentials(t *testing.T) {
	sec := &secrets{}
	jpeg := "/9j/" + strings.Repeat("A", 400)
	in := `{"thumbnail":"` + jpeg + `","path":"rtsp://viewer:s3cr%40t@192.0.2.9:554/stream1","mqtt":{"password":"hunter22"},"other":"ok"}`
	out := string(scrubImages([]byte(in), sec))
	for _, bad := range []string{jpeg[:20], "viewer", "s3cr", "hunter22"} {
		if strings.Contains(out, bad) {
			t.Errorf("scrubbed output still contains %q: %s", bad, out)
		}
	}
	if !strings.Contains(out, `"other": "ok"`) {
		t.Errorf("benign field lost: %s", out)
	}
	for _, want := range []string{"viewer", "s3cr@t", "hunter22"} {
		if _, ok := sec.v[want]; !ok {
			t.Errorf("secret %q not collected for the leak check", want)
		}
	}
}
