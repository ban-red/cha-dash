package specs

import (
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"

	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/integration/integrationtest"
)

// An Arcane API key is arc_ and 64 hex digits; this one is made up.
const arcaneKey = "arc_0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c4b5a69788796a5b4c3d2e1f0"

// arcane answers as Arcane 2.17 does: /api/app-version without a key
// (Arcane's is public, but Cha Dash sends it anyway), the project list with
// the X-API-Key header, paginated by start and limit.
func arcane(t *testing.T) *app {
	return &app{t: t, dir: "testdata/arcane", base: "/arcane",
		auth: func(r *http.Request) bool { return r.Header.Get("X-API-Key") == arcaneKey },
		route: func(r *http.Request) string {
			q := r.URL.Query()
			if strings.HasSuffix(r.URL.Path, "/projects") && (q.Get("start") != "0" || q.Get("limit") != "50" || q.Get("sort") != "name") {
				return ""
			}
			return strings.TrimPrefix(r.URL.Path, "/arcane")
		},
		routes: map[string]string{
			"/api/app-version":             "app-version.json",
			"/api/environments/0/projects": "projects.json",
		}}
}

func TestArcane(t *testing.T) {
	integ, m, ok := integration.Default.Get("arcane")
	if !ok || m.Engine != integration.EngineSpec || m.Name != "Arcane" || !slices.Equal(m.SecretFields(), []string{"api_key"}) {
		t.Fatalf("arcane isn't registered as a spec with an API key: %+v", m)
	}
	cfg := map[string]any{"url": "https://arcane.home.example", "environment": "0"}
	if errs := integrationtest.Validate(integ, cfg, map[string]string{"api_key": arcaneKey}); len(errs) != 0 {
		t.Fatalf("a full config: %v", errs)
	}
	if errs := integrationtest.Validate(integ, map[string]any{"url": "https://arcane.home.example", "environment": "../users"}, map[string]string{"api_key": arcaneKey}); len(errs) == 0 {
		t.Fatal("an environment ID that isn't one should be refused")
	}
	up := arcane(t)
	srv := up.start()
	url := srv.URL + "/arcane"
	h := integrationtest.New(t)
	inst := h.Connect(integ, map[string]any{"url": url, "environment": "0"}, map[string]string{"api_key": arcaneKey})
	if res := inst.Test(t.Context()); !res.OK || res.Version != "2.17.2" {
		t.Fatalf("test = %+v", res)
	}
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	attrs := checkApp(t, h, "arcane", url)
	app, _ := h.Resource("arcane:i_test/app")
	if app.Health != integration.HealthAttention || app.Attrs["health.reason"] != "paperless is unhealthy" || attrs["version"] != "2.17.2" || attrs["environment"] != "0" {
		t.Fatalf("app = %+v", app)
	}
	checkSeries(t, h, "arcane", map[string]any{"stacks": 3.0, "stacks.running": 1.0, "updates": 1.0}, nil)

	media, _ := h.Resource("arcane:i_test/stack/5b0e8d4a-2f1c-4e7b-9a63-0c1d2e3f4a51")
	if media.Kind != "stack" || media.Name != "media" || media.State != "running" || media.Health != integration.HealthOK ||
		media.Attrs["update_available"] != true || media.Attrs["running"] != 2.0 || media.Attrs["gitops"] != false ||
		!reflect.DeepEqual(media.Attrs["services"], []any{"radarr", "sonarr"}) {
		t.Fatalf("media = %+v", media)
	}
	paperless, _ := h.Resource("arcane:i_test/stack/5b0e8d4a-2f1c-4e7b-9a63-0c1d2e3f4a52")
	if paperless.State != "partial" || paperless.Health != integration.HealthAttention || paperless.Attrs["health.reason"] != "webserver is unhealthy" ||
		paperless.Attrs["gitops"] != true || paperless.Attrs["update_available"] != false || !reflect.DeepEqual(paperless.Attrs["tags"], []any{"docs"}) {
		t.Fatalf("paperless = %+v", paperless)
	}
	sandbox, _ := h.Resource("arcane:i_test/stack/5b0e8d4a-2f1c-4e7b-9a63-0c1d2e3f4a53")
	if sandbox.State != "stopped" || sandbox.Health != integration.HealthOK || !reflect.DeepEqual(sandbox.Attrs["services"], []any{}) || sandbox.Attrs["service_count"] != 1.0 {
		t.Fatalf("sandbox = %+v", sandbox)
	}
	if h.Graph().Relations["arcane:i_test/stack/5b0e8d4a-2f1c-4e7b-9a63-0c1d2e3f4a51 part_of arcane:i_test/app"] == nil {
		t.Fatal("a project should be part of the app")
	}
	for _, req := range up.requests() {
		if strings.Contains(req.URL.String(), arcaneKey) {
			t.Fatal("the key is in a URL")
		}
	}

	// Without the version (a proxy in front), the projects still read.
	up.set("/api/app-version", "")
	if res := inst.Test(t.Context()); !res.OK || res.Version != "" {
		t.Fatalf("no version: %+v", res)
	}
}

// A project whose services all run but one is partly up, without a health
// check: the reason counts them, from Arcane's own runningCount.
func TestArcanePartlyRunning(t *testing.T) {
	integ, _, _ := integration.Default.Get("arcane")
	up := arcane(t)
	up.set("/api/environments/0/projects", `{"success": true, "data": [{"tags": [], "status": "partially running", "name": "photos", "id": "5b0e8d4a-2f1c-4e7b-9a63-0c1d2e3f4a54",
		"path": "/app/data/projects/photos", "updatedAt": "2026-10-30T08:00:00Z", "createdAt": "2026-10-01T08:00:00Z",
		"runtimeServices": [{"name": "server", "image": "ghcr.io/immich-app/immich-server:v2", "status": "running"}, {"name": "ml", "image": "ghcr.io/immich-app/immich-machine-learning:v2", "status": "exited"}],
		"runningCount": 1, "serviceCount": 2, "isArchived": false}],
		"pagination": {"totalPages": 1, "totalItems": 1, "currentPage": 1, "itemsPerPage": 50}}`)
	srv := up.start()
	h := integrationtest.New(t)
	inst := h.Connect(integ, map[string]any{"url": srv.URL + "/arcane", "environment": "0"}, map[string]string{"api_key": arcaneKey})
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	photos, _ := h.Resource("arcane:i_test/stack/5b0e8d4a-2f1c-4e7b-9a63-0c1d2e3f4a54")
	if photos.State != "partial" || photos.Health != integration.HealthAttention || photos.Attrs["health.reason"] != "1 of 2 services are running" || photos.Attrs["update_available"] != false {
		t.Fatalf("photos = %+v", photos)
	}
	if app, _ := h.Resource("arcane:i_test/app"); app.Attrs["health.reason"] != "photos is partly running" {
		t.Fatalf("app = %+v", app)
	}
	checkSeries(t, h, "arcane", map[string]any{"updates": 0.0}, nil)
}

func TestArcaneFailures(t *testing.T) {
	integ, _, _ := integration.Default.Get("arcane")
	up := arcane(t)
	srv := up.start()
	h := integrationtest.New(t)
	res := h.Connect(integ, map[string]any{"url": srv.URL + "/arcane", "environment": "0"}, map[string]string{"api_key": "arc_wrongwrongwrong"}).Test(t.Context())
	if res.OK || res.ErrorCode != integration.CodeAuth || strings.Contains(res.ErrorMessage, "arc_wrong") {
		t.Fatalf("wrong key: %+v", res)
	}
	// An environment Arcane doesn't have: its 404 is JSON, so the API changed
	// as far as the engine can tell (Arcane answers 404 for both).
	res = h.Connect(integ, map[string]any{"url": srv.URL + "/arcane", "environment": "7d1c"}, map[string]string{"api_key": arcaneKey}).Test(t.Context())
	if res.OK {
		t.Fatalf("missing environment: %+v", res)
	}
}
