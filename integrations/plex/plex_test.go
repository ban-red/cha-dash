package plex

import (
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"dash.cha.sh/integrations/internal/fixture"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/integration/integrationtest"
)

const token = "synthetic-plex-token-0001"

// server is a fake Plex Media Server: /identity answers anyone, the rest
// wants the token in X-Plex-Token.
func server(t *testing.T) *fixture.Server {
	auth := func(r *http.Request) bool { return r.URL.Path == "/identity" || r.Header.Get("X-Plex-Token") == token }
	return fixture.New(t, fixture.Dir(t, ID), auth, map[string]fixture.Route{
		"GET /identity":                    {File: "identity.json"},
		"GET /status/sessions":             {File: "status_sessions.json"},
		"GET /library/sections":            {File: "library_sections.json"},
		"GET /library/sections/1/all":      {File: "section_1_all.json"},
		"GET /library/sections/2/all":      {File: "section_2_all.json"},
		"GET /library/sections/2/all?type": {File: "section_2_episodes.json"},
		"GET /library/sections/3/all":      {File: "section_3_all.json"},
		"GET /library/sections/5/all":      {File: "section_5_all.json"},
		"GET /library/recentlyAdded":       {File: "recently_added.json"},
	})
}

func connect(t *testing.T, h *integrationtest.Harness, url, tok string) integration.Instance {
	return h.Connect(Integration{}, map[string]any{"url": url, "tls": map[string]any{"mode": "verify"}}, map[string]string{"token": tok})
}

func TestManifest(t *testing.T) {
	_, m, ok := integration.Default.Get(ID)
	if !ok || !reflect.DeepEqual(m.SecretFields(), []string{"token"}) || !reflect.DeepEqual(m.AuthorizeFields(), []string{"token"}) ||
		!reflect.DeepEqual(m.Capabilities, []integration.Capability{"media.sessions", "media.library"}) {
		t.Fatalf("manifest = %+v", m)
	}
	// The importer's shape.
	cfg := map[string]any{"url": "https://plex.lab.example", "tls": map[string]any{"mode": "verify"}}
	if errs := integrationtest.Validate(Integration{}, cfg, map[string]string{"token": token}); len(errs) != 0 {
		t.Fatalf("importer shape: %v", errs)
	}
	if errs := integrationtest.Validate(Integration{}, cfg, nil); len(errs) != 1 || errs[0].Path != "/token" {
		t.Fatalf("no token: %v", errs)
	}
}

func TestSync(t *testing.T) {
	pms := server(t)
	h := integrationtest.New(t)
	inst := connect(t, h, pms.URL+"/", token)
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	rid := "plex:i_test/app"
	r, ok := h.Resource(rid)
	if !ok || r.Kind != "app" || r.Name != "Test" || r.State != "running" || r.Health != integration.HealthOK ||
		r.Attrs["type"] != "plex" || r.Attrs["version"] != "1.41.6.9685-d301f511a" || r.Attrs["url"] != pms.URL ||
		r.Attrs["machine_identifier"] != "synthetic0machine0identifier0plex0000001" {
		t.Fatalf("app = %+v", r)
	}
	// The Demo Home's library attrs: movies across both movie sections,
	// shows and their episodes, music artists.
	if r.Attrs["movies"] != 1240.0 || r.Attrs["shows"] != 318.0 || r.Attrs["episodes"] != 14212.0 || r.Attrs["artists"] != 412.0 || r.Attrs["libraries"] != 4.0 {
		t.Fatalf("libraries = %v", r.Attrs)
	}
	if q := pms.Requests("/library/sections/2/all"); len(q) != 2 || q[1].URL.Query().Get("type") != "4" || q[1].URL.Query().Get("X-Plex-Container-Size") != "0" {
		t.Fatalf("show section reads = %d", len(q))
	}
	// What's playing, in Tautulli's shape: no addresses or player ids.
	np, _ := r.Attrs["now_playing"].([]any)
	if len(np) != 2 {
		t.Fatalf("now_playing = %v", r.Attrs["now_playing"])
	}
	ep, mv := np[0].(map[string]any), np[1].(map[string]any)
	if ep["title"] != "The Lighthouse Keepers - Low Tide" || ep["kind"] != "episode" || ep["episode"] != "S3 E4" || ep["state"] != "playing" ||
		ep["progress"] != 44.0 || ep["decision"] != "direct play" || ep["remote"] != false || ep["resolution"] != "1080p" ||
		ep["user"] != "household-owner" || ep["player"] != "Living room TV" {
		t.Errorf("episode = %v", ep)
	}
	if mv["title"] != "Sintel" || mv["state"] != "paused" || mv["progress"] != 5.0 || mv["decision"] != "transcode" || mv["remote"] != true || mv["resolution"] != "4K" {
		t.Errorf("movie = %v", mv)
	}
	for _, row := range np {
		for k, v := range row.(map[string]any) {
			if s, ok := v.(string); ok && (strings.Contains(s, "10.0.11.") || strings.Contains(s, "203.0.113.") || strings.Contains(s, "synthetic-")) {
				t.Errorf("now_playing %s = %q", k, s)
			}
		}
	}
	if r.Attrs["streams_remote"] != 1.0 {
		t.Errorf("streams_remote = %v", r.Attrs["streams_remote"])
	}
	// Recently added, newest first: a season stands for its episodes; photos
	// are left out.
	ra, _ := r.Attrs["recently_added"].([]any)
	var got []string
	for _, x := range ra {
		m := x.(map[string]any)
		got = append(got, fmt.Sprintf("%s|%v|%v|%v", m["kind"], m["title"], m["detail"], m["at"]))
	}
	want := []string{
		"movie|Tears of Steel|2012|1.759499e+09",
		"season|The Lighthouse Keepers|Season 3|1.75949e+09",
		"episode|Harbour Lights|S2 E7 · The Long Haul|1.75948e+09",
		"album|Open Water|The Synthetic Quartet|1.75946e+09",
		"movie|Sintel|2010|1.759e+09",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("recently_added =\n%v\nwant\n%v", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if q := pms.Requests("/library/recentlyAdded")[0].URL.Query(); q.Get("X-Plex-Container-Size") != "16" {
		t.Errorf("recently added query = %v", q)
	}
	if caps := r.Attrs["caps"].([]any); !reflect.DeepEqual(caps, []any{"media.sessions", "media.library"}) {
		t.Fatalf("caps = %v", caps)
	}
	if se := h.Series(rid, "streams"); se == nil || se["value"] != 2.0 || se["unit"] != "count" || se["max"] != 10.0 {
		t.Fatalf("streams = %v", se)
	}
	// Tautulli's keys, so the tile reads Plex alone the way it reads both.
	if se := h.Series(rid, "streams.transcode"); se == nil || se["value"] != 1.0 {
		t.Fatalf("streams.transcode = %v", se)
	}
	if se := h.Series(rid, "bandwidth.mbps"); se == nil || se["value"] != 16.0 || se["unit"] != "Mb/s" {
		t.Fatalf("bandwidth.mbps = %v", se)
	}
	if h.Graph().Relations["integration:i_test monitors "+rid] == nil {
		t.Fatal("the instance should monitor its app")
	}
	for _, req := range pms.Requests("") {
		if req.Header.Get("Accept") != "application/json" || strings.Contains(req.URL.RawQuery, token) {
			t.Fatalf("%s: headers %v", req.URL, req.Header)
		}
	}
	if q := pms.Requests("/library/sections/1/all")[0].URL.Query(); q.Get("X-Plex-Container-Size") != "0" {
		t.Fatalf("section query = %v", q)
	}

	// Later syncs read the sessions only; nothing playing is 0, not unknown.
	pms.Set("GET /status/sessions", fixture.Route{File: "status_sessions_idle.json"})
	pms.Reset()
	h.Advance(30 * time.Second)
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	if n := len(pms.Requests("")); n != 1 {
		t.Fatalf("%d requests on a fast sync", n)
	}
	if se := h.Series(rid, "streams"); se["value"] != 0.0 {
		t.Fatalf("idle streams = %v", se)
	}
	if r, _ := h.Resource(rid); len(r.Attrs["now_playing"].([]any)) != 0 || h.Series(rid, "bandwidth.mbps")["value"] != 0.0 {
		t.Fatalf("idle = %v", r.Attrs["now_playing"])
	}
	// Recently added again after 5 minutes, while watched.
	pms.Reset()
	h.Advance(5 * time.Minute)
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	if n := len(pms.Requests("")); n != 2 || len(pms.Requests("/library/recentlyAdded")) != 1 {
		t.Fatalf("%d requests after 5 minutes", n)
	}
	// After 30 minutes, while watched, the libraries again.
	h.Advance(30 * time.Minute)
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	if len(pms.Requests("/library/sections")) != 1 {
		t.Fatal("the libraries should be read again")
	}
}

func TestErrors(t *testing.T) {
	pms := server(t)
	h := integrationtest.New(t)
	err := connect(t, h, pms.URL, "signed-out-token").Sync(t.Context(), integrationtest.Watched)
	if code, _, msg := integration.Classify(err); code != integration.CodeAuth || integration.FieldOf(err) != "token" || !strings.Contains(msg, "Sign in with Plex again") {
		t.Fatalf("bad token: %s %q", code, msg)
	}
	res := connect(t, h, pms.URL, "signed-out-token").Test(t.Context())
	if res.OK || res.ErrorCode != integration.CodeAuth {
		t.Fatalf("test with a bad token = %+v", res)
	}
	pms.Set("GET /identity", fixture.Route{Body: "<html>not plex</html>", ContentType: "text/html"})
	err = connect(t, integrationtest.New(t), pms.URL, token).Sync(t.Context(), integrationtest.Watched)
	if code, _, _ := integration.Classify(err); code != integration.CodeAPIChanged || integration.FieldOf(err) != "url" {
		t.Fatalf("not plex: %v", err)
	}
}

func TestTest(t *testing.T) {
	pms := server(t)
	h := integrationtest.New(t)
	res := connect(t, h, pms.URL, token).Test(t.Context())
	if !res.OK || res.Version != "1.41.6.9685-d301f511a" || len(res.Grants) != 1 || res.Grants[0].Access != integration.AccessAdmin {
		t.Fatalf("test = %+v", res)
	}
	if _, ok := h.Resource("plex:i_test/app"); ok {
		t.Fatal("Test must not emit facts")
	}
}
