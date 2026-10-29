package specs

import (
	"net/http"
	"strings"
	"testing"

	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/integration/integrationtest"
)

const speedtestToken = "1|fakeSpeedtestTrackerToken0123456789"

func speedtest(t *testing.T) *app {
	return &app{t: t, dir: "testdata/speedtesttracker",
		auth:   func(r *http.Request) bool { return r.Header.Get("Authorization") == "Bearer "+speedtestToken },
		routes: map[string]string{"/api/v1/results": "results.json", "/api/v1/results/latest": "latest.json"}}
}

func TestSpeedtestTracker(t *testing.T) {
	integ := bundled(t, "speedtesttracker", "Speedtest Tracker", "token")
	importerShape(t, integ, "https://speedtest.home.arpa", "token")
	up := speedtest(t)
	srv := up.start()
	h := integrationtest.New(t)
	inst := h.Connect(integ, map[string]any{"url": srv.URL}, map[string]string{"token": speedtestToken})

	if res := inst.Test(t.Context()); !res.OK {
		t.Fatalf("test = %+v", res)
	}
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	attrs := checkApp(t, h, "speedtesttracker", srv.URL)
	r, _ := h.Resource("speedtesttracker:i_test/app")
	if r.Health != integration.HealthOK {
		t.Fatalf("health = %+v", r)
	}
	if attrs["results"] != 4812.0 || attrs["last_status"] != "completed" || attrs["last_scheduled"] != true || attrs["tested_at"] != "2026-10-03T13:00:41.000000Z" {
		t.Fatalf("attrs = %v", attrs)
	}
	checkSeries(t, h, "speedtesttracker", map[string]any{"dl.mbps": 941.2, "ul.mbps": 41.2, "ping.ms": 12.3, "jitter.ms": 1.9, "packet_loss.pct": 0.0},
		map[string]string{"dl.mbps": "Mb/s", "ul.mbps": "Mb/s", "ping.ms": "ms", "jitter.ms": "ms", "packet_loss.pct": "%"})
	for _, req := range up.requests() {
		if req.URL.Path == "/api/v1/results" && req.URL.Query().Get("page[size]") != "1" {
			t.Fatalf("list query = %s", req.URL.RawQuery)
		}
		if strings.Contains(req.URL.String(), speedtestToken) {
			t.Fatal("the token is in a URL")
		}
	}

	// Below the thresholds set in Speedtest Tracker: attention.
	up.set("/api/v1/results/latest", `{"data":{"id":4813,"status":"completed","healthy":false,"ping":80.1,"download_bits":120000000,"upload_bits":9000000,"data":{"ping":{"jitter":20},"packetLoss":2.5}}}`)
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	r, _ = h.Resource("speedtesttracker:i_test/app")
	if r.Health != integration.HealthAttention || r.Attrs["health.reason"] != "The last speed test was below your thresholds" {
		t.Fatalf("unhealthy = %+v", r)
	}
	checkSeries(t, h, "speedtesttracker", map[string]any{"dl.mbps": 120.0, "packet_loss.pct": 2.5}, map[string]string{"dl.mbps": "Mb/s", "packet_loss.pct": "%"})

	// A failed test: attention, and its numbers are unknown rather than 0.
	up.set("/api/v1/results/latest", `{"data":{"id":4814,"status":"failed","healthy":null,"ping":null,"download_bits":null,"upload_bits":null,"data":{"message":"Ookla could not reach a server"}}}`)
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	r, _ = h.Resource("speedtesttracker:i_test/app")
	if r.Health != integration.HealthAttention || r.Attrs["health.reason"] != "The last speed test failed" {
		t.Fatalf("failed = %+v", r)
	}
	checkSeries(t, h, "speedtesttracker", map[string]any{"dl.mbps": nil, "ping.ms": nil}, map[string]string{"dl.mbps": "Mb/s", "ping.ms": "ms"})

	// No test has run yet: latest is a 404, the instance still reads.
	up.set("/api/v1/results/latest", "")
	up.set("/api/v1/results", `{"data":[],"meta":{"total":0}}`)
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	r, _ = h.Resource("speedtesttracker:i_test/app")
	if r.Health != integration.HealthUnknown || r.Attrs["health.reason"] != "No speed test has finished yet" || r.Attrs["results"] != 0.0 {
		t.Fatalf("no tests yet = %+v", r)
	}
}

func TestSpeedtestTrackerFailures(t *testing.T) {
	integ := bundled(t, "speedtesttracker", "Speedtest Tracker", "token")
	srv := speedtest(t).start()
	// A wrong token (or one without results:read): an auth Repair.
	res := integrationtest.New(t).Connect(integ, map[string]any{"url": srv.URL}, map[string]string{"token": "wrong-token"}).Test(t.Context())
	if res.OK || res.ErrorCode != integration.CodeAuth || strings.Contains(res.ErrorMessage, "wrong-token") {
		t.Fatalf("wrong token: %+v", res)
	}
	// An old release without the v1 API answers nothing else either, so it
	// can't be told from a proxy with nothing behind it.
	res = integrationtest.New(t).Connect(integ, map[string]any{"url": srv.URL + "/old"}, map[string]string{"token": speedtestToken}).Test(t.Context())
	if res.OK || res.ErrorCode != integration.CodeUnreachable {
		t.Fatalf("no v1 API: %+v", res)
	}
}
