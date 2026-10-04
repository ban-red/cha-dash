package webui

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
)

const indexHTML = `<!doctype html><title>Cha Dash</title><script type="module" src="/assets/index-3f9a1c.js"></script>`

func testFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html":                 {Data: []byte(indexHTML)},
		"assets/index-3f9a1c.js":     {Data: []byte("console.log(1)")},
		"assets/index-77b0e2.css":    {Data: []byte("body{}")},
		"assets/inter-4c1d.woff2":    {Data: []byte("wOF2")},
		"assets/logo-9e8d.svg":       {Data: []byte("<svg/>")},
		"manifest.webmanifest":       {Data: []byte(`{"name":"Cha Dash"}`)},
		"favicon.svg":                {Data: []byte("<svg/>")},
		"icons/apple-touch-icon.png": {Data: []byte("\x89PNG")},
	}
}

func get(t *testing.T, h http.Handler, method, target string, hdr map[string]string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Result()
}

func body(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestFilesAndTypes(t *testing.T) {
	h := Handler(testFS())
	for _, tc := range []struct {
		path, ctype, cache, body string
	}{
		{"/assets/index-3f9a1c.js", "text/javascript; charset=utf-8", cacheImmutable, "console.log(1)"},
		{"/assets/index-77b0e2.css", "text/css; charset=utf-8", cacheImmutable, "body{}"},
		{"/assets/inter-4c1d.woff2", "font/woff2", cacheImmutable, "wOF2"},
		{"/assets/logo-9e8d.svg", "image/svg+xml", cacheImmutable, "<svg/>"},
		{"/manifest.webmanifest", "application/manifest+json", cacheRevalidate, `{"name":"Cha Dash"}`},
		{"/favicon.svg", "image/svg+xml", cacheRevalidate, "<svg/>"},
		{"/icons/apple-touch-icon.png", "image/png", cacheRevalidate, "\x89PNG"},
		{"/index.html", "text/html; charset=utf-8", cacheRevalidate, indexHTML},
	} {
		resp := get(t, h, "GET", tc.path, nil)
		if resp.StatusCode != 200 {
			t.Errorf("%s: status %d", tc.path, resp.StatusCode)
			continue
		}
		if got := resp.Header.Get("Content-Type"); got != tc.ctype {
			t.Errorf("%s: Content-Type %q, want %q", tc.path, got, tc.ctype)
		}
		if got := resp.Header.Get("Cache-Control"); got != tc.cache {
			t.Errorf("%s: Cache-Control %q, want %q", tc.path, got, tc.cache)
		}
		if got := body(t, resp); got != tc.body {
			t.Errorf("%s: body %q", tc.path, got)
		}
		if resp.Header.Get("ETag") == "" {
			t.Errorf("%s: no ETag", tc.path)
		}
	}
}

func TestHistoryFallback(t *testing.T) {
	h := Handler(testFS())
	for _, p := range []string{"/", "/boards/home", "/settings/integrations?tab=all", "/icons/", "/icons", "/nope.png", "/a/../b"} {
		resp := get(t, h, "GET", p, nil)
		if resp.StatusCode != 200 || body(t, resp) != indexHTML {
			t.Errorf("%s: want index.html, got %d", p, resp.StatusCode)
			continue
		}
		if got := resp.Header.Get("Cache-Control"); got != cacheRevalidate {
			t.Errorf("%s: index.html must be revalidated, got Cache-Control %q", p, got)
		}
		if got := resp.Header.Get("Content-Type"); got != "text/html; charset=utf-8" {
			t.Errorf("%s: Content-Type %q", p, got)
		}
	}
	// HEAD gets the headers without the body.
	resp := get(t, h, "HEAD", "/boards/home", nil)
	if resp.StatusCode != 200 || body(t, resp) != "" || resp.Header.Get("Content-Length") != strconv.Itoa(len(indexHTML)) {
		t.Errorf("HEAD: %d, length %q", resp.StatusCode, resp.Header.Get("Content-Length"))
	}
}

func TestNoFallback(t *testing.T) {
	h := Handler(testFS())
	for _, p := range []string{
		"/api", "/api/boards", "/ws", "/ws/x", // never the app
		"/assets/index-0000.js", "/assets/", "/assets", // stale chunks and the directory itself
	} {
		resp := get(t, h, "GET", p, nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", p, resp.StatusCode)
		}
		if strings.Contains(body(t, resp), "<title>Cha Dash") {
			t.Errorf("%s: served index.html", p)
		}
	}
	resp := get(t, h, "POST", "/", nil)
	if resp.StatusCode != http.StatusMethodNotAllowed || resp.Header.Get("Allow") != "GET, HEAD" {
		t.Errorf("POST: %d %q", resp.StatusCode, resp.Header.Get("Allow"))
	}
}

func TestRevalidation(t *testing.T) {
	h := Handler(testFS())
	first := get(t, h, "GET", "/", nil)
	et := first.Header.Get("ETag")
	if et == "" {
		t.Fatal("no ETag on index.html")
	}
	if again := get(t, h, "GET", "/boards/home", map[string]string{"If-None-Match": et}); again.StatusCode != http.StatusNotModified {
		t.Fatalf("If-None-Match: %d, want 304", again.StatusCode)
	}
	other := get(t, h, "GET", "/favicon.svg", nil).Header.Get("ETag")
	if other == et {
		t.Fatal("different files share an ETag")
	}
}

func TestNoIndex(t *testing.T) {
	h := Handler(fstest.MapFS{"assets/a.js": {Data: []byte("1")}})
	if resp := get(t, h, "GET", "/", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("no index.html: %d", resp.StatusCode)
	}
	if resp := get(t, h, "GET", "/assets/a.js", nil); resp.StatusCode != 200 {
		t.Fatalf("asset: %d", resp.StatusCode)
	}
}

func TestBuiltInFiles(t *testing.T) {
	resp := get(t, New(), "GET", "/some/route", nil)
	b := body(t, resp)
	if resp.StatusCode != 200 || !strings.Contains(b, "Cha Dash") {
		t.Fatalf("built-in app: %d %q", resp.StatusCode, b)
	}
	if !Embedded && !strings.Contains(b, "-tags webui") {
		t.Fatalf("the placeholder should say how to build the app in: %q", b)
	}
	if !Embedded && (strings.Contains(b, "style=") || strings.Contains(b, "<style") || strings.Contains(b, "<script")) {
		t.Fatal("the placeholder must not need inline styles or scripts (CSP)")
	}
}

func TestContentTypeFallback(t *testing.T) {
	if got := contentType("x.unknownext"); got != "application/octet-stream" {
		t.Fatalf("unknown extension: %q", got)
	}
	if got := contentType("A.JS"); got != "text/javascript; charset=utf-8" {
		t.Fatalf("upper-case extension: %q", got)
	}
}
