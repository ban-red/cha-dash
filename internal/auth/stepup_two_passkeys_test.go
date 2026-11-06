package auth

import (
	"net/http"
	"testing"
)

// Someone with two passkeys steps up with either: the ceremony lists both,
// and the check knows they own both (found by the e2e passkey tests).
func TestStepUpWithEitherOfTwoPasskeys(t *testing.T) {
	h := newPasskeyHarness(t)
	c := h.owner()
	laptop := newSoftPasskey(t, testOrigin)
	phone := newSoftPasskey(t, testOrigin)
	h.register(c, laptop)
	h.register(c, phone)
	for name, p := range map[string]*softPasskey{"the first": laptop, "the second": phone} {
		_, _, o := h.beginStepUp(c)
		if len(o.PublicKey.AllowCredentials) != 2 {
			t.Fatalf("allowed = %d, want both passkeys", len(o.PublicKey.AllowCredentials))
		}
		if resp, body := h.do(c, "POST", "/api/auth/step-up", p.get(t, o)); resp.StatusCode != http.StatusOK {
			t.Fatalf("step-up with %s passkey = %d %v", name, resp.StatusCode, body)
		}
	}
}
