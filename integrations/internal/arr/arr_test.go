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
