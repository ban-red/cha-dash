package demo

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"dash.cha.sh/internal/causes"
	"dash.cha.sh/internal/topics"
)

// runDay drives the Demo Home from 13:30 to the all-open moment, 14:05,
// with the cause engine passing every 10 simulated seconds as the hub runs
// it, so it sees the moments a story passes through (qBittorrent's OOM
// before Docker restarts it).
func runDay(t *testing.T) *topics.Set {
	t.Helper()
	d, tp, clock := newDriver(t, Options{Seed: 1, At: 13*time.Hour + 30*time.Minute})
	s := causes.New(causes.Options{Topics: tp, Now: d.Now, Clock: d.Clock})
	for !d.Now().After(time.Date(2026, 10, 3, 14, 5, 0, 0, time.UTC)) {
		d.Step()
		if err := s.Sync(t.Context()); err != nil {
			t.Fatal(err)
		}
		clock.Advance(10 * time.Second)
	}
	return tp
}

// The incident suite: the engine runs over the Demo Home at 14:05, when
// every story is open, and finds the causes the stories script, first
// cause first, and nothing else.
func TestDemoHomeCauses(t *testing.T) {
	tp := runDay(t)
	qbit := "qBittorrent's container was OOM-killed at 14:03."
	want := map[string]struct{ resource, rule, text string }{
		// The demo moment, and the app itself.
		"app:sonarr":      {"docker:magpie/qbittorrent", causes.RuleOOMKilled, qbit + " Sonarr depends on qBittorrent."},
		"app:radarr":      {"docker:magpie/qbittorrent", causes.RuleOOMKilled, qbit + " Radarr depends on qBittorrent."},
		"app:qbittorrent": {"docker:magpie/qbittorrent", causes.RuleOOMKilled, qbit},
		// The update that left Nextcloud in maintenance mode.
		"app:nextcloud": {"docker:rook/nextcloud", causes.RuleStarted, "Nextcloud's container started at 07:10."},
		// stork's memory, for the LXC that runs on it.
		"pve:stork/lxc/220": {"pve:stork", causes.RuleIncident, "stork has a problem: stork is running out of memory. files runs on stork."},
	}
	_, g := tp.Graph.Snapshot(nil)
	for id := range g.Resources {
		got := tp.Causes.Of(string(id))
		w, ok := want[string(id)]
		switch {
		case !ok && len(got) > 0:
			t.Errorf("%s: unexpected causes: %s", id, got[0].Text)
		case ok && len(got) == 0:
			t.Errorf("%s: no causes", id)
		case ok && (got[0].Resource != w.resource || got[0].Rule != w.rule || got[0].Text != w.text):
			t.Errorf("%s: first cause %s %s %q, want %s %s %q", id, got[0].Resource, got[0].Rule, got[0].Text, w.resource, w.rule, w.text)
		}
	}
	// The scripted incidents already name what the engine finds for their
	// targets, so none gains a cause twice.
	_, inbox := tp.Inbox.Snapshot()
	for id := range inbox[topics.Incidents].(map[string]json.RawMessage) {
		var named []string
		for _, c := range incident(t, tp, id).Causes {
			if slices.Contains(named, c.Resource+c.Text) {
				t.Errorf("%s: cause twice: %s", id, c.Text)
			}
			named = append(named, c.Resource+c.Text)
			if strings.HasSuffix(c.Text, "depends on qBittorrent.") {
				t.Errorf("%s gained Sonarr's cause", id)
			}
		}
	}
}
