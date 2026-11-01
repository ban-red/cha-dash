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

// A Dockhand API token is dh_ and 43 base64url characters; this one is made up.
const dockhandToken = "dh_fAkE0123456789abcdefghijklmnopqrstuvwxyzAB"

// dockhand answers GET /api/stacks?env=1 as Dockhand 1.0 does. With auth
// on, it reads a bearer token only when it starts dh_; with auth off
// (open), it answers anyone and ignores the header.
func dockhand(t *testing.T, open bool) *app {
	return &app{t: t, dir: "testdata/dockhand", base: "/dockhand",
		auth: func(r *http.Request) bool {
			return open || r.Header.Get("Authorization") == "Bearer "+dockhandToken
		},
		route: func(r *http.Request) string {
			if r.URL.Path == "/dockhand/api/stacks" && r.URL.Query().Get("env") != "1" {
				return ""
			}
			return strings.TrimPrefix(r.URL.Path, "/dockhand")
		},
		unauthorized: "unauthorized.json",
		routes:       map[string]string{"/api/stacks": "stacks.json"}}
}

func TestDockhand(t *testing.T) {
	integ, m, ok := integration.Default.Get("dockhand")
	if !ok || m.Engine != integration.EngineSpec || m.Name != "Dockhand" || !slices.Equal(m.SecretFields(), []string{"token"}) {
		t.Fatalf("dockhand isn't registered as a spec with a token: %+v", m)
	}
	// The token is optional: Dockhand without authentication needs none.
	if errs := integrationtest.Validate(integ, map[string]any{"url": "https://dockhand.home.example", "environment": "1"}, nil); len(errs) != 0 {
		t.Fatalf("no token: %v", errs)
	}
	if errs := integrationtest.Validate(integ, map[string]any{"url": "https://dockhand.home.example", "environment": "1&x=y"}, nil); len(errs) == 0 {
		t.Fatal("an environment ID that isn't a number should be refused")
	}
	up := dockhand(t, false)
	srv := up.start()
	url := srv.URL + "/dockhand"
	h := integrationtest.New(t)
	inst := h.Connect(integ, map[string]any{"url": url, "environment": "1"}, map[string]string{"token": dockhandToken})
	if res := inst.Test(t.Context()); !res.OK || res.Version != "" {
		t.Fatalf("test = %+v", res)
	}
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	attrs := checkApp(t, h, "dockhand", url)
	app, _ := h.Resource("dockhand:i_test/app")
	if app.Health != integration.HealthAttention || app.Attrs["health.reason"] != "paperless is unhealthy, photos is partly running" || attrs["environment"] != "1" {
		t.Fatalf("app = %+v", app)
	}
	checkSeries(t, h, "dockhand", map[string]any{"stacks": 4.0, "stacks.running": 2.0, "updates": 1.0}, nil)

	media, _ := h.Resource("dockhand:i_test/stack/media")
	if media.Kind != "stack" || media.Name != "media" || media.State != "running" || media.Health != integration.HealthOK ||
		media.Attrs["update_available"] != true || media.Attrs["updates"] != 1.0 || media.Attrs["stack_health"] != "healthy" || media.Attrs["source"] != "internal" ||
		!reflect.DeepEqual(media.Attrs["services"], []any{"radarr", "sonarr"}) {
		t.Fatalf("media = %+v", media)
	}
	paperless, _ := h.Resource("dockhand:i_test/stack/paperless")
	if paperless.State != "running" || paperless.Health != integration.HealthAttention || paperless.Attrs["health.reason"] != "webserver is unhealthy" || paperless.Attrs["source"] != "git" {
		t.Fatalf("paperless = %+v", paperless)
	}
	photos, _ := h.Resource("dockhand:i_test/stack/photos")
	if photos.State != "partial" || photos.Health != integration.HealthAttention || photos.Attrs["health.reason"] != "ml isn't running (exited)" ||
		photos.Attrs["stack_health"] != "none" || photos.Attrs["source"] != nil {
		t.Fatalf("photos = %+v", photos)
	}
	// Defined in Dockhand, never started: no containers, no update fields.
	sandbox, _ := h.Resource("dockhand:i_test/stack/sandbox")
	if sandbox.State != "created" || sandbox.Health != integration.HealthOK || sandbox.Attrs["update_available"] != false ||
		sandbox.Attrs["updates"] != nil || !reflect.DeepEqual(sandbox.Attrs["services"], []any{}) {
		t.Fatalf("sandbox = %+v", sandbox)
	}
	if h.Graph().Relations["dockhand:i_test/stack/media part_of dockhand:i_test/app"] == nil {
		t.Fatal("a stack should be part of the app")
	}
	for _, req := range up.requests() {
		if strings.Contains(req.URL.String(), dockhandToken) {
			t.Fatal("the token is in a URL")
		}
	}
}

// With Dockhand's authentication off, no token is needed.
func TestDockhandWithoutAuth(t *testing.T) {
	integ, _, _ := integration.Default.Get("dockhand")
	srv := dockhand(t, true).start()
	h := integrationtest.New(t)
	inst := h.Connect(integ, map[string]any{"url": srv.URL + "/dockhand", "environment": "1"}, nil)
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	checkSeries(t, h, "dockhand", map[string]any{"stacks": 4.0}, nil)
}

func TestDockhandFailures(t *testing.T) {
	integ, _, _ := integration.Default.Get("dockhand")
	srv := dockhand(t, false).start()
	h := integrationtest.New(t)
	// Authentication on and no token: Dockhand's 401.
	res := h.Connect(integ, map[string]any{"url": srv.URL + "/dockhand", "environment": "1"}, nil).Test(t.Context())
	if res.OK || res.ErrorCode != integration.CodeAuth {
		t.Fatalf("no token: %+v", res)
	}
	res = h.Connect(integ, map[string]any{"url": srv.URL + "/dockhand", "environment": "1"}, map[string]string{"token": "dh_wrongwrongwrong"}).Test(t.Context())
	if res.OK || res.ErrorCode != integration.CodeAuth || strings.Contains(res.ErrorMessage, "dh_wrong") {
		t.Fatalf("wrong token: %+v", res)
	}
}
