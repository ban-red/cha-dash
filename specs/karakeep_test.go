package specs

import (
	"net/http"
	"strings"
	"testing"

	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/integration/integrationtest"
)

const karakeepKey = "fake-karakeep-key"

func karakeep(t *testing.T) *app {
	return &app{t: t, dir: "testdata/karakeep",
		auth: func(r *http.Request) bool {
			return r.URL.Path == "/api/version" || r.Header.Get("Authorization") == "Bearer "+karakeepKey
		},
		routes: map[string]string{
			"/api/version":           "version.json",
			"/api/v1/users/me/stats": "users_me_stats.json",
		}}
}

func TestKarakeep(t *testing.T) {
	integ := bundled(t, "karakeep", "Karakeep", "api_key")
	importerShape(t, integ, "https://hoarder.lab.example", "api_key")
	up := karakeep(t)
	srv := up.start()
	h := integrationtest.New(t)
	inst := h.Connect(integ, map[string]any{"url": srv.URL}, map[string]string{"api_key": karakeepKey})

	if res := inst.Test(t.Context()); !res.OK || res.Version != "0.30.2" {
		t.Fatalf("test = %+v", res)
	}
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	attrs := checkApp(t, h, "karakeep", srv.URL)
	if attrs["version"] != "0.30.2" || attrs["favorites"] != 57.0 || attrs["archived"] != 310.0 || attrs["tags"] != 142.0 ||
		attrs["lists"] != 9.0 || attrs["highlights"] != 33.0 {
		t.Fatalf("attrs = %v", attrs)
	}
	checkSeries(t, h, "karakeep", map[string]any{"bookmarks": 1201.0}, nil)

	// A release without /api/version, or a build that doesn't stamp one:
	// no version, and everything else still reads.
	for _, body := range []string{"", `{"version":"unknown"}`} {
		up.set("/api/version", body)
		h := integrationtest.New(t)
		inst := h.Connect(integ, map[string]any{"url": srv.URL}, map[string]string{"api_key": karakeepKey})
		if res := inst.Test(t.Context()); !res.OK || res.Version != "" {
			t.Fatalf("version %q: test = %+v", body, res)
		}
		if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
			t.Fatal(err)
		}
		if r, _ := h.Resource("karakeep:i_test/app"); r.Attrs["version"] != nil {
			t.Fatalf("version %q: attrs = %v", body, r.Attrs)
		}
		checkSeries(t, h, "karakeep", map[string]any{"bookmarks": 1201.0}, nil)
	}
}

func TestKarakeepFailures(t *testing.T) {
	integ := bundled(t, "karakeep", "Karakeep", "api_key")
	up := karakeep(t)
	srv := up.start()

	res := integrationtest.New(t).Connect(integ, map[string]any{"url": srv.URL}, map[string]string{"api_key": "wrong-key"}).Test(t.Context())
	if res.OK || res.ErrorCode != integration.CodeAuth || strings.Contains(res.ErrorMessage, "wrong-key") {
		t.Fatalf("wrong key: %+v", res)
	}

	// Hoarder 0.22 has no stats endpoint: an unsupported version.
	inst := integrationtest.New(t).Connect(integ, map[string]any{"url": srv.URL}, map[string]string{"api_key": karakeepKey})
	up.set("/api/version", `{"version":"0.22.0"}`)
	up.set("/api/v1/users/me/stats", "")
	if code, msg := classify(t, inst); code != integration.CodeUnsupportedVersion || msg != "Karakeep 0.22.0 is older than this spec supports (0.23 or later)." {
		t.Fatalf("0.22: %s %q", code, msg)
	}
	// Without a version to go on, the API changed.
	up.set("/api/version", "")
	if code, msg := classify(t, inst); code != integration.CodeAPIChanged || !strings.Contains(msg, "/api/v1/users/me/stats answered 404 Not Found") {
		t.Fatalf("404: %s %q", code, msg)
	}
	// 0.23 and later aren't caught by the repair.
	for _, v := range []string{"0.23.0", "0.30.2", "1.0.0"} {
		up.set("/api/version", `{"version":"`+v+`"}`)
		if code, _ := classify(t, inst); code != integration.CodeAPIChanged {
			t.Errorf("%s: %s", v, code)
		}
	}
}
