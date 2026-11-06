package specs

import (
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/integration/integrationtest"
	"dash.cha.sh/internal/spec/spectest"
)

// backupFixturesNow is the moment the backup tools' fixtures (backrest,
// kopia, duplicati, borgui, zerobyte) were written for: their last runs,
// next runs and overdue plans are relative to it.
var backupFixturesNow = time.Date(2026, 11, 6, 6, 0, 0, 0, time.UTC)

// backupShift is how far the shifted fixtures move, fixed once so that
// shiftedFixtures and shifted agree.
var backupShift = time.Since(backupFixturesNow).Truncate(time.Second)

var (
	fixtureRFC3339 = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})`)
	fixtureCompact = regexp.MustCompile(`\b\d{8}T\d{6}Z\b`)
	fixtureMillis  = regexp.MustCompile(`\b1[78]\d{11}\b`)
)

// shiftedFixtures copies a fixtures folder with every timestamp moved by
// the time since backupFixturesNow, so a spec that judges staleness against
// the clock sees the runs as recent as they were written: RFC 3339 times,
// Duplicati's 20261105T031208Z and Unix milliseconds.
func shiftedFixtures(t *testing.T, dir string) string {
	t.Helper()
	shift := backupShift
	out := t.TempDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".json" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		b = fixtureRFC3339.ReplaceAllFunc(b, func(m []byte) []byte {
			at, err := time.Parse(time.RFC3339Nano, string(m))
			if err != nil {
				t.Fatalf("%s: %s", e.Name(), m)
			}
			return []byte(at.Add(shift).Format(time.RFC3339Nano))
		})
		b = fixtureCompact.ReplaceAllFunc(b, func(m []byte) []byte {
			at, err := time.Parse("20060102T150405Z07:00", string(m))
			if err != nil {
				t.Fatalf("%s: %s", e.Name(), m)
			}
			return []byte(at.Add(shift).UTC().Format("20060102T150405Z07:00"))
		})
		b = fixtureMillis.ReplaceAllFunc(b, func(m []byte) []byte {
			ms, _ := strconv.ParseInt(string(m), 10, 64)
			return []byte(strconv.FormatInt(ms+shift.Milliseconds(), 10))
		})
		if err := os.WriteFile(filepath.Join(out, e.Name()), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

// shifted is a fixture time as the shifted fixtures have it.
func shifted(at string) string {
	ts, err := time.Parse(time.RFC3339, at)
	if err != nil {
		panic(err)
	}
	return ts.Add(backupShift).UTC().Format(time.RFC3339)
}

// checkTarget checks a backup target's kind, state, health, reason and attrs.
func checkTarget(t *testing.T, h *integrationtest.Harness, app, rid, name, state, health, reason string, attrs map[string]any) {
	t.Helper()
	r, ok := h.Resource(rid)
	if !ok {
		t.Errorf("%s isn't published", rid)
		return
	}
	if r.Kind != "backup_target" || r.Name != name || r.State != state || string(r.Health) != health {
		t.Errorf("%s = kind %s, name %q, state %s, health %s", rid, r.Kind, r.Name, r.State, r.Health)
	}
	if got, _ := r.Attrs["health.reason"].(string); got != reason {
		t.Errorf("%s: reason %q, want %q", rid, got, reason)
	}
	if caps, _ := r.Attrs["caps"].([]any); !reflect.DeepEqual(caps, []any{"backups.targets"}) {
		t.Errorf("%s: caps %v", rid, r.Attrs["caps"])
	}
	for k, v := range attrs {
		if !reflect.DeepEqual(r.Attrs[k], v) {
			t.Errorf("%s: %s = %#v, want %#v", rid, k, r.Attrs[k], v)
		}
	}
	if h.Graph().Relations[rid+" part_of "+app] == nil || h.Graph().Relations["integration:i_test monitors "+rid] == nil {
		t.Errorf("%s: relations", rid)
	}
}

// specTest runs chadash spec test over testdata/<id> and wants PASS.
func specTest(t *testing.T, id string) string {
	t.Helper()
	b, err := fs.ReadFile(FS, id+".yaml")
	if err != nil {
		t.Fatal(err)
	}
	if problems := spectest.Lint(b); len(problems) != 0 {
		t.Fatalf("lint: %v", problems)
	}
	var out strings.Builder
	ok, err := spectest.Run(t.Context(), b, "testdata/"+id, &out)
	if err != nil || !ok || !strings.Contains(out.String(), "\nPASS\n") {
		t.Fatalf("spec test: %v\n%s", err, out.String())
	}
	return out.String()
}

// covers builds a backup.covers value as the graph holds it.
func covers(host string, paths ...string) []any {
	out := []any{}
	for _, p := range paths {
		out = append(out, map[string]any{"host": host, "path": p})
	}
	return out
}

const backrestAuth = "Basic Y2hhZGFzaDpmYWtlLWJhY2tyZXN0LXBhc3N3b3Jk" // chadash:fake-backrest-password

func backrest(t *testing.T, dir string) *app {
	return &app{t: t, dir: dir, base: "/backrest",
		auth: func(r *http.Request) bool { return r.Header.Get("Authorization") == backrestAuth },
		route: func(r *http.Request) string {
			if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
				return "not a Connect call"
			}
			return r.URL.Path[len("/backrest"):]
		},
		routes: map[string]string{
			"/v1.Backrest/GetConfig":           "config.json",
			"/v1.Backrest/GetSummaryDashboard": "summary.json",
			"/v1.Backrest/GetOperations":       "operations.json",
		}}
}

func TestBackrest(t *testing.T) {
	integ := bundled(t, "backrest", "Backrest", "basic_auth")
	importerShape(t, integ, "http://10.20.30.40:9898", "basic_auth")
	srv := backrest(t, shiftedFixtures(t, "testdata/backrest")).start()
	url := srv.URL + "/backrest"
	h := integrationtest.New(t)
	inst := h.Connect(integ, map[string]any{"url": url, "host": "10.20.30.40", "path_map": "/userdata:/mnt/user"}, map[string]string{"basic_auth": backrestAuth[len("Basic "):]})
	if res := inst.Test(t.Context()); !res.OK {
		t.Fatalf("test = %+v", res)
	}
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	attrs := checkApp(t, h, "backrest", url)
	app, _ := h.Resource("backrest:i_test/app")
	if app.Health != integration.HealthAttention || app.Attrs["health.reason"] != "photos-weekly failed, docs-hourly is overdue" || attrs["plans"] != 4.0 || attrs["instance"] != "nas" {
		t.Fatalf("app = %+v", app)
	}
	checkSeries(t, h, "backrest", map[string]any{"backups": 4.0, "backups.failed": 1.0, "backups.stale": 1.0}, nil)

	const a = "backrest:i_test/app"
	// A good nightly backup: ok, with its size and next run.
	checkTarget(t, h, a, "backrest:i_test/plan/appdata-nightly", "appdata-nightly", "ok", integration.HealthOK, "", map[string]any{
		"backup.covers":      covers("10.20.30.40", "/mnt/user/appdata"),
		"backup.schedule":    "0 3 * * *",
		"backup.last_status": "ok",
		"backup.last_at":     shifted("2026-11-06T03:05:12Z"),
		"backup.last_ok":     shifted("2026-11-06T03:05:12Z"),
		"backup.next_at":     shifted("2026-11-07T03:00:00Z"),
		"backup.enabled":     true,
		"size_bytes":         48318382080.0,
		"repository":         "nas-b2",
		"overdue":            false,
	})
	// A failed backup: down, with Backrest's message (its first line), and
	// the good one before it.
	checkTarget(t, h, a, "backrest:i_test/plan/photos-weekly", "photos-weekly", "failed", integration.HealthDown,
		"The last backup failed: repository is already locked by PID 4123 on backrest by root", map[string]any{
			"backup.covers":      covers("10.20.30.40", "/mnt/user/photos", "/mnt/user/flash"),
			"backup.schedule":    "weekly",
			"backup.last_status": "failed",
			"backup.last_at":     shifted("2026-11-05T02:00:04Z"),
			"backup.last_ok":     shifted("2026-10-29T03:30:12Z"),
			"backup.last_error":  "repository is already locked by PID 4123 on backrest by root",
		})
	// Running now, but nothing good for days: Backrest marks today overdue.
	checkTarget(t, h, a, "backrest:i_test/plan/docs-hourly", "docs-hourly", "running", integration.HealthAttention,
		"No good backup within its schedule", map[string]any{
			"backup.schedule":    "hourly",
			"backup.last_status": "ok",
			"backup.last_ok":     shifted("2026-11-03T22:00:41Z"),
			"overdue":            true,
		})
	// Never run, schedule off.
	checkTarget(t, h, a, "backrest:i_test/plan/archive-manual", "archive-manual", "never", integration.HealthUnknown,
		"No backup has run yet", map[string]any{"backup.enabled": false, "backup.covers": covers("10.20.30.40", "/mnt/user/archive")})
	if r, _ := h.Resource("backrest:i_test/plan/archive-manual"); r.Attrs["backup.schedule"] != nil || r.Attrs["backup.last_status"] != nil || r.Attrs["backup.last_at"] != nil {
		t.Errorf("never run: %v", r.Attrs)
	}
	if r, _ := h.Resource("backrest:i_test/plan/docs-hourly"); r.Attrs["backup.last_error"] != nil {
		t.Errorf("no error on a good run: %v", r.Attrs["backup.last_error"])
	}
}

// Without a host or path map, the covers name the URL's host and Backrest's
// own paths; without the operations, a failure has no message.
func TestBackrestDefaults(t *testing.T) {
	integ, _, _ := integration.Default.Get("backrest")
	up := backrest(t, shiftedFixtures(t, "testdata/backrest"))
	up.set("/v1.Backrest/GetOperations", "")
	srv := up.start()
	h := integrationtest.New(t)
	inst := h.Connect(integ, map[string]any{"url": srv.URL + "/backrest"}, map[string]string{"basic_auth": backrestAuth[len("Basic "):]})
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	r, _ := h.Resource("backrest:i_test/plan/appdata-nightly")
	if !reflect.DeepEqual(r.Attrs["backup.covers"], covers("127.0.0.1", "/userdata/appdata")) {
		t.Errorf("covers = %#v", r.Attrs["backup.covers"])
	}
	r, _ = h.Resource("backrest:i_test/plan/photos-weekly")
	if r.Health != integration.HealthDown || r.Attrs["health.reason"] != "The last backup failed" || r.Attrs["backup.last_error"] != nil {
		t.Errorf("failed without operations: %+v", r)
	}
}

func TestBackrestFailures(t *testing.T) {
	integ, _, _ := integration.Default.Get("backrest")
	srv := backrest(t, "testdata/backrest").start()
	res := integrationtest.New(t).Connect(integ, map[string]any{"url": srv.URL + "/backrest"}, map[string]string{"basic_auth": "d3Jvbmc6d3Jvbmc="}).Test(t.Context())
	if res.OK || res.ErrorCode != integration.CodeAuth {
		t.Fatalf("wrong password: %+v", res)
	}
	res = integrationtest.New(t).Connect(integ, map[string]any{"url": srv.URL + "/elsewhere"}, map[string]string{"basic_auth": backrestAuth[len("Basic "):]}).Test(t.Context())
	if res.OK || res.ErrorCode != integration.CodeUnreachable {
		t.Fatalf("wrong url: %+v", res)
	}
}

// chadash spec test passes over the fixtures as they are.
func TestBackrestSpecTest(t *testing.T) {
	specTest(t, "backrest")
}
