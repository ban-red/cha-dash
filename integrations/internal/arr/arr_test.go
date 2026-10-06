package arr

import (
	"encoding/json"
	"testing"
	"time"

	"dash.cha.sh/internal/integration"
)

func TestCalendar(t *testing.T) {
	now := time.Date(2026, 10, 3, 14, 5, 0, 0, time.UTC)
	if b, _ := json.Marshal(Calendar(nil, now)); string(b) != "[]" {
		t.Fatalf("empty calendar = %s", b)
	}
	var many []CalendarItem
	for i := range 40 {
		many = append(many, CalendarItem{At: now.Add(time.Duration(40-i) * time.Hour), Title: "x"})
	}
	many = append(many, CalendarItem{At: now, Title: "b"}, CalendarItem{At: now, Title: "a"}, CalendarItem{At: now, Title: ""})
	got := Calendar(many, now)
	if len(got) != CalendarMax || got[0].Title != "a" || got[1].Title != "b" || !got[2].At.Equal(now.Add(time.Hour)) {
		t.Fatalf("calendar = %v", got[:3])
	}

	// Unmonitored releases, sooner than all of them, don't push any
	// monitored one out: each kind has its own CalendarMax.
	for i := range 40 {
		many = append(many, CalendarItem{At: now.Add(-time.Duration(i+1) * time.Minute), Title: "u", Unmonitored: true})
	}
	got = Calendar(many, now)
	var un int
	for _, it := range got {
		if it.Unmonitored {
			un++
		}
	}
	if len(got) != 2*CalendarMax || un != CalendarMax || !got[0].Unmonitored {
		t.Fatalf("with unmonitored: %d items, %d unmonitored", len(got), un)
	}
	if b, _ := json.Marshal(got[len(got)-1]); string(b) != `{"at":"2026-10-04T18:05:00Z","title":"x"}` {
		t.Fatalf("a monitored item = %s", b)
	}
}

func TestHealthOf(t *testing.T) {
	app := App{Name: "Sonarr"}
	if h := HealthOf(app, []HealthItem{{Type: "warning", Message: "update"}, {Type: "notice"}}); h.Status != integration.HealthOK {
		t.Fatalf("warnings only = %+v", h)
	}
	h := HealthOf(app, []HealthItem{{Type: "Error", Message: "Disk is full."}})
	if h.Status != integration.HealthAttention || h.Reason != "Sonarr: Disk is full" {
		t.Fatalf("error = %+v", h)
	}
}
