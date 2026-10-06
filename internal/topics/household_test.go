package topics

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/livedoc"
	"dash.cha.sh/internal/realtime"
)

const plexDown = `{"id":"plex-down","scenario":"oom","class":"problem","severity":"down","status":"open","phase":"","title":"Plex is down",
	"summary":"Killed for memory at 14:03 on jupiter.","target":"app:plex","opened_at":"2026-10-03T14:03:00Z","updated_at":"2026-10-03T14:03:00Z",
	"facts":{"exit_code":137,"host":"192.0.2.5"},"causes":[{"text":"qBittorrent grew to 4 GiB","resource":"container:qb"}],
	"blast_radius":[{"resource":"app:overseerr","via":"depends_on","depth":1}],
	"suggested":{"id":"container.update","label":"Raise the limit","tier":"T2","target":"container:plex","detail":"Recreates it","impact":[{"resource":"app:plex","via":"runs_on","depth":1}]},
	"alternatives":[{"id":"pihole.pause","label":"Pause blocking","tier":"T1","target":"app:pihole","household":true,"detail":"for 5 minutes"}],
	"resolves_when":"Resolves when it stays up.","resolution":"Restarted."}`

func TestProjectIncident(t *testing.T) {
	only := func(ids ...string) func(string) bool {
		return func(id string) bool {
			for _, x := range ids {
				if x == id {
					return true
				}
			}
			return false
		}
	}
	p, ok := ProjectIncident(json.RawMessage(plexDown), only("app:plex"))
	if !ok {
		t.Fatal("a relevant problem isn't shown")
	}
	var got map[string]any
	if err := json.Unmarshal(p, &got); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]any{"id": "plex-down", "title": "Plex is down", "severity": "down", "status": "open", "target": "app:plex", "summary": "", "resolves_when": ""} {
		if got[k] != want {
			t.Errorf("%s = %v, want %v", k, got[k], want)
		}
	}
	for _, k := range []string{"facts", "resolution", "suggested"} {
		if _, ok := got[k]; ok {
			t.Errorf("projection keeps %s: %v", k, got[k])
		}
	}
	if len(got["causes"].([]any)) != 0 || len(got["blast_radius"].([]any)) != 0 {
		t.Errorf("causes or blast radius kept: %v", got)
	}
	alts := got["alternatives"].([]any)
	if len(alts) != 1 || alts[0].(map[string]any)["id"] != "pihole.pause" || alts[0].(map[string]any)["detail"] != nil {
		t.Errorf("alternatives = %v", alts)
	}
	for _, leak := range []string{"192.0.2.5", "qBittorrent", "jupiter", "Recreates", "overseerr"} {
		if strings.Contains(string(p), leak) {
			t.Errorf("projection leaks %q: %s", leak, p)
		}
	}

	// Relevant through the blast radius; not relevant at all; nil.
	if _, ok := ProjectIncident(json.RawMessage(plexDown), only("app:overseerr")); !ok {
		t.Error("an incident reaching a board's resource isn't shown")
	}
	if _, ok := ProjectIncident(json.RawMessage(plexDown), only("app:sonarr")); ok {
		t.Error("an unrelated incident is shown")
	}
	if _, ok := ProjectIncident(json.RawMessage(plexDown), nil); ok {
		t.Error("shown with no relevance at all")
	}
	// Repairs, suggestions and security findings never are.
	for _, swap := range [][2]string{{`"class":"problem"`, `"class":"repair"`}, {`"class":"problem"`, `"class":"suggestion"`}, {`"class":"problem"`, `"class":"problem","category":"security"`}} {
		raw := strings.Replace(plexDown, swap[0], swap[1], 1)
		if _, ok := ProjectIncident(json.RawMessage(raw), only("app:plex")); ok {
			t.Errorf("%s is shown to the household", swap[1])
		}
	}
}

func TestInboxSourcePerViewer(t *testing.T) {
	s := New(graph.New(), time.Date(2026, 10, 3, 14, 5, 0, 0, time.UTC))
	s.Inbox.Update(func(tx *livedoc.Tx) error {
		tx.Put(Incidents, "plex-down", json.RawMessage(plexDown))
		tx.Put(Incidents, "nas-full", json.RawMessage(strings.ReplaceAll(strings.ReplaceAll(plexDown, "plex-down", "nas-full"), `"target":"app:plex"`, `"target":"unraid:nas"`)))
		return nil
	})
	type who struct{}
	src := s.InboxSource(func(ctx context.Context) InboxView {
		switch ctx.Value(who{}) {
		case "admin":
			return InboxView{Detail: true}
		case "household":
			return InboxView{Relevant: func(id string) bool { return id == "app:plex" }}
		}
		return InboxView{}
	})
	count := func(name string) (int, string) {
		f, err := realtime.OpenWith(context.WithValue(context.Background(), who{}, name), src, Inbox, make(chan struct{}, 1))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		_, doc := f.Snapshot()
		b, _ := json.Marshal(doc)
		return len(doc.(map[string]any)[Incidents].(map[string]json.RawMessage)), string(b)
	}
	if n, _ := count("admin"); n != 2 {
		t.Errorf("admin sees %d", n)
	}
	if n, b := count("household"); n != 1 || strings.Contains(b, "facts") || !strings.Contains(b, `"now"`) {
		t.Errorf("household sees %d: %s", n, b)
	}
	if n, _ := count("stranger"); n != 0 {
		t.Errorf("no viewer sees %d", n)
	}

	// ProjectInbox is the same view for a snapshot (first paint).
	_, doc := s.Inbox.Snapshot()
	if got := ProjectInbox(doc, InboxView{Relevant: func(id string) bool { return id == "unraid:nas" }}); len(got[Incidents].(map[string]json.RawMessage)) != 1 {
		t.Errorf("ProjectInbox = %v", got)
	}
}
