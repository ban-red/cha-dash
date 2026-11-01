package timeline

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dash.cha.sh/internal/audit"
	"dash.cha.sh/internal/incidents"
	"dash.cha.sh/internal/store"
)

var t0 = time.Date(2026, 10, 25, 9, 0, 0, 0, time.UTC)

func at(m int) time.Time { return t0.Add(time.Duration(m) * time.Minute) }

type fixture struct {
	st  *store.Store
	inc *incidents.Store
	log *audit.Log
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	st, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "chadash.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	f := fixture{st: st, inc: incidents.NewStore(st), log: audit.New(st)}
	ev := func(id string, m int, kind incidents.EventKind, d incidents.Data) incidents.Event {
		raw, _ := json.Marshal(d)
		return incidents.Event{IncidentID: id, At: at(m), Kind: kind, Data: raw}
	}
	sonarr := incidents.Data{Target: "container:sonarr", Class: "problem", Title: "Sonarr isn't answering", Severity: "attention"}
	down := sonarr
	down.Field, down.From, down.To = "severity", "attention", "down"
	cause := sonarr
	cause.Resource, cause.Text = "host:stork", "stork is out of memory"
	fixed := sonarr
	fixed.By = "action"
	secret := incidents.Data{Target: "container:sonarr", Class: "problem", Category: "security", Title: "Sonarr's API key is in a label"}
	repair := incidents.Data{Target: "container:sonarr", Class: "repair", Title: "Reconnect Sonarr"}
	other := incidents.Data{Target: "container:radarr", Class: "problem", Title: "Radarr isn't answering"}
	if err := f.inc.Append(t.Context(),
		ev("sonarr.down", 10, incidents.Opened, sonarr),
		ev("sonarr.down", 12, incidents.Changed, down),
		ev("sonarr.down", 12, incidents.Caused, cause),
		ev("sonarr.down", 30, incidents.Resolved, fixed),
		ev("sonarr.secret", 5, incidents.Opened, secret),
		ev("sonarr.repair", 6, incidents.Opened, repair),
		ev("radarr.down", 7, incidents.Opened, other),
		ev("old", -2000, incidents.Opened, sonarr), // before the range
	); err != nil {
		t.Fatal(err)
	}
	f.log.SetClock(func() time.Time { return at(25) })
	for _, e := range []audit.Entry{
		{Actor: audit.Actor{Kind: "user", ID: "u_owner", Name: "owner"}, Action: "docker.container.restart", Target: "container:sonarr", Tier: "T2", Result: audit.OK, Task: "t_1"},
		{Actor: audit.Actor{Kind: "user", ID: "u_home", Name: "home"}, Action: "docker.container.restart", Target: "container:sonarr", Tier: "T2", Result: audit.Failed, Task: "t_2"},
		{Actor: audit.Actor{Kind: "user", ID: "u_owner", Name: "owner"}, Action: "settings.history.update", Target: "settings:history", Result: audit.OK}, // not a task
	} {
		if _, err := f.log.Append(t.Context(), e); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.Writer().Exec(`INSERT INTO maintenance_windows (id, name, hosts, starts_at, ends_at, weekly, created_at) VALUES
		('mw_1', 'Sunday updates', '["host:stork"]', ?, ?, 0, 0),
		('mw_2', 'Elsewhere', '["host:owl"]', ?, ?, 0, 0)`, at(0).UnixMilli(), at(45).UnixMilli(), at(0).UnixMilli(), at(45).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f fixture) sources() Sources {
	return Sources{Incidents: f.inc, Audit: f.log, DB: f.st.Reader(),
		Name: func(id string) string { return strings.TrimPrefix(strings.TrimPrefix(id, "container:"), "host:") },
		Hosts: func(id string) []string {
			if id == "container:sonarr" {
				return []string{"engine:stork", "host:stork"}
			}
			return nil
		}}
}

func summary(tl Timeline) []string {
	var out []string
	for _, e := range tl.Entries {
		out = append(out, e.At.Format("15:04")+" "+e.Kind+" "+e.Text)
	}
	return out
}

func TestMergeOrderForOneResource(t *testing.T) {
	f := newFixture(t)
	tl, err := Build(t.Context(), f.sources(), Query{Resource: "container:sonarr", From: at(0), To: at(60)}, View{Whole: true})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"09:00 window Maintenance window Sunday updates began",
		"09:05 incident Sonarr's API key is in a label opened",
		"09:06 incident Reconnect Sonarr opened",
		"09:10 incident Sonarr isn't answering opened",
		"09:12 incident Sonarr isn't answering went from attention to down",
		"09:12 cause Sonarr isn't answering: stork is out of memory",
		"09:25 action owner ran docker.container.restart on sonarr",
		"09:25 action home ran docker.container.restart on sonarr, and it failed",
		"09:30 incident Sonarr isn't answering resolved after an action",
		"09:45 window Maintenance window Sunday updates ended",
	}
	got := summary(tl)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("timeline =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if e := tl.Entries[4]; e.Message != "timeline.severity" || e.Args["to"] != "down" || e.From != "attention" || e.Severity != "attention" {
		t.Fatalf("severity entry = %+v", e)
	}
	if tl.History != "off" || tl.Truncated {
		t.Fatalf("history %q, truncated %v", tl.History, tl.Truncated)
	}
}

func TestWholeHome(t *testing.T) {
	f := newFixture(t)
	tl, err := Build(t.Context(), f.sources(), Query{From: at(0), To: at(60)}, View{Whole: true})
	if err != nil {
		t.Fatal(err)
	}
	got := summary(tl)
	// Both windows, Radarr's incident; nothing before the range.
	if len(got) != 13 || !strings.Contains(strings.Join(got, "\n"), "Radarr isn't answering opened") {
		t.Fatalf("whole home = %d entries:\n%s", len(got), strings.Join(got, "\n"))
	}
}

func TestHouseholdSeesItsProjection(t *testing.T) {
	f := newFixture(t)
	v := View{
		User:     "u_home",
		Relevant: func(id string) bool { return id == "container:sonarr" },
		Visible:  func(id string) bool { return id == "container:sonarr" || id == "host:stork" },
	}
	tl, err := Build(t.Context(), f.sources(), Query{From: at(0), To: at(60)}, v)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"09:00 window Maintenance window Sunday updates began",
		"09:10 incident Sonarr isn't answering opened",
		"09:12 incident Sonarr isn't answering went from attention to down",
		"09:25 action home ran docker.container.restart on sonarr, and it failed",
		"09:30 incident Sonarr isn't answering resolved after an action",
		"09:45 window Maintenance window Sunday updates ended",
	}
	got := summary(tl)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("household =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// Nobody recognised sees nothing of incidents or actions.
	tl, _ = Build(t.Context(), f.sources(), Query{From: at(0), To: at(60)}, View{})
	if len(tl.Entries) != 0 {
		t.Fatalf("an empty view = %v", summary(tl))
	}
}

func TestWeeklyWindowRepeats(t *testing.T) {
	f := newFixture(t)
	if _, err := f.st.Writer().Exec(`INSERT INTO maintenance_windows (id, name, hosts, starts_at, ends_at, weekly, created_at) VALUES ('mw_w', 'Weekly', '["host:stork"]', ?, ?, 1, 0)`,
		at(-3*7*24*60+120).UnixMilli(), at(-3*7*24*60+180).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	tl, err := Build(t.Context(), Sources{DB: f.st.Reader()}, Query{From: at(0), To: at(7 * 24 * 60)}, View{Whole: true})
	if err != nil {
		t.Fatal(err)
	}
	var weekly []string
	for _, e := range tl.Entries {
		if e.Window == "mw_w" {
			weekly = append(weekly, e.At.Format("Mon 15:04")+" "+e.Event)
		}
	}
	if len(weekly) != 2 || weekly[0] != "Sun 11:00 start" || weekly[1] != "Sun 12:00 end" {
		t.Fatalf("weekly occurrences = %q", weekly)
	}
}

func TestTruncatedKeepsTheNewest(t *testing.T) {
	f := newFixture(t)
	var evs []incidents.Event
	for i := range MaxEntries + 5 {
		evs = append(evs, incidents.Event{IncidentID: "flap", At: at(100).Add(time.Duration(i) * time.Second), Kind: incidents.Opened, Data: json.RawMessage(`{"class":"problem","title":"Flap"}`)})
	}
	if err := f.inc.Append(t.Context(), evs...); err != nil {
		t.Fatal(err)
	}
	tl, err := Build(t.Context(), Sources{Incidents: f.inc}, Query{From: at(100), To: at(200)}, View{Whole: true})
	if err != nil {
		t.Fatal(err)
	}
	if !tl.Truncated || len(tl.Entries) != MaxEntries || !tl.Entries[len(tl.Entries)-1].At.Equal(evs[len(evs)-1].At) {
		t.Fatalf("truncated %v, %d entries", tl.Truncated, len(tl.Entries))
	}
}
