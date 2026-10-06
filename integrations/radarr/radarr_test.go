package radarr

import (
	"net/http"
	"reflect"
	"testing"

	"dash.cha.sh/integrations/internal/arr/arrtest"
	"dash.cha.sh/integrations/internal/fixture"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/integration/integrationtest"
)

const key = "fedcba9876543210fedcba9876543210" // synthetic

func upstream(t *testing.T) *fixture.Server {
	return fixture.New(t, fixture.Dir(t, ID), func(r *http.Request) bool { return r.Header.Get("X-Api-Key") == key }, map[string]fixture.Route{
		"GET /radarr/api/v3/system/status":  {File: "system_status.json"},
		"GET /radarr/api/v3/health":         {File: "health.json"},
		"GET /radarr/api/v3/queue/status":   {File: "queue_status.json"},
		"GET /radarr/api/v3/wanted/missing": {File: "wanted_missing.json"},
		"GET /radarr/api/v3/wanted/cutoff":  {File: "wanted_cutoff.json"},
		"GET /radarr/api/v3/calendar":       {File: "calendar.json"},
	})
}

func connect(t *testing.T, h *integrationtest.Harness, url string) integration.Instance {
	return h.Connect(Integration{}, map[string]any{"url": url, "tls": map[string]any{"mode": "verify"}}, map[string]string{"api_key": key})
}

func TestValidate(t *testing.T) {
	cfg := map[string]any{"url": "https://jupiter.lab.example/radarr", "tls": map[string]any{"mode": "verify"}}
	if errs := integrationtest.Validate(Integration{}, cfg, map[string]string{"api_key": key}); len(errs) != 0 {
		t.Fatalf("importer shape: %v", errs)
	}
	if _, m, ok := integration.Default.Get(ID); !ok || !reflect.DeepEqual(m.Capabilities, []integration.Capability{"media.calendar"}) {
		t.Fatalf("manifest = %+v", m)
	}
}

func TestSync(t *testing.T) {
	up := upstream(t)
	h := integrationtest.New(t)
	inst := connect(t, h, up.URL+"/radarr")
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	rid := "radarr:i_test/app"
	r, ok := h.Resource(rid)
	if !ok || r.Kind != "app" || r.Health != integration.HealthOK || r.Attrs["type"] != "radarr" || r.Attrs["version"] != "5.26.2.10099" {
		t.Fatalf("app = %+v", r)
	}
	// One entry per release date inside the window.
	want := []any{
		map[string]any{"at": "2026-10-09T00:00:00Z", "title": "The Long Way Round the Moon", "detail": "digital release"},
		map[string]any{"at": "2026-10-10T00:00:00Z", "title": "Paper Harbour", "detail": "in cinemas"},
	}
	if got := r.Attrs["calendar"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("calendar = %#v", got)
	}
	// An empty queue is 0 ("Queue clear"), never unknown.
	for metric, v := range map[string]float64{"queue": 0, "wanted": 10, "missing.movies": 7} {
		if se := h.Series(rid, metric); se == nil || se["value"] != v {
			t.Errorf("%s = %v", metric, se)
		}
	}
	if h.Series(rid, "queued") != nil {
		t.Fatal("the queue is published as queue, like Sonarr's")
	}
}

func TestBeforeV5(t *testing.T) {
	// Radarr 4 has no wanted endpoints: the counts are unknown, the rest works.
	up := upstream(t)
	up.Set("GET /radarr/api/v3/system/status", fixture.Route{Body: `{"appName":"Radarr","version":"4.7.5.7809"}`})
	up.Set("GET /radarr/api/v3/wanted/missing", fixture.Route{Status: 404, Body: `{"message":"NotFound"}`})
	up.Set("GET /radarr/api/v3/wanted/cutoff", fixture.Route{Status: 404, Body: `{"message":"NotFound"}`})
	h := integrationtest.New(t)
	if err := connect(t, h, up.URL+"/radarr").Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	for _, metric := range []string{"wanted", "missing.movies"} {
		if se := h.Series("radarr:i_test/app", metric); se == nil || se["value"] != nil {
			t.Errorf("%s should be unknown: %v", metric, se)
		}
	}
	if se := h.Series("radarr:i_test/app", "queue"); se["value"] != 0.0 {
		t.Fatalf("queue = %v", se)
	}
}

func TestQueueErrors(t *testing.T) {
	up := upstream(t)
	up.Set("GET /radarr/api/v3/queue/status", fixture.Route{File: "queue_status_error.json"})
	h := integrationtest.New(t)
	if err := connect(t, h, up.URL+"/radarr").Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	r, _ := h.Resource("radarr:i_test/app")
	if r.Health != integration.HealthAttention || r.Attrs["health.reason"] != "A download in Radarr's queue has an error" {
		t.Fatalf("health = %s %v", r.Health, r.Attrs["health.reason"])
	}
	if se := h.Series("radarr:i_test/app", "queue"); se["value"] != 2.0 {
		t.Fatalf("queue = %v", se)
	}
}

func TestTest(t *testing.T) {
	up := upstream(t)
	res := connect(t, integrationtest.New(t), up.URL+"/radarr").Test(t.Context())
	if !res.OK || res.Version != "5.26.2.10099" || len(res.Grants) != 1 || res.Grants[0].Access != integration.AccessAdmin {
		t.Fatalf("test = %+v", res)
	}
}

func TestSearchMissing(t *testing.T) {
	arrtest.SearchMissing(t, Integration{}, fixture.Dir(t, ID), "/radarr/api/v3", key, "MissingMoviesSearch", "movies")
}
