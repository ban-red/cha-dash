package server

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"dash.cha.sh/internal/auth"
	"dash.cha.sh/internal/livedoc"
	"dash.cha.sh/internal/topics"
)

// TestAcknowledgeThroughTheRouter: operators and up acknowledge an open
// problem; viewers and household are refused by the route table; the
// Inbox shows who; an agent reading incidents sees only that someone is
// on it; agents can't acknowledge (they reach nothing but POST /mcp, and
// MCP has no such tool).
func TestAcknowledgeThroughTheRouter(t *testing.T) {
	e, st, tp := fullEnvTopics(t)
	labGraph(t, tp)
	owner := signIn(t, e)
	_, operator := addUser(t, e, owner, "ack-operator", auth.RoleOperator)
	_, viewer := addUser(t, e, owner, "ack-viewer", auth.RoleViewer)
	_, household := addUser(t, e, owner, "ack-home", auth.RoleHousehold)
	const id = "docker:i_x/c/sonarr.down"
	in := map[string]any{"id": id, "class": "problem", "scenario": "check-failed", "severity": "down", "status": "open", "phase": "open",
		"title": "Sonarr isn't answering", "summary": "", "target": "thing:1", "opened_at": "2026-11-01T14:00:00Z", "causes": []any{}, "blast_radius": []any{}}
	if _, err := tp.Inbox.Update(func(tx *livedoc.Tx) error { tx.Put(topics.Incidents, id, in); return nil }); err != nil {
		t.Fatal(err)
	}
	path := "/api/inbox/" + url.PathEscape(id) + "/acknowledge"

	for name, c := range map[string]*http.Client{"viewer": viewer, "household": household} {
		if status, code, _ := e.call(t, c, "POST", path, map[string]string{}); status != http.StatusForbidden || code != "forbidden" {
			t.Fatalf("%s acknowledged: %d %s", name, status, code)
		}
	}
	if status, _, b := e.call(t, operator, "POST", path, map[string]string{"note": "on it"}); status != http.StatusOK {
		t.Fatalf("operator: %d %s", status, b)
	}
	status, _, b := e.call(t, owner, "GET", "/api/inbox", nil)
	var inbox struct {
		Incidents map[string]struct {
			Acknowledged *struct{ By, Note string } `json:"acknowledged"`
		} `json:"incidents"`
	}
	if status != http.StatusOK || json.Unmarshal(b, &inbox) != nil || inbox.Incidents[id].Acknowledged == nil ||
		inbox.Incidents[id].Acknowledged.By != "ack-operator" || inbox.Incidents[id].Acknowledged.Note != "on it" {
		t.Fatalf("inbox: %d %s", status, b)
	}
	if ev, ok := lastAudit(t, st, "incident.acknowledge"); !ok || ev.Actor.Name != "ack-operator" || ev.Target != "thing:1" {
		t.Fatalf("audit: %+v", ev)
	}

	// An agent reading incidents: acknowledged, not by whom or why.
	agentsOn(t, e, owner, true)
	_, secret := mintAgent(t, e, owner, "assistant", "graph.read", "inbox.read")
	agent := withToken(nil, secret)
	_, r, raw := mcpCall(t, e, agent, "resources/read", map[string]any{"uri": "chadash://incidents"})
	if r.Error != nil || !strings.Contains(raw, `\"acknowledged\":true`) || strings.Contains(raw, "ack-operator") || strings.Contains(raw, "on it") {
		t.Fatalf("incidents through MCP: %s", raw)
	}
	if status, code, _ := e.call(t, agent, "DELETE", path, nil); status != http.StatusForbidden || code != "agent_mcp_only" {
		t.Fatalf("an agent took it back: %d %s", status, code)
	}

	if status, _, b := e.call(t, owner, "DELETE", path, nil); status != http.StatusOK {
		t.Fatalf("owner takes it back: %d %s", status, b)
	}
	if ev, ok := lastAudit(t, st, "incident.unacknowledge"); !ok || ev.Actor.Name == "" {
		t.Fatalf("audit: %+v", ev)
	}
}
