package specs

import (
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"

	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/integration/integrationtest"
)

const (
	komodoKey    = "K-fake0123456789abcdef"
	komodoSecret = "S-fake0123456789abcdef0123456789"
)

// komodo answers POST /read by the body's type, as Komodo Core does.
func komodo(t *testing.T) *app {
	return &app{t: t, dir: "testdata/komodo", base: "/komodo",
		auth: func(r *http.Request) bool {
			return r.Header.Get("X-Api-Key") == komodoKey && r.Header.Get("X-Api-Secret") == komodoSecret
		},
		route: func(r *http.Request) string {
			if r.Method != http.MethodPost || r.URL.Path != "/komodo/read" || r.Header.Get("Content-Type") != "application/json" {
				return ""
			}
			var body struct {
				Type   string
				Params map[string]any
			}
			b, _ := io.ReadAll(r.Body)
			if json.Unmarshal(b, &body) != nil || body.Params == nil {
				return ""
			}
			return body.Type
		},
		routes: map[string]string{"GetVersion": "version.json", "ListStacks": "stacks.json", "ListServers": "servers.json"}}
}

func TestKomodo(t *testing.T) {
	integ, m, ok := integration.Default.Get("komodo")
	if !ok || m.Engine != integration.EngineSpec || m.Name != "Komodo" || !slices.Equal(m.SecretFields(), []string{"api_key", "api_secret"}) {
		t.Fatalf("komodo isn't registered as a spec with a key and a secret: %+v", m)
	}
	if errs := integrationtest.Validate(integ, map[string]any{"url": "https://komodo.home.example"}, map[string]string{"api_key": "x"}); len(errs) == 0 || errs[0].Path != "/api_secret" {
		t.Fatalf("a missing secret: %v", errs)
	}
	up := komodo(t)
	srv := up.start()
	url := srv.URL + "/komodo"
	h := integrationtest.New(t)
	inst := h.Connect(integ, map[string]any{"url": url}, map[string]string{"api_key": komodoKey, "api_secret": komodoSecret})
	if res := inst.Test(t.Context()); !res.OK || res.Version != "1.19.5" {
		t.Fatalf("test = %+v", res)
	}
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	attrs := checkApp(t, h, "komodo", url)
	app, _ := h.Resource("komodo:i_test/app")
	if app.Health != integration.HealthAttention || app.Attrs["health.reason"] != "paperless is unhealthy" || attrs["version"] != "1.19.5" || attrs["servers"] != 2.0 {
		t.Fatalf("app = %+v", app)
	}
	checkSeries(t, h, "komodo", map[string]any{"stacks": 3.0, "stacks.running": 1.0, "updates": 1.0}, nil)

	media, _ := h.Resource("komodo:i_test/stack/6720a1b2c3d4e5f601234567")
	if media.Kind != "stack" || media.Name != "media" || media.State != "running" || media.Health != integration.HealthOK ||
		media.Attrs["server"] != "magpie" || media.Attrs["update_available"] != true || media.Attrs["status"] != "Up 3 days" ||
		!reflect.DeepEqual(media.Attrs["services"], []any{"sonarr", "radarr"}) || media.Attrs["repo"] != nil || media.Attrs["behind"] != nil {
		t.Fatalf("media = %+v", media)
	}
	paperless, _ := h.Resource("komodo:i_test/stack/6720a1b2c3d4e5f601234568")
	if paperless.State != "unhealthy" || paperless.Health != integration.HealthAttention || paperless.Attrs["health.reason"] != "Komodo says it's unhealthy (Up 2 hours (unhealthy))" ||
		paperless.Attrs["server"] != "egret" || paperless.Attrs["repo"] != "lab/stacks" || paperless.Attrs["branch"] != "main" || paperless.Attrs["behind"] != true {
		t.Fatalf("paperless = %+v", paperless)
	}
	sandbox, _ := h.Resource("komodo:i_test/stack/6720a1b2c3d4e5f601234569")
	if sandbox.State != "stopped" || sandbox.Health != integration.HealthOK || sandbox.Attrs["status"] != nil || sandbox.Attrs["update_available"] != false {
		t.Fatalf("sandbox = %+v", sandbox)
	}
	if h.Graph().Relations["komodo:i_test/stack/6720a1b2c3d4e5f601234567 part_of komodo:i_test/app"] == nil {
		t.Fatal("a stack should be part of the app")
	}
	for _, req := range up.requests() {
		if strings.Contains(req.URL.String(), komodoKey) || strings.Contains(req.URL.String(), komodoSecret) {
			t.Fatal("a secret is in a URL")
		}
	}

	// A user without read on servers: stacks still read, without server names.
	up.set("ListServers", "")
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	if media, _ := h.Resource("komodo:i_test/stack/6720a1b2c3d4e5f601234567"); media.Attrs["server"] != nil || media.State != "running" {
		t.Fatalf("without servers: %+v", media)
	}
}

func TestKomodoFailures(t *testing.T) {
	integ, _, _ := integration.Default.Get("komodo")
	srv := komodo(t).start()
	res := integrationtest.New(t).Connect(integ, map[string]any{"url": srv.URL + "/komodo"}, map[string]string{"api_key": komodoKey, "api_secret": "wrong-secret"}).Test(t.Context())
	if res.OK || res.ErrorCode != integration.CodeAuth || strings.Contains(res.ErrorMessage, "wrong-secret") {
		t.Fatalf("wrong secret: %+v", res)
	}
}
