package webui

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"io/fs"
	"math/rand/v2"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
)

// Big enough to be compressed (httpx.MinGzipBytes), and compressible.
var (
	bigJS   = strings.Repeat("export const tile=(a,b)=>a.size===b.size?a.id.localeCompare(b.id):a.size-b.size;\n", 40)
	bigCSS  = strings.Repeat(".tile{display:grid;gap:var(--space-2);color:var(--text-1)}\n", 40)
	bigSVG  = `<svg xmlns="http://www.w3.org/2000/svg">` + strings.Repeat(`<path d="M0 0h24v24H0z" fill="none"/>`, 40) + `</svg>`
	bigFont = strings.Repeat("wOF2", 600) // a big file of a type that isn't compressed
	bigHTML = `<!doctype html><html><head><title>Cha Dash</title>` + strings.Repeat(`<link rel="modulepreload" href="/assets/chunk-0a1b2c3d.js">`, 30) + `</head><body><div id="app"></div></body></html>`
)

func gzipFS() fstest.MapFS {
	fs := testFS()
	fs["index.html"] = &fstest.MapFile{Data: []byte(bigHTML)}
	fs["assets/app-1a2b.js"] = &fstest.MapFile{Data: []byte(bigJS)}
	fs["assets/app-1a2b.css"] = &fstest.MapFile{Data: []byte(bigCSS)}
	fs["icons/big.svg"] = &fstest.MapFile{Data: []byte(bigSVG)}
	fs["assets/font-9f8e.woff2"] = &fstest.MapFile{Data: []byte(bigFont)}
	fs["sw.js"] = &fstest.MapFile{Data: []byte(bigJS)}
	return fs
}

var gz = map[string]string{"Accept-Encoding": "gzip, deflate, br, zstd"}

func with(h map[string]string, kv ...string) map[string]string {
	out := map[string]string{}
	for k, v := range h {
		out[k] = v
	}
	for i := 0; i+1 < len(kv); i += 2 {
		out[kv[i]] = kv[i+1]
	}
	return out
}

func gunzip(t *testing.T, b string) string {
	t.Helper()
	zr, err := gzip.NewReader(strings.NewReader(b))
	if err != nil {
		t.Fatalf("not gzip: %v", err)
	}
	out, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func varies(resp *http.Response, token string) bool {
	return slices.Contains(resp.Header.Values("Vary"), token)
}

func TestGzipNegotiation(t *testing.T) {
	h := Handler(gzipFS())
	for _, tc := range []struct{ path, body, ctype, cache string }{
		{"/assets/app-1a2b.js", bigJS, "text/javascript; charset=utf-8", cacheImmutable},
		{"/assets/app-1a2b.css", bigCSS, "text/css; charset=utf-8", cacheImmutable},
		{"/icons/big.svg", bigSVG, "image/svg+xml", cacheRevalidate},
		{"/sw.js", bigJS, "text/javascript; charset=utf-8", cacheRevalidate},
		{"/", bigHTML, "text/html; charset=utf-8", cacheRevalidate},
		{"/boards/home", bigHTML, "text/html; charset=utf-8", cacheRevalidate},
	} {
		plain := get(t, h, "GET", tc.path, nil)
		zipped := get(t, h, "GET", tc.path, gz)
		pb, zb := body(t, plain), body(t, zipped)

		if plain.Header.Get("Content-Encoding") != "" || pb != tc.body {
			t.Errorf("%s without gzip: encoding %q", tc.path, plain.Header.Get("Content-Encoding"))
		}
		if zipped.Header.Get("Content-Encoding") != "gzip" || gunzip(t, zb) != tc.body || len(zb) >= len(pb) {
			t.Errorf("%s with gzip: encoding %q, %d bytes from %d", tc.path, zipped.Header.Get("Content-Encoding"), len(zb), len(pb))
		}
		if got := zipped.Header.Get("Content-Length"); got != strconv.Itoa(len(zb)) {
			t.Errorf("%s with gzip: Content-Length %s, body %d", tc.path, got, len(zb))
		}
		for _, resp := range []*http.Response{plain, zipped} {
			if !varies(resp, "Accept-Encoding") {
				t.Errorf("%s: Vary %q", tc.path, resp.Header.Values("Vary"))
			}
			if resp.Header.Get("Content-Type") != tc.ctype || resp.Header.Get("Cache-Control") != tc.cache {
				t.Errorf("%s: Content-Type %q, Cache-Control %q", tc.path, resp.Header.Get("Content-Type"), resp.Header.Get("Cache-Control"))
			}
		}
		pe, ze := plain.Header.Get("ETag"), zipped.Header.Get("ETag")
		if pe == "" || ze != strings.TrimSuffix(pe, `"`)+`-gz"` {
			t.Errorf("%s: ETags %s and %s", tc.path, pe, ze)
		}
	}

	// gzip refused, or not offered.
	for _, ae := range []string{"gzip;q=0", "br, zstd", "identity", "*;q=0"} {
		resp := get(t, h, "GET", "/assets/app-1a2b.js", map[string]string{"Accept-Encoding": ae})
		if resp.Header.Get("Content-Encoding") != "" || body(t, resp) != bigJS {
			t.Errorf("Accept-Encoding %q: got %q", ae, resp.Header.Get("Content-Encoding"))
		}
	}
}

func TestGzipOnlyWhereItHelps(t *testing.T) {
	h := Handler(gzipFS())
	for _, p := range []string{
		"/assets/font-9f8e.woff2",     // compressed already
		"/icons/apple-touch-icon.png", // likewise
		"/assets/index-3f9a1c.js",     // under the minimum
		"/manifest.webmanifest",       // likewise
	} {
		resp := get(t, h, "GET", p, gz)
		if resp.Header.Get("Content-Encoding") != "" || varies(resp, "Accept-Encoding") {
			t.Errorf("%s: encoding %q, Vary %q", p, resp.Header.Get("Content-Encoding"), resp.Header.Values("Vary"))
		}
	}
	// A file that gzip can't shrink is served as it is.
	noise := make([]byte, 4096)
	_, _ = rand.NewChaCha8([32]byte{1}).Read(noise)
	h = Handler(fstest.MapFS{"index.html": {Data: []byte(indexHTML)}, "assets/noise.js": {Data: noise}})
	if resp := get(t, h, "GET", "/assets/noise.js", gz); resp.Header.Get("Content-Encoding") != "" || body(t, resp) != string(noise) {
		t.Errorf("an incompressible file was gzipped anyway: %q", resp.Header.Get("Content-Encoding"))
	}
}

func TestGzipRevalidation(t *testing.T) {
	h := Handler(gzipFS())
	const p = "/assets/app-1a2b.js"
	plainTag := get(t, h, "GET", p, nil).Header.Get("ETag")
	gzipTag := get(t, h, "GET", p, gz).Header.Get("ETag")

	for _, tc := range []struct {
		name string
		hdr  map[string]string
		code int
		etag string
	}{
		{"gzip copy, gzip validator", with(gz, "If-None-Match", gzipTag), http.StatusNotModified, gzipTag},
		{"identity, identity validator", map[string]string{"If-None-Match": plainTag}, http.StatusNotModified, plainTag},
		{"gzip copy, identity validator", with(gz, "If-None-Match", plainTag), http.StatusOK, gzipTag},
		{"identity, gzip validator", map[string]string{"If-None-Match": gzipTag}, http.StatusOK, plainTag},
		{"either validator listed", with(gz, "If-None-Match", plainTag+", "+gzipTag), http.StatusNotModified, gzipTag},
		{"weak comparison", with(gz, "If-None-Match", "W/"+gzipTag), http.StatusNotModified, gzipTag},
	} {
		resp := get(t, h, "GET", p, tc.hdr)
		if resp.StatusCode != tc.code || resp.Header.Get("ETag") != tc.etag {
			t.Errorf("%s: %d ETag %s, want %d %s", tc.name, resp.StatusCode, resp.Header.Get("ETag"), tc.code, tc.etag)
		}
		if !varies(resp, "Accept-Encoding") {
			t.Errorf("%s: Vary %q", tc.name, resp.Header.Values("Vary"))
		}
		if resp.StatusCode == http.StatusNotModified && (body(t, resp) != "" || resp.Header.Get("Content-Encoding") != "") {
			t.Errorf("%s: a 304 with a body or an encoding", tc.name)
		}
	}
}

func TestGzipRange(t *testing.T) {
	h := Handler(gzipFS())
	const p = "/assets/app-1a2b.js"
	// A range is always of the identity bytes, gzip offered or not.
	for _, hdr := range []map[string]string{with(gz, "Range", "bytes=10-19"), {"Range": "bytes=10-19"}} {
		resp := get(t, h, "GET", p, hdr)
		if resp.StatusCode != http.StatusPartialContent || resp.Header.Get("Content-Encoding") != "" {
			t.Fatalf("%v: %d, encoding %q", hdr, resp.StatusCode, resp.Header.Get("Content-Encoding"))
		}
		if b := body(t, resp); b != bigJS[10:20] {
			t.Fatalf("%v: range body %q", hdr, b)
		}
		if got := resp.Header.Get("Content-Range"); got != "bytes 10-19/"+strconv.Itoa(len(bigJS)) {
			t.Fatalf("%v: Content-Range %q", hdr, got)
		}
		if !varies(resp, "Accept-Encoding") || resp.Header.Get("ETag") == "" || strings.HasSuffix(resp.Header.Get("ETag"), `-gz"`) {
			t.Fatalf("%v: headers %v", hdr, resp.Header)
		}
	}
	// If-Range with the gzip validator doesn't match the identity bytes: the
	// whole file comes back, not a slice of the wrong representation.
	gzipTag := get(t, h, "GET", p, gz).Header.Get("ETag")
	resp := get(t, h, "GET", p, with(gz, "Range", "bytes=10-19", "If-Range", gzipTag))
	if resp.StatusCode != http.StatusOK || body(t, resp) != bigJS {
		t.Fatalf("If-Range with the gzip ETag: %d", resp.StatusCode)
	}
	// A range past the end is still refused properly.
	resp = get(t, h, "GET", p, with(gz, "Range", "bytes=99999-"))
	if resp.StatusCode != http.StatusRequestedRangeNotSatisfiable || resp.Header.Get("Content-Encoding") != "" {
		t.Fatalf("unsatisfiable range: %d %q", resp.StatusCode, resp.Header.Get("Content-Encoding"))
	}
}

func TestGzipHead(t *testing.T) {
	h := Handler(gzipFS())
	full := get(t, h, "GET", "/assets/app-1a2b.js", gz)
	n := len(body(t, full))
	resp := get(t, h, "HEAD", "/assets/app-1a2b.js", gz)
	if resp.StatusCode != 200 || body(t, resp) != "" || resp.Header.Get("Content-Encoding") != "gzip" || resp.Header.Get("Content-Length") != strconv.Itoa(n) {
		t.Fatalf("HEAD: %d, %q, length %q (GET sent %d)", resp.StatusCode, resp.Header.Get("Content-Encoding"), resp.Header.Get("Content-Length"), n)
	}
}

// countingFS counts whole-file reads, to show a gzip copy is made once.
type countingFS struct {
	fstest.MapFS
	reads atomic.Int32
}

func (c *countingFS) ReadFile(name string) ([]byte, error) {
	if name == "assets/app-1a2b.js" {
		c.reads.Add(1)
	}
	return fs.ReadFile(c.MapFS, name)
}

func TestGzipOnce(t *testing.T) {
	cfs := &countingFS{MapFS: gzipFS()}
	h := Handler(cfs)
	if n := cfs.reads.Load(); n != 1 {
		t.Fatalf("%d reads at startup, want 1 (the ETag)", n)
	}
	get(t, h, "GET", "/assets/app-1a2b.js", nil)
	if n := cfs.reads.Load(); n != 1 {
		t.Fatalf("an identity request compressed the file (%d reads)", n)
	}
	first := body(t, get(t, h, "GET", "/assets/app-1a2b.js", gz))
	for range 5 {
		if b := body(t, get(t, h, "GET", "/assets/app-1a2b.js", gz)); b != first {
			t.Fatal("the gzip copy changed between requests")
		}
	}
	if n := cfs.reads.Load(); n != 2 {
		t.Fatalf("%d reads after six gzip requests, want 2: compressed once and kept", n)
	}
}

func TestGzipPersonalisedPage(t *testing.T) {
	h := Handler(inlineFS())
	// A snapshot over the minimum, with a value that tries to end the block.
	snap := map[string]any{"v": 1, "name": "</script><!--", "resources": strings.Repeat("proxmox:i_1/node/pve ", 200)}
	data, err := json.Marshal(snap) // escapes < and >
	if err != nil {
		t.Fatal(err)
	}
	f := func() []byte { return data }

	plain := getWith(t, h, "/", f, nil)
	pb := body(t, plain)
	zipped := getWith(t, h, "/", f, gz)
	zb := gunzip(t, body(t, zipped))
	if zipped.Header.Get("Content-Encoding") != "gzip" || plain.Header.Get("Content-Encoding") != "" {
		t.Fatalf("encodings %q and %q", zipped.Header.Get("Content-Encoding"), plain.Header.Get("Content-Encoding"))
	}
	if zb != pb {
		t.Fatal("the gzipped page isn't the same page")
	}
	m := block.FindStringSubmatch(zb)
	lt := string(rune(92)) + "u003c" // what json.Marshal writes for '<'
	if m == nil || m[1] != string(data) || !strings.Contains(m[1], lt+"/script"+string(rune(92))+"u003e"+lt+"!--") {
		t.Fatalf("the data block isn't intact and escaped: %q", zb)
	}
	for name, resp := range map[string]*http.Response{"identity": plain, "gzip": zipped} {
		if resp.Header.Get("Cache-Control") != cacheNoStore || resp.Header.Get("ETag") != "" {
			t.Errorf("%s: Cache-Control %q, ETag %q", name, resp.Header.Get("Cache-Control"), resp.Header.Get("ETag"))
		}
		if !varies(resp, "Cookie") || !varies(resp, "Accept-Encoding") {
			t.Errorf("%s: Vary %q", name, resp.Header.Values("Vary"))
		}
	}

	// A range gets the identity bytes.
	resp := getWith(t, h, "/", f, with(gz, "Range", "bytes=0-14"))
	if resp.StatusCode != http.StatusPartialContent || resp.Header.Get("Content-Encoding") != "" || body(t, resp) != pb[:15] {
		t.Fatalf("range: %d %q", resp.StatusCode, resp.Header.Get("Content-Encoding"))
	}

	// A small personalised page isn't worth compressing.
	small := getWith(t, h, "/", func() []byte { return []byte(`{"v":1}`) }, gz)
	if small.Header.Get("Content-Encoding") != "" || !strings.Contains(body(t, small), SnapshotID) {
		t.Fatalf("small page: %q", small.Header.Get("Content-Encoding"))
	}
}

func TestGzipBuiltIn(t *testing.T) {
	// Whatever is embedded (the placeholder in tests) serves under gzip too.
	resp := get(t, New(), "GET", "/", gz)
	b := body(t, resp)
	if resp.Header.Get("Content-Encoding") == "gzip" {
		b = gunzip(t, b)
	}
	if resp.StatusCode != 200 || !bytes.Contains([]byte(b), []byte("Cha Dash")) {
		t.Fatalf("built-in page under gzip: %d", resp.StatusCode)
	}
}
