package security

// Settings → Security → Sign-in (ADR-0022): the OIDC provider, forward-auth
// and the group mappings they share, in the settings row SignInKey as a
// JSON object, and the provider's client secret in the vault at
// auth.OIDCClientSecretName. internal/auth enforces them, reading them
// through SignIn and ClientSecret (server.New wires auth.SetSignIn).
//
//	GET  /api/settings/sign-in        the settings, client_secret_set, redirect_uri,
//	                                  trusted_proxies, last_test, forward_groups (owner)
//	PUT  /api/settings/sign-in        the settings, client_secret? ("" clears it) → the same;
//	                                  needs a step-up (owner)
//	POST /api/settings/sign-in/test   {} → {ok, error?, discovery facts, login_url?} (owner)

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"dash.cha.sh/internal/auth"
	"dash.cha.sh/internal/authz"
	"dash.cha.sh/internal/httpx"
	"dash.cha.sh/internal/vault"
)

// SignInKey is the settings row the sign-in settings live in.
const SignInKey = "security.sign_in"

// ParseSignIn reads a stored or imported sign-in settings object:
// normalized, then validated.
func ParseSignIn(raw []byte) (auth.SignInSettings, error) {
	set := auth.DefaultSignIn()
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&set); err != nil {
		return auth.SignInSettings{}, &auth.ValidationError{Msg: "the sign-in settings aren't valid: " + err.Error()}
	}
	set = set.Normalize()
	if err := set.Validate(); err != nil {
		return auth.SignInSettings{}, err
	}
	return set, nil
}

// CanonicalSignIn is set as the settings row stores it.
func CanonicalSignIn(set auth.SignInSettings) string {
	set = set.Normalize()
	if set.Mappings == nil {
		set.Mappings = []auth.GroupMapping{}
	}
	b, _ := json.Marshal(set)
	return string(b)
}

// loadSignIn reads the row; a missing or unreadable one is the defaults.
func (s *Service) loadSignIn(ctx context.Context) error {
	var raw string
	err := s.st.Reader().QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, SignInKey).Scan(&raw)
	set := auth.DefaultSignIn()
	switch {
	case err == nil:
		if p, perr := ParseSignIn([]byte(raw)); perr == nil {
			set = p
		} else {
			s.log.Warn("security: the stored sign-in settings aren't valid; OIDC and forward-auth are off", "err", perr)
		}
	case !isNoRows(err):
		return err
	}
	s.mu.Lock()
	s.signIn = set
	s.mu.Unlock()
	return nil
}

// SignIn is the sign-in settings in force; auth reads them through it.
func (s *Service) SignIn() auth.SignInSettings {
	s.mu.Lock()
	defer s.mu.Unlock()
	set := s.signIn
	set.OIDC.Scopes = slices.Clone(set.OIDC.Scopes)
	set.Mappings = slices.Clone(set.Mappings)
	return set
}

// ClientSecret reads the provider's client secret ("" when none is set).
func (s *Service) ClientSecret(ctx context.Context) (string, error) {
	if s.Vault == nil {
		return "", nil
	}
	b, err := s.Vault.Get(ctx, auth.OIDCClientSecretName)
	if errors.Is(err, vault.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (s *Service) hasClientSecret(ctx context.Context) bool {
	v, err := s.ClientSecret(ctx)
	return err == nil && v != ""
}

// SetSignIn stores set (which must be valid).
func (s *Service) SetSignIn(ctx context.Context, set auth.SignInSettings) error {
	set = set.Normalize()
	if err := set.Validate(); err != nil {
		return err
	}
	s.set.Lock()
	defer s.set.Unlock()
	if _, err := s.st.Writer().ExecContext(ctx, `INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		SignInKey, CanonicalSignIn(set), s.now().UnixMilli()); err != nil {
		return fmt.Errorf("security: saving the sign-in settings: %w", err)
	}
	s.mu.Lock()
	s.signIn = set
	s.mu.Unlock()
	s.log.Warn("sign-in settings changed", "oidc", set.OIDC.Enabled, "forward_auth", set.ForwardAuth.Enabled, "mappings", len(set.Mappings), "provision", set.Provision)
	return nil
}

// SignInView is the API's answer.
type SignInView struct {
	auth.SignInSettings
	ClientSecretSet bool `json:"client_secret_set"`
	// RedirectURI is the callback to register with the provider.
	RedirectURI string `json:"redirect_uri"`
	// TrustedProxies: CHADASH_TRUSTED_PROXIES lists at least one proxy,
	// without which forward-auth headers never count.
	TrustedProxies bool                 `json:"trusted_proxies"`
	LastTest       *auth.OIDCTestResult `json:"last_test"`
	// ForwardGroups is the groups header a trusted proxy last sent, since
	// the hub started; null when none has.
	ForwardGroups *auth.ForwardGroupsSeen `json:"forward_groups"`
}

// RegisterSignIn mounts the sign-in settings routes; a is the auth service
// whose redirect URI and test sign-in they use. wrap attaches the session;
// Guard admits the owner only.
func (s *Service) RegisterSignIn(mux *http.ServeMux, wrap func(http.Handler) http.Handler, a *auth.Service, trusted bool) {
	view := func(r *http.Request) SignInView {
		return SignInView{SignInSettings: s.SignIn(), ClientSecretSet: s.hasClientSecret(r.Context()), RedirectURI: a.RedirectURI(r),
			TrustedProxies: trusted, LastTest: a.LastOIDCTest(), ForwardGroups: a.LastForwardGroups()}
	}
	mux.Handle("GET /api/settings/sign-in", wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, view(r))
	})))
	mux.Handle("PUT /api/settings/sign-in", wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.handlePutSignIn(w, r, view)
	})))
	mux.Handle("POST /api/settings/sign-in/test", wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.handleTestSignIn(w, r, a)
	})))
}

// signInAudit is what the audit log keeps of a change: the settings (no
// secret is in them) and whether the client secret was set or cleared.
type signInAudit struct {
	auth.SignInSettings
	ClientSecret string `json:"client_secret,omitempty"`
}

func (s *Service) handlePutSignIn(w http.ResponseWriter, r *http.Request, view func(*http.Request) SignInView) {
	var req struct {
		auth.SignInSettings
		ClientSecret *string `json:"client_secret"`
	}
	req.SignInSettings = auth.DefaultSignIn()
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	set := req.SignInSettings.Normalize()
	params := signInAudit{SignInSettings: set}
	if req.ClientSecret != nil {
		params.ClientSecret = "set"
		if *req.ClientSecret == "" {
			params.ClientSecret = "cleared"
		}
	}
	const action, target = "settings.sign_in.update", "settings:sign_in"
	if err := set.Validate(); err != nil {
		if verr, ok := errors.AsType[*auth.ValidationError](err); ok {
			s.Audit.Refuse(r, action, target, params, authz.T3, "invalid", verr.Msg)
			httpx.Error(w, http.StatusUnprocessableEntity, "invalid", verr.Msg)
			return
		}
	}
	if req.ClientSecret != nil && len(*req.ClientSecret) > 1024 {
		httpx.Error(w, http.StatusUnprocessableEntity, "invalid", "the client secret is too long")
		return
	}
	if req.ClientSecret != nil && *req.ClientSecret != "" && s.Vault == nil {
		httpx.Error(w, http.StatusConflict, "no_vault", "This hub has no vault to keep the client secret in.")
		return
	}
	// How people get in is the most sensitive setting there is: a step-up,
	// whatever changes (T3).
	actor, _ := authz.ActorFrom(r)
	if err := authz.Decide(actor, authz.Request{Scope: action, Tier: authz.T3}, authz.Env{Now: s.now()}); err != nil {
		s.Audit.Change(r, action, target, params, authz.T3, err)
		authz.WriteRefusal(w, err)
		return
	}
	if req.ClientSecret != nil && s.Vault != nil {
		var err error
		if *req.ClientSecret == "" {
			if err = s.Vault.Delete(r.Context(), auth.OIDCClientSecretName); errors.Is(err, vault.ErrNotFound) {
				err = nil
			}
		} else {
			err = s.Vault.Put(r.Context(), auth.OIDCClientSecretName, []byte(*req.ClientSecret))
		}
		if err != nil {
			s.log.Error("security: saving the OIDC client secret failed", "err", err)
			httpx.Error(w, http.StatusInternalServerError, "internal", "internal error")
			return
		}
	}
	if err := s.SetSignIn(r.Context(), set); err != nil {
		s.log.Error("security: saving the sign-in settings failed", "err", err)
		httpx.Error(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	s.Audit.Change(r, action, target, params, authz.T3, nil)
	httpx.WriteJSON(w, http.StatusOK, view(r))
}

// TestView is Test's answer.
type TestView struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	// What discovery found.
	Issuer                string `json:"issuer,omitempty"`
	AuthorizationEndpoint string `json:"authorization_endpoint,omitempty"`
	TokenEndpoint         string `json:"token_endpoint,omitempty"`
	// AuthTime: whether the provider lists auth_time among its claims
	// ("listed", "not_listed", or "unknown" when it lists no claims). A
	// test sign-in (login_url) says for sure.
	AuthTime string `json:"auth_time,omitempty"`
	// LoginURL starts a test sign-in at the provider, when OIDC is on.
	LoginURL string `json:"login_url,omitempty"`
}

func (s *Service) handleTestSignIn(w http.ResponseWriter, r *http.Request, a *auth.Service) {
	set := s.SignIn()
	if set.OIDC.Issuer == "" {
		httpx.Error(w, http.StatusUnprocessableEntity, "invalid", "Save the provider's issuer URL first.")
		return
	}
	d, err := a.FetchDiscovery(r.Context(), set.OIDC.Issuer)
	if err != nil {
		httpx.WriteJSON(w, http.StatusOK, TestView{Error: err.Error()})
		return
	}
	v := TestView{OK: true, Issuer: d.Issuer, AuthorizationEndpoint: d.AuthorizationEndpoint, TokenEndpoint: d.TokenEndpoint, AuthTime: "unknown"}
	if len(d.ClaimsSupported) > 0 {
		v.AuthTime = "not_listed"
		if slices.Contains(d.ClaimsSupported, "auth_time") {
			v.AuthTime = "listed"
		}
	}
	if set.OIDCReady() {
		id, _ := auth.FromContext(r.Context())
		u, err := a.BeginOIDCTest(w, r, id.User.ID, "/settings/security")
		if err != nil {
			v.OK, v.Error = false, err.Error()
		} else {
			v.LoginURL = u
		}
	}
	httpx.WriteJSON(w, http.StatusOK, v)
}
