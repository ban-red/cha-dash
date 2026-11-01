package specs

import (
	"net/http"
	"strings"
	"testing"

	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/integration/integrationtest"
)

// A PocketBase auth token is a JWT; this one is made up.
const beszelToken = "eyJhbGciOiJIUzI1NiJ9.eyJpZCI6InU3eDh5OXowYTFiMmMzZCIsInR5cGUiOiJhdXRoIn0.fake-signature-0123456789"

func beszel(t *testing.T) *app {
	return &app{t: t, dir: "testdata/beszel", base: "/beszel",
		auth: func(r *http.Request) bool { return r.Header.Get("Authorization") == beszelToken },
		route: func(r *http.Request) string {
			q := r.URL.Query()
			if r.URL.Path == "/beszel/api/collections/systems/records" && (q.Get("perPage") != "50" || q.Get("sort") != "name") {
				return ""
			}
			return strings.TrimPrefix(r.URL.Path, "/beszel")
		},
		routes: map[string]string{"/api/collections/systems/records": "systems.json", "/api/beszel/getkey": "getkey.json"}}
}

func TestBeszel(t *testing.T) {
	integ := bundled(t, "beszel", "Beszel", "token")
	importerShape(t, integ, "https://beszel.home.example", "token")
	up := beszel(t)
	srv := up.start()
	url := srv.URL + "/beszel"
	h := integrationtest.New(t)
	inst := h.Connect(integ, map[string]any{"url": url}, map[string]string{"token": beszelToken})
	if res := inst.Test(t.Context()); !res.OK || res.Version != "0.12.3" {
		t.Fatalf("test = %+v", res)
	}
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	attrs := checkApp(t, h, "beszel", url)
	app, _ := h.Resource("beszel:i_test/app")
	if app.Health != integration.HealthAttention || app.Attrs["health.reason"] != "stork is down" || attrs["version"] != "0.12.3" {
		t.Fatalf("app = %+v", app)
	}
	checkSeries(t, h, "beszel", map[string]any{"systems": 3.0, "systems.down": 1.0}, nil)

	egret, _ := h.Resource("beszel:i_test/system/a1b2c3d4e5f6g7h")
	want := map[string]any{"hostname": "egret", "address": "10.20.30.31", "kernel": "6.8.12-4-pve", "cores": 8.0, "threads": 16.0,
		"cpu_pct": 12.4, "mem_pct": 61.2, "disk_pct": 47.9, "net_mbps": 10.0, "agent_version": "0.12.3", "uptime_s": 1294033.0}
	if egret.Kind != "host" || egret.Name != "egret" || egret.State != "online" || egret.Health != integration.HealthOK {
		t.Fatalf("egret = %+v", egret)
	}
	for k, v := range want {
		if egret.Attrs[k] != v {
			t.Errorf("egret %s = %v, want %v", k, egret.Attrs[k], v)
		}
	}
	magpie, _ := h.Resource("beszel:i_test/system/b2c3d4e5f6g7h8i")
	if magpie.Health != integration.HealthAttention || magpie.Attrs["health.reason"] != "Its disk is 93% full" {
		t.Fatalf("magpie = %+v", magpie)
	}
	stork, _ := h.Resource("beszel:i_test/system/c3d4e5f6g7h8i9j")
	if stork.State != "offline" || stork.Health != integration.HealthDown || stork.Attrs["cpu_pct"] != nil || stork.Attrs["hostname"] != "stork" {
		t.Fatalf("stork: its last readings aren't current: %+v", stork)
	}
	if h.Graph().Relations["beszel:i_test/system/a1b2c3d4e5f6g7h part_of beszel:i_test/app"] == nil {
		t.Fatal("a system should be part of the app")
	}

	// Without getkey (not PocketBase's), everything else reads.
	up.set("/api/beszel/getkey", "")
	if res := inst.Test(t.Context()); !res.OK || res.Version != "" {
		t.Fatalf("no getkey: %+v", res)
	}
}

func TestBeszelDiskThreshold(t *testing.T) {
	integ := bundled(t, "beszel", "Beszel", "token")
	h := integrationtest.New(t)
	inst := h.Connect(integ, map[string]any{"url": beszel(t).start().URL + "/beszel", "disk_warn": 95}, map[string]string{"token": beszelToken})
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	if magpie, _ := h.Resource("beszel:i_test/system/b2c3d4e5f6g7h8i"); magpie.Health != integration.HealthOK {
		t.Fatalf("93%% under a 95%% threshold: %+v", magpie)
	}
}

func TestBeszelFailures(t *testing.T) {
	integ := bundled(t, "beszel", "Beszel", "token")
	srv := beszel(t).start()
	res := integrationtest.New(t).Connect(integ, map[string]any{"url": srv.URL + "/beszel"}, map[string]string{"token": "expired-token"}).Test(t.Context())
	if res.OK || res.ErrorCode != integration.CodeAuth || strings.Contains(res.ErrorMessage, "expired-token") {
		t.Fatalf("an expired token: %+v", res)
	}
}
