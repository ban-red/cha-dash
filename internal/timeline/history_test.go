package timeline

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dash.cha.sh/internal/history"
	"dash.cha.sh/internal/store"
)

// One resource's timeline from the three real sources together: the
// incident, the action that fixed it, and the health changes the metrics
// history recorded, in order. 14b's manual test 10, which needed a week of
// recording on a real hub (docs/plan/17-test-automation.md).
func TestTimelineMergesHealthChangesFromHistory(t *testing.T) {
	f := newFixture(t)
	hst, err := store.OpenHistory(t.Context(), filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { hst.Close() })
	if _, err := hst.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	now := at(0)
	rec, err := history.Open(t.Context(), history.Options{Store: hst, Level: history.Standard, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	rec.State("container:sonarr", "ok", at(0))
	rec.State("container:sonarr", "down", at(11))
	rec.State("container:sonarr", "ok", at(26))
	rec.State("container:radarr", "down", at(20)) // another resource: not on sonarr's timeline
	now = at(40)
	if err := rec.Close(t.Context()); err != nil {
		t.Fatal(err)
	}

	src := f.sources()
	src.History = rec
	tl, err := Build(t.Context(), src, Query{Resource: "container:sonarr", From: at(0), To: at(60)}, View{Whole: true})
	if err != nil {
		t.Fatal(err)
	}
	if tl.History != string(history.Standard) {
		t.Errorf("history %q", tl.History)
	}
	var got []string
	for _, e := range tl.Entries {
		got = append(got, e.At.Format("15:04")+" "+e.Kind+" "+e.From+">"+e.To)
	}
	// The incident opens at 09:10, health goes down at 09:11, the restart
	// at 09:25, health back at 09:26, the incident resolved at 09:30.
	order := []string{"09:10 incident", "09:11 state ok>down", "09:25 action", "09:26 state down>ok", "09:30 incident"}
	i := 0
	for _, g := range got {
		if i < len(order) && strings.HasPrefix(g, order[i]) {
			i++
		}
	}
	if i != len(order) {
		t.Fatalf("timeline =\n%s\nwant, in this order:\n%s", strings.Join(got, "\n"), strings.Join(order, "\n"))
	}
	for _, e := range tl.Entries {
		if e.Kind == KindState && (e.Resource != "container:sonarr" || e.Text == "") {
			t.Errorf("state entry %+v", e)
		}
	}
}
