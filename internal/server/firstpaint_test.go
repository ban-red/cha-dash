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
	"dash.cha.sh/internal/livedoc"
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

// firstPaintEnv is a hub with the web app and first-paint snapshots on, a
// graph with one resource whose name tries to break out of the data block,
// and an inbox with a problem on it (with facts), a repair and a security
// finding.
func firstPaintEnv(t *testing.T) (*env, *store.Store, *firstpaint.Builder) {
	t.Helper()
	var st *store.Store
	var fp *firstpaint.Builder
	e := newEnvWith(t, func(d *Deps) {
		st = d.Store
		tp := topics.New(graph.New(), time.Date(2026, 10, 3, 14, 5, 0, 0, time.UTC))
		if _, err := tp.Graph.Update(func(tx *graph.Tx) error {
			return tx.Upsert(graph.Resource{ID: "app:x", Kind: "app", Name: `</script><script>alert(1)</script>`})
		}); err != nil {
			t.Fatal(err)
		}
		tp.Inbox.Update(func(tx *livedoc.Tx) error {
			for id, in := range map[string]string{
				"x-down":   `{"id":"x-down","class":"problem","severity":"down","status":"open","title":"X is down","target":"app:x","facts":{"exit_code":137},"causes":[{"text":"out of memory","resource":"app:x"}],"blast_radius":[],"suggested":{"id":"x.restart","label":"Restart","tier":"T2","target":"app:x"}}`,
				"x-repair": `{"id":"x-repair","class":"repair","severity":"attention","status":"open","title":"Reconnect X","target":"app:x","causes":[],"blast_radius":[]}`,
				"x-socket": `{"id":"x-socket","class":"problem","category":"security","severity":"info","status":"open","title":"Socket open","target":"app:x","causes":[],"blast_radius":[]}`,
			} {
				tx.Put(topics.Incidents, id, json.RawMessage(in))
			}
			return nil
		})
		d.Web = webui.Handler(webFS())
		fp = &firstpaint.Builder{Topics: tp, Boards: boards.New(d.Store), Inbox: InboxView(boards.New(d.Store), tp.Graph, nil)}
		d.FirstPaint = fp
	})
	return e, st, fp
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

func TestFirstPaintForEveryRole(t *testing.T) {
	e, st, fp := firstPaintEnv(t)
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

	// Every role gets one, seeing the inbox the way its inbox topic shows
	// it: whole for viewers and up, the household's projection below.
	setRole := func(role string) {
		t.Helper()
		if _, err := st.Writer().ExecContext(t.Context(), `UPDATE users SET role = ?`, role); err != nil {
			t.Fatal(err)
		}
	}
	incidents := func(s *firstpaint.Snapshot) map[string]json.RawMessage {
		t.Helper()
		b, _ := json.Marshal(s.Inbox["incidents"])
		var m map[string]json.RawMessage
		json.Unmarshal(b, &m)
		return m
	}
	for _, role := range []string{"admin", "operator", "viewer"} {
		setRole(role)
		resp, body, snap := e.page(t, c, "/")
		if snap == nil || snap.User.Role != role || snap.Board == nil {
			t.Fatalf("%s: snapshot %+v", role, snap)
		}
		if n := len(incidents(snap)); n != 3 {
			t.Fatalf("%s: %d incidents, want all 3", role, n)
		}
		if resp.Header.Get("Cache-Control") != "no-store" || resp.Header.Get("Vary") == "" {
			t.Fatalf("%s: headers %v", role, resp.Header)
		}
		assertNothingInlineRuns(t, resp, body)
	}
	setRole("household")
	resp, body, snap = e.page(t, c, "/")
	if snap == nil || snap.User.Role != "household" {
		t.Fatalf("household: snapshot %+v", snap)
	}
	got := incidents(snap)
	if len(got) != 1 || got["x-down"] == nil {
		t.Fatalf("household sees %v", got)
	}
	for _, hidden := range []string{"exit_code", "out of memory", "x.restart"} {
		if strings.Contains(string(got["x-down"]), hidden) {
			t.Fatalf("household's incident carries %q: %s", hidden, got["x-down"])
		}
	}
	if !strings.Contains(resp.Header.Get("Vary"), "Cookie") || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("household: headers %v", resp.Header)
	}
	assertNothingInlineRuns(t, resp, body)
	// A builder that can't project the inbox inlines nothing for household.
	fp.Inbox = nil
	if _, _, snap := e.page(t, c, "/"); snap != nil {
		t.Fatal("household got an unprojected snapshot")
	}
	setRole("owner")

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
