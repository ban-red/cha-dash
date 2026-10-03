package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"dash.cha.sh/internal/auth"
	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/httpx"
	"dash.cha.sh/internal/realtime"
	"dash.cha.sh/internal/store"
)

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

type env struct {
	srv   *httptest.Server
	g     *graph.Graph
	token string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	logs := &lockedBuffer{}
	log := slog.New(slog.NewTextHandler(logs, nil))
	st, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "chadash.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	a, err := auth.New(t.Context(), st, log, httpx.Proxies{})
	if err != nil {
		t.Fatal(err)
	}
	g := graph.New()
	hub := realtime.New(realtime.GraphSource{Graph: g}, realtime.Options{Coalesce: 10 * time.Millisecond, Log: log})
	h, err := New(Deps{Log: log, Store: st, Auth: a, Realtime: hub, AllowedOrigins: []string{"https://dash.example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(func() {
		hub.Close()
		srv.Close()
	})
	m := regexp.MustCompile(`setup_token=(\S+)`).FindStringSubmatch(logs.String())
	if m == nil {
		t.Fatal("no setup token logged")
	}
	return &env{srv: srv, g: g, token: m[1]}
}

func (e *env) request(t *testing.T, c *http.Client, method, path string, body any, hdr map[string]string) *http.Response {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestEndToEnd(t *testing.T) {
	e := newEnv(t)
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}

	resp := e.request(t, c, "GET", "/healthz", nil, nil)
	if resp.StatusCode != 200 || resp.Header.Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(resp.Header.Get("Content-Security-Policy"), "default-src 'self'") {
		t.Fatalf("healthz: %d %v", resp.StatusCode, resp.Header)
	}
	if resp := e.request(t, c, "GET", "/", nil, nil); resp.StatusCode != 200 {
		t.Fatalf("placeholder: %d", resp.StatusCode)
	}
	if resp := e.request(t, c, "GET", "/api/nope", nil, nil); resp.StatusCode != 404 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("unknown api: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	for _, p := range []string{"/api/version", "/api/auth/session", "/ws"} {
		if resp := e.request(t, c, "GET", p, nil, nil); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s without a session: %d", p, resp.StatusCode)
		}
	}

	setup := map[string]string{"token": e.token, "username": "owner", "password": "correct horse battery"}
	// A cross-site POST is refused before it reaches the handler.
	if resp := e.request(t, c, "POST", "/api/setup", setup, map[string]string{"Origin": "https://evil.example", "Sec-Fetch-Site": "cross-site"}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-site setup: %d", resp.StatusCode)
	}
	// A configured extra origin is accepted.
	if resp := e.request(t, c, "POST", "/api/setup", setup, map[string]string{"Origin": "https://dash.example.com", "Sec-Fetch-Site": "cross-site"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("setup from trusted origin: %d", resp.StatusCode)
	}
	if resp := e.request(t, c, "GET", "/api/version", nil, nil); resp.StatusCode != 200 {
		t.Fatalf("version with session: %d", resp.StatusCode)
	}

	// The WebSocket needs the session cookie.
	u := "ws" + strings.TrimPrefix(e.srv.URL, "http") + "/ws"
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, u, &websocket.DialOptions{HTTPClient: c})
	if err != nil {
		t.Fatalf("dial with session: %v", err)
	}
	defer ws.CloseNow()
	write := func(v any) {
		b, _ := json.Marshal(v)
		if err := ws.Write(ctx, websocket.MessageText, b); err != nil {
			t.Fatal(err)
		}
	}
	read := func() map[string]any {
		for {
			_, b, err := ws.Read(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var f map[string]any
			json.Unmarshal(b, &f)
			if f["op"] != "ping" {
				return f
			}
		}
	}
	write(map[string]any{"op": "hello", "v": 1})
	if f := read(); f["op"] != "hello" {
		t.Fatalf("hello: %v", f)
	}
	write(map[string]any{"op": "sub", "id": 1, "topics": []string{"graph"}})
	if f := read(); f["op"] != "snap" {
		t.Fatalf("snap: %v", f)
	}
	e.g.Update(func(tx *graph.Tx) error {
		return tx.Upsert(graph.Resource{ID: "container:sonarr", Kind: "container", State: "running"})
	})
	if f := read(); f["op"] != "diff" {
		t.Fatalf("diff: %v", f)
	}

	// Without the cookie the upgrade is refused.
	if _, resp, err := websocket.Dial(ctx, u, nil); err == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("dial without session should be 401, got %v", err)
	}
}

func TestServeShutsDownGracefully(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	slow := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /slow", func(w http.ResponseWriter, r *http.Request) {
		close(slow)
		time.Sleep(200 * time.Millisecond)
		io.WriteString(w, "done")
	})
	ctx, cancel := context.WithCancel(t.Context())
	hookRan := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, ln, mux, slog.New(slog.DiscardHandler), func() { close(hookRan) })
	}()

	// An in-flight request survives the shutdown signal.
	got := make(chan string, 1)
	go func() {
		resp, err := http.Get("http://" + ln.Addr().String() + "/slow")
		if err != nil {
			got <- err.Error()
			return
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		got <- string(b)
	}()
	<-slow
	cancel()
	if body := <-got; body != "done" {
		t.Fatalf("in-flight request got %q", body)
	}
	if err := <-done; err != nil {
		t.Fatalf("Serve returned %v", err)
	}
	select {
	case <-hookRan:
	default:
		t.Fatal("onShutdown hook did not run")
	}
	if _, err := net.DialTimeout("tcp", ln.Addr().String(), time.Second); err == nil {
		t.Fatal("listener still accepting after shutdown")
	}
}
