package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"dash.cha.sh/internal/audit"
	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/incidents"
	"dash.cha.sh/internal/store"
	"dash.cha.sh/internal/timeline"
	"dash.cha.sh/internal/topics"
)

// GET /api/timeline: the route, its range checks, and each role's view.
func TestTimelineRoute(t *testing.T) {
	g := graph.New()
	if _, err := g.Update(func(tx *graph.Tx) error {
		if err := tx.Upsert(graph.Resource{ID: "host:stork", Kind: "host", Name: "stork", Health: graph.HealthOK}); err != nil {
			return err
		}
		return tx.Upsert(graph.Resource{ID: "container:sonarr", Kind: "container", Name: "sonarr", Health: graph.HealthOK})
	}); err != nil {
		t.Fatal(err)
	}
	var inc *incidents.Store
	e := newEnvWith(t, func(d *Deps) {
		inc = incidents.NewStore(d.Store)
		d.Topics, d.Audit, d.Incidents = topics.New(g, time.Now()), audit.New(d.Store), inc
	})
	owner := signIn(t, e)
	_, viewer := addUser(t, e, owner, "gran", "viewer")
	_, home := addUser(t, e, owner, "kid", "household")

	now := time.Now().UTC()
	ev := func(id string, ago time.Duration, kind incidents.EventKind, d incidents.Data) incidents.Event {
		raw, _ := json.Marshal(d)
		return incidents.Event{IncidentID: id, At: now.Add(-ago), Kind: kind, Data: raw}
	}
	sonarr := incidents.Data{Target: "container:sonarr", Class: "problem", Title: "Sonarr isn't answering", Severity: "down"}
	cause := sonarr
	cause.Resource, cause.Text = "host:stork", "stork is out of memory"
	secret := incidents.Data{Target: "container:sonarr", Class: "problem", Category: "security", Title: "Sonarr's API key is in a label", Severity: "info"}
	if err := inc.Append(t.Context(),
		ev("sonarr.down", 3*time.Hour, incidents.Opened, sonarr),
		ev("sonarr.down", 3*time.Hour, incidents.Caused, cause),
		ev("sonarr.down", 2*time.Hour, incidents.Resolved, sonarr),
		ev("sonarr.secret", time.Hour, incidents.Opened, secret),
		ev("old", 10*24*time.Hour, incidents.Opened, sonarr),
	); err != nil {
		t.Fatal(err)
	}

	get := func(c *http.Client, query string) timeline.Timeline {
		t.Helper()
		status, _, b := e.call(t, c, "GET", "/api/timeline"+query, nil)
		if status != http.StatusOK {
			t.Fatalf("GET /api/timeline%s: %d %s", query, status, b)
		}
		var tl timeline.Timeline
		if err := json.Unmarshal(b, &tl); err != nil {
			t.Fatal(err)
		}
		return tl
	}
	texts := func(tl timeline.Timeline) string {
		var out []string
		for _, en := range tl.Entries {
			out = append(out, en.Text)
		}
		return strings.Join(out, " | ")
	}

	// The week by default; viewer and up see everything.
	for _, c := range []*http.Client{owner, viewer} {
		tl := get(c, "?resource=container:sonarr")
		if got := texts(tl); got != "Sonarr isn't answering opened | Sonarr isn't answering: stork is out of memory | Sonarr isn't answering resolved | Sonarr's API key is in a label opened" {
			t.Fatalf("viewer's timeline = %q", got)
		}
		if tl.History != "off" || tl.Resource != "container:sonarr" {
			t.Fatalf("timeline = %+v", tl)
		}
	}
	// Household: no causes, no security findings.
	if got := texts(get(home, "")); got != "Sonarr isn't answering opened | Sonarr isn't answering resolved" {
		t.Fatalf("household's timeline = %q", got)
	}
	// A range that reaches the old one.
	from := now.Add(-11 * 24 * time.Hour).UnixMilli()
	if tl := get(owner, fmt.Sprintf("?from=%d", from)); len(tl.Entries) != 5 {
		t.Fatalf("eleven days = %d entries", len(tl.Entries))
	}
	for _, q := range []string{
		fmt.Sprintf("?from=%d&to=%d", now.UnixMilli(), now.Add(-time.Hour).UnixMilli()),
		fmt.Sprintf("?from=%d", now.Add(-100*24*time.Hour).UnixMilli()),
		"?from=yesterday",
		"?resource=" + strings.Repeat("x", 513),
	} {
		status, _, b := e.call(t, owner, "GET", "/api/timeline"+q, nil)
		if status != http.StatusBadRequest || !strings.Contains(string(b), `"id":"timeline.`) {
			t.Fatalf("GET %s: %d %s", q[:min(len(q), 40)], status, b)
		}
	}
}

// Incidents' retention is set in Settings → History, also while history
// itself is locked.
func TestIncidentRetentionSetting(t *testing.T) {
	var inc *incidents.Store
	var st *store.Store
	e := newEnvWith(t, func(d *Deps) {
		inc, st = incidents.NewStore(d.Store), d.Store
		d.Topics, d.Incidents, d.Audit = topics.New(graph.New(), time.Now()), inc, audit.New(d.Store)
	})
	owner := signIn(t, e)
	status, _, b := e.call(t, owner, "GET", "/api/settings/history", nil)
	if status != http.StatusOK || !strings.Contains(string(b), `"incident_keep_ms":7776000000`) || !strings.Contains(string(b), `"incident_choices_ms":[604800000,`) {
		t.Fatalf("GET: %d %s", status, b)
	}
	thirty := int64(30 * 24 * time.Hour / time.Millisecond)
	if status, _, b := e.call(t, owner, "PUT", "/api/settings/history", map[string]any{"incident_keep_ms": thirty}); status != http.StatusOK || !strings.Contains(string(b), fmt.Sprintf(`"incident_keep_ms":%d`, thirty)) {
		t.Fatalf("PUT 30 days: %d %s", status, b)
	}
	if inc.Keep() != 30*24*time.Hour {
		t.Fatalf("keep = %v", inc.Keep())
	}
	if status, _, b := e.call(t, owner, "PUT", "/api/settings/history", map[string]any{"incident_keep_ms": 12345}); status != http.StatusUnprocessableEntity || !strings.Contains(string(b), "bad_incident_keep") {
		t.Fatalf("PUT a bad value: %d %s", status, b)
	}
	if status, _, b := e.call(t, owner, "PUT", "/api/settings/history", map[string]any{"incident_keep_ms": thirty, "level": "standard"}); status != http.StatusConflict {
		t.Fatalf("PUT with a locked part: %d %s", status, b)
	}
	ev, ok := lastAudit(t, st, "settings.history.update")
	if !ok || !strings.Contains(string(ev.Params), `"incident_keep_to":2592000000`) {
		t.Fatalf("audit = %+v", ev)
	}
}
