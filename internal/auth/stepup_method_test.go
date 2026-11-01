package auth

import (
	"database/sql"
	"net/http"
	"net/url"
	"testing"
)

// TestStepUpKeepsItsMethod: a grant remembers how it was made, the
// session's lookup reads it back, and clearing the grant clears it too.
func TestStepUpKeepsItsMethod(t *testing.T) {
	h := newHarness(t)
	c := h.owner()
	if resp, body := h.do(c, "POST", "/api/auth/step-up", map[string]string{"password": "correct horse battery"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("step-up = %d %v", resp.StatusCode, body)
	}
	var id, token string
	var method sql.NullString
	if err := h.st.Reader().QueryRow(`SELECT id, step_up_method FROM sessions`).Scan(&id, &method); err != nil {
		t.Fatal(err)
	}
	if method.String != StepUpMethodPassword {
		t.Fatalf("stored method %q", method.String)
	}
	u, _ := url.Parse(h.srv.URL)
	for _, ck := range c.Jar.Cookies(u) {
		if ck.Name == CookieName {
			token = ck.Value
		}
	}
	sess, _, err := h.svc.lookupSession(t.Context(), token)
	if err != nil || sess.StepUpMethod != StepUpMethodPassword || sess.StepUpUntil.IsZero() {
		t.Fatalf("lookup: %+v %v", sess, err)
	}
	if err := h.svc.ClearStepUp(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if err := h.st.Reader().QueryRow(`SELECT step_up_method FROM sessions WHERE id = ?`, id).Scan(&method); err != nil || method.Valid {
		t.Fatalf("after clearing: %v %v", method, err)
	}
}
