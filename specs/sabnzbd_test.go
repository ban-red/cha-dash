package specs

import (
	"net/http"
	"strings"
	"testing"

	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/integration/integrationtest"
)

const sabnzbdKey = "fake-sabnzbd-key"

// sabnzbd serves the fixtures by mode under /sabnzbd/api, SABnzbd's default
// URL base. mode=version needs no key; everything else wants apikey.
func sabnzbd(t *testing.T) *app {
	return &app{t: t, dir: "testdata/sabnzbd",
		auth: func(r *http.Request) bool {
			return r.URL.Query().Get("mode") == "version" || r.URL.Query().Get("apikey") == sabnzbdKey
		},
		route: func(r *http.Request) string {
			if r.URL.Path != "/sabnzbd/api" || r.URL.Query().Get("output") != "json" {
				return ""
			}
			return r.URL.Query().Get("mode")
		},
		routes: map[string]string{"version": "version.json", "queue": "queue.json"}}
}

func TestSABnzbd(t *testing.T) {
	integ := bundled(t, "sabnzbd", "SABnzbd", "api_key")
	importerShape(t, integ, "https://downloads.lab.example/sabnzbd", "api_key")
	up := sabnzbd(t)
	srv := up.start()
	url := srv.URL + "/sabnzbd"
	h := integrationtest.New(t)
	inst := h.Connect(integ, map[string]any{"url": url}, map[string]string{"api_key": sabnzbdKey})

	if res := inst.Test(t.Context()); !res.OK || res.Version != "4.5.3" {
		t.Fatalf("test = %+v", res)
	}
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	attrs := checkApp(t, h, "sabnzbd", url)
	r, _ := h.Resource("sabnzbd:i_test/app")
	if r.Health != integration.HealthOK || r.State != "running" {
		t.Fatalf("app = %+v", r)
	}
	if attrs["version"] != "4.5.3" || attrs["status"] != "Downloading" || attrs["paused"] != false || attrs["timeleft"] != "0:49:28" ||
		attrs["speedlimit_pct"] != 100.0 || attrs["disk_free_gib"] != 1288.42 {
		t.Fatalf("attrs = %v", attrs)
	}
	if caps, _ := attrs["caps"].([]any); len(caps) != 1 || caps[0] != "downloads.queue" {
		t.Fatalf("caps = %v", attrs["caps"])
	}
	// 2295.41 KiB/s is 18.8 Mb/s; 6655.28 MiB is 6.5 GiB.
	checkSeries(t, h, "sabnzbd", map[string]any{"dl.mbps": 18.8, "queue": 3.0, "left.gib": 6.5},
		map[string]string{"dl.mbps": "Mb/s", "left.gib": "GiB"})
	// The key rides in the query string, only where it's needed.
	for _, r := range up.requests() {
		q := r.URL.Query()
		if r.URL.Path != "/sabnzbd/api" || (q.Get("mode") == "version") != (q.Get("apikey") == "") {
			t.Fatalf("request %s", r.URL.Path)
		}
		if q.Get("mode") == "queue" && q.Get("limit") != "1" {
			t.Fatal("the queue should be read without its jobs")
		}
	}

	// Paused: state paused, still ok; 0 Mb/s, not unknown; no speed limit.
	up.set("queue", "queue_paused.json")
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	r, _ = h.Resource("sabnzbd:i_test/app")
	if r.State != "paused" || r.Health != integration.HealthOK || r.Attrs["paused"] != true || r.Attrs["status"] != "Paused" {
		t.Fatalf("paused = %+v", r)
	}
	if _, ok := r.Attrs["speedlimit_pct"]; ok {
		t.Fatal("no speed limit, no speedlimit_pct")
	}
	checkSeries(t, h, "sabnzbd", map[string]any{"dl.mbps": 0.0, "queue": 3.0}, map[string]string{"dl.mbps": "Mb/s"})

	// mode=version is optional: the queue's own version stands in.
	up.set("version", "")
	if res := inst.Test(t.Context()); !res.OK || res.Version != "4.5.3" {
		t.Fatalf("no mode=version: %+v", res)
	}
}

func TestSABnzbdFailures(t *testing.T) {
	integ := bundled(t, "sabnzbd", "SABnzbd", "api_key")
	up := sabnzbd(t)
	srv := up.start()
	url := srv.URL + "/sabnzbd"

	// A wrong key refused with a status: an auth Repair, the key never shown.
	res := integrationtest.New(t).Connect(integ, map[string]any{"url": url}, map[string]string{"api_key": "wrong-key"}).Test(t.Context())
	if res.OK || res.ErrorCode != integration.CodeAuth || strings.Contains(res.ErrorMessage, "wrong-key") || strings.Contains(res.ErrorMessage, "apikey") {
		t.Fatalf("wrong key: %+v", res)
	}

	h := integrationtest.New(t)
	inst := h.Connect(integ, map[string]any{"url": url}, map[string]string{"api_key": sabnzbdKey})
	// Older releases report it inside a 200.
	up.set("queue", "error_apikey.json")
	if code, msg := classify(t, inst); code != integration.CodeAuth || !strings.HasPrefix(msg, "SABnzbd rejected the API key") {
		t.Fatalf("key error in a 200: %s %q", code, msg)
	}
	up.set("queue", `{"status": false, "error": "not implemented"}`)
	if code, msg := classify(t, inst); code != integration.CodeAPIChanged || msg != "SABnzbd answered mode=queue with an error: not implemented" {
		t.Fatalf("error in a 200: %s %q", code, msg)
	}
	// Too old.
	up.set("queue", "queue.json")
	up.set("version", `{"version": "2.3.9"}`)
	if code, msg := classify(t, inst); code != integration.CodeUnsupportedVersion || msg != "SABnzbd 2.3.9 is older than this spec supports (3.0 or later)." {
		t.Fatalf("old: %s %q", code, msg)
	}
	// Not SABnzbd at that URL: the queue is a 404 and nothing else answered.
	res = integrationtest.New(t).Connect(integ, map[string]any{"url": srv.URL}, map[string]string{"api_key": sabnzbdKey}).Test(t.Context())
	if res.OK || res.ErrorCode != integration.CodeUnreachable {
		t.Fatalf("wrong url: %+v", res)
	}
}
