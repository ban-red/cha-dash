package specs

import (
	"net/http"
	"strings"
	"testing"

	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/integration/integrationtest"
)

// Technitium's tokens are 64 hex digits; this one is made up, and
// session.json echoes it back the way Technitium does.
const technitiumToken = "fake0technitium0token0000000000000000000000000000000000000000000"

// technitium answers like Technitium: 200 always, with invalid-token in the
// body for a token it doesn't know. Stats are routed by their type.
func technitium(t *testing.T) *app {
	a := &app{t: t, dir: "testdata/technitium", base: "/dns",
		auth: func(*http.Request) bool { return true },
		route: func(r *http.Request) string {
			if r.Header.Get("Authorization") != "Bearer "+technitiumToken || r.URL.Query().Has("token") {
				return "invalid-token"
			}
			p := strings.TrimPrefix(r.URL.Path, "/dns")
			if p == "/api/dashboard/stats/get" {
				if r.URL.Query().Get("utc") != "true" {
					return ""
				}
				return p + "?" + r.URL.Query().Get("type")
			}
			return p
		},
		routes: map[string]string{
			"invalid-token":                     "invalid_token.json",
			"/api/user/session/get":             "session.json",
			"/api/dashboard/stats/get?LastDay":  "stats_day.json",
			"/api/dashboard/stats/get?LastHour": "stats_hour.json",
		}}
	return a
}

func TestTechnitium(t *testing.T) {
	integ := bundled(t, "technitium", "Technitium DNS", "token")
	importerShape(t, integ, "http://10.20.30.53:5380", "token")
	up := technitium(t)
	url := up.start().URL + "/dns"
	h := integrationtest.New(t)
	inst := h.Connect(integ, map[string]any{"url": url}, map[string]string{"token": technitiumToken})
	if res := inst.Test(t.Context()); !res.OK || res.Version != "15.6.2" {
		t.Fatalf("test = %+v", res)
	}
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	attrs := checkApp(t, h, "technitium", url)
	app, _ := h.Resource("technitium:i_test/app")
	if app.Health != integration.HealthOK || app.Attrs["health.reason"] != nil {
		t.Fatalf("app = %+v", app)
	}
	for k, v := range map[string]any{"version": "15.6.2", "up_since": "2026-10-28T06:12:40.1234567Z", "clients": 23.0, "zones": 12.0, "cache_entries": 18422.0} {
		if attrs[k] != v {
			t.Errorf("%s = %v, want %v", k, attrs[k], v)
		}
	}
	checkSeries(t, h, "technitium", map[string]any{"queries.24h": 48210.0, "blocked.24h": 8033.0, "blocked.pct": 16.7, "cached.pct": 44.9,
		"blocklist": 412884.0, "servfail.pct": 0.5},
		map[string]string{"blocked.pct": "%", "cached.pct": "%", "servfail.pct": "%"})
	// The token goes in the header, never a URL; session/get echoes it, and
	// it isn't read from there.
	for _, r := range up.requests() {
		if strings.Contains(r.URL.String(), technitiumToken) {
			t.Errorf("the token is in %s", r.URL.Path)
		}
	}
	tailscaleKeyNowhere(t, h, technitiumToken)

	// Upstream resolution failing in the last hour: 552 of 1840 is 30%.
	up.set("/api/dashboard/stats/get?LastHour", "stats_hour_failing.json")
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	app, _ = h.Resource("technitium:i_test/app")
	if app.Health != integration.HealthAttention || app.Attrs["health.reason"] != "30% of queries in the last hour failed to resolve (SERVFAIL)" {
		t.Fatalf("failing: %+v", app)
	}
	checkSeries(t, h, "technitium", map[string]any{"servfail.pct": 30.0}, map[string]string{"servfail.pct": "%"})
}

// The owner's threshold: at 40%, 30% failing is still ok.
func TestTechnitiumThreshold(t *testing.T) {
	integ := bundled(t, "technitium", "Technitium DNS", "token")
	up := technitium(t)
	up.set("/api/dashboard/stats/get?LastHour", "stats_hour_failing.json")
	h := integrationtest.New(t)
	inst := h.Connect(integ, map[string]any{"url": up.start().URL + "/dns", "servfail_warn": 40}, map[string]string{"token": technitiumToken})
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	if app, _ := h.Resource("technitium:i_test/app"); app.Health != integration.HealthOK {
		t.Fatalf("app = %+v", app)
	}
}

func TestTechnitiumFailures(t *testing.T) {
	integ := bundled(t, "technitium", "Technitium DNS", "token")
	up := technitium(t)
	url := up.start().URL + "/dns"
	// A deleted token: invalid-token inside a 200.
	h := integrationtest.New(t)
	const gone = "fake0deleted0token000000000000000000000000000000000000000000000"
	res := h.Connect(integ, map[string]any{"url": url}, map[string]string{"token": gone}).Test(t.Context())
	if res.OK || res.ErrorCode != integration.CodeAuth || !strings.Contains(res.ErrorMessage, "rejected the token") || strings.Contains(res.ErrorMessage, gone) {
		t.Fatalf("a deleted token: %+v", res)
	}
	tailscaleKeyNowhere(t, h, gone)

	// A user without Dashboard: View.
	up.set("/api/dashboard/stats/get?LastDay", "denied.json")
	up.set("/api/dashboard/stats/get?LastHour", "denied.json")
	h = integrationtest.New(t)
	res = h.Connect(integ, map[string]any{"url": url}, map[string]string{"token": technitiumToken}).Test(t.Context())
	if res.OK || res.ErrorCode != integration.CodeAuth || !strings.Contains(res.ErrorMessage, "Dashboard: View") {
		t.Fatalf("no dashboard permission: %+v", res)
	}
	tailscaleKeyNowhere(t, h, technitiumToken)

	// Another error inside a 200.
	up.set("/api/dashboard/stats/get?LastDay", `{"status": "error", "errorMessage": "Stats are being rebuilt."}`)
	up.set("/api/dashboard/stats/get?LastHour", "stats_hour.json")
	res = integrationtest.New(t).Connect(integ, map[string]any{"url": url}, map[string]string{"token": technitiumToken}).Test(t.Context())
	if res.OK || res.ErrorCode != integration.CodeAPIChanged || !strings.Contains(res.ErrorMessage, "Stats are being rebuilt.") {
		t.Fatalf("an error: %+v", res)
	}
}
