package specs

import (
	"net/http"
	"strings"
	"testing"

	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/integration/integrationtest"
)

const linkdingToken = "fake-linkding-token"

func linkding(t *testing.T) *app {
	return &app{t: t, dir: "testdata/linkding",
		auth: func(r *http.Request) bool {
			return r.URL.Path == "/health" || r.Header.Get("Authorization") == "Token "+linkdingToken
		},
		routes: map[string]string{
			"/health":                  "health.json",
			"/api/bookmarks/":          "bookmarks.json",
			"/api/bookmarks/archived/": "bookmarks_archived.json",
			"/api/tags/":               "tags.json",
		}}
}

func TestLinkding(t *testing.T) {
	integ := bundled(t, "linkding", "Linkding", "token")
	importerShape(t, integ, "https://links.lab.example", "token")
	up := linkding(t)
	srv := up.start()
	h := integrationtest.New(t)
	inst := h.Connect(integ, map[string]any{"url": srv.URL}, map[string]string{"token": linkdingToken})

	if res := inst.Test(t.Context()); !res.OK || res.Version != "1.42.0" {
		t.Fatalf("test = %+v", res)
	}
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	attrs := checkApp(t, h, "linkding", srv.URL)
	if r, _ := h.Resource("linkding:i_test/app"); r.Health != integration.HealthOK {
		t.Fatalf("health = %s", r.Health)
	}
	if attrs["version"] != "1.42.0" || attrs["archived"] != 21.0 || attrs["tags"] != 96.0 {
		t.Fatalf("attrs = %v", attrs)
	}
	// Unarchived and archived together.
	checkSeries(t, h, "linkding", map[string]any{"bookmarks": 1201.0}, nil)
	// One result per page is enough: only count is read.
	for _, r := range up.requests() {
		if r.URL.Path != "/health" && r.URL.Query().Get("limit") != "1" {
			t.Fatalf("request %s", r.URL)
		}
	}
}

func TestLinkdingFailures(t *testing.T) {
	integ := bundled(t, "linkding", "Linkding", "token")
	up := linkding(t)
	srv := up.start()

	res := integrationtest.New(t).Connect(integ, map[string]any{"url": srv.URL}, map[string]string{"token": "wrong-token"}).Test(t.Context())
	if res.OK || res.ErrorCode != integration.CodeAuth || res.Version != "1.42.0" || strings.Contains(res.ErrorMessage, "wrong-token") {
		t.Fatalf("wrong token: %+v", res)
	}
	inst := integrationtest.New(t).Connect(integ, map[string]any{"url": srv.URL}, map[string]string{"token": linkdingToken})
	up.set("/api/bookmarks/archived/", "")
	if code, msg := classify(t, inst); code != integration.CodeAPIChanged || !strings.Contains(msg, "/api/bookmarks/archived/ answered 404 Not Found (detected version 1.42.0)") {
		t.Fatalf("404: %s %q", code, msg)
	}
}
