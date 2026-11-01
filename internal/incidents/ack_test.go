package incidents

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"dash.cha.sh/internal/audit"
	"dash.cha.sh/internal/authz"
	"dash.cha.sh/internal/livedoc"
)

func (e *env) acks() *Acks {
	return NewAcks(AcksOptions{Store: e.s, Inbox: e.inbox, Now: e.clock.Now})
}

// ackOf is the inbox item's acknowledgement, or nil.
func (e *env) ackOf(id string) map[string]any {
	e.t.Helper()
	var m map[string]any
	if err := json.Unmarshal(e.inbox.Item("incidents", id), &m); err != nil {
		e.t.Fatal(err)
	}
	a, _ := m["acknowledged"].(map[string]any)
	return a
}

// TestAcknowledge: an open problem takes an acknowledgement with who, when
// and a note; it survives its writer replacing the item; it's on the
// timeline; un-acknowledging and resolving clear it.
func TestAcknowledge(t *testing.T) {
	e := newEnv(t)
	a := e.acks()
	e.publish("sonarr.down", nil)
	e.step()

	ack, err := a.Acknowledge(t.Context(), "sonarr.down", "sam", "  On it:\nthe disk is‮ full. ")
	if err != nil {
		t.Fatal(err)
	}
	if ack.By != "sam" || ack.Note != "On it: the disk is full." || !ack.At.Equal(e.clock.t) {
		t.Fatalf("ack %+v", ack)
	}
	if got := e.ackOf("sonarr.down"); got["by"] != "sam" || got["note"] != "On it: the disk is full." {
		t.Fatalf("inbox item's ack %v", got)
	}
	if _, err := a.Acknowledge(t.Context(), "sonarr.down", "alex", ""); !errors.Is(err, ErrAcknowledged) {
		t.Fatalf("twice: %v", err)
	}

	// Its writer replaces the item whole (it doesn't know the field): kept.
	e.clock.t = e.clock.t.Add(time.Minute)
	e.publish("sonarr.down", map[string]any{"severity": "down"})
	if got := e.ackOf("sonarr.down"); got["by"] != "sam" {
		t.Fatalf("after a rewrite: %v", got)
	}
	if got, ok := a.Acked("sonarr.down"); !ok || got.By != "sam" {
		t.Fatalf("Acked: %+v %v", got, ok)
	}

	// Taking it back.
	was, err := a.Unacknowledge(t.Context(), "sonarr.down", "alex")
	if err != nil || was.By != "sam" {
		t.Fatalf("unack: %+v %v", was, err)
	}
	if got := e.ackOf("sonarr.down"); got != nil {
		t.Fatalf("still acknowledged: %v", got)
	}
	if _, err := a.Unacknowledge(t.Context(), "sonarr.down", "alex"); !errors.Is(err, ErrNotAcknowledged) {
		t.Fatalf("twice: %v", err)
	}
	evs, _ := e.s.Events(t.Context(), Query{Incident: "sonarr.down", Kinds: []EventKind{Acknowledged}})
	if len(evs) != 2 {
		t.Fatalf("events %+v", evs)
	}
	if d := DataOf(evs[0]); d.Who != "sam" || d.Note != "On it: the disk is full." || d.Cleared || d.Target != "container:sonarr" || d.Title == "" {
		t.Fatalf("acknowledged %+v", d)
	}
	if d := DataOf(evs[1]); d.Who != "alex" || !d.Cleared {
		t.Fatalf("cleared %+v", d)
	}

	// Resolution clears it, with no event of its own.
	if _, err := a.Acknowledge(t.Context(), "sonarr.down", "sam", ""); err != nil {
		t.Fatal(err)
	}
	e.publish("sonarr.down", map[string]any{"status": "resolved", "resolved_by": "auto"})
	if got := e.ackOf("sonarr.down"); got != nil {
		t.Fatalf("resolved but acknowledged: %v", got)
	}
	if _, err := a.Acknowledge(t.Context(), "sonarr.down", "sam", ""); !errors.Is(err, ErrResolved) {
		t.Fatalf("resolved: %v", err)
	}
	// Reopened: a new opening, not acknowledged.
	e.publish("sonarr.down", nil)
	if got := e.ackOf("sonarr.down"); got != nil {
		t.Fatalf("reopened acknowledged: %v", got)
	}

	// Only problems, only what's there.
	e.publish("plex.repair", map[string]any{"class": "repair"})
	if _, err := a.Acknowledge(t.Context(), "plex.repair", "sam", ""); !errors.Is(err, ErrNotProblem) {
		t.Fatalf("repair: %v", err)
	}
	if _, err := a.Acknowledge(t.Context(), "nope", "sam", ""); !errors.Is(err, ErrNoIncident) {
		t.Fatalf("missing: %v", err)
	}
}

// TestAckSurvivesRestart: after a restart the inbox is empty until its
// writers publish again; the recorder reads the acknowledgement back from
// the timeline and puts it on the incident when it reappears, unless it
// resolved or was cleared meanwhile.
func TestAckSurvivesRestart(t *testing.T) {
	e := newEnv(t)
	a := e.acks()
	e.rec = NewRecorder(RecorderOptions{Store: e.s, Inbox: e.inbox, Acks: a, Now: e.clock.Now})
	e.publish("sonarr.down", nil)
	e.publish("radarr.down", map[string]any{"target": "container:radarr"})
	e.step()
	if _, err := a.Acknowledge(t.Context(), "sonarr.down", "sam", "on it"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Acknowledge(t.Context(), "radarr.down", "sam", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Unacknowledge(t.Context(), "radarr.down", "sam"); err != nil {
		t.Fatal(err)
	}

	// The hub restarts: a new inbox, new Acks, a new recorder.
	e.inbox = livedoc.New("incidents")
	a = e.acks()
	e.rec = NewRecorder(RecorderOptions{Store: e.s, Inbox: e.inbox, Acks: a, Now: e.clock.Now})
	e.step()
	e.clock.t = e.clock.t.Add(time.Minute)
	e.publish("sonarr.down", nil)
	e.publish("radarr.down", map[string]any{"target": "container:radarr"})
	e.step()
	if got := e.ackOf("sonarr.down"); got["by"] != "sam" || got["note"] != "on it" {
		t.Fatalf("not restored: %v", got)
	}
	if got := e.ackOf("radarr.down"); got != nil {
		t.Fatalf("a cleared acknowledgement came back: %v", got)
	}
	// Restoring writes no new event.
	if evs, _ := e.s.Events(t.Context(), Query{Incident: "sonarr.down", Kinds: []EventKind{Acknowledged}}); len(evs) != 1 {
		t.Fatalf("events %d", len(evs))
	}
}

// TestKeepAck: the inbox's Keep function on its own.
func TestKeepAck(t *testing.T) {
	ack := `"acknowledged":{"by":"sam","at":"2026-10-06T02:00:00Z"}`
	open := `{"class":"problem","status":"open","title":"x"}`
	acked := `{` + ack + `,"class":"problem","status":"open","title":"x"}`
	for _, c := range []struct{ name, cur, next, want string }{
		{"nothing to keep", open, `{"class":"problem","status":"open","title":"y"}`, `{"class":"problem","status":"open","title":"y"}`},
		{"kept", acked, `{"class":"problem","status":"open","title":"y"}`, `{` + ack + `,"class":"problem","status":"open","title":"y"}`},
		{"cleared", acked, `{"acknowledged":null,"class":"problem","status":"open"}`, `{"class":"problem","status":"open"}`},
		{"resolved", acked, `{"class":"problem","status":"resolved"}`, `{"class":"problem","status":"resolved"}`},
		{"set on a resolved one", open, `{` + ack + `,"class":"problem","status":"resolved"}`, `{"class":"problem","status":"resolved"}`},
		{"new item", "", `{"class":"problem","status":"open"}`, `{"class":"problem","status":"open"}`},
		{"not a problem", `{` + ack + `,"class":"approval","status":"open"}`, `{"class":"approval","status":"open"}`, `{"class":"approval","status":"open"}`},
	} {
		var cur json.RawMessage
		if c.cur != "" {
			cur = json.RawMessage(c.cur)
		}
		if got := string(keepAck(cur, json.RawMessage(c.next))); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

// TestAckRoutes: both routes through a mux, with their refusals and their
// audit records.
func TestAckRoutes(t *testing.T) {
	e := newEnv(t)
	a := e.acks()
	log := audit.New(e.st)
	a.Audit = &authz.Recorder{Log: log}
	a.whoFor = func(*http.Request) (string, bool) { return "sam", true }
	mux := http.NewServeMux()
	a.Register(mux, func(h http.Handler) http.Handler { return h })
	do := func(method, id, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, "/api/inbox/"+url.PathEscape(id)+"/acknowledge", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		mux.ServeHTTP(w, r)
		return w
	}
	e.publish("docker:i_x/c/sonarr.down", nil)

	if w := do("POST", "nope", `{}`); w.Code != 404 || !strings.Contains(w.Body.String(), `"id":"incidents.no_incident"`) {
		t.Fatalf("missing %d %s", w.Code, w.Body)
	}
	if w := do("POST", "docker:i_x/c/sonarr.down", `{"note":"`+strings.Repeat("x", MaxNote+1)+`"}`); w.Code != 422 {
		t.Fatalf("long note %d %s", w.Code, w.Body)
	}
	w := do("POST", "docker:i_x/c/sonarr.down", `{"note":"on it"}`)
	var resp AckResponse
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &resp) != nil || resp.Acknowledged == nil || resp.Acknowledged.By != "sam" || resp.Incident != "docker:i_x/c/sonarr.down" {
		t.Fatalf("acknowledge %d %s", w.Code, w.Body)
	}
	if w := do("POST", "docker:i_x/c/sonarr.down", `{}`); w.Code != 409 || !strings.Contains(w.Body.String(), `"code":"already_acknowledged"`) {
		t.Fatalf("twice %d %s", w.Code, w.Body)
	}
	if w := do("DELETE", "docker:i_x/c/sonarr.down", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"acknowledged":null`) {
		t.Fatalf("unacknowledge %d %s", w.Code, w.Body)
	}
	if w := do("DELETE", "docker:i_x/c/sonarr.down", ""); w.Code != 409 || !strings.Contains(w.Body.String(), `"code":"not_acknowledged"`) {
		t.Fatalf("unacknowledge twice %d %s", w.Code, w.Body)
	}

	page, err := log.Query(t.Context(), audit.Filter{Action: "incident."})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, ev := range page.Events {
		got = append(got, ev.Action+" "+ev.Target+" "+string(ev.Params))
	}
	if len(got) != 2 || !strings.HasPrefix(got[0], "incident.unacknowledge container:sonarr") || !strings.Contains(got[0], `"was_by":"sam"`) ||
		!strings.HasPrefix(got[1], "incident.acknowledge container:sonarr") || !strings.Contains(got[1], `"note":"on it"`) {
		t.Fatalf("audit %q", got)
	}
}
