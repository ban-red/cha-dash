//go:build devlogin

package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDevLoginCreatesOwnerOnFirstRun(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	if _, body := h.do(c, "GET", "/api/setup", nil); body["required"] != true || body["dev_login"] != true {
		t.Fatalf("setup status = %v", body)
	}

	resp, body := h.do(c, "POST", "/api/auth/dev-login", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("dev login = %d %v", resp.StatusCode, body)
	}
	if u := body["user"].(map[string]any); u["username"] != "dev" || u["role"] != "owner" {
		t.Fatalf("dev login user = %v", u)
	}
	if resp, body := h.do(c, "GET", "/api/auth/session", nil); resp.StatusCode != http.StatusOK || body["session"].(map[string]any)["method"] != MethodDev {
		t.Fatalf("session = %d %v", resp.StatusCode, body)
	}

	// Setup is over: the token is spent, and the dev owner has no password.
	if h.svc.SetupRequired() {
		t.Fatal("setup still required after dev login")
	}
	if resp, _ := h.do(h.client(), "POST", "/api/setup", setupRequest{Token: h.token, Username: "owner", Password: "correct horse battery"}); resp.StatusCode != http.StatusConflict {
		t.Fatalf("setup after dev login = %d", resp.StatusCode)
	}
	if resp, _ := h.do(h.client(), "POST", "/api/auth/login", loginRequest{Username: "dev", Password: "correct horse battery"}); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("password login as dev = %d", resp.StatusCode)
	}
}

func TestDevLoginSignsInExistingOwner(t *testing.T) {
	h := newHarness(t)
	if resp, _ := h.do(h.client(), "POST", "/api/setup", setupRequest{Token: h.token, Username: "owner", Password: "correct horse battery"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("setup = %d", resp.StatusCode)
	}
	c := h.client()
	for range 2 {
		resp, body := h.do(c, "POST", "/api/auth/dev-login", nil)
		if resp.StatusCode != http.StatusOK || body["user"].(map[string]any)["username"] != "owner" {
			t.Fatalf("dev login = %d %v", resp.StatusCode, body)
		}
	}
	var users int
	if err := h.st.Reader().QueryRow(`SELECT count(*) FROM users`).Scan(&users); err != nil || users != 1 {
		t.Fatalf("users = %d (%v), want 1", users, err)
	}
}

func TestDevLoginOnlyFromLoopback(t *testing.T) {
	h := newHarness(t)
	mux := http.NewServeMux()
	h.svc.Register(mux)
	for addr, want := range map[string]int{
		"192.0.2.10:5000":       http.StatusForbidden,
		"[2001:db8::1]:5000":    http.StatusForbidden,
		"[::ffff:10.0.0.2]:443": http.StatusForbidden,
		"[::1]:5000":            http.StatusOK,
		"127.0.0.1:5000":        http.StatusOK,
	} {
		req := httptest.NewRequest("POST", "/api/auth/dev-login", nil)
		req.RemoteAddr = addr
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("dev login from %s = %d, want %d", addr, rec.Code, want)
		}
	}
}
