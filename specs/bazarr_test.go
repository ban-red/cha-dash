package specs

import (
	"net/http"
	"strings"
	"testing"

	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/integration/integrationtest"
)

const bazarrKey = "fake-bazarr-key"

// bazarr serves the fixtures under /bazarr, the dogfood home's path prefix.
func bazarr(t *testing.T) *app {
	return &app{t: t, dir: "testdata/bazarr", base: "/bazarr",
		auth: func(r *http.Request) bool { return r.Header.Get("X-API-KEY") == bazarrKey },
		routes: map[string]string{
			"/api/system/status": "system_status.json",
			"/api/badges":        "badges.json",
		}}
}

func TestBazarr(t *testing.T) {
	integ := bundled(t, "bazarr", "Bazarr", "api_key")
	importerShape(t, integ, "https://jupiter.lab.example/bazarr", "api_key")
	up := bazarr(t)
	srv := up.start()
	url := srv.URL + "/bazarr"
	h := integrationtest.New(t)
	inst := h.Connect(integ, map[string]any{"url": url}, map[string]string{"api_key": bazarrKey})

	if res := inst.Test(t.Context()); !res.OK || res.Version != "1.5.3" {
		t.Fatalf("test = %+v", res)
	}
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	attrs := checkApp(t, h, "bazarr", url)
	if r, _ := h.Resource("bazarr:i_test/app"); r.Health != integration.HealthOK {
		t.Fatalf("health = %s", r.Health)
	}
	if attrs["version"] != "1.5.3" || attrs["sonarr_version"] != "4.0.15.2941" || attrs["sonarr_signalr"] != "LIVE" || attrs["health_issues"] != 0.0 {
		t.Fatalf("attrs = %v", attrs)
	}
	checkSeries(t, h, "bazarr", map[string]any{"wanted": 804.0, "missing.episodes": 798.0, "missing.movies": 6.0}, nil)

	// Health issues come first in the reason; throttled providers otherwise.
	up.set("/api/badges", `{"episodes":798,"movies":6,"providers":2,"status":1}`)
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	if r, _ := h.Resource("bazarr:i_test/app"); r.Health != integration.HealthAttention || r.Attrs["health.reason"] != "1 health issue; see System → Status" {
		t.Fatalf("issues = %+v", r)
	}
	up.set("/api/badges", `{"episodes":798,"providers":2,"status":0}`)
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	if r, _ := h.Resource("bazarr:i_test/app"); r.Health != integration.HealthAttention || r.Attrs["health.reason"] != "2 subtitle providers are throttled" {
		t.Fatalf("throttled = %+v", r)
	}
	// No movie count (Radarr off): wanted is the episodes alone, movies unknown.
	checkSeries(t, h, "bazarr", map[string]any{"wanted": 798.0, "missing.movies": nil}, nil)
}

func TestBazarrFailures(t *testing.T) {
	integ := bundled(t, "bazarr", "Bazarr", "api_key")
	up := bazarr(t)
	srv := up.start()
	url := srv.URL + "/bazarr"

	res := integrationtest.New(t).Connect(integ, map[string]any{"url": url}, map[string]string{"api_key": "wrong-key"}).Test(t.Context())
	if res.OK || res.ErrorCode != integration.CodeAuth || strings.Contains(res.ErrorMessage, "wrong-key") {
		t.Fatalf("wrong key: %+v", res)
	}
	inst := integrationtest.New(t).Connect(integ, map[string]any{"url": url}, map[string]string{"api_key": bazarrKey})
	up.set("/api/system/status", `{"data":{"bazarr_version":"0.9.9"}}`)
	if code, msg := classify(t, inst); code != integration.CodeUnsupportedVersion || msg != "Bazarr 0.9.9 is older than this spec supports (1.0 or later)." {
		t.Fatalf("old: %s %q", code, msg)
	}
	up.set("/api/system/status", "system_status.json")
	up.set("/api/badges", "")
	if code, msg := classify(t, inst); code != integration.CodeAPIChanged || !strings.Contains(msg, "/bazarr/api/badges answered 404 Not Found (detected version 1.5.3)") {
		t.Fatalf("404: %s %q", code, msg)
	}
}
