package auth

// Test automation (CHADASH_TEST_AUTOMATION): a lowered security level for
// testing a hub end to end without a person at the keyboard. Off unless the
// operator sets the flag; never for a hub that holds anything real.
//
// At start the hub writes a fresh random key to <data>/automation.key
// (0600). A script that can read it signs in as the owner and steps up with
// it, instead of a password and a passkey:
//
//	POST /api/auth/automation          key in X-Chadash-Automation-Key
//	  → 200 {"user", "step_up_until"} + session cookie, already stepped up
//	POST /api/auth/automation/step-up  the same key, on that session
//	  → 200 {"until"}: a new step-up grant
//
// Only the step-up is lowered. Roles, tiers, typed confirmations, approvals
// (an agent's plan still waits for a person: the script is that person) and
// the audit all apply; the grant's method is "automation", so the audit and
// the Inbox say how it was confirmed. It makes no account: with no owner yet,
// it refuses. While it's on, the hub logs a warning at start, the session
// endpoint says so (the web shows a banner), and the Inbox keeps a security
// finding open (internal/server).

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"dash.cha.sh/internal/httpx"
	"dash.cha.sh/internal/msg"
)

const (
	// MethodAutomation marks sessions and step-up grants made with the
	// automation key.
	MethodAutomation = "automation"
	// StepUpMethodAutomation names a grant made with the automation key.
	StepUpMethodAutomation = MethodAutomation
	// AutomationKeyFile is the key's file name in the data directory.
	AutomationKeyFile = "automation.key"
	// AutomationHeader carries the key.
	AutomationHeader = "X-Chadash-Automation-Key"
)

// Attempts per client address: a burst, then one per interval, so the key
// can't be guessed by brute force (it's 26 base-32 characters anyway).
const (
	automationBurst = 10
	automationEvery = 6 * time.Second
)

// EnableAutomation turns test automation on: it writes a new key to
// dir/AutomationKeyFile, readable by the hub's user only, and mounts the
// routes at Register. Call it before Register. Each start makes a new key,
// so a copied key stops working when the hub restarts.
func (s *Service) EnableAutomation(dir string) error {
	key := rand.Text()
	path := filepath.Join(dir, AutomationKeyFile)
	_ = os.Remove(path) // a stale file with wider permissions isn't reused
	if err := os.WriteFile(path, []byte(key+"\n"), 0o600); err != nil {
		return fmt.Errorf("auth: writing the automation key: %w", err)
	}
	sum := sha256.Sum256([]byte(key))
	s.mu.Lock()
	s.automationSum = sum[:]
	s.automationLimit = httpx.NewLimiter(automationBurst, automationEvery)
	s.mu.Unlock()
	s.log.Warn("TEST AUTOMATION IS ON: anyone who can read the automation key signs in as the owner and steps up without a passkey; never on a hub that holds anything real",
		"key_file", path)
	return nil
}

// Automation reports whether EnableAutomation ran.
func (s *Service) Automation() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.automationSum != nil
}

func (s *Service) registerAutomation(mux *http.ServeMux) {
	if s.Automation() {
		mux.HandleFunc("POST /api/auth/automation", s.handleAutomation)
		mux.HandleFunc("POST /api/auth/automation/step-up", s.handleAutomationStepUp)
		s.registerTestClock(mux)
	}
}

// automationKeyOK checks the request's key, limited per client address.
// A refusal has been written when it returns false.
func (s *Service) automationKeyOK(w http.ResponseWriter, r *http.Request) bool {
	s.mu.Lock()
	want, limit := s.automationSum, s.automationLimit
	s.mu.Unlock()
	if ok, wait := limit.Allow(httpx.ClientKey(s.proxies.ClientIP(r))); !ok {
		w.Header().Set("Retry-After", fmt.Sprint(int(wait.Seconds())+1))
		httpx.Say(w, http.StatusTooManyRequests, "rate_limited", msg.New("auth.rate_limited"))
		return false
	}
	got := sha256.Sum256([]byte(r.Header.Get(AutomationHeader)))
	if r.Header.Get(AutomationHeader) == "" || subtle.ConstantTimeCompare(got[:], want) != 1 {
		s.log.Warn("automation: a wrong key", "ip", s.proxies.ClientIP(r))
		httpx.Say(w, http.StatusUnauthorized, "unauthenticated", msg.New("auth.automation_key"))
		return false
	}
	return true
}

func (s *Service) handleAutomation(w http.ResponseWriter, r *http.Request) {
	if !s.automationKeyOK(w, r) {
		return
	}
	var u User
	var disabled sql.NullInt64
	err := s.st.Reader().QueryRowContext(r.Context(), `SELECT id, username, role, disabled_at FROM users WHERE role = ?`, RoleOwner).
		Scan(&u.ID, &u.Username, &u.Role, &disabled)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		httpx.Say(w, http.StatusConflict, "no_owner", msg.New("auth.automation_no_owner"))
		return
	case err != nil:
		s.internalError(w, "automation: reading the owner", err)
		return
	case disabled.Valid:
		httpx.Say(w, http.StatusForbidden, "forbidden", msg.New("auth.owner_disabled"))
		return
	}
	sess, err := s.StartSession(w, r, u, MethodAutomation)
	if err != nil {
		s.internalError(w, "automation: starting a session", err)
		return
	}
	id := Identity{User: u, Session: sess, IP: s.proxies.ClientIP(r).String()}
	until, err := s.storeStepUp(r, id, StepUpMethodAutomation)
	if err != nil {
		s.internalError(w, "automation: recording the step-up", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, struct {
		User        User      `json:"user"`
		StepUpUntil time.Time `json:"step_up_until"`
	}{u, until.UTC()})
}

func (s *Service) handleAutomationStepUp(w http.ResponseWriter, r *http.Request) {
	if !s.automationKeyOK(w, r) {
		return
	}
	id, ok := s.Identify(r)
	if !ok {
		httpx.Say(w, http.StatusUnauthorized, "unauthenticated", msg.New("auth.signin_required"))
		return
	}
	if id.Session.Method != MethodAutomation {
		// The key steps up only the sessions it started: a person's session
		// keeps its own step-up.
		httpx.Say(w, http.StatusForbidden, "forbidden", msg.New("auth.automation_session_only"))
		return
	}
	s.grantStepUp(w, r, id, StepUpMethodAutomation)
}
