//go:build !devlogin

package auth

import (
	"net/http"
	"testing"
)

// Release builds have no dev login: the route doesn't exist and setup
// status doesn't offer it.
func TestNoDevLoginWithoutTag(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	if _, body := h.do(c, "GET", "/api/setup", nil); body["dev_login"] != nil {
		t.Fatalf("setup status offers dev login: %v", body)
	}
	if resp, _ := h.do(c, "POST", "/api/auth/dev-login", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("dev login without the tag = %d, want 404", resp.StatusCode)
	}
	if !h.svc.SetupRequired() {
		t.Fatal("setup no longer required")
	}
}
