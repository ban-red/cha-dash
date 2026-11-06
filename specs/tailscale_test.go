package specs

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/integration/integrationtest"
)

// A Tailscale API key's shape; this one is made up.
const tailscaleKey = "tskey-api-kFAKE1CNTRL-0123456789abcdefFAKEFAKEFAKEFAKE"

// tailscaleDevices is devices.json with its dates moved to around now, since
// the spec compares them with the clock: each device's lastSeen and expires
// become now plus the offset given (zero leaves the fixture's value).
func tailscaleDevices(t *testing.T, at map[string][2]time.Duration) string {
	t.Helper()
	b, err := os.ReadFile("testdata/tailscale/devices.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Devices []map[string]any `json:"devices"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, d := range doc.Devices {
		o, ok := at[d["hostname"].(string)]
		if !ok {
			continue
		}
		if o[0] != 0 {
			d["lastSeen"] = now.Add(o[0]).Format(time.RFC3339)
		}
		if o[1] != 0 {
			d["expires"] = now.Add(o[1]).Format(time.RFC3339)
		}
	}
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

const tsDay = 24 * time.Hour

func tailscale(t *testing.T) *app {
	devices := tailscaleDevices(t, map[string][2]time.Duration{
		"heron":   {0, 5*tsDay + time.Hour},   // connected, its key expires in 5 days
		"ibis":    {-69 * tsDay, -70 * tsDay}, // its key expired 70 days ago
		"kite":    {-time.Hour, 160 * tsDay},  // a phone, asleep for an hour
		"lark":    {-37*tsDay - time.Hour, 0}, // gone for 37 days, no key expiry
		"myna":    {0, 180 * tsDay},           // waiting for approval
		"printer": {0, -30 * tsDay},           // shared in: not this tailnet's key
	})
	return &app{t: t, dir: "testdata/tailscale", base: "/ts",
		auth:         func(r *http.Request) bool { return r.Header.Get("Authorization") == "Bearer "+tailscaleKey },
		unauthorized: "unauthorized.json",
		routes:       map[string]string{"/api/v2/tailnet/-/devices": devices}}
}

// tailscaleKeyNowhere checks a secret is in no resource, series, relation
// or log line.
func tailscaleKeyNowhere(t *testing.T, h *integrationtest.Harness, secret string) {
	t.Helper()
	doc, err := json.Marshal(h.Graph())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(doc), secret) || strings.Contains(h.Logs(), secret) {
		t.Fatal("the secret reached the graph or the log")
	}
}

func TestTailscale(t *testing.T) {
	integ := bundled(t, "tailscale", "Tailscale", "api_key")
	importerShape(t, integ, "https://api.tailscale.com", "api_key")
	up := tailscale(t)
	url := up.start().URL + "/ts"
	h := integrationtest.New(t)
	inst := h.Connect(integ, map[string]any{"url": url}, map[string]string{"api_key": tailscaleKey})
	if res := inst.Test(t.Context()); !res.OK || res.Version != "" {
		t.Fatalf("test = %+v", res)
	}
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	app, ok := h.Resource("tailscale:i_test/app")
	if !ok || app.Kind != "app" || app.State != "running" || app.Attrs["type"] != "tailscale" ||
		app.Health != integration.HealthAttention || app.Attrs["health.reason"] != "1 key has expired; 1 key expires within 14 days" ||
		app.Attrs["devices"] != 6.0 || app.Attrs["shared_in"] != 1.0 {
		t.Fatalf("app = %+v", app)
	}
	if links, _ := app.Attrs["links"].([]any); len(links) != 1 || links[0].(map[string]any)["url"] != url {
		t.Fatalf("links = %v", app.Attrs["links"])
	}
	checkSeries(t, h, "tailscale", map[string]any{"devices": 6.0, "devices.online": 3.0, "keys.expiring": 2.0}, nil)

	for id, want := range map[string]struct {
		name, state, health, reason string
		attrs                       map[string]any
	}{
		"nEGRET1CNTRL": {"egret", "online", integration.HealthOK, "", map[string]any{"hostname": "egret.tail1a2b3c.ts.net", "tailscale_ip": "100.101.12.4",
			"os": "linux", "key_expiry_disabled": true, "update_available": false, "authorized": true}},
		"nHERON2CNTRL": {"heron", "online", integration.HealthAttention, "Its key expires in 5 days", map[string]any{"update_available": true}},
		"nIBIS3CNTRL": {"ibis", "offline", integration.HealthDown,
			"Its key expired on " + time.Now().UTC().Add(-70*tsDay).Format("2006-01-02") + ", so it's off the tailnet until it signs in again", map[string]any{"os": "windows"}},
		"nKITE4CNTRL": {"kite", "offline", integration.HealthOK, "", map[string]any{"os": "iOS"}},
		"nLARK5CNTRL": {"lark", "offline", integration.HealthAttention, "Not seen for 37 days", map[string]any{}},
		"nMYNA6CNTRL": {"myna", "online", integration.HealthAttention, "It's waiting for approval in the admin console", map[string]any{"authorized": false}},
	} {
		rid := "tailscale:i_test/device/" + id
		r, ok := h.Resource(rid)
		if !ok || r.Kind != "host" || r.Name != want.name || r.State != want.state || string(r.Health) != want.health {
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
		if h.Graph().Relations[rid+" part_of tailscale:i_test/app"] == nil {
			t.Errorf("%s: not part of the app", id)
		}
	}
	egret, _ := h.Resource("tailscale:i_test/device/nEGRET1CNTRL")
	if egret.Attrs["key_expires"] != nil || egret.Attrs["last_seen"] != nil {
		t.Errorf("egret: no expiry and connected, so no dates: %v", egret.Attrs)
	}
	if tags, _ := egret.Attrs["tags"].([]any); len(tags) != 1 || tags[0] != "tag:server" {
		t.Errorf("egret tags = %v", egret.Attrs["tags"])
	}
	if _, ok := h.Resource("tailscale:i_test/device/nSHARED7CNTRL"); ok {
		t.Error("a device shared in from another tailnet isn't this tailnet's")
	}
	tailscaleKeyNowhere(t, h, tailscaleKey)
	for _, r := range up.requests() {
		if r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer "+tailscaleKey {
			t.Errorf("request %s: the key goes in the header only", r.URL)
		}
	}
}

// The thresholds are the owner's: a 3-day warning leaves heron's 5 days
// alone, and 60 days offline leaves lark's 37.
func TestTailscaleThresholds(t *testing.T) {
	integ := bundled(t, "tailscale", "Tailscale", "api_key")
	h := integrationtest.New(t)
	inst := h.Connect(integ, map[string]any{"url": tailscale(t).start().URL + "/ts", "expiry_warn_days": 3, "stale_days": 60},
		map[string]string{"api_key": tailscaleKey})
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"nHERON2CNTRL", "nLARK5CNTRL"} {
		if r, _ := h.Resource("tailscale:i_test/device/" + id); r.Health != integration.HealthOK {
			t.Errorf("%s = %+v", id, r)
		}
	}
	if app, _ := h.Resource("tailscale:i_test/app"); app.Attrs["health.reason"] != "1 key has expired" {
		t.Errorf("app = %+v", app)
	}
	checkSeries(t, h, "tailscale", map[string]any{"keys.expiring": 1.0}, nil)
}

// Every key fine and every device connected: everything is ok.
func TestTailscaleAllWell(t *testing.T) {
	integ := bundled(t, "tailscale", "Tailscale", "api_key")
	up := tailscale(t)
	up.routes = map[string]string{"/api/v2/tailnet/-/devices": "devices_ok.json"}
	h := integrationtest.New(t)
	inst := h.Connect(integ, map[string]any{"url": up.start().URL + "/ts"}, map[string]string{"api_key": tailscaleKey})
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	if app, _ := h.Resource("tailscale:i_test/app"); app.Health != integration.HealthOK || app.Attrs["health.reason"] != nil {
		t.Fatalf("app = %+v", app)
	}
	if r, _ := h.Resource("tailscale:i_test/device/nEGRET1CNTRL"); r.Health != integration.HealthOK || r.State != "online" {
		t.Fatalf("egret = %+v", r)
	}
	checkSeries(t, h, "tailscale", map[string]any{"devices": 1.0, "devices.online": 1.0, "keys.expiring": 0.0}, nil)
}

func TestTailscaleFailures(t *testing.T) {
	integ := bundled(t, "tailscale", "Tailscale", "api_key")
	srv := tailscale(t).start()
	// An expired or revoked API key: Tailscale answers 401.
	h := integrationtest.New(t)
	const old = "tskey-api-kOLD9CNTRL-revokedrevokedrevokedrevoked0000"
	res := h.Connect(integ, map[string]any{"url": srv.URL + "/ts"}, map[string]string{"api_key": old}).Test(t.Context())
	if res.OK || res.ErrorCode != integration.CodeAuth || strings.Contains(res.ErrorMessage, old) {
		t.Fatalf("a revoked key: %+v", res)
	}
	tailscaleKeyNowhere(t, h, old)
	res = integrationtest.New(t).Connect(integ, map[string]any{"url": srv.URL + "/elsewhere"}, map[string]string{"api_key": tailscaleKey}).Test(t.Context())
	if res.OK || res.ErrorCode != integration.CodeUnreachable {
		t.Fatalf("wrong url: %+v", res)
	}
}
