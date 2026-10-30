package history

import (
	"path/filepath"
	"testing"
	"time"
)

func TestPulseCountsTheWorstOfEachSlot(t *testing.T) {
	c := &clock{t0}
	r := openRecorder(t, openStore(t, filepath.Join(t.TempDir(), "history.db")), Standard, c)
	at := func(m int) time.Time { return t0.Add(time.Duration(m) * time.Minute) }
	r.State("app:a", "ok", at(-60)) // in effect from before the range
	r.State("app:a", "down", at(20))
	r.State("app:a", "ok", at(25)) // down for part of the 15-minute slot: the slot is down
	r.State("app:b", "ok", at(0))
	r.State("app:b", "attention", at(40))
	r.State("app:c", "maintenance", at(0)) // unwatched: not counted
	r.State("app:hidden", "down", at(0))
	if err := r.flush(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	r.State("app:b", "ok", at(50)) // pending, not yet written
	p, err := r.Pulse(t.Context(), at(0), at(60), 4, func(id string) bool { return id != "app:hidden" })
	if err != nil {
		t.Fatal(err)
	}
	if p.Step != 15*time.Minute || len(p.Slots) != 4 || !p.Start.Equal(at(0)) {
		t.Fatalf("grid %v × %d from %v", p.Step, len(p.Slots), p.Start)
	}
	want := []map[string]int{
		{"ok": 2},
		{"ok": 1, "down": 1},
		{"ok": 1, "attention": 1},
		{"ok": 1, "attention": 1},
	}
	for i, w := range want {
		got := p.Slots[i].Counts
		if len(got) != len(w) {
			t.Fatalf("slot %d = %v, want %v", i, got, w)
		}
		for k, v := range w {
			if got[k] != v {
				t.Fatalf("slot %d = %v, want %v", i, got, w)
			}
		}
	}
	if p.Slots[1].Issues["app:a"] != "down" || len(p.Slots[0].Issues) != 0 || p.Slots[3].Issues["app:b"] != "attention" {
		t.Fatalf("issues = %v", p.Slots)
	}
}
