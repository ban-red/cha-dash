package prowlarr

import (
	"net/http"
	"reflect"
	"testing"
	"time"

	"dash.cha.sh/integrations/internal/fixture"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/integration/integrationtest"
)

const key = "00112233445566778899aabbccddeeff" // synthetic

func upstream(t *testing.T) *fixture.Server {
	return fixture.New(t, fixture.Dir(t, ID), func(r *http.Request) bool { return r.Header.Get("X-Api-Key") == key }, map[string]fixture.Route{
		"GET /prowlarr/api/v1/system/status":          {File: "system_status.json"},
		"GET /prowlarr/api/v1/health":                 {File: "health.json"},
		"GET /prowlarr/api/v1/indexer":                {File: "indexer.json"},
		"GET /prowlarr/api/v1/indexerstatus":          {File: "indexerstatus.json"},
		"GET /prowlarr/api/v1/indexerstats":           {File: "indexerstats.json"},
		"GET /prowlarr/api/v1/indexerstats?startDate": {File: "indexerstats_24h.json"},
	})
}

func connect(t *testing.T, h *integrationtest.Harness, url string) integration.Instance {
	return h.Connect(Integration{}, map[string]any{"url": url, "tls": map[string]any{"mode": "verify"}}, map[string]string{"api_key": key})
}

func TestValidate(t *testing.T) {
	cfg := map[string]any{"url": "https://jupiter.lab.example/prowlarr", "tls": map[string]any{"mode": "verify"}}
	if errs := integrationtest.Validate(Integration{}, cfg, map[string]string{"api_key": key}); len(errs) != 0 {
		t.Fatalf("importer shape: %v", errs)
	}
	if _, m, ok := integration.Default.Get(ID); !ok || !reflect.DeepEqual(m.Capabilities, []integration.Capability{"downloads.indexers"}) {
		t.Fatalf("manifest = %+v", m)
	}
}

func TestSync(t *testing.T) {
	up := upstream(t)
	h := integrationtest.New(t)
	if err := connect(t, h, up.URL+"/prowlarr").Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	rid := "prowlarr:i_test/app"
	r, ok := h.Resource(rid)
	// Archive Tracker is failing; Retired Tracker is too, but it's switched
	// off, so it doesn't count. The health check's warning alone wouldn't
	// make it attention.
	if !ok || r.Kind != "app" || r.Attrs["type"] != "prowlarr" || r.Attrs["version"] != "1.37.0.5076" ||
		r.Health != integration.HealthAttention || r.Attrs["health.reason"] != "Archive Tracker is failing (1 of 3 indexers)" ||
		r.Attrs["indexers"] != 3.0 || !reflect.DeepEqual(r.Attrs["indexers_failing"], []any{"Archive Tracker"}) {
		t.Fatalf("app = %+v", r)
	}
	for metric, v := range map[string]float64{"grabs": 1100, "queries.24h": 54, "failures.24h": 5, "indexers": 3, "indexers.failing": 1} {
		if se := h.Series(rid, metric); se == nil || se["value"] != v {
			t.Errorf("%s = %v", metric, se)
		}
	}
	day := up.Requests("/indexerstats")[1].URL.Query()
	if day.Get("startDate") != "2026-10-02T14:05:00Z" || day.Get("endDate") != "2026-10-03T14:05:00Z" {
		t.Fatalf("24 h stats query = %v", day)
	}

	// Once the indexer recovers (no status entry), the app is ok again.
	up.Set("GET /prowlarr/api/v1/indexerstatus", fixture.Route{Body: "[]"})
	up.Set("GET /prowlarr/api/v1/health", fixture.Route{Body: "[]"})
	h.Advance(time.Minute)
	up.Reset()
	if err := connect(t, h, up.URL+"/prowlarr").Sync(t.Context(), integration.Interest{}); err != nil {
		t.Fatal(err)
	}
	if r, _ := h.Resource(rid); r.Health != integration.HealthOK || r.Attrs["indexers_failing"] != nil {
		t.Fatalf("recovered = %+v", r)
	}
}

func TestFailingReason(t *testing.T) {
	if got := failingReason([]string{"A", "B", "C", "D", "E"}, 9); got != "A, B, C and 2 more are failing (5 of 9 indexers)" {
		t.Fatal(got)
	}
	now := time.Date(2026, 10, 3, 14, 5, 0, 0, time.UTC)
	past := now.Add(-time.Hour)
	enabled, failing := failingIndexers(
		[]indexer{{ID: 1, Name: "B", Enable: true}, {ID: 2, Name: "A", Enable: true}, {ID: 3, Name: "C", Enable: true}},
		[]indexerStatus{{IndexerID: 1, DisabledTill: &past}, {IndexerID: 2, MostRecentFailure: &past}, {IndexerID: 3}}, now)
	if enabled != 3 || !reflect.DeepEqual(failing, []string{"A"}) {
		t.Fatalf("%d %v", enabled, failing)
	}
}

func TestTest(t *testing.T) {
	up := upstream(t)
	res := connect(t, integrationtest.New(t), up.URL+"/prowlarr").Test(t.Context())
	if !res.OK || res.Version != "1.37.0.5076" || res.Grants[0].Access != integration.AccessAdmin {
		t.Fatalf("test = %+v", res)
	}
	h := integrationtest.New(t)
	bad := h.Connect(Integration{}, map[string]any{"url": up.URL + "/prowlarr"}, map[string]string{"api_key": "nope-nope"})
	if res := bad.Test(t.Context()); res.OK || res.ErrorCode != integration.CodeAuth {
		t.Fatalf("bad key = %+v", res)
	}
}
