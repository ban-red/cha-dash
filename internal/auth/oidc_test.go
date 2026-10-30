package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"dash.cha.sh/internal/httpx"
)

// ── A fake OIDC provider ─────────────────────────────────────────────────────

const (
	idpClientID = "chadash-test"
	idpSecret   = "FAKE-client-secret-0000"
)

var idpKeys = sync.OnceValue(func() [2]*rsa.PrivateKey {
	var out [2]*rsa.PrivateKey
	for i := range out {
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			panic(err)
		}
		out[i] = k
	}
	return out
})

// fakeIdP serves discovery, authorize (which signs in whoever the test
// says, at once), token and JWKS, signing ID tokens with a test key.
type fakeIdP struct {
	t   *testing.T
	srv *httptest.Server

	mu    sync.Mutex
	codes map[string]authorizeReq
	// The person the next sign-in is, and what the token says.
	sub, email, username string
	groups               []string
	authTime             time.Time // zero: no auth_time claim
	// Knobs for broken tokens.
	aud       string        // overrides the audience
	expIn     time.Duration // exp relative to now (default an hour)
	nonce     string        // overrides the nonce
	otherKey  bool          // signs with a key the JWKS doesn't list
	badPKCE   bool          // pretends the code was issued for another challenge
	claimsSup []string
	// lastAuthorize is the query the hub sent to authorize.
	lastAuthorize url.Values
	// tokenAuth is how the hub authenticated at the token endpoint.
	tokenAuth string
}

type authorizeReq struct {
	challenge, nonce, redirect string
}

func newFakeIdP(t *testing.T) *fakeIdP {
	p := &fakeIdP{t: t, codes: map[string]authorizeReq{}, sub: "sub-alice", email: "alice@lab.example", username: "alice"}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		doc := map[string]any{
			"issuer":                                p.srv.URL,
			"authorization_endpoint":                p.srv.URL + "/authorize",
			"token_endpoint":                        p.srv.URL + "/token",
			"jwks_uri":                              p.srv.URL + "/jwks",
			"code_challenge_methods_supported":      []string{"S256"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
		}
		p.mu.Lock()
		if p.claimsSup != nil {
			doc["claims_supported"] = p.claimsSup
		}
		p.mu.Unlock()
		_ = json.NewEncoder(w).Encode(doc)
	})
	mux.HandleFunc("GET /jwks", func(w http.ResponseWriter, r *http.Request) {
		k := idpKeys()[0].PublicKey
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "kid": "k1", "use": "sig", "alg": "RS256",
			"n": base64.RawURLEncoding.EncodeToString(k.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(k.E)).Bytes()),
		}}})
	})
	mux.HandleFunc("GET /authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		p.mu.Lock()
		p.lastAuthorize = q
		code := rand.Text()
		ch := q.Get("code_challenge")
		if p.badPKCE {
			ch = pkceChallenge("another verifier entirely, not the hub's one")
		}
		p.codes[code] = authorizeReq{challenge: ch, nonce: q.Get("nonce"), redirect: q.Get("redirect_uri")}
		p.mu.Unlock()
		if q.Get("client_id") != idpClientID || q.Get("code_challenge_method") != "S256" || q.Get("response_type") != "code" {
			http.Error(w, "bad authorize request", http.StatusBadRequest)
			return
		}
		to := q.Get("redirect_uri") + "?" + url.Values{"code": {code}, "state": {q.Get("state")}}.Encode()
		http.Redirect(w, r, to, http.StatusFound)
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		p.mu.Lock()
		defer p.mu.Unlock()
		id, secret, ok := r.BasicAuth()
		p.tokenAuth = "none"
		if ok {
			p.tokenAuth = "basic"
		}
		if !ok || id != idpClientID || secret != idpSecret {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_client"})
			return
		}
		req, found := p.codes[r.Form.Get("code")]
		delete(p.codes, r.Form.Get("code"))
		if !found || req.redirect != r.Form.Get("redirect_uri") || pkceChallenge(r.Form.Get("code_verifier")) != req.challenge {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
			return
		}
		now := time.Now()
		exp := p.expIn
		if exp == 0 {
			exp = time.Hour
		}
		claims := jwt.MapClaims{"iss": p.srv.URL, "sub": p.sub, "aud": idpClientID, "iat": now.Unix(), "exp": now.Add(exp).Unix(),
			"nonce": req.nonce, "email": p.email, "preferred_username": p.username, "groups": p.groups}
		if p.aud != "" {
			claims["aud"] = p.aud
		}
		if p.nonce != "" {
			claims["nonce"] = p.nonce
		}
		if !p.authTime.IsZero() {
			claims["auth_time"] = p.authTime.Unix()
		}
		tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
		tok.Header["kid"] = "k1"
		key := idpKeys()[0]
		if p.otherKey {
			key = idpKeys()[1]
		}
		signed, err := tok.SignedString(key)
		if err != nil {
			p.t.Error(err)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"id_token": signed, "access_token": "FAKE-access", "token_type": "Bearer"})
	})
	p.srv = httptest.NewServer(mux)
	t.Cleanup(p.srv.Close)
	return p
}

func (p *fakeIdP) set(f func(p *fakeIdP)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	f(p)
}

// settingsBox holds the sign-in settings a test changes as it goes.
type settingsBox struct {
	mu  sync.Mutex
	set SignInSettings
}

func (b *settingsBox) get() SignInSettings {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.set
}

func (b *settingsBox) update(f func(*SignInSettings)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	f(&b.set)
}

// newOIDCHarness is a hub with OIDC pointed at a fake provider, linking
// allowed with a step-up (as authz decides it in the hub), and an owner
// signed in on the returned client.
func newOIDCHarness(t *testing.T) (*harness, *fakeIdP, *settingsBox, *http.Client) {
	t.Helper()
	idp := newFakeIdP(t)
	h := newHarness(t)
	// Every flow is two requests on the sign-in budget; tests run many.
	h.svc.limiter = httpx.NewLimiter(10000, time.Millisecond)
	box := &settingsBox{set: DefaultSignIn()}
	box.set.OIDC = OIDCSettings{Enabled: true, Name: "Test IdP", Issuer: idp.srv.URL, ClientID: idpClientID, Scopes: DefaultScopes, GroupClaim: "groups"}
	h.svc.SetSignIn(box.get, func(context.Context) (string, error) { return idpSecret, nil })
	h.svc.SetLinkCheck(func(r *http.Request) error {
		id, _ := FromContext(r.Context())
		if !id.Session.StepUpUntil.After(h.svc.now()) {
			return ErrNoSession
		}
		return nil
	})
	return h, idp, box, h.owner()
}

// flow runs path on the hub with c, following every redirect (to the
// provider and back), and returns where it ended.
func (h *harness) flow(c *http.Client, path string) *url.URL {
	h.t.Helper()
	resp, _ := h.doRaw(c, "GET", path, nil, nil)
	return resp.Request.URL
}

func oidcResult(u *url.URL) (string, string) {
	return u.Query().Get("oidc"), u.Query().Get("oidc_error")
}

func (h *harness) stepUpWithPassword(c *http.Client) {
	h.t.Helper()
	if resp, body := h.do(c, "POST", "/api/auth/step-up", map[string]string{"password": "correct horse battery"}); resp.StatusCode != 200 {
		h.t.Fatalf("step-up = %d %v", resp.StatusCode, body)
	}
}

func (h *harness) sessionMethod(c *http.Client) (string, string) {
	h.t.Helper()
	resp, body := h.do(c, "GET", "/api/auth/session", nil)
	if resp.StatusCode != 200 {
		return "", ""
	}
	return body["session"].(map[string]any)["method"].(string), body["user"].(map[string]any)["username"].(string)
}

// ── Tests ────────────────────────────────────────────────────────────────────

func TestSafeReturn(t *testing.T) {
	for in, want := range map[string]string{
		"":                            "/",
		"/":                           "/",
		"/settings/security":          "/settings/security",
		"/boards/b1?x=1":              "/boards/b1?x=1",
		"https://evil.example/":       "/",
		"//evil.example/path":         "/",
		"/\\evil.example":             "/",
		"javascript:alert(1)":         "/",
		"evil.example":                "/",
		"/ok\nLocation: x":            "/",
		"http:/evil.example":          "/",
		"/%2F%2Fevil.example":         "/%2F%2Fevil.example",
		"/a/../../evil.example?q=1#x": "/a/../../evil.example?q=1",
	} {
		if got := SafeReturn(in); got != want {
			t.Errorf("SafeReturn(%q) = %q, want %q", in, got, want)
		}
	}
}

// Unlinked → refused; link (with a step-up) → signs in as the owner, with
// method oidc; the return path is kept, and one off the origin is not.
func TestOIDCLinkThenSignIn(t *testing.T) {
	h, idp, _, owner := newOIDCHarness(t)
	var events eventLog
	h.svc.OnEvent(events.add)

	// Nobody is linked yet.
	anon := h.client()
	if _, code := oidcResult(h.flow(anon, "/api/auth/oidc/start?return=/boards/b1")); code != OIDCErrUnlinked {
		t.Fatalf("unlinked sign-in: oidc_error = %q", code)
	}
	if e := events.last(); e.Kind != EventSignInRefused || e.Reason != "unlinked" || e.Method != MethodOIDC {
		t.Fatalf("refusal event = %+v", e)
	}

	// Linking needs a step-up first.
	if _, code := oidcResult(h.flow(owner, "/api/auth/oidc/start?link=1&return=/settings/security")); code != OIDCErrStepUp {
		t.Fatalf("link without step-up: oidc_error = %q", code)
	}
	h.stepUpWithPassword(owner)
	end := h.flow(owner, "/api/auth/oidc/start?link=1&return=/settings/security")
	if ok, code := oidcResult(end); ok != "linked" || code != "" || end.Path != "/settings/security" {
		t.Fatalf("link ended at %s", end)
	}
	if e := events.last(); e.Kind != EventIdentityLinked || e.Issuer != idp.srv.URL || e.Subject != "sub-alice" {
		t.Fatalf("link event = %+v", e)
	}
	ids, err := h.svc.ListIdentities(t.Context(), mustUserID(t, h, "owner"))
	if err != nil || len(ids) != 1 || ids[0].Email != "alice@lab.example" || ids[0].Kind != MethodOIDC {
		t.Fatalf("identities = %+v, %v", ids, err)
	}
	if idp.tokenAuth != "basic" {
		t.Fatalf("token endpoint auth = %q, want basic", idp.tokenAuth)
	}

	// Now the identity signs in, to the return path.
	c := h.client()
	end = h.flow(c, "/api/auth/oidc/start?return=/boards/b1")
	if _, code := oidcResult(end); code != "" || end.Path != "/boards/b1" {
		t.Fatalf("sign-in ended at %s", end)
	}
	if m, u := h.sessionMethod(c); m != MethodOIDC || u != "owner" {
		t.Fatalf("session = %s %s", m, u)
	}
	signed := events.last()
	if signed.Kind != EventSignedIn || signed.Method != MethodOIDC {
		t.Fatalf("signed_in event = %+v", signed)
	}
	if id, ok := FromContext(signed.Request.Context()); !ok || id.User.Username != "owner" {
		t.Fatal("the signed_in event's request doesn't carry who signed in")
	}

	// A return path off the origin lands at /.
	for _, bad := range []string{"https://evil.example/", "//evil.example/", "/\\evil.example"} {
		c := h.client()
		end := h.flow(c, "/api/auth/oidc/start?return="+url.QueryEscape(bad))
		if end.Host != strings.TrimPrefix(h.srv.URL, "http://") || end.Path != "/" {
			t.Fatalf("return %q ended at %s", bad, end)
		}
	}
}

func mustUserID(t *testing.T, h *harness, username string) string {
	t.Helper()
	var id string
	if err := h.st.Reader().QueryRow(`SELECT id FROM users WHERE username = ?`, username).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func linkIdentity(t *testing.T, h *harness, userID, iss, sub string) {
	t.Helper()
	now := time.Now().UnixMilli()
	if _, err := h.st.Writer().Exec(`INSERT INTO oidc_identities (issuer, subject, user_id, email, created_at, last_used_at) VALUES (?, ?, ?, '', ?, ?)`,
		iss, sub, userID, now, now); err != nil {
		t.Fatal(err)
	}
}

// State, nonce and PKCE mismatches, a wrong audience, an expired token,
// a signature from another key, a lapsed or replayed state: each refuses.
func TestOIDCRefusesBadFlows(t *testing.T) {
	h, idp, _, _ := newOIDCHarness(t)
	linkIdentity(t, h, mustUserID(t, h, "owner"), idp.srv.URL, "sub-alice")

	cases := []struct {
		name string
		knob func(*fakeIdP)
		want string
	}{
		{"nonce", func(p *fakeIdP) { p.nonce = "not-the-hubs-nonce" }, OIDCErrBadToken},
		{"pkce", func(p *fakeIdP) { p.badPKCE = true }, OIDCErrProvider},
		{"audience", func(p *fakeIdP) { p.aud = "another-client" }, OIDCErrBadToken},
		{"expired", func(p *fakeIdP) { p.expIn = -2 * time.Hour }, OIDCErrBadToken},
		{"signature", func(p *fakeIdP) { p.otherKey = true }, OIDCErrBadToken},
	}
	for _, tc := range cases {
		idp.set(func(p *fakeIdP) { p.nonce, p.badPKCE, p.aud, p.expIn, p.otherKey = "", false, "", 0, false })
		idp.set(tc.knob)
		c := h.client()
		if _, code := oidcResult(h.flow(c, "/api/auth/oidc/start")); code != tc.want {
			t.Errorf("%s: oidc_error = %q, want %q", tc.name, code, tc.want)
		}
		if m, _ := h.sessionMethod(c); m != "" {
			t.Errorf("%s: signed in anyway", tc.name)
		}
	}
	idp.set(func(p *fakeIdP) { p.nonce, p.badPKCE, p.aud, p.expIn, p.otherKey = "", false, "", 0, false })

	// The state cookie binds the flow to the browser that began it.
	starter := h.client()
	starter.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, _ := h.doRaw(starter, "GET", "/api/auth/oidc/start", nil, nil)
	toIdP := resp.Header.Get("Location")
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	r2, err := noFollow.Get(toIdP)
	if err != nil {
		t.Fatal(err)
	}
	r2.Body.Close()
	callback := r2.Header.Get("Location")
	victim := h.client()
	r3, err := victim.Get(callback)
	if err != nil {
		t.Fatal(err)
	}
	r3.Body.Close()
	if _, code := oidcResult(r3.Request.URL); code != OIDCErrState {
		t.Fatalf("someone else's callback: oidc_error = %q", code)
	}
	// And a state is good once: replaying the callback finds nothing.
	starterFollow := h.client()
	starterFollow.Jar = starter.Jar
	r4, err := starterFollow.Get(callback)
	if err != nil {
		t.Fatal(err)
	}
	r4.Body.Close()
	if _, code := oidcResult(r4.Request.URL); code != OIDCErrExpired {
		t.Fatalf("replayed callback: oidc_error = %q", code)
	}

	// A flow left for more than 10 minutes lapses.
	slow := h.client()
	slow.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, _ = h.doRaw(slow, "GET", "/api/auth/oidc/start", nil, nil)
	r5, err := noFollow.Get(resp.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	r5.Body.Close()
	h.clock.advance(11 * time.Minute)
	slow.CheckRedirect = nil
	r6, err := slow.Get(r5.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	r6.Body.Close()
	if _, code := oidcResult(r6.Request.URL); code != OIDCErrExpired {
		t.Fatalf("late callback: oidc_error = %q", code)
	}
}

// Provisioning creates an account with the mapped role; a group change
// lowers it at the next sign-in; losing every mapped group refuses and ends
// the account's sessions; the owner is never changed.
func TestOIDCGroupsAndProvisioning(t *testing.T) {
	h, idp, box, owner := newOIDCHarness(t)
	var events eventLog
	h.svc.OnEvent(events.add)
	box.update(func(s *SignInSettings) {
		s.Mappings = []GroupMapping{{Group: "lab-admins", Role: RoleAdmin}, {Group: "lab-ops", Role: RoleOperator}, {Group: "family", Role: RoleHousehold}}
	})

	// Provisioning is off: a mapped group alone doesn't make an account.
	idp.set(func(p *fakeIdP) { p.sub, p.username, p.groups = "sub-sam", "sam", []string{"lab-ops", "family"} })
	if _, code := oidcResult(h.flow(h.client(), "/api/auth/oidc/start")); code != OIDCErrUnlinked {
		t.Fatalf("provisioning off: %q", code)
	}
	box.update(func(s *SignInSettings) { s.Provision = true })

	// On, with no mapped group: still refused.
	idp.set(func(p *fakeIdP) { p.groups = []string{"strangers"} })
	if _, code := oidcResult(h.flow(h.client(), "/api/auth/oidc/start")); code != OIDCErrUnlinked {
		t.Fatalf("provisioning, no group: %q", code)
	}

	// On, with groups: the highest mapped role wins.
	idp.set(func(p *fakeIdP) { p.groups = []string{"family", "lab-ops"} })
	sam := h.client()
	h.flow(sam, "/api/auth/oidc/start")
	if m, u := h.sessionMethod(sam); m != MethodOIDC || u != "sam" {
		t.Fatalf("provisioned session = %q %q", m, u)
	}
	info, err := h.svc.GetUser(t.Context(), mustUserID(t, h, "sam"))
	if err != nil || info.Role != RoleOperator || !info.RoleFromGroups {
		t.Fatalf("provisioned user = %+v, %v", info, err)
	}
	// Its role can't be changed in Cha Dash any more.
	viewer := RoleViewer
	if _, err := h.svc.UpdateUser(t.Context(), info.ID, &viewer, nil); !errors.Is(err, ErrRoleFromGroups) {
		t.Fatalf("UpdateUser on a role from groups = %v", err)
	}

	// A group change lowers the role at the next sign-in.
	idp.set(func(p *fakeIdP) { p.groups = []string{"family"} })
	sam2 := h.client()
	h.flow(sam2, "/api/auth/oidc/start")
	if info, _ := h.svc.GetUser(t.Context(), info.ID); info.Role != RoleHousehold {
		t.Fatalf("role after the group change = %s", info.Role)
	}
	if !slicesContainsKind(events.kinds(), string(EventUserChanged)) {
		t.Fatal("a role change from groups didn't emit user.changed")
	}

	// Losing every mapped group: refused, and the account's sessions end.
	idp.set(func(p *fakeIdP) { p.groups = nil })
	if _, code := oidcResult(h.flow(h.client(), "/api/auth/oidc/start")); code != OIDCErrNoGroup {
		t.Fatalf("no mapped group: %q", code)
	}
	if m, _ := h.sessionMethod(sam); m != "" {
		t.Fatal("sam's open session survived losing every group")
	}
	if e := events.last(); e.Kind != EventSignInRefused || e.Reason != "no mapped group" {
		t.Fatalf("last event = %+v", e)
	}

	// The owner is never changed, and needs no group.
	h.stepUpWithPassword(owner)
	idp.set(func(p *fakeIdP) { p.sub, p.username, p.groups = "sub-owner", "owner", []string{"family"} })
	h.flow(owner, "/api/auth/oidc/start?link=1")
	for _, groups := range [][]string{{"family"}, nil} {
		idp.set(func(p *fakeIdP) { p.groups = groups })
		c := h.client()
		h.flow(c, "/api/auth/oidc/start")
		if m, u := h.sessionMethod(c); m != MethodOIDC || u != "owner" {
			t.Fatalf("owner with groups %v: %q %q", groups, m, u)
		}
		if info, _ := h.svc.GetUser(t.Context(), mustUserID(t, h, "owner")); info.Role != RoleOwner || info.RoleFromGroups {
			t.Fatalf("owner changed: %+v", info)
		}
	}
}

func slicesContainsKind(kinds []string, k string) bool {
	return slices.Contains(kinds, k)
}

// An account with neither passkey nor password steps up at the provider,
// which is asked for a fresh sign-in; the grant needs auth_time under 60 s.
func TestOIDCStepUp(t *testing.T) {
	h, idp, box, _ := newOIDCHarness(t)
	box.update(func(s *SignInSettings) {
		s.Mappings = []GroupMapping{{Group: "lab-ops", Role: RoleOperator}}
		s.Provision = true
	})
	idp.set(func(p *fakeIdP) { p.sub, p.username, p.groups = "sub-sam", "sam", []string{"lab-ops"} })
	sam := h.client()
	h.flow(sam, "/api/auth/oidc/start")

	_, sess := h.do(sam, "GET", "/api/auth/session", nil)
	if m := sess["step_up_methods"].([]any); len(m) != 1 || m[0] != StepUpMethodOIDC {
		t.Fatalf("step_up_methods = %v", m)
	}
	resp, begin := h.do(sam, "POST", "/api/auth/step-up/begin", nil)
	if resp.StatusCode != 200 || begin["method"] != StepUpMethodOIDC || !strings.HasPrefix(begin["url"].(string), "/api/auth/oidc/start?step_up=1") {
		t.Fatalf("step-up begin = %d %v", resp.StatusCode, begin)
	}

	grant := func() any {
		_, s := h.do(sam, "GET", "/api/auth/session", nil)
		return s["step_up_until"]
	}
	// No auth_time: refused.
	if _, code := oidcResult(h.flow(sam, "/api/auth/oidc/start?step_up=1&return=/settings/users")); code != OIDCErrStale || grant() != nil {
		t.Fatalf("no auth_time: %q %v", code, grant())
	}
	if q := idp.lastAuthorize; q.Get("prompt") != "login" || q.Get("max_age") != "0" {
		t.Fatalf("step-up authorize query = %v", q)
	}
	// An old auth_time: refused.
	idp.set(func(p *fakeIdP) { p.authTime = time.Now().Add(-5 * time.Minute) })
	if _, code := oidcResult(h.flow(sam, "/api/auth/oidc/start?step_up=1")); code != OIDCErrStale {
		t.Fatalf("old auth_time: %q", code)
	}
	// Someone else's identity: refused.
	idp.set(func(p *fakeIdP) { p.sub, p.authTime = "sub-stranger", time.Now() })
	if _, code := oidcResult(h.flow(sam, "/api/auth/oidc/start?step_up=1")); code != OIDCErrNotLinked {
		t.Fatalf("stranger's identity: %q", code)
	}
	// A fresh sign-in: granted, back where it started.
	idp.set(func(p *fakeIdP) { p.sub, p.authTime = "sub-sam", time.Now() })
	end := h.flow(sam, "/api/auth/oidc/start?step_up=1&return=/settings/users")
	if ok, code := oidcResult(end); ok != "step_up" || code != "" || end.Path != "/settings/users" || grant() == nil {
		t.Fatalf("fresh step-up ended at %s, grant %v", end, grant())
	}
	// Signed out, step-up through the provider isn't on offer.
	if _, code := oidcResult(h.flow(h.client(), "/api/auth/oidc/start?step_up=1")); code != OIDCErrSignedOut {
		t.Fatalf("signed out: %q", code)
	}
	// A sign-in sends neither prompt nor max_age.
	h.flow(h.client(), "/api/auth/oidc/start")
	if q := idp.lastAuthorize; q.Has("prompt") || q.Has("max_age") {
		t.Fatalf("sign-in authorize query = %v", q)
	}
}

// Unlinking keeps the account's last way in; a linked identity can't be
// linked to a second account.
func TestOIDCUnlinkAndDoubleLink(t *testing.T) {
	h, idp, box, owner := newOIDCHarness(t)
	box.update(func(s *SignInSettings) {
		s.Mappings = []GroupMapping{{Group: "lab-ops", Role: RoleOperator}}
		s.Provision = true
	})
	idp.set(func(p *fakeIdP) { p.sub, p.username, p.groups = "sub-sam", "sam", []string{"lab-ops"} })
	h.flow(h.client(), "/api/auth/oidc/start")
	samID := mustUserID(t, h, "sam")
	if err := h.svc.UnlinkIdentity(t.Context(), samID, idp.srv.URL, "sub-sam"); !IsLastSignInMethod(err) {
		t.Fatalf("unlinking the only way in = %v", err)
	}
	// The owner tries to link sam's identity.
	h.stepUpWithPassword(owner)
	if _, code := oidcResult(h.flow(owner, "/api/auth/oidc/start?link=1")); code != OIDCErrLinkedElse {
		t.Fatalf("double link: %q", code)
	}
	// The owner links their own, then unlinks it: they still have a password.
	idp.set(func(p *fakeIdP) { p.sub = "sub-owner" })
	h.flow(owner, "/api/auth/oidc/start?link=1")
	ownerID := mustUserID(t, h, "owner")
	if err := h.svc.UnlinkIdentity(t.Context(), ownerID, idp.srv.URL, "sub-owner"); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.UnlinkIdentity(t.Context(), ownerID, idp.srv.URL, "sub-owner"); !errors.Is(err, ErrIdentityNotFound) {
		t.Fatalf("second unlink = %v", err)
	}
}

// GET /api/setup tells the sign-in page about the provider and the form.
func TestPublicSignInInfo(t *testing.T) {
	h, _, box, _ := newOIDCHarness(t)
	_, body := h.do(h.client(), "GET", "/api/setup", nil)
	si := body["sign_in"].(map[string]any)
	if si["oidc"].(map[string]any)["name"] != "Test IdP" || si["password_form"] != true {
		t.Fatalf("sign_in = %v", si)
	}
	box.update(func(s *SignInSettings) { s.HidePasswordForm = true })
	_, body = h.do(h.client(), "GET", "/api/setup", nil)
	if body["sign_in"].(map[string]any)["password_form"] != false {
		t.Fatalf("hidden form: %v", body["sign_in"])
	}
	// With nothing else to sign in with, the form never hides.
	box.update(func(s *SignInSettings) { s.OIDC.Enabled = false })
	_, body = h.do(h.client(), "GET", "/api/setup", nil)
	if si := body["sign_in"].(map[string]any); si["password_form"] != true || si["oidc"] != nil {
		t.Fatalf("OIDC off: %v", si)
	}
	// The hidden form still signs in (break-glass).
	box.update(func(s *SignInSettings) { s.OIDC.Enabled = true })
	if resp, _ := h.do(h.client(), "POST", "/api/auth/login", loginRequest{Username: "owner", Password: "correct horse battery"}); resp.StatusCode != 200 {
		t.Fatalf("password sign-in with the form hidden = %d", resp.StatusCode)
	}
}

// The owner's Test sign-in reports what the token carries and signs nobody in.
func TestOIDCTestSignIn(t *testing.T) {
	h, idp, box, _ := newOIDCHarness(t)
	box.update(func(s *SignInSettings) { s.Mappings = []GroupMapping{{Group: "lab-ops", Role: RoleOperator}} })
	idp.set(func(p *fakeIdP) { p.groups, p.authTime = []string{"lab-ops", "other"}, time.Now() })
	c := h.client()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/settings/sign-in/test", nil)
	req.Host = strings.TrimPrefix(h.srv.URL, "http://")
	to, err := h.svc.BeginOIDCTest(rec, req, mustUserID(t, h, "owner"), "/settings/security")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(h.srv.URL)
	c.Jar.SetCookies(u, rec.Result().Cookies())
	resp, err := c.Get(to)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if ok, _ := oidcResult(resp.Request.URL); ok != "tested" {
		t.Fatalf("test ended at %s", resp.Request.URL)
	}
	got := h.svc.LastOIDCTest()
	if got == nil || !got.OK || !got.AuthTime || !got.AuthTimeFresh || got.MappedRole != RoleOperator || len(got.Groups) != 2 {
		t.Fatalf("test result = %+v", got)
	}
	if m, _ := h.sessionMethod(c); m != "" {
		t.Fatal("a test sign-in signed someone in")
	}
}

func TestSignInSettingsValidate(t *testing.T) {
	ok := DefaultSignIn()
	ok.OIDC = OIDCSettings{Enabled: true, Issuer: "https://id.lab.example", ClientID: "dash"}
	ok.Mappings = []GroupMapping{{Group: "admins", Role: RoleAdmin}}
	if err := ok.Normalize().Validate(); err != nil {
		t.Fatal(err)
	}
	if n := ok.Normalize(); n.OIDC.Scopes[0] != "openid" || n.ForwardAuth.UserHeader != DefaultUserHeader {
		t.Fatalf("normalized = %+v", n)
	}
	for name, f := range map[string]func(*SignInSettings){
		"owner mapping":   func(s *SignInSettings) { s.Mappings = []GroupMapping{{Group: "x", Role: RoleOwner}} },
		"unknown role":    func(s *SignInSettings) { s.Mappings = []GroupMapping{{Group: "x", Role: "god"}} },
		"twice":           func(s *SignInSettings) { s.Mappings = []GroupMapping{{"x", RoleAdmin}, {"x", RoleViewer}} },
		"no client":       func(s *SignInSettings) { s.OIDC.ClientID = "" },
		"bad issuer":      func(s *SignInSettings) { s.OIDC.Issuer = "id.lab.example" },
		"provision bare":  func(s *SignInSettings) { s.Provision, s.Mappings = true, nil },
		"bad header":      func(s *SignInSettings) { s.ForwardAuth.UserHeader = "Remote User" },
		"header clash":    func(s *SignInSettings) { s.ForwardAuth.GroupsHeader = s.ForwardAuth.UserHeader },
		"bad group claim": func(s *SignInSettings) { s.OIDC.GroupClaim = "a b" },
	} {
		s := ok
		s.Mappings = append([]GroupMapping{}, ok.Mappings...)
		f(&s)
		if err := s.Normalize().Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if got := (SignInSettings{Mappings: []GroupMapping{{"a", RoleViewer}, {"b", RoleAdmin}, {"c", RoleOperator}}}).MappedRole([]string{"c", "a"}); got != RoleOperator {
		t.Fatalf("MappedRole = %q", got)
	}
}
