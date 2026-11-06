package server

import (
	"encoding/json"
	"testing"
)

// The summary Home Assistant reads: health from the open problems, the
// worst first, acknowledged ones counted but not making it down.
func TestSummarize(t *testing.T) {
	inc := func(id, class, sev, status, opened string, ackBy string) json.RawMessage {
		m := map[string]any{"id": id, "class": class, "severity": sev, "status": status, "title": id, "opened_at": opened, "target": "host:" + id}
		if ackBy != "" {
			m["acknowledged"] = map[string]any{"by": ackBy, "at": opened}
		}
		b, _ := json.Marshal(m)
		return b
	}
	doc := func(items ...json.RawMessage) map[string]any {
		m := map[string]json.RawMessage{}
		for i, it := range items {
			m[string(rune('a'+i))] = it
		}
		return map[string]any{"now": "2026-11-06T10:00:00Z", "incidents": m}
	}
	if s := summarize(doc()); s.Health != "ok" || s.Incidents.Open != 0 || len(s.Top) != 0 || s.Now != "2026-11-06T10:00:00Z" {
		t.Fatalf("empty: %+v", s)
	}
	s := summarize(doc(
		inc("slow", "problem", "attention", "open", "2026-11-06T09:00:00Z", ""),
		inc("nas", "problem", "down", "open", "2026-11-06T08:00:00Z", "sam"),
		inc("old", "problem", "down", "resolved", "2026-11-05T08:00:00Z", ""),
		inc("cred", "repair", "attention", "open", "2026-11-06T09:30:00Z", ""),
		inc("info", "problem", "info", "open", "2026-11-06T09:40:00Z", ""),
	))
	if s.Health != "attention" || s.Incidents != (SummaryCounts{Open: 2, Down: 1, Attention: 1, Acknowledged: 1}) {
		t.Fatalf("an acknowledged down doesn't make the home down: %+v", s)
	}
	if len(s.Top) != 2 || s.Top[0].ID != "slow" || s.Top[1].ID != "nas" || s.Top[1].AcknowledgedBy != "sam" {
		t.Fatalf("top: %+v", s.Top)
	}
	s = summarize(doc(
		inc("slow", "problem", "attention", "open", "2026-11-06T09:00:00Z", ""),
		inc("down1", "problem", "down", "open", "2026-11-06T07:00:00Z", ""),
		inc("down2", "problem", "down", "open", "2026-11-06T09:59:00Z", ""),
	))
	if s.Health != "down" || s.Top[0].ID != "down2" || s.Top[1].ID != "down1" || s.Top[2].ID != "slow" {
		t.Fatalf("down: %+v", s)
	}
	var many []json.RawMessage
	for i := range 8 {
		many = append(many, inc(string(rune('a'+i)), "problem", "attention", "open", "2026-11-06T09:00:00Z", ""))
	}
	if s := summarize(doc(many...)); len(s.Top) != SummaryTop || s.Incidents.Open != 8 {
		t.Fatalf("top is capped: %d of %d", len(s.Top), s.Incidents.Open)
	}
}
