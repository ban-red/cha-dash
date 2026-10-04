package jellyfin

import (
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"dash.cha.sh/integrations/internal/fixture"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/integration/integrationtest"
)

const key = "fake-jellyfin-key-0001"

// server is a fake Jellyfin under a /jellyfin base path, the way it's often
// proxied, answering 401 to a request without the key in the Authorization
// header.
func server(t *testing.T) *fixture.Server {
	t.Helper()
	auth := func(r *http.Request) bool { return r.Header.Get("Authorization") == `MediaBrowser Token="`+key+`"` }
	return fixture.New(t, fixture.Dir(t, ID), auth, map[string]fixture.Route{
		"GET /jellyfin/System/Info":  {File: "system_info.json"},
		"GET /jellyfin/Sessions":     {File: "sessions.json"},
		"GET /jellyfin/Items/Counts": {File: "items_counts.json"},
	})
}

func connect(t *testing.T, h *integrationtest.Harness, url, apiKey string) integration.Instance {
	t.Helper()
	return h.Connect(Integration{}, map[string]any{"url": url, "tls": map[string]any{"mode": "verify"}}, map[string]string{"api_key": apiKey})
}

func TestValidate(t *testing.T) {
	cfg := map[string]any{"url": "https://jellyfin.lab.example", "tls": map[string]any{"mode": "verify"}}
	if errs := integrationtest.Validate(Integration{}, cfg, map[string]string{"api_key": key}); len(errs) != 0 {
		t.Fatalf("valid config: %v", errs)
	}
	if errs := integrationtest.Validate(Integration{}, cfg, nil); len(errs) != 1 || errs[0].Path != "/api_key" {
		t.Fatalf("no key: %v", errs)
	}
	if errs := integrationtest.Validate(Integration{}, map[string]any{"url": "ftp://jellyfin.lab.example"}, map[string]string{"api_key": key}); len(errs) != 1 || errs[0].Path != "/url" {
		t.Fatalf("ftp: %v", errs)
	}
	_, m, ok := integration.Default.Get(ID)
	if !ok || !reflect.DeepEqual(m.SecretFields(), []string{"api_key"}) ||
		!reflect.DeepEqual(m.Capabilities, []integration.Capability{"media.sessions", "media.library"}) {
		t.Fatalf("manifest = %+v", m)
	}
}

func TestSync(t *testing.T) {
	srv := server(t)
	h := integrationtest.New(t)
	// The web app's address works too: /web is dropped.
	inst := connect(t, h, srv.URL+"/jellyfin/web/", key)
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	rid := "jellyfin:i_test/app"
	r, ok := h.Resource(rid)
	if !ok || r.Kind != "app" || r.Name != "Test" || r.State != "running" || r.Health != integration.HealthOK || r.Source != "integration:i_test" {
		t.Fatalf("app = %+v", r)
	}
	want := map[string]any{
		"type": "jellyfin", "url": srv.URL + "/jellyfin", "version": "10.11.2", "server_id": "0000000000004000800000000000f11e",
		"server_name": "media-server", "pending_restart": false,
		"movies": 1240.0, "shows": 318.0, "episodes": 14872.0, "albums": 806.0, "songs": 9310.0, "books": 54.0,
	}
	for k, v := range want {
		if r.Attrs[k] != v {
			t.Errorf("attrs[%s] = %v, want %v", k, r.Attrs[k], v)
		}
	}
	if caps, _ := r.Attrs["caps"].([]any); len(caps) != 2 || caps[0] != "media.sessions" || caps[1] != "media.library" {
		t.Errorf("caps = %v", r.Attrs["caps"])
	}
	if links, _ := r.Attrs["links"].([]any); len(links) != 1 || links[0].(map[string]any)["url"] != srv.URL+"/jellyfin/web/" {
		t.Errorf("links = %v", r.Attrs["links"])
	}
	if h.Graph().Relations["integration:i_test monitors "+rid] == nil {
		t.Fatal("the instance should monitor its app")
	}
	// Two sessions play (one paused, one transcoding); the third plays nothing.
	for metric, v := range map[string]float64{"streams": 2, "streams.transcode": 1} {
		if se := h.Series(rid, metric); se == nil || se["value"] != v || se["unit"] != "count" || se["max"] != 10.0 {
			t.Errorf("%s = %v", metric, se)
		}
	}
	if got := srv.Requests("/Sessions"); len(got) != 1 || got[0].URL.Query().Get("activeWithinSeconds") != "960" {
		t.Fatalf("sessions requests: %v", got)
	}
	for _, r := range srv.Requests("") {
		if r.URL.Query().Get("api_key") != "" || r.Header.Get("X-Emby-Token") != "" {
			t.Fatalf("the key went somewhere other than Authorization: %s", r.URL)
		}
	}

	// Nothing playing is 0, never unknown; the slow reads don't repeat
	// within half an hour.
	srv.Set("GET /jellyfin/Sessions", fixture.Route{Body: "[]"})
	h.Advance(10 * time.Minute)
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	if se := h.Series(rid, "streams"); se["value"] != 0.0 {
		t.Fatalf("idle streams = %v", se)
	}
	if n := len(srv.Requests("/Items/Counts")); n != 1 {
		t.Fatalf("library counts read %d times", n)
	}
	h.Advance(30 * time.Minute)
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	if n := len(srv.Requests("/Items/Counts")); n != 2 {
		t.Fatalf("library counts read %d times after 40 minutes", n)
	}
}

func TestTest(t *testing.T) {
	srv := server(t)
	h := integrationtest.New(t)
	res := connect(t, h, srv.URL+"/jellyfin", key).Test(t.Context())
	if !res.OK || res.Version != "10.11.2" || len(res.Grants) != 1 || res.Grants[0].Access != integration.AccessAdmin {
		t.Fatalf("test = %+v", res)
	}
	if _, ok := h.Resource("jellyfin:i_test/app"); ok {
		t.Fatal("Test must not emit facts")
	}
}

func TestFailures(t *testing.T) {
	srv := server(t)

	// A wrong key: an auth Repair on api_key, without the key in the message.
	err := connect(t, integrationtest.New(t), srv.URL+"/jellyfin", "wrong-key-0002").Sync(t.Context(), integrationtest.Watched)
	if code, _, msg := integration.Classify(err); code != integration.CodeAuth || integration.FieldOf(err) != "api_key" || strings.Contains(msg, "wrong-key") {
		t.Fatalf("wrong key: %s %q (%s)", code, msg, integration.FieldOf(err))
	}
	// Not Jellyfin at that URL.
	res := connect(t, integrationtest.New(t), srv.URL, key).Test(t.Context())
	if res.OK || res.ErrorCode != integration.CodeAPIChanged {
		t.Fatalf("wrong url = %+v", res)
	}
	// Emby answers the same paths.
	srv.Set("GET /jellyfin/System/Info", fixture.Route{Body: `{"ServerName":"emby","Version":"4.9.1.80","ProductName":"Emby Server","Id":"0000000000004000800000000000e3b1"}`})
	res = connect(t, integrationtest.New(t), srv.URL+"/jellyfin", key).Test(t.Context())
	if res.OK || res.ErrorCode != integration.CodeAPIChanged || res.ErrorMessage != "the URL answers as Emby Server, not Jellyfin" {
		t.Fatalf("emby = %+v", res)
	}
	// Too old.
	srv.Set("GET /jellyfin/System/Info", fixture.Route{Body: `{"ServerName":"old","Version":"10.7.7","ProductName":"Jellyfin Server","Id":"0000000000004000800000000000f11e"}`})
	res = connect(t, integrationtest.New(t), srv.URL+"/jellyfin", key).Test(t.Context())
	if res.OK || res.ErrorCode != integration.CodeUnsupportedVersion || res.ErrorMessage != "Jellyfin 10.7.7 is older than this integration supports (10.8 or later)" {
		t.Fatalf("old = %+v", res)
	}
	// An answer that isn't Jellyfin's JSON.
	srv.Set("GET /jellyfin/System/Info", fixture.Route{Body: "<html></html>", ContentType: "text/html"})
	res = connect(t, integrationtest.New(t), srv.URL+"/jellyfin", key).Test(t.Context())
	if res.OK || res.ErrorCode != integration.CodeAPIChanged {
		t.Fatalf("html = %+v", res)
	}
}

func TestOlder(t *testing.T) {
	for v, want := range map[string]bool{"10.7.7": true, "10.8.0": false, "10.11.2": false, "9.99": true, "11.0.0": false, "12": false, "unstable": false} {
		if got := older(v, MinVersion); got != want {
			t.Errorf("older(%q) = %v", v, got)
		}
	}
}
