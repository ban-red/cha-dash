package specs

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/integration/integrationtest"
)

// A made-up token in the shape of a Cloudflare API token.
const cloudflareToken = "fake-cloudflare-token-0123456789abcdefghijklmn"

const cloudflareAccount = "0123456789abcdef0123456789abcdef"

func cloudflaretunnel(t *testing.T) *app {
	return &app{t: t, dir: "testdata/cloudflaretunnel", base: "/client/v4", unauthorized: "forbidden.json",
		auth: func(r *http.Request) bool { return r.Header.Get("Authorization") == "Bearer "+cloudflareToken },
		route: func(r *http.Request) string {
			if q := r.URL.Query(); q.Get("is_deleted") != "false" || q.Get("per_page") != "50" {
				return "unfiltered"
			}
			return strings.TrimPrefix(r.URL.Path, "/client/v4")
		},
		routes: map[string]string{"/accounts/" + cloudflareAccount + "/cfd_tunnel": "tunnels.json"}}
}

func TestCloudflareTunnel(t *testing.T) {
	integ := bundled(t, "cloudflaretunnel", "Cloudflare Tunnel", "token")
	cfg := map[string]any{"url": "https://api.cloudflare.com/client/v4", "tls": map[string]any{"mode": "verify"}, "account_id": cloudflareAccount}
	if errs := integrationtest.Validate(integ, cfg, map[string]string{"token": "fake"}); len(errs) != 0 {
		t.Fatalf("config: %v", errs)
	}
	cfg["account_id"] = "not-an-account"
	if errs := integrationtest.Validate(integ, cfg, map[string]string{"token": "fake"}); len(errs) == 0 {
		t.Fatal("an account ID is 32 hex digits")
	}
	up := cloudflaretunnel(t)
	url := up.start().URL + "/client/v4"
	h := integrationtest.New(t)
	inst := h.Connect(integ, map[string]any{"url": url, "account_id": cloudflareAccount}, map[string]string{"token": cloudflareToken})
	if res := inst.Test(t.Context()); !res.OK || res.Version != "" {
		t.Fatalf("test = %+v", res)
	}
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	app, ok := h.Resource("cloudflaretunnel:i_test/app")
	if !ok || app.Kind != "app" || app.State != "running" || app.Attrs["type"] != "cloudflaretunnel" ||
		app.Health != integration.HealthAttention || app.Attrs["health.reason"] != "cabin is down; media is degraded" || app.Attrs["tunnels"] != 4.0 {
		t.Fatalf("app = %+v", app)
	}
	if links, _ := app.Attrs["links"].([]any); len(links) != 1 || links[0].(map[string]any)["url"] != url {
		t.Fatalf("links = %v", app.Attrs["links"])
	}
	checkSeries(t, h, "cloudflaretunnel", map[string]any{"tunnels": 4.0, "tunnels.healthy": 1.0, "tunnels.down": 1.0}, nil)

	for id, want := range map[string]struct {
		name, state, health, reason string
	}{
		"2f6c1a7e-4b1d-4c9a-9e0f-0a1b2c3d4e01": {"home", "online", integration.HealthOK, ""},
		"2f6c1a7e-4b1d-4c9a-9e0f-0a1b2c3d4e02": {"media", "online", integration.HealthAttention, "Cloudflare reports it degraded: it serves traffic, but a connector or connection isn't healthy"},
		"2f6c1a7e-4b1d-4c9a-9e0f-0a1b2c3d4e03": {"cabin", "offline", integration.HealthDown, "No connector is connected to Cloudflare since 2026-11-04 03:12 UTC"},
		"2f6c1a7e-4b1d-4c9a-9e0f-0a1b2c3d4e04": {"spare", "inactive", integration.HealthUnknown, "It has never run"},
	} {
		rid := "cloudflaretunnel:i_test/tunnel/" + id
		r, ok := h.Resource(rid)
		if !ok || r.Kind != "tunnel" || r.Name != want.name || r.State != want.state || string(r.Health) != want.health {
			t.Errorf("%s = %+v", want.name, r)
			continue
		}
		if reason, _ := r.Attrs["health.reason"].(string); reason != want.reason {
			t.Errorf("%s: reason %q, want %q", want.name, reason, want.reason)
		}
		if h.Graph().Relations[rid+" part_of cloudflaretunnel:i_test/app"] == nil {
			t.Errorf("%s: not part of the app", want.name)
		}
	}
	home, _ := h.Resource("cloudflaretunnel:i_test/tunnel/2f6c1a7e-4b1d-4c9a-9e0f-0a1b2c3d4e01")
	for k, v := range map[string]any{"status": "healthy", "config_src": "cloudflare", "connections": 4.0, "connectors": 1.0,
		"client_version": "2026.10.0", "active_at": "2025-06-01T10:05:00.000000Z"} {
		if home.Attrs[k] != v {
			t.Errorf("home %s = %v, want %v", k, home.Attrs[k], v)
		}
	}
	if !reflect.DeepEqual(home.Attrs["colos"], []any{"ams01", "fra02"}) || home.Attrs["inactive_at"] != nil {
		t.Errorf("home = %v", home.Attrs)
	}
	// No connections listed (the list's are deprecated): unknown, not 0.
	cabin, _ := h.Resource("cloudflaretunnel:i_test/tunnel/2f6c1a7e-4b1d-4c9a-9e0f-0a1b2c3d4e03")
	if cabin.Attrs["connections"] != nil || cabin.Attrs["colos"] != nil {
		t.Errorf("cabin = %v", cabin.Attrs)
	}
	doc, _ := json.Marshal(h.Graph())
	if strings.Contains(string(doc), "198.51.100.") {
		t.Error("connectors' public addresses are left out")
	}
	tailscaleKeyNowhere(t, h, cloudflareToken)

	// Every tunnel healthy: the app is ok.
	up.set("/accounts/"+cloudflareAccount+"/cfd_tunnel", "tunnels_ok.json")
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	if app, _ := h.Resource("cloudflaretunnel:i_test/app"); app.Health != integration.HealthOK || app.Attrs["health.reason"] != nil {
		t.Fatalf("all healthy: %+v", app)
	}
	checkSeries(t, h, "cloudflaretunnel", map[string]any{"tunnels": 1.0, "tunnels.down": 0.0}, nil)
}

func TestCloudflareTunnelFailures(t *testing.T) {
	integ := bundled(t, "cloudflaretunnel", "Cloudflare Tunnel", "token")
	up := cloudflaretunnel(t)
	url := up.start().URL + "/client/v4"
	// A wrong or revoked token: Cloudflare's 10000 "Authentication error".
	h := integrationtest.New(t)
	const bad = "fake-revoked-token-abcdefghijklmnopqrstuvwxyz0123"
	res := h.Connect(integ, map[string]any{"url": url, "account_id": cloudflareAccount}, map[string]string{"token": bad}).Test(t.Context())
	if res.OK || res.ErrorCode != integration.CodeAuth || strings.Contains(res.ErrorMessage, bad) {
		t.Fatalf("a bad token: %+v", res)
	}
	tailscaleKeyNowhere(t, h, bad)

	// An error inside a 200.
	up.set("/accounts/"+cloudflareAccount+"/cfd_tunnel", "error.json")
	h = integrationtest.New(t)
	res = h.Connect(integ, map[string]any{"url": url, "account_id": cloudflareAccount}, map[string]string{"token": cloudflareToken}).Test(t.Context())
	if res.OK || res.ErrorCode != integration.CodeAPIChanged || !strings.Contains(res.ErrorMessage, "Something went wrong on Cloudflare's side") {
		t.Fatalf("an error inside a 200: %+v", res)
	}
	tailscaleKeyNowhere(t, h, cloudflareToken)
}
