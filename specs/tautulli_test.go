package specs

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"

	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/integration/integrationtest"
)

const tautulliKey = "fake-tautulli-key"

// tautulli serves the fixtures by cmd under /tautulli/api/v2, the way the
// dogfood home reaches it behind a path prefix.
func tautulli(t *testing.T) *app {
	a := &app{t: t, dir: "testdata/tautulli", unauthorized: "unauthorized.json",
		auth: func(r *http.Request) bool { return r.URL.Query().Get("apikey") == tautulliKey },
		route: func(r *http.Request) string {
			if r.URL.Path != "/tautulli/api/v2" {
				return ""
			}
			return r.URL.Query().Get("cmd")
		},
		routes: map[string]string{
			"get_tautulli_info": "get_tautulli_info.json",
			"get_activity":      "get_activity.json",
			"server_status":     "server_status.json",
			"get_history":       "get_history.json",
		}}
	return a
}

func TestTautulli(t *testing.T) {
	integ := bundled(t, "tautulli", "Tautulli", "api_key")
	importerShape(t, integ, "https://jupiter.lab.example/tautulli", "api_key")
	up := tautulli(t)
	srv := up.start()
	url := srv.URL + "/tautulli"
	h := integrationtest.New(t)
	inst := h.Connect(integ, map[string]any{"url": url}, map[string]string{"api_key": tautulliKey})

	if res := inst.Test(t.Context()); !res.OK || res.Version != "2.15.3" {
		t.Fatalf("test = %+v", res)
	}
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	attrs := checkApp(t, h, "tautulli", url)
	if r, _ := h.Resource("tautulli:i_test/app"); r.Health != integration.HealthOK {
		t.Fatalf("health = %s", r.Health)
	}
	if attrs["version"] != "2.15.3" || attrs["branch"] != "master" || attrs["install_type"] != "docker" || attrs["plex_connected"] != true {
		t.Fatalf("attrs = %v", attrs)
	}
	// stream_count arrives as the string "2".
	checkSeries(t, h, "tautulli", map[string]any{"streams": 2.0, "streams.transcode": 1.0, "bandwidth.mbps": 18.45},
		map[string]string{"bandwidth.mbps": "Mb/s"})
	// What's playing, and the last plays, ride as attrs.
	np, _ := attrs["now_playing"].([]any)
	if len(np) != 2 {
		t.Fatalf("now_playing = %v", attrs["now_playing"])
	}
	if first, _ := np[0].(map[string]any); first["title"] != "Night of the Living Dead" || first["user"] != "Lab Member" || first["state"] != "playing" || first["decision"] != "direct play" || fmt.Sprint(first["progress"]) != "37" {
		t.Fatalf("now_playing[0] = %v", np[0])
	}
	if _, leaked := np[0].(map[string]any)["ip_address"]; leaked {
		t.Fatal("now_playing carries a player address")
	}
	hist, _ := attrs["history"].([]any)
	if len(hist) != 3 {
		t.Fatalf("history = %v", attrs["history"])
	}
	if h0, _ := hist[0].(map[string]any); h0["title"] != "Example Show - Pilot" || h0["user"] != "Lab Member" || h0["watched"] != true {
		t.Fatalf("history[0] = %v", hist[0])
	}
	// The key rides in the query string, never in the path.
	for _, r := range up.requests() {
		if r.URL.Path != "/tautulli/api/v2" || r.URL.Query().Get("apikey") != tautulliKey {
			t.Fatalf("request %s", r.URL)
		}
	}

	// Tautulli loses Plex: attention, with the reason.
	up.set("server_status", `{"response":{"result":"success","message":null,"data":{"result":"success","connected":false}}}`)
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	if r, _ := h.Resource("tautulli:i_test/app"); r.Health != integration.HealthAttention || r.Attrs["health.reason"] != "Tautulli has lost its connection to the Plex server" {
		t.Fatalf("plex gone = %+v", r)
	}
	// server_status is optional: without it, the rest still reads.
	up.set("server_status", "")
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	if r, _ := h.Resource("tautulli:i_test/app"); r.Health != integration.HealthOK {
		t.Fatalf("no server_status = %+v", r)
	}
	// Nothing playing is 0 streams, not unknown.
	up.set("get_activity", `{"response":{"result":"success","message":null,"data":{"stream_count":"0","stream_count_transcode":0,"total_bandwidth":0,"sessions":[]}}}`)
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	checkSeries(t, h, "tautulli", map[string]any{"streams": 0.0, "streams.transcode": 0.0, "bandwidth.mbps": 0.0}, map[string]string{"bandwidth.mbps": "Mb/s"})
}

func TestTautulliFailures(t *testing.T) {
	integ := bundled(t, "tautulli", "Tautulli", "api_key")
	up := tautulli(t)
	srv := up.start()
	url := srv.URL + "/tautulli"

	// A wrong key: 401, an auth Repair; the key in the query never shows.
	res := integrationtest.New(t).Connect(integ, map[string]any{"url": url}, map[string]string{"api_key": "wrong-key"}).Test(t.Context())
	if res.OK || res.ErrorCode != integration.CodeAuth || strings.Contains(res.ErrorMessage, "wrong-key") || strings.Contains(res.ErrorMessage, "apikey") {
		t.Fatalf("wrong key: %+v", res)
	}
	// Nor when the server can't be reached at all (url.Error prints URLs).
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := "http://" + ln.Addr().String()
	ln.Close()
	res = integrationtest.New(t).Connect(integ, map[string]any{"url": closed}, map[string]string{"api_key": tautulliKey}).Test(t.Context())
	if res.OK || res.ErrorCode != integration.CodeUnreachable || strings.Contains(res.ErrorMessage, tautulliKey) {
		t.Fatalf("unreachable: %+v", res)
	}

	h := integrationtest.New(t)
	inst := h.Connect(integ, map[string]any{"url": url}, map[string]string{"api_key": tautulliKey})
	// An error inside a 200.
	up.set("get_tautulli_info", `{"response":{"result":"error","message":"Invalid apikey","data":{}}}`)
	if code, msg := classify(t, inst); code != integration.CodeAuth || msg != "Tautulli rejected the API key." {
		t.Fatalf("apikey error in a 200: %s %q", code, msg)
	}
	up.set("get_tautulli_info", `{"response":{"result":"error","message":"Unknown command","data":{}}}`)
	if code, msg := classify(t, inst); code != integration.CodeAPIChanged || msg != "Tautulli answered get_tautulli_info with an error: Unknown command" {
		t.Fatalf("error in a 200: %s %q", code, msg)
	}
	// Too old.
	up.set("get_tautulli_info", `{"response":{"result":"success","message":null,"data":{"tautulli_version":"v1.4.25"}}}`)
	if code, msg := classify(t, inst); code != integration.CodeUnsupportedVersion || msg != "Tautulli v1.4.25 is older than this spec supports (2.0 or later)." {
		t.Fatalf("old: %s %q", code, msg)
	}
}
