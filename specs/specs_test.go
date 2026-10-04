package specs

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/integration/integrationtest"
)

const key = "fake-seerr-key"

// seerr serves the fixtures under an optional base path, the way Seerr
// answers behind a reverse proxy.
func seerr(t *testing.T, base string) (*httptest.Server, *atomic.Value) {
	t.Helper()
	files := &atomic.Value{}
	files.Store(map[string]string{
		"/api/v1/status":        "testdata/seerr/status.json",
		"/api/v1/request/count": "testdata/seerr/request_count.json",
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != key {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		name, ok := files.Load().(map[string]string)[strings.TrimPrefix(r.URL.Path, base)]
		if !ok {
			http.NotFound(w, r)
			return
		}
		b, err := os.ReadFile(name)
		if err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
	}))
	t.Cleanup(srv.Close)
	return srv, files
}

func seerrIntegration(t *testing.T) integration.Integration {
	integ, m, ok := integration.Default.Get("seerr")
	if !ok || m.Engine != integration.EngineSpec || m.Name != "Seerr" || !m.IsSecret("api_key") {
		t.Fatalf("seerr isn't registered as a spec: %+v", m)
	}
	return integ
}

func TestSeerr(t *testing.T) {
	srv, _ := seerr(t, "/seerr")
	h := integrationtest.New(t)
	inst := h.Connect(seerrIntegration(t), map[string]any{"url": srv.URL + "/seerr/"}, map[string]string{"api_key": key})

	res := inst.Test(t.Context())
	if !res.OK || res.Version != "2.7.3" {
		t.Fatalf("test = %+v", res)
	}
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	r, ok := h.Resource("seerr:i_test/app")
	if !ok || r.Kind != "app" || r.Name != "Test" || r.State != "running" || r.Health != integration.HealthOK ||
		r.Attrs["type"] != "seerr" || r.Attrs["version"] != "2.7.3" || r.Attrs["update_available"] != false {
		t.Fatalf("resource = %+v", r)
	}
	if links := r.Attrs["links"].([]any); links[0].(map[string]any)["url"] != srv.URL+"/seerr/" {
		t.Fatalf("links = %v", links)
	}
	for metric, want := range map[string]float64{"requests.pending": 3, "requests.processing": 2, "requests.available": 389} {
		if se := h.Series("seerr:i_test/app", metric); se == nil || se["value"] != want || se["unit"] != "count" {
			t.Errorf("%s = %v, want %v", metric, se, want)
		}
	}
	if h.Graph().Relations["integration:i_test monitors seerr:i_test/app"] == nil {
		t.Fatal("the instance should monitor its app")
	}
}

func TestSeerrThreshold(t *testing.T) {
	srv, _ := seerr(t, "")
	h := integrationtest.New(t)
	inst := h.Connect(seerrIntegration(t), map[string]any{"url": srv.URL, "pending_warn": 3}, map[string]string{"api_key": key})
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	r, _ := h.Resource("seerr:i_test/app")
	if r.Health != integration.HealthAttention || r.Attrs["health.reason"] != "3 requests are waiting for approval" {
		t.Fatalf("over the threshold: %+v", r)
	}
}

func TestSeerrFailures(t *testing.T) {
	srv, files := seerr(t, "")
	integ := seerrIntegration(t)

	// A wrong key is an auth failure: the runtime turns it into a Repair.
	h := integrationtest.New(t)
	res := h.Connect(integ, map[string]any{"url": srv.URL}, map[string]string{"api_key": "wrong"}).Test(t.Context())
	if res.OK || res.ErrorCode != integration.CodeAuth {
		t.Fatalf("wrong key: %+v", res)
	}
	if strings.Contains(res.ErrorMessage, "wrong") {
		t.Fatalf("the message quotes the key: %s", res.ErrorMessage)
	}

	// An endpoint that disappears is "the API changed", naming the version.
	files.Store(map[string]string{"/api/v1/status": "testdata/seerr/status.json"})
	h = integrationtest.New(t)
	inst := h.Connect(integ, map[string]any{"url": srv.URL}, map[string]string{"api_key": key})
	err := inst.Sync(t.Context(), integrationtest.Watched)
	code, class, msg := integration.Classify(err)
	if code != integration.CodeAPIChanged || class != integration.NeedsRepair ||
		msg != "Seerr's API changed: GET /api/v1/request/count answered 404 Not Found (detected version 2.7.3)" {
		t.Fatalf("404: %s %d %q", code, class, msg)
	}
}

func TestBundledSpecsMatchTheirFiles(t *testing.T) {
	entries, err := FS.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		id := strings.TrimSuffix(e.Name(), ".yaml")
		if _, m, ok := integration.Default.Get(id); !ok || m.Engine != integration.EngineSpec {
			t.Errorf("%s isn't registered as a spec", e.Name())
		}
	}
}
