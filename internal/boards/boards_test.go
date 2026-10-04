package boards

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"dash.cha.sh/internal/store"
)

func newService(t *testing.T) (*Service, *store.Store) {
	t.Helper()
	st, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "chadash.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	return New(st), st
}

func newServer(t *testing.T) (*httptest.Server, *store.Store) {
	t.Helper()
	s, st := newService(t)
	mux := http.NewServeMux()
	s.Register(mux, func(h http.Handler) http.Handler { return h }, slog.New(slog.DiscardHandler))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, st
}

type response struct {
	status int
	body   map[string]any
	raw    string
}

func call(t *testing.T, srv *httptest.Server, method, path, body string) response {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, _ := http.NewRequest(method, srv.URL+path, rd)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	r := response{status: resp.StatusCode, raw: string(b)}
	json.Unmarshal(b, &r.body)
	return r
}

func homeID(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	r := call(t, srv, "GET", "/api/boards", "")
	boards, _ := r.body["boards"].([]any)
	if r.status != 200 || len(boards) != 1 {
		t.Fatalf("boards: %d %s", r.status, r.raw)
	}
	b := boards[0].(map[string]any)
	if b["name"] != "Home" || !strings.HasPrefix(b["id"].(string), "b_") {
		t.Fatalf("default board: %v", b)
	}
	return b["id"].(string)
}

const layoutV1 = `{"version":1,"columns":12,"sections":[{"id":"s1","title":"Media","items":[]}]}`

func TestDefaultBoardOnce(t *testing.T) {
	s, _ := newService(t)
	var wg sync.WaitGroup
	ids := make([]string, 8)
	for i := range ids {
		wg.Go(func() {
			b, err := s.List(t.Context())
			if err != nil || len(b) != 1 {
				t.Errorf("List: %v %v", b, err)
				return
			}
			ids[i] = b[0].ID
		})
	}
	wg.Wait()
	for _, id := range ids {
		if id != ids[0] {
			t.Fatalf("concurrent first calls made different boards: %v", ids)
		}
	}
}

func TestLayoutLifecycle(t *testing.T) {
	srv, _ := newServer(t)
	id := homeID(t, srv)
	if again := homeID(t, srv); again != id {
		t.Fatal("listing twice created a second board")
	}
	path := "/api/boards/" + id + "/layout"

	// Never saved: the SPA creates the first layout.
	r := call(t, srv, "GET", path, "")
	if r.status != 200 || r.raw != `{"revision":0,"format_version":0,"layout":null,"tiles":{}}`+"\n" {
		t.Fatalf("empty layout: %d %s", r.status, r.raw)
	}

	r = call(t, srv, "PUT", path, `{"revision":0,"format_version":1,"layout":`+layoutV1+`}`)
	if r.status != 200 || r.body["revision"] != 1.0 {
		t.Fatalf("first save: %d %s", r.status, r.raw)
	}
	r = call(t, srv, "GET", path, "")
	if r.status != 200 || r.body["revision"] != 1.0 || r.body["format_version"] != 1.0 {
		t.Fatalf("after save: %d %s", r.status, r.raw)
	}
	// The hub stores the document opaquely: it comes back as sent, key
	// order included.
	if !strings.Contains(r.raw, `"layout":`+layoutV1) {
		t.Fatalf("layout came back as %s", r.raw)
	}

	// A second tab still at revision 0 is told to reload.
	r = call(t, srv, "PUT", path, `{"revision":0,"format_version":1,"layout":{"version":1}}`)
	if r.status != http.StatusConflict || r.body["revision"] != 1.0 || r.body["error"].(map[string]any)["code"] != "conflict" {
		t.Fatalf("stale save: %d %s", r.status, r.raw)
	}
	r = call(t, srv, "PUT", path, `{"revision":1,"format_version":2,"layout":{"version":2}}`)
	if r.status != 200 || r.body["revision"] != 2.0 {
		t.Fatalf("second save: %d %s", r.status, r.raw)
	}
	if r := call(t, srv, "GET", path, ""); r.body["format_version"] != 2.0 || r.body["layout"].(map[string]any)["version"] != 2.0 {
		t.Fatalf("format version not stored: %s", r.raw)
	}
}

func TestMissingBoard(t *testing.T) {
	srv, _ := newServer(t)
	homeID(t, srv)
	for _, method := range []string{"GET", "PUT"} {
		body := ""
		if method == "PUT" {
			body = `{"revision":0,"format_version":1,"layout":{}}`
		}
		r := call(t, srv, method, "/api/boards/b_nope/layout", body)
		if r.status != http.StatusNotFound || r.body["error"].(map[string]any)["code"] != "not_found" {
			t.Fatalf("%s missing board: %d %s", method, r.status, r.raw)
		}
	}
}

func TestInvalidSaves(t *testing.T) {
	srv, _ := newServer(t)
	path := "/api/boards/" + homeID(t, srv) + "/layout"
	for name, tc := range map[string]struct {
		body   string
		status int
	}{
		"no revision":       {`{"format_version":1,"layout":{}}`, 422},
		"negative revision": {`{"revision":-1,"format_version":1,"layout":{}}`, 422},
		"no format version": {`{"revision":0,"layout":{}}`, 422},
		"format version 0":  {`{"revision":0,"format_version":0,"layout":{}}`, 422},
		"null layout":       {`{"revision":0,"format_version":1,"layout":null}`, 422},
		"no layout":         {`{"revision":0,"format_version":1}`, 422},
		"array layout":      {`{"revision":0,"format_version":1,"layout":[]}`, 422},
		"unknown field":     {`{"revision":0,"format_version":1,"layout":{},"board":"x"}`, 400},
		"too large":         {`{"revision":0,"format_version":1,"layout":{"pad":"` + strings.Repeat("x", MaxLayoutBytes) + `"}}`, 413},
	} {
		if r := call(t, srv, "PUT", path, tc.body); r.status != tc.status {
			t.Errorf("%s: %d %s, want %d", name, r.status, r.raw, tc.status)
		}
	}
	// Bigger than the generic 64 KiB API limit is fine for a layout.
	big := `{"revision":0,"format_version":1,"layout":{"pad":"` + strings.Repeat("x", 200<<10) + `"}}`
	if r := call(t, srv, "PUT", path, big); r.status != 200 {
		t.Fatalf("200 KB layout: %d %s", r.status, r.raw)
	}
	req, _ := http.NewRequest("PUT", srv.URL+path, strings.NewReader(`{"revision":1,"format_version":1,"layout":{}}`))
	req.Header.Set("Content-Type", "text/plain")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("text/plain: %d", resp.StatusCode)
	}
}

func TestConcurrentSavesOneWins(t *testing.T) {
	s, _ := newService(t)
	boards, err := s.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	id := boards[0].ID
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins, conflicts := 0, 0
	for range 10 {
		wg.Go(func() {
			_, err := s.SaveLayout(t.Context(), id, 0, 1, json.RawMessage(`{}`), nil, "")
			var c *ConflictError
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				wins++
			case errors.As(err, &c) && c.Current == 1:
				conflicts++
			default:
				t.Errorf("save: %v", err)
			}
		})
	}
	wg.Wait()
	if wins != 1 || conflicts != 9 {
		t.Fatalf("%d saves won and %d conflicted; want 1 and 9", wins, conflicts)
	}
}

func TestHistoryIsPruned(t *testing.T) {
	s, st := newService(t)
	boards, _ := s.List(t.Context())
	id := boards[0].ID
	for rev := range int64(KeepRevisions + 5) {
		if _, err := s.SaveLayout(t.Context(), id, rev, 1, json.RawMessage(`{"n":1}`), nil, ""); err != nil {
			t.Fatal(err)
		}
	}
	var n, oldest int64
	if err := st.Reader().QueryRowContext(t.Context(), `SELECT COUNT(*), MIN(revision) FROM layouts WHERE board_id = ?`, id).Scan(&n, &oldest); err != nil {
		t.Fatal(err)
	}
	if n != KeepRevisions || oldest != 6 {
		t.Fatalf("kept %d revisions from %d; want %d from 6", n, oldest, KeepRevisions)
	}
	l, err := s.Layout(t.Context(), id)
	if err != nil || l.Revision != KeepRevisions+5 {
		t.Fatalf("latest: %+v %v", l, err)
	}
}
