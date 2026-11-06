package specs

import (
	"net/http"
	"testing"

	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/integration/integrationtest"
)

const duplicatiToken = "fake-duplicati-forever-token"

func duplicati(t *testing.T, dir string) *app {
	return &app{t: t, dir: dir, base: "/duplicati",
		auth: func(r *http.Request) bool { return r.Header.Get("Authorization") == "Bearer "+duplicatiToken },
		routes: map[string]string{
			"/api/v1/systeminfo": "systeminfo.json",
			"/api/v1/backups":    "backups.json",
		}}
}

func TestDuplicati(t *testing.T) {
	integ := bundled(t, "duplicati", "Duplicati", "token")
	importerShape(t, integ, "http://10.20.30.40:8200", "token")
	srv := duplicati(t, "testdata/duplicati").start()
	url := srv.URL + "/duplicati"
	h := integrationtest.New(t)
	at(h, backupFixturesNow)
	inst := h.Connect(integ, map[string]any{"url": url, "host": "10.20.30.40", "path_map": "/source:/"}, map[string]string{"token": duplicatiToken})
	if res := inst.Test(t.Context()); !res.OK || res.Version != "2.4.1.0" {
		t.Fatalf("test = %+v", res)
	}
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	attrs := checkApp(t, h, "duplicati", url)
	app, _ := h.Resource("duplicati:i_test/app")
	if app.Health != integration.HealthAttention || app.Attrs["health.reason"] != "Photos failed, Documents is overdue" || attrs["jobs"] != 4.0 || attrs["version"] != "2.4.1.0" {
		t.Fatalf("app = %+v", app)
	}
	checkSeries(t, h, "duplicati", map[string]any{"backups": 4.0, "backups.failed": 1.0, "backups.stale": 1.0}, nil)

	const a = "duplicati:i_test/app"
	checkTarget(t, h, a, "duplicati:i_test/job/1", "Appdata", "ok", integration.HealthOK, "", map[string]any{
		"backup.covers":      covers("10.20.30.40", "/mnt/user/appdata"),
		"backup.schedule":    "daily",
		"backup.last_status": "ok",
		"backup.last_at":     "2026-11-06T03:04:37Z",
		"backup.last_ok":     "2026-11-06T03:04:37Z",
		"backup.next_at":     "2026-11-07T03:00:00Z",
		"size_bytes":         48318382080.0,
		"stored_bytes":       39728447488.0,
		"versions":           34.0,
	})
	// The last error is newer than the last good backup: failed, down.
	checkTarget(t, h, a, "duplicati:i_test/job/2", "Photos", "failed", integration.HealthDown,
		"The last run failed: The remote server returned an error: (403) Forbidden.", map[string]any{
			"backup.covers":      covers("10.20.30.40", "/mnt/user/photos", "/boot"),
			"backup.schedule":    "weekly",
			"backup.last_status": "failed",
			"backup.last_at":     "2026-11-06T02:01:12Z",
			"backup.last_ok":     "2026-10-30T03:28:11Z",
			"backup.last_error":  "The remote server returned an error: (403) Forbidden.",
		})
	// An old error, a good run since, but nothing for days on a daily job;
	// %MY_DOCUMENTS% covers nothing.
	checkTarget(t, h, a, "duplicati:i_test/job/3", "Documents", "ok", integration.HealthAttention, "No backup within its schedule", map[string]any{
		"backup.covers":      covers("10.20.30.40", "/mnt/user/documents"),
		"backup.last_status": "ok",
	})
	if r, _ := h.Resource("duplicati:i_test/job/3"); r.Attrs["backup.last_error"] != nil {
		t.Errorf("an error older than the last good run: %v", r.Attrs["backup.last_error"])
	}
	// No schedule, never run.
	checkTarget(t, h, a, "duplicati:i_test/job/4", "Scratch", "never", integration.HealthUnknown, "No backup has run yet; it runs only by hand",
		map[string]any{"backup.covers": covers("10.20.30.40", "/mnt/user/scratch")})
	if r, _ := h.Resource("duplicati:i_test/job/4"); r.Attrs["backup.schedule"] != nil || r.Attrs["backup.last_status"] != nil || r.Attrs["size_bytes"] != nil {
		t.Errorf("never run: %v", r.Attrs)
	}
}

func TestDuplicatiFailures(t *testing.T) {
	integ, _, _ := integration.Default.Get("duplicati")
	srv := duplicati(t, "testdata/duplicati").start()
	res := integrationtest.New(t).Connect(integ, map[string]any{"url": srv.URL + "/duplicati"}, map[string]string{"token": "expired"}).Test(t.Context())
	if res.OK || res.ErrorCode != integration.CodeAuth {
		t.Fatalf("wrong token: %+v", res)
	}
	// Without systeminfo the version is unknown, and the jobs still read.
	up := duplicati(t, "testdata/duplicati")
	up.set("/api/v1/systeminfo", "")
	h := integrationtest.New(t)
	at(h, backupFixturesNow)
	inst := h.Connect(integ, map[string]any{"url": up.start().URL + "/duplicati"}, map[string]string{"token": duplicatiToken})
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	if r, ok := h.Resource("duplicati:i_test/job/1"); !ok || r.Attrs["backup.covers"] == nil {
		t.Fatalf("job 1 = %+v", r)
	}
}

func TestDuplicatiSpecTest(t *testing.T) {
	specTest(t, "duplicati")
}
