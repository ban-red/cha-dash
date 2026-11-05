package incidents

import (
	"encoding/json"
	"errors"
	"maps"
	"path/filepath"
	"testing"
	"time"

	"dash.cha.sh/internal/livedoc"
	"dash.cha.sh/internal/store"
)

type clock struct{ t time.Time }

func (c *clock) Now() time.Time { return c.t }

type env struct {
	t     *testing.T
	st    *store.Store
	s     *Store
	inbox *livedoc.Doc
	clock *clock
	exp   *Expectations
	rec   *Recorder
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "chadash.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, st: st, s: NewStore(st), inbox: livedoc.New("incidents"), clock: &clock{t: time.Date(2026, 10, 6, 2, 0, 0, 0, time.UTC)}, exp: &Expectations{}}
	e.restart()
	return e
}

// restart starts a new recorder over the same database, as the hub does.
func (e *env) restart() {
	e.rec = NewRecorder(RecorderOptions{Store: e.s, Inbox: e.inbox, Expect: e.exp, Now: e.clock.Now})
}

func (e *env) publish(id string, fields map[string]any) {
	e.t.Helper()
	in := map[string]any{"id": id, "class": "problem", "scenario": "check-failed", "severity": "attention", "status": "open", "phase": "open",
		"title": "Sonarr isn't answering", "target": "container:sonarr", "opened_at": e.clock.t.Format(time.RFC3339), "updated_at": e.clock.t.Format(time.RFC3339),
		"causes": []any{}, "blast_radius": []any{}}
	maps.Copy(in, fields)
	raw, _ := json.Marshal(in)
	if _, err := e.inbox.Update(func(tx *livedoc.Tx) error { tx.Put("incidents", id, json.RawMessage(raw)); return nil }); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) step() { e.rec.Step(e.t.Context()) }

func (e *env) kinds(q Query) []EventKind {
	e.t.Helper()
	evs, err := e.s.Events(e.t.Context(), q)
	if err != nil {
		e.t.Fatal(err)
	}
	out := []EventKind{}
	for _, ev := range evs {
		out = append(out, ev.Kind)
	}
	return out
}

func eq(a, b []EventKind) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestRecorderLifecycle(t *testing.T) {
	e := newEnv(t)
	e.publish("docker.sonarr.down", nil)
	e.step()
	e.clock.t = e.clock.t.Add(time.Minute)
	e.publish("docker.sonarr.down", map[string]any{"severity": "down", "phase": "restarting", "updated_at": e.clock.t.Format(time.RFC3339),
		"causes": []any{map[string]any{"text": "Its host is out of memory", "resource": "host:stork"}}})
	e.step()
	e.step() // nothing changed: nothing written
	e.clock.t = e.clock.t.Add(time.Minute)
	e.publish("docker.sonarr.down", map[string]any{"status": "resolved", "phase": "resolved", "resolved_by": "action", "resolved_at": e.clock.t.Format(time.RFC3339)})
	e.step()

	got := e.kinds(Query{Incident: "docker.sonarr.down"})
	want := []EventKind{Opened, Changed, Changed, Caused, Resolved}
	if !eq(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	evs, _ := e.s.Events(t.Context(), Query{Kinds: []EventKind{Changed}})
	if d := DataOf(evs[0]); d.Field != "severity" || d.From != "attention" || d.To != "down" || d.Target != "container:sonarr" {
		t.Fatalf("severity change = %+v", d)
	}
	evs, _ = e.s.Events(t.Context(), Query{Kinds: []EventKind{Resolved}})
	if d := DataOf(evs[0]); d.By != "action" || !evs[0].At.Equal(e.clock.t) {
		t.Fatalf("resolved = %+v at %v", d, evs[0].At)
	}
	// By resource: the target, every kind.
	if n := len(e.kinds(Query{Resource: "container:sonarr"})); n != 5 {
		t.Fatalf("by resource = %d events", n)
	}
	if n := len(e.kinds(Query{Resource: "container:radarr"})); n != 0 {
		t.Fatalf("another resource = %d events", n)
	}

	// Reopened: a new opening.
	e.clock.t = e.clock.t.Add(time.Hour)
	e.publish("docker.sonarr.down", map[string]any{"updated_at": e.clock.t.Format(time.RFC3339)})
	e.step()
	if got := e.kinds(Query{Incident: "docker.sonarr.down", From: e.clock.t}); !eq(got, []EventKind{Opened}) {
		t.Fatalf("reopened = %v", got)
	}
}

func TestRecorderSkipsRequestsAndSurvivesRestart(t *testing.T) {
	e := newEnv(t)
	e.publish("corr.merge.1", map[string]any{"class": "suggestion"})
	e.publish("approval.p_1", map[string]any{"class": "approval"})
	e.publish("unraid-magpie.repair", map[string]any{"class": "repair"})
	e.step()
	if got := e.kinds(Query{}); !eq(got, []EventKind{Opened}) {
		t.Fatalf("events = %v, want only the repair's opening", got)
	}
	// A restart re-reads the inbox as the integrations publish it again.
	e.restart()
	e.step()
	if got := e.kinds(Query{}); len(got) != 1 {
		t.Fatalf("after a restart: %v, want nothing new", got)
	}
}

func TestRecorderExpected(t *testing.T) {
	e := newEnv(t)
	e.exp.Set(ExpecterFunc(func(resource string, at time.Time) (string, bool) {
		return "mw_sunday", resource == "container:sonarr"
	}))
	e.publish("docker.sonarr.down", nil)
	e.publish("docker.radarr.down", map[string]any{"target": "container:radarr"})
	e.step()
	evs, _ := e.s.Events(t.Context(), Query{Kinds: []EventKind{Expected}})
	if len(evs) != 1 || evs[0].IncidentID != "docker.sonarr.down" || DataOf(evs[0]).Window != "mw_sunday" {
		t.Fatalf("expected events = %+v", evs)
	}
	var nilExp *Expectations
	if _, ok := nilExp.Expected("container:sonarr", e.clock.t); ok {
		t.Fatal("a nil Expectations expected something")
	}
	e.exp.Set(nil)
	if _, ok := e.exp.Expected("container:sonarr", e.clock.t); ok {
		t.Fatal("a removed Expecter still answers")
	}
}

func TestRetention(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	now := e.clock.t
	old := Event{IncidentID: "a", At: now.Add(-91 * day), Kind: Opened}
	mid := Event{IncidentID: "b", At: now.Add(-20 * day), Kind: Opened}
	fresh := Event{IncidentID: "c", At: now.Add(-time.Hour), Kind: Opened}
	if err := e.s.Append(ctx, old, mid, fresh); err != nil {
		t.Fatal(err)
	}
	if n, err := e.s.Prune(ctx, now); err != nil || n != 1 {
		t.Fatalf("prune at 90 days = %d, %v", n, err)
	}
	if err := e.s.SetKeep(ctx, 12*day, now); !errors.Is(err, ErrBadKeep) {
		t.Fatalf("12 days = %v, want ErrBadKeep", err)
	}
	if err := e.s.SetKeep(ctx, 7*day, now); err != nil {
		t.Fatal(err)
	}
	// Saved for the next start.
	again := NewStore(e.st)
	if err := again.Load(ctx); err != nil || again.Keep() != 7*day {
		t.Fatalf("loaded keep = %v, %v", again.Keep(), err)
	}
	// The recorder prunes on its hourly schedule.
	e.rec.store = again
	e.step()
	if got := e.kinds(Query{}); len(got) != 1 {
		t.Fatalf("after pruning at 7 days: %v, want the fresh one", got)
	}
}
