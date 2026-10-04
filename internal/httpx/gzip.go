package httpx

// Response compression: gzip only, from the standard library. Three places
// use it:
//
//   - the web app's files, compressed once and kept (webui);
//   - the personalised index.html, compressed per request (webui);
//   - the API's JSON responses, through GzipJSON (the server mounts it on
//     /api/ only).
//
// Never compressed: the WebSocket upgrade (/ws), /healthz, a response that
// already has a Content-Encoding, byte ranges, and bodies under MinGzipBytes,
// where gzip's ~20-byte frame and the CPU aren't worth it.
//
// # BREACH
//
// BREACH (like CRIME before it) recovers a secret from a compressed HTTPS
// response by measuring its length while the attacker varies input that the
// response reflects next to the secret: the guess that compresses best
// matches. It needs all of these at once:
//
//  1. a secret in the response body (a CSRF token, a session ID, a key);
//  2. attacker-chosen input reflected in the same body;
//  3. the attacker making the victim's browser send many authenticated
//     requests, and seeing the sizes of the encrypted responses.
//
// The hub breaks (1) everywhere it compresses:
//
//   - There are no CSRF tokens: cross-origin protection is net/http's
//     CrossOriginProtection (Sec-Fetch-Site and Origin), not a token in pages.
//   - The session lives in an HttpOnly cookie. Cookies travel in headers,
//     which gzip never touches.
//   - Integration secrets are write-only: the API answers {"set": true} for
//     them, exports carry placeholders, diagnostics redact them.
//   - The personalised index.html carries the owner's board and last-known
//     state, but no credential, and nothing from the request: not the path,
//     not the query, not a header.
//
// The few responses that do carry a secret are kept out of compression by
// path (server.compressible): sign-in, passkey ceremonies and the session
// (/api/auth/…), first-run setup (/api/setup), and the integration sign-in
// flow that hands a Plex token to the form once (/api/integrations/authorize/…).
// Most are smaller than MinGzipBytes anyway; the exclusion keeps it that way
// on purpose. YAML exports, the only response that can hold sealed secrets,
// aren't JSON, so GzipJSON leaves them alone.
//
// (2) and (3) are weak too: SameSite=Lax keeps the cookie off cross-site
// subresource requests, and cross-origin writes are refused. What's left is
// theoretical: someone on the network path who can also plant text in what
// an integration reads (a container or torrent name, say) and get the owner
// to reload many times could learn a little more of what the dashboard
// shows. No credential can leak that way.

import (
	"bytes"
	"compress/gzip"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// MinGzipBytes is the smallest body the hub compresses.
const MinGzipBytes = 1024

// AcceptsGzip reports whether r's Accept-Encoding allows a gzip response:
// gzip (or x-gzip) with a non-zero q, or, when neither is named, "*" with a
// non-zero q. No Accept-Encoding means identity only.
func AcceptsGzip(r *http.Request) bool {
	gzipQ, starQ := -1.0, -1.0
	for _, v := range r.Header.Values("Accept-Encoding") {
		for part := range strings.SplitSeq(v, ",") {
			coding, params, _ := strings.Cut(part, ";")
			q := qvalue(params)
			switch strings.ToLower(strings.TrimSpace(coding)) {
			case "gzip", "x-gzip":
				gzipQ = max(gzipQ, q)
			case "*":
				starQ = max(starQ, q)
			}
		}
	}
	if gzipQ >= 0 {
		return gzipQ > 0
	}
	return starQ > 0
}

// qvalue reads the q parameter from an Accept-Encoding element's
// parameters: 1 when absent, 0 when malformed (so a garbled entry never
// turns compression on).
func qvalue(params string) float64 {
	for p := range strings.SplitSeq(params, ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(p), "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(k), "q") {
			continue
		}
		q, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil || q < 0 || q > 1 {
			return 0
		}
		return q
	}
	return 1
}

// AddVary adds value to the Vary header unless it's already listed.
func AddVary(h http.Header, value string) {
	for _, v := range h.Values("Vary") {
		for tok := range strings.SplitSeq(v, ",") {
			if t := strings.TrimSpace(tok); t == "*" || strings.EqualFold(t, value) {
				return
			}
		}
	}
	h.Add("Vary", value)
}

// GzipETag is the validator for the gzip copy of a representation whose
// identity ETag is etag: a distinct value, so a cache never mixes the two.
func GzipETag(etag string) string {
	if strings.HasSuffix(etag, `"`) && len(etag) >= 2 {
		return etag[:len(etag)-1] + `-gz"`
	}
	return etag + "-gz"
}

// gzipLevel is the per-request level, for API bodies and the personalised
// index.html. Level 6 (the default) keeps most of level 9's saving at a
// fraction of its time; the web app's files are compressed once, at 9.
const gzipLevel = gzip.DefaultCompression

// A gzip.Writer holds ~800 KB of compressor state; reuse them rather than
// allocate one per response.
var gzipWriters = sync.Pool{New: func() any {
	zw, _ := gzip.NewWriterLevel(io.Discard, gzipLevel) // a valid level: no error
	return zw
}}

func getGzip(w io.Writer) *gzip.Writer {
	zw := gzipWriters.Get().(*gzip.Writer)
	zw.Reset(w)
	return zw
}

func putGzip(zw *gzip.Writer) {
	zw.Reset(io.Discard) // drop the response writer
	gzipWriters.Put(zw)
}

// GzipBytes returns b compressed at the per-request level.
func GzipBytes(b []byte) []byte {
	var out bytes.Buffer
	out.Grow(len(b)/4 + 64)
	zw := getGzip(&out)
	_, _ = zw.Write(b) // a bytes.Buffer never fails
	_ = zw.Close()
	putGzip(zw)
	return out.Bytes()
}

// GzipJSON compresses JSON responses of MinGzipBytes or more for clients
// that accept gzip, and marks them Vary: Accept-Encoding whether or not this
// client did. Anything else passes through unchanged:
//
//   - WebSocket and other upgrade requests, which never see the wrapper (the
//     connection is hijacked, and Hijack must reach the real writer);
//   - HEAD requests;
//   - responses that aren't application/json or +json, that already have a
//     Content-Encoding, that carry a Content-Range, or that have no body
//     (1xx, 204, 304).
//
// The body is held until MinGzipBytes have been written or the handler
// returns, so small responses go out exactly as before. A handler that
// flushes decides early. An ETag set by the handler gets GzipETag's suffix
// on the compressed copy.
//
// Which routes may be compressed is the caller's choice; see the BREACH
// notes at the top of this file.
func GzipJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead || isUpgrade(r) {
			next.ServeHTTP(w, r)
			return
		}
		gw := &gzipWriter{ResponseWriter: w, accept: AcceptsGzip(r)}
		next.ServeHTTP(gw, r)
		gw.finish()
	})
}

// isUpgrade reports whether r asks to switch protocols (a WebSocket, h2c).
func isUpgrade(r *http.Request) bool {
	if r.Header.Get("Upgrade") != "" {
		return true
	}
	for _, v := range r.Header.Values("Connection") {
		for tok := range strings.SplitSeq(v, ",") {
			if strings.EqualFold(strings.TrimSpace(tok), "upgrade") {
				return true
			}
		}
	}
	return false
}

// gzipWriter buffers a response until it knows whether to compress it.
type gzipWriter struct {
	http.ResponseWriter
	accept bool // the client takes gzip

	code    int          // the status the handler set; 0 until it does
	buf     []byte       // the body so far, while undecided
	decided bool         // headers have gone to the real writer
	zw      *gzip.Writer // non-nil when compressing
	err     error        // the first write error, once decided
}

func (g *gzipWriter) WriteHeader(code int) {
	switch {
	case g.decided:
		g.ResponseWriter.WriteHeader(code) // superfluous; net/http says so
	case code < 200:
		g.ResponseWriter.WriteHeader(code) // informational (103 Early Hints): send it now
	case g.code == 0:
		g.code = code
		if !g.compressible() {
			g.decide(false)
		}
	}
}

func (g *gzipWriter) Write(p []byte) (int, error) {
	if !g.decided {
		if g.code == 0 {
			g.code = http.StatusOK
			if !g.compressible() {
				g.decide(false)
				return g.write(p)
			}
		}
		g.buf = append(g.buf, p...)
		if len(g.buf) >= MinGzipBytes {
			g.decide(true)
		}
		return len(p), g.err
	}
	return g.write(p)
}

// write sends p on after the decision.
func (g *gzipWriter) write(p []byte) (int, error) {
	if g.err != nil {
		return 0, g.err
	}
	var n int
	if g.zw != nil {
		n, g.err = g.zw.Write(p)
	} else {
		n, g.err = g.ResponseWriter.Write(p)
	}
	return n, g.err
}

// compressible reports whether the status and headers allow compression;
// the size is decided separately.
func (g *gzipWriter) compressible() bool {
	switch {
	case g.code < 200, g.code == http.StatusNoContent, g.code == http.StatusNotModified, g.code == http.StatusPartialContent:
		return false
	}
	h := g.Header()
	if h.Get("Content-Encoding") != "" || h.Get("Content-Range") != "" {
		return false
	}
	mt, _, err := mime.ParseMediaType(h.Get("Content-Type"))
	return err == nil && (mt == "application/json" || strings.HasSuffix(mt, "+json"))
}

// decide sends the headers, compressing when big is true and the client
// takes gzip, and then whatever was buffered.
func (g *gzipWriter) decide(big bool) {
	g.decided = true
	if g.code == 0 {
		g.code = http.StatusOK
	}
	if big {
		h := g.Header()
		AddVary(h, "Accept-Encoding")
		if g.accept {
			h.Set("Content-Encoding", "gzip")
			h.Del("Content-Length")
			if et := h.Get("ETag"); et != "" {
				h.Set("ETag", GzipETag(et))
			}
			g.zw = getGzip(g.ResponseWriter)
		}
	}
	g.ResponseWriter.WriteHeader(g.code)
	if len(g.buf) > 0 {
		buf := g.buf
		g.buf = nil
		_, _ = g.write(buf)
	}
}

// finish ends the response once the handler has returned.
func (g *gzipWriter) finish() {
	if !g.decided {
		if g.code == 0 && len(g.buf) == 0 {
			return // nothing written: net/http sends its empty 200
		}
		g.decide(false) // under MinGzipBytes, or it would have been decided
	}
	if g.zw != nil {
		if err := g.zw.Close(); err != nil && g.err == nil {
			g.err = err
		}
		putGzip(g.zw)
		g.zw = nil
	}
}

// FlushError sends what's buffered: a handler that flushes is streaming, so
// the decision can't wait for MinGzipBytes. http.ResponseController finds it.
func (g *gzipWriter) FlushError() error {
	if !g.decided {
		g.decide(g.code != 0 && g.compressible() && len(g.buf) >= MinGzipBytes)
	}
	if g.zw != nil {
		if err := g.zw.Flush(); err != nil {
			return err
		}
	}
	return http.NewResponseController(g.ResponseWriter).Flush()
}

// Flush implements http.Flusher for handlers that type-assert it.
func (g *gzipWriter) Flush() { _ = g.FlushError() }

// Unwrap lets http.ResponseController reach the real writer's deadlines.
// Upgrades never get here (GzipJSON passes them through), so Hijack through
// it can't bypass a half-written gzip stream.
func (g *gzipWriter) Unwrap() http.ResponseWriter { return g.ResponseWriter }
