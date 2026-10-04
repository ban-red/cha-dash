package lidarr

import (
	"net/http"
	"reflect"
	"strings"
	"testing"

	"dash.cha.sh/integrations/internal/fixture"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/integration/integrationtest"
)

const key = "0123abcd0123abcd0123abcd0123abcd" // synthetic

func upstream(t *testing.T) *fixture.Server {
	return fixture.New(t, fixture.Dir(t, ID), func(r *http.Request) bool { return r.Header.Get("X-Api-Key") == key }, map[string]fixture.Route{
		"GET /lidarr/api/v1/system/status":  {File: "system_status.json"},
		"GET /lidarr/api/v1/health":         {File: "health.json"},
		"GET /lidarr/api/v1/queue/status":   {File: "queue_status.json"},
		"GET /lidarr/api/v1/wanted/missing": {File: "wanted_missing.json"},
		"GET /lidarr/api/v1/wanted/cutoff":  {File: "wanted_cutoff.json"},
		"GET /lidarr/api/v1/calendar":       {File: "calendar.json"},
	})
}

func connect(t *testing.T, h *integrationtest.Harness, url, apiKey string) integration.Instance {
	return h.Connect(Integration{}, map[string]any{"url": url, "tls": map[string]any{"mode": "verify"}}, map[string]string{"api_key": apiKey})
}

func TestValidate(t *testing.T) {
	cfg := map[string]any{"url": "https://lidarr.home.arpa/lidarr", "tls": map[string]any{"mode": "tofu"}}
	if errs := integrationtest.Validate(Integration{}, cfg, map[string]string{"api_key": key}); len(errs) != 0 {
		t.Fatalf("importer shape: %v", errs)
	}
	if errs := integrationtest.Validate(Integration{}, cfg, nil); len(errs) == 0 || errs[0].Path != "/api_key" {
		t.Fatalf("no key: %v", errs)
	}
	if _, m, ok := integration.Default.Get(ID); !ok || !reflect.DeepEqual(m.Capabilities, []integration.Capability{"media.calendar"}) || !m.IsSecret("api_key") {
		t.Fatalf("manifest = %+v", m)
	}
}

func TestSync(t *testing.T) {
	up := upstream(t)
	h := integrationtest.New(t)
	inst := connect(t, h, up.URL+"/lidarr", key)
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	rid := "lidarr:i_test/app"
	r, ok := h.Resource(rid)
	if !ok || r.Kind != "app" || r.Name != "Test" || r.Health != integration.HealthOK || r.Attrs["type"] != "lidarr" ||
		r.Attrs["version"] != "2.13.3.4711" || r.Attrs["url"] != up.URL+"/lidarr" {
		t.Fatalf("app = %+v", r)
	}
	if caps, _ := r.Attrs["caps"].([]any); len(caps) != 1 || caps[0] != "media.calendar" {
		t.Fatalf("caps = %v", r.Attrs["caps"])
	}
	// The artist is the title, the album (and its type unless it's an album)
	// the detail; the single before the window is left out.
	want := []any{
		map[string]any{"at": "2026-10-06T00:00:00Z", "title": "Marlow Avenue", "detail": "Night Ferries · EP"},
		map[string]any{"at": "2026-10-09T00:00:00Z", "title": "The Quiet Tide", "detail": "Harbour Lights"},
	}
	if got := r.Attrs["calendar"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("calendar = %#v", got)
	}
	for metric, v := range map[string]float64{"queue": 1, "wanted": 16, "missing.albums": 14} {
		if se := h.Series(rid, metric); se == nil || se["value"] != v || se["unit"] != "count" {
			t.Errorf("%s = %v", metric, se)
		}
	}
	if h.Graph().Relations["integration:i_test monitors "+rid] == nil {
		t.Fatal("the instance should monitor its app")
	}
	for _, req := range up.Requests("/calendar") {
		if req.URL.Query().Get("includeArtist") != "true" || req.URL.Query().Get("start") == "" {
			t.Fatalf("calendar query = %s", req.URL.RawQuery)
		}
	}
	for _, req := range up.Requests("/wanted/missing") {
		if q := req.URL.Query(); q.Get("pageSize") != "1" || q.Get("monitored") != "true" {
			t.Fatalf("wanted query = %s", req.URL.RawQuery)
		}
	}
}

func TestTest(t *testing.T) {
	up := upstream(t)
	res := connect(t, integrationtest.New(t), up.URL+"/lidarr", key).Test(t.Context())
	if !res.OK || res.Version != "2.13.3.4711" || len(res.Grants) != 1 || res.Grants[0].Name != "Lidarr API key" || res.Grants[0].Access != integration.AccessAdmin {
		t.Fatalf("test = %+v", res)
	}
}

func TestFailures(t *testing.T) {
	up := upstream(t)
	// A wrong key: an auth Repair on api_key, the key never quoted.
	res := connect(t, integrationtest.New(t), up.URL+"/lidarr", "wrong-key").Test(t.Context())
	if res.OK || res.ErrorCode != integration.CodeAuth || strings.Contains(res.ErrorMessage, "wrong-key") {
		t.Fatalf("wrong key: %+v", res)
	}
	// Another *arr at that URL.
	up.Set("GET /lidarr/api/v1/system/status", fixture.Route{Body: `{"appName":"Readarr","version":"0.4.18.2805"}`})
	res = connect(t, integrationtest.New(t), up.URL+"/lidarr", key).Test(t.Context())
	if res.OK || res.ErrorCode != integration.CodeInvalidConfig || !strings.Contains(res.ErrorMessage, "Readarr, not Lidarr") {
		t.Fatalf("other app: %+v", res)
	}
	// Lidarr 0.x.
	up.Set("GET /lidarr/api/v1/system/status", fixture.Route{Body: `{"appName":"Lidarr","version":"0.8.1.2135"}`})
	res = connect(t, integrationtest.New(t), up.URL+"/lidarr", key).Test(t.Context())
	if res.OK || res.ErrorCode != integration.CodeUnsupportedVersion {
		t.Fatalf("0.x: %+v", res)
	}
	// No URL base: every endpoint is a 404.
	res = connect(t, integrationtest.New(t), up.URL, key).Test(t.Context())
	if res.OK || res.ErrorCode != integration.CodeAPIChanged {
		t.Fatalf("no URL base: %+v", res)
	}
}

func TestQueueErrors(t *testing.T) {
	up := upstream(t)
	up.Set("GET /lidarr/api/v1/queue/status", fixture.Route{Body: `{"totalCount":3,"count":3,"errors":true}`})
	h := integrationtest.New(t)
	if err := connect(t, h, up.URL+"/lidarr", key).Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	r, _ := h.Resource("lidarr:i_test/app")
	if r.Health != integration.HealthAttention || r.Attrs["health.reason"] != "A download in Lidarr's queue has an error" {
		t.Fatalf("health = %s %v", r.Health, r.Attrs["health.reason"])
	}
}
