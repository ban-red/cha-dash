package auth

import (
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
)

// eventLog collects what OnEvent hears.
type eventLog struct {
	mu  sync.Mutex
	got []Event
}

func (l *eventLog) add(e Event) { l.mu.Lock(); l.got = append(l.got, e); l.mu.Unlock() }

func (l *eventLog) kinds() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, e := range l.got {
		out = append(out, string(e.Kind))
	}
	return out
}

func (l *eventLog) last() Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.got[len(l.got)-1]
}

// Every change to a session or an account is told to the listeners, with
// the IDs a listener needs (the realtime hub closes sockets, the audit log
// records), and never a secret.
func TestAuthEvents(t *testing.T) {
	h := newPasskeyHarness(t)
	var log eventLog
	h.svc.OnEvent(log.add)
	c := h.owner()

	id := h.register(c, newSoftPasskey(t, testOrigin))
	if e := log.last(); e.Kind != EventPasskeyRegistered || e.Passkey != id || e.Name == "" || e.UserID == "" || e.Request == nil {
		t.Fatalf("register: %+v", e)
	}
	if resp, _ := h.do(c, "PATCH", "/api/auth/passkeys/"+id, map[string]string{"name": "Laptop"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("rename = %d", resp.StatusCode)
	}
	if e := log.last(); e.Kind != EventPasskeyRenamed || e.Passkey != id || e.Name != "Laptop" {
		t.Fatalf("rename: %+v", e)
	}
	if resp, _ := h.do(c, "DELETE", "/api/auth/passkeys/"+id, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete = %d", resp.StatusCode)
	}
	if e := log.last(); e.Kind != EventPasskeyDeleted || e.Passkey != id {
		t.Fatalf("delete: %+v", e)
	}

	// Step-up: a wrong password, then the right one.
	if resp, _ := h.do(c, "POST", "/api/auth/step-up", map[string]string{"password": "not my password at all"}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("wrong step-up = %d", resp.StatusCode)
	}
	if e := log.last(); e.Kind != EventStepUpFailed || e.Reason == "" || e.SessionID == "" || strings.Contains(e.Reason, "not my password") {
		t.Fatalf("failed step-up: %+v", e)
	}
	if resp, _ := h.do(c, "POST", "/api/auth/step-up", map[string]string{"password": "correct horse battery"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("step-up = %d", resp.StatusCode)
	}
	if e := log.last(); e.Kind != EventStepUp || e.Method != MethodPassword {
		t.Fatalf("step-up: %+v", e)
	}

	// Account changes: a role change and a disable are told; re-enabling and
	// a change to nothing aren't.
	u, err := h.svc.CreateUser(t.Context(), "kim", "correct horse battery", RoleOperator)
	if err != nil {
		t.Fatal(err)
	}
	viewer, disabled, enabled := RoleViewer, true, false
	n := len(log.kinds())
	for _, step := range []struct {
		role     *string
		disabled *bool
	}{{&viewer, nil}, {&viewer, nil}, {nil, &disabled}, {nil, &enabled}} {
		if _, err := h.svc.UpdateUser(t.Context(), u.ID, step.role, step.disabled); err != nil {
			t.Fatal(err)
		}
	}
	if got := log.kinds()[n:]; !slices.Equal(got, []string{string(EventUserChanged), string(EventUserChanged)}) || log.last().UserID != u.ID {
		t.Fatalf("account changes told %v", got)
	}
	// Handing over ownership changes both accounts.
	n = len(log.kinds())
	owner := h.svc.mustOwnerID(t)
	if _, err := h.svc.TransferOwnership(t.Context(), owner, u.ID); err != nil {
		t.Fatal(err)
	}
	if got := log.kinds()[n:]; len(got) != 2 {
		t.Fatalf("transfer told %v", got)
	}

	// Signing out names the session that ended.
	if resp, _ := h.do(c, "POST", "/api/auth/logout", nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("logout = %d", resp.StatusCode)
	}
	if e := log.last(); e.Kind != EventSessionEnded || !strings.HasPrefix(e.SessionID, "s_") {
		t.Fatalf("logout: %+v", e)
	}
	// Signing out again (no session left) tells nobody.
	n = len(log.kinds())
	h.do(c, "POST", "/api/auth/logout", nil)
	if len(log.kinds()) != n {
		t.Fatal("a second sign-out was told")
	}
}

// StillValid holds while the session, the account and the role do.
func TestStillValid(t *testing.T) {
	h := newHarness(t)
	h.owner()
	u, err := h.svc.CreateUser(t.Context(), "kim", "correct horse battery", RoleOperator)
	if err != nil {
		t.Fatal(err)
	}
	c := h.client()
	if resp, _ := h.do(c, "POST", "/api/auth/login", map[string]string{"username": "kim", "password": "correct horse battery"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("login = %d", resp.StatusCode)
	}
	var sid string
	if err := h.st.Reader().QueryRow(`SELECT id FROM sessions WHERE user_id = ?`, u.ID).Scan(&sid); err != nil {
		t.Fatal(err)
	}
	id := Identity{User: User{ID: u.ID, Username: "kim", Role: RoleOperator}, Session: Session{ID: sid}}
	check := func(want bool, what string) {
		t.Helper()
		if ok, err := h.svc.StillValid(t.Context(), id); err != nil || ok != want {
			t.Fatalf("%s: StillValid = %v, %v", what, ok, err)
		}
	}
	check(true, "fresh")
	viewer := RoleViewer
	if _, err := h.svc.UpdateUser(t.Context(), u.ID, &viewer, nil); err != nil {
		t.Fatal(err)
	}
	check(false, "demoted")
	id.User.Role = RoleViewer
	check(true, "as the new role")
	if _, err := h.st.Writer().Exec(`UPDATE sessions SET expires_at = 1 WHERE id = ?`, sid); err != nil {
		t.Fatal(err)
	}
	check(false, "expired")
	check2 := Identity{User: id.User}
	if ok, _ := h.svc.StillValid(t.Context(), check2); ok {
		t.Fatal("an identity without a session (a token's) still valid")
	}
}

// mustOwnerID is the owner's user ID.
func (s *Service) mustOwnerID(t *testing.T) string {
	t.Helper()
	var id string
	if err := s.st.Reader().QueryRow(`SELECT id FROM users WHERE role = 'owner'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// Every sign-in tells the listeners once, with the session's method: setup
// signs the owner in with a password, then a password sign-in and a passkey
// sign-in. A failed sign-in tells nobody. (Dev login: devlogin_test.go.)
func TestSignedInEvents(t *testing.T) {
	h := newPasskeyHarness(t)
	var log eventLog
	h.svc.OnEvent(log.add)
	signedIn := func() []Event {
		log.mu.Lock()
		defer log.mu.Unlock()
		var out []Event
		for _, e := range log.got {
			if e.Kind == EventSignedIn {
				out = append(out, e)
			}
		}
		return out
	}
	check := func(what string, n int, method string) {
		t.Helper()
		got := signedIn()
		if len(got) != n {
			t.Fatalf("%s: %d signed_in events, want %d", what, len(got), n)
		}
		e := got[n-1]
		if e.Method != method || e.UserID == "" || !strings.HasPrefix(e.SessionID, "s_") || e.Request == nil {
			t.Fatalf("%s: %+v", what, e)
		}
	}

	c := h.owner()
	check("setup", 1, MethodPassword)
	if resp, _ := h.do(h.client(), "POST", "/api/auth/login", map[string]string{"username": "owner", "password": "not the password"}); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong login = %d", resp.StatusCode)
	}
	if len(signedIn()) != 1 {
		t.Fatal("a failed sign-in was told")
	}
	if resp, _ := h.do(h.client(), "POST", "/api/auth/login", map[string]string{"username": "owner", "password": "correct horse battery"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("login = %d", resp.StatusCode)
	}
	check("password", 2, MethodPassword)
	p := newSoftPasskey(t, testOrigin)
	h.register(c, p)
	if resp, body := h.login(p); resp.StatusCode != http.StatusOK {
		t.Fatalf("passkey login = %d %v", resp.StatusCode, body)
	}
	check("passkey", 3, MethodPasskey)
}
