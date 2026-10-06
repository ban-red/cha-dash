package sonarr

import (
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"dash.cha.sh/integrations/internal/arr/arrtest"
	"dash.cha.sh/integrations/internal/fixture"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/integration/integrationtest"
)

const key = "0123456789abcdef0123456789abcdef" // synthetic

// upstream serves the synthetic fixtures under the URL base /sonarr.
func upstream(t *testing.T) *fixture.Server {
	dir := fixture.Dir(t, ID)
	return fixture.New(t, dir, func(r *http.Request) bool { return r.Header.Get("X-Api-Key") == key }, map[string]fixture.Route{
		"GET /sonarr/api/v3/system/status":  {File: "system_status.json"},
		"GET /sonarr/api/v3/health":         {File: "health.json"},
		"GET /sonarr/api/v3/queue/status":   {File: "queue_status.json"},
		"GET /sonarr/api/v3/wanted/missing": {File: "wanted_missing.json"},
		"GET /sonarr/api/v3/wanted/cutoff":  {File: "wanted_cutoff.json"},
		"GET /sonarr/api/v3/calendar":       {File: "calendar.json"},
	})
}

func connect(t *testing.T, h *integrationtest.Harness, url, apiKey string) integration.Instance {
	return h.Connect(Integration{}, map[string]any{"url": url, "tls": map[string]any{"mode": "verify"}}, map[string]string{"api_key": apiKey})
}

func TestRegistered(t *testing.T) {
	_, m, ok := integration.Default.Get(ID)
	if !ok || m.Name != "Sonarr" || !reflect.DeepEqual(m.SecretFields(), []string{"api_key"}) ||
		!reflect.DeepEqual(m.Capabilities, []integration.Capability{"media.calendar"}) || m.Poll.Interval != time.Minute {
		t.Fatalf("manifest = %+v", m)
	}
}

func TestValidate(t *testing.T) {
	// The importer's shape (testdata/importers/homepage/dogfood/expected-plan.json).
	cfg := map[string]any{"url": "https://jupiter.lab.example/sonarr", "tls": map[string]any{"mode": "verify"}}
	if errs := integrationtest.Validate(Integration{}, cfg, map[string]string{"api_key": key}); len(errs) != 0 {
		t.Fatalf("importer shape: %v", errs)
	}
	if errs := integrationtest.Validate(Integration{}, cfg, nil); len(errs) != 1 || errs[0].Path != "/api_key" {
		t.Fatalf("no key: %v", errs)
	}
	if errs := integrationtest.Validate(Integration{}, map[string]any{"url": "jupiter/sonarr"}, map[string]string{"api_key": key}); len(errs) != 1 || errs[0].Path != "/url" {
		t.Fatalf("bad url: %v", errs)
	}
	if errs := integrationtest.Validate(Integration{}, map[string]any{"url": "https://s.lab.example", "username": "x"}, map[string]string{"api_key": key}); len(errs) != 1 {
		t.Fatalf("unknown key: %v", errs)
	}
}

func TestSync(t *testing.T) {
	up := upstream(t)
	h := integrationtest.New(t)
	inst := connect(t, h, up.URL+"/sonarr/", key)
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	rid := "sonarr:i_test/app"
	r, ok := h.Resource(rid)
	if !ok || r.Kind != "app" || r.Name != "Test" || r.State != "running" || r.Health != integration.HealthOK || r.Source != "integration:i_test" ||
		r.Attrs["type"] != "sonarr" || r.Attrs["version"] != "4.0.15.2941" || r.Attrs["url"] != up.URL+"/sonarr" {
		t.Fatalf("app = %+v", r)
	}
	if caps := r.Attrs["caps"].([]any); len(caps) != 1 || caps[0] != "media.calendar" {
		t.Fatalf("caps = %v", caps)
	}
	if links := r.Attrs["links"].([]any); links[0].(map[string]any)["url"] != up.URL+"/sonarr" {
		t.Fatalf("links = %v", links)
	}
	// The calendar, in the Demo Home's shape: inside the window, soonest
	// first, TBA dates left out.
	want := []any{
		map[string]any{"at": "2026-10-04T04:00:00Z", "title": "Orbital Kitchen", "detail": "S02E01 · Re-entry · season premiere"},
		map[string]any{"at": "2026-10-04T10:00:00Z", "title": "Night Garden Detectives", "detail": "S01E07", "unmonitored": true},
		map[string]any{"at": "2026-10-06T20:00:00Z", "title": "The Lighthouse Keepers", "detail": "S03E06 · Beacon · season finale"},
		map[string]any{"at": "2026-10-08T02:00:00Z", "title": "Quiet Valley", "detail": "S01E01 · Pilot · series premiere"},
	}
	if got := r.Attrs["calendar"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("calendar = %#v", got)
	}
	if h.Graph().Relations["integration:i_test monitors "+rid] == nil {
		t.Fatal("the instance should monitor its app")
	}
	for metric, v := range map[string]float64{"queue": 10, "wanted": 25, "missing.episodes": 21} {
		se := h.Series(rid, metric)
		if se == nil || se["value"] != v || se["unit"] != "count" {
			t.Errorf("%s = %v", metric, se)
		}
	}

	// The key goes in the header only; the calendar asks for its window
	// with the series; the counts ask for one record.
	for _, req := range up.Requests("") {
		if req.Header.Get("X-Api-Key") != key || req.URL.Query().Has("apikey") {
			t.Fatalf("%s: key handling", req.URL)
		}
	}
	cal := up.Requests("/calendar")[0].URL.Query()
	if cal.Get("start") != "2026-10-03T02:05:00Z" || cal.Get("end") != "2026-10-10T14:05:00Z" || cal.Get("includeSeries") != "true" || cal.Get("unmonitored") != "true" {
		t.Fatalf("calendar query = %v", cal)
	}
	if q := up.Requests("/wanted/missing")[0].URL.Query(); q.Get("pageSize") != "1" || q.Get("monitored") != "true" {
		t.Fatalf("missing query = %v", q)
	}
}

func TestPollPlan(t *testing.T) {
	up := upstream(t)
	h := integrationtest.New(t)
	inst := connect(t, h, up.URL+"/sonarr", key)
	sync := func(want integration.Interest) {
		t.Helper()
		if err := inst.Sync(t.Context(), want); err != nil {
			t.Fatal(err)
		}
	}
	slow := func() int { return len(up.Requests("/calendar")) + len(up.Requests("/system/status")) }
	sync(integrationtest.Watched)
	if slow() != 2 {
		t.Fatalf("first sync: %d slow reads", slow())
	}
	// A minute later only health and the queue are read.
	h.Advance(time.Minute)
	up.Reset()
	sync(integrationtest.Watched)
	if slow() != 0 || len(up.Requests("/health")) != 1 || len(up.Requests("/queue/status")) != 1 {
		t.Fatalf("fast sync: %d requests", len(up.Requests("")))
	}
	// Nobody watching: still no slow reads, however long it's been.
	h.Advance(time.Hour)
	sync(integration.Interest{})
	if slow() != 0 {
		t.Fatal("idle syncs skip the slow reads")
	}
	sync(integrationtest.Watched)
	if slow() != 2 {
		t.Fatal("a watched sync after 10 minutes refreshes them")
	}
	// The calendar drops what's past without being re-read.
	h.Advance(3 * 24 * time.Hour)
	sync(integration.Interest{})
	r, _ := h.Resource("sonarr:i_test/app")
	if cal := r.Attrs["calendar"].([]any); len(cal) != 3 || cal[0].(map[string]any)["title"] != "The Lighthouse Keepers" {
		t.Fatalf("calendar three days later = %v", cal)
	}
}

func TestHealthAndErrors(t *testing.T) {
	up := upstream(t)
	up.Set("GET /sonarr/api/v3/health", fixture.Route{File: "health_error.json"})
	h := integrationtest.New(t)
	inst := connect(t, h, up.URL+"/sonarr", key)
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	r, _ := h.Resource("sonarr:i_test/app")
	if r.Health != integration.HealthAttention ||
		r.Attrs["health.reason"] != "Sonarr: Unable to communicate with qBittorrent. Connection refused (torrents.lab.example:443) (and 1 more)" {
		t.Fatalf("health = %s %v", r.Health, r.Attrs["health.reason"])
	}

	const status = "GET /sonarr/api/v3/system/status"
	cases := map[string]struct {
		base, key string
		route     fixture.Route
		code      string
		field     string
	}{
		"wrong key":     {"/sonarr", "wrong-key-0000", fixture.Route{}, integration.CodeAuth, "api_key"},
		"no url base":   {"", key, fixture.Route{}, integration.CodeAPIChanged, "url"},
		"not json":      {"/sonarr", key, fixture.Route{Body: "<html>login</html>", ContentType: "text/html"}, integration.CodeAPIChanged, "url"},
		"that's radarr": {"/sonarr", key, fixture.Route{Body: `{"appName":"Radarr","version":"5.26.2.10099"}`}, integration.CodeInvalidConfig, "url"},
		"too old":       {"/sonarr", key, fixture.Route{Body: `{"appName":"Sonarr","version":"2.0.0.5344"}`}, integration.CodeUnsupportedVersion, ""},
	}
	for name, c := range cases {
		up := upstream(t)
		if c.route.Body != "" {
			up.Set(status, c.route)
		}
		h := integrationtest.New(t)
		inst := connect(t, h, up.URL+c.base, c.key)
		err := inst.Sync(t.Context(), integrationtest.Watched)
		code, _, msg := integration.Classify(err)
		if code != c.code || integration.FieldOf(err) != c.field {
			t.Errorf("%s: %s (%s) field %q", name, code, msg, integration.FieldOf(err))
		}
		if strings.Contains(msg, c.key) {
			t.Errorf("%s: the key is in the message: %s", name, msg)
		}
		if res := inst.Test(t.Context()); res.OK || res.ErrorCode != c.code {
			t.Errorf("%s: test = %+v", name, res)
		}
	}
}

func TestTest(t *testing.T) {
	up := upstream(t)
	h := integrationtest.New(t)
	res := connect(t, h, up.URL+"/sonarr", key).Test(t.Context())
	if !res.OK || res.Version != "4.0.15.2941" || len(res.Grants) != 1 || !res.Grants[0].CanWrite() || res.Grants[0].Access != integration.AccessAdmin {
		t.Fatalf("test = %+v", res)
	}
	if w := integration.WriteGrantWarning(res.Grants); w == nil {
		t.Fatal("an *arr key is a write grant")
	}
	if _, ok := h.Resource("sonarr:i_test/app"); ok {
		t.Fatal("Test must not emit facts")
	}
}

func TestSearchMissing(t *testing.T) {
	arrtest.SearchMissing(t, Integration{}, fixture.Dir(t, ID), "/sonarr/api/v3", key, "MissingEpisodeSearch", "episodes")
}
