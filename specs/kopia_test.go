package specs

import (
	"net/http"
	"reflect"
	"testing"

	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/integration/integrationtest"
)

const kopiaAuth = "Basic c2VydmVyLWNvbnRyb2w6ZmFrZS1rb3BpYS1wYXNzd29yZA==" // server-control:fake-kopia-password

func kopia(t *testing.T, dir string) *app {
	return &app{t: t, dir: dir, base: "/kopia",
		auth: func(r *http.Request) bool {
			// The control API takes no CSRF token; the UI API would want one.
			return r.Header.Get("Authorization") == kopiaAuth && r.Header.Get("X-Kopia-Csrf-Token") == ""
		},
		routes: map[string]string{"/api/v1/control/sources": "sources.json"}}
}

func TestKopia(t *testing.T) {
	integ := bundled(t, "kopia", "Kopia", "basic_auth")
	importerShape(t, integ, "http://10.20.30.40:51515", "basic_auth")
	srv := kopia(t, shiftedFixtures(t, "testdata/kopia")).start()
	url := srv.URL + "/kopia"
	h := integrationtest.New(t)
	inst := h.Connect(integ, map[string]any{"url": url, "path_map": "/data:/mnt/user"}, map[string]string{"basic_auth": kopiaAuth[len("Basic "):]})
	if res := inst.Test(t.Context()); !res.OK {
		t.Fatalf("test = %+v", res)
	}
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	attrs := checkApp(t, h, "kopia", url)
	app, _ := h.Resource("kopia:i_test/app")
	if app.Health != integration.HealthAttention || app.Attrs["health.reason"] != "root@nas:/data/photos failed, root@nas:/data/documents is overdue" ||
		attrs["sources"] != 4.0 || attrs["host"] != "nas" {
		t.Fatalf("app = %+v", app)
	}
	checkSeries(t, h, "kopia", map[string]any{"backups": 4.0, "backups.failed": 1.0, "backups.stale": 1.0}, nil)

	const a = "kopia:i_test/app"
	// A good daily snapshot; the source's own host name is the host.
	checkTarget(t, h, a, "kopia:i_test/source/root_nas_data_appdata", "root@nas:/data/appdata", "idle", integration.HealthOK, "", map[string]any{
		"backup.covers":      covers("nas", "/mnt/user/appdata"),
		"backup.schedule":    "daily",
		"backup.last_status": "ok",
		"backup.last_at":     shifted("2026-11-06T03:04:41Z"),
		"backup.next_at":     shifted("2026-11-07T03:00:00Z"),
		"size_bytes":         48318382080.0,
		"files":              182311.0,
		"backup.manual":      false,
	})
	// Files that failed to read: the snapshot counts as failed.
	checkTarget(t, h, a, "kopia:i_test/source/root_nas_data_photos", "root@nas:/data/photos", "idle", integration.HealthDown,
		"The last snapshot had errors: open /data/photos/2026/10/IMG_0412.HEIC: permission denied", map[string]any{
			"backup.covers":      covers("nas", "/mnt/user/photos"),
			"backup.schedule":    "daily",
			"backup.last_status": "failed",
			"backup.last_error":  "open /data/photos/2026/10/IMG_0412.HEIC: permission denied",
		})
	// Every six hours, but the newest snapshot is from days ago.
	checkTarget(t, h, a, "kopia:i_test/source/root_nas_data_documents", "root@nas:/data/documents", "idle", integration.HealthAttention,
		"No snapshot within its schedule", map[string]any{"backup.schedule": "0 */6 * * *", "backup.last_status": "ok"})
	// Another client's source, by hand only, never snapshotted.
	checkTarget(t, h, a, "kopia:i_test/source/backup_workstation_srv_projects", "backup@workstation:/srv/projects", "remote", integration.HealthUnknown,
		"No snapshot yet; it runs only by hand", map[string]any{"backup.covers": covers("workstation", "/srv/projects"), "backup.manual": true})
	for _, id := range []string{"root_nas_data_photos", "backup_workstation_srv_projects"} {
		if r, _ := h.Resource("kopia:i_test/source/" + id); r.Attrs["backup.last_ok"] != nil {
			t.Errorf("%s: last_ok %v", id, r.Attrs["backup.last_ok"])
		}
	}
	if r, _ := h.Resource("kopia:i_test/source/backup_workstation_srv_projects"); r.Attrs["backup.schedule"] != nil {
		t.Errorf("manual: schedule %v", r.Attrs["backup.schedule"])
	}

	// A host set in the config names every source's machine.
	h2 := integrationtest.New(t)
	inst2 := h2.Connect(integ, map[string]any{"url": url, "host": "10.20.30.40"}, map[string]string{"basic_auth": kopiaAuth[len("Basic "):]})
	if err := inst2.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	if r, _ := h2.Resource("kopia:i_test/source/root_nas_data_appdata"); !reflect.DeepEqual(r.Attrs["backup.covers"], covers("10.20.30.40", "/data/appdata")) {
		t.Errorf("covers = %#v", r.Attrs["backup.covers"])
	}
}

func TestKopiaFailures(t *testing.T) {
	integ, _, _ := integration.Default.Get("kopia")
	srv := kopia(t, "testdata/kopia").start()
	res := integrationtest.New(t).Connect(integ, map[string]any{"url": srv.URL + "/kopia"}, map[string]string{"basic_auth": "d3Jvbmc6d3Jvbmc="}).Test(t.Context())
	if res.OK || res.ErrorCode != integration.CodeAuth {
		t.Fatalf("wrong password: %+v", res)
	}
}

func TestKopiaSpecTest(t *testing.T) {
	specTest(t, "kopia")
}
