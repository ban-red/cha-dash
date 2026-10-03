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

// Identity is the signed-in user attached to a request by Require.
type Identity struct {
	User    User
	Session Session
}

type ctxKey struct{}

// FromContext returns the identity Require attached, if any.
func FromContext(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(ctxKey{}).(Identity)
	return id, ok
}

// Register mounts the auth endpoints:
//
//	GET  /api/setup          {"required": bool}
//	POST /api/setup          {"token","username","password"} → 201 {"user"} + session cookie
//	POST /api/auth/login     {"username","password"} → 200 {"user"} + session cookie
//	POST /api/auth/logout    → 204, cookie cleared
//	GET  /api/auth/session   → 200 {"user","session"} or 401
func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/setup", s.handleSetupStatus)
	mux.HandleFunc("POST /api/setup", s.handleSetup)
	mux.HandleFunc("POST /api/auth/login", s.handleLogin)
	mux.HandleFunc("POST /api/auth/logout", s.handleLogout)
	mux.Handle("GET /api/auth/session", s.Require(http.HandlerFunc(s.handleSession)))
}

// Require rejects requests without a valid session cookie with 401 and
// attaches the Identity for the rest.
func (s *Service) Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		s.touchSession(r.Context(), sess)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, Identity{User: u, Session: sess})))
	})
}

// StartSession signs u in on this response. Every sign-in method (password
// today, passkeys and OIDC later) ends here.
func (s *Service) StartSession(w http.ResponseWriter, r *http.Request, u User, method string) (Session, error) {
	token, sess, err := s.createSession(r.Context(), u.ID, method, s.proxies.ClientIP(r).String(), r.UserAgent())
	if err != nil {
		return Session{}, err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/",
		Expires:  sess.ExpiresAt,
		MaxAge:   int(SessionTTL / time.Second),
		HttpOnly: true,
		Secure:   s.proxies.IsHTTPS(r),
		SameSite: http.SameSiteLaxMode,
	})
	return sess, nil
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

// rateLimited applies the per-IP attempt budget and writes 429 when spent.
func (s *Service) rateLimited(w http.ResponseWriter, r *http.Request, scope string) bool {
	ok, wait := s.limiter.allow(scope + "|" + s.proxies.ClientIP(r).String())
	if ok {
		return false
	}
	w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
	httpx.Error(w, http.StatusTooManyRequests, "rate_limited", "too many attempts; try again later")
	return true
}

func (s *Service) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]bool{"required": s.SetupRequired()})
}

type setupRequest struct {
	Token    string `json:"token"`
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *Service) handleSetup(w http.ResponseWriter, r *http.Request) {
	if s.rateLimited(w, r, "setup") {
		return
	}
	var req setupRequest
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	u, err := s.Setup(r.Context(), req.Token, req.Username, req.Password)
	var verr *ValidationError
	switch {
	case errors.Is(err, ErrSetupComplete):
		httpx.Error(w, http.StatusConflict, "setup_complete", "setup has already been completed")
		return
	case errors.Is(err, ErrBadSetupToken):
		s.log.Warn("setup attempt with a wrong token", "client", s.proxies.ClientIP(r).String())
		httpx.Error(w, http.StatusForbidden, "bad_setup_token", "invalid setup token")
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
	if s.rateLimited(w, r, "login") {
		return
	}
	var req loginRequest
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	u, err := s.Login(r.Context(), req.Username, req.Password)
	if errors.Is(err, ErrBadCredentials) {
		// Usernames aren't logged: people sometimes type a password into that field.
		s.log.Info("sign-in failed", "client", s.proxies.ClientIP(r).String())
		httpx.Error(w, http.StatusUnauthorized, "bad_credentials", "invalid username or password")
		return
	}
	if err != nil {
		s.log.Error("sign-in error", "err", err)
		httpx.Error(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	if _, err := s.StartSession(w, r, u, MethodPassword); err != nil {
		s.log.Error("could not start session", "err", err)
		httpx.Error(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]User{"user": u})
}

func (s *Service) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(CookieName); err == nil && c.Value != "" {
		if err := s.deleteSession(r.Context(), c.Value); err != nil {
			s.log.Error("could not delete session", "err", err)
			httpx.Error(w, http.StatusInternalServerError, "internal", "internal error")
			return
		}
	}
	s.clearCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handleSession(w http.ResponseWriter, r *http.Request) {
	id, _ := FromContext(r.Context())
	httpx.WriteJSON(w, http.StatusOK, struct {
		User    User    `json:"user"`
		Session Session `json:"session"`
	}{id.User, id.Session})
}
