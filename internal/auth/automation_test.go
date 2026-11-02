package auth

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dash.cha.sh/internal/httpx"
)

// automationHarness is h with test automation on: a fresh service on h's
// database, its key written to a temp directory.
func automationHarness(t *testing.T, h *harness) (key string) {
	t.Helper()
	svc, err := New(t.Context(), h.st, slog.New(slog.NewTextHandler(h.logs, nil)), httpx.NewProxies(nil))
	if err != nil {
		t.Fatal(err)
	}
	svc.params = testParams
	svc.now = h.clock.now
	dir := t.TempDir()
	if err := svc.EnableAutomation(dir); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, AutomationKeyFile))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("key file: %v %v", info, err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, AutomationKeyFile))
	h.svc = svc
	mux := http.NewServeMux()
	svc.Register(mux)
	h.srv.Close()
	h.srv = httptest.NewServer(mux)
	t.Cleanup(h.srv.Close)
	return strings.TrimSpace(string(b))
}

func TestAutomationSignsInSteppedUp(t *testing.T) {
	h := newHarness(t)
	h.owner()
	key := automationHarness(t, h)

	// A wrong or missing key is refused.
	for _, k := range []string{"", "AAAAAAAAAAAAAAAAAAAAAAAAAA"} {
		c := h.client()
		resp, _ := h.doRaw(c, "POST", "/api/auth/automation", nil, map[string]string{AutomationHeader: k})
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("key %q: %d", k, resp.StatusCode)
		}
	}
	c := h.client()
	resp, raw := h.doRaw(c, "POST", "/api/auth/automation", nil, map[string]string{AutomationHeader: key})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("sign-in = %d %s", resp.StatusCode, raw)
	}
	var got struct {
		User        User   `json:"user"`
		StepUpUntil string `json:"step_up_until"`
	}
	_ = json.Unmarshal(raw, &got)
	if got.User.Role != RoleOwner || got.StepUpUntil == "" {
		t.Fatalf("answer %s", raw)
	}
	var method, stepUp sql.NullString
	if err := h.st.Reader().QueryRow(`SELECT method, step_up_method FROM sessions WHERE user_id = ? ORDER BY created_at DESC LIMIT 1`, got.User.ID).Scan(&method, &stepUp); err != nil {
		t.Fatal(err)
	}
	if method.String != MethodAutomation || stepUp.String != StepUpMethodAutomation {
		t.Fatalf("session method %q, step-up %q", method.String, stepUp.String)
	}
	// The session endpoint says it's on, for the web's banner.
	if resp, body := h.do(c, "GET", "/api/auth/session", nil); resp.StatusCode != http.StatusOK || body["automation"] != true {
		t.Fatalf("session = %d %v", resp.StatusCode, body)
	}
	// The key steps up again the session it started.
	if resp, raw := h.doRaw(c, "POST", "/api/auth/automation/step-up", nil, map[string]string{AutomationHeader: key}); resp.StatusCode != http.StatusOK {
		t.Fatalf("step-up = %d %s", resp.StatusCode, raw)
	}
}

func TestAutomationStepsUpOnlyItsOwnSessions(t *testing.T) {
	h := newHarness(t)
	person := h.owner()
	key := automationHarness(t, h)
	// The person's cookie is still good on the new service (same database).
	if resp, raw := h.doRaw(person, "POST", "/api/auth/automation/step-up", nil, map[string]string{AutomationHeader: key}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a person's session stepped up with the key: %d %s", resp.StatusCode, raw)
	}
	if resp, _ := h.doRaw(h.client(), "POST", "/api/auth/automation/step-up", nil, map[string]string{AutomationHeader: key}); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no session: %d", resp.StatusCode)
	}
}

func TestAutomationMakesNoOwner(t *testing.T) {
	h := newHarness(t)
	key := automationHarness(t, h)
	if resp, raw := h.doRaw(h.client(), "POST", "/api/auth/automation", nil, map[string]string{AutomationHeader: key}); resp.StatusCode != http.StatusConflict {
		t.Fatalf("no owner = %d %s", resp.StatusCode, raw)
	}
	var n int
	if err := h.st.Reader().QueryRow(`SELECT count(*) FROM users`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("users: %d %v", n, err)
	}
}

func TestAutomationOffMountsNothing(t *testing.T) {
	h := newHarness(t)
	h.owner()
	if resp, _ := h.doRaw(h.client(), "POST", "/api/auth/automation", nil, map[string]string{AutomationHeader: "x"}); resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("automation off answered %d", resp.StatusCode)
	}
	if h.svc.Automation() {
		t.Fatal("on without EnableAutomation")
	}
}

// A guessing client runs into the limit.
func TestAutomationLimitsGuesses(t *testing.T) {
	h := newHarness(t)
	h.owner()
	automationHarness(t, h)
	limited := false
	for range automationBurst + 3 {
		if resp, _ := h.doRaw(h.client(), "POST", "/api/auth/automation", nil, map[string]string{AutomationHeader: "wrong"}); resp.StatusCode == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("no limit on wrong keys")
	}
}
