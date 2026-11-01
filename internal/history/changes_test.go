package history

import (
	"path/filepath"
	"testing"
	"time"
)

// Changes, for the timeline: every resource's changes with what came
// before, leaving out the hub's own stops and the row after each.
func TestChangesForTheTimeline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	c := &clock{t0}
	r := openRecorder(t, openStore(t, path), Standard, c)
	at := func(m int) time.Time { return t0.Add(time.Duration(m) * time.Minute) }
	r.State("app:a", "ok", at(0))
	r.State("app:b", "ok", at(1))
	r.State("app:a", "down", at(10))
	c.t = at(20)
	if err := r.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	c.t = at(30)
	r = openRecorder(t, openStore(t, path), Standard, c)
	r.State("app:a", "down", at(30)) // after the restart: not a change
	r.State("app:a", "ok", at(40))
	r.tick(t.Context(), at(41))

	all, err := r.Changes(t.Context(), "", at(5), at(60), 0)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, ch := range all {
		got = append(got, ch.At.Format("15:04")+" "+ch.Resource+" "+ch.From+">"+ch.Status)
	}
	want := []string{"10:10 app:a ok>down", "10:40 app:a down>ok"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("Changes = %q, want %q", got, want)
	}
	one, _ := r.Changes(t.Context(), "app:b", at(0), at(60), 0)
	if len(one) != 1 || one[0].From != "" || one[0].Status != "ok" {
		t.Fatalf("app:b = %+v", one)
	}
	last, _ := r.Changes(t.Context(), "", at(0), at(60), 1)
	if len(last) != 1 || last[0].Status != "ok" || last[0].Resource != "app:a" {
		t.Fatalf("limit 1 keeps the newest: %+v", last)
	}
}
