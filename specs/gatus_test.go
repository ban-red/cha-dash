package specs

import (
	"net/http"
	"testing"

	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/integration/integrationtest"
)

func gatus(t *testing.T) *app {
	return &app{t: t, dir: "testdata/gatus", base: "/gatus",
		auth: func(r *http.Request) bool { return r.Header.Get("Authorization") == "" },
		route: func(r *http.Request) string {
			if r.URL.Query().Get("page") != "1" || r.URL.Query().Get("pageSize") != "1" {
				return "unpaged"
			}
			return r.URL.Path[len("/gatus"):]
		},
		routes: map[string]string{"/api/v1/endpoints/statuses": "statuses.json"}}
}

func TestGatus(t *testing.T) {
	integ, m, ok := integration.Default.Get("gatus")
	if !ok || m.Engine != integration.EngineSpec || m.Name != "Gatus" || len(m.SecretFields()) != 0 {
		t.Fatalf("gatus isn't registered as a spec without a credential: %+v", m)
	}
	if errs := integrationtest.Validate(integ, map[string]any{"url": "http://10.20.30.6:8080", "tls": map[string]any{"mode": "verify"}}, nil); len(errs) != 0 {
		t.Fatalf("config: %v", errs)
	}
	srv := gatus(t).start()
	url := srv.URL + "/gatus"
	h := integrationtest.New(t)
	inst := h.Connect(integ, map[string]any{"url": url}, nil)
	if res := inst.Test(t.Context()); !res.OK || res.Version != "" {
		t.Fatalf("test = %+v", res)
	}
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	attrs := checkApp(t, h, "gatus", url)
	app, _ := h.Resource("gatus:i_test/app")
	if app.Health != integration.HealthAttention || app.Attrs["health.reason"] != "2 of 4 checks are failing" || attrs["endpoints"] != 4.0 {
		t.Fatalf("app = %+v", app)
	}
	checkSeries(t, h, "gatus", map[string]any{"checks": 4.0, "checks.down": 2.0}, nil)

	for id, want := range map[string]struct {
		name, state string
		health      string
		reason      string
		attrs       map[string]any
	}{
		"core_front-door": {"core / Front door", "online", integration.HealthOK, "",
			map[string]any{"check": "gatus", "group": "core", "target": "home.example", "status_code": 200.0, "response_ms": 48.0, "checked_at": "2026-10-31T21:59:30.123456789Z"}},
		"apps_photos": {"apps / Photos", "error", integration.HealthDown, "Failed: [STATUS] == 200",
			map[string]any{"status_code": 502.0, "response_ms": 1204.0}},
		"nas-ssh": {"nas-ssh", "error", integration.HealthDown, "dial tcp 10.20.30.12:22: i/o timeout",
			map[string]any{"target": "10.20.30.12", "response_ms": 2000.0}},
		"external_backups-bucket": {"external / Backups bucket", "unknown", integration.HealthUnknown, "", map[string]any{}},
	} {
		rid := "gatus:i_test/check/" + id
		r, ok := h.Resource(rid)
		if !ok || r.Kind != "check" || r.Name != want.name || r.State != want.state || string(r.Health) != want.health {
			t.Errorf("%s = %+v", id, r)
			continue
		}
		if reason, _ := r.Attrs["health.reason"].(string); reason != want.reason {
			t.Errorf("%s: reason %q, want %q", id, reason, want.reason)
		}
		for k, v := range want.attrs {
			if r.Attrs[k] != v {
				t.Errorf("%s: %s = %v, want %v", id, k, r.Attrs[k], v)
			}
		}
		if h.Graph().Relations[rid+" part_of gatus:i_test/app"] == nil || h.Graph().Relations["integration:i_test monitors "+rid] == nil {
			t.Errorf("%s: relations", id)
		}
	}
	if r, _ := h.Resource("gatus:i_test/check/nas-ssh"); r.Attrs["group"] != nil || r.Attrs["status_code"] != nil {
		t.Errorf("no group, no status: %v", r.Attrs)
	}

	// Everything passing: the app is ok.
	up := gatus(t)
	up.routes = map[string]string{"/api/v1/endpoints/statuses": "statuses_ok.json"}
	h2 := integrationtest.New(t)
	inst2 := h2.Connect(integ, map[string]any{"url": up.start().URL + "/gatus"}, nil)
	if err := inst2.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	if r, _ := h2.Resource("gatus:i_test/app"); r.Health != integration.HealthOK {
		t.Fatalf("all passing: %+v", r)
	}
}

func TestGatusFailures(t *testing.T) {
	integ, _, _ := integration.Default.Get("gatus")
	srv := gatus(t).start()
	// Security on in Gatus: the API answers 401, an auth Repair.
	locked := &app{t: t, dir: "testdata/gatus", base: "/gatus", auth: func(*http.Request) bool { return false }}
	res := integrationtest.New(t).Connect(integ, map[string]any{"url": locked.start().URL + "/gatus"}, nil).Test(t.Context())
	if res.OK || res.ErrorCode != integration.CodeAuth {
		t.Fatalf("security on: %+v", res)
	}
	res = integrationtest.New(t).Connect(integ, map[string]any{"url": srv.URL + "/elsewhere"}, nil).Test(t.Context())
	if res.OK || res.ErrorCode != integration.CodeUnreachable {
		t.Fatalf("wrong url: %+v", res)
	}
}
