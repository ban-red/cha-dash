package history

import (
	"path/filepath"
	"testing"
	"time"
)

func TestMatchKey(t *testing.T) {
	for _, c := range []struct {
		p, key string
		want   bool
	}{
		{"docker:*#net_rx", "docker:abc#net_rx", true},
		{"docker:*#net_rx", "docker:abc#net_tx", false},
		{"docker:*#net_rx", "vm:abc#net_rx", false},
		{"*#temp", "host:pve#temp", true},
		{"*#temp", "host:pve#temp_max", false},
		{"vm:a#cpu", "vm:a#cpu", true},
		{"vm:a#cpu", "vm:a#cpu2", false},
		{"*a*b*", "xxaxxbxx", true},
		{"*a*b", "xxaxxbxxb", true},
		{"*a*b", "xxaxxbxxc", false},
		{"a**", "a", true},
	} {
		if got := matchKey(c.p, c.key); got != c.want {
			t.Errorf("matchKey(%q, %q) = %v", c.p, c.key, got)
		}
	}
	for _, bad := range []string{"", "*", "**", "a\nb"} {
		if CheckKeyPattern(bad) == nil {
			t.Errorf("CheckKeyPattern(%q) allowed", bad)
		}
	}
}

func TestExcludedKeysStopRecording(t *testing.T) {
	c := &clock{t0}
	r := openRecorder(t, openStore(t, filepath.Join(t.TempDir(), "history.db")), Standard, c)
	r.Record("docker:a#net_rx", "i_dk", "B/s", c.t, 1) // recorded before the pattern
	r.SetExcludedKeys([]string{"docker:*#net_rx"})
	for i := range 5 {
		at := c.t.Add(time.Duration(i) * time.Minute)
		r.Record("docker:a#net_rx", "i_dk", "B/s", at, 1)
		r.Record("docker:b#net_rx", "i_dk", "B/s", at, 1)
		r.Record("docker:a#cpu", "i_dk", "%", at, 1)
	}
	if r.Known("docker:b#net_rx") || !r.Known("docker:a#cpu") {
		t.Fatal("the pattern should leave out docker:b's receive rate and nothing else")
	}
	if n := r.Matching("docker:*#net_rx"); n != 2 {
		t.Fatalf("Matching = %d, want both containers", n)
	}
	if got := r.ExcludedKeys(); len(got) != 1 {
		t.Fatalf("ExcludedKeys = %v", got)
	}
	r.SetExcludedKeys(nil)
	r.Record("docker:b#net_rx", "i_dk", "B/s", c.t.Add(10*time.Minute), 1)
	if !r.Known("docker:b#net_rx") {
		t.Fatal("clearing the patterns should record it again")
	}
}

func TestCheckKeep(t *testing.T) {
	ok := []map[Tier]time.Duration{
		nil,
		{Minute: 6 * time.Hour},
		{Minute: 14 * day, Quarter: 14 * day},
		{Quarter: 365 * day, Hour: 730 * day, Day: 0},
		{Minute: 36 * time.Hour}, // between choices
	}
	for _, o := range ok {
		if err := CheckKeep(o); err != nil {
			t.Errorf("CheckKeep(%v) = %v", o, err)
		}
	}
	bad := []map[Tier]time.Duration{
		{Minute: time.Hour},                  // under the shortest
		{Minute: 0},                          // the 1-minute tier can't be forever
		{Hour: 3650 * day},                   // over the longest
		{Minute: 7 * day, Quarter: 2 * day},  // coarser keeps less
		{Hour: 1825 * day, Day: 730 * day},   // likewise
		{Quarter: 7 * day, Minute: 14 * day}, // at Standard, 14 > 7
	}
	for _, o := range bad {
		if CheckKeep(o) == nil {
			t.Errorf("CheckKeep(%v) allowed", o)
		}
	}
	if words(14*day) != "14 days" || words(365*day) != "1 year" || words(6*time.Hour) != "6 hours" || words(0) != "forever" {
		t.Fatal("words")
	}
}

func TestKeepOverridesRetentionAndQueries(t *testing.T) {
	c := &clock{t0}
	st := openStore(t, filepath.Join(t.TempDir(), "history.db"))
	r := openRecorder(t, st, Standard, c)
	feed(t, r, c, 12*time.Hour, []string{"vm:a#cpu"}, minutes)
	if err := r.flush(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	// An hour's range reads the 1-minute tier while it keeps that far back.
	if rg, err := r.Query(t.Context(), "vm:a#cpu", c.t.Add(-time.Hour), c.t, 60); err != nil || rg.Step != time.Minute {
		t.Fatalf("Query step %v (%v)", rg.Step, err)
	}
	r.SetKeep(map[Tier]time.Duration{Minute: 6 * time.Hour})
	if got := r.Keep(); got[Minute] != 6*time.Hour {
		t.Fatalf("Keep = %v", got)
	}
	// From 8 hours back the 1-minute tier no longer reaches: the quarter does.
	if rg, _ := r.Query(t.Context(), "vm:a#cpu", c.t.Add(-8*time.Hour), c.t.Add(-7*time.Hour), 60); rg.Step != 15*time.Minute {
		t.Fatalf("an old hour reads step %v, want the quarter tier", rg.Step)
	}
	// The next tick applies it rather than waiting for the hour.
	r.lastRetain = c.t
	r.tick(t.Context(), c.t)
	var oldest int64
	if err := st.Reader().QueryRow(`SELECT min(start) FROM chunks WHERE tier = 1`).Scan(&oldest); err != nil {
		t.Fatal(err)
	}
	if age := c.t.Sub(time.UnixMilli(oldest)); age > 7*time.Hour+time.Minute {
		t.Fatalf("the oldest 1-minute chunk is %v old, want about 6 h", age)
	}
	s, err := r.Stats(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if m := s.Tiers[0]; m.Keep != 6*time.Hour || !m.Custom || m.Default != 48*time.Hour || s.Tiers[1].Custom {
		t.Fatalf("tier stats = %+v", s.Tiers[:2])
	}
	if EstimateKeep(Standard, map[Tier]time.Duration{Minute: 6 * time.Hour}, 1000) >= Estimate(Standard, 1000) {
		t.Fatal("keeping less should estimate less")
	}
}
