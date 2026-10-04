package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"dash.cha.sh/internal/boards"
	"dash.cha.sh/internal/firstpaint"
	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/store"
	"dash.cha.sh/internal/topics"
	"dash.cha.sh/internal/webui"
)

// shell is a built index.html in miniature: no inline script or style.
const shell = `<!doctype html><html lang="en"><head><meta charset="utf-8"><title>Cha Dash</title>` +
	`<script src="/boot.js"></script><script type="module" crossorigin src="/assets/index-abc.js"></script></head>` +
	`<body><div id="app"></div></body></html>`

func webFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html":          {Data: []byte(shell)},
		"boot.js":             {Data: []byte("1")},
		"assets/index-abc.js": {Data: []byte("2")},
	}
}

// firstPaintEnv is a hub with the web app and first-paint snapshots on, and
// a graph with one resource whose name tries to break out of the data block.
func firstPaintEnv(t *testing.T) (*env, *store.Store) {
	t.Helper()
	var st *store.Store
	e := newEnvWith(t, func(d *Deps) {
		st = d.Store
		tp := topics.New(graph.New(), time.Date(2026, 10, 3, 14, 5, 0, 0, time.UTC))
		if _, err := tp.Graph.Update(func(tx *graph.Tx) error {
			return tx.Upsert(graph.Resource{ID: "app:x", Kind: "app", Name: `</script><script>alert(1)</script>`})
		}); err != nil {
			t.Fatal(err)
		}
		d.Web = webui.Handler(webFS())
		d.FirstPaint = &firstpaint.Builder{Topics: tp, Boards: boards.New(d.Store)}
	})
	return e, st
}

var dataBlock = regexp.MustCompile(`<script type="application/json" id="chadash-snapshot">([^<]*)</script>`)

// page fetches path and returns the body and the inline snapshot, if any.
func (e *env) page(t *testing.T, c *http.Client, path string) (*http.Response, string, *firstpaint.Snapshot) {
	t.Helper()
	resp := e.request(t, c, "GET", path, nil, nil)
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	m := dataBlock.FindSubmatch(b)
	if m == nil {
		if strings.Contains(string(b), webui.SnapshotID) {
			t.Fatalf("%s: a snapshot that isn't a clean data block:\n%s", path, b)
		}
		return resp, string(b), nil
	}
	var s firstpaint.Snapshot
	if err := json.Unmarshal(m[1], &s); err != nil {
		t.Fatalf("%s: data block isn't JSON: %v", path, err)
	}
	return resp, string(b), &s
}

func TestFirstPaintOnlyForTheOwner(t *testing.T) {
	e, st := firstPaintEnv(t)
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}

	// Signed out: the generic shell, cacheable by validator like any static file.
	resp, body, snap := e.page(t, c, "/")
	if snap != nil || body != shell {
		t.Fatalf("anonymous: got a snapshot or a changed page:\n%s", body)
	}
	if resp.Header.Get("Cache-Control") != "no-cache" || resp.Header.Get("ETag") == "" {
		t.Fatalf("anonymous: headers %v", resp.Header)
	}
	// A made-up cookie is nobody.
	u := e.srv.URL
	req, _ := http.NewRequest("GET", u+"/", nil)
	req.AddCookie(&http.Cookie{Name: "chadash_session", Value: "forged"})
	if r2, err := http.DefaultClient.Do(req); err != nil {
		t.Fatal(err)
	} else {
		b, _ := io.ReadAll(r2.Body)
		r2.Body.Close()
		if string(b) != shell {
			t.Fatalf("forged cookie: %s", b)
		}
	}

	if resp := e.request(t, c, "POST", "/api/setup", map[string]string{"token": e.token, "username": "owner", "password": "correct horse battery"}, nil); resp.StatusCode != http.StatusCreated {
		t.Fatalf("setup: %d", resp.StatusCode)
	}
	for _, path := range []string{"/", "/boards/anything", "/settings/integrations"} {
		resp, body, snap := e.page(t, c, path)
		if snap == nil {
			t.Fatalf("%s: signed-in owner got no snapshot:\n%s", path, body)
		}
		if snap.V != firstpaint.Version || snap.User.Username != "owner" || snap.User.Role != "owner" || snap.Board == nil || snap.Board.Name != "Home" {
			t.Fatalf("%s: snapshot %+v", path, snap)
		}
		if snap.Graph.Resources["app:x"].Name != `</script><script>alert(1)</script>` {
			t.Fatalf("%s: the resource name didn't round-trip", path)
		}
		if got := resp.Header.Get("Cache-Control"); got != "no-store" {
			t.Fatalf("%s: personalised page with Cache-Control %q", path, got)
		}
		if resp.Header.Get("ETag") != "" {
			t.Fatalf("%s: personalised page with an ETag", path)
		}
		assertNothingInlineRuns(t, resp, body)
	}
	// Static files never carry it.
	if _, body, snap := e.page(t, c, "/assets/index-abc.js"); snap != nil || body != "2" {
		t.Fatalf("asset: %q", body)
	}

	// Another role (they arrive with RBAC) gets nothing.
	if _, err := st.Writer().ExecContext(t.Context(), `UPDATE users SET role = 'viewer'`); err != nil {
		t.Fatal(err)
	}
	if _, _, snap := e.page(t, c, "/"); snap != nil {
		t.Fatal("a non-owner got the snapshot")
	}
	if _, err := st.Writer().ExecContext(t.Context(), `UPDATE users SET role = 'owner'`); err != nil {
		t.Fatal(err)
	}

	// Signed out again: nothing.
	if resp := e.request(t, c, "POST", "/api/auth/logout", nil, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("logout: %d", resp.StatusCode)
	}
	if _, body, snap := e.page(t, c, "/"); snap != nil || body != shell {
		t.Fatal("snapshot after sign-out")
	}
}

// assertNothingInlineRuns checks the CSP and that every script element is
// either a file or the JSON data block, which browsers never execute.
func assertNothingInlineRuns(t *testing.T, resp *http.Response, body string) {
	t.Helper()
	csp := resp.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "default-src 'self'") || strings.Contains(csp, "unsafe-inline") || strings.Contains(csp, "script-src") {
		t.Fatalf("CSP %q", csp)
	}
	tags := regexp.MustCompile(`(?i)<script\b[^>]*>`).FindAllString(body, -1)
	blocks := 0
	for _, tag := range tags {
		switch {
		case strings.Contains(tag, ` src="`):
		case tag == `<script type="application/json" id="chadash-snapshot">`:
			blocks++
		default:
			t.Fatalf("inline script %s", tag)
		}
	}
	if blocks != 1 || strings.Count(strings.ToLower(body), "</script") != len(tags) {
		t.Fatalf("%d data blocks, %d script tags, %d closing tags", blocks, len(tags), strings.Count(strings.ToLower(body), "</script"))
	}
	if strings.Contains(body, "<!--") || strings.Contains(strings.ToLower(body), "style=") || strings.Contains(strings.ToLower(body), "<style") {
		t.Fatal("comment or inline style in the page")
	}
}
