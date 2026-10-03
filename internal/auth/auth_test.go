package auth

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"dash.cha.sh/internal/httpx"
	"dash.cha.sh/internal/store"
)

// cheap argon2 settings keep the suite fast; production uses defaultParams.
var testParams = argonParams{time: 1, memory: 64, threads: 1, keyLen: 32, saltLen: 16}

type harness struct {
	t     *testing.T
	st    *store.Store
	svc   *Service
	srv   *httptest.Server
	logs  *syncBuffer
	token string
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

var tokenRE = regexp.MustCompile(`setup_token=(cds_[a-z2-7]+)`)

func newHarness(t *testing.T, proxies ...netip.Prefix) *harness {
	t.Helper()
	st, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "chadash.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, st: st, logs: &syncBuffer{}}
	h.start(proxies...)
	return h
}

// start (re)boots the auth service on the harness's database, as a hub restart would.
func (h *harness) start(proxies ...netip.Prefix) {
	h.t.Helper()
	if h.srv != nil {
		h.srv.Close()
	}
	svc, err := New(h.t.Context(), h.st, slog.New(slog.NewTextHandler(h.logs, nil)), httpx.NewProxies(proxies))
	if err != nil {
		h.t.Fatal(err)
	}
	svc.params = testParams
	h.svc = svc
	mux := http.NewServeMux()
	svc.Register(mux)
	mux.Handle("GET /api/private", svc.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := FromContext(r.Context())
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"hello": id.User.Username})
	})))
	h.srv = httptest.NewServer(mux)
	h.t.Cleanup(h.srv.Close)
	if m := tokenRE.FindAllStringSubmatch(h.logs.String(), -1); len(m) > 0 {
		h.token = m[len(m)-1][1]
	}
}

func (h *harness) client() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar}
}

func (h *harness) do(c *http.Client, method, path string, body any) (*http.Response, map[string]any) {
	h.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, h.srv.URL+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

func TestFirstRunFlow(t *testing.T) {
	h := newHarness(t)
	if h.token == "" {
		t.Fatalf("no setup token logged: %q", h.logs.String())
	}
	if n := strings.Count(h.logs.String(), h.token); n != 1 {
		t.Fatalf("setup token logged %d times, want once", n)
	}
	c := h.client()

	if _, body := h.do(c, "GET", "/api/setup", nil); body["required"] != true {
		t.Fatalf("setup status = %v", body)
	}
	if resp, _ := h.do(c, "GET", "/api/private", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("private before setup = %d", resp.StatusCode)
	}

	// Wrong token, then a weak password: both rejected, the token stays valid.
	if resp, _ := h.do(c, "POST", "/api/setup", setupRequest{Token: "cds_wrong", Username: "owner", Password: "correct horse battery"}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("wrong token = %d", resp.StatusCode)
	}
	if resp, _ := h.do(c, "POST", "/api/setup", setupRequest{Token: h.token, Username: "owner", Password: "short"}); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("weak password = %d", resp.StatusCode)
	}
	if resp, _ := h.do(c, "POST", "/api/setup", setupRequest{Token: h.token, Username: "bad name", Password: "correct horse battery"}); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("bad username = %d", resp.StatusCode)
	}

	resp, body := h.do(c, "POST", "/api/setup", setupRequest{Token: h.token, Username: "owner", Password: "correct horse battery"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("setup = %d %v", resp.StatusCode, body)
	}
	if u := body["user"].(map[string]any); u["role"] != "owner" || u["username"] != "owner" {
		t.Fatalf("setup user = %v", u)
	}
	cookie := findCookie(resp, CookieName)
	if cookie == nil || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.Secure || cookie.Path != "/" {
		t.Fatalf("session cookie = %+v", cookie)
	}

	// The cookie authenticates.
	if resp, body := h.do(c, "GET", "/api/private", nil); resp.StatusCode != http.StatusOK || body["hello"] != "owner" {
		t.Fatalf("private after setup = %d %v", resp.StatusCode, body)
	}
	if resp, body := h.do(c, "GET", "/api/auth/session", nil); resp.StatusCode != http.StatusOK || body["session"].(map[string]any)["method"] != "password" {
		t.Fatalf("session = %d %v", resp.StatusCode, body)
	}

	// Only a hash of the token is stored.
	var n int
	if err := h.st.Reader().QueryRow(`SELECT count(*) FROM sessions WHERE token_hash = ? OR CAST(token_hash AS TEXT) = ?`, cookie.Value, cookie.Value).Scan(&n); err != nil || n != 0 {
		t.Fatalf("raw token found in sessions table (%d, %v)", n, err)
	}

	// The token is single-use, from any client.
	other := h.client()
	if resp, _ := h.do(other, "POST", "/api/setup", setupRequest{Token: h.token, Username: "intruder", Password: "correct horse battery"}); resp.StatusCode != http.StatusConflict {
		t.Fatalf("token reuse = %d", resp.StatusCode)
	}
	if _, body := h.do(other, "GET", "/api/setup", nil); body["required"] != false {
		t.Fatalf("setup status after setup = %v", body)
	}

	// Logout kills the session server-side, not just the cookie.
	if resp, _ := h.do(c, "POST", "/api/auth/logout", nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("logout = %d", resp.StatusCode)
	}
	replay := &http.Client{}
	req, _ := http.NewRequest("GET", h.srv.URL+"/api/private", nil)
	req.AddCookie(&http.Cookie{Name: CookieName, Value: cookie.Value})
	if resp, err := replay.Do(req); err != nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("replayed cookie after logout: %v %v", resp.StatusCode, err)
	}

	// Login works with the right password only.
	if resp, _ := h.do(c, "POST", "/api/auth/login", loginRequest{Username: "owner", Password: "wrong password!!"}); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad login = %d", resp.StatusCode)
	}
	if resp, _ := h.do(c, "POST", "/api/auth/login", loginRequest{Username: "nobody", Password: "correct horse battery"}); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unknown user login = %d", resp.StatusCode)
	}
	if resp, _ := h.do(c, "POST", "/api/auth/login", loginRequest{Username: "OWNER", Password: "correct horse battery"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("login (case-insensitive username) = %d", resp.StatusCode)
	}
	if resp, _ := h.do(c, "GET", "/api/private", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("private after login = %d", resp.StatusCode)
	}
	if strings.Contains(h.logs.String(), "correct horse") {
		t.Fatal("a password reached the logs")
	}

	// A restart with an owner present mints no new token.
	before := len(tokenRE.FindAllString(h.logs.String(), -1))
	h.start()
	if h.svc.SetupRequired() || len(tokenRE.FindAllString(h.logs.String(), -1)) != before {
		t.Fatal("restart after setup must not offer setup again")
	}
}

func TestSetupIsRaceFree(t *testing.T) {
	h := newHarness(t)
	var wg sync.WaitGroup
	codes := make(chan int, 8)
	for i := range 8 {
		wg.Go(func() {
			resp, _ := h.do(h.client(), "POST", "/api/setup", setupRequest{Token: h.token, Username: "owner" + string(rune('a'+i)), Password: "correct horse battery"})
			codes <- resp.StatusCode
		})
	}
	wg.Wait()
	close(codes)
	created := 0
	for c := range codes {
		if c == http.StatusCreated {
			created++
		} else if c != http.StatusConflict {
			t.Fatalf("unexpected status %d", c)
		}
	}
	if created != 1 {
		t.Fatalf("%d owners created, want 1", created)
	}
}

func TestSecureCookieBehindTrustedProxy(t *testing.T) {
	h := newHarness(t, netip.MustParsePrefix("10.0.0.0/8"))
	body, _ := json.Marshal(setupRequest{Token: h.token, Username: "owner", Password: "correct horse battery"})
	req := httptest.NewRequest("POST", "/api/setup", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-Proto", "https")
	req.RemoteAddr = "10.0.0.2:4444"
	w := httptest.NewRecorder()
	h.svc.handleSetup(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("setup = %d %s", w.Code, w.Body)
	}
	c := findCookie(w.Result(), CookieName)
	if c == nil || !c.Secure {
		t.Fatalf("cookie behind TLS proxy should be Secure: %+v", c)
	}
}

func TestExpiredSessionRejected(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	if resp, _ := h.do(c, "POST", "/api/setup", setupRequest{Token: h.token, Username: "owner", Password: "correct horse battery"}); resp.StatusCode != http.StatusCreated {
		t.Fatal("setup failed")
	}
	if _, err := h.st.Writer().Exec(`UPDATE sessions SET expires_at = ?`, time.Now().Add(-time.Minute).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if resp, _ := h.do(c, "GET", "/api/private", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expired session = %d", resp.StatusCode)
	}
}

func TestLoginRateLimited(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	var last *http.Response
	for range 11 {
		last, _ = h.do(c, "POST", "/api/auth/login", loginRequest{Username: "owner", Password: "guess guess guess"})
	}
	if last.StatusCode != http.StatusTooManyRequests || last.Header.Get("Retry-After") == "" {
		t.Fatalf("11th attempt = %d (Retry-After %q)", last.StatusCode, last.Header.Get("Retry-After"))
	}
}

func TestPasswordHash(t *testing.T) {
	enc := hashPassword("correct horse battery", testParams)
	if !strings.HasPrefix(enc, "$argon2id$v=19$m=64,t=1,p=1$") {
		t.Fatalf("hash format = %q", enc)
	}
	if ok, err := verifyPassword("correct horse battery", enc); !ok || err != nil {
		t.Fatalf("verify right password = %v, %v", ok, err)
	}
	if ok, _ := verifyPassword("correct horse batterY", enc); ok {
		t.Fatal("wrong password verified")
	}
	if enc == hashPassword("correct horse battery", testParams) {
		t.Fatal("hashes must be salted")
	}
	for _, bad := range []string{"", "$argon2i$v=19$m=64,t=1,p=1$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaA", "$argon2id$v=19$m=99999999,t=1,p=1$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaA"} {
		if _, err := verifyPassword("x", bad); err == nil {
			t.Errorf("verify accepted malformed hash %q", bad)
		}
	}
}

func TestLimiter(t *testing.T) {
	now := time.Unix(0, 0)
	l := newLimiter(2, time.Second)
	l.now = func() time.Time { return now }
	for i := range 2 {
		if ok, _ := l.allow("a"); !ok {
			t.Fatalf("attempt %d denied within burst", i)
		}
	}
	ok, wait := l.allow("a")
	if ok || wait != time.Second {
		t.Fatalf("over burst: ok=%v wait=%v", ok, wait)
	}
	if ok, _ := l.allow("b"); !ok {
		t.Fatal("keys must be independent")
	}
	now = now.Add(time.Second)
	if ok, _ := l.allow("a"); !ok {
		t.Fatal("token should refill")
	}
}

func findCookie(resp *http.Response, name string) *http.Cookie {
	for _, c := range resp.Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}
