package webui

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"time"

	"dash.cha.sh/internal/httpx"
)

// SnapshotID is the id of the data block index.html carries the first-paint
// snapshot in; the web app reads it (web/src/lib/inline.ts).
const SnapshotID = "chadash-snapshot"

// cacheNoStore is the personalised index.html's Cache-Control: it holds one
// person's data, so no cache may keep it, the service worker included (its
// shell is the generic index.html).
const cacheNoStore = "no-store"

// InlineFunc returns the JSON to embed in index.html for one request, or nil
// for the generic page. The JSON must already be safe inside a <script>
// element: no '<' anywhere (firstpaint.Encode escapes it).
type InlineFunc func() []byte

type inlineKey struct{}

// WithInline asks the handler to embed f's JSON when it answers this request
// with index.html. f is only called then, so the caller can attach it to
// every request cheaply. The JSON goes in a non-executable data block,
//
//	<script type="application/json" id="chadash-snapshot">…</script>
//
// which the CSP allows: data blocks are never run, so default-src 'self'
// holds and nothing inline executes.
func WithInline(ctx context.Context, f InlineFunc) context.Context {
	return context.WithValue(ctx, inlineKey{}, f)
}

// servePersonal answers with index.html plus the request's inline snapshot,
// if it has one, and reports whether it did. The page is never cached (no
// ETag, no-store), so a validator from the generic page can't match it and
// a shared or service-worker cache can't keep it.
//
// It's gzipped per request for clients that accept it (a Range request gets
// the identity bytes). The page holds no credential and reflects nothing
// from the request, so compressing it can't make a BREACH oracle; see
// internal/httpx/gzip.go.
func (h *handler) servePersonal(w http.ResponseWriter, r *http.Request, index io.ReadSeeker) bool {
	f, _ := r.Context().Value(inlineKey{}).(InlineFunc)
	if f == nil {
		return false
	}
	data := f()
	if len(data) == 0 || bytes.IndexByte(data, '<') >= 0 {
		return false // nothing for this request, or not safe to embed
	}
	page, err := io.ReadAll(index)
	if err != nil {
		return false
	}
	page = withDataBlock(page, data)
	hdr := w.Header()
	hdr.Set("Cache-Control", cacheNoStore)
	httpx.AddVary(hdr, "Cookie")
	httpx.AddVary(hdr, "Accept-Encoding")
	hdr.Del("ETag")
	if len(page) >= httpx.MinGzipBytes && r.Header.Get("Range") == "" && httpx.AcceptsGzip(r) {
		hdr.Set("Content-Encoding", "gzip")
		serveEncoded(w, r, indexFile, httpx.GzipBytes(page))
		return true
	}
	http.ServeContent(w, r, indexFile, time.Time{}, bytes.NewReader(page))
	return true
}

// withDataBlock puts the data block just before </body>, or at the end of a page
// without one.
func withDataBlock(page, data []byte) []byte {
	block := make([]byte, 0, len(data)+80)
	block = append(block, `<script type="application/json" id="`+SnapshotID+`">`...)
	block = append(block, data...)
	block = append(block, `</script>`...)
	at := len(page)
	if i := bytes.LastIndex(bytes.ToLower(page), []byte("</body>")); i >= 0 {
		at = i
	}
	out := make([]byte, 0, len(page)+len(block))
	out = append(out, page[:at]...)
	out = append(out, block...)
	return append(out, page[at:]...)
}
