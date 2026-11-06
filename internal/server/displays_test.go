package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"dash.cha.sh/internal/audit"
	"dash.cha.sh/internal/auth"
	"dash.cha.sh/internal/authz"
	"dash.cha.sh/internal/boards"
	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/livedoc"
	"dash.cha.sh/internal/realtime"
	storepkg "dash.cha.sh/internal/store"
	"dash.cha.sh/internal/topics"
)

// TestDisplays: an admin makes a display for one board, a device claims
// it with the link's secret and reads that board and nothing else, the
// display's credential counts for nothing on any other route, and revoking
// it ends its socket and its reads. Each change is audited, and the secret
// is in no audit record.
func TestDisplays(t *testing.T) {
	tp := topics.New(graph.New(), time.Now())
	if _, err := tp.Graph.Update(func(tx *graph.Tx) error {
		for _, r := range []graph.Resource{
			{ID: "host:a", Kind: "host", Name: "a", Attrs: map[string]any{"ip": "192.0.2.10"}},
			{ID: "host:b", Kind: "host", Name: "b"},
			{ID: "node:n1", Kind: "node", Name: "n1"},
		} {
			if err := tx.Upsert(r); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tp.Inbox.Update(func(tx *livedoc.Tx) error {
		tx.Put(topics.Incidents, "inc_a", map[string]any{"id": "inc_a", "class": "problem", "severity": "attention", "title": "a is slow", "target": "host:a"})
		tx.Put(topics.Incidents, "inc_b", map[string]any{"id": "inc_b", "class": "problem", "severity": "down", "title": "b is down", "target": "host:b"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tp.Metrics.Update(func(tx *livedoc.Tx) error {
		tx.Put(topics.Series, topics.SeriesKey("host:a", "cpu"), map[string]any{"value": 1})
		tx.Put(topics.Series, topics.SeriesKey("host:b", "cpu"), map[string]any{"value": 2})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var store *storepkg.Store
	e := newEnvWith(t, func(d *Deps) {
		store = d.Store
		d.Audit = audit.New(d.Store)
		hub := realtime.New(DisplaySource(tp, boards.New(d.Store), tp.Graph, d.Log), realtime.Options{Coalesce: 10 * time.Millisecond, Log: d.Log,
			Recheck: DisplaySocketRecheck(d.Auth), RecheckEvery: 100 * time.Millisecond})
		t.Cleanup(hub.Close)
		d.DisplaySocket = hub
	})
	owner := signIn(t, e)

	// A board that shows host:a only.
	var bl struct{ Boards []struct{ ID string } }
	_, _, b := e.call(t, owner, "GET", "/api/boards", nil)
	json.Unmarshal(b, &bl)
	board := bl.Boards[0].ID
	layout := map[string]any{"sections": []any{map[string]any{"items": []any{map[string]any{"id": "host@host:a", "type": "host"}}}}}
	if status, _, b := e.call(t, owner, "PUT", "/api/boards/"+board+"/layout", map[string]any{"revision": 0, "format_version": 1, "layout": layout}); status != 200 {
		t.Fatalf("layout: %d %s", status, b)
	}

	// Making one: bad input is refused and audited; then one is made.
	for _, bad := range []map[string]any{
		{"name": "Hall", "board": "brd_nope"},
		{"name": "", "board": board},
		{"name": "Hall", "board": board, "quiet": map[string]string{"start": "22:00", "end": "22:00"}},
		{"name": "Hall", "board": board, "quiet": map[string]string{"start": "25:00", "end": "07:00"}},
		{"name": "Hall", "board": board, "style": "hologram"},
	} {
		if status, code, b := e.call(t, owner, "POST", "/api/displays", bad); status != http.StatusUnprocessableEntity || code != "invalid" {
			t.Errorf("POST %v: %d %s", bad, status, b)
		}
	}
	if ev, ok := lastAudit(t, store, "displays.create"); !ok || ev.Result == audit.OK {
		t.Fatalf("a refused display isn't audited as refused: %+v", ev)
	}
	status, _, b := e.call(t, owner, "POST", "/api/displays", map[string]any{"name": "Hall tablet", "board": board, "quiet": map[string]string{"start": "22:00", "end": "07:00"}})
	var made struct {
		Display struct {
			ID    string
			Name  string
			Board struct{ ID, Name string }
			Quiet *auth.Quiet
		}
		Secret string
	}
	json.Unmarshal(b, &made)
	if status != http.StatusCreated || !strings.HasPrefix(made.Secret, "cdd_") || made.Display.Board.ID != board || made.Display.Quiet == nil || made.Display.Quiet.Start != "22:00" {
		t.Fatalf("POST /api/displays: %d %s", status, b)
	}
	if ev, ok := lastAudit(t, store, "displays.create"); !ok || ev.Result != audit.OK || ev.Target != "display:"+made.Display.ID {
		t.Fatalf("displays.create: %+v", ev)
	}
	_, _, b = e.call(t, owner, "GET", "/api/displays", nil)
	if !strings.Contains(string(b), made.Display.ID) || strings.Contains(string(b), made.Secret) {
		t.Fatalf("GET /api/displays: %s", b)
	}
	if status, _, b := e.call(t, owner, "PATCH", "/api/displays/"+made.Display.ID, map[string]any{"name": "Hall", "style": "ink", "quiet": nil}); status != 200 || !strings.Contains(string(b), `"quiet":null`) || !strings.Contains(string(b), `"style":"ink"`) || !strings.Contains(string(b), `"name":"Hall"`) {
		t.Fatalf("PATCH: %d %s", status, b)
	}
	if ev, ok := lastAudit(t, store, "displays.update"); !ok || ev.Result != audit.OK {
		t.Fatalf("displays.update: %+v", ev)
	}

	// Claiming: a bad secret is 401; the right one sets the cookie, on
	// /api/display only.
	jar, _ := cookiejar.New(nil)
	device := &http.Client{Jar: jar}
	if status, code, _ := e.call(t, device, "POST", "/api/display/claim", map[string]string{"token": "cdd_" + strings.Repeat("a", 26) + "_" + strings.Repeat("A", 52)}); status != 401 || code != "invalid_display" {
		t.Fatalf("a wrong secret: %d %s", status, code)
	}
	resp := e.request(t, device, "POST", "/api/display/claim", map[string]string{"token": made.Secret}, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("claim: %d", resp.StatusCode)
	}
	var cookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == auth.DisplayCookie {
			cookie = c
		}
	}
	if cookie == nil || cookie.Path != "/api/display" || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.MaxAge < 300*24*3600 {
		t.Fatalf("the display cookie: %+v", cookie)
	}
	status, _, b = e.call(t, device, "GET", "/api/display", nil)
	var read struct {
		Display struct{ ID, Name string }
		Board   struct{ ID string }
		Layout  struct{ Revision int64 }
		Icons   map[string]map[string]string
		Now     int64
	}
	json.Unmarshal(b, &read)
	if status != 200 || read.Display.ID != made.Display.ID || read.Board.ID != board || read.Layout.Revision != 1 || read.Now == 0 || read.Icons["by_icon"] == nil {
		t.Fatalf("GET /api/display: %d %s", status, b)
	}
	// The secret as a bearer token works too, for scripts.
	if status, _, b := e.raw(t, &http.Client{}, "GET", "/api/display", nil, map[string]string{"Authorization": "Bearer " + made.Secret}); status != 200 {
		t.Fatalf("GET /api/display with Bearer cdd_: %d %s", status, b)
	}

	// The display's credential, as a cookie sent anywhere or as a bearer
	// token, counts for nothing on every other route; a session counts for
	// nothing on the display's.
	creds := map[string]map[string]string{
		"cookie": {"Cookie": auth.DisplayCookie + "=" + made.Secret},
		"bearer": {"Authorization": "Bearer " + made.Secret},
	}
	checked := 0
	for _, row := range authz.Routes {
		method, path, ok := strings.Cut(row.Pattern, " ")
		if !ok || strings.HasSuffix(path, "/") || row.Access == authz.Public || row.Access == authz.NodeKey || row.Access == authz.Signature || row.Access == authz.DisplayToken {
			continue
		}
		path = pathParam.ReplaceAllString(path, "zz-matrix")
		var body []byte
		if method != "GET" {
			body = []byte("{")
		}
		mounted := true
		for how, hdr := range creds {
			status, _, b := e.raw(t, &http.Client{}, method, path, body, hdr)
			if status == http.StatusNotFound && strings.Contains(string(b), "no such endpoint") || status == http.StatusMethodNotAllowed {
				mounted = false // a subsystem this env doesn't mount
				continue
			}
			if status != http.StatusUnauthorized {
				t.Errorf("%s with the display's %s: %d %s, want 401", row.Pattern, how, status, b)
			}
		}
		if mounted {
			checked++
		}
	}
	if checked < 30 {
		t.Fatalf("checked only %d routes", checked)
	}
	if status, code, _ := e.call(t, owner, "GET", "/api/display", nil); status != 401 || code != "invalid_display" {
		t.Fatalf("GET /api/display with the owner's session: %d %s", status, code)
	}

	// The socket: household's graph and metrics, its board's incidents, and
	// no tasks.
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(e.srv.URL, "http")+"/api/display/ws", &websocket.DialOptions{HTTPClient: device})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	send := func(m map[string]any) {
		b, _ := json.Marshal(m)
		if err := ws.Write(ctx, websocket.MessageText, b); err != nil {
			t.Fatal(err)
		}
	}
	next := func() map[string]any {
		for {
			_, b, err := ws.Read(ctx)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			var f map[string]any
			json.Unmarshal(b, &f)
			if f["op"] != "ping" && f["op"] != "hello" {
				return f
			}
		}
	}
	send(map[string]any{"op": "hello", "v": 1})
	for i, topic := range []string{"graph", "inbox", "metrics"} {
		send(map[string]any{"op": "sub", "id": i + 1, "topics": []string{topic}})
		f := next()
		if f["op"] != "snap" {
			t.Fatalf("sub %s: %v", topic, f)
		}
		data, _ := json.Marshal(f["data"].(map[string]any)[topic])
		s := string(data)
		switch topic {
		case "graph":
			// Household's graph: no chadash-node, no addresses.
			if !strings.Contains(s, `"host:a"`) || strings.Contains(s, "node:n1") || strings.Contains(s, "192.0.2.10") {
				t.Errorf("the display's graph: %s", s)
			}
		case "inbox":
			if !strings.Contains(s, "inc_a") || strings.Contains(s, "inc_b") {
				t.Errorf("the display's inbox: %s", s)
			}
		case "metrics":
			if !strings.Contains(s, "host:a#cpu") {
				t.Errorf("the display's metrics: %s", s)
			}
		}
	}
	send(map[string]any{"op": "sub", "id": 9, "topics": []string{"tasks"}})
	if f := next(); f["op"] != "error" || !strings.Contains(f["code"].(string), "unknown_topic") {
		t.Fatalf("sub tasks: %v", f)
	}

	// Revoking ends the socket and every read.
	if status, _, b := e.call(t, owner, "DELETE", "/api/displays/"+made.Display.ID, nil); status != http.StatusNoContent {
		t.Fatalf("DELETE: %d %s", status, b)
	}
	if ev, ok := lastAudit(t, store, "displays.revoke"); !ok || ev.Result != audit.OK {
		t.Fatalf("displays.revoke: %+v", ev)
	}
	for {
		if _, _, err := ws.Read(ctx); err != nil {
			if ctx.Err() != nil {
				t.Fatal("the revoked display's socket stayed open")
			}
			break
		}
	}
	if status, code, b := e.call(t, device, "GET", "/api/display", nil); status != 401 || code != "invalid_display" || !strings.Contains(string(b), "removed") {
		t.Fatalf("GET /api/display once revoked: %d %s", status, b)
	}
	if status, _, _ := e.call(t, owner, "DELETE", "/api/displays/"+made.Display.ID, nil); status != http.StatusNotFound {
		t.Fatalf("revoking twice: %d", status)
	}
	for _, ev := range auditEvents(t, store) {
		if strings.Contains(string(ev.Params), made.Secret) || strings.Contains(ev.Target, made.Secret) {
			t.Fatalf("an audit record carries the secret: %+v", ev)
		}
	}
}
