package specs

import (
	"net/http"
	"reflect"
	"testing"

	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/integration/integrationtest"
)

const borguiToken = "borgui_fakefakefakefakefakefakefake"

func borgui(t *testing.T, dir string) *app {
	return &app{t: t, dir: dir, base: "/borg",
		auth: func(r *http.Request) bool {
			// /api/system/info is public; the rest want the token.
			return r.URL.Path == "/borg/api/system/info" || r.Header.Get("X-Borg-Authorization") == "Bearer "+borguiToken
		},
		route: func(r *http.Request) string {
			if r.URL.Path == "/borg/api/backup-plans/runs" && r.URL.Query().Get("limit") != "50" {
				return "unlimited"
			}
			return r.URL.Path[len("/borg"):]
		},
		routes: map[string]string{
			"/api/system/info":          "info.json",
			"/api/backup-plans/":        "plans.json",
			"/api/backup-plans/runs":    "runs.json",
			"/api/ssh-keys/connections": "connections.json",
		}}
}

func TestBorgUI(t *testing.T) {
	integ := bundled(t, "borgui", "borg-ui", "token")
	importerShape(t, integ, "http://10.20.30.40:8081", "token")
	srv := borgui(t, "testdata/borgui").start()
	url := srv.URL + "/borg"
	h := integrationtest.New(t)
	at(h, backupFixturesNow)
	// path_map is left to its default, /local:/.
	inst := h.Connect(integ, map[string]any{"url": url, "host": "10.20.30.40"}, map[string]string{"token": borguiToken})
	if res := inst.Test(t.Context()); !res.OK || res.Version != "2.4.6" {
		t.Fatalf("test = %+v", res)
	}
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	attrs := checkApp(t, h, "borgui", url)
	app, _ := h.Resource("borgui:i_test/app")
	if app.Health != integration.HealthAttention || app.Attrs["health.reason"] != "Pi config failed, Media weekly is overdue" || attrs["plans"] != 4.0 || attrs["version"] != "2.4.6" {
		t.Fatalf("app = %+v", app)
	}
	checkSeries(t, h, "borgui", map[string]any{"backups": 4.0, "backups.failed": 1.0, "backups.stale": 1.0}, nil)

	const a = "borgui:i_test/app"
	checkTarget(t, h, a, "borgui:i_test/plan/1", "Appdata nightly", "completed", integration.HealthOK, "", map[string]any{
		"backup.covers":      covers("10.20.30.40", "/mnt/user/appdata"),
		"backup.schedule":    "0 2 * * *",
		"backup.last_status": "ok",
		"backup.last_at":     "2026-11-06T02:41:07Z",
		"backup.last_ok":     "2026-11-06T02:41:07Z",
		"backup.next_at":     "2026-11-07T02:00:00Z",
		"repositories":       []any{"nas-offsite", "local-usb"},
	})
	// A remote source: its SSH connection's host, its paths as written.
	checkTarget(t, h, a, "borgui:i_test/plan/2", "Pi config", "failed", integration.HealthDown,
		"The last run failed: Remote source unavailable: ssh: connect to host 10.20.30.51 port 22: Connection refused", map[string]any{
			"backup.covers":      covers("10.20.30.51", "/etc", "/home/pi"),
			"backup.last_status": "failed",
			"backup.last_at":     "2026-11-06T01:30:31Z",
			"backup.last_ok":     "2026-11-05T01:31:12Z",
			"backup.last_error":  "Remote source unavailable: ssh: connect to host 10.20.30.51 port 22: Connection refused",
		})
	// Weekly, a skipped run since, and the last good one twelve days ago.
	checkTarget(t, h, a, "borgui:i_test/plan/3", "Media weekly", "completed", integration.HealthAttention, "No good backup within its schedule", map[string]any{
		"backup.covers":   covers("10.20.30.40", "/mnt/user/media/music", "/mnt/user/media/books"),
		"backup.schedule": "0 4 * * 0",
		"backup.last_ok":  "2026-10-25T05:12:44Z",
	})
	// An agent's machine isn't named, so it covers nothing; no schedule.
	checkTarget(t, h, a, "borgui:i_test/plan/4", "Laptop", "never", integration.HealthUnknown, "No backup has run yet",
		map[string]any{"backup.covers": []any{}, "backup.enabled": false})
	if r, _ := h.Resource("borgui:i_test/plan/4"); r.Attrs["backup.schedule"] != nil || r.Attrs["backup.next_at"] != nil {
		t.Errorf("no schedule: %v", r.Attrs)
	}

	// Without the SSH connections (a user who may not list them), a remote
	// source covers nothing rather than a guess.
	up := borgui(t, "testdata/borgui")
	up.set("/api/ssh-keys/connections", "")
	h2 := integrationtest.New(t)
	at(h2, backupFixturesNow)
	if err := h2.Connect(integ, map[string]any{"url": up.start().URL + "/borg", "host": "10.20.30.40"}, map[string]string{"token": borguiToken}).Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	if r, _ := h2.Resource("borgui:i_test/plan/2"); !reflect.DeepEqual(r.Attrs["backup.covers"], []any{}) {
		t.Errorf("covers without connections = %#v", r.Attrs["backup.covers"])
	}
}

func TestBorgUIFailures(t *testing.T) {
	integ, _, _ := integration.Default.Get("borgui")
	srv := borgui(t, "testdata/borgui").start()
	res := integrationtest.New(t).Connect(integ, map[string]any{"url": srv.URL + "/borg"}, map[string]string{"token": "borgui_revoked"}).Test(t.Context())
	if res.OK || res.ErrorCode != integration.CodeAuth {
		t.Fatalf("revoked token: %+v", res)
	}
}

func TestBorgUISpecTest(t *testing.T) {
	specTest(t, "borgui")
}
