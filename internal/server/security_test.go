package server

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"dash.cha.sh/internal/demo"
	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/security"
	"dash.cha.sh/internal/store"
	"dash.cha.sh/internal/topics"
)

// TestSecurityRoutes: the posture's API is the owner's to change, refuses
// unknown levels, and a change reaches the inbox before the PUT returns.
func TestSecurityRoutes(t *testing.T) {
	wall := time.Now()
	tp := topics.New(graph.New(), wall)
	var sec *security.Service
	var st *store.Store
	e := newEnvWith(t, func(d *Deps) {
		st = d.Store
		var err error
		if sec, err = security.New(t.Context(), d.Store, d.Log); err != nil {
			t.Fatal(err)
		}
		drv, err := demo.New(tp, demo.Options{Seed: 1, At: demo.DefaultAt, Clock: func() time.Time { return wall }, Posture: sec.Posture()})
		if err != nil {
			t.Fatal(err)
		}
		sec.OnChange(drv.SetPosture)
		d.Security = sec
	})
	severity := func() string {
		var in struct{ Severity, Category string }
		if err := json.Unmarshal(tp.Inbox.Item(topics.Incidents, "docker-magpie.hint.docker_endpoint"), &in); err != nil {
			t.Fatal(err)
		}
		if in.Category != "security" {
			t.Fatalf("category %q", in.Category)
		}
		return in.Severity
	}

	anon := &http.Client{}
	for _, m := range []string{"GET", "PUT"} {
		if resp := e.request(t, anon, m, "/api/settings/security", map[string]string{"posture": "strict"}, nil); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s without a session: %d", m, resp.StatusCode)
		}
	}
	c := signIn(t, e)
	got := decode(t, e.request(t, c, "GET", "/api/settings/security", nil, nil))
	levels, _ := got["levels"].([]any)
	if got["posture"] != "relaxed" || len(levels) != 3 {
		t.Fatalf("GET = %v", got)
	}
	first := levels[0].(map[string]any)
	if first["id"] != "relaxed" || first["label"] != "Relaxed · LAN-only home network" || first["description"] == "" || first["default"] != true {
		t.Fatalf("first level = %v", first)
	}
	if severity() != "info" {
		t.Fatal("a hint isn't info under relaxed")
	}

	resp := e.request(t, c, "PUT", "/api/settings/security", map[string]string{"posture": "paranoid"}, nil)
	if resp.StatusCode != http.StatusUnprocessableEntity || decode(t, resp)["error"].(map[string]any)["code"] != "invalid" {
		t.Fatalf("unknown level: %d", resp.StatusCode)
	}

	if got := decode(t, e.request(t, c, "PUT", "/api/settings/security", map[string]string{"posture": "strict"}, nil)); got["posture"] != "strict" {
		t.Fatalf("PUT = %v", got)
	}
	if severity() != "attention" {
		t.Fatal("strict didn't reach the inbox before the PUT returned")
	}
	if got := decode(t, e.request(t, c, "GET", "/api/settings/security", nil, nil)); got["posture"] != "strict" {
		t.Fatalf("GET after PUT = %v", got)
	}
	// It's a settings row: a restart reads it back.
	if again, err := security.New(t.Context(), st, nil); err != nil {
		t.Fatal(err)
	} else if again.Posture() != security.Strict {
		t.Fatalf("reloaded: %v", again.Posture())
	}

	// Only the owner changes it; other roles may read it.
	if _, err := st.Writer().ExecContext(t.Context(), `UPDATE users SET role = 'viewer'`); err != nil {
		t.Fatal(err)
	}
	if resp := e.request(t, c, "PUT", "/api/settings/security", map[string]string{"posture": "relaxed"}, nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a viewer's PUT: %d", resp.StatusCode)
	}
	if resp := e.request(t, c, "GET", "/api/settings/security", nil, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("a viewer's GET: %d", resp.StatusCode)
	}
	if _, err := st.Writer().ExecContext(t.Context(), `UPDATE users SET role = 'owner'`); err != nil {
		t.Fatal(err)
	}
	if severity() != "attention" {
		t.Fatal("a refused PUT changed the posture")
	}

	// Changes from another origin are refused like every other change.
	if resp := e.request(t, c, "PUT", "/api/settings/security", map[string]string{"posture": "relaxed"}, map[string]string{"Origin": "https://evil.example", "Sec-Fetch-Site": "cross-site"}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin PUT: %d", resp.StatusCode)
	}
}
