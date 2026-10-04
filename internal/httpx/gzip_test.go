package httpx

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestAcceptsGzip(t *testing.T) {
	for _, tc := range []struct {
		ae   []string
		want bool
	}{
		{nil, false},
		{[]string{""}, false},
		{[]string{"gzip"}, true},
		{[]string{"GZip"}, true},
		{[]string{"x-gzip"}, true},
		{[]string{"gzip, deflate, br, zstd"}, true},
		{[]string{"br;q=1.0, gzip;q=0.5"}, true},
		{[]string{"gzip;q=0"}, false},
		{[]string{"gzip; q=0.000"}, false},
		{[]string{"gzip;Q=0"}, false},
		{[]string{"gzip;q=nope"}, false}, // malformed: never turns it on
		{[]string{"gzip;q=2"}, false},
		{[]string{"identity"}, false},
		{[]string{"br, deflate"}, false},
		{[]string{"*"}, true},
		{[]string{"*;q=0"}, false},
		{[]string{"br, *;q=0.1"}, true},
		{[]string{"gzip;q=0, *"}, false}, // named beats the wildcard
		{[]string{"*;q=0, gzip"}, true},
		{[]string{"br", "gzip"}, true}, // several header lines
	} {
		r := httptest.NewRequest("GET", "/", nil)
		for _, v := range tc.ae {
			r.Header.Add("Accept-Encoding", v)
		}
		if got := AcceptsGzip(r); got != tc.want {
			t.Errorf("Accept-Encoding %q: AcceptsGzip = %v, want %v", tc.ae, got, tc.want)
		}
	}
}

func TestAddVary(t *testing.T) {
	h := http.Header{}
	AddVary(h, "Cookie")
	AddVary(h, "Accept-Encoding")
	AddVary(h, "accept-encoding")
	AddVary(h, "Cookie")
	if got := h.Values("Vary"); len(got) != 2 || got[0] != "Cookie" || got[1] != "Accept-Encoding" {
		t.Fatalf("Vary %q", got)
	}
	h = http.Header{"Vary": {"Origin, Accept-Encoding"}}
	AddVary(h, "Accept-Encoding")
	if got := h.Values("Vary"); len(got) != 1 {
		t.Fatalf("a listed token was added again: %q", got)
	}
	h = http.Header{"Vary": {"*"}}
	AddVary(h, "Cookie")
	if got := h.Values("Vary"); len(got) != 1 {
		t.Fatalf("Vary: * already covers everything: %q", got)
	}
}

func TestGzipETag(t *testing.T) {
	for in, want := range map[string]string{
		`"abc"`:   `"abc-gz"`,
		`W/"abc"`: `W/"abc-gz"`,
		`abc`:     `abc-gz`,
	} {
		if got := GzipETag(in); got != want {
			t.Errorf("GzipETag(%s) = %s, want %s", in, got, want)
		}
	}
}

func TestGzipBytes(t *testing.T) {
	in := bytes.Repeat([]byte(`{"id":"proxmox:i_1/node/pve","state":"running"},`), 100)
	out := GzipBytes(in)
	if len(out) >= len(in) || gunzip(t, out) != string(in) {
		t.Fatalf("GzipBytes: %d bytes from %d", len(out), len(in))
	}
}

// bigJSON is a JSON body over MinGzipBytes.
var bigJSON = `{"items":[` + strings.TrimSuffix(strings.Repeat(`{"id":"sonarr:i_1/app","kind":"app","health":"ok"},`, 40), ",") + `]}`

func gunzip(t *testing.T, b []byte) string {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("not gzip: %v", err)
	}
	out, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func serveGzip(t *testing.T, h http.Handler, method string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, "/api/x", nil)
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	GzipJSON(h).ServeHTTP(rec, r)
	return rec
}

func jsonHandler(status int, body string, extra map[string]string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		for k, v := range extra {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
		io.WriteString(w, body)
	})
}

var acceptGzip = map[string]string{"Accept-Encoding": "gzip, deflate, br"}

func TestGzipJSONCompresses(t *testing.T) {
	h := jsonHandler(http.StatusOK, bigJSON, map[string]string{"ETag": `"v1"`, "Content-Length": fmt.Sprint(len(bigJSON))})
	rec := serveGzip(t, h, "GET", acceptGzip)
	if rec.Code != 200 || rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("%d, Content-Encoding %q", rec.Code, rec.Header().Get("Content-Encoding"))
	}
	if got := gunzip(t, rec.Body.Bytes()); got != bigJSON {
		t.Fatalf("body changed: %q", got)
	}
	if rec.Body.Len() >= len(bigJSON) {
		t.Fatalf("%d bytes compressed from %d", rec.Body.Len(), len(bigJSON))
	}
	hdr := rec.Header()
	if hdr.Get("Vary") != "Accept-Encoding" || hdr.Get("Content-Length") != "" || hdr.Get("ETag") != `"v1-gz"` {
		t.Fatalf("headers %v", hdr)
	}
	if hdr.Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type %q", hdr.Get("Content-Type"))
	}

	// Without gzip in Accept-Encoding: the same bytes, but it still varies.
	for _, ae := range []map[string]string{nil, {"Accept-Encoding": "gzip;q=0, br"}} {
		rec = serveGzip(t, h, "GET", ae)
		if rec.Header().Get("Content-Encoding") != "" || rec.Body.String() != bigJSON || rec.Header().Get("ETag") != `"v1"` {
			t.Fatalf("%v: %q, ETag %q", ae, rec.Header().Get("Content-Encoding"), rec.Header().Get("ETag"))
		}
		if rec.Header().Get("Vary") != "Accept-Encoding" {
			t.Fatalf("%v: Vary %q", ae, rec.Header().Get("Vary"))
		}
	}
}

func TestGzipJSONStatusesAndTypes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		h     http.Handler
		code  int
		gzip  bool
		vary  bool
		check string // the body, when not compressed
	}{
		{"an error body", jsonHandler(500, bigJSON, nil), 500, true, true, ""},
		{"problem+json", jsonHandler(400, bigJSON, map[string]string{"Content-Type": "application/problem+json"}), 400, true, true, ""},
		{"small", jsonHandler(200, `{"ok":true}`, nil), 200, false, false, `{"ok":true}`},
		{"one byte short", jsonHandler(200, bigJSON[:MinGzipBytes-1], nil), 200, false, false, bigJSON[:MinGzipBytes-1]},
		{"exactly the minimum", jsonHandler(200, bigJSON[:MinGzipBytes], nil), 200, true, true, ""},
		{"YAML", jsonHandler(200, bigJSON, map[string]string{"Content-Type": "application/yaml; charset=utf-8"}), 200, false, false, bigJSON},
		{"plain text", jsonHandler(200, bigJSON, map[string]string{"Content-Type": "text/plain"}), 200, false, false, bigJSON},
		{"no Content-Type", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, bigJSON) }), 200, false, false, bigJSON},
		{"already encoded", jsonHandler(200, bigJSON, map[string]string{"Content-Encoding": "br"}), 200, false, false, bigJSON},
		{"a range", jsonHandler(206, bigJSON, map[string]string{"Content-Range": "bytes 0-9/100"}), 206, false, false, bigJSON},
		{"no content", jsonHandler(204, "", nil), 204, false, false, ""},
		{"not modified", jsonHandler(304, "", map[string]string{"ETag": `"v1"`}), 304, false, false, ""},
		{"implicit 200", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			for i := 0; i < len(bigJSON); i += 100 { // many small writes across the threshold
				io.WriteString(w, bigJSON[i:min(i+100, len(bigJSON))])
			}
		}), 200, true, true, ""},
	} {
		rec := serveGzip(t, tc.h, "GET", acceptGzip)
		if rec.Code != tc.code {
			t.Errorf("%s: status %d, want %d", tc.name, rec.Code, tc.code)
		}
		gz := rec.Header().Get("Content-Encoding") == "gzip"
		if gz != tc.gzip {
			t.Errorf("%s: compressed %v, want %v", tc.name, gz, tc.gzip)
		}
		if vary := rec.Header().Get("Vary") == "Accept-Encoding"; vary != tc.vary {
			t.Errorf("%s: Vary %q", tc.name, rec.Header().Get("Vary"))
		}
		switch {
		case gz && !strings.HasPrefix(bigJSON, gunzip(t, rec.Body.Bytes())):
			t.Errorf("%s: compressed body changed", tc.name)
		case !gz && tc.check != "" && rec.Body.String() != tc.check:
			t.Errorf("%s: body %q", tc.name, rec.Body.String())
		case !gz && tc.name == "already encoded" && rec.Header().Get("Content-Encoding") != "br":
			t.Errorf("%s: Content-Encoding %q", tc.name, rec.Header().Get("Content-Encoding"))
		}
	}
}

func TestGzipJSONHead(t *testing.T) {
	called := false
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if _, ok := w.(*gzipWriter); ok {
			t.Error("HEAD went through the gzip writer")
		}
		w.Header().Set("Content-Type", "application/json")
	})
	rec := serveGzip(t, h, "HEAD", acceptGzip)
	if !called || rec.Header().Get("Content-Encoding") != "" {
		t.Fatalf("HEAD: %v", rec.Header())
	}
}

// hijackRecorder is a ResponseWriter that can be hijacked, like net/http's.
type hijackRecorder struct {
	*httptest.ResponseRecorder
	hijacked bool
}

func (h *hijackRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h.hijacked = true
	c1, c2 := net.Pipe()
	c2.Close()
	return c1, bufio.NewReadWriter(bufio.NewReader(c1), bufio.NewWriter(c1)), nil
}

func TestGzipJSONLeavesUpgradesAlone(t *testing.T) {
	for _, hdr := range []map[string]string{
		{"Upgrade": "websocket", "Connection": "Upgrade", "Accept-Encoding": "gzip"},
		{"Connection": "keep-alive, Upgrade", "Accept-Encoding": "gzip"},
		{"Upgrade": "h2c", "Accept-Encoding": "gzip"},
	} {
		orig := &hijackRecorder{ResponseRecorder: httptest.NewRecorder()}
		h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if w != http.ResponseWriter(orig) {
				t.Errorf("%v: the handler got a wrapped writer %T", hdr, w)
			}
			conn, _, err := http.NewResponseController(w).Hijack()
			if err != nil {
				t.Errorf("%v: hijack: %v", hdr, err)
				return
			}
			conn.Close()
		})
		r := httptest.NewRequest("GET", "/ws", nil)
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		GzipJSON(h).ServeHTTP(orig, r)
		if !orig.hijacked {
			t.Errorf("%v: not hijacked", hdr)
		}
	}
}

func TestGzipJSONFlush(t *testing.T) {
	// A streaming handler: the decision is made at the first flush.
	small := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"a":`)
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Errorf("flush: %v", err)
		}
		io.WriteString(w, bigJSON+`}`)
	})
	rec := serveGzip(t, small, "GET", acceptGzip)
	if rec.Header().Get("Content-Encoding") != "" || !rec.Flushed || rec.Body.String() != `{"a":`+bigJSON+`}` {
		t.Fatalf("small then flush: %q, flushed %v", rec.Header().Get("Content-Encoding"), rec.Flushed)
	}

	big := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `[`+bigJSON)
		w.(http.Flusher).Flush()
		io.WriteString(w, `,`+bigJSON+`]`)
	})
	rec = serveGzip(t, big, "GET", acceptGzip)
	if rec.Header().Get("Content-Encoding") != "gzip" || !rec.Flushed {
		t.Fatalf("big then flush: %q, flushed %v", rec.Header().Get("Content-Encoding"), rec.Flushed)
	}
	if got := gunzip(t, rec.Body.Bytes()); got != `[`+bigJSON+`,`+bigJSON+`]` {
		t.Fatalf("body %q", got)
	}
}

func TestGzipJSONEmpty(t *testing.T) {
	rec := serveGzip(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), "GET", acceptGzip)
	if rec.Code != 200 || rec.Body.Len() != 0 || rec.Header().Get("Content-Encoding") != "" {
		t.Fatalf("empty: %d %q", rec.Code, rec.Body.String())
	}
}

// TestGzipJSONConcurrent shares the writer pool across goroutines; under
// -race it catches a writer handed to two responses at once.
func TestGzipJSONConcurrent(t *testing.T) {
	h := GzipJSON(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, strings.Repeat(r.URL.Query().Get("c"), 2000))
	}))
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Go(func() {
			c := string(rune('a' + i%26))
			r := httptest.NewRequest("GET", "/api/x?c="+c, nil)
			r.Header.Set("Accept-Encoding", "gzip")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)
			if got := gunzip(t, rec.Body.Bytes()); got != strings.Repeat(c, 2000) {
				t.Errorf("request %d: body mixed up", i)
			}
		})
	}
	wg.Wait()
}
