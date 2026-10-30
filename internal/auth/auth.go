// Package auth handles first-run setup, local accounts and cookie sessions.
//
// First run: while no user exists, POST /api/setup creates the owner (named
// admin unless the request names someone else) and signs them in. There's no
// setup token: whoever reaches a new hub first claims it.
//
// Sessions: a random 256-bit token in an HttpOnly, SameSite=Lax cookie. Only
// its SHA-256 is stored, so a database leak doesn't leak live sessions.
//
// Passkeys (WebAuthn; passkeys.go) sit beside password login: verifying a
// credential is separate from StartSession, which every sign-in method ends with.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"dash.cha.sh/internal/httpx"
	"dash.cha.sh/internal/store"
)

const (
	CookieName = "chadash_session"
	// SessionTTL is an idle timeout: each use renews it, so a session in
	// regular use never expires and one left unused for 30 days does.
	SessionTTL = 30 * 24 * time.Hour
	// renewEvery throttles renewal: expiry (and the cookie) moves forward at
	// most once a day, so sliding costs one write per session per day.
	renewEvery = 24 * time.Hour
	// lastSeenEvery throttles session last_seen writes on the request path.
	lastSeenEvery = 5 * time.Minute

	MinPasswordLen = 6   // characters
	MaxPasswordLen = 512 // bytes; bounds hashing input
)

// Sign-in methods recorded on sessions; step-up checks will key off these.
const (
	MethodPassword = "password"
	MethodPasskey  = "passkey"
	// MethodOIDC and MethodForwardAuth are in signin.go.
)

// Roles, most powerful first. What each may do is decided in internal/authz,
// never here; this package only stores them and keeps the one-owner rule.
const (
	RoleOwner     = "owner"
	RoleAdmin     = "admin"
	RoleOperator  = "operator"
	RoleViewer    = "viewer"
	RoleHousehold = "household"
)

// Roles lists every role, most powerful first. The users table's CHECK
// constraint lists the same five.
var Roles = []string{RoleOwner, RoleAdmin, RoleOperator, RoleViewer, RoleHousehold}

// ValidRole reports whether r is one of Roles.
func ValidRole(r string) bool { return slices.Contains(Roles, r) }

var (
	ErrSetupComplete  = errors.New("auth: setup already completed")
	ErrBadCredentials = errors.New("auth: invalid username or password")
	ErrNoSession      = errors.New("auth: no valid session")
)

// AccountLimitedError is Login's answer when an account has used up its
// budget of wrong passwords. Passkey sign-in isn't limited this way.
type AccountLimitedError struct {
	UserID string
	Wait   time.Duration
}

func (e *AccountLimitedError) Error() string {
	return "auth: too many wrong passwords for this account"
}

// ValidationError is a user-fixable input problem.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

var usernameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@-]{0,63}$`)

// User is an account as the API exposes it.
type User struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Role     string `json:"role"`
}

// Session is a signed-in browser or client.
type Session struct {
	ID        string    `json:"id"`
	UserID    string    `json:"-"`
	Method    string    `json:"method"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
	// StepUpUntil is when the session's step-up grant lapses (zero: none).
	// GET /api/auth/session reports it beside the session; see stepup.go.
	StepUpUntil time.Time `json:"-"`
	lastSeen    time.Time
}

// Service is the auth subsystem.
type Service struct {
	st      *store.Store
	log     *slog.Logger
	proxies httpx.Proxies
	limiter *httpx.Limiter
	// failures is the per-account budget for wrong passwords, keyed by user
	// ID, so changing address doesn't buy more guesses at one account.
	failures *httpx.Limiter
	// tokenFailures is the per-client budget for bad API tokens.
	tokenFailures *httpx.Limiter
	now           func() time.Time
	params        argonParams

	// Passkeys: the relying party from CHADASH_PUBLIC_URL (or why it can't be
	// one), and the ceremonies begun but not yet finished.
	publicURL  string
	publicRP   relyingParty
	publicWhy  string
	ceremonies ceremonies

	// stepUpMode is the owner's step-up rule (SetStepUpMode).
	stepUpMode stepUpModeFunc

	// events are told about sign-outs, account changes, step-ups and
	// passkey changes (OnEvent).
	events listeners

	mu        sync.Mutex
	setupOpen bool // true until an owner exists

	// strikes counts wrong passwords per client address, then per username (bans.go).
	banMu   sync.Mutex
	strikes map[string]map[string]int

	// signIn is where the OIDC and forward-auth settings come from
	// (signin.go); oidc caches the provider's discovery and keys (oidc.go);
	// oidcTest holds the owner's last Test sign-in; forward holds
	// forward-auth's per-peer audit throttle (forwardauth.go).
	signIn   signInSource
	oidc     oidcClient
	oidcTest oidcTests
	forward  forwardState
}

// Option adjusts New.
type Option func(*Service)

// WithPublicURL passes CHADASH_PUBLIC_URL, the scheme://host[:port] people open
// Cha Dash at. Passkeys take their relying party ID and origin from it.
func WithPublicURL(u string) Option { return func(s *Service) { s.publicURL = u } }

// New starts the auth service. With no users it opens first-run setup.
func New(ctx context.Context, st *store.Store, log *slog.Logger, proxies httpx.Proxies, opts ...Option) (*Service, error) {
	s := &Service{
		st:       st,
		log:      log,
		proxies:  proxies,
		limiter:  httpx.NewLimiter(10, 6*time.Second), // 10 attempts, then one per 6 s, per client IP
		failures: httpx.NewLimiter(20, time.Minute),   // 20 wrong passwords, then one a minute, per account
		// 10 bad API tokens, then one per 6 s, per client IP.
		tokenFailures: httpx.NewLimiter(10, 6*time.Second),
		now:           time.Now,
		params:        defaultParams,
	}
	for _, o := range opts {
		o(s)
	}
	if s.publicURL != "" {
		rp, why, err := publicRelyingParty(s.publicURL)
		if err != nil {
			return nil, err
		}
		s.publicRP, s.publicWhy = rp, why
		if why != "" {
			log.Warn("passkeys are unavailable: "+why, "public_url", s.publicURL)
		}
	}
	if _, err := st.Writer().ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, s.now().UnixMilli()); err != nil {
		return nil, fmt.Errorf("auth: prune sessions: %w", err)
	}
	var users int
	if err := st.Reader().QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&users); err != nil {
		return nil, fmt.Errorf("auth: %w", err)
	}
	// Compute the timing-equaliser hash now, not during the first failed login.
	go dummyHash()
	if users == 0 {
		s.setupOpen = true
		// At warn so it survives a quiet log level: until an owner exists,
		// anyone who can reach the hub can claim it.
		log.Warn("first run: no owner account yet; open Cha Dash to create one")
	}
	return s, nil
}

// SetupRequired reports whether the owner account still needs creating.
func (s *Service) SetupRequired() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.setupOpen
}

// DefaultOwnerUsername names the owner when setup doesn't.
const DefaultOwnerUsername = "admin"

// Setup creates the owner account, named DefaultOwnerUsername when username
// is empty. Invalid input leaves setup open; success closes it.
func (s *Service) Setup(ctx context.Context, username, password string) (User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.setupOpen {
		return User{}, ErrSetupComplete
	}
	if username == "" {
		username = DefaultOwnerUsername
	}
	if err := validateCredentials(username, password); err != nil {
		return User{}, err
	}
	u := User{ID: "u_" + strings.ToLower(rand.Text()), Username: username, Role: RoleOwner}
	hash := hashPassword(password, s.params)
	err := s.st.WriteTx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return ErrSetupComplete
		}
		now := s.now().UnixMilli()
		_, err := tx.ExecContext(ctx,
			`INSERT INTO users (id, username, password_hash, role, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
			u.ID, u.Username, hash, u.Role, now, now)
		return err
	})
	if err != nil {
		if errors.Is(err, ErrSetupComplete) {
			s.setupOpen = false
		}
		return User{}, err
	}
	s.setupOpen = false
	s.log.Info("owner account created; setup closed", "user", u.Username)
	return u, nil
}

func validateCredentials(username, password string) error {
	if !usernameRE.MatchString(username) {
		return &ValidationError{"username must be 1–64 letters, digits or . _ @ -, starting with a letter or digit"}
	}
	if utf8.RuneCountInString(password) < MinPasswordLen {
		return &ValidationError{fmt.Sprintf("password must be at least %d characters", MinPasswordLen)}
	}
	if len(password) > MaxPasswordLen {
		return &ValidationError{fmt.Sprintf("password must be at most %d bytes", MaxPasswordLen)}
	}
	return nil
}

// dummyHash is verified against when a username doesn't exist, so response
// timing doesn't reveal which usernames are real.
var dummyHash = sync.OnceValue(func() string { return hashPassword("not a real password", defaultParams) })

// Login checks a username and password.
func (s *Service) Login(ctx context.Context, username, password string) (User, error) {
	if len(password) > MaxPasswordLen {
		return User{}, ErrBadCredentials
	}
	var u User
	var hash sql.NullString
	var disabled sql.NullInt64
	err := s.st.Reader().QueryRowContext(ctx,
		`SELECT id, username, role, password_hash, disabled_at FROM users WHERE username = ?`, username).
		Scan(&u.ID, &u.Username, &u.Role, &hash, &disabled)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !hash.Valid) {
		_, _ = verifyPassword(password, dummyHash())
		return User{}, ErrBadCredentials
	}
	if err != nil {
		return User{}, fmt.Errorf("auth: %w", err)
	}
	if wait := s.failures.Wait(u.ID); wait > 0 {
		return User{}, &AccountLimitedError{UserID: u.ID, Wait: wait}
	}
	ok, err := verifyPassword(password, hash.String)
	if err != nil {
		return User{}, fmt.Errorf("auth: user %s: %w", u.ID, err)
	}
	if !ok {
		s.failures.Allow(u.ID)
	}
	if !ok || disabled.Valid {
		return User{}, ErrBadCredentials
	}
	return u, nil
}

// createSession stores a new session and returns its cookie token.
func (s *Service) createSession(ctx context.Context, userID, method, ip, userAgent string) (string, Session, error) {
	token := rand.Text() + rand.Text() // two 128-bit draws: 256 bits
	now := s.now()
	sess := Session{
		ID:        "s_" + strings.ToLower(rand.Text()),
		UserID:    userID,
		Method:    method,
		CreatedAt: now,
		ExpiresAt: now.Add(SessionTTL),
	}
	if len(userAgent) > 256 {
		userAgent = userAgent[:256]
	}
	err := s.st.WriteTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, now.UnixMilli()); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO sessions (id, token_hash, user_id, method, created_at, expires_at, last_seen_at, ip, user_agent)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			sess.ID, tokenHash(token), userID, method, now.UnixMilli(), sess.ExpiresAt.UnixMilli(), now.UnixMilli(), ip, userAgent)
		return err
	})
	if err != nil {
		return "", Session{}, fmt.Errorf("auth: create session: %w", err)
	}
	return token, sess, nil
}

// lookupSession resolves a cookie token to its session and user.
func (s *Service) lookupSession(ctx context.Context, token string) (Session, User, error) {
	if token == "" || len(token) > 128 {
		return Session{}, User{}, ErrNoSession
	}
	var sess Session
	var u User
	var created, expires, seen int64
	var stepUp sql.NullInt64
	// The role comes from the users row on every lookup, so a role change
	// takes effect on the user's next request.
	err := s.st.Reader().QueryRowContext(ctx, `
		SELECT s.id, s.method, s.created_at, s.expires_at, s.last_seen_at, s.step_up_until, u.id, u.username, u.role
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = ? AND s.expires_at > ? AND u.disabled_at IS NULL`,
		tokenHash(token), s.now().UnixMilli()).
		Scan(&sess.ID, &sess.Method, &created, &expires, &seen, &stepUp, &u.ID, &u.Username, &u.Role)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, User{}, ErrNoSession
	}
	if err != nil {
		return Session{}, User{}, fmt.Errorf("auth: %w", err)
	}
	sess.UserID = u.ID
	sess.CreatedAt = time.UnixMilli(created)
	sess.ExpiresAt = time.UnixMilli(expires)
	sess.lastSeen = time.UnixMilli(seen)
	if stepUp.Valid {
		sess.StepUpUntil = time.UnixMilli(stepUp.Int64)
	}
	return sess, u, nil
}

// touchSession records a use of sess and slides its expiry forward once the
// last renewal is more than renewEvery old. It reports whether it renewed, so
// the caller can reissue the cookie with the new expiry.
func (s *Service) touchSession(ctx context.Context, sess *Session) (renewed bool) {
	now := s.now()
	switch {
	case sess.ExpiresAt.Sub(now) < SessionTTL-renewEvery:
		expires := now.Add(SessionTTL)
		if _, err := s.st.Writer().ExecContext(ctx, `UPDATE sessions SET last_seen_at = ?, expires_at = ? WHERE id = ?`,
			now.UnixMilli(), expires.UnixMilli(), sess.ID); err != nil {
			s.log.Warn("could not renew session", "session", sess.ID, "err", err)
			return false
		}
		sess.ExpiresAt, sess.lastSeen = expires, now
		return true
	case now.Sub(sess.lastSeen) >= lastSeenEvery:
		if _, err := s.st.Writer().ExecContext(ctx, `UPDATE sessions SET last_seen_at = ? WHERE id = ?`, now.UnixMilli(), sess.ID); err != nil {
			s.log.Warn("could not update session last_seen", "session", sess.ID, "err", err)
		}
	}
	return false
}

// deleteSession deletes the session behind token and returns its ID and
// user ("" when there was none).
func (s *Service) deleteSession(ctx context.Context, token string) (id, userID string, err error) {
	err = s.st.Writer().QueryRowContext(ctx, `DELETE FROM sessions WHERE token_hash = ? RETURNING id, user_id`, tokenHash(token)).Scan(&id, &userID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	return id, userID, err
}

func tokenHash(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}
