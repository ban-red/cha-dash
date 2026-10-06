package boards

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

const tilesV1 = `{"arr@sonarr:i_x/app":{"name":"TV","icon":"sonarr.png","href":"https://sonarr.lab.example","bind":{"instance":"i_x"},"options":{"queue":true,"big":12345678901234567890},"origin":{"source":"homepage","file":"services.yaml","group":"Media","entry":"Sonarr"}},"calendar":{"chromeless":true,"bind":{"sources":[{"instance":"i_x","capability":"media.calendar","options":{"unmonitored":true}}]}}}`

func TestTilesTravelWithTheLayout(t *testing.T) {
	srv, _ := newServer(t)
	path := "/api/boards/" + homeID(t, srv) + "/layout"

	// Saved together, they come back together, as sent.
	r := call(t, srv, "PUT", path, `{"revision":0,"format_version":1,"layout":`+layoutV1+`,"tiles":`+tilesV1+`}`)
	if r.status != 200 || r.body["revision"] != 1.0 {
		t.Fatalf("save: %d %s", r.status, r.raw)
	}
	r = call(t, srv, "GET", path, "")
	if !strings.Contains(r.raw, `"tiles":`+tilesV1) {
		t.Fatalf("tiles came back as %s", r.raw)
	}

	// A layout-only save (a drag) keeps them.
	r = call(t, srv, "PUT", path, `{"revision":1,"format_version":1,"layout":{"version":1,"moved":true}}`)
	if r.status != 200 {
		t.Fatalf("layout-only save: %d %s", r.status, r.raw)
	}
	if r := call(t, srv, "GET", path, ""); !strings.Contains(r.raw, `"tiles":`+tilesV1) || !strings.Contains(r.raw, `"moved":true`) {
		t.Fatalf("after a layout-only save: %s", r.raw)
	}
	// So does an explicit null.
	call(t, srv, "PUT", path, `{"revision":2,"format_version":1,"layout":{"version":1},"tiles":null}`)
	if r := call(t, srv, "GET", path, ""); r.body["revision"] != 3.0 || !strings.Contains(r.raw, `"tiles":`+tilesV1) {
		t.Fatalf("after tiles: null: %s", r.raw)
	}

	// {} clears them; a stale revision is a conflict, tiles or not.
	if r := call(t, srv, "PUT", path, `{"revision":3,"format_version":1,"layout":{"version":1},"tiles":{}}`); r.status != 200 {
		t.Fatalf("clear: %d %s", r.status, r.raw)
	}
	if r := call(t, srv, "GET", path, ""); !strings.Contains(r.raw, `"tiles":{}`) {
		t.Fatalf("after clearing: %s", r.raw)
	}
	if r := call(t, srv, "PUT", path, `{"revision":3,"format_version":1,"layout":{"version":1},"tiles":`+tilesV1+`}`); r.status != http.StatusConflict {
		t.Fatalf("stale save with tiles: %d %s", r.status, r.raw)
	}
}

func TestInvalidTiles(t *testing.T) {
	srv, _ := newServer(t)
	path := "/api/boards/" + homeID(t, srv) + "/layout"
	for name, tiles := range map[string]string{
		"array":             `[]`,
		"string":            `"x"`,
		"tile not object":   `{"a":"x"}`,
		"unknown field":     `{"a":{"title":"x"}}`,
		"unknown bind":      `{"a":{"bind":{"host":"x"}}}`,
		"unknown origin":    `{"a":{"origin":{"line":3}}}`,
		"javascript href":   `{"a":{"href":"javascript:alert(1)"}}`,
		"relative href":     `{"a":{"href":"/admin"}}`,
		"options not obj":   `{"a":{"options":[1]}}`,
		"chromeless string": `{"a":{"chromeless":"yes"}}`,
		"source no cap":     `{"a":{"bind":{"sources":[{"instance":"i"}]}}}`,
		"empty item id":     `{"":{"name":"x"}}`,
		"long item id":      `{"` + strings.Repeat("x", MaxTileIDLen+1) + `":{}}`,
	} {
		r := call(t, srv, "PUT", path, `{"revision":0,"format_version":1,"layout":{},"tiles":`+tiles+`}`)
		if r.status != http.StatusUnprocessableEntity || !strings.Contains(r.raw, "invalid tiles") {
			t.Errorf("%s: %d %s", name, r.status, r.raw)
		}
	}
	if r := call(t, srv, "GET", path, ""); r.body["revision"] != 0.0 {
		t.Fatalf("a refused save was stored: %s", r.raw)
	}
	// HTTP(S) in any case is fine.
	if r := call(t, srv, "PUT", path, `{"revision":0,"format_version":1,"layout":{},"tiles":{"a":{"href":"HTTPS://x.example"}}}`); r.status != 200 {
		t.Fatalf("upper-case scheme: %d %s", r.status, r.raw)
	}
}

func TestParseTilesLimits(t *testing.T) {
	var b strings.Builder
	b.WriteString("{")
	for i := range MaxTiles + 1 {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `"t%d":{}`, i)
	}
	b.WriteString("}")
	if _, err := ParseTiles(json.RawMessage(b.String())); !errors.Is(err, ErrInvalidTiles) {
		t.Fatalf("%d tiles: %v", MaxTiles+1, err)
	}
	tiles, err := ParseTiles(json.RawMessage(tilesV1))
	if err != nil {
		t.Fatal(err)
	}
	if tiles["arr@sonarr:i_x/app"].Bind.Instance != "i_x" || !tiles["calendar"].Chromeless || tiles["calendar"].Bind.Sources[0].Capability != "media.calendar" {
		t.Fatalf("parsed = %+v", tiles)
	}
	if _, err := ParseTiles(json.RawMessage(`{} {}`)); err == nil {
		t.Fatal("trailing data accepted")
	}
}

func TestDismissals(t *testing.T) {
	srv, st := newServer(t)
	id := homeID(t, srv)
	path := "/api/boards/" + id + "/discovery"

	if r := call(t, srv, "GET", path, ""); r.status != 200 || r.raw != `{"dismissed":[]}`+"\n" {
		t.Fatalf("none yet: %d %s", r.status, r.raw)
	}
	r := call(t, srv, "POST", path+"/dismiss", `{"resources":["docker:i_d/container/mealie","docker:i_d/container/immich_server"]}`)
	if r.status != 200 || len(r.body["dismissed"].([]any)) != 2 {
		t.Fatalf("dismiss: %d %s", r.status, r.raw)
	}
	// Again, plus one: idempotent.
	r = call(t, srv, "POST", path+"/dismiss", `{"resources":["docker:i_d/container/mealie","docker:i_d/container/pbs"]}`)
	got := r.body["dismissed"].([]any)
	if r.status != 200 || len(got) != 3 || got[2] != "docker:i_d/container/pbs" {
		t.Fatalf("dismiss again: %d %s", r.status, r.raw)
	}
	if r := call(t, srv, "GET", path, ""); len(r.body["dismissed"].([]any)) != 3 {
		t.Fatalf("after: %s", r.raw)
	}

	// Restore takes one back (Undo, or Settings); restoring what isn't
	// dismissed changes nothing.
	r = call(t, srv, "POST", path+"/restore", `{"resources":["docker:i_d/container/mealie","docker:i_d/container/never"]}`)
	if got := r.body["dismissed"].([]any); r.status != 200 || len(got) != 2 || got[0] != "docker:i_d/container/immich_server" {
		t.Fatalf("restore: %d %s", r.status, r.raw)
	}
	if r := call(t, srv, "POST", path+"/restore", `{"resources":[]}`); r.status != http.StatusUnprocessableEntity {
		t.Fatalf("restore nothing: %d", r.status)
	}
	if r := call(t, srv, "POST", "/api/boards/b_nope/discovery/restore", `{"resources":["x"]}`); r.status != http.StatusNotFound {
		t.Fatalf("restore on a missing board: %d", r.status)
	}
	r = call(t, srv, "POST", path+"/dismiss", `{"resources":["docker:i_d/container/mealie"]}`)
	if r.status != 200 || len(r.body["dismissed"].([]any)) != 3 {
		t.Fatalf("dismiss after restore: %d %s", r.status, r.raw)
	}

	for name, body := range map[string]string{
		"none":      `{"resources":[]}`,
		"empty id":  `{"resources":[""]}`,
		"too long":  `{"resources":["` + strings.Repeat("x", MaxRefLen+1) + `"]}`,
		"too many":  `{"resources":[` + strings.TrimSuffix(strings.Repeat(`"x",`, MaxDismissBatch+1), ",") + `]}`,
		"not array": `{"resources":"x"}`,
	} {
		if r := call(t, srv, "POST", path+"/dismiss", body); r.status/100 != 4 {
			t.Errorf("%s: %d %s", name, r.status, r.raw)
		}
	}
	for _, m := range []string{"GET", "POST"} {
		p, body := "/api/boards/b_nope/discovery", ""
		if m == "POST" {
			p, body = p+"/dismiss", `{"resources":["x"]}`
		}
		if r := call(t, srv, m, p, body); r.status != http.StatusNotFound {
			t.Errorf("%s on a missing board: %d", m, r.status)
		}
	}

	// They go with their board.
	if _, err := st.Writer().ExecContext(t.Context(), `DELETE FROM boards WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := st.Reader().QueryRowContext(t.Context(), `SELECT count(*) FROM board_dismissals`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("%d dismissals outlived their board (%v)", n, err)
	}
}

func TestDismissalsAreCapped(t *testing.T) {
	s, _ := newService(t)
	boards, _ := s.List(t.Context())
	id := boards[0].ID
	clock := int64(0)
	s.now = func() int64 { clock++; return clock }
	for batch := range MaxDismissed/MaxDismissBatch + 1 {
		refs := make([]string, MaxDismissBatch)
		for i := range refs {
			refs[i] = fmt.Sprintf("r%05d", batch*MaxDismissBatch+i)
		}
		if _, err := s.Dismiss(t.Context(), id, refs); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Dismissed(t.Context(), id)
	if err != nil || len(got) != MaxDismissed || got[0] != fmt.Sprintf("r%05d", MaxDismissBatch) {
		t.Fatalf("kept %d, oldest %v (%v)", len(got), got[:1], err)
	}
}
