package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
)

const shellHTML = `<!doctype html><html><head><title>Cha Dash</title><script type="module" src="/assets/index-3f9a1c.js"></script></head><body><div id="app"></div></BODY></html>`

func inlineFS() fstest.MapFS {
	fs := testFS()
	fs["index.html"] = &fstest.MapFile{Data: []byte(shellHTML)}
	return fs
}

// getWith serves target with f attached the way the server attaches it.
func getWith(t *testing.T, h http.Handler, target string, f InlineFunc, hdr map[string]string) *http.Response {
	t.Helper()
	req := httptest.NewRequest("GET", target, nil)
	if f != nil {
		req = req.WithContext(WithInline(context.Background(), f))
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Result()
}

var block = regexp.MustCompile(`<script type="application/json" id="chadash-snapshot">([^<]*)</script>`)

func TestInlineSnapshot(t *testing.T) {
	h := Handler(inlineFS())
	data, err := json.Marshal(map[string]any{"v": 1, "name": "</script><!--"}) // escapes < and >
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/", "/index.html", "/boards/home", "/settings/integrations"} {
		resp := getWith(t, h, p, func() []byte { return data }, nil)
		b := body(t, resp)
		m := block.FindStringSubmatch(b)
		if resp.StatusCode != 200 || m == nil || m[1] != string(data) {
			t.Fatalf("%s: %d, no data block in %q", p, resp.StatusCode, b)
		}
		// Just before </body>, and the rest of the page untouched.
		if want := strings.Replace(shellHTML, "</BODY>", m[0]+"</BODY>", 1); b != want {
			t.Fatalf("%s: page\n%s\nwant\n%s", p, b, want)
		}
		if got := resp.Header.Get("Cache-Control"); got != cacheNoStore {
			t.Errorf("%s: personalised page has Cache-Control %q, want no-store", p, got)
		}
		if resp.Header.Get("ETag") != "" {
			t.Errorf("%s: personalised page has an ETag", p)
		}
		if resp.Header.Get("Vary") != "Cookie" {
			t.Errorf("%s: Vary %q", p, resp.Header.Get("Vary"))
		}
		if got := resp.Header.Get("Content-Type"); got != "text/html; charset=utf-8" {
			t.Errorf("%s: Content-Type %q", p, got)
		}
	}
}

func TestInlineOnlyInIndex(t *testing.T) {
	h := Handler(inlineFS())
	called := 0
	f := func() []byte { called++; return []byte(`{"v":1}`) }
	for _, p := range []string{"/assets/index-3f9a1c.js", "/manifest.webmanifest", "/favicon.svg", "/assets/missing.js", "/api/boards"} {
		resp := getWith(t, h, p, f, nil)
		if strings.Contains(body(t, resp), SnapshotID) {
			t.Errorf("%s: carries the snapshot", p)
		}
	}
	if called != 0 {
		t.Fatalf("the snapshot was built %d times for files that aren't index.html", called)
	}
}

func TestInlineGenericShell(t *testing.T) {
	h := Handler(inlineFS())
	for name, f := range map[string]InlineFunc{
		"no inline func":          nil,
		"nothing for this caller": func() []byte { return nil },
		"unsafe JSON":             func() []byte { return []byte(`{"x":"</script><script>alert(1)</script>"}`) },
	} {
		resp := getWith(t, h, "/", f, nil)
		b := body(t, resp)
		if b != shellHTML {
			t.Errorf("%s: page %q, want the generic shell", name, b)
		}
		if resp.Header.Get("Cache-Control") != cacheRevalidate || resp.Header.Get("ETag") == "" {
			t.Errorf("%s: generic shell headers %v", name, resp.Header)
		}
	}
}

func TestInlineIgnoresValidators(t *testing.T) {
	h := Handler(inlineFS())
	et := getWith(t, h, "/", nil, nil).Header.Get("ETag")
	if again := getWith(t, h, "/", nil, map[string]string{"If-None-Match": et}); again.StatusCode != http.StatusNotModified {
		t.Fatalf("generic shell: %d, want 304", again.StatusCode)
	}
	// Signed in now: the cached generic page must not satisfy the request.
	resp := getWith(t, h, "/", func() []byte { return []byte(`{"v":1}`) }, map[string]string{"If-None-Match": et})
	if resp.StatusCode != 200 || !strings.Contains(body(t, resp), SnapshotID) {
		t.Fatalf("personalised page behind a stale validator: %d", resp.StatusCode)
	}
}

func TestDataBlockPlacement(t *testing.T) {
	data := []byte(`{}`)
	for page, want := range map[string]string{
		`<body><p>x</p></body></html>`: `<body><p>x</p><script type="application/json" id="chadash-snapshot">{}</script></body></html>`,
		`<p>no body tag</p>`:           `<p>no body tag</p><script type="application/json" id="chadash-snapshot">{}</script>`,
	} {
		if got := string(withDataBlock([]byte(page), data)); got != want {
			t.Errorf("%q: got %q, want %q", page, got, want)
		}
	}
}
