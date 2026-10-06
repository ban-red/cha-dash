package httpx

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func TestProxyCheck(t *testing.T) {
	var logs bytes.Buffer
	c := NewProxyCheck(slog.New(slog.NewTextHandler(&logs, nil)), NewProxies([]netip.Prefix{netip.MustParsePrefix("10.0.0.1/32")}), "https://dash.example")
	h := c.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	send := func(method, path, peer string, hdr map[string]string) {
		r := httptest.NewRequest(method, path, nil)
		r.RemoteAddr = peer + ":5555"
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		h.ServeHTTP(httptest.NewRecorder(), r)
	}

	// A trusted proxy that says https: nothing to note.
	send("POST", "/api/auth/login", "10.0.0.1", map[string]string{"X-Forwarded-For": "192.0.2.7", "X-Forwarded-Proto": "https"})
	// A direct request without forwarded headers to a page: nothing either.
	send("GET", "/", "192.0.2.8", nil)
	if n := c.Notes(); len(n) != 0 {
		t.Fatalf("notes = %v, want none", n)
	}

	// An untrusted peer with forwarded headers, twice: one note, one log line.
	for range 2 {
		send("GET", "/api/session", "10.0.0.9", map[string]string{"X-Forwarded-For": "192.0.2.7"})
	}
	// A sign-in over plain HTTP although the public URL is https.
	send("POST", "/api/auth/login", "192.0.2.8", nil)

	n := c.Notes()
	if len(n) != 2 || n[0].Code != NoteUntrustedProxy || n[1].Code != NotePlainSignIn {
		t.Fatalf("notes = %+v", n)
	}
	if strings.Contains(n[0].Message, "10.0.0.9") {
		t.Error("the note must not name the peer: anyone can send X-Forwarded-For")
	}
	if got := strings.Count(logs.String(), "level=WARN"); got != 2 {
		t.Errorf("%d warnings logged, want 2:\n%s", got, logs.String())
	}
	if !strings.Contains(logs.String(), "peer=10.0.0.9") {
		t.Error("the log should name the peer")
	}
}

func TestProxyCheckHTTPPublicURL(t *testing.T) {
	c := NewProxyCheck(slog.New(slog.DiscardHandler), NewProxies(nil), "http://dash.example:3696")
	r := httptest.NewRequest("POST", "/api/auth/login", nil)
	c.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(httptest.NewRecorder(), r)
	if n := c.Notes(); len(n) != 0 {
		t.Fatalf("plain-HTTP sign-in with an http:// public URL noted: %v", n)
	}
	var nilCheck *ProxyCheck
	if nilCheck.Notes() != nil {
		t.Fatal("a nil check has no notes")
	}
}
