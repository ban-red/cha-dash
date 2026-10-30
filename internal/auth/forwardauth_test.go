package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"

	"dash.cha.sh/internal/httpx"
)

// faHub serves the auth routes and /api/private behind ForwardAuth, as the
// hub's middleware chain does; /api/echo reports the user header a handler
// would see.
func faHub(t *testing.T, trusted bool) (*harness, *settingsBox, *httptest.Server) {
	t.Helper()
	var h *harness
	if trusted {
		h = newHarness(t, netip.MustParsePrefix("127.0.0.0/8"))
	} else {
		h = newHarness(t)
	}
	box := &settingsBox{set: DefaultSignIn()}
	box.set.ForwardAuth.Enabled = true
	h.svc.SetSignIn(box.get, func(context.Context) (string, error) { return "", nil })
	mux := http.NewServeMux()
	h.svc.Register(mux)
	mux.Handle("GET /api/private", h.svc.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := FromContext(r.Context())
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"user": id.User.Username, "method": id.Session.Method, "role": id.User.Role})
	})))
	mux.Handle("GET /api/whoami", h.svc.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := FromContext(r.Context())
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"user": id.User.Username, "session": id.Session.ID, "role": id.User.Role})
	})))
	mux.HandleFunc("GET /api/echo", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.Header.Get("Remote-User")+"|"+r.Header.Get("Remote-Groups"))
	})
	srv := httptest.NewServer(h.svc.ForwardAuth(mux))
	t.Cleanup(srv.Close)
	return h, box, srv
}

func faGet(t *testing.T, c *http.Client, srv *httptest.Server, path string, hdr map[string]string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", srv.URL+path, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// nav adds what a browser sends on a top-level navigation to hdr.
func nav(hdr map[string]string) map[string]string {
	out := map[string]string{"Sec-Fetch-Mode": "navigate"}
	maps.Copy(out, hdr)
	return out
}

func jarClient() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar}
}

// From a peer that isn't a trusted proxy, the headers never reach a
// handler and sign nobody in; the first such request is an event, the
// next within the hour isn't.
func TestForwardAuthUntrustedPeerStripped(t *testing.T) {
	h, _, srv := faHub(t, false)
	h.owner()
	alice, err := h.svc.CreateUser(t.Context(), "alice", "alice password", RoleOperator)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.LinkForwardAuth(t.Context(), alice.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	var events eventLog
	h.svc.OnEvent(events.add)
	c := jarClient()
	hdr := map[string]string{"Remote-User": "alice", "Remote-Groups": "admins"}
	if code, body := faGet(t, c, srv, "/api/echo", hdr); code != 200 || body != "|" {
		t.Fatalf("a handler saw %q", body)
	}
	if code, _ := faGet(t, c, srv, "/api/private", hdr); code != http.StatusUnauthorized {
		t.Fatalf("untrusted headers signed in: %d", code)
	}
	if k := events.kinds(); len(k) != 1 || k[0] != string(EventForwardAuthUntrusted) || events.last().IP != "127.0.0.1" {
		t.Fatalf("events = %v", k)
	}
}

// From a trusted proxy, a linked username signs in with method
// forward_auth; a changed user header ends the session and signs in the
// one it names; an unlinked username never takes over a password session.
func TestForwardAuthTrustedPeer(t *testing.T) {
	h, box, srv := faHub(t, true)
	h.owner()
	var events eventLog
	h.svc.OnEvent(events.add)
	alice, _ := h.svc.CreateUser(t.Context(), "alice", "alice password", RoleOperator)
	bob, _ := h.svc.CreateUser(t.Context(), "bob", "bob password!", RoleViewer)
	_, _ = h.svc.LinkForwardAuth(t.Context(), alice.ID, "alice@idp")
	_, _ = h.svc.LinkForwardAuth(t.Context(), bob.ID, "bob@idp")

	// The headers that counted are gone by the time a handler runs.
	c := jarClient()
	if _, body := faGet(t, c, srv, "/api/echo", map[string]string{"Remote-User": "alice@idp"}); body != "|" {
		t.Fatalf("a handler saw %q", body)
	}
	code, body := faGet(t, c, srv, "/api/private", nav(map[string]string{"Remote-User": "alice@idp"}))
	if code != 200 || body != `{"method":"forward_auth","role":"operator","user":"alice"}`+"\n" {
		t.Fatalf("trusted sign-in = %d %s", code, body)
	}
	// The cookie carries on; the header still matches.
	if code, body := faGet(t, c, srv, "/api/private", map[string]string{"Remote-User": "alice@idp"}); code != 200 || body != `{"method":"forward_auth","role":"operator","user":"alice"}`+"\n" {
		t.Fatalf("second request = %d %s", code, body)
	}
	sessions := func(userID string) int {
		var n int
		_ = h.st.Reader().QueryRow(`SELECT count(*) FROM sessions WHERE user_id = ?`, userID).Scan(&n)
		return n
	}
	if n := sessions(alice.ID); n != 1 {
		t.Fatalf("alice has %d sessions", n)
	}

	// The proxy now says bob: alice's session ends, bob signs in.
	code, body = faGet(t, c, srv, "/api/private", nav(map[string]string{"Remote-User": "bob@idp"}))
	if code != 200 || body != `{"method":"forward_auth","role":"viewer","user":"bob"}`+"\n" {
		t.Fatalf("changed header = %d %s", code, body)
	}
	if n := sessions(alice.ID); n != 0 {
		t.Fatal("alice's session survived the header naming bob")
	}
	if !slicesContainsKind(events.kinds(), string(EventSessionEnded)) {
		t.Fatal("ending the session emitted nothing")
	}

	// An unlinked name ends a forward_auth session and signs nobody in.
	if code, _ := faGet(t, c, srv, "/api/private", nav(map[string]string{"Remote-User": "mallory@idp"})); code != http.StatusUnauthorized {
		t.Fatalf("unlinked header = %d", code)
	}
	if e := events.last(); e.Kind != EventSignInRefused || e.Method != MethodForwardAuth || e.Reason != "unlinked" {
		t.Fatalf("last event = %+v", e)
	}

	// A password session meets an unlinked name: it stays, so its user can
	// link the name.
	pw := jarClient()
	if resp, _ := h.do(pw, "POST", "/api/auth/login", loginRequest{Username: "alice", Password: "alice password"}); resp.StatusCode != 200 {
		t.Fatal("password sign-in failed")
	}
	pw.Jar.SetCookies(mustURL(srv.URL), pw.Jar.Cookies(mustURL(h.srv.URL)))
	if code, body := faGet(t, pw, srv, "/api/private", map[string]string{"Remote-User": "alice-new@idp"}); code != 200 || body != `{"method":"password","role":"operator","user":"alice"}`+"\n" {
		t.Fatalf("password session with an unlinked header = %d %s", code, body)
	}

	// Forward-auth off: the headers are stripped and count for nothing.
	box.update(func(s *SignInSettings) { s.ForwardAuth.Enabled = false })
	if code, _ := faGet(t, jarClient(), srv, "/api/private", nav(map[string]string{"Remote-User": "alice@idp"})); code != http.StatusUnauthorized {
		t.Fatalf("forward-auth off = %d", code)
	}
}

// Groups map on every request of a forward_auth session: a lower role
// applies at once, and no mapped group ends the session. Provisioning makes
// an account for a mapped stranger.
func TestForwardAuthGroups(t *testing.T) {
	h, box, srv := faHub(t, true)
	h.owner()
	var events eventLog
	h.svc.OnEvent(events.add)
	box.update(func(s *SignInSettings) {
		s.Mappings = []GroupMapping{{Group: "admins", Role: RoleAdmin}, {Group: "family", Role: RoleHousehold}}
		s.ForwardAuth.UserHeader = "X-Auth-User"
		s.ForwardAuth.GroupsHeader = "X-Auth-Groups"
	})
	alice, _ := h.svc.CreateUser(t.Context(), "alice", "alice password", RoleViewer)
	_, _ = h.svc.LinkForwardAuth(t.Context(), alice.ID, "alice")
	c := jarClient()
	who := func(groups string) (int, string) {
		return faGet(t, c, srv, "/api/private", nav(map[string]string{"X-Auth-User": "alice", "X-Auth-Groups": groups}))
	}
	if code, body := who("family, admins"); code != 200 || body != `{"method":"forward_auth","role":"admin","user":"alice"}`+"\n" {
		t.Fatalf("admins = %d %s", code, body)
	}
	if code, body := who("family"); code != 200 || body != `{"method":"forward_auth","role":"household","user":"alice"}`+"\n" {
		t.Fatalf("family = %d %s", code, body)
	}
	// Each actual change is recorded, from the proxy; the same groups again aren't.
	who("family")
	rc := events.ofKind(EventRoleFromGroups)
	if len(rc) != 2 || rc[0].FromRole != RoleViewer || rc[0].ToRole != RoleAdmin || rc[1].FromRole != RoleAdmin || rc[1].ToRole != RoleHousehold || rc[1].Method != MethodForwardAuth {
		t.Fatalf("role-change events = %+v", rc)
	}
	if code, _ := who("strangers"); code != http.StatusUnauthorized {
		t.Fatalf("no mapped group = %d", code)
	}

	box.update(func(s *SignInSettings) { s.Provision = true })
	code, body := faGet(t, jarClient(), srv, "/api/private", nav(map[string]string{"X-Auth-User": "carol", "X-Auth-Groups": "family"}))
	if code != 200 || body != `{"method":"forward_auth","role":"household","user":"carol"}`+"\n" {
		t.Fatalf("provisioned = %d %s", code, body)
	}
	if e := events.ofKind(EventSignedIn); e[len(e)-1].ToRole != RoleHousehold || !e[len(e)-1].Provisioned {
		t.Fatalf("provisioning sign-in event = %+v", e[len(e)-1])
	}
	// The groups header is shown in Settings → Sign-in as last received.
	if g := h.svc.LastForwardGroups(); g == nil || g.Header != "X-Auth-Groups" || g.Value != "family" || g.At.IsZero() {
		t.Fatalf("last groups = %+v", g)
	}
}

func mustURL(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		panic(err)
	}
	return u
}

// Only a top-level navigation opens a session. Any other request without a
// cookie reuses a live forward_auth session for the same user, address and
// user agent, or is served as the user with no session; neither writes a
// sign-in.
func TestForwardAuthSessionsOnlyOnNavigation(t *testing.T) {
	h, _, srv := faHub(t, true)
	h.owner()
	var events eventLog
	h.svc.OnEvent(events.add)
	alice, _ := h.svc.CreateUser(t.Context(), "alice", "alice password", RoleOperator)
	_, _ = h.svc.LinkForwardAuth(t.Context(), alice.ID, "alice")
	sessions := func() int {
		var n int
		_ = h.st.Reader().QueryRow(`SELECT count(*) FROM sessions WHERE user_id = ?`, alice.ID).Scan(&n)
		return n
	}
	who := func(c *http.Client, hdr map[string]string) map[string]string {
		t.Helper()
		code, body := faGet(t, c, srv, "/api/whoami", hdr)
		if code != 200 {
			t.Fatalf("whoami = %d %s", code, body)
		}
		var out map[string]string
		if err := json.Unmarshal([]byte(body), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	fetch := map[string]string{"Remote-User": "alice", "Sec-Fetch-Mode": "cors", "User-Agent": "test-browser"}

	// Script requests with no cookie: served as alice, no session, no sign-in.
	for range 3 {
		if got := who(jarClient(), fetch); got["user"] != "alice" || got["session"] != "" {
			t.Fatalf("fetch = %v", got)
		}
	}
	if n := sessions(); n != 0 || len(events.ofKind(EventSignedIn)) != 0 {
		t.Fatalf("fetches made %d sessions, %d sign-ins", n, len(events.ofKind(EventSignedIn)))
	}
	// Without Sec-Fetch-Mode, an Accept asking for HTML counts as a navigation.
	page := jarClient()
	got := who(page, map[string]string{"Remote-User": "alice", "Accept": "text/html,application/xhtml+xml", "User-Agent": "test-browser"})
	if got["session"] == "" || sessions() != 1 || len(events.ofKind(EventSignedIn)) != 1 {
		t.Fatalf("navigation = %v, %d sessions", got, sessions())
	}
	// Now a cookie-less fetch from the same browser reuses that session.
	if again := who(jarClient(), fetch); again["session"] != got["session"] {
		t.Fatalf("fetch after the navigation = %v, want session %s", again, got["session"])
	}
	// Another user agent doesn't.
	other := map[string]string{"Remote-User": "alice", "Sec-Fetch-Mode": "cors", "User-Agent": "another-browser"}
	if again := who(jarClient(), other); again["session"] != "" {
		t.Fatalf("fetch from another browser = %v", again)
	}
	if n := sessions(); n != 1 || len(events.ofKind(EventSignedIn)) != 1 {
		t.Fatalf("%d sessions, %d sign-ins", n, len(events.ofKind(EventSignedIn)))
	}
	// A sessionless request can't step up.
	req, _ := http.NewRequest("POST", srv.URL+"/api/auth/step-up", strings.NewReader(`{"password":"alice password"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Remote-User", "alice")
	req.Header.Set("User-Agent", "another-browser")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == 200 {
		t.Fatal("a sessionless request stepped up")
	}
}

// A linked user's groups apply whatever their session's method: a password
// session loses the role the proxy took away, and its sessions end when no
// group maps.
func TestForwardAuthGroupsBindEverySession(t *testing.T) {
	h, box, srv := faHub(t, true)
	h.owner()
	box.update(func(s *SignInSettings) {
		s.Mappings = []GroupMapping{{Group: "admins", Role: RoleAdmin}, {Group: "family", Role: RoleHousehold}}
	})
	alice, _ := h.svc.CreateUser(t.Context(), "alice", "alice password", RoleAdmin)
	_, _ = h.svc.LinkForwardAuth(t.Context(), alice.ID, "alice")
	pw := jarClient()
	if resp, _ := h.do(pw, "POST", "/api/auth/login", loginRequest{Username: "alice", Password: "alice password"}); resp.StatusCode != 200 {
		t.Fatal("password sign-in failed")
	}
	pw.Jar.SetCookies(mustURL(srv.URL), pw.Jar.Cookies(mustURL(h.srv.URL)))
	code, body := faGet(t, pw, srv, "/api/private", map[string]string{"Remote-User": "alice", "Remote-Groups": "family"})
	if code != 200 || body != `{"method":"password","role":"household","user":"alice"}`+"\n" {
		t.Fatalf("password session, family = %d %s", code, body)
	}
	if code, _ := faGet(t, pw, srv, "/api/private", map[string]string{"Remote-User": "alice", "Remote-Groups": "strangers"}); code != http.StatusUnauthorized {
		t.Fatalf("password session, no mapped group = %d", code)
	}
}

// A banned address gets no forward-auth session and no new account.
func TestForwardAuthBanned(t *testing.T) {
	h, box, srv := faHub(t, true)
	h.owner()
	var events eventLog
	h.svc.OnEvent(events.add)
	box.update(func(s *SignInSettings) {
		s.Mappings = []GroupMapping{{Group: "family", Role: RoleHousehold}}
		s.Provision = true
	})
	alice, _ := h.svc.CreateUser(t.Context(), "alice", "alice password", RoleViewer)
	_, _ = h.svc.LinkForwardAuth(t.Context(), alice.ID, "alice")
	if _, err := h.st.Writer().Exec(`INSERT INTO ip_bans (ip, banned_at, failures) VALUES ('127.0.0.1', 1, 5)`); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"alice", "carol"} {
		if code, _ := faGet(t, jarClient(), srv, "/api/private", nav(map[string]string{"Remote-User": name, "Remote-Groups": "family"})); code != http.StatusUnauthorized {
			t.Fatalf("%s from a banned address = %d", name, code)
		}
	}
	var n int
	_ = h.st.Reader().QueryRow(`SELECT count(*) FROM users WHERE username = 'carol'`).Scan(&n)
	if n != 0 {
		t.Fatal("a banned address provisioned an account")
	}
	if e := events.last(); e.Kind != EventSignInRefused || e.Reason != "address banned" || e.Method != MethodForwardAuth {
		t.Fatalf("last event = %+v", e)
	}
}

// A forward-auth header sent twice makes none of them count, and is
// recorded once an hour per client.
func TestForwardAuthRepeatedHeader(t *testing.T) {
	h, _, srv := faHub(t, true)
	h.owner()
	var events eventLog
	h.svc.OnEvent(events.add)
	alice, _ := h.svc.CreateUser(t.Context(), "alice", "alice password", RoleViewer)
	_, _ = h.svc.LinkForwardAuth(t.Context(), alice.ID, "alice")
	send := func(header string, values ...string) int {
		req, _ := http.NewRequest("GET", srv.URL+"/api/private", nil)
		req.Header.Set("Sec-Fetch-Mode", "navigate")
		req.Header.Set("Remote-User", "alice")
		req.Header.Del(header)
		for _, v := range values {
			req.Header.Add(header, v)
		}
		resp, err := jarClient().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := send("Remote-User", "alice", "bob"); code != http.StatusUnauthorized {
		t.Fatalf("two user headers = %d", code)
	}
	if code := send("Remote-Groups", "family", "admins"); code != http.StatusUnauthorized {
		t.Fatalf("two groups headers = %d", code)
	}
	d := events.ofKind(EventForwardAuthDuplicate)
	if len(d) != 1 || d[0].Reason != "Remote-User" || d[0].IP != "127.0.0.1" || d[0].Count != 1 {
		t.Fatalf("duplicate events = %+v", d)
	}
	h.clock.advance(time.Hour + time.Minute)
	send("Remote-User", "alice", "bob")
	if d := events.ofKind(EventForwardAuthDuplicate); len(d) != 2 || d[1].Count != 2 {
		t.Fatalf("duplicate events an hour later = %+v", d)
	}
	if code := send("Remote-User", "alice"); code != 200 {
		t.Fatalf("one user header = %d", code)
	}
}

// The groups header last received is kept for Settings → Sign-in, also when
// it names nobody; nothing is kept while forward-auth is off.
func TestForwardAuthGroupsSeen(t *testing.T) {
	h, box, srv := faHub(t, true)
	h.owner()
	box.update(func(s *SignInSettings) { s.ForwardAuth.Enabled = false })
	faGet(t, jarClient(), srv, "/api/echo", map[string]string{"Remote-Groups": "admins"})
	if g := h.svc.LastForwardGroups(); g != nil {
		t.Fatalf("seen with forward-auth off: %+v", g)
	}
	box.update(func(s *SignInSettings) { s.ForwardAuth.Enabled = true })
	faGet(t, jarClient(), srv, "/api/echo", map[string]string{"Remote-Groups": "admins, family"})
	if g := h.svc.LastForwardGroups(); g == nil || g.Header != "Remote-Groups" || g.Value != "admins, family" {
		t.Fatalf("last groups = %+v", g)
	}
}

// The throttles key untrusted peers by client (an IPv6 /64 is one), keep
// their own maps, and forget the oldest key when full instead of going
// quiet.
func TestForwardAuthThrottles(t *testing.T) {
	var th noteThrottle
	now := time.Now()
	for i := range maxNotes {
		if ok, _ := th.allow(fmt.Sprint("k", i), now.Add(time.Duration(i)), time.Hour); !ok {
			t.Fatalf("key %d refused", i)
		}
	}
	if ok, n := th.allow("new", now.Add(time.Hour/2), time.Hour); !ok || n != 1 {
		t.Fatal("a full throttle refused a new key")
	}
	if ok, _ := th.allow("k4095", now.Add(time.Hour/2), time.Hour); ok {
		t.Fatal("a recent key got through again")
	}
	if ok, _ := th.allow("k0", now.Add(time.Hour/2), time.Hour); !ok {
		t.Fatal("the oldest key wasn't the one forgotten")
	}
	if ok, _ := th.allow("k4095", now.Add(time.Hour/2+time.Minute), time.Hour); ok {
		t.Fatal("k4095 got through within the hour")
	}
	if ok, n := th.allow("k4095", now.Add(2*time.Hour), time.Hour); !ok || n != 3 {
		t.Fatalf("k4095 after the hour = %v, count %d; want the held ones counted", ok, n)
	}

	h, _, srv := faHub(t, false)
	h.owner()
	var events eventLog
	h.svc.OnEvent(events.add)
	// Refusals filling their throttle don't touch the untrusted one.
	for i := range maxNotes + 10 {
		h.svc.forward.refused.allow(fmt.Sprint("refuse|", i), h.clock.now(), untrustedEvery)
	}
	faGet(t, jarClient(), srv, "/api/echo", map[string]string{"Remote-User": "alice"})
	if n := len(events.ofKind(EventForwardAuthUntrusted)); n != 1 {
		t.Fatalf("untrusted events = %d", n)
	}
	// Two addresses in one IPv6 /64 are one client.
	r1 := httptest.NewRequest("GET", "/", nil)
	r1.RemoteAddr = "[2001:db8:1:2::1]:4000"
	r2 := httptest.NewRequest("GET", "/", nil)
	r2.RemoteAddr = "[2001:db8:1:2::99]:4000"
	h.svc.noteUntrusted(r1)
	h.svc.noteUntrusted(r2)
	if n := len(events.ofKind(EventForwardAuthUntrusted)); n != 2 {
		t.Fatalf("untrusted events after one /64 = %d, want 2", n)
	}
}
