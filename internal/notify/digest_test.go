package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"dash.cha.sh/internal/audit"
	"dash.cha.sh/internal/incidents"
)

// digestEnv: an ntfy channel, the digest on at 07:30 UTC, the clock at
// 07:00 (newEnv starts at 02:00).
func digestEnv(t *testing.T) (*env, *ntfyServer) {
	t.Helper()
	e := newEnv(t)
	e.s.loc = time.UTC
	n := newNtfy(t)
	e.addNtfy(n)
	if _, err := e.s.UpdateRule(t.Context(), RuleDigest, RuleUpdate{Enabled: new(true)}, "u_owner"); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(5 * time.Hour)
	return e, n
}

func TestDigestSaysNothingOnAQuietDay(t *testing.T) {
	e, n := digestEnv(t)
	e.s.SetDigestSources(DigestSources{
		Overnight:    func(context.Context, time.Time, time.Time) ([]DigestIncident, error) { return nil, nil },
		Updates:      func(context.Context) (int, error) { return 0, nil },
		StaleBackups: nil, // the backups track hasn't wired it: nothing
	})
	e.tick(31 * time.Minute) // 07:31
	e.settle()
	if got := n.msgs(); len(got) != 0 {
		t.Fatalf("a quiet day sent %+v", got)
	}
	if v := e.s.Digest(); v.LastSentAt == nil || v.NextAt == nil || !v.NextAt.Equal(time.Date(2026, 10, 7, 7, 30, 0, 0, time.UTC)) {
		t.Fatalf("after a quiet day: %+v", v)
	}

	// All quiet on: tomorrow it says so, once.
	e.s.mu.Lock()
	d := e.s.digest
	e.s.mu.Unlock()
	d.AllQuiet = true
	if err := e.s.saveDigest(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	e.tick(24 * time.Hour)
	e.settle()
	e.tick(time.Minute)
	e.settle()
	got := n.msgs()
	if len(got) != 1 || got[0].Title != "All quiet" || !strings.Contains(got[0].Message, "nothing is open") || got[0].Click != "https://dash.lab.example/settings/timeline" {
		t.Fatalf("all quiet = %+v", got)
	}
}

func TestDigestSaysWhatHappened(t *testing.T) {
	e, n := digestEnv(t)
	var since time.Time
	e.s.SetDigestSources(DigestSources{
		Overnight: func(_ context.Context, from, _ time.Time) ([]DigestIncident, error) {
			since = from
			return []DigestIncident{{ID: "pbs", Title: "PBS datastore is filling up", Severity: "attention", Resolved: true}, {ID: "owl", Title: "owl's backup failed", Severity: "attention"}}, nil
		},
		Updates:      func(context.Context) (int, error) { return 3, nil },
		StaleBackups: func(context.Context) ([]string, error) { return []string{"homeassistant"}, nil },
	})
	e.put(inc("owl", "problem", "attention", "open", "open", withTitle("owl's backup failed")),
		inc("hint", "problem", "info", "open", "open"), // a hint: not named
		inc("merge", "suggestion", "info", "open", "open"))
	e.tick(30 * time.Minute) // 07:30 exactly
	e.settle()
	got := n.msgs()
	if len(got) != 1 {
		t.Fatalf("got %d messages: %+v", len(got), got)
	}
	m := got[0]
	for _, want := range []string{"Opened since Mon 07:30: 2, 1 of them resolved.", "· PBS datastore is filling up", "Open now: 1.", "Updates waiting: 3.", "Backups stale: homeassistant."} {
		if !strings.Contains(m.Message, want) {
			t.Fatalf("digest lacks %q:\n%s", want, m.Message)
		}
	}
	if m.Title != "Morning digest" || m.Priority != ntfyPriority("attention") {
		t.Fatalf("digest = %+v", m)
	}
	if !since.Equal(time.Date(2026, 10, 5, 7, 30, 0, 0, time.UTC)) {
		t.Fatalf("first digest looks back from %v, want a day", since)
	}
	digests := func() int {
		c := 0
		for _, m := range n.msgs() {
			if m.Title == "Morning digest" {
				c++
			}
		}
		return c
	}
	// Once a day (owl's own attention notice comes on its own).
	e.tick(time.Hour)
	e.settle()
	if digests() != 1 {
		t.Fatal("sent twice in a day")
	}
	// The hub was down at the time, more than two hours: it waits for tomorrow.
	e.tick(24*time.Hour + time.Hour)
	e.settle()
	if digests() != 1 {
		t.Fatal("sent three hours late")
	}
}

func TestDigestOffByDefault(t *testing.T) {
	e := newEnv(t)
	n := newNtfy(t)
	e.addNtfy(n)
	e.s.loc = time.UTC
	e.put(inc("owl", "problem", "attention", "open", "open"))
	e.tick(5*time.Hour + 31*time.Minute)
	e.settle()
	for _, m := range n.msgs() {
		if m.Title == "Morning digest" {
			t.Fatal("the digest went out while its rule is off")
		}
	}
	if v := e.s.Digest(); v.Enabled || v.Time != DefaultDigestTime || v.NextAt != nil {
		t.Fatalf("defaults = %+v", v)
	}
}

func TestDigestRoutes(t *testing.T) {
	e := newEnv(t)
	mux := http.NewServeMux()
	e.s.Register(mux, func(h http.Handler) http.Handler { return h })
	call := func(method, body string) *httptest.ResponseRecorder {
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		r := httptest.NewRequest(method, "/api/notify/digest", rd)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	if w := call("GET", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"time":"07:30"`) || !strings.Contains(w.Body.String(), `"all_quiet":false`) {
		t.Fatalf("GET = %d %s", w.Code, w.Body)
	}
	for _, bad := range []string{`{"time":"7:30"}`, `{"time":"24:00"}`, `{"time":"07:30 pm"}`} {
		if w := call("PUT", bad); w.Code != 422 || !strings.Contains(w.Body.String(), `"id":"digest.bad_time"`) {
			t.Fatalf("PUT %s = %d %s", bad, w.Code, w.Body)
		}
	}
	if w := call("PUT", `{}`); w.Code != 422 {
		t.Fatalf("empty PUT = %d", w.Code)
	}
	w := call("PUT", `{"time":"06:45","all_quiet":true}`)
	var v DigestView
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &v) != nil || v.Time != "06:45" || !v.AllQuiet {
		t.Fatalf("PUT = %d %s", w.Code, w.Body)
	}
	page, err := e.audit.Query(t.Context(), audit.Filter{Action: "settings.notify.digest.update"})
	if err != nil || len(page.Events) != 1 || !strings.Contains(string(page.Events[0].Params), `"time_to":"06:45"`) {
		t.Fatalf("audit = %+v, %v", page.Events, err)
	}
	// Kept for the next start.
	e.start()
	if got := e.s.Digest(); got.Time != "06:45" || !got.AllQuiet {
		t.Fatalf("after a restart: %+v", got)
	}
}

// An incident that opens inside a maintenance window doesn't notify while
// the window lasts; still open after it, it notifies then.
func TestExpectedIncidentsWait(t *testing.T) {
	e := newEnv(t)
	n := newNtfy(t)
	e.addNtfy(n)
	until := e.clock.Now().Add(time.Hour)
	exp := &incidents.Expectations{}
	exp.Set(incidents.ExpecterFunc(func(resource string, at time.Time) (string, bool) {
		return "mw_1", resource == "app:sonarr" && at.Before(until)
	}))
	e.s.det.expected = func(target string, at time.Time) bool { _, ok := exp.Expected(target, at); return ok }

	e.put(inc("sonarr", "problem", "down", "open", "open"))
	e.settle()
	e.tick(30 * time.Minute)
	e.settle()
	if len(n.msgs()) != 0 {
		t.Fatalf("an expected incident notified: %+v", n.msgs())
	}
	e.tick(31 * time.Minute) // the window has ended
	e.settle()
	if got := n.msgs(); len(got) != 1 || got[0].Title != "sonarr is broken" {
		t.Fatalf("after the window = %+v", got)
	}
}
