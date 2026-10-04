package auth

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"
)

func (h *harness) beginStepUp(c *http.Client) (*http.Response, map[string]any, requestOptions) {
	h.t.Helper()
	resp, raw := h.doRaw(c, "POST", "/api/auth/step-up/begin", nil, nil)
	var body map[string]any
	var o requestOptions
	_ = json.Unmarshal(raw, &body)
	_ = json.Unmarshal(raw, &o)
	return resp, body, o
}

func (h *harness) sessionInfo(c *http.Client) (until any, methods []any) {
	h.t.Helper()
	resp, body := h.do(c, "GET", "/api/auth/session", nil)
	if resp.StatusCode != http.StatusOK {
		h.t.Fatalf("session = %d %v", resp.StatusCode, body)
	}
	methods, _ = body["step_up_methods"].([]any)
	return body["step_up_until"], methods
}

func TestStepUpWithPasskey(t *testing.T) {
	h := newPasskeyHarness(t)
	c := h.owner()
	p := newSoftPasskey(t, testOrigin)
	h.register(c, p)
	if until, methods := h.sessionInfo(c); until != nil || len(methods) != 1 || methods[0] != StepUpMethodPasskey {
		t.Fatalf("session before = %v %v", until, methods)
	}

	resp, body, o := h.beginStepUp(c)
	if resp.StatusCode != http.StatusOK || body["method"] != StepUpMethodPasskey {
		t.Fatalf("begin = %d %v", resp.StatusCode, body)
	}
	if len(o.PublicKey.AllowCredentials) != 1 || o.PublicKey.UserVerification != "required" || o.PublicKey.RPID != testRPID {
		t.Fatalf("request options = %+v", o.PublicKey)
	}
	// With a passkey, the password isn't a way in.
	if resp, body := h.do(c, "POST", "/api/auth/step-up", map[string]string{"password": "correct horse battery"}); resp.StatusCode != http.StatusForbidden || errCode(body) != "passkey_required" {
		t.Fatalf("password with a passkey = %d %v", resp.StatusCode, body)
	}
	assertion := p.get(t, o)
	resp, body = h.do(c, "POST", "/api/auth/step-up", assertion)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("step-up = %d %v", resp.StatusCode, body)
	}
	until, err := time.Parse(time.RFC3339, body["until"].(string))
	if err != nil || until.Sub(h.clock.now()) > StepUpTTL || until.Sub(h.clock.now()) < StepUpTTL-time.Second {
		t.Fatalf("until = %v %v", body["until"], err)
	}
	if got, _ := h.sessionInfo(c); got == nil {
		t.Fatal("the session doesn't show the grant")
	}
	// The challenge is spent.
	if resp, body := h.do(c, "POST", "/api/auth/step-up", assertion); resp.StatusCode != http.StatusBadRequest || errCode(body) != "ceremony_expired" {
		t.Fatalf("replayed = %d %v", resp.StatusCode, body)
	}
	// The grant lapses after five minutes.
	h.clock.advance(StepUpTTL + time.Second)
	if got, _ := h.sessionInfo(c); got != nil {
		t.Fatalf("grant after five minutes = %v", got)
	}

	// Someone else's passkey doesn't confirm it's you.
	if _, err := h.svc.CreateUser(t.Context(), "sam", "correct horse battery", RoleViewer); err != nil {
		t.Fatal(err)
	}
	sam := h.client()
	if resp, body := h.do(sam, "POST", "/api/auth/login", loginRequest{Username: "sam", Password: "correct horse battery"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("sam login = %d %v", resp.StatusCode, body)
	}
	theirs := newSoftPasskey(t, testOrigin)
	h.register(sam, theirs) // any role manages its own passkeys
	_, _, o = h.beginStepUp(c)
	if resp, body := h.do(c, "POST", "/api/auth/step-up", theirs.get(t, o)); resp.StatusCode != http.StatusForbidden || errCode(body) != "step_up_failed" {
		t.Fatalf("someone else's passkey = %d %v", resp.StatusCode, body)
	}
	if got, _ := h.sessionInfo(c); got != nil {
		t.Fatal("someone else's passkey granted a step-up")
	}
	// A ceremony begun by one user can't be finished by another session.
	_, _, o = h.beginStepUp(c)
	if resp, body := h.do(sam, "POST", "/api/auth/step-up", p.get(t, o)); resp.StatusCode != http.StatusBadRequest || errCode(body) != "ceremony_expired" {
		t.Fatalf("finishing another session's ceremony = %d %v", resp.StatusCode, body)
	}
}

func TestStepUpRules(t *testing.T) {
	h := newPasskeyHarness(t)
	c := h.owner()
	mode := StepUpPasskeyOrPassword
	h.svc.SetStepUpMode(func() StepUpMode { return mode })

	// No passkey: the password fallback, unless passkeys are required.
	if resp, body, _ := h.beginStepUp(c); resp.StatusCode != http.StatusOK || body["method"] != StepUpMethodPassword {
		t.Fatalf("fallback begin = %d %v", resp.StatusCode, body)
	}
	mode = StepUpPasskeyOnly
	if _, methods := h.sessionInfo(c); len(methods) != 0 {
		t.Fatalf("methods under passkey_only = %v", methods)
	}
	if resp, body, _ := h.beginStepUp(c); resp.StatusCode != http.StatusForbidden || errCode(body) != "passkey_required" {
		t.Fatalf("passkey_only begin = %d %v", resp.StatusCode, body)
	}
	if resp, body := h.do(c, "POST", "/api/auth/step-up", map[string]string{"password": "correct horse battery"}); resp.StatusCode != http.StatusForbidden || errCode(body) != "passkey_required" {
		t.Fatalf("passkey_only password = %d %v", resp.StatusCode, body)
	}
	// An unknown rule from the settings reads as the default.
	mode = "sometimes"
	if resp, body, _ := h.beginStepUp(c); resp.StatusCode != http.StatusOK || body["method"] != StepUpMethodPassword {
		t.Fatalf("unknown rule begin = %d %v", resp.StatusCode, body)
	}

	// A passkey that can't work at this address doesn't lock anyone out under
	// the default rule, but passkey_only says why it can't.
	h.register(c, newSoftPasskey(t, testOrigin))
	mode = StepUpPasskeyOrPassword
	h.origin = "https://other.example"
	if resp, body, _ := h.beginStepUp(c); resp.StatusCode != http.StatusOK || body["method"] != StepUpMethodPassword {
		t.Fatalf("elsewhere, default rule = %d %v", resp.StatusCode, body)
	}
	if resp, body := h.do(c, "POST", "/api/auth/step-up", map[string]string{"password": "correct horse battery"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("elsewhere, password = %d %v", resp.StatusCode, body)
	}
	mode = StepUpPasskeyOnly
	if resp, body, _ := h.beginStepUp(c); resp.StatusCode != http.StatusConflict || errCode(body) != "passkeys_unavailable" {
		t.Fatalf("elsewhere, passkey_only = %d %v", resp.StatusCode, body)
	}
	h.origin = testOrigin

	// Bodies that are neither.
	for _, body := range []any{map[string]any{"password": 7}, map[string]any{"password": "x", "id": "y"}, []byte(`[]`)} {
		if resp, _ := h.do(c, "POST", "/api/auth/step-up", body); resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusForbidden {
			t.Errorf("body %v = %d", body, resp.StatusCode)
		}
	}
	// Without a session there's nothing to step up.
	if resp, _ := h.do(h.client(), "POST", "/api/auth/step-up/begin", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous begin = %d", resp.StatusCode)
	}
}

func TestClearStepUp(t *testing.T) {
	h := newHarness(t)
	c := h.owner()
	if resp, body := h.do(c, "POST", "/api/auth/step-up", map[string]string{"password": "correct horse battery"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("step-up = %d %v", resp.StatusCode, body)
	}
	var id string
	if err := h.st.Reader().QueryRow(`SELECT id FROM sessions`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.ClearStepUp(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if got, _ := h.sessionInfo(c); got != nil {
		t.Fatalf("after ClearStepUp = %v", got)
	}
}

func TestUserInvariants(t *testing.T) {
	h := newHarness(t)
	h.owner()
	ctx := t.Context()
	users, err := h.svc.ListUsers(ctx)
	if err != nil || len(users) != 1 {
		t.Fatalf("users = %v %v", users, err)
	}
	owner := users[0]
	admin, err := h.svc.CreateUser(ctx, "ada", "correct horse battery", RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	role := func(r string) *string { return &r }
	yes := true

	if _, err := h.svc.UpdateUser(ctx, owner.ID, role(RoleAdmin), nil); !errors.Is(err, ErrOwnerProtected) {
		t.Errorf("demote owner: %v", err)
	}
	if _, err := h.svc.UpdateUser(ctx, owner.ID, nil, &yes); !errors.Is(err, ErrOwnerProtected) {
		t.Errorf("disable owner: %v", err)
	}
	if u, err := h.svc.UpdateUser(ctx, owner.ID, role(RoleOwner), nil); err == nil {
		t.Errorf("owner role via update: %+v", u)
	}
	if _, err := h.svc.TransferOwnership(ctx, admin.ID, owner.ID); !errors.Is(err, ErrNotOwner) {
		t.Errorf("transfer from a non-owner: %v", err)
	}
	if _, err := h.svc.TransferOwnership(ctx, owner.ID, "u_nobody"); !errors.Is(err, ErrUserNotFound) {
		t.Errorf("transfer to nobody: %v", err)
	}
	if _, err := h.svc.TransferOwnership(ctx, owner.ID, owner.ID); err == nil {
		t.Error("transfer to yourself")
	}
	if _, err := h.svc.TransferOwnership(ctx, owner.ID, admin.ID); err != nil {
		t.Fatal(err)
	}
	var owners int
	if err := h.st.Reader().QueryRow(`SELECT count(*) FROM users WHERE role = 'owner'`).Scan(&owners); err != nil || owners != 1 {
		t.Fatalf("owners = %d %v", owners, err)
	}
	// The single-owner index backs the rule up.
	if _, err := h.st.Writer().Exec(`UPDATE users SET role = 'owner' WHERE id = ?`, owner.ID); err == nil {
		t.Fatal("the database accepted a second owner")
	}
}
