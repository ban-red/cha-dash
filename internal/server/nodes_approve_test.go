package server

import (
	"net/http"
	"strings"
	"testing"

	"dash.cha.sh/internal/audit"
	"dash.cha.sh/internal/auth"
	"dash.cha.sh/internal/nodeproto"
)

// TestNodeApproveNeedsAStepUp: approving a pending node is admin+ with a
// step-up on the session (decisions 6); without one it's 403
// step_up_required, audited as a refused nodes.approve. With one it goes
// through, and approving again is 409. The join token routes answer the
// admin and nobody below.
func TestNodeApproveNeedsAStepUp(t *testing.T) {
	e, st := fullEnv(t)
	owner := signIn(t, e)
	_, viewer := addUser(t, e, owner, "vic", auth.RoleViewer)
	k, _ := nodeproto.GenerateKey()
	if _, err := st.Writer().Exec(`INSERT INTO nodes (id, name, public_key, state, enrolled_via, created_at, updated_at)
		VALUES ('n_far', 'far', ?, 'pending', 'token', 1, 1)`, nodeproto.PinOf(k)); err != nil {
		t.Fatal(err)
	}
	if status, code, _ := e.call(t, viewer, "POST", "/api/nodes/n_far/approve", nil); status != http.StatusForbidden || code != "forbidden" {
		t.Fatalf("viewer: %d %s", status, code)
	}
	if status, code, _ := e.call(t, owner, "POST", "/api/nodes/n_far/approve", nil); status != http.StatusForbidden || code != "step_up_required" {
		t.Fatalf("without a step-up: %d %s", status, code)
	}
	if ev, ok := lastAudit(t, st, "nodes.approve"); !ok || ev.Result != audit.Refused || ev.Error == nil || ev.Error.Code != "step_up_required" || ev.Target != "node:n_far" {
		t.Fatalf("the refusal wasn't audited: %+v", ev)
	}
	stepUp(t, e, owner)
	if status, _, b := e.call(t, owner, "POST", "/api/nodes/n_far/approve", nil); status != http.StatusOK || !strings.Contains(string(b), `"state":"approved"`) {
		t.Fatalf("with a step-up: %d %s", status, b)
	}
	if status, code, _ := e.call(t, owner, "POST", "/api/nodes/n_far/approve", nil); status != http.StatusConflict || code != "not_pending" {
		t.Fatalf("again: %d %s", status, code)
	}

	// Join tokens: the answer carries the secret once and isn't compressed.
	status, _, b := e.call(t, owner, "POST", "/api/nodes/tokens", map[string]any{"name": "attic"})
	if status != http.StatusCreated || !strings.Contains(string(b), `"secret":"cdj_`) {
		t.Fatalf("create: %d %s", status, b)
	}
	if compressible("/api/nodes/tokens") {
		t.Fatal("the join token's answer would be gzipped")
	}
	if status, _, _ := e.call(t, viewer, "GET", "/api/nodes/tokens", nil); status != http.StatusForbidden {
		t.Fatalf("viewer lists tokens: %d", status)
	}
}
