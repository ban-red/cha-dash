package server

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"dash.cha.sh/internal/boards"
	"dash.cha.sh/internal/firstpaint"
	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/topics"
	"dash.cha.sh/internal/webui"
)

// Setting Accept-Encoding by hand stops net/http's client from decoding
// gzip for us, so the tests see what the hub sent.
var acceptGzip = map[string]string{"Accept-Encoding": "gzip, deflate, br, zstd"}

func gunzipBody(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Header.Get("Content-Encoding") != "gzip" {
		return b
	}
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("Content-Encoding gzip, but: %v", err)
	}
	out, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestCompressiblePaths(t *testing.T) {
	for path, want := range map[string]bool{
		"/api/boards":                          true,
		"/api/boards/b_1/layout":               true,
		"/api/integrations/catalog":            true,
		"/api/integrations/instances/i_1/test": true,
		"/api/onboarding":                      true,
		"/api/authority":                       true, // not /api/auth
		"/api/auth":                            false,
		"/api/auth/session":                    false,
		"/api/auth/passkeys/register/begin":    false,
		"/api/setup":                           false,
		"/api/setup/":                          false,
		"/api/integrations/authorize":          false,
		"/api/integrations/authorize/plex":     false,
		"/api/integrations/authorize/plex/a_1": false,
		"/healthz":                             false,
		"/ws":                                  false,
		"/":                                    false, // the web app compresses its own files
		"/assets/index-abc.js":                 false,
		"/api":                                 false,
		"/apiary":                              false,
		"/api/integrations/authorized-but-public": true,
		"/api/stacks/hooks":                       false, // a hook's secret, once
		"/api/stacks/hooks/whk_1/rotate":          false,
		"/api/stacks/local/media":                 true,
	} {
		if got := compressible(path); got != want {
			t.Errorf("compressible(%q) = %v, want %v", path, got, want)
		}
	}
}

// TestCompressAPIRoutes runs compressAPI over a handler that answers every
// path with a large JSON body: only the allowed API routes come back gzipped.
func TestCompressAPIRoutes(t *testing.T) {
	big := `{"items":[` + strings.Repeat(`{"id":"x","state":"running"},`, 60) + `{}]}`
	h := compressAPI(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, big)
	}))
	for path, want := range map[string]bool{
		"/api/integrations/catalog":           true,
		"/api/auth/session":                   false,
		"/api/setup":                          false,
		"/api/integrations/authorize/plex/a1": false,
		"/healthz":                            false,
		"/ws":                                 false,
		"/":                                   false,
	} {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Accept-Encoding", "gzip")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if got := rec.Header().Get("Content-Encoding") == "gzip"; got != want {
			t.Errorf("%s: gzipped %v, want %v", path, got, want)
		}
		if !want && rec.Body.String() != big {
			t.Errorf("%s: body changed", path)
		}
	}
}

func TestAPICompressionEndToEnd(t *testing.T) {
	e := newEnv(t)
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	if resp := e.request(t, c, "POST", "/api/setup", map[string]string{"username": "owner", "password": "correct horse battery"}, acceptGzip); resp.StatusCode != http.StatusCreated || resp.Header.Get("Content-Encoding") != "" {
		t.Fatalf("setup: %d %q", resp.StatusCode, resp.Header.Get("Content-Encoding"))
	}
	var list struct{ Boards []boards.Board }
	if err := json.NewDecoder(e.request(t, c, "GET", "/api/boards", nil, nil).Body).Decode(&list); err != nil || len(list.Boards) == 0 {
		t.Fatalf("boards: %v", err)
	}
	layout := "/api/boards/" + list.Boards[0].ID + "/layout"
	if resp := e.request(t, c, "PUT", layout, map[string]any{"revision": 0, "format_version": 1, "layout": demoLayout()}, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("save layout: %d", resp.StatusCode)
	}

	plain := e.request(t, c, "GET", layout, nil, map[string]string{"Accept-Encoding": "identity"})
	zipped := e.request(t, c, "GET", layout, nil, acceptGzip)
	pb, zb := gunzipBody(t, plain), gunzipBody(t, zipped)
	if plain.Header.Get("Content-Encoding") != "" || zipped.Header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("encodings %q and %q", plain.Header.Get("Content-Encoding"), zipped.Header.Get("Content-Encoding"))
	}
	if len(pb) < 1024 || !bytes.Equal(pb, zb) {
		t.Fatalf("the layout (%d bytes) changed when gzipped", len(pb))
	}
	for _, resp := range []*http.Response{plain, zipped} {
		if !slices.Contains(resp.Header.Values("Vary"), "Accept-Encoding") || resp.Header.Get("Cache-Control") != "no-store" {
			t.Fatalf("headers %v", resp.Header)
		}
		if resp.Header.Get("Content-Security-Policy") == "" {
			t.Fatal("security headers lost")
		}
	}

	// Small answers, health checks and the session stay as they are.
	for _, p := range []string{"/api/version", "/api/auth/session", "/healthz"} {
		resp := e.request(t, c, "GET", p, nil, acceptGzip)
		if resp.StatusCode != 200 || resp.Header.Get("Content-Encoding") != "" {
			t.Errorf("%s: %d %q", p, resp.StatusCode, resp.Header.Get("Content-Encoding"))
		}
	}
}

func TestWebSocketUntouchedByCompression(t *testing.T) {
	e := newEnv(t)
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	if resp := e.request(t, c, "POST", "/api/setup", map[string]string{"username": "owner", "password": "correct horse battery"}, nil); resp.StatusCode != http.StatusCreated {
		t.Fatalf("setup: %d", resp.StatusCode)
	}
	// Enough resources that the snapshot is well over the gzip minimum.
	e.g.Update(func(tx *graph.Tx) error {
		for i := range 50 {
			if err := tx.Upsert(graph.Resource{ID: graph.RID(fmt.Sprintf("container:c%d", i)), Kind: "container", Name: fmt.Sprintf("container %d", i), State: "running"}); err != nil {
				return err
			}
		}
		return nil
	})

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	u := "ws" + strings.TrimPrefix(e.srv.URL, "http") + "/ws"
	ws, resp, err := websocket.Dial(ctx, u, &websocket.DialOptions{HTTPClient: c, HTTPHeader: http.Header{"Accept-Encoding": {"gzip"}}})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer ws.CloseNow()
	if resp.StatusCode != http.StatusSwitchingProtocols || resp.Header.Get("Content-Encoding") != "" || slices.Contains(resp.Header.Values("Vary"), "Accept-Encoding") {
		t.Fatalf("handshake: %d %v", resp.StatusCode, resp.Header)
	}
	send := func(v any) {
		b, _ := json.Marshal(v)
		if err := ws.Write(ctx, websocket.MessageText, b); err != nil {
			t.Fatal(err)
		}
	}
	recv := func() map[string]any {
		for {
			_, b, err := ws.Read(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var f map[string]any
			if err := json.Unmarshal(b, &f); err != nil {
				t.Fatalf("a frame that isn't JSON (compressed?): %v", err)
			}
			if f["op"] != "ping" {
				return f
			}
		}
	}
	send(map[string]any{"op": "hello", "v": 1})
	if f := recv(); f["op"] != "hello" {
		t.Fatalf("hello: %v", f)
	}
	send(map[string]any{"op": "sub", "id": 1, "topics": []string{"graph"}})
	if f := recv(); f["op"] != "snap" {
		t.Fatalf("snap: %v", f)
	}
}

func TestFirstPaintCompressed(t *testing.T) {
	e := newEnvWith(t, func(d *Deps) {
		tp := topics.New(graph.New(), time.Date(2026, 10, 3, 14, 5, 0, 0, time.UTC))
		if _, err := tp.Graph.Update(func(tx *graph.Tx) error {
			if err := tx.Upsert(graph.Resource{ID: "app:x", Kind: "app", Name: `</script><script>alert(1)</script>`}); err != nil {
				return err
			}
			for i := range 30 { // a page over the gzip minimum
				if err := tx.Upsert(graph.Resource{ID: graph.RID(fmt.Sprintf("container:c%d", i)), Kind: "container", Name: fmt.Sprintf("container %d", i), State: "running"}); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		d.Web = webui.Handler(webFS())
		d.FirstPaint = &firstpaint.Builder{Topics: tp, Boards: boards.New(d.Store)}
	})
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}

	// Signed out: the generic shell, too small to compress.
	if resp := e.request(t, c, "GET", "/", nil, acceptGzip); resp.Header.Get("Content-Encoding") != "" || string(gunzipBody(t, resp)) != shell {
		t.Fatalf("anonymous: %q", resp.Header.Get("Content-Encoding"))
	}
	if resp := e.request(t, c, "POST", "/api/setup", map[string]string{"username": "owner", "password": "correct horse battery"}, nil); resp.StatusCode != http.StatusCreated {
		t.Fatalf("setup: %d", resp.StatusCode)
	}

	plainResp := e.request(t, c, "GET", "/boards/home", nil, map[string]string{"Accept-Encoding": "identity"})
	plain := gunzipBody(t, plainResp)
	resp := e.request(t, c, "GET", "/boards/home", nil, acceptGzip)
	page := gunzipBody(t, resp)
	if resp.Header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("the owner's page (%d bytes) wasn't gzipped", len(page))
	}
	if !bytes.Equal(page, plain) {
		t.Fatal("the gzipped page differs from the identity page")
	}
	m := dataBlock.FindSubmatch(page)
	if m == nil {
		t.Fatalf("no clean data block in the gzipped page:\n%s", page)
	}
	var snap firstpaint.Snapshot
	if err := json.Unmarshal(m[1], &snap); err != nil || snap.Graph.Resources["app:x"].Name != `</script><script>alert(1)</script>` {
		t.Fatalf("snapshot: %v", err)
	}
	assertNothingInlineRuns(t, resp, string(page))
	if resp.Header.Get("Cache-Control") != "no-store" || resp.Header.Get("ETag") != "" {
		t.Fatalf("personalised page: Cache-Control %q, ETag %q", resp.Header.Get("Cache-Control"), resp.Header.Get("ETag"))
	}
	if v := resp.Header.Values("Vary"); !slices.Contains(v, "Cookie") || !slices.Contains(v, "Accept-Encoding") {
		t.Fatalf("Vary %q", v)
	}
}
