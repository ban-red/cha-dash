package auth

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"dash.cha.sh/internal/httpx"
)

func tokenHarness(t *testing.T) (*harness, User, *httptest.Server) {
	t.Helper()
	h := newHarness(t)
	u, err := h.svc.Setup(t.Context(), "sam", "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h.svc.RequireToken(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := FromContext(r.Context())
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"user": id.User.Username, "token": id.Token.ID, "scopes": id.Token.Scopes, "session": id.Session.ID})
	})))
	t.Cleanup(srv.Close)
	return h, u, srv
}

func bearer(t *testing.T, srv *httptest.Server, value string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", srv.URL, nil)
	if value != "" {
		req.Header.Set("Authorization", value)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var b bytes.Buffer
	_, _ = b.ReadFrom(resp.Body)
	return resp.StatusCode, b.String()
}

func TestTokenLifecycle(t *testing.T) {
	h, u, srv := tokenHarness(t)
	ctx := t.Context()
	tok, secret, err := h.svc.CreateToken(ctx, u.ID, "  backup script ", []string{"graph.read", "docker.*"}, h.clock.now().Add(90*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if tok.Name != "backup script" || !strings.HasPrefix(tok.ID, "tok_") || !tokenSecretRE.MatchString(secret) ||
		!strings.Contains(secret, strings.TrimPrefix(tok.ID, "tok_")) {
		t.Fatalf("created %+v %q", tok, secret)
	}
	// The secret is stored only as its hash.
	var stored []byte
	if err := h.st.Reader().QueryRow(`SELECT token_hash FROM api_tokens WHERE id = ?`, tok.ID).Scan(&stored); err != nil || bytes.Contains(stored, []byte(secret)) || len(stored) != 32 {
		t.Fatalf("stored %x %v", stored, err)
	}
	if strings.Contains(h.logs.String(), secret) {
		t.Fatal("the secret reached the log")
	}

	status, body := bearer(t, srv, "Bearer "+secret)
	if status != http.StatusOK || !strings.Contains(body, `"token":"`+tok.ID) || !strings.Contains(body, `"session":""`) {
		t.Fatalf("valid: %d %s", status, body)
	}
	// Case doesn't matter for the scheme; nothing else is ours.
	if status, _ := bearer(t, srv, "bearer "+secret); status != http.StatusOK {
		t.Fatalf("lower-case scheme: %d", status)
	}
	wrongSecret := secret[:len(secret)-4] + "AAAA"
	if wrongSecret == secret {
		wrongSecret = secret[:len(secret)-4] + "BBBB"
	}
	for name, c := range map[string]struct {
		value, code string
		status      int
	}{
		"no header":       {"", "unauthenticated", 401},
		"basic auth":      {"Basic c2FtOnB3", "unauthenticated", 401},
		"another bearer":  {"Bearer eyJhbGciOi.x.y", "unauthenticated", 401},
		"wrong prefix":    {"Bearer cdx_" + strings.TrimPrefix(secret, "cdt_"), "unauthenticated", 401},
		"wrong secret":    {"Bearer " + wrongSecret, "invalid_token", 401},
		"unknown id":      {"Bearer cdt_" + strings.Repeat("a", 26) + "_" + strings.Repeat("A", 52), "invalid_token", 401},
		"malformed":       {"Bearer cdt_short", "invalid_token", 401},
		"secret too long": {"Bearer " + secret + "A", "invalid_token", 401},
	} {
		status, body := bearer(t, srv, c.value)
		if status != c.status || !strings.Contains(body, `"code":"`+c.code+`"`) {
			t.Errorf("%s: %d %s", name, status, body)
		}
	}

	// last_used_at: written on use, then at most once a minute.
	lastUsed := func() int64 {
		var v int64
		if err := h.st.Reader().QueryRow(`SELECT coalesce(last_used_at, 0) FROM api_tokens WHERE id = ?`, tok.ID).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	first := lastUsed()
	if first == 0 {
		t.Fatal("last_used_at not written")
	}
	h.clock.advance(30 * time.Second)
	bearer(t, srv, "Bearer "+secret)
	if lastUsed() != first {
		t.Fatal("last_used_at written twice within a minute")
	}
	h.clock.advance(31 * time.Second)
	bearer(t, srv, "Bearer "+secret)
	if lastUsed() == first {
		t.Fatal("last_used_at not written after a minute")
	}
	list, err := h.svc.ListTokens(ctx, u.ID)
	if err != nil || len(list) != 1 || list[0].LastUsedAt == nil || list[0].LastUsedIP == "" {
		t.Fatalf("list %+v %v", list, err)
	}

	// Expired, then revoked: only the matching secret learns which.
	if _, err := h.st.Writer().Exec(`UPDATE api_tokens SET expires_at = ? WHERE id = ?`, h.clock.now().UnixMilli(), tok.ID); err != nil {
		t.Fatal(err)
	}
	if status, body := bearer(t, srv, "Bearer "+secret); status != 401 || !strings.Contains(body, "token_expired") {
		t.Fatalf("expired: %d %s", status, body)
	}
	if status, body := bearer(t, srv, "Bearer "+wrongSecret); !strings.Contains(body, "invalid_token") {
		t.Fatalf("expired, wrong secret: %d %s", status, body)
	}
	acc, _ := h.svc.TokenAccess(ctx, tok.ID)
	if acc.OK {
		t.Fatal("an expired token has access")
	}
	tok2, secret2, _ := h.svc.CreateToken(ctx, u.ID, "second", []string{"*"}, h.clock.now().Add(time.Hour))
	if acc, _ := h.svc.TokenAccess(ctx, tok2.ID); !acc.OK || acc.Role != RoleOwner || acc.Scopes[0] != "*" {
		t.Fatalf("access %+v", acc)
	}
	if _, changed, err := h.svc.RevokeToken(ctx, tok2.ID, u.ID); !changed || err != nil {
		t.Fatal("revoke", err)
	}
	if _, changed, _ := h.svc.RevokeToken(ctx, tok2.ID, u.ID); changed {
		t.Fatal("revoked twice")
	}
	if status, body := bearer(t, srv, "Bearer "+secret2); status != 401 || !strings.Contains(body, "token_revoked") {
		t.Fatalf("revoked: %d %s", status, body)
	}
	if acc, _ := h.svc.TokenAccess(ctx, tok2.ID); acc.OK {
		t.Fatal("a revoked token has access")
	}
	got, _ := h.svc.GetToken(ctx, tok2.ID)
	if got.RevokedAt == nil || got.RevokedBy != u.ID {
		t.Fatalf("revoked token %+v", got)
	}
	if _, err := h.svc.GetToken(ctx, "tok_nope"); !errors.Is(err, ErrTokenNotFound) {
		t.Fatal(err)
	}

	// Names are checked.
	for _, bad := range []string{"", "   ", strings.Repeat("x", 65), "tab\there"} {
		if _, _, err := h.svc.CreateToken(ctx, u.ID, bad, nil, h.clock.now().Add(time.Hour)); err == nil {
			t.Errorf("name %q accepted", bad)
		}
	}
}

func TestBadTokensRateLimited(t *testing.T) {
	_, _, srv := tokenHarness(t)
	bad := "Bearer cdt_" + strings.Repeat("a", 26) + "_" + strings.Repeat("A", 52)
	limited := false
	for i := 0; i < 15 && !limited; i++ {
		status, _ := bearer(t, srv, bad)
		limited = status == http.StatusTooManyRequests
	}
	if !limited {
		t.Fatal("fifteen bad tokens in a row were never rate-limited")
	}
}

// The comparison is constant-time: the secret's hash is compared with
// subtle.ConstantTimeCompare, also for an unknown ID (against a dummy), and
// nothing in the file compares hashes any other way.
func TestTokenCompareIsConstantTime(t *testing.T) {
	src, err := os.ReadFile("tokens.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	if strings.Count(s, "subtle.ConstantTimeCompare(tokenHash(presented)") != 2 {
		t.Fatal("tokens.go should compare the presented token's hash with subtle.ConstantTimeCompare, for known and unknown IDs")
	}
	for _, bad := range []string{"bytes.Equal", "string(stored) ==", "reflect.DeepEqual"} {
		if strings.Contains(s, bad) {
			t.Fatalf("tokens.go compares with %s", bad)
		}
	}
}
