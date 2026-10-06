package auth

// API tokens (v0.2, wave 10). This file stores and checks them; what a
// token may do (its scopes, its role ceiling, the routes it may call) is
// internal/authz's decision, which also serves the tokens API.
//
// A token is `cdt_<id>_<secret>`: `cdt_`, the token's public ID (26 base32
// characters, the part after `tok_` in the API and the audit log), an
// underscore, and 256 random bits as 52 base32 characters. Only the SHA-256
// of the whole string is stored. A request names its token's ID, so the
// lookup is one indexed read, and the hashes are compared in constant time.
//
// Why SHA-256 and not argon2id, as for passwords: argon2id exists to slow
// down guessing a low-entropy secret a person chose. A token's secret is 256
// random bits; nobody can enumerate that space however fast the hash is, so
// a slow hash buys nothing and would cost about 19 MiB and tens of
// milliseconds on every API request. Sessions are stored the same way.
//
// Tokens travel only in `Authorization: Bearer`, never in a cookie, so a
// browser never sends one on its own and there is nothing for a cross-site
// request to ride on.

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"dash.cha.sh/internal/httpx"
)

// TokenPrefix starts every API token's secret.
const TokenPrefix = "cdt_"

// tokenIDPrefix starts a token's public ID.
const tokenIDPrefix = "tok_"

// tokenLastUsedEvery throttles last_used_at writes: at most once a minute
// per token.
const tokenLastUsedEvery = time.Minute

// Token errors. Every way a presented token can fail is ErrBadToken to the
// client unless the secret matched: only then does it learn the token has
// expired or been revoked.
var (
	ErrBadToken      = errors.New("auth: invalid API token")
	ErrTokenExpired  = errors.New("auth: API token expired")
	ErrTokenRevoked  = errors.New("auth: API token revoked")
	ErrTokenNotFound = errors.New("auth: no such API token")
)

// APIToken is a token as the tokens API lists it. It never carries the
// secret or its hash.
type APIToken struct {
	ID         string     `json:"id"`
	UserID     string     `json:"user_id"`
	Username   string     `json:"username"`
	Name       string     `json:"name"`
	Scopes     []string   `json:"scopes"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  *time.Time `json:"expires_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	LastUsedIP string     `json:"last_used_ip,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at"`
	RevokedBy  string     `json:"revoked_by,omitempty"`
}

// TokenRef is the token behind a request, on its Identity.
type TokenRef struct {
	ID     string
	Name   string
	Scopes []string
}

// TokenAccess is what a token may do right now, for the policy: its user's
// current role and its scopes. OK is false when the token is revoked or
// expired, or its user is disabled.
type TokenAccess struct {
	OK       bool
	UserID   string
	Username string
	Role     string
	Scopes   []string
}

var tokenSecretRE = regexp.MustCompile(`^cdt_([a-z2-7]{26})_([A-Z2-7]{52})$`)

// ValidateTokenName checks a token's name: 1–64 characters, no control
// characters, trimmed.
func ValidateTokenName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 64 {
		return "", &ValidationError{"A token's name must be 1 to 64 characters."}
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", &ValidationError{"A token's name can't contain control characters."}
		}
	}
	return name, nil
}

// CreateToken stores a new token for userID and returns it with its secret,
// which is never stored or shown again. Scopes are stored as given:
// internal/authz validates them first.
func (s *Service) CreateToken(ctx context.Context, userID, name string, scopes []string, expires time.Time) (APIToken, string, error) {
	name, err := ValidateTokenName(name)
	if err != nil {
		return APIToken{}, "", err
	}
	if scopes == nil {
		scopes = []string{}
	}
	raw, err := json.Marshal(scopes)
	if err != nil {
		return APIToken{}, "", err
	}
	short := strings.ToLower(rand.Text())
	secret := TokenPrefix + short + "_" + rand.Text() + rand.Text()
	now := s.now().UTC().Truncate(time.Millisecond)
	exp := expires.UTC().Truncate(time.Millisecond)
	t := APIToken{ID: tokenIDPrefix + short, UserID: userID, Name: name, Scopes: scopes, CreatedAt: now, ExpiresAt: &exp}
	err = s.st.WriteTx(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `SELECT username FROM users WHERE id = ? AND disabled_at IS NULL`, userID).Scan(&t.Username); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrUserNotFound
			}
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO api_tokens (id, user_id, name, token_hash, scopes, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			t.ID, userID, name, tokenHash(secret), string(raw), now.UnixMilli(), exp.UnixMilli())
		return err
	})
	if err != nil {
		return APIToken{}, "", err
	}
	s.log.Info("API token created", "token", t.ID, "user", t.Username)
	return t, secret, nil
}

const tokenQuery = `SELECT t.id, t.user_id, u.username, t.name, t.scopes, t.created_at, t.expires_at, t.last_used_at, t.last_used_ip, t.revoked_at, t.revoked_by
	FROM api_tokens t JOIN users u ON u.id = t.user_id`

func scanToken(sc scanner) (APIToken, error) {
	var t APIToken
	var scopes string
	var created int64
	var expires, used, revoked sql.NullInt64
	var ip, by sql.NullString
	if err := sc.Scan(&t.ID, &t.UserID, &t.Username, &t.Name, &scopes, &created, &expires, &used, &ip, &revoked, &by); err != nil {
		return APIToken{}, err
	}
	if json.Unmarshal([]byte(scopes), &t.Scopes) != nil || t.Scopes == nil {
		t.Scopes = []string{}
	}
	t.CreatedAt = time.UnixMilli(created).UTC()
	t.ExpiresAt, t.LastUsedAt, t.RevokedAt = msTime(expires), msTime(used), msTime(revoked)
	t.LastUsedIP, t.RevokedBy = ip.String, by.String
	return t, nil
}

func msTime(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := time.UnixMilli(v.Int64).UTC()
	return &t
}

// ListTokens returns userID's tokens, or everyone's for "", newest first.
// Revoked and expired tokens are listed too, so their use stays explained.
func (s *Service) ListTokens(ctx context.Context, userID string) ([]APIToken, error) {
	q, args := tokenQuery+` ORDER BY t.created_at DESC, t.id`, []any{}
	if userID != "" {
		q, args = tokenQuery+` WHERE t.user_id = ? ORDER BY t.created_at DESC, t.id`, []any{userID}
	}
	rows, err := s.st.Reader().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("auth: %w", err)
	}
	defer rows.Close()
	out := []APIToken{}
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// GetToken returns one token, or ErrTokenNotFound.
func (s *Service) GetToken(ctx context.Context, id string) (APIToken, error) {
	t, err := scanToken(s.st.Reader().QueryRowContext(ctx, tokenQuery+` WHERE t.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return APIToken{}, ErrTokenNotFound
	}
	return t, err
}

// RevokeToken revokes a token at once; by is the user who did it. Revoking
// one already revoked changes nothing and reports false.
func (s *Service) RevokeToken(ctx context.Context, id, by string) (APIToken, bool, error) {
	res, err := s.st.Writer().ExecContext(ctx, `UPDATE api_tokens SET revoked_at = ?, revoked_by = ? WHERE id = ? AND revoked_at IS NULL`,
		s.now().UnixMilli(), by, id)
	if err != nil {
		return APIToken{}, false, fmt.Errorf("auth: %w", err)
	}
	n, _ := res.RowsAffected()
	t, err := s.GetToken(ctx, id)
	if err != nil {
		return APIToken{}, false, err
	}
	if n == 1 {
		s.log.Info("API token revoked", "token", id, "by", by)
	}
	return t, n == 1, nil
}

// TokenAccess reports what a token may do now: its user's current role and
// its scopes, or OK false once it's revoked, expired or its user disabled.
// The policy asks on every decision, so revoking a token or demoting its
// user applies at once, also to plans waiting for approval.
func (s *Service) TokenAccess(ctx context.Context, id string) (TokenAccess, error) {
	var a TokenAccess
	var scopes string
	var expires, revoked, disabled sql.NullInt64
	err := s.st.Reader().QueryRowContext(ctx, `SELECT u.id, u.username, u.role, u.disabled_at, t.scopes, t.expires_at, t.revoked_at
		FROM api_tokens t JOIN users u ON u.id = t.user_id WHERE t.id = ?`, id).
		Scan(&a.UserID, &a.Username, &a.Role, &disabled, &scopes, &expires, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return TokenAccess{}, nil
	}
	if err != nil {
		return TokenAccess{}, fmt.Errorf("auth: %w", err)
	}
	if json.Unmarshal([]byte(scopes), &a.Scopes) != nil || a.Scopes == nil {
		a.Scopes = []string{}
	}
	now := s.now().UnixMilli()
	a.OK = !disabled.Valid && !revoked.Valid && expires.Valid && expires.Int64 > now
	return a, nil
}

// dummyTokenHash is compared against when a token's ID is unknown, so the
// work done doesn't depend on whether the ID exists.
var dummyTokenHash = tokenHash(TokenPrefix + strings.Repeat("a", 26) + "_" + strings.Repeat("A", 52))

// AuthenticateToken checks a presented token and returns the identity it
// acts as. Errors: ErrBadToken (malformed, unknown, wrong secret, or its
// user disabled), and, only when the secret matched, ErrTokenExpired or
// ErrTokenRevoked. ip is recorded as its last use, at most once a minute.
func (s *Service) AuthenticateToken(ctx context.Context, presented, ip string) (Identity, error) {
	m := tokenSecretRE.FindStringSubmatch(presented)
	if m == nil {
		return Identity{}, ErrBadToken
	}
	var (
		stored                 []byte
		name, scopes           string
		u                      User
		expires, used, revoked sql.NullInt64
		disabled               sql.NullInt64
	)
	err := s.st.Reader().QueryRowContext(ctx, `SELECT t.token_hash, t.name, t.scopes, t.expires_at, t.last_used_at, t.revoked_at, u.id, u.username, u.role, u.disabled_at
		FROM api_tokens t JOIN users u ON u.id = t.user_id WHERE t.id = ?`, tokenIDPrefix+m[1]).
		Scan(&stored, &name, &scopes, &expires, &used, &revoked, &u.ID, &u.Username, &u.Role, &disabled)
	if errors.Is(err, sql.ErrNoRows) {
		subtle.ConstantTimeCompare(tokenHash(presented), dummyTokenHash)
		return Identity{}, ErrBadToken
	}
	if err != nil {
		return Identity{}, fmt.Errorf("auth: %w", err)
	}
	if subtle.ConstantTimeCompare(tokenHash(presented), stored) != 1 || disabled.Valid {
		return Identity{}, ErrBadToken
	}
	now := s.now()
	switch {
	case revoked.Valid:
		return Identity{}, ErrTokenRevoked
	case !expires.Valid || expires.Int64 <= now.UnixMilli():
		return Identity{}, ErrTokenExpired
	}
	ref := &TokenRef{ID: tokenIDPrefix + m[1], Name: name}
	if json.Unmarshal([]byte(scopes), &ref.Scopes) != nil || ref.Scopes == nil {
		ref.Scopes = []string{}
	}
	if !used.Valid || now.Sub(time.UnixMilli(used.Int64)) >= tokenLastUsedEvery {
		if _, err := s.st.Writer().ExecContext(ctx, `UPDATE api_tokens SET last_used_at = ?, last_used_ip = ? WHERE id = ? AND (last_used_at IS NULL OR last_used_at <= ?)`,
			now.UnixMilli(), ip, ref.ID, now.Add(-tokenLastUsedEvery).UnixMilli()); err != nil {
			s.log.Warn("could not record an API token's use", "token", ref.ID, "err", err)
		}
	}
	return Identity{User: u, Token: ref, IP: ip}, nil
}

// BearerToken returns the Cha Dash API token in r's Authorization header,
// if there is one. Only `Bearer cdt_…` counts: another scheme (a reverse
// proxy's Basic auth) or another kind of bearer token (an identity proxy's)
// isn't ours, so it's ignored rather than refused.
func BearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	scheme, value, ok := strings.Cut(h, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, TokenPrefix) {
		return "", false
	}
	return value, true
}

// RequireToken authenticates r's API token and attaches its Identity, or
// answers 401 (429 once a client has failed too often). Bad tokens draw on
// a per-client budget, 10 then one every 6 seconds, of their own, so a
// guessing script is slowed and nobody's sign-in budget is touched. A valid
// token never draws on it.
func (s *Service) RequireToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := FromContext(r.Context()); ok {
			next.ServeHTTP(w, r)
			return
		}
		presented, ok := BearerToken(r)
		if !ok {
			httpx.Error(w, http.StatusUnauthorized, "unauthenticated", "sign in required")
			return
		}
		ip := s.proxies.ClientIP(r)
		key := "token|" + clientKey(ip)
		if wait := s.tokenFailures.wait(key); wait > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
			httpx.Error(w, http.StatusTooManyRequests, "rate_limited", "Too many bad API tokens from this address; try again later.")
			return
		}
		id, err := s.AuthenticateToken(r.Context(), presented, ip.String())
		if err != nil {
			s.tokenFailures.allow(key)
			switch {
			case errors.Is(err, ErrTokenExpired):
				httpx.Error(w, http.StatusUnauthorized, "token_expired", "This API token has expired. Make a new one in Settings → API tokens.")
			case errors.Is(err, ErrTokenRevoked):
				httpx.Error(w, http.StatusUnauthorized, "token_revoked", "This API token has been revoked.")
			case errors.Is(err, ErrBadToken):
				httpx.Error(w, http.StatusUnauthorized, "invalid_token", "This API token isn't valid.")
			default:
				s.log.Error("API token lookup failed", "err", err)
				httpx.Error(w, http.StatusInternalServerError, "internal", "internal error")
			}
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, id)))
	})
}
