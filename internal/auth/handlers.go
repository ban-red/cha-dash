package auth

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strconv"
	"time"

	"dash.cha.sh/internal/httpx"
)

// Identity is the signed-in user attached to a request by Require, or the
// user an API token acts for, attached by RequireToken.
type Identity struct {
	User User
	// Session is the cookie session; zero for an API token.
	Session Session
	// Token is the API token the request carried; nil for a session.
	Token *TokenRef
	// IP is the client address, behind trusted proxies, for audit records.
	IP string
}

type ctxKey struct{}

// FromContext returns the identity Require attached, if any.
func FromContext(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(ctxKey{}).(Identity)
	return id, ok
}

// Register mounts the auth endpoints:
//
//	GET  /api/setup          {"required": bool, "dev_login": true (devlogin builds only)}
//	POST /api/setup          {"token","username","password"} → 201 {"user"} + session cookie
//	POST /api/auth/login     {"username","password"} → 200 {"user"} + session cookie
//	POST /api/auth/logout    → 204, cookie cleared
//	GET  /api/auth/session   → 200 {"user","session","step_up_until","step_up_methods"} or 401
//
// plus the passkey endpoints under /api/auth/passkeys (registerPasskeys),
// step-up under /api/auth/step-up (registerStepUp) and OIDC under
// /api/auth/oidc (registerOIDC; oidc_http.go). GET /api/setup also says how
// the sign-in page may sign in: {"sign_in": {"oidc"?: {"name"}, "password_form"}}.
// Binaries built with -tags devlogin also mount POST /api/auth/dev-login.
func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/setup", s.handleSetupStatus)
	mux.HandleFunc("POST /api/setup", s.handleSetup)
	mux.HandleFunc("POST /api/auth/login", s.handleLogin)
	mux.HandleFunc("POST /api/auth/logout", s.handleLogout)
	mux.Handle("GET /api/auth/session", s.Require(http.HandlerFunc(s.handleSession)))
	s.registerPasskeys(mux)
	s.registerStepUp(mux)
	s.registerOIDC(mux)
	s.registerDevLogin(mux)
}

// Require rejects requests without a valid session cookie with 401 and
// attaches the Identity for the rest. A request that already carries one
// (authz.Guard runs Require in front of every signed-in route) passes
// straight through, so wrapping a route again costs nothing.
func (s *Service) Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := FromContext(r.Context()); ok {
			next.ServeHTTP(w, r)
			return
		}
		c, err := r.Cookie(CookieName)
		if err != nil {
			httpx.Error(w, http.StatusUnauthorized, "unauthenticated", "sign in required")
			return
		}
		sess, u, err := s.lookupSession(r.Context(), c.Value)
		if err != nil {
			if !errors.Is(err, ErrNoSession) {
				s.log.Error("session lookup failed", "err", err)
				httpx.Error(w, http.StatusInternalServerError, "internal", "internal error")
				return
			}
			httpx.Error(w, http.StatusUnauthorized, "unauthenticated", "sign in required")
			return
		}
		if s.touchSession(r.Context(), &sess) {
			s.setCookie(w, r, c.Value, sess.ExpiresAt)
		}
		id := Identity{User: u, Session: sess, IP: s.proxies.ClientIP(r).String()}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, id)))
	})
}

// StartSession signs u in on this response and emits EventSignedIn with
// method. Every sign-in method (password, passkey, OIDC, forward-auth and
// dev) ends here.
func (s *Service) StartSession(w http.ResponseWriter, r *http.Request, u User, method string) (Session, error) {
	_, sess, err := s.startSession(w, r, u, method)
	return sess, err
}

// startSession is StartSession, also returning the cookie token.
func (s *Service) startSession(w http.ResponseWriter, r *http.Request, u User, method string) (string, Session, error) {
	ip := s.proxies.ClientIP(r).String()
	token, sess, err := s.createSession(r.Context(), u.ID, method, ip, r.UserAgent())
	if err != nil {
		return "", Session{}, err
	}
	s.setCookie(w, r, token, sess.ExpiresAt)
	// The event's request carries who just signed in, so the audit log can
	// name them.
	id := Identity{User: u, Session: sess, IP: ip}
	s.emit(Event{Kind: EventSignedIn, UserID: u.ID, SessionID: sess.ID, Method: method, Request: r.WithContext(context.WithValue(r.Context(), ctxKey{}, id)), IP: ip})
	return token, sess, nil
}

// setCookie writes the session cookie; its lifetime tracks the session's expiry.
func (s *Service) setCookie(w http.ResponseWriter, r *http.Request, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/",
		Expires:  expires,
		MaxAge:   int(expires.Sub(s.now()) / time.Second),
		HttpOnly: true,
		Secure:   s.proxies.IsHTTPS(r),
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Service) clearCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.proxies.IsHTTPS(r),
		SameSite: http.SameSiteLaxMode,
	})
}

// rateLimited applies the per-client attempt budget and writes 429 when spent.
func (s *Service) rateLimited(w http.ResponseWriter, r *http.Request, scope string) bool {
	ok, wait := s.limiter.Allow(scope + "|" + httpx.ClientKey(s.proxies.ClientIP(r)))
	if ok {
		return false
	}
	tooManyAttempts(w, wait)
	return true
}

// banned answers 403 ip_banned when the client's address is banned (bans.go).
func (s *Service) banned(w http.ResponseWriter, r *http.Request) bool {
	ip := s.proxies.ClientIP(r)
	ban, err := s.Banned(r.Context(), ip)
	if err != nil {
		s.internalError(w, "could not check the ban list", err)
		return true
	}
	if ban {
		s.log.Info("sign-in refused: client is banned", "client", banKey(ip))
		ipBanned(w)
	}
	return ban
}

func ipBanned(w http.ResponseWriter) {
	httpx.Error(w, http.StatusForbidden, "ip_banned", "this address is banned after too many wrong passwords; an admin can clear it")
}

func tooManyAttempts(w http.ResponseWriter, wait time.Duration) {
	w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
	httpx.Error(w, http.StatusTooManyRequests, "rate_limited", "too many attempts; try again later")
}

func (s *Service) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	status := map[string]any{"required": s.SetupRequired(), "sign_in": s.PublicSignInInfo()}
	if devLoginBuilt {
		status["dev_login"] = true
	}
	httpx.WriteJSON(w, http.StatusOK, status)
}

type setupRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *Service) handleSetup(w http.ResponseWriter, r *http.Request) {
	if s.rateLimited(w, r, "setup") {
		return
	}
	if !s.rebindSafe(r) {
		httpx.Error(w, http.StatusForbidden, "unknown_host",
			"Cha Dash doesn't know the name "+requestHost(r)+". Open it at its IP address, or set CHADASH_PUBLIC_URL to the address you use, then create the owner.")
		return
	}
	var req setupRequest
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	u, err := s.Setup(r.Context(), req.Username, req.Password)
	var verr *ValidationError
	switch {
	case errors.Is(err, ErrSetupComplete):
		httpx.Error(w, http.StatusConflict, "setup_complete", "setup has already been completed")
		return
	case errors.As(err, &verr):
		httpx.Error(w, http.StatusUnprocessableEntity, "invalid", verr.Msg)
		return
	case err != nil:
		s.log.Error("setup failed", "err", err)
		httpx.Error(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	if _, err := s.StartSession(w, r, u, MethodPassword); err != nil {
		s.log.Error("could not start session after setup", "err", err)
		httpx.Error(w, http.StatusInternalServerError, "internal", "account created, but signing in failed; sign in again")
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]User{"user": u})
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *Service) handleLogin(w http.ResponseWriter, r *http.Request) {
	if s.rateLimited(w, r, "login") || s.banned(w, r) {
		return
	}
	var req loginRequest
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	u, err := s.Login(r.Context(), req.Username, req.Password)
	if limited, ok := errors.AsType[*AccountLimitedError](err); ok {
		s.log.Warn("sign-in refused: too many wrong passwords for this account", "user_id", limited.UserID, "client", s.proxies.ClientIP(r).String())
		tooManyAttempts(w, limited.Wait)
		return
	}
	if errors.Is(err, ErrBadCredentials) {
		// Usernames aren't logged: people sometimes type a password into that field.
		ip := s.proxies.ClientIP(r)
		s.log.Info("sign-in failed", "client", ip.String())
		banned, err := s.strike(r.Context(), ip, req.Username)
		if err != nil {
			s.log.Error("could not record the wrong password", "err", err)
		}
		if banned {
			ipBanned(w)
			return
		}
		httpx.Error(w, http.StatusUnauthorized, "bad_credentials", "invalid username or password")
		return
	}
	if err != nil {
		s.log.Error("sign-in error", "err", err)
		httpx.Error(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	s.forgive(s.proxies.ClientIP(r), req.Username)
	if _, err := s.StartSession(w, r, u, MethodPassword); err != nil {
		s.log.Error("could not start session", "err", err)
		httpx.Error(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]User{"user": u})
}

func (s *Service) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(CookieName); err == nil && c.Value != "" {
		id, user, err := s.deleteSession(r.Context(), c.Value)
		if err != nil {
			s.log.Error("could not delete session", "err", err)
			httpx.Error(w, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		if id != "" {
			// Its open sockets close too (the server wiring listens).
			s.emit(Event{Kind: EventSessionEnded, SessionID: id, UserID: user, Request: r})
		}
	}
	s.clearCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handleSession(w http.ResponseWriter, r *http.Request) {
	id, _ := FromContext(r.Context())
	opts, err := s.stepUpOptions(r, id.User.ID)
	if err != nil {
		s.internalError(w, "could not work out the step-up methods", err)
		return
	}
	var until *time.Time
	if id.Session.StepUpUntil.After(s.now()) {
		t := id.Session.StepUpUntil.UTC()
		until = &t
	}
	httpx.WriteJSON(w, http.StatusOK, struct {
		User          User       `json:"user"`
		Session       Session    `json:"session"`
		StepUpUntil   *time.Time `json:"step_up_until"`
		StepUpMethods []string   `json:"step_up_methods"`
	}{id.User, id.Session, until, opts.methods()})
}
