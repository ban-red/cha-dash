package auth

// OIDC sign-in, linking and step-up (ADR-0022):
//
//	GET /api/auth/oidc/start?return=/path            sign in
//	GET /api/auth/oidc/start?link=1&return=/path     link to the signed-in user (needs a step-up)
//	GET /api/auth/oidc/start?step_up=1&return=/path  step up through the provider
//	GET /api/auth/oidc/callback?code&state           finish any of them
//
// Start writes an oidc_logins row (state, nonce, PKCE verifier, return path,
// the user when linking or stepping up), sets a cookie holding the state so
// the callback only finishes a flow this browser began, and redirects to
// the provider. The row lives 10 minutes and is taken (deleted) by the
// first callback that names it. The state's prefix says what the flow is
// for: "in." signs in, "ln." links, "su." steps up, "ts." is the owner's
// Test from Settings → Sign-in.
//
// Both routes are browser navigations, so they answer with redirects, never
// JSON: success goes to the return path, a failure to it (or to the sign-in
// page) with ?oidc_error=<code>. The return path is always a path on this
// origin; anything else becomes "/".
//
// Linking and stepping up start only from this origin's own pages (or a
// typed address): another site can't send a signed-in browser through a
// flow it chose. Step-up through the provider follows the step-up rule like
// the other methods, at the start and again at the callback.
//
// The callback audits only what got as far as a flow this hub began: a
// missing or unknown state, or any callback while OIDC is off, just
// redirects. Refused sign-ins are recorded once per client and reason every
// ten minutes, with a count of the ones in between.

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"dash.cha.sh/internal/httpx"
)

const (
	oidcLoginTTL = 10 * time.Minute
	oidcCookie   = "chadash_oidc"
	oidcStart    = "/api/auth/oidc/start"
	oidcCallback = "/api/auth/oidc/callback"
)

// OIDC flow purposes, as the state's prefix.
const (
	purposeSignIn = "in"
	purposeLink   = "ln"
	purposeStepUp = "su"
	purposeTest   = "ts"
)

// Codes the redirect carries in ?oidc_error=. The web maps each to a sentence.
const (
	OIDCErrOff         = "oidc_off"         // OIDC isn't configured
	OIDCErrExpired     = "expired"          // no such sign-in in flight, or it lapsed
	OIDCErrState       = "state_mismatch"   // the browser didn't begin this one
	OIDCErrProvider    = "provider_error"   // the provider said no, or couldn't be reached
	OIDCErrBadToken    = "bad_token"        // signature, issuer, audience, expiry or nonce
	OIDCErrUnlinked    = "unlinked"         // no Cha Dash user for this identity
	OIDCErrNoGroup     = "no_group"         // no group maps to a role
	OIDCErrDisabled    = "disabled"         // the linked account is disabled
	OIDCErrLinkedElse  = "already_linked"   // linked to another account
	OIDCErrWrongUser   = "wrong_user"       // the session changed during the flow
	OIDCErrStale       = "stale_auth"       // step-up: auth_time missing or too old
	OIDCErrNotLinked   = "not_linked"       // step-up with an identity not linked to you
	OIDCErrStepUp      = "step_up_required" // linking needs a step-up first
	OIDCErrSignedOut   = "not_signed_in"    // linking or stepping up needs a session
	OIDCErrBanned      = "ip_banned"
	OIDCErrRateLimited = "rate_limited"
	OIDCErrInternal    = "internal"
	// OIDCErrNoStepUp: the step-up rule doesn't offer the provider to this
	// account (it has a passkey or a password, or passkeys are required).
	OIDCErrNoStepUp = "step_up_unavailable"
	// OIDCErrCrossSite: linking or stepping up was started from another site.
	OIDCErrCrossSite = "cross_site"
)

// refusalEvery: one refused-sign-in record per client and reason this often.
const refusalEvery = 10 * time.Minute

// refusalReasons are the audit reasons for refused sign-ins.
var refusalReasons = map[string]string{
	OIDCErrUnlinked: "unlinked",
	OIDCErrNoGroup:  "no mapped group",
	OIDCErrBadToken: "bad token",
	OIDCErrDisabled: "account disabled",
	OIDCErrState:    "state mismatch",
	OIDCErrProvider: "provider error",
	OIDCErrExpired:  "expired",
	OIDCErrBanned:   "address banned",
}

// OIDCTestResult is what the last Test sign-in found, for Settings → Sign-in.
type OIDCTestResult struct {
	At    time.Time `json:"at"`
	OK    bool      `json:"ok"`
	Error string    `json:"error,omitempty"`
	// AuthTime says whether the ID token carried auth_time, and
	// AuthTimeFresh whether it was within StepUpAuthAge, as step-up needs.
	AuthTime      bool     `json:"auth_time"`
	AuthTimeFresh bool     `json:"auth_time_fresh"`
	Groups        []string `json:"groups"`
	MappedRole    string   `json:"mapped_role"`
	Email         string   `json:"email,omitempty"`
	Username      string   `json:"username,omitempty"`
	// Linked names the Cha Dash user this identity is linked to, if any.
	Linked string `json:"linked,omitempty"`
}

type oidcTests struct {
	mu   sync.Mutex
	last *OIDCTestResult
}

// LastOIDCTest is the last Test sign-in's result, or nil.
func (s *Service) LastOIDCTest() *OIDCTestResult {
	s.oidcTest.mu.Lock()
	defer s.oidcTest.mu.Unlock()
	if s.oidcTest.last == nil {
		return nil
	}
	c := *s.oidcTest.last
	return &c
}

func (s *Service) registerOIDC(mux *http.ServeMux) {
	mux.HandleFunc("GET "+oidcStart, s.handleOIDCStart)
	mux.HandleFunc("GET "+oidcCallback, s.handleOIDCCallback)
}

// SafeReturn keeps p only when it's a path on this origin: it starts with
// one slash, not two or a backslash, and parses with no scheme or host.
// Anything else is "/".
func SafeReturn(p string) string {
	if p == "" || len(p) > 1024 || p[0] != '/' || strings.HasPrefix(p, "//") || strings.HasPrefix(p, "/\\") {
		return "/"
	}
	if strings.ContainsFunc(p, func(r rune) bool { return r < 0x20 || r == 0x7f || r == '\\' }) {
		return "/"
	}
	u, err := url.Parse(p)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || u.Opaque != "" {
		return "/"
	}
	return u.RequestURI()
}

// withParam adds key=value to a same-origin path's query.
func withParam(p, key, value string) string {
	u, err := url.Parse(p)
	if err != nil {
		return "/?" + url.Values{key: {value}}.Encode()
	}
	q := u.Query()
	q.Set(key, value)
	u.RawQuery = q.Encode()
	return u.RequestURI()
}

// redirectURI is the callback the provider sends people back to: on
// CHADASH_PUBLIC_URL when it's set, else on the address in use.
func (s *Service) redirectURI(r *http.Request) string {
	if s.publicURL != "" {
		return strings.TrimSuffix(s.publicURL, "/") + oidcCallback
	}
	scheme := "http"
	if s.proxies.IsHTTPS(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host + oidcCallback
}

// RedirectURI is the callback URL to register with the provider, as the
// hub would send it for r.
func (s *Service) RedirectURI(r *http.Request) string { return s.redirectURI(r) }

// oidcFail redirects a failed browser flow with its code, to a path on
// this origin only.
func oidcFail(w http.ResponseWriter, r *http.Request, to, code string) {
	http.Redirect(w, r, withParam(SafeReturn(to), "oidc_error", code), http.StatusSeeOther) //nolint:gosec // G710: SafeReturn keeps only a path on this origin
}

func (s *Service) handleOIDCStart(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ret := SafeReturn(q.Get("return"))
	purpose := purposeSignIn
	switch {
	case q.Get("link") == "1":
		purpose = purposeLink
	case q.Get("step_up") == "1":
		purpose = purposeStepUp
	}
	fail := func(code string) {
		to := ret
		if purpose == purposeSignIn {
			to = "/"
		}
		oidcFail(w, r, to, code)
	}
	if ok, _ := s.limiter.Allow("oidc|" + httpx.ClientKey(s.proxies.ClientIP(r))); !ok {
		fail(OIDCErrRateLimited)
		return
	}
	var userID string
	if purpose != purposeSignIn {
		if !startedHere(r) {
			fail(OIDCErrCrossSite)
			return
		}
		id, ok := s.Identify(r)
		if !ok {
			fail(OIDCErrSignedOut)
			return
		}
		userID = id.User.ID
		switch purpose {
		case purposeLink:
			id.IP = s.proxies.ClientIP(r).String()
			if err := s.linkAllowed(r.WithContext(context.WithValue(r.Context(), ctxKey{}, id))); err != nil {
				fail(OIDCErrStepUp)
				return
			}
		case purposeStepUp:
			if code := s.oidcStepUpOffered(r, userID); code != "" {
				fail(code)
				return
			}
		}
	} else {
		ban, err := s.Banned(r.Context(), s.proxies.ClientIP(r))
		if err != nil {
			s.log.Error("could not check the ban list", "err", err)
			fail(OIDCErrInternal)
			return
		}
		if ban {
			fail(OIDCErrBanned)
			return
		}
	}
	to, err := s.beginOIDC(w, r, purpose, userID, ret)
	if err != nil {
		code := OIDCErrProvider
		if errors.Is(err, errOIDCOff) {
			code = OIDCErrOff
		} else if _, ok := errors.AsType[*errProvider](err); !ok {
			s.log.Error("could not start an OIDC sign-in", "err", err)
			code = OIDCErrInternal
		} else {
			s.log.Warn("could not start an OIDC sign-in", "err", err)
		}
		fail(code)
		return
	}
	http.Redirect(w, r, to, http.StatusSeeOther) //nolint:gosec // G710: the provider's authorization endpoint, from the issuer the owner set
}

var errOIDCOff = errors.New("auth: OIDC isn't configured")

// startedHere: a navigation this origin's pages began, or one typed into
// the address bar. Sec-Fetch-Site says so; a browser that doesn't send it
// must send a Referer on this host instead. (The hub's own pages send no
// Referer, under Referrer-Policy: no-referrer, so such a browser can't link
// or step up through the provider; current browsers all send the header.)
func startedHere(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "same-origin", "none":
		return true
	case "":
		ref, err := url.Parse(r.Header.Get("Referer"))
		return err == nil && ref.Host != "" && strings.EqualFold(ref.Host, r.Host)
	}
	return false
}

// oidcStepUpOffered says why the step-up rule doesn't let userID step up
// at the provider from r, or "" when it does.
func (s *Service) oidcStepUpOffered(r *http.Request, userID string) string {
	c, err := s.stepUpOptions(r, userID)
	if err != nil {
		s.log.Error("could not work out the step-up methods", "err", err)
		return OIDCErrInternal
	}
	if !c.oidc {
		return OIDCErrNoStepUp
	}
	return ""
}

// BeginOIDCTest starts the owner's Test sign-in from Settings → Sign-in:
// the same flow, asking the provider for a fresh sign-in (prompt=login,
// max_age=0) so the result says whether step-up can work. Nobody is signed
// in or linked; the callback records LastOIDCTest and returns to ret. The
// caller has already decided the user may (an owner-only route).
func (s *Service) BeginOIDCTest(w http.ResponseWriter, r *http.Request, userID, ret string) (string, error) {
	return s.beginOIDC(w, r, purposeTest, userID, SafeReturn(ret))
}

// beginOIDC stores a flow and returns the provider URL to send the browser to.
func (s *Service) beginOIDC(w http.ResponseWriter, r *http.Request, purpose, userID, ret string) (string, error) {
	ctx := r.Context()
	set := s.SignIn()
	if !set.OIDCReady() {
		return "", errOIDCOff
	}
	d, err := s.oidc.discover(ctx, set.OIDC.Issuer, s.now())
	if err != nil {
		return "", err
	}
	state := purpose + "." + rand.Text()
	nonce := rand.Text()
	verifier := rand.Text() + rand.Text() // 52 characters of base32: within PKCE's 43–128
	now := s.now()
	var link any
	if userID != "" {
		link = userID
	}
	err = s.st.WriteTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM oidc_logins WHERE expires_at <= ?`, now.UnixMilli()); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO oidc_logins (state, nonce, verifier, return_to, link_user, expires_at) VALUES (?, ?, ?, ?, ?, ?)`,
			state, nonce, verifier, ret, link, now.Add(oidcLoginTTL).UnixMilli())
		return err
	})
	if err != nil {
		return "", fmt.Errorf("auth: store the sign-in: %w", err)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     oidcCookie,
		Value:    state,
		Path:     oidcCallback,
		MaxAge:   int(oidcLoginTTL / time.Second),
		HttpOnly: true,
		Secure:   s.proxies.IsHTTPS(r),
		SameSite: http.SameSiteLaxMode, // sent on the provider's top-level redirect back
	})
	v := url.Values{
		"response_type":         {"code"},
		"client_id":             {set.OIDC.ClientID},
		"redirect_uri":          {s.redirectURI(r)},
		"scope":                 {strings.Join(set.OIDC.Scopes, " ")},
		"state":                 {state},
		"nonce":                 {nonce},
		"code_challenge":        {pkceChallenge(verifier)},
		"code_challenge_method": {"S256"},
	}
	if purpose == purposeStepUp || purpose == purposeTest {
		v.Set("prompt", "login")
		v.Set("max_age", "0")
	}
	sep := "?"
	if strings.Contains(d.AuthorizationEndpoint, "?") {
		sep = "&"
	}
	return d.AuthorizationEndpoint + sep + v.Encode(), nil
}

// oidcLogin is a taken oidc_logins row.
type oidcLogin struct {
	purpose, nonce, verifier, returnTo, linkUser string
}

// takeLogin deletes and returns the live row for state.
func (s *Service) takeLogin(ctx context.Context, state string) (oidcLogin, bool, error) {
	var l oidcLogin
	var link sql.NullString
	err := s.st.Writer().QueryRowContext(ctx, `DELETE FROM oidc_logins WHERE state = ? AND expires_at > ? RETURNING nonce, verifier, return_to, link_user`,
		state, s.now().UnixMilli()).Scan(&l.nonce, &l.verifier, &l.returnTo, &link)
	if errors.Is(err, sql.ErrNoRows) {
		return l, false, nil
	}
	if err != nil {
		return l, false, err
	}
	l.purpose, _, _ = strings.Cut(state, ".")
	l.linkUser = link.String
	l.returnTo = SafeReturn(l.returnTo)
	return l, true, nil
}

func (s *Service) clearOIDCCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: oidcCookie, Value: "", Path: oidcCallback, MaxAge: -1, HttpOnly: true, Secure: s.proxies.IsHTTPS(r), SameSite: http.SameSiteLaxMode})
}

func (s *Service) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()
	state := q.Get("state")
	if ok, _ := s.limiter.Allow("oidc|" + httpx.ClientKey(s.proxies.ClientIP(r))); !ok {
		oidcFail(w, r, "/", OIDCErrRateLimited)
		return
	}
	s.clearOIDCCookie(w, r)
	// None of these is audited: anyone can call this URL, and none of
	// them is a flow this hub began.
	if !s.SignIn().OIDCReady() {
		oidcFail(w, r, "/", OIDCErrOff)
		return
	}
	if state == "" || len(state) > 64 {
		oidcFail(w, r, "/", OIDCErrExpired)
		return
	}
	l, ok, err := s.takeLogin(ctx, state)
	if err != nil {
		s.log.Error("could not read the sign-in in flight", "err", err)
		oidcFail(w, r, "/", OIDCErrInternal)
		return
	}
	if !ok {
		oidcFail(w, r, "/", OIDCErrExpired)
		return
	}
	// Where a failure goes: the sign-in page for a sign-in, else back.
	failTo := l.returnTo
	if l.purpose == purposeSignIn {
		failTo = "/"
	}
	// The state is bound to the browser that began the flow: a callback
	// link someone else hands you can't sign you in as them.
	c, cerr := r.Cookie(oidcCookie)
	if cerr != nil || subtle.ConstantTimeCompare([]byte(c.Value), []byte(state)) != 1 {
		s.refuseFlow(w, r, l, failTo, OIDCErrState, nil)
		return
	}
	if l.purpose == purposeSignIn {
		ban, err := s.Banned(ctx, s.proxies.ClientIP(r))
		if err != nil {
			s.log.Error("could not check the ban list", "err", err)
			oidcFail(w, r, failTo, OIDCErrInternal)
			return
		}
		if ban {
			oidcFail(w, r, failTo, OIDCErrBanned)
			return
		}
	}
	if e := q.Get("error"); e != "" {
		s.log.Info("the provider refused the sign-in", "error", clip(e, 64))
		s.refuseFlow(w, r, l, failTo, OIDCErrProvider, nil)
		return
	}
	set := s.SignIn()
	if !set.OIDCReady() {
		oidcFail(w, r, failTo, OIDCErrOff)
		return
	}
	d, err := s.oidc.discover(ctx, set.OIDC.Issuer, s.now())
	if err != nil {
		s.log.Warn("OIDC discovery failed", "err", err)
		s.refuseFlow(w, r, l, failTo, OIDCErrProvider, err)
		return
	}
	secret, err := s.clientSecret(ctx)
	if err != nil {
		s.log.Error("could not read the OIDC client secret", "err", err)
		oidcFail(w, r, failTo, OIDCErrInternal)
		return
	}
	raw, err := s.oidc.exchange(ctx, d, set.OIDC.ClientID, secret, q.Get("code"), l.verifier, s.redirectURI(r))
	if err != nil {
		s.log.Warn("OIDC code exchange failed", "err", err)
		s.refuseFlow(w, r, l, failTo, OIDCErrProvider, err)
		return
	}
	claims, err := s.oidc.verify(ctx, d, raw, set.OIDC.ClientID, l.nonce, set.OIDC.GroupClaim, s.now())
	if err != nil {
		s.log.Warn("OIDC ID token refused", "err", err)
		s.refuseFlow(w, r, l, failTo, OIDCErrBadToken, err)
		return
	}
	switch l.purpose {
	case purposeSignIn:
		s.oidcSignIn(w, r, set, claims, l)
	case purposeLink:
		s.oidcLink(w, r, claims, l)
	case purposeStepUp:
		s.oidcStepUp(w, r, claims, l)
	case purposeTest:
		s.finishTest(w, r, set, claims, l)
	default:
		oidcFail(w, r, "/", OIDCErrExpired)
	}
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// refuseFlow fails a flow; a refused sign-in is also an event (audited as
// auth.oidc.refused), and a test records what went wrong.
func (s *Service) refuseFlow(w http.ResponseWriter, r *http.Request, l oidcLogin, to, code string, cause error) {
	switch l.purpose {
	case purposeSignIn:
		s.refuseSignIn(w, r, "", to, code, nil)
		return
	case purposeTest:
		msg := code
		if p, ok := errors.AsType[*errProvider](cause); ok {
			msg = p.msg
		} else if cause != nil && code == OIDCErrBadToken {
			msg = "the ID token was refused: " + clip(cause.Error(), 160)
		}
		s.recordTest(&OIDCTestResult{At: s.now().UTC(), Error: msg, Groups: []string{}})
	case purposeStepUp:
		s.emit(Event{Kind: EventStepUpFailed, UserID: l.linkUser, Reason: "provider: " + refusalReason(code), Method: MethodOIDC, Request: r, IP: s.proxies.ClientIP(r).String()})
	}
	oidcFail(w, r, to, code)
}

func refusalReason(code string) string {
	if r, ok := refusalReasons[code]; ok {
		return r
	}
	return code
}

// refuseSignIn answers a refused sign-in and emits EventSignInRefused, at
// most once per client and reason every refusalEvery. userID is the
// account it would have been, when known.
func (s *Service) refuseSignIn(w http.ResponseWriter, r *http.Request, userID, to, code string, u *User) {
	ip := s.proxies.ClientIP(r)
	e := Event{Kind: EventSignInRefused, UserID: userID, Method: MethodOIDC, Reason: refusalReason(code), Request: r, IP: ip.String()}
	if ok, n := s.oidcRefusals.allow(httpx.ClientKey(ip)+"|"+e.Reason, s.now(), refusalEvery); ok {
		if u != nil {
			e.Request = r.WithContext(context.WithValue(r.Context(), ctxKey{}, Identity{User: *u, IP: e.IP}))
		}
		e.Count = n
		s.log.Info("OIDC sign-in refused", "reason", e.Reason, "client", e.IP, "count", n)
		s.emit(e)
	}
	oidcFail(w, r, to, code)
}

// identityUser returns the user (iss, sub) is linked to, if any, with
// whether that account is disabled.
func (s *Service) identityUser(ctx context.Context, q queryRower, iss, sub string) (User, bool, bool, error) {
	var u User
	var disabled sql.NullInt64
	err := q.QueryRowContext(ctx, `SELECT u.id, u.username, u.role, u.disabled_at FROM oidc_identities i JOIN users u ON u.id = i.user_id
		WHERE i.issuer = ? AND i.subject = ?`, iss, sub).Scan(&u.ID, &u.Username, &u.Role, &disabled)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, false, false, nil
	}
	if err != nil {
		return User{}, false, false, err
	}
	return u, true, disabled.Valid, nil
}

func (s *Service) oidcSignIn(w http.ResponseWriter, r *http.Request, set SignInSettings, c idClaims, l oidcLogin) {
	ctx := r.Context()
	provisioned := false
	u, linked, disabled, err := s.identityUser(ctx, s.st.Reader(), c.Issuer, c.Subject)
	if err != nil {
		s.log.Error("could not look up the identity", "err", err)
		oidcFail(w, r, "/", OIDCErrInternal)
		return
	}
	switch {
	case linked && disabled:
		s.refuseSignIn(w, r, u.ID, "/", OIDCErrDisabled, &u)
		return
	case linked:
		role, ok, err := s.applyGroups(r, u, set, c.Groups, MethodOIDC)
		if err != nil {
			s.log.Error("could not apply the group mapping", "err", err)
			oidcFail(w, r, "/", OIDCErrInternal)
			return
		}
		if !ok {
			s.refuseSignIn(w, r, u.ID, "/", OIDCErrNoGroup, &u)
			return
		}
		u.Role = role
		if _, err := s.st.Writer().ExecContext(ctx, `UPDATE oidc_identities SET last_used_at = ?, email = ? WHERE issuer = ? AND subject = ?`,
			s.now().UnixMilli(), c.Email, c.Issuer, c.Subject); err != nil {
			s.log.Warn("could not record the identity's use", "err", err)
		}
	default:
		role := set.MappedRole(c.Groups)
		if !set.Provision || role == "" || s.SetupRequired() {
			s.refuseSignIn(w, r, "", "/", OIDCErrUnlinked, nil)
			return
		}
		provisioned = true
		nu, err := s.provision(ctx, []string{c.Username, emailLocal(c.Email)}, role, func(tx *sql.Tx, userID string, now int64) error {
			_, err := tx.ExecContext(ctx, `INSERT INTO oidc_identities (issuer, subject, user_id, email, created_at, last_used_at) VALUES (?, ?, ?, ?, ?, ?)`,
				c.Issuer, c.Subject, userID, c.Email, now, now)
			return err
		})
		if err != nil {
			s.log.Error("could not create an account for a first OIDC sign-in", "err", err)
			oidcFail(w, r, "/", OIDCErrInternal)
			return
		}
		u = nu
		s.log.Info("account created on first OIDC sign-in", "user", u.Username, "role", u.Role)
	}
	s.forgive(s.proxies.ClientIP(r), u.Username)
	if _, _, err := s.startSession(w, r, u, MethodOIDC, provisioned); err != nil {
		s.log.Error("could not start session", "err", err)
		oidcFail(w, r, "/", OIDCErrInternal)
		return
	}
	http.Redirect(w, r, l.returnTo, http.StatusSeeOther)
}

func emailLocal(e string) string {
	local, _, _ := strings.Cut(e, "@")
	return local
}

// applyGroups sets u's role from groups under set, the owner excepted, and
// reports whether u may sign in: false when mappings exist and none
// matches, in which case u's sessions end and EventUserChanged closes its
// sockets. With no mappings, roles are Cha Dash's and nothing changes. It
// clears an admin's override (role_override): a sign-in through the
// provider or the proxy makes the role the groups' again. An actual change
// emits EventRoleFromGroups (audited) with method, then EventUserChanged.
func (s *Service) applyGroups(r *http.Request, u User, set SignInSettings, groups []string, method string) (string, bool, error) {
	ctx := r.Context()
	if len(set.Mappings) == 0 {
		return u.Role, true, nil
	}
	var override bool
	if err := s.st.Reader().QueryRowContext(ctx, `SELECT role, role_override FROM users WHERE id = ?`, u.ID).Scan(&u.Role, &override); err != nil {
		return u.Role, false, err
	}
	if u.Role == RoleOwner {
		return u.Role, true, nil
	}
	role := set.MappedRole(groups)
	if role == "" {
		res, err := s.st.Writer().ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, u.ID)
		if err != nil {
			return u.Role, false, err
		}
		// A proxy repeats the refused name on every request: only the first
		// one that ended sessions needs to close sockets.
		if n, _ := res.RowsAffected(); n > 0 {
			s.log.Info("sign-in refused: no group maps to a role; the account's sessions end", "user", u.Username)
			s.emit(Event{Kind: EventUserChanged, UserID: u.ID})
		}
		return u.Role, false, nil
	}
	if role == u.Role && !override {
		return role, true, nil
	}
	res, err := s.st.Writer().ExecContext(ctx, `UPDATE users SET role = ?, role_override = 0, updated_at = ? WHERE id = ? AND role = ?`,
		role, s.now().UnixMilli(), u.ID, u.Role)
	if err != nil {
		return u.Role, false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return u.Role, true, nil // changed in between (became the owner, say); the next sign-in applies it
	}
	if role == u.Role {
		return role, true, nil // only the override cleared
	}
	s.log.Info("role set from groups", "user", u.Username, "from", u.Role, "to", role, "method", method)
	ip := s.proxies.ClientIP(r).String()
	who := Identity{User: User{ID: u.ID, Username: u.Username, Role: role}, IP: ip}
	s.emit(Event{Kind: EventRoleFromGroups, UserID: u.ID, Method: method, FromRole: u.Role, ToRole: role, IP: ip,
		Request: r.WithContext(context.WithValue(ctx, ctxKey{}, who))})
	s.emit(Event{Kind: EventUserChanged, UserID: u.ID})
	return role, true, nil
}

// provision creates a passwordless account named after the first usable
// candidate (or "user"), with a numeric suffix if it's taken, and runs
// link in the same transaction.
func (s *Service) provision(ctx context.Context, candidates []string, role string, link func(tx *sql.Tx, userID string, now int64) error) (User, error) {
	if role == RoleOwner || !ValidRole(role) {
		return User{}, errOwnerByTransfer
	}
	base := "user"
	for _, c := range candidates {
		if c = strings.TrimSpace(c); usernameRE.MatchString(c) {
			base = c
			break
		}
	}
	u := User{ID: "u_" + strings.ToLower(rand.Text()), Role: role}
	err := s.st.WriteTx(ctx, func(tx *sql.Tx) error {
		name := ""
		for i := 1; i <= 50 && name == ""; i++ {
			cand := base
			if i > 1 {
				suffix := fmt.Sprintf("-%d", i)
				cand = clip(base, 64-len(suffix)) + suffix
			}
			var n int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE username = ?`, cand).Scan(&n); err != nil {
				return err
			}
			if n == 0 {
				name = cand
			}
		}
		if name == "" {
			return ErrUserExists
		}
		u.Username = name
		now := s.now().UnixMilli()
		if _, err := tx.ExecContext(ctx, `INSERT INTO users (id, username, password_hash, role, created_at, updated_at) VALUES (?, ?, NULL, ?, ?, ?)`,
			u.ID, u.Username, u.Role, now, now); err != nil {
			return err
		}
		return link(tx, u.ID, now)
	})
	if err != nil {
		return User{}, err
	}
	return u, nil
}

// flowSession is the session behind r, which must be l's user's.
func (s *Service) flowSession(r *http.Request, l oidcLogin) (Identity, string) {
	id, ok := s.Identify(r)
	switch {
	case !ok:
		return id, OIDCErrSignedOut
	case l.linkUser == "" || id.User.ID != l.linkUser:
		return id, OIDCErrWrongUser
	}
	id.IP = s.proxies.ClientIP(r).String()
	return id, ""
}

func (s *Service) oidcLink(w http.ResponseWriter, r *http.Request, c idClaims, l oidcLogin) {
	ctx := r.Context()
	id, code := s.flowSession(r, l)
	if code != "" {
		oidcFail(w, r, l.returnTo, code)
		return
	}
	// The step-up that let the link start may have lapsed on the way.
	if err := s.linkAllowed(r.WithContext(context.WithValue(ctx, ctxKey{}, id))); err != nil {
		oidcFail(w, r, l.returnTo, OIDCErrStepUp)
		return
	}
	var other string
	err := s.st.WriteTx(ctx, func(tx *sql.Tx) error {
		other = ""
		u, linked, _, err := s.identityUser(ctx, tx, c.Issuer, c.Subject)
		if err != nil {
			return err
		}
		if linked {
			other = u.ID
			if u.ID == id.User.ID {
				_, err = tx.ExecContext(ctx, `UPDATE oidc_identities SET email = ? WHERE issuer = ? AND subject = ?`, c.Email, c.Issuer, c.Subject)
			}
			return err
		}
		now := s.now().UnixMilli()
		_, err = tx.ExecContext(ctx, `INSERT INTO oidc_identities (issuer, subject, user_id, email, created_at, last_used_at) VALUES (?, ?, ?, ?, ?, ?)`,
			c.Issuer, c.Subject, id.User.ID, c.Email, now, now)
		return err
	})
	if err != nil {
		s.log.Error("could not link the identity", "err", err)
		oidcFail(w, r, l.returnTo, OIDCErrInternal)
		return
	}
	if other != "" && other != id.User.ID {
		oidcFail(w, r, l.returnTo, OIDCErrLinkedElse)
		return
	}
	if other == "" {
		s.log.Info("identity linked", "user", id.User.Username, "issuer", c.Issuer)
		s.emit(Event{Kind: EventIdentityLinked, UserID: id.User.ID, SessionID: id.Session.ID, Method: MethodOIDC, Issuer: c.Issuer, Subject: c.Subject,
			Request: r.WithContext(context.WithValue(ctx, ctxKey{}, id)), IP: id.IP})
	}
	http.Redirect(w, r, withParam(l.returnTo, "oidc", "linked"), http.StatusSeeOther)
}

func (s *Service) oidcStepUp(w http.ResponseWriter, r *http.Request, c idClaims, l oidcLogin) {
	ctx := r.Context()
	id, code := s.flowSession(r, l)
	if code != "" {
		oidcFail(w, r, l.returnTo, code)
		return
	}
	req := r.WithContext(context.WithValue(ctx, ctxKey{}, id))
	fail := func(code, reason string) {
		s.log.Info("step-up failed", "user", id.User.Username, "method", MethodOIDC, "reason", reason)
		s.emit(Event{Kind: EventStepUpFailed, UserID: id.User.ID, SessionID: id.Session.ID, Method: MethodOIDC, Reason: reason, Request: req, IP: id.IP})
		oidcFail(w, r, l.returnTo, code)
	}
	u, linked, disabled, err := s.identityUser(ctx, s.st.Reader(), c.Issuer, c.Subject)
	if err != nil {
		s.log.Error("could not look up the identity", "err", err)
		oidcFail(w, r, l.returnTo, OIDCErrInternal)
		return
	}
	if !linked || disabled || u.ID != id.User.ID {
		fail(OIDCErrNotLinked, "an identity not linked to this account")
		return
	}
	// The rule, or the account's passkeys and password, may have changed
	// since the start.
	switch code := s.oidcStepUpOffered(req, id.User.ID); code {
	case "":
	case OIDCErrInternal:
		oidcFail(w, r, l.returnTo, code)
		return
	default:
		fail(code, "the step-up rule doesn't offer the provider to this account")
		return
	}
	if !authTimeFresh(c.AuthTime, s.now()) {
		fail(OIDCErrStale, "auth_time missing or older than a minute")
		return
	}
	if code := s.recordStepUp(req, id, MethodOIDC); code != "" {
		oidcFail(w, r, l.returnTo, code)
		return
	}
	http.Redirect(w, r, withParam(l.returnTo, "oidc", "step_up"), http.StatusSeeOther)
}

// authTimeFresh: the provider says the person signed in within
// StepUpAuthAge (a little clock skew forgiven both ways).
func authTimeFresh(t, now time.Time) bool {
	return !t.IsZero() && now.Sub(t) < StepUpAuthAge && t.Sub(now) < tokenLeeway
}

func (s *Service) finishTest(w http.ResponseWriter, r *http.Request, set SignInSettings, c idClaims, l oidcLogin) {
	res := &OIDCTestResult{At: s.now().UTC(), OK: true, AuthTime: !c.AuthTime.IsZero(), AuthTimeFresh: authTimeFresh(c.AuthTime, s.now()),
		Groups: c.Groups, MappedRole: set.MappedRole(c.Groups), Email: c.Email, Username: c.Username}
	if res.Groups == nil {
		res.Groups = []string{}
	}
	if u, linked, _, err := s.identityUser(r.Context(), s.st.Reader(), c.Issuer, c.Subject); err == nil && linked {
		res.Linked = u.Username
	}
	s.recordTest(res)
	http.Redirect(w, r, withParam(l.returnTo, "oidc", "tested"), http.StatusSeeOther)
}

func (s *Service) recordTest(res *OIDCTestResult) {
	s.oidcTest.mu.Lock()
	defer s.oidcTest.mu.Unlock()
	s.oidcTest.last = res
}
