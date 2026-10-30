package auth

// The OIDC client: discovery, the provider's signing keys (JWKS), the code
// exchange with PKCE, and ID token verification. Standard library plus
// golang-jwt (already in the module graph through go-webauthn) for the
// signature checks; no other dependency. Cha Dash keeps no access or
// refresh token: the ID token is read once, at the callback, and dropped.

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	// discoveryTTL is how long a provider's discovery document is reused.
	discoveryTTL = time.Hour
	// jwksRefetchAfter: an unknown key ID refetches the provider's keys at
	// most this often, so a rotation is picked up and a flood of forged
	// tokens can't make the hub hammer the provider.
	jwksRefetchAfter = time.Minute
	// maxProviderBody bounds what the hub reads from the provider.
	maxProviderBody = 1 << 20
	// tokenLeeway forgives clock skew on exp, iat and auth_time.
	tokenLeeway = time.Minute
	// StepUpAuthAge is how recent auth_time must be for a step-up through
	// the provider.
	StepUpAuthAge = 60 * time.Second
)

// signingAlgs are the ID token algorithms accepted: asymmetric only, so a
// client secret can never sign one.
var signingAlgs = []string{"RS256", "RS384", "RS512", "PS256", "PS384", "PS512", "ES256", "ES384", "ES512", "EdDSA"}

// Discovery is the part of a provider's discovery document the hub uses.
type Discovery struct {
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	JWKSURI               string   `json:"jwks_uri"`
	ClaimsSupported       []string `json:"claims_supported,omitempty"`
	IDTokenAlgs           []string `json:"id_token_signing_alg_values_supported,omitempty"`
	TokenAuthMethods      []string `json:"token_endpoint_auth_methods_supported,omitempty"`
	CodeChallengeMethods  []string `json:"code_challenge_methods_supported,omitempty"`
}

// oidcClient caches one provider's discovery and keys.
type oidcClient struct {
	http *http.Client

	mu          sync.Mutex
	issuer      string
	disc        *Discovery
	discAt      time.Time
	keys        map[string]any // kid → public key
	keysAt      time.Time
	keysFetched bool
}

// errProvider is a problem talking to the provider; its message is safe to
// show the owner (no secret, no response body).
type errProvider struct{ msg string }

func (e *errProvider) Error() string { return e.msg }

func providerErr(format string, a ...any) error { return &errProvider{fmt.Sprintf(format, a...)} }

// WithOIDCClient sets the HTTP client the hub talks to the provider with
// (tests point it at a fake provider).
func WithOIDCClient(c *http.Client) Option { return func(s *Service) { s.oidc.http = c } }

func (c *oidcClient) client() *http.Client {
	if c.http != nil {
		return c.http
	}
	return &http.Client{Timeout: 10 * time.Second}
}

// reset forgets what's cached when the issuer changes.
func (c *oidcClient) resetLocked(issuer string) {
	if c.issuer != issuer {
		c.issuer, c.disc, c.keys, c.keysFetched = issuer, nil, nil, false
	}
}

// discover returns issuer's discovery document, cached for discoveryTTL.
func (c *oidcClient) discover(ctx context.Context, issuer string, now time.Time) (*Discovery, error) {
	c.mu.Lock()
	c.resetLocked(issuer)
	if c.disc != nil && now.Sub(c.discAt) < discoveryTTL {
		d := c.disc
		c.mu.Unlock()
		return d, nil
	}
	c.mu.Unlock()
	d, err := c.fetchDiscovery(ctx, issuer)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	if c.issuer == issuer {
		c.disc, c.discAt = d, now
	}
	c.mu.Unlock()
	return d, nil
}

// FetchDiscovery reads issuer's discovery document without caching it,
// for Settings → Sign-in → Test.
func (s *Service) FetchDiscovery(ctx context.Context, issuer string) (*Discovery, error) {
	return s.oidc.fetchDiscovery(ctx, issuer)
}

func (c *oidcClient) fetchDiscovery(ctx context.Context, issuer string) (*Discovery, error) {
	var d Discovery
	if err := c.getJSON(ctx, strings.TrimSuffix(issuer, "/")+"/.well-known/openid-configuration", &d); err != nil {
		return nil, err
	}
	// The issuer must be exactly the one configured (a trailing slash
	// aside), or tokens from another tenant on the same host would pass.
	if strings.TrimSuffix(d.Issuer, "/") != strings.TrimSuffix(issuer, "/") {
		return nil, providerErr("the provider calls itself %q, not %q: use its issuer exactly", d.Issuer, issuer)
	}
	for name, v := range map[string]string{"authorization_endpoint": d.AuthorizationEndpoint, "token_endpoint": d.TokenEndpoint, "jwks_uri": d.JWKSURI} {
		u, err := url.Parse(v)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return nil, providerErr("the discovery document has no usable %s", name)
		}
	}
	if len(d.CodeChallengeMethods) > 0 && !slices.Contains(d.CodeChallengeMethods, "S256") {
		return nil, providerErr("the provider doesn't support PKCE with S256, which Cha Dash needs")
	}
	return &d, nil
}

func (c *oidcClient) getJSON(ctx context.Context, u string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return providerErr("couldn't build a request to %s", hostOf(u))
	}
	req.Header.Set("Accept", "application/json")
	res, err := c.client().Do(req)
	if err != nil {
		return providerErr("couldn't reach %s", hostOf(u))
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return providerErr("%s answered %d", hostOf(u), res.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, maxProviderBody)).Decode(dst); err != nil {
		return providerErr("%s didn't send JSON", hostOf(u))
	}
	return nil
}

func hostOf(u string) string {
	if p, err := url.Parse(u); err == nil && p.Host != "" {
		return p.Host
	}
	return "the provider"
}

// key returns the signing key kid names, refetching the provider's keys
// when it's unknown and the last fetch is older than jwksRefetchAfter.
func (c *oidcClient) key(ctx context.Context, d *Discovery, kid string, now time.Time) (any, error) {
	c.mu.Lock()
	k, ok := pick(c.keys, kid)
	stale := !c.keysFetched || now.Sub(c.keysAt) >= jwksRefetchAfter
	c.mu.Unlock()
	if ok {
		return k, nil
	}
	if !stale {
		return nil, errors.New("unknown signing key")
	}
	var set struct {
		Keys []jwk `json:"keys"`
	}
	if err := c.getJSON(ctx, d.JWKSURI, &set); err != nil {
		return nil, err
	}
	keys := map[string]any{}
	for i, j := range set.Keys {
		if j.Use != "" && j.Use != "sig" {
			continue
		}
		pk, err := j.public()
		if err != nil {
			continue // a key type the hub doesn't use
		}
		id := j.Kid
		if id == "" {
			id = fmt.Sprintf("#%d", i)
		}
		keys[id] = pk
	}
	c.mu.Lock()
	c.keys, c.keysAt, c.keysFetched = keys, now, true
	k, ok = pick(c.keys, kid)
	c.mu.Unlock()
	if !ok {
		return nil, errors.New("unknown signing key")
	}
	return k, nil
}

// pick finds kid; a token without one may use a provider's only key.
func pick(keys map[string]any, kid string) (any, bool) {
	if kid != "" {
		k, ok := keys[kid]
		return k, ok
	}
	if len(keys) == 1 {
		for _, k := range keys {
			return k, true
		}
	}
	return nil, false
}

// jwk is one JSON Web Key (RFC 7517), public parts only.
type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	N   string `json:"n"`
	E   string `json:"e"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

func b64url(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
}

func (j jwk) public() (any, error) {
	switch j.Kty {
	case "RSA":
		n, err1 := b64url(j.N)
		e, err2 := b64url(j.E)
		if err1 != nil || err2 != nil || len(e) == 0 || len(e) > 4 {
			return nil, errors.New("bad RSA key")
		}
		pk := &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
		if pk.N.BitLen() < 2048 {
			return nil, errors.New("RSA key too short")
		}
		return pk, nil
	case "EC":
		var curve elliptic.Curve
		switch j.Crv {
		case "P-256":
			curve = elliptic.P256()
		case "P-384":
			curve = elliptic.P384()
		case "P-521":
			curve = elliptic.P521()
		default:
			return nil, errors.New("unsupported curve")
		}
		x, err1 := b64url(j.X)
		y, err2 := b64url(j.Y)
		size := (curve.Params().BitSize + 7) / 8
		if err1 != nil || err2 != nil || len(x) != size || len(y) != size {
			return nil, errors.New("bad EC key")
		}
		return ecdsa.ParseUncompressedPublicKey(curve, append(append([]byte{4}, x...), y...))
	case "OKP":
		x, err := b64url(j.X)
		if j.Crv != "Ed25519" || err != nil || len(x) != ed25519.PublicKeySize {
			return nil, errors.New("bad OKP key")
		}
		return ed25519.PublicKey(x), nil
	}
	return nil, errors.New("unsupported key type")
}

// tokenResponse is the token endpoint's answer; only the ID token is kept.
type tokenResponse struct {
	IDToken string `json:"id_token"`
	Error   string `json:"error"`
}

// exchange trades code for an ID token, with the PKCE verifier.
func (c *oidcClient) exchange(ctx context.Context, d *Discovery, clientID, secret, code, verifier, redirectURI string) (string, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"code_verifier": {verifier},
		"client_id":     {clientID},
	}
	// client_secret_basic unless the provider lists only _post.
	post := secret != "" && len(d.TokenAuthMethods) > 0 && !slices.Contains(d.TokenAuthMethods, "client_secret_basic") && slices.Contains(d.TokenAuthMethods, "client_secret_post")
	if post {
		form.Set("client_secret", secret)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", providerErr("couldn't build the token request")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if secret != "" && !post {
		req.SetBasicAuth(url.QueryEscape(clientID), url.QueryEscape(secret))
	}
	res, err := c.client().Do(req)
	if err != nil {
		return "", providerErr("couldn't reach %s", hostOf(d.TokenEndpoint))
	}
	defer res.Body.Close()
	var tr tokenResponse
	_ = json.NewDecoder(io.LimitReader(res.Body, maxProviderBody)).Decode(&tr)
	if res.StatusCode != http.StatusOK {
		if tr.Error != "" && len(tr.Error) <= 64 {
			return "", providerErr("the provider refused the code: %s", tr.Error)
		}
		return "", providerErr("the token endpoint answered %d", res.StatusCode)
	}
	if tr.IDToken == "" {
		return "", providerErr("the provider sent no ID token; is the openid scope allowed?")
	}
	return tr.IDToken, nil
}

// idClaims is what the hub reads from a verified ID token.
type idClaims struct {
	Issuer   string
	Subject  string
	Email    string
	Username string // preferred_username
	Groups   []string
	// AuthTime is when the person last authenticated at the provider;
	// zero when the token doesn't say.
	AuthTime time.Time
}

// verify checks raw's signature against the provider's keys, its issuer,
// audience (and azp when there are several), expiry, issue time and nonce.
func (c *oidcClient) verify(ctx context.Context, d *Discovery, raw, clientID, nonce, groupClaim string, now time.Time) (idClaims, error) {
	algs := signingAlgs
	if len(d.IDTokenAlgs) > 0 {
		algs = slices.DeleteFunc(slices.Clone(signingAlgs), func(a string) bool { return !slices.Contains(d.IDTokenAlgs, a) })
		if len(algs) == 0 {
			return idClaims{}, errors.New("the provider signs ID tokens with no algorithm Cha Dash accepts")
		}
	}
	p := jwt.NewParser(
		jwt.WithValidMethods(algs),
		jwt.WithIssuer(d.Issuer),
		jwt.WithAudience(clientID),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithLeeway(tokenLeeway),
		jwt.WithTimeFunc(func() time.Time { return now }),
	)
	mc := jwt.MapClaims{}
	_, err := p.ParseWithClaims(raw, mc, func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		return c.key(ctx, d, kid, now)
	})
	if err != nil {
		return idClaims{}, err
	}
	aud, _ := mc.GetAudience()
	if len(aud) > 1 {
		if azp, _ := mc["azp"].(string); azp != clientID {
			return idClaims{}, errors.New("the token is for several clients and not authorized for this one")
		}
	}
	got, _ := mc["nonce"].(string)
	if nonce == "" || subtle.ConstantTimeCompare([]byte(got), []byte(nonce)) != 1 {
		return idClaims{}, errors.New("nonce mismatch")
	}
	out := idClaims{Issuer: d.Issuer}
	out.Subject, _ = mc["sub"].(string)
	if out.Subject == "" || len(out.Subject) > 255 {
		return idClaims{}, errors.New("no usable subject")
	}
	out.Email, _ = mc["email"].(string)
	out.Username, _ = mc["preferred_username"].(string)
	out.Groups = claimStrings(mc[groupClaim])
	switch v := mc["auth_time"].(type) {
	case float64:
		out.AuthTime = time.Unix(int64(v), 0)
	case json.Number:
		if n, err := v.Int64(); err == nil {
			out.AuthTime = time.Unix(n, 0)
		}
	}
	if len(out.Email) > 320 {
		out.Email = ""
	}
	return out, nil
}

// claimStrings reads a groups claim: a list of strings, or one string.
func claimStrings(v any) []string {
	var out []string
	switch g := v.(type) {
	case string:
		out = []string{g}
	case []any:
		for _, x := range g {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
	}
	if len(out) > 256 {
		out = out[:256]
	}
	return out
}

// pkceChallenge is the S256 code challenge for verifier.
func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
