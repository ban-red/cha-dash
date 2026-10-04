package adguard

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"dash.cha.sh/integrations/internal/fixture"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/integration/integrationtest"
)

const (
	user = "admin"
	pass = "synthetic-adguard-pass-0001"
)

// server is a fake AdGuard Home behind a /adguard path, answering 401 to a
// request without the right basic auth.
func server(t *testing.T) *fixture.Server {
	t.Helper()
	auth := func(r *http.Request) bool {
		u, p, ok := r.BasicAuth()
		return ok && u == user && p == pass
	}
	return fixture.New(t, fixture.Dir(t, ID), auth, map[string]fixture.Route{
		"GET /adguard/control/status":           {File: "control_status.json"},
		"GET /adguard/control/stats":            {File: "control_stats.json"},
		"GET /adguard/control/filtering/status": {File: "control_filtering_status.json"},
	})
}

func connect(t *testing.T, h *integrationtest.Harness, url, password string) integration.Instance {
	t.Helper()
	return h.Connect(Integration{}, map[string]any{"url": url, "tls": map[string]any{"mode": "verify"}, "username": user}, map[string]string{"password": password})
}

func TestValidate(t *testing.T) {
	cfg := map[string]any{"url": "https://adguard.lab.example", "tls": map[string]any{"mode": "verify"}, "username": user}
	if errs := integrationtest.Validate(Integration{}, cfg, map[string]string{"password": pass}); len(errs) != 0 {
		t.Fatalf("valid config: %v", errs)
	}
	if errs := integrationtest.Validate(Integration{}, cfg, nil); len(errs) != 1 || errs[0].Path != "/password" {
		t.Fatalf("username without password: %v", errs)
	}
	if errs := integrationtest.Validate(Integration{}, map[string]any{"url": "https://adguard.lab.example"}, map[string]string{"password": pass}); len(errs) != 1 || errs[0].Path != "/username" {
		t.Fatalf("password without username: %v", errs)
	}
	if errs := integrationtest.Validate(Integration{}, map[string]any{"url": "https://adguard.lab.example"}, nil); len(errs) != 0 {
		t.Fatalf("no users: %v", errs)
	}
	_, m, ok := integration.Default.Get(ID)
	if !ok || !reflect.DeepEqual(m.SecretFields(), []string{"password"}) || m.ConfigSchema.Property("username").WriteOnly ||
		!reflect.DeepEqual(m.Capabilities, []integration.Capability{"dns.filter"}) {
		t.Fatalf("manifest = %+v", m)
	}
}

func TestSync(t *testing.T) {
	srv := server(t)
	h := integrationtest.New(t)
	inst := connect(t, h, srv.URL+"/adguard/", pass)
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	rid := "adguard:i_test/app"
	r, ok := h.Resource(rid)
	if !ok || r.Kind != "app" || r.Name != "Test" || r.State != "running" || r.Health != integration.HealthOK || r.Source != "integration:i_test" {
		t.Fatalf("app = %+v", r)
	}
	want := map[string]any{
		"type": "adguard", "url": srv.URL + "/adguard", "version": "0.107.79", "running": true, "protection_enabled": true,
		"filtering_enabled": true, "filters": 2.0, "stats_hours": 24.0,
	}
	for k, v := range want {
		if r.Attrs[k] != v {
			t.Errorf("attrs[%s] = %v, want %v", k, r.Attrs[k], v)
		}
	}
	if caps, _ := r.Attrs["caps"].([]any); len(caps) != 1 || caps[0] != "dns.filter" {
		t.Errorf("caps = %v", r.Attrs["caps"])
	}
	if h.Graph().Relations["integration:i_test monitors "+rid] == nil {
		t.Fatal("the instance should monitor its app")
	}
	// Blocked is filters + Safe Browsing + parental: 6115 + 16 + 0. The
	// blocklist is the enabled lists' rules: 79412 + 51208.
	checkSeries(t, h, map[string]float64{"queries.24h": 33750, "blocked.24h": 6131, "blocked.pct": 18.2, "blocklist": 130620})
	// The last 24 hours were asked for.
	if got := srv.Requests("/control/stats"); len(got) != 1 || got[0].URL.Query().Get("recent") != "86400000" {
		t.Fatalf("stats requests: %v", got)
	}
	for _, r := range srv.Requests("") {
		if r.URL.RawQuery != "" && strings.Contains(r.URL.RawQuery, pass) {
			t.Fatal("the password reached a URL")
		}
	}

	// The filter lists aren't read again within half an hour.
	h.Advance(10 * time.Minute)
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	if n := len(srv.Requests("/control/filtering/status")); n != 1 {
		t.Fatalf("filter lists read %d times", n)
	}
}

func checkSeries(t *testing.T, h *integrationtest.Harness, want map[string]float64) {
	t.Helper()
	for metric, v := range want {
		se := h.Series("adguard:i_test/app", metric)
		unit := "count"
		if metric == "blocked.pct" {
			unit = "%"
		}
		if se == nil || se["value"] != v || se["unit"] != unit {
			t.Errorf("%s = %v, want %v %s", metric, se, v, unit)
		}
	}
}

// hourly is a stats answer with n hourly entries: 100 queries and 10
// blocked each.
func hourly(n int, units string) string {
	q, b := make([]int, n), make([]int, n)
	for i := range q {
		q[i], b[i] = 100, 10
	}
	out, _ := json.Marshal(map[string]any{"time_units": units, "dns_queries": q, "blocked_filtering": b,
		"replaced_safebrowsing": make([]int, n), "replaced_parental": make([]int, n), "num_dns_queries": 100 * n})
	return string(out)
}

func TestStatsWindows(t *testing.T) {
	srv := server(t)

	// Before v0.107.72, recent is ignored: a 7-day interval answers 168
	// hours, and the last 24 are summed.
	srv.Set("GET /adguard/control/stats", fixture.Route{Body: hourly(168, "hours")})
	h := integrationtest.New(t)
	if err := connect(t, h, srv.URL+"/adguard", pass).Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	checkSeries(t, h, map[string]float64{"queries.24h": 2400, "blocked.24h": 240, "blocked.pct": 10})

	// A 6-hour interval: recent=24h is refused (400), the whole interval is
	// read, and stats_hours says it's 6 hours.
	srv.Set("GET /adguard/control/stats?recent", fixture.Route{Status: http.StatusBadRequest, Body: "recent: out of range", ContentType: "text/plain"})
	srv.Set("GET /adguard/control/stats", fixture.Route{Body: hourly(6, "hours")})
	h = integrationtest.New(t)
	if err := connect(t, h, srv.URL+"/adguard", pass).Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	checkSeries(t, h, map[string]float64{"queries.24h": 600, "blocked.24h": 60})
	if r, _ := h.Resource("adguard:i_test/app"); r.Attrs["stats_hours"] != 6.0 {
		t.Fatalf("stats_hours = %v", r.Attrs["stats_hours"])
	}

	// A 90-day interval on an old release: days, so the 24-hour figures are
	// unknown, and Test says why.
	srv.Set("GET /adguard/control/stats?recent", fixture.Route{Body: hourly(90, "days")})
	srv.Set("GET /adguard/control/stats", fixture.Route{Body: hourly(90, "days")})
	h = integrationtest.New(t)
	inst := connect(t, h, srv.URL+"/adguard", pass)
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	for _, metric := range []string{"queries.24h", "blocked.24h", "blocked.pct"} {
		if se := h.Series("adguard:i_test/app", metric); se == nil || se["value"] != nil {
			t.Errorf("%s = %v, want unknown", metric, se)
		}
	}
	if se := h.Series("adguard:i_test/app", "blocklist"); se["value"] != 130620.0 {
		t.Errorf("blocklist = %v", se)
	}
	res := inst.Test(t.Context())
	if !res.OK || len(res.Warnings) != 1 || res.Warnings[0].Code != "stats_days" {
		t.Fatalf("test = %+v", res)
	}

	// No queries at all is 0 %, not unknown.
	srv.Set("GET /adguard/control/stats?recent", fixture.Route{Body: `{"time_units":"hours","dns_queries":[0,0],"blocked_filtering":[0,0],"replaced_safebrowsing":[0,0],"replaced_parental":[0,0]}`})
	h = integrationtest.New(t)
	if err := connect(t, h, srv.URL+"/adguard", pass).Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	checkSeries(t, h, map[string]float64{"queries.24h": 0, "blocked.24h": 0, "blocked.pct": 0})
}

func TestProtection(t *testing.T) {
	srv := server(t)
	h := integrationtest.New(t)
	inst := connect(t, h, srv.URL+"/adguard", pass)
	// The harness clock is 2026-10-03 14:05 UTC.
	for _, c := range []struct {
		status, filtering, health, reason string
	}{
		{`{"version":"v0.107.79","running":true,"protection_enabled":false,"protection_disabled_until":"2026-10-03T14:14:30.5Z"}`, "", integration.HealthAttention, "Protection is paused for another 10 min"},
		{`{"version":"v0.107.79","running":true,"protection_enabled":false,"protection_disabled_until":null}`, "", integration.HealthAttention, "Protection is off"},
		{`{"version":"v0.107.79","running":false,"protection_enabled":true}`, "", integration.HealthDown, "AdGuard Home's DNS server isn't running"},
		{`{"version":"v0.107.79","running":true,"protection_enabled":true}`, `{"enabled":false,"filters":[]}`, integration.HealthAttention, "Filtering is off, so nothing is blocked"},
	} {
		srv.Set("GET /adguard/control/status", fixture.Route{Body: c.status})
		if c.filtering != "" {
			srv.Set("GET /adguard/control/filtering/status", fixture.Route{Body: c.filtering})
			h.Advance(time.Hour) // past the filter lists' interval
		}
		if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
			t.Fatal(err)
		}
		r, _ := h.Resource("adguard:i_test/app")
		if string(r.Health) != c.health || r.Attrs["health.reason"] != c.reason {
			t.Errorf("%s: %s %v", c.status, r.Health, r.Attrs["health.reason"])
		}
	}
	srv.Set("GET /adguard/control/status", fixture.Route{Body: `{"version":"v0.107.79","running":true,"protection_enabled":false,"protection_disabled_until":"2026-10-03T16:14:30Z"}`})
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	if r, _ := h.Resource("adguard:i_test/app"); r.Attrs["protection_paused_until"] != "2026-10-03T16:14:30Z" {
		t.Errorf("protection_paused_until = %v", r.Attrs["protection_paused_until"])
	}
}

func TestFailures(t *testing.T) {
	srv := server(t)

	err := connect(t, integrationtest.New(t), srv.URL+"/adguard", "wrong-password-0002").Sync(t.Context(), integrationtest.Watched)
	if code, _, msg := integration.Classify(err); code != integration.CodeAuth || integration.FieldOf(err) != "password" || msg != "AdGuard Home rejected the username or password" {
		t.Fatalf("wrong password: %s %q (%s)", code, msg, integration.FieldOf(err))
	}
	// No users configured, but AdGuard Home has some.
	err = integrationtest.New(t).Connect(Integration{}, map[string]any{"url": srv.URL + "/adguard"}, nil).Sync(t.Context(), integrationtest.Watched)
	if code, _, _ := integration.Classify(err); code != integration.CodeAuth || integration.FieldOf(err) != "username" {
		t.Fatalf("sign-in wanted: %v (%s)", err, integration.FieldOf(err))
	}
	// Not AdGuard Home at that URL.
	res := connect(t, integrationtest.New(t), srv.URL, pass).Test(t.Context())
	if res.OK || res.ErrorCode != integration.CodeAPIChanged {
		t.Fatalf("wrong url = %+v", res)
	}
}

func TestTest(t *testing.T) {
	srv := server(t)
	h := integrationtest.New(t)
	res := connect(t, h, srv.URL+"/adguard", pass).Test(t.Context())
	if !res.OK || res.Version != "0.107.79" || len(res.Grants) != 1 || res.Grants[0].Access != integration.AccessAdmin || len(res.Warnings) != 0 {
		t.Fatalf("test = %+v", res)
	}
	if _, ok := h.Resource("adguard:i_test/app"); ok {
		t.Fatal("Test must not emit facts")
	}
	res = integrationtest.New(t).Connect(Integration{}, map[string]any{"url": srv.URL + "/adguard"}, nil).Test(t.Context())
	if res.OK || res.ErrorCode != integration.CodeAuth {
		t.Fatalf("test without credentials = %+v", res)
	}
}
