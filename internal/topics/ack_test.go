package topics

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestProjectIncidentAck: the household sees who acknowledged an incident
// and when, never the note.
func TestProjectIncidentAck(t *testing.T) {
	raw := strings.Replace(plexDown, `"status":"open"`, `"status":"open","acknowledged":{"by":"sam","at":"2026-10-03T14:05:00Z","note":"the NAS at 192.0.2.9 is full"}`, 1)
	p, ok := ProjectIncident(json.RawMessage(raw), func(string) bool { return true })
	if !ok {
		t.Fatal("not shown")
	}
	var got struct {
		Acknowledged map[string]any `json:"acknowledged"`
	}
	if err := json.Unmarshal(p, &got); err != nil {
		t.Fatal(err)
	}
	if got.Acknowledged["by"] != "sam" || got.Acknowledged["at"] != "2026-10-03T14:05:00Z" || len(got.Acknowledged) != 2 {
		t.Fatalf("acknowledged %v", got.Acknowledged)
	}
	if strings.Contains(string(p), "192.0.2.9") {
		t.Fatalf("the note leaks: %s", p)
	}
	// Without one, none.
	if p, _ := ProjectIncident(json.RawMessage(plexDown), func(string) bool { return true }); strings.Contains(string(p), "acknowledged") {
		t.Fatalf("acknowledged out of nowhere: %s", p)
	}
}
