package history

import (
	"path/filepath"
	"testing"
	"time"
)

func changes(cs []StateChange) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.At.Format("15:04") + " " + c.Status
	}
	return out
}

func TestStatesRecordChangesAndSurviveARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	st := openStore(t, path)
	c := &clock{t0}
	r := openRecorder(t, st, Standard, c)
	at := func(m int) time.Time { return t0.Add(time.Duration(m) * time.Minute) }

	if got := r.Since("app:a", "ok", at(0)); !got.Equal(at(0)) {
		t.Fatalf("Since of an unknown resource = %v", got)
	}
	r.State("app:a", "ok", at(0))
	r.State("app:a", "ok", at(5)) // no change, no row
	r.State("app:a", "down", at(10))
	r.State("app:a", "down", at(12))
	r.Gone("app:never", at(12)) // never seen: nothing to say
	if got := r.Since("app:a", "down", at(14)); !got.Equal(at(10)) {
		t.Fatalf("down since %v, want 10:10", got)
	}
	// Rows not yet written are read too.
	got, err := r.States(t.Context(), "app:a", at(0), at(60))
	if err != nil || len(got) != 2 {
		t.Fatalf("States = %v (%v)", changes(got), err)
	}

	c.t = at(20)
	if err := r.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	st.Close()
	// Back at 10:30: still down, so down since 10:10, and the half hour the
	// hub was stopped shows as such.
	c.t = at(30)
	r = openRecorder(t, openStore(t, path), Standard, c)
	if got := r.Since("app:a", "down", at(30)); !got.Equal(at(10)) {
		t.Fatalf("down since %v after a restart, want 10:10", got)
	}
	if got := r.Since("app:a", "ok", at(30)); !got.Equal(at(30)) {
		t.Fatalf("a different status starts now, got %v", got)
	}
	r.State("app:a", "down", at(10)) // the emitter passes what Since said
	r.State("app:a", "ok", at(40))
	r.Gone("app:a", at(50))
	got, _ = r.States(t.Context(), "app:a", at(5), at(60))
	want := []string{"10:05 ok", "10:10 down", "10:20 stopped", "10:20 down", "10:40 ok", "10:50 gone"}
	if g := changes(got); len(g) != len(want) || g[0] != want[0] || g[2] != want[2] || g[4] != want[4] || g[5] != want[5] {
		t.Fatalf("States = %v, want %v", g, want)
	}
	// The row after the stop goes 1 ms after it.
	if got[3].At.Sub(got[2].At) != time.Millisecond {
		t.Fatalf("down again at %v", got[3].At)
	}

	s := Summarize(got, at(5), at(60))
	// Watched: down 10:10–10:20 and 10:20–10:40 (30 min), ok 10:05–10:10 and 10:40–10:50 (15 min).
	if s.Totals["down"] != 30*time.Minute-time.Millisecond || s.Totals["stopped"] != time.Millisecond || s.Totals["gone"] != 10*time.Minute {
		t.Fatalf("totals = %v", s.Totals)
	}
	if s.Up < 0.333 || s.Up > 0.334 {
		t.Fatalf("up = %v, want a third", s.Up)
	}
	if Summarize(nil, at(0), at(60)).Up != -1 {
		t.Fatal("no rows should be unwatched")
	}
}

func TestStateRetentionKeepsTheRowInEffect(t *testing.T) {
	c := &clock{t0}
	r := openRecorder(t, openStore(t, filepath.Join(t.TempDir(), "history.db")), Standard, c)
	r.State("app:steady", "ok", t0)
	r.State("app:flappy", "ok", t0)
	r.State("app:flappy", "down", t0.Add(time.Hour))
	r.State("app:flappy", "ok", t0.Add(2*time.Hour))
	r.State("app:old", "ok", t0)
	r.Gone("app:old", t0.Add(time.Hour))
	if err := r.flush(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	if err := r.retain(t.Context(), t0.Add(100*day)); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := r.st.Reader().QueryRow(`SELECT count(*) FROM states`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("%d rows left (%v), want steady's and flappy's newest", n, err)
	}
	got, _ := r.States(t.Context(), "app:flappy", t0.Add(99*day), t0.Add(100*day))
	if len(got) != 1 || got[0].Status != "ok" {
		t.Fatalf("flappy = %v", changes(got))
	}
	if r.states["app:old"] != nil || r.states["app:steady"] == nil {
		t.Fatal("the gone resource should be forgotten, the steady one kept")
	}
}
