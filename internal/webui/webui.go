// Package webui serves the single-page app.
//
// The built SPA (Vite's output) is embedded with `-tags webui`; without the
// tag the hub serves a placeholder page, so `go test ./...` and plain
// `go build` need no JavaScript toolchain.
//
// Routing is history-mode: a GET for a path that isn't a file gets
// index.html, so client-side routes survive a reload. Hashed files under
// /assets/ are cached for a year; everything else is revalidated.
//
// index.html can carry a first-paint snapshot for whoever asked (WithInline,
// inline.go); that variant is never cached.
//
// Text files of httpx.MinGzipBytes or more (HTML, JS, CSS, JSON, SVG, the
// manifest, source maps) are also served gzipped to clients that accept it:
// each is compressed once, on first request, and kept. The gzip copy has its
// own ETag (the identity one plus "-gz"), so revalidation answers 304 per
// encoding, and both carry Vary: Accept-Encoding. A Range request always gets
// the identity bytes: a byte range of a gzip stream is no use to anyone. The
// personalised index.html is compressed per request (inline.go). Why
// compression is safe here (BREACH) is in internal/httpx/gzip.go.
package webui

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"dash.cha.sh/internal/httpx"
)

const (
	indexFile = "index.html"
	assetsDir = "assets"

	cacheImmutable  = "public, max-age=31536000, immutable"
	cacheRevalidate = "no-cache"
)

// New serves the web app built into this binary: the SPA with -tags webui,
// otherwise the placeholder page.
func New() http.Handler { return Handler(Files()) }

// Handler serves the SPA in fsys, which holds index.html at its root.
func Handler(fsys fs.FS) http.Handler {
	h := &handler{fsys: fsys, etags: map[string]string{}, gz: map[string]*gzFile{}}
	// embed.FS has no modification times, so a content hash is the only
	// validator there is. It lets "no-cache" revalidation answer 304.
	_ = fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		b, err := fs.ReadFile(fsys, p)
		if err != nil {
			return nil
		}
		sum := sha256.Sum256(b)
		h.etags[p] = `"` + hex.EncodeToString(sum[:12]) + `"`
		if compressible(p) && len(b) >= httpx.MinGzipBytes {
			h.gz[p] = &gzFile{etag: httpx.GzipETag(h.etags[p])}
		}
		return nil
	})
	return h
}

type handler struct {
	fsys  fs.FS
	etags map[string]string  // by file path; immutable after Handler returns
	gz    map[string]*gzFile // the files served gzipped; the map is immutable, entries fill on first use
}

// gzFile is a file's gzip copy, made the first time a client asks for it.
// Compressing lazily keeps startup and memory free of files nobody loads
// (lazy chunks, source maps); each file costs a few milliseconds, once.
type gzFile struct {
	etag string
	once sync.Once
	data []byte // nil if it didn't come out smaller, or the file couldn't be read
}

func (g *gzFile) load(fsys fs.FS, name string) []byte {
	g.once.Do(func() {
		b, err := fs.ReadFile(fsys, name)
		if err != nil {
			return
		}
		var out bytes.Buffer
		zw, _ := gzip.NewWriterLevel(&out, gzip.BestCompression) // a valid level: no error
		_, _ = zw.Write(b)                                       // a bytes.Buffer never fails
		_ = zw.Close()
		if out.Len() < len(b) {
			g.data = out.Bytes()
		}
	})
	return g.data
}

// compressible reports whether a file's type is text worth compressing.
// Images, fonts and WebAssembly are compressed already, or nearly.
func compressible(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".html", ".js", ".mjs", ".css", ".json", ".map", ".svg", ".webmanifest":
		return true
	}
	return false
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// The API and the WebSocket never fall back to the app, even if this
	// handler is mounted in front of them by mistake.
	if under(r.URL.Path, "/api") || under(r.URL.Path, "/ws") {
		http.NotFound(w, r)
		return
	}
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name == "" {
		name = indexFile
	}
	if h.isFile(name) {
		h.serveFile(w, r, name)
		return
	}
	// A missing hashed asset is usually a stale tab asking for last
	// release's chunk. index.html would be the wrong answer: the browser
	// would refuse it as a script, so say 404 plainly.
	if under("/"+name, "/"+assetsDir) {
		http.NotFound(w, r)
		return
	}
	h.serveFile(w, r, indexFile)
}

// under reports whether p is dir or inside it.
func under(p, dir string) bool { return p == dir || strings.HasPrefix(p, dir+"/") }

func (h *handler) isFile(name string) bool {
	if !fs.ValidPath(name) {
		return false
	}
	fi, err := fs.Stat(h.fsys, name)
	return err == nil && fi.Mode().IsRegular()
}

func (h *handler) serveFile(w http.ResponseWriter, r *http.Request, name string) {
	f, err := h.fsys.Open(name)
	if err != nil {
		http.Error(w, "web app not found in this build", http.StatusNotFound)
		return
	}
	defer f.Close()
	rs, ok := f.(io.ReadSeeker)
	if !ok {
		b, err := io.ReadAll(f)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		rs = bytes.NewReader(b)
	}

	hdr := w.Header()
	hdr.Set("Content-Type", contentType(name))
	if strings.HasPrefix(name, assetsDir+"/") {
		hdr.Set("Cache-Control", cacheImmutable)
	} else {
		hdr.Set("Cache-Control", cacheRevalidate)
	}
	if name == indexFile && h.servePersonal(w, r, rs) {
		return
	}
	if h.serveGzip(w, r, name) {
		return
	}
	if et := h.etags[name]; et != "" {
		hdr.Set("ETag", et)
	}
	// A zero modtime leaves out Last-Modified; ServeContent still handles
	// HEAD, ranges and If-None-Match against the ETag.
	http.ServeContent(w, r, name, time.Time{}, rs)
}

// serveGzip answers with name's gzip copy when it has one and the client
// takes gzip, and reports whether it did. Every file with a gzip copy says
// Vary: Accept-Encoding, whichever encoding this answer uses.
func (h *handler) serveGzip(w http.ResponseWriter, r *http.Request, name string) bool {
	g := h.gz[name]
	if g == nil {
		return false
	}
	hdr := w.Header()
	httpx.AddVary(hdr, "Accept-Encoding")
	if r.Header.Get("Range") != "" || !httpx.AcceptsGzip(r) {
		return false
	}
	data := g.load(h.fsys, name)
	if data == nil {
		return false
	}
	hdr.Set("Content-Encoding", "gzip")
	hdr.Set("ETag", g.etag)
	serveEncoded(w, r, name, data)
	return true
}

// serveEncoded serves an already-encoded body with ServeContent, which
// handles HEAD and If-None-Match against the ETag set, but leaves out
// Content-Length once Content-Encoding is set, so it's set here. A 304 or
// an error response drops it again. Content-Type is already set, so the
// gzip bytes are never sniffed.
func serveEncoded(w http.ResponseWriter, r *http.Request, name string, data []byte) {
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
}

// types pins the MIME types the SPA needs. The system tables differ between
// hosts and are often absent in containers, and nosniff makes a wrong type
// fatal for scripts and styles.
var types = map[string]string{
	".html":        "text/html; charset=utf-8",
	".js":          "text/javascript; charset=utf-8",
	".mjs":         "text/javascript; charset=utf-8",
	".css":         "text/css; charset=utf-8",
	".json":        "application/json",
	".map":         "application/json",
	".webmanifest": "application/manifest+json",
	".svg":         "image/svg+xml",
	".png":         "image/png",
	".jpg":         "image/jpeg",
	".jpeg":        "image/jpeg",
	".webp":        "image/webp",
	".avif":        "image/avif",
	".ico":         "image/x-icon",
	".woff2":       "font/woff2",
	".woff":        "font/woff",
	".txt":         "text/plain; charset=utf-8",
	".wasm":        "application/wasm",
	".xml":         "application/xml",
}

func contentType(name string) string {
	ext := strings.ToLower(path.Ext(name))
	if t, ok := types[ext]; ok {
		return t
	}
	if t := mime.TypeByExtension(ext); t != "" {
		return t
	}
	return "application/octet-stream"
}
