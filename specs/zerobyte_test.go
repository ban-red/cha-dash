package specs

import (
	"net/http"
	"testing"

	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/integration/integrationtest"
)

const zerobyteKey = "fake-zerobyte-api-key"

func zerobyte(t *testing.T, dir string) *app {
	return &app{t: t, dir: dir, base: "/zerobyte",
		auth:   func(r *http.Request) bool { return r.Header.Get("X-Api-Key") == zerobyteKey },
		routes: map[string]string{"/api/v1/backups": "backups.json"}}
}

func TestZerobyte(t *testing.T) {
	integ := bundled(t, "zerobyte", "Zerobyte", "api_key")
	importerShape(t, integ, "http://10.20.30.40:4096", "api_key")
	srv := zerobyte(t, "testdata/zerobyte").start()
	url := srv.URL + "/zerobyte"
	h := integrationtest.New(t)
	at(h, backupFixturesNow)
	inst := h.Connect(integ, map[string]any{"url": url, "host": "10.20.30.40", "path_map": "/data:/mnt/user"}, map[string]string{"api_key": zerobyteKey})
	if res := inst.Test(t.Context()); !res.OK {
		t.Fatalf("test = %+v", res)
	}
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	attrs := checkApp(t, h, "zerobyte", url)
	app, _ := h.Resource("zerobyte:i_test/app")
	if app.Health != integration.HealthAttention || app.Attrs["health.reason"] != "NAS photos failed, Documents is overdue" || attrs["schedules"] != 4.0 {
		t.Fatalf("app = %+v", app)
	}
	checkSeries(t, h, "zerobyte", map[string]any{"backups": 4.0, "backups.failed": 1.0, "backups.stale": 1.0}, nil)

	const a = "zerobyte:i_test/app"
	// A directory volume under the path map, narrowed by include paths.
	checkTarget(t, h, a, "zerobyte:i_test/schedule/a1b2c3", "Appdata", "success", integration.HealthOK, "", map[string]any{
		"backup.covers":      covers("10.20.30.40", "/mnt/user/appdata/nextcloud", "/mnt/user/appdata/vaultwarden"),
		"backup.schedule":    "0 3 * * *",
		"backup.last_status": "ok",
		"backup.last_at":     "2026-11-06T03:06:40Z",
		"backup.last_ok":     "2026-11-06T03:06:40Z",
		"backup.next_at":     "2026-11-07T03:00:00Z",
		"volume":             "appdata",
		"volume_type":        "directory",
		"repository":         "b2-offsite",
	})
	// An NFS export: the NAS's own folder, on the NAS.
	checkTarget(t, h, a, "zerobyte:i_test/schedule/d4e5f6", "NAS photos", "failed", integration.HealthDown,
		"The last backup failed: Fatal: unable to create lock in backend: repository is already locked exclusively by PID 2211 on zerobyte by root", map[string]any{
			"backup.covers":      covers("10.20.30.12", "/volume1/photos"),
			"backup.last_status": "failed",
			"backup.last_at":     "2026-11-06T02:31:05Z",
			"backup.last_error":  "Fatal: unable to create lock in backend: repository is already locked exclusively by PID 2211 on zerobyte by root",
		})
	if r, _ := h.Resource("zerobyte:i_test/schedule/d4e5f6"); r.Attrs["backup.last_ok"] != nil {
		t.Errorf("a failed newest run hides the last good one: %v", r.Attrs["backup.last_ok"])
	}
	// A folder on an agent's machine; a warning, and days old on a six-hourly
	// schedule.
	checkTarget(t, h, a, "zerobyte:i_test/schedule/g7h8i9", "Documents", "warning", integration.HealthAttention, "No good backup within its schedule", map[string]any{
		"backup.covers":      covers("workstation", "/srv/documents"),
		"backup.schedule":    "0 */6 * * *",
		"backup.last_status": "ok",
		"volume_type":        "filesystem",
	})
	// SMB names no host folder; turned off, never run.
	checkTarget(t, h, a, "zerobyte:i_test/schedule/j1k2l3", "Office share", "disabled", integration.HealthOK, "Turned off, and it hasn't run", map[string]any{
		"backup.covers":  []any{},
		"backup.enabled": false,
	})
	if r, _ := h.Resource("zerobyte:i_test/schedule/j1k2l3"); r.Attrs["backup.schedule"] != nil || r.Attrs["backup.last_at"] != nil {
		t.Errorf("off: %v", r.Attrs)
	}
}

func TestZerobyteFailures(t *testing.T) {
	integ, _, _ := integration.Default.Get("zerobyte")
	srv := zerobyte(t, "testdata/zerobyte").start()
	res := integrationtest.New(t).Connect(integ, map[string]any{"url": srv.URL + "/zerobyte"}, map[string]string{"api_key": "revoked"}).Test(t.Context())
	if res.OK || res.ErrorCode != integration.CodeAuth {
		t.Fatalf("revoked key: %+v", res)
	}
}

func TestZerobyteSpecTest(t *testing.T) {
	specTest(t, "zerobyte")
}
