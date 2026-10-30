package auth

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"dash.cha.sh/internal/httpx"
	"dash.cha.sh/internal/store"
)

// cheap argon2 settings keep the suite fast; production uses defaultParams.
var testParams = argonParams{time: 1, memory: 64, threads: 1, keyLen: 32, saltLen: 16}

type harness struct {
	t    *testing.T
	st   *store.Store
	svc  *Service
	srv  *httptest.Server
	logs *syncBuffer

	opts  []Option   // passed to New
	clock *testClock // the service's clock; real time plus an offset tests move
	// origin and host, when set, go out as the Origin and Host headers of
	// every request, as a browser at that address would send them.
	origin, host string
}

// testClock is the real clock plus an offset, safe to move while handlers run.
type testClock struct{ offset atomic.Int64 }

func (c *testClock) now() time.Time          { return time.Now().Add(time.Duration(c.offset.Load())) }
func (c *testClock) advance(d time.Duration) { c.offset.Add(int64(d)) }

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

func newHarness(t *testing.T, proxies ...netip.Prefix) *harness {
	return newHarnessWith(t, nil, proxies...)
}

// newHarnessWith is newHarness with options for New.
func newHarnessWith(t *testing.T, opts []Option, proxies ...netip.Prefix) *harness {
	t.Helper()
	st, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "chadash.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, st: st, logs: &syncBuffer{}, opts: opts, clock: &testClock{}}
	h.start(proxies...)
	return h
}

// start (re)boots the auth service on the harness's database, as a hub restart would.
func (h *harness) start(proxies ...netip.Prefix) {
	h.t.Helper()
	if h.srv != nil {
		h.srv.Close()
	}
	svc, err := New(h.t.Context(), h.st, slog.New(slog.NewTextHandler(h.logs, nil)), httpx.NewProxies(proxies), h.opts...)
	if err != nil {
		h.t.Fatal(err)
	}
	svc.params = testParams
	svc.now = h.clock.now // set before the server starts serving
	h.svc = svc
	mux := http.NewServeMux()
	svc.Register(mux)
	mux.Handle("GET /api/private", svc.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := FromContext(r.Context())
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"hello": id.User.Username})
	})))
	h.srv = httptest.NewServer(mux)
	h.t.Cleanup(h.srv.Close)
}

func (h *harness) client() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar}
}

func (h *harness) do(c *http.Client, method, path string, body any) (*http.Response, map[string]any) {
	h.t.Helper()
	resp, raw := h.doRaw(c, method, path, body, nil)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return resp, out
}

// doRaw sends body as JSON ([]byte as is) with extra headers, and returns the
// response body unparsed.
func (h *harness) doRaw(c *http.Client, method, path string, body any, hdr map[string]string) (*http.Response, []byte) {
	h.t.Helper()
	var rd io.Reader
	if body != nil {
		b, ok := body.([]byte)
		if !ok {
			b, _ = json.Marshal(body)
		}
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, h.srv.URL+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if h.origin != "" {
		req.Header.Set("Origin", h.origin)
	}
	if h.host != "" {
		req.Host = h.host
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := c.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		h.t.Fatal(err)
	}
	return resp, raw
}

func TestFirstRunFlow(t *testing.T) {
	h := newHarness(t)
	if n := strings.Count(h.logs.String(), "first run: no owner account yet"); n != 1 {
		t.Fatalf("first-run note logged %d times, want once: %q", n, h.logs.String())
	}
	c := h.client()

	if _, body := h.do(c, "GET", "/api/setup", nil); body["required"] != true {
		t.Fatalf("setup status = %v", body)
	}
	if resp, _ := h.do(c, "GET", "/api/private", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("private before setup = %d", resp.StatusCode)
	}

	// A weak password or a bad username is rejected, and setup stays open.
	if resp, _ := h.do(c, "POST", "/api/setup", setupRequest{Username: "owner", Password: "short"}); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("weak password = %d", resp.StatusCode)
	}
	if resp, _ := h.do(c, "POST", "/api/setup", setupRequest{Username: "bad name", Password: "correct horse battery"}); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("bad username = %d", resp.StatusCode)
	}

	// A name a public DNS server hands out could be rebound to the hub by a
	// page a LAN browser opens: setup refuses it.
	h.host = "dash.attacker.example"
	if resp, _ := h.do(c, "POST", "/api/setup", setupRequest{Username: "owner", Password: "correct horse battery"}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("setup under a public name = %d", resp.StatusCode)
	}
	h.host = ""

	resp, body := h.do(c, "POST", "/api/setup", setupRequest{Username: "owner", Password: "correct horse battery"})
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

	// Setup happens once, from any client.
	other := h.client()
	if resp, _ := h.do(other, "POST", "/api/setup", setupRequest{Username: "intruder", Password: "correct horse battery"}); resp.StatusCode != http.StatusConflict {
		t.Fatalf("second setup = %d", resp.StatusCode)
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

	// A restart with an owner present doesn't open setup again.
	h.start()
	if h.svc.SetupRequired() || strings.Count(h.logs.String(), "first run: no owner account yet") != 1 {
		t.Fatal("restart after setup must not offer setup again")
	}
}

func TestSetupNamesTheOwnerAdminByDefault(t *testing.T) {
	h := newHarness(t)
	resp, body := h.do(h.client(), "POST", "/api/setup", setupRequest{Password: "correct horse battery"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("setup = %d %v", resp.StatusCode, body)
	}
	if u := body["user"].(map[string]any); u["username"] != DefaultOwnerUsername || u["role"] != "owner" {
		t.Fatalf("setup user = %v", u)
	}
	if resp, _ := h.do(h.client(), "POST", "/api/auth/login", loginRequest{Username: "admin", Password: "correct horse battery"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("login as admin = %d", resp.StatusCode)
	}
}

func TestSetupIsRaceFree(t *testing.T) {
	h := newHarness(t)
	var wg sync.WaitGroup
	codes := make(chan int, 8)
	for i := range 8 {
		wg.Go(func() {
			resp, _ := h.do(h.client(), "POST", "/api/setup", setupRequest{Username: "owner" + string(rune('a'+i)), Password: "correct horse battery"})
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
	body, _ := json.Marshal(setupRequest{Username: "owner", Password: "correct horse battery"})
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
	if resp, _ := h.do(c, "POST", "/api/setup", setupRequest{Username: "owner", Password: "correct horse battery"}); resp.StatusCode != http.StatusCreated {
		t.Fatal("setup failed")
	}
	if _, err := h.st.Writer().Exec(`UPDATE sessions SET expires_at = ?`, time.Now().Add(-time.Minute).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if resp, _ := h.do(c, "GET", "/api/private", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expired session = %d", resp.StatusCode)
	}
}

func TestSessionRenewsWhileInUse(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	if resp, _ := h.do(c, "POST", "/api/setup", setupRequest{Username: "owner", Password: "correct horse battery"}); resp.StatusCode != http.StatusCreated {
		t.Fatal("setup failed")
	}
	expiresAt := func() time.Duration {
		t.Helper()
		var ms int64
		if err := h.st.Reader().QueryRow(`SELECT expires_at FROM sessions`).Scan(&ms); err != nil {
			t.Fatal(err)
		}
		return time.Until(time.UnixMilli(ms))
	}

	// Used again within a day: no renewal, no new cookie.
	resp, _ := h.do(c, "GET", "/api/private", nil)
	if resp.StatusCode != http.StatusOK || findCookie(resp, CookieName) != nil {
		t.Fatalf("fresh session: status %d, cookie reissued %v", resp.StatusCode, findCookie(resp, CookieName) != nil)
	}

	// 28 days on, two days left: the next use slides it back to 30 days and reissues the cookie.
	if _, err := h.st.Writer().Exec(`UPDATE sessions SET expires_at = ?`, time.Now().Add(48*time.Hour).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	resp, _ = h.do(c, "GET", "/api/private", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("renewing request = %d", resp.StatusCode)
	}
	ck := findCookie(resp, CookieName)
	if ck == nil || ck.MaxAge < int((SessionTTL-time.Minute)/time.Second) {
		t.Fatalf("cookie not reissued with a full lifetime: %+v", ck)
	}
	if left := expiresAt(); left < SessionTTL-time.Minute {
		t.Fatalf("expiry not slid forward: %s left", left)
	}

	// Right after a renewal, further use doesn't write again.
	if resp, _ := h.do(c, "GET", "/api/private", nil); findCookie(resp, CookieName) != nil {
		t.Fatal("renewal should be throttled to once a day")
	}
}

func TestLoginRateLimited(t *testing.T) {
	h := newHarness(t)
	// A frozen clock: under a loaded -race run, 11 attempts can take longer
	// than the 6 s refill, and a refilled token would let the 11th through.
	frozen := time.Now()
	h.svc.limiter.SetClock(func() time.Time { return frozen })
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

func TestLoginAccountLimited(t *testing.T) {
	// Trust the test client as a proxy so each attempt can claim a new address.
	h := newHarness(t, netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128"))
	h.owner()
	// A frozen clock (see TestLoginRateLimited) that the test can move safely.
	frozen := time.Now()
	var later atomic.Int64
	h.svc.failures.SetClock(func() time.Time { return frozen.Add(time.Duration(later.Load())) })
	c := h.client()
	login := func(i int, password string) *http.Response {
		resp, _ := h.doRaw(c, "POST", "/api/auth/login", loginRequest{Username: "owner", Password: password},
			map[string]string{"X-Forwarded-For": fmt.Sprintf("198.51.100.%d", i%250+1)})
		return resp
	}
	for i := range 20 {
		if resp := login(i, "guess guess guess"); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("wrong password %d = %d", i, resp.StatusCode)
		}
	}
	resp := login(20, "correct horse battery")
	if resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("after 20 wrong passwords from 20 addresses, sign-in = %d (Retry-After %q)", resp.StatusCode, resp.Header.Get("Retry-After"))
	}
	// Unknown usernames have no account budget to spend.
	if resp, _ := h.doRaw(c, "POST", "/api/auth/login", loginRequest{Username: "nobody", Password: "guess guess guess"},
		map[string]string{"X-Forwarded-For": "198.51.100.251"}); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unknown user = %d", resp.StatusCode)
	}
	later.Store(int64(time.Minute))
	if resp := login(21, "correct horse battery"); resp.StatusCode != http.StatusOK {
		t.Fatalf("after a refill, sign-in = %d", resp.StatusCode)
	}
}

// Many addresses failing once each don't wipe another address's count.
func TestStrikesSurviveAFloodOfOnes(t *testing.T) {
	h := newHarness(t)
	target := netip.MustParseAddr("198.51.100.7")
	for range BanAfter - 1 {
		if banned, err := h.svc.strike(t.Context(), target, "admin"); banned || err != nil {
			t.Fatal(banned, err)
		}
	}
	base := netip.MustParseAddr("2001:db8::")
	for i := range maxStrikes + 1 {
		a := base.As16()
		a[14], a[15] = byte(i>>8), byte(i)
		if _, err := h.svc.strike(t.Context(), netip.AddrFrom16(a), "admin"); err != nil {
			t.Fatal(err)
		}
	}
	if banned, err := h.svc.strike(t.Context(), target, "admin"); !banned || err != nil {
		t.Fatalf("fifth strike after the flood: banned %v, %v", banned, err)
	}
}

func TestBanAfterWrongPasswords(t *testing.T) {
	// Trust the test client as a proxy so each attempt can name its address.
	h := newHarness(t, netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128"))
	h.owner()
	c := h.client()
	login := func(from, password string) (*http.Response, map[string]any) {
		resp, raw := h.doRaw(c, "POST", "/api/auth/login", loginRequest{Username: "owner", Password: password},
			map[string]string{"X-Forwarded-For": from})
		var out map[string]any
		_ = json.Unmarshal(raw, &out)
		return resp, out
	}
	errCode := func(body map[string]any) string {
		e, _ := body["error"].(map[string]any)
		code, _ := e["code"].(string)
		return code
	}

	// A sign-in that works clears the strikes: four wrong, one right, four wrong.
	for range BanAfter - 1 {
		login("198.51.100.1", "guess guess guess")
	}
	if resp, _ := login("198.51.100.1", "correct horse battery"); resp.StatusCode != http.StatusOK {
		t.Fatalf("right password = %d", resp.StatusCode)
	}
	for i := range BanAfter - 1 {
		if resp, _ := login("198.51.100.1", "guess guess guess"); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("wrong password %d after a good sign-in = %d", i, resp.StatusCode)
		}
	}

	// Signing in to your own account doesn't clear guesses at another's.
	if _, err := h.svc.CreateUser(t.Context(), "guest", "guest password 1", RoleViewer); err != nil {
		t.Fatal(err)
	}
	for i := range BanAfter - 1 {
		if resp, _ := login("198.51.100.4", "guess guess guess"); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("guess %d at the owner = %d", i, resp.StatusCode)
		}
		resp, _ := h.doRaw(c, "POST", "/api/auth/login", loginRequest{Username: "guest", Password: "guest password 1"},
			map[string]string{"X-Forwarded-For": "198.51.100.4"})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("guest's own sign-in %d = %d", i, resp.StatusCode)
		}
	}
	if resp, body := login("198.51.100.4", "guess guess guess"); resp.StatusCode != http.StatusForbidden || errCode(body) != "ip_banned" {
		t.Fatalf("fifth guess at the owner between the guest's sign-ins = %d %v", resp.StatusCode, body)
	}
	if err := h.svc.Unban(t.Context(), "198.51.100.4"); err != nil {
		t.Fatal(err)
	}

	// Five wrong in a row, unknown usernames included: the fifth bans.
	for i := range BanAfter - 1 {
		if resp, _ := login("198.51.100.2", "guess guess guess"); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("wrong password %d = %d", i, resp.StatusCode)
		}
	}
	resp, body := login("198.51.100.2", "guess guess guess")
	if resp.StatusCode != http.StatusForbidden || errCode(body) != "ip_banned" {
		t.Fatalf("fifth wrong password = %d %v", resp.StatusCode, body)
	}
	// Banned: even the right password, and passkeys, are refused. Other
	// addresses aren't affected, and the ban survives a restart.
	h.start(netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128"))
	if resp, body := login("198.51.100.2", "correct horse battery"); resp.StatusCode != http.StatusForbidden || errCode(body) != "ip_banned" {
		t.Fatalf("banned, right password = %d %v", resp.StatusCode, body)
	}
	if resp, _ := h.doRaw(c, "POST", "/api/auth/passkeys/login/begin", nil, map[string]string{"X-Forwarded-For": "198.51.100.2"}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("banned, passkey begin = %d", resp.StatusCode)
	}
	if resp, _ := login("198.51.100.3", "correct horse battery"); resp.StatusCode != http.StatusOK {
		t.Fatalf("another address = %d", resp.StatusCode)
	}
	// IPv4-mapped IPv6 is the same client.
	if resp, _ := login("::ffff:198.51.100.2", "correct horse battery"); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("mapped form of a banned address = %d", resp.StatusCode)
	}

	bans, err := h.svc.ListBans(t.Context())
	if err != nil || len(bans) != 1 || bans[0].IP != "198.51.100.2" || bans[0].Failures != BanAfter {
		t.Fatalf("bans = %+v, %v", bans, err)
	}
	// Only clearing lifts it.
	if err := h.svc.Unban(t.Context(), "not an address"); err == nil {
		t.Fatal("unban accepted a non-address")
	}
	if err := h.svc.Unban(t.Context(), "203.0.113.9"); !errors.Is(err, ErrBanNotFound) {
		t.Fatalf("unban of an address that isn't banned = %v", err)
	}
	if err := h.svc.Unban(t.Context(), "198.51.100.2"); err != nil {
		t.Fatal(err)
	}
	if resp, _ := login("198.51.100.2", "correct horse battery"); resp.StatusCode != http.StatusOK {
		t.Fatalf("after unban = %d", resp.StatusCode)
	}
	if strings.Contains(h.logs.String(), "guess guess") {
		t.Fatal("a password reached the logs")
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
