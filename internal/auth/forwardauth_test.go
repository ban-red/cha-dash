package auth

import (
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"testing"

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
	code, body := faGet(t, c, srv, "/api/private", map[string]string{"Remote-User": "alice@idp"})
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
	code, body = faGet(t, c, srv, "/api/private", map[string]string{"Remote-User": "bob@idp"})
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
	if code, _ := faGet(t, c, srv, "/api/private", map[string]string{"Remote-User": "mallory@idp"}); code != http.StatusUnauthorized {
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
	if code, _ := faGet(t, jarClient(), srv, "/api/private", map[string]string{"Remote-User": "alice@idp"}); code != http.StatusUnauthorized {
		t.Fatalf("forward-auth off = %d", code)
	}
}

// Groups map on every request of a forward_auth session: a lower role
// applies at once, and no mapped group ends the session. Provisioning makes
// an account for a mapped stranger.
func TestForwardAuthGroups(t *testing.T) {
	h, box, srv := faHub(t, true)
	h.owner()
	box.update(func(s *SignInSettings) {
		s.Mappings = []GroupMapping{{Group: "admins", Role: RoleAdmin}, {Group: "family", Role: RoleHousehold}}
		s.ForwardAuth.UserHeader = "X-Auth-User"
		s.ForwardAuth.GroupsHeader = "X-Auth-Groups"
	})
	alice, _ := h.svc.CreateUser(t.Context(), "alice", "alice password", RoleViewer)
	_, _ = h.svc.LinkForwardAuth(t.Context(), alice.ID, "alice")
	c := jarClient()
	who := func(groups string) (int, string) {
		return faGet(t, c, srv, "/api/private", map[string]string{"X-Auth-User": "alice", "X-Auth-Groups": groups})
	}
	if code, body := who("family, admins"); code != 200 || body != `{"method":"forward_auth","role":"admin","user":"alice"}`+"\n" {
		t.Fatalf("admins = %d %s", code, body)
	}
	if code, body := who("family"); code != 200 || body != `{"method":"forward_auth","role":"household","user":"alice"}`+"\n" {
		t.Fatalf("family = %d %s", code, body)
	}
	if code, _ := who("strangers"); code != http.StatusUnauthorized {
		t.Fatalf("no mapped group = %d", code)
	}

	box.update(func(s *SignInSettings) { s.Provision = true })
	code, body := faGet(t, jarClient(), srv, "/api/private", map[string]string{"X-Auth-User": "carol", "X-Auth-Groups": "family"})
	if code != 200 || body != `{"method":"forward_auth","role":"household","user":"carol"}`+"\n" {
		t.Fatalf("provisioned = %d %s", code, body)
	}
}

func mustURL(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		panic(err)
	}
	return u
}
