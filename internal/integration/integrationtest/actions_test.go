package integrationtest

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestWritesRecordsChangesOnly(t *testing.T) {
	var w Writes
	srv := httptest.NewServer(w.Wrap(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		r.ParseForm() // the handler still sees the body
		if r.Method == http.MethodPost && r.PostForm.Get("snapname") != "pre" {
			t.Errorf("handler form = %v", r.PostForm)
		}
	})))
	defer srv.Close()
	do := func(method, path, ct, body string) {
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer secret-token")
		if ct != "" {
			req.Header.Set("Content-Type", ct)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	do("GET", "/read", "", "")
	do("POST", "/api/snapshot", "application/x-www-form-urlencoded", "snapname=pre&vmstate=1")
	do("DELETE", "/api/snapshot/pre", "", "")
	do("PUT", "/api/thing", "application/json", `{"on":true,"n":2}`)

	all := w.All()
	if len(all) != 3 || strings.Contains(describe(all), "secret-token") {
		t.Fatalf("writes = %s", describe(all))
	}
	w.Expect(t,
		Write{Method: "POST", Path: "/api/snapshot", Form: url.Values{"snapname": {"pre"}, "vmstate": {"1"}}},
		Write{Method: "DELETE", Path: "/api/snapshot/pre"},
		Write{Method: "PUT", Path: "/api/thing", JSON: map[string]any{"n": 2, "on": true}},
	)
	if len(w.All()) != 0 {
		t.Fatal("Expect didn't forget what it checked")
	}

	// A mismatch is reported.
	do("POST", "/api/snapshot", "application/x-www-form-urlencoded", "snapname=pre")
	ft := &fakeT{}
	w.Expect(ft, Write{Method: "POST", Path: "/api/snapshot", Form: url.Values{"snapname": {"other"}}})
	if !ft.failed {
		t.Fatal("a wrong form passed")
	}
}

type fakeT struct {
	testing.TB
	failed bool
}

func (f *fakeT) Helper()               {}
func (f *fakeT) Errorf(string, ...any) { f.failed = true }
