package server

import (
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dash.cha.sh/internal/auth"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/livedoc"
)

// Test automation through the real server: authz lets the routes through
// (Public), the session works on signed-in routes, and the session endpoint
// says it's on.
func TestAutomationThroughTheServer(t *testing.T) {
	dir := t.TempDir()
	e := newEnvWith(t, func(d *Deps) {
		if err := d.Auth.EnableAutomation(dir); err != nil {
			t.Fatal(err)
		}
	})
	signIn(t, e) // the owner automation signs in as
	b, _ := os.ReadFile(filepath.Join(dir, auth.AutomationKeyFile))
	key := strings.TrimSpace(string(b))
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	resp := e.request(t, c, "POST", "/api/auth/automation", nil, map[string]string{auth.AutomationHeader: key, "Origin": "https://dash.example.com"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("sign-in: %d %v", resp.StatusCode, decode(t, resp))
	}
	resp = e.request(t, c, "GET", "/api/auth/session", nil, nil)
	if m := decode(t, resp); resp.StatusCode != http.StatusOK || m["automation"] != true || m["step_up_until"] == nil {
		t.Fatalf("session: %d %v", resp.StatusCode, m)
	}
	resp = e.request(t, c, "POST", "/api/auth/automation/step-up", nil, map[string]string{auth.AutomationHeader: key, "Origin": "https://dash.example.com"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("step-up: %d", resp.StatusCode)
	}
	// A wrong key, through the same route.
	resp = e.request(t, &http.Client{}, "POST", "/api/auth/automation", nil, map[string]string{auth.AutomationHeader: "nope", "Origin": "https://dash.example.com"})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong key: %d", resp.StatusCode)
	}
}

// While test automation is on, the Inbox holds a security finding at
// attention, on the hub.
func TestAutomationFinding(t *testing.T) {
	inbox := livedoc.New("incidents")
	AutomationFinding(inbox, time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC))
	_, doc := inbox.Snapshot()
	coll, _ := doc["incidents"].(map[string]json.RawMessage)
	raw, ok := coll[AutomationFindingID]
	var inc integration.Incident
	if !ok || json.Unmarshal(raw, &inc) != nil {
		t.Fatalf("no finding: %v", doc)
	}
	if inc.Category != integration.CategorySecurity || inc.Severity != integration.SeverityAttention || inc.Status != integration.StatusOpen ||
		inc.Target != integration.HubResource || !strings.Contains(inc.Summary, "CHADASH_TEST_AUTOMATION") {
		t.Fatalf("finding: %#v", raw)
	}
}
