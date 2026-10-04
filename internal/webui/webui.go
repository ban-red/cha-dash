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
package webui

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"
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
	h := &handler{fsys: fsys, etags: map[string]string{}}
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
		return nil
	})
	return h
}

type handler struct {
	fsys  fs.FS
	etags map[string]string // by file path; immutable after Handler returns
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
	if et := h.etags[name]; et != "" {
		hdr.Set("ETag", et)
	}
	// A zero modtime leaves out Last-Modified; ServeContent still handles
	// HEAD, ranges and If-None-Match against the ETag.
	http.ServeContent(w, r, name, time.Time{}, rs)
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
