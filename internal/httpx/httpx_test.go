package httpx

import (
	"bytes"
	"crypto/tls"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func TestClientIP(t *testing.T) {
	p := NewProxies([]netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")})
	cases := []struct {
		name, remote, xff, want string
	}{
		{"direct", "192.0.2.10:5000", "", "192.0.2.10"},
		{"untrusted peer can't spoof", "192.0.2.10:5000", "198.51.100.1", "192.0.2.10"},
		{"trusted proxy", "10.0.0.2:5000", "198.51.100.1", "198.51.100.1"},
		{"rightmost untrusted wins", "10.0.0.2:5000", "203.0.113.9, 198.51.100.1, 10.0.0.3", "198.51.100.1"},
		{"all trusted", "10.0.0.2:5000", "10.0.0.5", "10.0.0.5"},
		{"malformed hop", "10.0.0.2:5000", "garbage", "10.0.0.2"},
		{"mapped v4", "[::ffff:192.0.2.10]:5000", "", "192.0.2.10"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			r.RemoteAddr = c.remote
			if c.xff != "" {
				r.Header.Set("X-Forwarded-For", c.xff)
			}
			if got := p.ClientIP(r).String(); got != c.want {
				t.Fatalf("ClientIP = %s, want %s", got, c.want)
			}
		})
	}
}

func TestIsHTTPS(t *testing.T) {
	p := NewProxies([]netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")})
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "192.0.2.1:1"
	r.Header.Set("X-Forwarded-Proto", "https")
	if p.IsHTTPS(r) {
		t.Fatal("untrusted X-Forwarded-Proto must be ignored")
	}
	r.RemoteAddr = "10.1.2.3:1"
	if !p.IsHTTPS(r) {
		t.Fatal("trusted X-Forwarded-Proto: https should count")
	}
	r.Header.Set("X-Forwarded-Proto", "http")
	if p.IsHTTPS(r) {
		t.Fatal("http is not https")
	}
	r.Header.Del("X-Forwarded-Proto")
	r.TLS = &tls.ConnectionState{}
	if !p.IsHTTPS(r) {
		t.Fatal("direct TLS should count")
	}
}

func TestDecodeJSON(t *testing.T) {
	type body struct {
		Name string `json:"name"`
	}
	cases := []struct {
		name, ctype, body string
		status            int
	}{
		{"ok", "application/json", `{"name":"x"}`, 0},
		{"charset ok", "application/json; charset=utf-8", `{"name":"x"}`, 0},
		{"form post", "application/x-www-form-urlencoded", `name=x`, http.StatusUnsupportedMediaType},
		{"unknown field", "application/json", `{"name":"x","admin":true}`, http.StatusBadRequest},
		{"trailing data", "application/json", `{"name":"x"}{}`, http.StatusBadRequest},
		{"too big", "application/json", `{"name":"` + strings.Repeat("a", MaxBodyBytes) + `"}`, http.StatusRequestEntityTooLarge},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/", strings.NewReader(c.body))
			r.Header.Set("Content-Type", c.ctype)
			w := httptest.NewRecorder()
			var b body
			ok := DecodeJSON(w, r, &b)
			if c.status == 0 {
				if !ok || b.Name != "x" {
					t.Fatalf("ok=%v body=%+v resp=%s", ok, b, w.Body)
				}
				return
			}
			if ok || w.Code != c.status {
				t.Fatalf("ok=%v status=%d, want %d", ok, w.Code, c.status)
			}
		})
	}
}

func TestMiddleware(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/boom", func(http.ResponseWriter, *http.Request) { panic("kaboom") })
	mux.HandleFunc("GET /api/teapot", func(w http.ResponseWriter, r *http.Request) {
		// The recorder must not hide the underlying writer from ResponseController.
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Errorf("Flush through recorder: %v", err)
		}
		w.WriteHeader(http.StatusTeapot)
	})
	h := SecurityHeaders(AccessLog(log, Proxies{})(Recover(log)(mux)))

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/boom", nil))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("panic status = %d", w.Code)
	}
	if w.Header().Get("Content-Security-Policy") == "" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("missing security headers: %v", w.Header())
	}
	if !strings.Contains(logs.String(), "kaboom") || !strings.Contains(logs.String(), "status=500") {
		t.Fatalf("logs = %q", logs.String())
	}

	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/teapot", nil))
	if !strings.Contains(logs.String(), "status=418") {
		t.Fatalf("status not recorded: %q", logs.String())
	}

	logs.Reset()
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
	if logs.Len() != 0 {
		t.Fatalf("healthz should log at debug only: %q", logs.String())
	}
	io.Copy(io.Discard, w.Body)
}
