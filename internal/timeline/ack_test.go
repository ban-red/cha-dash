package timeline

import (
	"encoding/json"
	"strings"
	"testing"

	"dash.cha.sh/internal/incidents"
)

// TestAcknowledgements: who acknowledged an incident and their note, and
// who took it back, on the axis; the household sees who, not the note.
func TestAcknowledgements(t *testing.T) {
	f := newFixture(t)
	base := incidents.Data{Target: "container:radarr", Class: "problem", Title: "Radarr isn't answering"}
	ack, note, cleared := base, base, base
	ack.Who = "sam"
	note.Who, note.Note = "sam", "the NAS is full"
	cleared.Who, cleared.Cleared = "alex", true
	var evs []incidents.Event
	for _, x := range []struct {
		m int
		d incidents.Data
	}{{8, note}, {9, cleared}, {11, ack}} {
		raw, _ := json.Marshal(x.d)
		evs = append(evs, incidents.Event{IncidentID: "radarr.down", At: at(x.m), Kind: incidents.Acknowledged, Data: raw})
	}
	if err := f.inc.Append(t.Context(), evs...); err != nil {
		t.Fatal(err)
	}
	tl, err := Build(t.Context(), f.sources(), Query{Resource: "container:radarr", From: at(0), To: at(60)}, View{Whole: true})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"09:07 incident Radarr isn't answering opened",
		"09:08 incident Radarr isn't answering was acknowledged by sam: the NAS is full",
		"09:09 incident alex took back the acknowledgement of Radarr isn't answering",
		"09:11 incident Radarr isn't answering was acknowledged by sam",
	}
	if got := summary(tl); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("timeline =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if e := tl.Entries[2]; e.Event != "unacknowledged" || e.Actor != "alex" {
		t.Fatalf("cleared entry %+v", e)
	}

	home, err := Build(t.Context(), f.sources(), Query{Resource: "container:radarr", From: at(0), To: at(60)}, View{Relevant: func(string) bool { return true }, Visible: func(string) bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	if s := strings.Join(summary(home), "\n"); strings.Contains(s, "NAS") || !strings.Contains(s, "acknowledged by sam") {
		t.Fatalf("household timeline:\n%s", s)
	}
}
