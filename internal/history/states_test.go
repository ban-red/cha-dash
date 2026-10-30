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

func TestStatsEstimateAndClear(t *testing.T) {
	c := &clock{t0}
	path := filepath.Join(t.TempDir(), "history.db")
	r := openRecorder(t, openStore(t, path), Standard, c)
	feed(t, r, c, 2*time.Hour, []string{"vm:a#cpu", "vm:b#cpu"}, minutes)
	r.State("vm:a", "ok", t0)
	r.State("vm:a", "down", t0.Add(time.Hour))
	r.tick(t.Context(), c.t.Add(10*time.Minute))
	if err := r.flush(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	st, err := r.Stats(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if st.Level != Standard || st.Series != 2 || st.Resources != 1 || st.FileBytes == 0 || st.Tiers[0].Chunks != 4 || st.Tiers[1].Chunks != 2 || !st.Tiers[0].Oldest.Equal(t0) {
		t.Fatalf("stats = %+v", st)
	}
	// About 30 MB at 1,000 series for Standard, as 13 · History measured; long-term several times more.
	if mb := Estimate(Standard, 1000) / 1e6; mb < 25 || mb > 35 {
		t.Fatalf("Standard at 1,000 series ≈ %d MB", mb)
	}
	if Estimate(LongTerm, 1000) < 3*Estimate(Standard, 1000) || Estimate(Off, 1000) != 0 {
		t.Fatal("long-term should be several times Standard, and Off nothing")
	}

	if err := r.Clear(t.Context()); err != nil {
		t.Fatal(err)
	}
	st, _ = r.Stats(t.Context())
	if st.Series != 0 || st.Tiers[0].Chunks != 0 {
		t.Fatalf("after Clear: %+v", st)
	}
	// What vm:a is now is written again, so its uptime starts over.
	_ = r.flush(t.Context(), false)
	got, _ := r.States(t.Context(), "vm:a", t0, c.t.Add(time.Hour))
	if len(got) != 1 || got[0].Status != "down" {
		t.Fatalf("states after Clear = %v", changes(got))
	}
	// Recording carries on.
	feed(t, r, c, 10*time.Minute, []string{"vm:a#cpu"}, minutes)
	r.tick(t.Context(), c.t)
	if m := query(t, r, "vm:a#cpu", Minute, c.t.Add(-10*time.Minute), c.t); m[0].N == 0 {
		t.Fatal("nothing recorded after Clear")
	}
}

func TestExcludedInstancesAndClearingOne(t *testing.T) {
	c := &clock{t0}
	r := openRecorder(t, openStore(t, filepath.Join(t.TempDir(), "history.db")), Standard, c)
	r.SetExcluded([]string{"i_noisy"})
	end := t0.Add(2 * time.Hour)
	for ; c.t.Before(end); c.t = c.t.Add(time.Minute) {
		r.Record("vm:a#cpu", "i_pve", "%", c.t, 1)
		r.Record("vm:b#cpu", "i_pve", "%", c.t, 2)
		r.Record("app:x#rx", "i_noisy", "B/s", c.t, 3)
		r.Record("app:y#cpu", "i_arr", "%", c.t, 4)
	}
	r.State("app:x", "ok", t0) // health is recorded for every instance
	r.tick(t.Context(), c.t)
	if err := r.flush(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	if got := r.Excluded(); len(got) != 1 || got[0] != "i_noisy" {
		t.Fatalf("Excluded = %v", got)
	}
	s, err := r.Stats(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Instances) != 2 || s.Instances[0].ID != "i_pve" || s.Instances[0].Series != 2 || s.Instances[0].Bytes == 0 || s.Instances[1].ID != "i_arr" {
		t.Fatalf("instances = %+v", s.Instances)
	}
	if err := r.ClearInstance(t.Context(), "i_pve"); err != nil {
		t.Fatal(err)
	}
	s, _ = r.Stats(t.Context())
	if len(s.Instances) != 1 || s.Instances[0].ID != "i_arr" || s.Series != 1 {
		t.Fatalf("after clearing i_pve: %+v", s)
	}
	if got, _ := r.States(t.Context(), "app:x", t0, c.t); len(got) != 1 {
		t.Fatal("clearing an instance dropped health rows")
	}
	// It carries on recording from now.
	r.Record("vm:a#cpu", "i_pve", "%", c.t, 1)
	r.Record("vm:a#cpu", "i_pve", "%", c.t.Add(time.Minute), 1)
	if m := query(t, r, "vm:a#cpu", Minute, c.t, c.t.Add(time.Minute)); m[0].N != 1 {
		t.Fatalf("after clearing: %+v", m)
	}
}
