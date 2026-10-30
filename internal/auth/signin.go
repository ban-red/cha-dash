package auth

// Sign-in settings (ADR-0022): one OIDC provider, forward-auth headers from
// trusted proxies, the group → role mappings both share, provisioning, and
// hiding the password form. internal/security stores them (the settings row
// security.sign_in and the vault's auth/oidc/client-secret) and serves the
// owner's API; this package reads them through SetSignIn and enforces them.

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
)

// OIDCClientSecretName is where the vault keeps the provider's client secret.
const OIDCClientSecretName = "auth/oidc/client-secret"

// Sign-in methods beyond password and passkey.
const (
	MethodOIDC        = "oidc"
	MethodForwardAuth = "forward_auth"
)

// Forward-auth's default header names: Authelia's, which authentik and most
// proxies also send.
const (
	DefaultUserHeader   = "Remote-User"
	DefaultGroupsHeader = "Remote-Groups"
	DefaultEmailHeader  = "Remote-Email"
)

// DefaultScopes are asked for when the settings name none.
var DefaultScopes = []string{"openid", "profile", "email", "groups"}

// GroupMapping gives the people in a provider's group a role.
type GroupMapping struct {
	Group string `json:"group"`
	Role  string `json:"role"`
}

// OIDCSettings is the one provider.
type OIDCSettings struct {
	Enabled bool `json:"enabled"`
	// Name labels the button: "Sign in with <Name>".
	Name       string   `json:"name"`
	Issuer     string   `json:"issuer"`
	ClientID   string   `json:"client_id"`
	Scopes     []string `json:"scopes"`
	GroupClaim string   `json:"group_claim"`
}

// ForwardAuthSettings is the opt-in for a trusted proxy's headers.
type ForwardAuthSettings struct {
	Enabled      bool   `json:"enabled"`
	UserHeader   string `json:"user_header"`
	GroupsHeader string `json:"groups_header"`
	EmailHeader  string `json:"email_header"`
}

// SignInSettings is everything about signing in that the owner sets.
// The client secret isn't here: it lives in the vault.
type SignInSettings struct {
	OIDC        OIDCSettings        `json:"oidc"`
	ForwardAuth ForwardAuthSettings `json:"forward_auth"`
	// Mappings are shared by OIDC and forward-auth. Empty means roles are
	// managed in Cha Dash; otherwise every sign-in through either takes its
	// role from them.
	Mappings []GroupMapping `json:"mappings"`
	// Provision creates an account on a first sign-in from someone whose
	// groups map to a role. Off by default.
	Provision bool `json:"provision"`
	// HidePasswordForm hides the password form on the sign-in page behind a
	// link. Password sign-in itself keeps working (break-glass).
	HidePasswordForm bool `json:"hide_password_form"`
}

// DefaultSignIn is the settings before the owner changes anything.
func DefaultSignIn() SignInSettings {
	return SignInSettings{
		OIDC:        OIDCSettings{Scopes: slices.Clone(DefaultScopes), GroupClaim: "groups"},
		ForwardAuth: ForwardAuthSettings{UserHeader: DefaultUserHeader, GroupsHeader: DefaultGroupsHeader, EmailHeader: DefaultEmailHeader},
		Mappings:    []GroupMapping{},
	}
}

var (
	headerNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,63}$`)
	scopeRE      = regexp.MustCompile(`^[\x21\x23-\x5B\x5D-\x7E]{1,64}$`)
	claimRE      = regexp.MustCompile(`^[A-Za-z0-9_.:/-]{1,64}$`)
)

// Normalize fills defaults and tidies what was sent: trimmed strings,
// canonical header names, openid among the scopes, no duplicate scopes.
func (s SignInSettings) Normalize() SignInSettings {
	d := DefaultSignIn()
	s.OIDC.Name = strings.TrimSpace(s.OIDC.Name)
	s.OIDC.Issuer = strings.TrimSpace(s.OIDC.Issuer)
	s.OIDC.ClientID = strings.TrimSpace(s.OIDC.ClientID)
	s.OIDC.GroupClaim = strings.TrimSpace(s.OIDC.GroupClaim)
	if s.OIDC.GroupClaim == "" {
		s.OIDC.GroupClaim = d.OIDC.GroupClaim
	}
	var scopes []string
	for _, sc := range s.OIDC.Scopes {
		if sc = strings.TrimSpace(sc); sc != "" && !slices.Contains(scopes, sc) {
			scopes = append(scopes, sc)
		}
	}
	if len(scopes) == 0 {
		scopes = slices.Clone(d.OIDC.Scopes)
	}
	if !slices.Contains(scopes, "openid") {
		scopes = append([]string{"openid"}, scopes...)
	}
	s.OIDC.Scopes = scopes
	header := func(v, def string) string {
		if v = strings.TrimSpace(v); v == "" {
			return def
		}
		return http.CanonicalHeaderKey(v)
	}
	s.ForwardAuth.UserHeader = header(s.ForwardAuth.UserHeader, d.ForwardAuth.UserHeader)
	s.ForwardAuth.GroupsHeader = header(s.ForwardAuth.GroupsHeader, d.ForwardAuth.GroupsHeader)
	s.ForwardAuth.EmailHeader = header(s.ForwardAuth.EmailHeader, d.ForwardAuth.EmailHeader)
	maps := make([]GroupMapping, 0, len(s.Mappings))
	for _, m := range s.Mappings {
		maps = append(maps, GroupMapping{Group: strings.TrimSpace(m.Group), Role: strings.TrimSpace(m.Role)})
	}
	s.Mappings = maps
	return s
}

// Validate says what's wrong with normalized settings, in a sentence.
func (s SignInSettings) Validate() error {
	bad := func(format string, a ...any) error { return &ValidationError{fmt.Sprintf(format, a...)} }
	o := s.OIDC
	if len(o.Name) > 40 {
		return bad("the provider's name must be at most 40 characters")
	}
	if o.Issuer != "" || o.Enabled {
		u, err := url.Parse(o.Issuer)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
			return bad("the issuer must be the provider's http(s):// URL, as its discovery document names it")
		}
		if len(o.Issuer) > 512 {
			return bad("the issuer URL is too long")
		}
	}
	if o.Enabled && o.ClientID == "" {
		return bad("OIDC needs the client ID the provider gave Cha Dash")
	}
	if len(o.ClientID) > 256 {
		return bad("the client ID is too long")
	}
	if len(o.Scopes) > 16 {
		return bad("ask for at most 16 scopes")
	}
	for _, sc := range o.Scopes {
		if !scopeRE.MatchString(sc) {
			return bad("%q isn't a scope", sc)
		}
	}
	if !claimRE.MatchString(o.GroupClaim) {
		return bad("the group claim must be a claim name, such as groups")
	}
	f := s.ForwardAuth
	for _, h := range []string{f.UserHeader, f.GroupsHeader, f.EmailHeader} {
		if !headerNameRE.MatchString(h) {
			return bad("%q isn't a header name", h)
		}
	}
	if f.UserHeader == f.GroupsHeader || f.UserHeader == f.EmailHeader {
		return bad("the user header must differ from the groups and email headers")
	}
	if len(s.Mappings) > 64 {
		return bad("at most 64 group mappings")
	}
	seen := map[string]bool{}
	for _, m := range s.Mappings {
		switch {
		case m.Group == "" || len(m.Group) > 256:
			return bad("each mapping needs a group name of at most 256 characters")
		case m.Role == RoleOwner:
			return bad("the owner role never comes from a group")
		case !ValidRole(m.Role):
			return bad("%q isn't a role: use admin, operator, viewer or household", m.Role)
		case seen[m.Group]:
			return bad("the group %q is mapped twice", m.Group)
		}
		seen[m.Group] = true
	}
	if s.Provision && len(s.Mappings) == 0 {
		return bad("creating accounts on first sign-in needs at least one group mapping, so new accounts get a role")
	}
	return nil
}

// OIDCReady reports whether OIDC is on and has what a sign-in needs.
func (s SignInSettings) OIDCReady() bool {
	return s.OIDC.Enabled && s.OIDC.Issuer != "" && s.OIDC.ClientID != ""
}

// ProviderName is the name the sign-in button uses.
func (s SignInSettings) ProviderName() string {
	if s.OIDC.Name != "" {
		return s.OIDC.Name
	}
	if u, err := url.Parse(s.OIDC.Issuer); err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	return "your provider"
}

// roleRank orders the roles a group can give, highest first.
func roleRank(role string) int {
	switch role {
	case RoleAdmin:
		return 4
	case RoleOperator:
		return 3
	case RoleViewer:
		return 2
	case RoleHousehold:
		return 1
	}
	return 0
}

// MappedRole is the highest role groups map to under s, or "" for none.
func (s SignInSettings) MappedRole(groups []string) string {
	best := ""
	for _, m := range s.Mappings {
		if slices.Contains(groups, m.Group) && roleRank(m.Role) > roleRank(best) {
			best = m.Role
		}
	}
	return best
}

// signInSource is where the settings and the client secret come from.
type signInSource struct {
	mu       sync.Mutex
	settings func() SignInSettings
	secret   func(context.Context) (string, error)
	// link decides whether the session on r may link an identity (a fresh
	// step-up, decided in internal/authz). Nil refuses every link.
	link func(r *http.Request) error
}

// SetSignIn tells the service where the sign-in settings and the OIDC
// client secret live (internal/security). Until it's called, OIDC and
// forward-auth are off.
func (s *Service) SetSignIn(settings func() SignInSettings, secret func(context.Context) (string, error)) {
	s.signIn.mu.Lock()
	defer s.signIn.mu.Unlock()
	s.signIn.settings, s.signIn.secret = settings, secret
}

// SetLinkCheck sets who may link an identity: f gets the request with the
// signed-in identity on its context and returns nil to allow it. Linking
// needs a step-up; that decision belongs to internal/authz.
func (s *Service) SetLinkCheck(f func(r *http.Request) error) {
	s.signIn.mu.Lock()
	defer s.signIn.mu.Unlock()
	s.signIn.link = f
}

// SignIn returns the settings in force (the defaults until SetSignIn).
func (s *Service) SignIn() SignInSettings {
	s.signIn.mu.Lock()
	f := s.signIn.settings
	s.signIn.mu.Unlock()
	if f == nil {
		return DefaultSignIn()
	}
	return f()
}

func (s *Service) clientSecret(ctx context.Context) (string, error) {
	s.signIn.mu.Lock()
	f := s.signIn.secret
	s.signIn.mu.Unlock()
	if f == nil {
		return "", nil
	}
	return f(ctx)
}

func (s *Service) linkAllowed(r *http.Request) error {
	s.signIn.mu.Lock()
	f := s.signIn.link
	s.signIn.mu.Unlock()
	if f == nil {
		return fmt.Errorf("auth: linking isn't wired")
	}
	return f(r)
}

// PublicSignIn is what the sign-in page may know before anyone signs in.
type PublicSignIn struct {
	// OIDC names the provider when its button should show.
	OIDC *struct {
		Name string `json:"name"`
	} `json:"oidc,omitempty"`
	// PasswordForm is false when the owner hid it behind a link.
	PasswordForm bool `json:"password_form"`
}

// PublicSignInInfo is GET /api/setup's sign_in.
func (s *Service) PublicSignInInfo() PublicSignIn {
	set := s.SignIn()
	p := PublicSignIn{PasswordForm: !set.HidePasswordForm}
	if set.OIDCReady() {
		p.OIDC = &struct {
			Name string `json:"name"`
		}{set.ProviderName()}
	}
	if p.OIDC == nil && !set.ForwardAuth.Enabled {
		// Nothing else to sign in with: never hide the only form.
		p.PasswordForm = true
	}
	return p
}
