package auth

import (
	"net/http"
	"testing"
)

// A passkey that just signed a browser in also steps that session up: what
// a person does who signs in with a passkey and then confirms a
// destructive action (web/e2e/passkey.spec.ts found it failing).
func TestStepUpWithThePasskeyThatSignedIn(t *testing.T) {
	h := newPasskeyHarness(t)
	c := h.owner()
	p := newSoftPasskey(t, testOrigin)
	h.register(c, p)

	anon := h.client()
	_, lo := h.beginLogin(anon)
	if resp, body := h.finishLogin(anon, p.get(t, lo)); resp.StatusCode != http.StatusOK {
		t.Fatalf("login/finish = %d %v", resp.StatusCode, body)
	}
	resp, body, o := h.beginStepUp(anon)
	if resp.StatusCode != http.StatusOK || body["method"] != StepUpMethodPasskey {
		t.Fatalf("begin = %d %v", resp.StatusCode, body)
	}
	if resp, body := h.do(anon, "POST", "/api/auth/step-up", p.get(t, o)); resp.StatusCode != http.StatusOK {
		t.Fatalf("step-up after signing in with the same passkey = %d %v", resp.StatusCode, body)
	}
}
