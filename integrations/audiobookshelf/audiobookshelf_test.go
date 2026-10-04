package audiobookshelf

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"dash.cha.sh/integrations/internal/fixture"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/integration/integrationtest"
)

const key = "fake-abs-api-key-eyJ0eXAiOiJKV1Qi" // synthetic

const (
	books    = "/api/libraries/3f5a1c2e-0000-4000-8000-000000000001/stats"
	podcasts = "/api/libraries/3f5a1c2e-0000-4000-8000-000000000002/stats"
)

func upstream(t *testing.T) *fixture.Server {
	auth := func(r *http.Request) bool {
		return strings.HasSuffix(r.URL.Path, "/status") || r.Header.Get("Authorization") == "Bearer "+key
	}
	return fixture.New(t, fixture.Dir(t, ID), auth, map[string]fixture.Route{
		"GET /abs/status":           {File: "status.json"},
		"GET /abs/api/me":           {File: "me.json"},
		"GET /abs/api/libraries":    {File: "libraries.json"},
		"GET /abs" + books:          {File: "stats_books.json"},
		"GET /abs" + podcasts:       {File: "stats_podcasts.json"},
		"GET /abs/api/users/online": {File: "users_online.json"},
	})
}

func connect(t *testing.T, h *integrationtest.Harness, url, apiKey string) integration.Instance {
	return h.Connect(Integration{}, map[string]any{"url": url, "tls": map[string]any{"mode": "verify"}}, map[string]string{"api_key": apiKey})
}

func TestValidate(t *testing.T) {
	cfg := map[string]any{"url": "https://audiobooks.home.arpa", "tls": map[string]any{"mode": "verify"}}
	if errs := integrationtest.Validate(Integration{}, cfg, map[string]string{"api_key": key}); len(errs) != 0 {
		t.Fatalf("importer shape: %v", errs)
	}
	if errs := integrationtest.Validate(Integration{}, cfg, nil); len(errs) != 1 || errs[0].Path != "/api_key" {
		t.Fatalf("no key: %v", errs)
	}
}

func TestSync(t *testing.T) {
	up := upstream(t)
	h := integrationtest.New(t)
	inst := connect(t, h, up.URL+"/abs/", key)
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	rid := "audiobookshelf:i_test/app"
	r, ok := h.Resource(rid)
	if !ok || r.Kind != "app" || r.Name != "Test" || r.State != "running" || r.Health != integration.HealthOK {
		t.Fatalf("app = %+v", r)
	}
	if a := r.Attrs; a["type"] != "audiobookshelf" || a["version"] != "2.26.3" || a["libraries"] != 2.0 || a["url"] != up.URL+"/abs" {
		t.Fatalf("attrs = %v", a)
	}
	want := map[string]any{"streams": 1.0, "books": 124.0, "books.hours": 457.8, "podcasts": 9.0, "podcasts.hours": 600.4}
	units := map[string]string{"books.hours": "h", "podcasts.hours": "h"}
	for metric, v := range want {
		se := h.Series(rid, metric)
		unit := units[metric]
		if unit == "" {
			unit = "count"
		}
		if se == nil || se["value"] != v || se["unit"] != unit {
			t.Errorf("%s = %v, want %v", metric, se, v)
		}
	}
	if h.Graph().Relations["integration:i_test monitors "+rid] == nil {
		t.Fatal("the instance should monitor its app")
	}
	// The key is a bearer token, never in the URL; /status goes without it.
	for _, req := range up.Requests("") {
		if strings.Contains(req.URL.String(), key) {
			t.Fatal("the key is in a URL")
		}
		if req.URL.Path == "/abs/status" && req.Header.Get("Authorization") != "" {
			t.Fatal("/status got the key")
		}
	}

	// Within ten minutes, only the sessions are read again.
	up.Reset()
	h.Advance(5 * time.Minute)
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	if n := len(up.Requests("")); n != 1 || len(up.Requests("/users/online")) != 1 {
		t.Fatalf("%d requests on a quick sync", n)
	}
	h.Advance(6 * time.Minute)
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	if len(up.Requests("/stats")) != 2 {
		t.Fatal("the libraries weren't read again after ten minutes")
	}

	// A key that isn't an admin's: no sessions, the rest still reads.
	up.Set("GET /abs/api/users/online", fixture.Route{Status: http.StatusForbidden, Body: "Forbidden", ContentType: "text/plain"})
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	if se := h.Series(rid, "streams"); se["value"] != nil {
		t.Fatalf("streams without admin = %v", se)
	}
	if se := h.Series(rid, "books"); se["value"] != 124.0 {
		t.Fatalf("books without admin = %v", se)
	}
}

func TestTest(t *testing.T) {
	up := upstream(t)
	res := connect(t, integrationtest.New(t), up.URL+"/abs", key).Test(t.Context())
	if !res.OK || res.Version != "2.26.3" || len(res.Grants) != 1 || res.Grants[0].Name != "Audiobookshelf user" || res.Grants[0].Access != integration.AccessRead || len(res.Warnings) != 0 {
		t.Fatalf("test = %+v", res)
	}
	// Not an admin: a note, not a failure.
	up.Set("GET /abs/api/users/online", fixture.Route{Status: http.StatusForbidden, Body: "Forbidden", ContentType: "text/plain"})
	res = connect(t, integrationtest.New(t), up.URL+"/abs", key).Test(t.Context())
	if !res.OK || len(res.Warnings) != 1 || res.Warnings[0].Code != "sessions_not_admin" {
		t.Fatalf("not admin = %+v", res)
	}
	// The grant follows the user's type and permissions.
	up.Set("GET /abs/api/me", fixture.Route{Body: `{"type":"admin","permissions":{"update":true,"delete":true,"upload":true}}`})
	if res = connect(t, integrationtest.New(t), up.URL+"/abs", key).Test(t.Context()); res.Grants[0].Access != integration.AccessAdmin {
		t.Fatalf("admin = %+v", res.Grants)
	}
	up.Set("GET /abs/api/me", fixture.Route{Body: `{"type":"user","permissions":{"download":true,"upload":true,"delete":true}}`})
	if res = connect(t, integrationtest.New(t), up.URL+"/abs", key).Test(t.Context()); res.Grants[0].Access != integration.AccessReadWrite || res.Grants[0].Detail != "a user who can upload, delete items" {
		t.Fatalf("uploader = %+v", res.Grants)
	}
}

func TestFailures(t *testing.T) {
	up := upstream(t)
	res := connect(t, integrationtest.New(t), up.URL+"/abs", "wrong-key").Test(t.Context())
	if res.OK || res.ErrorCode != integration.CodeAuth || strings.Contains(res.ErrorMessage, "wrong-key") || res.Version != "2.26.3" {
		t.Fatalf("wrong key: %+v", res)
	}
	res = connect(t, integrationtest.New(t), up.URL, key).Test(t.Context())
	if res.OK || res.ErrorCode != integration.CodeAPIChanged {
		t.Fatalf("wrong URL: %+v", res)
	}
	up.Set("GET /abs/status", fixture.Route{Body: `{"app":"something-else","serverVersion":"1.0"}`})
	res = connect(t, integrationtest.New(t), up.URL+"/abs", key).Test(t.Context())
	if res.OK || res.ErrorCode != integration.CodeAPIChanged {
		t.Fatalf("not Audiobookshelf: %+v", res)
	}
	// A library ID that would leave its path segment is refused.
	up.Set("GET /abs/status", fixture.Route{File: "status.json"})
	up.Set("GET /abs/api/libraries", fixture.Route{Body: `{"libraries":[{"id":"../../api/users","mediaType":"book"}]}`})
	res = connect(t, integrationtest.New(t), up.URL+"/abs", key).Test(t.Context())
	if res.OK || res.ErrorCode != integration.CodeAPIChanged {
		t.Fatalf("odd library id: %+v", res)
	}
}
