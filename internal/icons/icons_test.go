package icons

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"dash.cha.sh/internal/boards"
	"dash.cha.sh/internal/store"
)

var (
	pngBytes = append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{1}, 40)...)
	icoBytes = append([]byte{0, 0, 1, 0, 1, 0}, bytes.Repeat([]byte{2}, 40)...)
	webpByte = append([]byte("RIFF\x10\x00\x00\x00WEBPVP8 "), bytes.Repeat([]byte{3}, 20)...)
	safeSVG  = []byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 8 8"><defs><linearGradient id="g"/></defs><rect width="8" height="8" fill="url(#g)"/><use href="#g"/></svg>`)
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "chadash.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	return st
}

// newService fetches from anywhere, loopback included (httptest), unless
// strict.
func newService(t *testing.T, st *store.Store, strict bool) *Service {
	t.Helper()
	s := New(Options{Store: st, Fetch: true})
	if !strict {
		s.allowed = func(netip.Addr) bool { return true }
	}
	return s
}

func TestAllowed(t *testing.T) {
	for _, c := range []struct {
		addr string
		ok   bool
	}{
		{"127.0.0.1", false}, {"::1", false}, {"169.254.169.254", false}, {"169.254.10.1", false}, {"fe80::1", false},
		{"0.0.0.0", false}, {"0.1.2.3", false}, {"::", false}, {"224.0.0.251", false}, {"ff02::1", false}, {"255.255.255.255", false},
		{"100.100.100.200", false}, {"fd00:ec2::254", false}, {"192.0.0.192", false}, {"::ffff:127.0.0.1", false}, {"::ffff:169.254.169.254", false},
		{"192.168.250.10", true}, {"10.88.0.1", true}, {"172.31.250.4", true}, {"203.0.113.5", true}, {"fd12:3456::1", true}, {"100.80.0.1", true},
	} {
		if got := Allowed(netip.MustParseAddr(c.addr)); got != c.ok {
			t.Errorf("Allowed(%s) = %v, want %v", c.addr, got, c.ok)
		}
	}
}

func serve(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func image(ctype string, body []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		if ctype != "-" {
			w.Header().Set("Content-Type", ctype)
		}
		_, _ = w.Write(body)
	}
}

func isRefused(err error) bool {
	var re *refusedError
	return errors.As(err, &re)
}

// TestFetchRefusesLoopbackAndMetadata: the real rule never dials loopback,
// even when the name resolves there (localhost), nor link-local.
func TestFetchRefusesLoopbackAndMetadata(t *testing.T) {
	var hits atomic.Int32
	srv := serve(t, func(w http.ResponseWriter, _ *http.Request) { hits.Add(1); _, _ = w.Write(pngBytes) })
	s := newService(t, openStore(t), true)
	port := srv.URL[strings.LastIndex(srv.URL, ":"):]
	for _, u := range []string{srv.URL + "/x.png", "http://localhost" + port + "/x.png", "http://[::1]" + port + "/x.png", "http://169.254.169.254/latest/meta-data/"} {
		_, _, err := s.Fetch(t.Context(), u)
		if !isRefused(err) {
			t.Errorf("Fetch(%s) = %v, want a refusal", u, err)
		}
		var re *refusedError
		if errors.As(err, &re) && !re.final {
			t.Errorf("Fetch(%s): an address refusal should be final", u)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("the server was reached %d times", hits.Load())
	}
	for _, u := range []string{"file:///etc/passwd", "ftp://example.test/x", "http://user:pw@example.test/x.png", "gopher://x", "/relative.png"} {
		if _, _, err := s.Fetch(t.Context(), u); !isRefused(err) {
			t.Errorf("Fetch(%s) = %v, want a refusal", u, err)
		}
	}
}

func TestFetchRedirects(t *testing.T) {
	s := newService(t, openStore(t), false)
	var srv *httptest.Server
	srv = serve(t, func(w http.ResponseWriter, r *http.Request) {
		port := srv.URL[strings.LastIndex(srv.URL, ":"):]
		switch r.URL.Path {
		case "/one":
			http.Redirect(w, r, "/icon.png", http.StatusFound)
		case "/two":
			http.Redirect(w, r, "/one", http.StatusFound)
		case "/elsewhere":
			http.Redirect(w, r, "http://localhost"+port+"/icon.png", http.StatusFound)
		case "/icon.png":
			image(TypePNG, pngBytes)(w, r)
		default:
			http.NotFound(w, r)
		}
	})
	if ct, _, err := s.Fetch(t.Context(), srv.URL+"/one"); err != nil || ct != TypePNG {
		t.Fatalf("one same-host redirect = %q, %v", ct, err)
	}
	for _, p := range []string{"/two", "/elsewhere"} {
		if _, _, err := s.Fetch(t.Context(), srv.URL+p); !isRefused(err) {
			t.Errorf("%s = %v, want a refusal", p, err)
		}
	}
}

func TestFetchSizeAndTypes(t *testing.T) {
	s := newService(t, openStore(t), false)
	big := append(append([]byte{}, pngBytes...), bytes.Repeat([]byte{0}, MaxBytes)...)
	cases := []struct {
		name  string
		h     http.HandlerFunc
		ctype string // "" = refused
	}{
		{"png", image(TypePNG, pngBytes), TypePNG},
		{"ico as vnd", image("image/vnd.microsoft.icon", icoBytes), TypeICO},
		{"ico as octet-stream", image("application/octet-stream", icoBytes), TypeICO},
		{"png named ico", image(TypeICO, pngBytes), TypePNG},
		{"webp", image(TypeWebP, webpByte), TypeWebP},
		{"svg", image(TypeSVG, safeSVG), TypeSVG},
		{"svg no type", image("-", safeSVG), TypeSVG},
		{"too large", image(TypePNG, big), ""},
		{"too large, no length", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", TypePNG)
			w.(http.Flusher).Flush() // chunked: no Content-Length
			_, _ = w.Write(big)
		}, ""},
		{"html", image("text/html", []byte("<html><body>sign in</body></html>")), ""},
		{"png bytes declared html", image("text/html", pngBytes), ""},
		{"svg declared png", image(TypePNG, safeSVG), ""},
		{"gif", image("image/gif", []byte("GIF89a\x01\x00\x01\x00")), ""},
		{"garbage", image("application/octet-stream", []byte("not an image at all")), ""},
		{"empty", image(TypePNG, nil), ""},
		{"404", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := serve(t, c.h)
			ct, body, err := s.Fetch(t.Context(), srv.URL+"/i")
			if c.ctype == "" {
				if err == nil {
					t.Fatalf("got %s, want a refusal", ct)
				}
				return
			}
			if err != nil || ct != c.ctype || len(body) == 0 {
				t.Fatalf("= %q, %d bytes, %v; want %s", ct, len(body), err, c.ctype)
			}
		})
	}
}

func TestCheckSVG(t *testing.T) {
	for _, bad := range []string{
		`<svg><script>alert(1)</script></svg>`,
		`<svg onload="alert(1)"><rect/></svg>`,
		`<svg><rect onclick = "x()"/></svg>`,
		`<svg><foreignObject><div/></foreignObject></svg>`,
		`<?xml version="1.0"?><!DOCTYPE svg [<!ENTITY x "y">]><svg>&x;</svg>`,
		`<svg><image href="https://tracker.example/p.png"/></svg>`,
		`<svg><use xlink:href="//evil.example/s.svg#a"/></svg>`,
		`<svg><a href="javascript:alert(1)"><rect/></a></svg>`,
		`<svg><style>@import url(https://x.example/a.css);</style></svg>`,
		`<svg><rect style="fill:url('https://x.example/p')"/></svg>`,
		`<html><svg/></html>`,
		`plain text`,
	} {
		if _, err := Check([]byte(bad)); err == nil {
			t.Errorf("Check(%q) accepted it", bad)
		}
	}
	for _, good := range []string{string(safeSVG), "\xef\xbb\xbf  <svg viewBox=\"0 0 1 1\"><path d=\"M0 0h1v1z\"/></svg>", `<!-- logo --><svg><image href="data:image/png;base64,iVBORw0KGgo="/></svg>`} {
		if ct, err := Check([]byte(good)); err != nil || ct != TypeSVG {
			t.Errorf("Check(%q) = %q, %v", good, ct, err)
		}
	}
}

// seed saves a board with tiles and an instance with a URL.
func seed(t *testing.T, st *store.Store, tiles map[string]any, instanceURL string) {
	t.Helper()
	raw, _ := json.Marshal(tiles)
	err := st.WriteTx(t.Context(), func(tx *sql.Tx) error {
		if err := boards.CreateTx(t.Context(), tx, boards.Detail{ID: "b_home", Slug: "home", Name: "Home"}); err != nil {
			return err
		}
		if _, err := boards.AppendLayoutTx(t.Context(), tx, "b_home", 1, json.RawMessage(`{"sections":[]}`), raw, ""); err != nil {
			return err
		}
		if instanceURL != "" {
			cfg, _ := json.Marshal(map[string]any{"url": instanceURL, "api_key": "${secret:instance/i_s/api_key}"})
			_, err := tx.ExecContext(t.Context(), `INSERT INTO integration_instances (id, integration, name, config, created_at, updated_at) VALUES ('i_s', 'sonarr', 'Sonarr', ?, 0, 0)`, string(cfg))
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSyncServeAndCache(t *testing.T) {
	st := openStore(t)
	var hits atomic.Int32
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch r.URL.Path {
		case "/logo.png":
			image(TypePNG, pngBytes)(w, r)
		case "/favicon.svg":
			image(TypeSVG, safeSVG)(w, r)
		default: // no /favicon.ico: the origin falls back to /favicon.svg
			http.NotFound(w, r)
		}
	})
	seed(t, st, map[string]any{
		"a": map[string]any{"name": "TV", "icon": srv.URL + "/logo.png"},
		"b": map[string]any{"name": "Films", "icon": "radarr.png", "href": srv.URL + "/movies"},
	}, srv.URL+"/sonarr")
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	s := newService(t, st, false)
	s.now = func() time.Time { return now }
	if err := s.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	m, err := s.Map(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	logo, fav := m.ByIcon[srv.URL+"/logo.png"], m.ByOrigin[srv.URL]
	if logo == "" || fav == "" || m.ByInstance["i_s"] != fav || m.ByIcon["radarr.png"] != "" {
		t.Fatalf("map = %+v", m)
	}

	mux := http.NewServeMux()
	s.Register(mux, func(h http.Handler) http.Handler { return h }, nil)
	get := func(path string, hdr ...string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		for i := 0; i+1 < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	rec := get(logo)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != TypePNG || !bytes.Equal(rec.Body.Bytes(), pngBytes) {
		t.Fatalf("GET logo = %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "private, max-age=31536000, immutable" {
		t.Errorf("Cache-Control = %q", cc)
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "sandbox") || !strings.Contains(csp, "default-src 'none'") {
		t.Errorf("CSP = %q", csp)
	}
	etag := rec.Header().Get("ETag")
	plain := strings.Split(logo, "?")[0]
	if rec := get(plain); rec.Header().Get("Cache-Control") != "private, max-age=604800" || rec.Header().Get("ETag") != etag {
		t.Errorf("without v: %q %q", rec.Header().Get("Cache-Control"), rec.Header().Get("ETag"))
	}
	if rec := get(plain, "If-None-Match", etag); rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
		t.Errorf("If-None-Match = %d", rec.Code)
	}
	if rec := get(strings.Split(fav, "?")[0]); rec.Header().Get("Content-Type") != TypeSVG || rec.Header().Get("Content-Disposition") != `inline; filename="icon.svg"` {
		t.Errorf("favicon = %q %q", rec.Header().Get("Content-Type"), rec.Header().Get("Content-Disposition"))
	}
	for _, p := range []string{"/api/icons/ic_nosuchicon00", "/api/icons/..%2f..%2fetc", "/api/icons/x"} {
		if rec := get(p); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d", p, rec.Code)
		}
	}
	if rec := get("/api/icons"); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"by_instance":{"i_s"`) {
		t.Errorf("GET /api/icons = %d %s", rec.Code, rec.Body.String())
	}

	// Nothing is due again for a week.
	before := hits.Load()
	now = now.Add(24 * time.Hour)
	if err := s.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != before {
		t.Fatalf("a fresh icon was fetched again (%d → %d)", before, hits.Load())
	}
	now = now.Add(7 * 24 * time.Hour)
	if err := s.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	if hits.Load() == before {
		t.Fatal("a week-old icon wasn't refreshed")
	}
}

func TestFailuresRetryAndForget(t *testing.T) {
	st := openStore(t)
	var hits atomic.Int32
	down := atomic.Bool{}
	down.Store(true)
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if down.Load() {
			http.Error(w, "nope", http.StatusServiceUnavailable)
			return
		}
		image(TypePNG, pngBytes)(w, r)
	})
	seed(t, st, map[string]any{"a": map[string]any{"icon": srv.URL + "/i.png"}}, "")
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	s := newService(t, st, false)
	s.now = func() time.Time { return now }
	if err := s.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	var msg string
	_ = st.Reader().QueryRow(`SELECT error FROM icons`).Scan(&msg)
	if !strings.Contains(msg, "503") {
		t.Fatalf("error = %q", msg)
	}
	before := hits.Load()
	now = now.Add(time.Hour)
	_ = s.Sync(t.Context())
	if hits.Load() != before {
		t.Fatal("retried within six hours")
	}
	down.Store(false)
	now = now.Add(6 * time.Hour)
	_ = s.Sync(t.Context())
	if m, _ := s.Map(t.Context()); len(m.ByIcon) != 1 {
		t.Fatalf("after the retry: %+v", m)
	}

	// Unreferenced for 30 days: forgotten.
	if _, err := st.Writer().Exec(`UPDATE layouts SET tiles = '{}'`); err != nil {
		t.Fatal(err)
	}
	now = now.Add(31 * 24 * time.Hour)
	_ = s.Sync(t.Context())
	var n int
	_ = st.Reader().QueryRow(`SELECT count(*) FROM icons`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d icons left", n)
	}
}

// TestNoFetchInDemo: with fetching off, sources are recorded and nothing
// reaches the network.
func TestNoFetchInDemo(t *testing.T) {
	st := openStore(t)
	var hits atomic.Int32
	srv := serve(t, func(w http.ResponseWriter, _ *http.Request) { hits.Add(1) })
	seed(t, st, map[string]any{"a": map[string]any{"href": srv.URL}}, "")
	s := New(Options{Store: st})
	s.allowed = func(netip.Addr) bool { return true }
	if err := s.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 0 {
		t.Fatal("fetched with fetching off")
	}
}

func TestSources(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"HTTPS://Sonarr.Lab.Example:8989/calendar?x=1#y", "https://sonarr.lab.example:8989"},
		{"http://10.88.0.5", "http://10.88.0.5"},
		{"https://photos.lab.example:443/x", "https://photos.lab.example"},
		{"http://[fd12::5]:80/", "http://[fd12::5]"},
	} {
		if got, ok := Origin(c.in); !ok || got != c.want {
			t.Errorf("Origin(%q) = %q, %v", c.in, got, ok)
		}
	}
	for _, bad := range []string{"sonarr.png", "mdi-home", "si-plex", "javascript:alert(1)", "data:image/png;base64,AAAA", "//cdn.example/x.png", "https://user:pw@x.example/i.png"} {
		if _, ok := IconURL(bad); ok {
			t.Errorf("IconURL(%q) accepted it", bad)
		}
	}
}
