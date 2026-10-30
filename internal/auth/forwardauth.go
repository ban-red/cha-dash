package auth

// Forward-auth (ADR-0022): a reverse proxy that has already signed someone
// in (Authelia, authentik, oauth2-proxy) passes who it is in headers, by
// default Remote-User, Remote-Groups and Remote-Email.
//
// Trust. The headers count only when forward-auth is on and the TCP peer is
// in CHADASH_TRUSTED_PROXIES. Otherwise ForwardAuth strips them before any
// handler runs, and when forward-auth is on, the first such request from a
// client (an address, or an IPv6 /64) in an hour is an event (audited as
// auth.forward.untrusted). A configured header sent more than once makes
// none of them count, and is an event too (auth.forward.duplicate), as
// often. Headers that count are consumed here: no handler ever reads them.
// The proxy must set or clear every configured header on every request: a
// groups or email header a client sent that the proxy passed on counts as
// the proxy's. Settings → Sign-in shows the groups header last received.
//
// Sessions. A trusted request naming a username linked to a user
// (forward_auth_links) and carrying no session opens a session with method
// forward_auth, mapped through the same groups as OIDC; with provisioning
// on, an unknown username whose groups map to a role gets an account. Only
// a top-level navigation opens a session (or provisions): any other request
// without a cookie reuses a live forward_auth session for the same user,
// address and user agent, or, when there's none, is served as the user
// with no session at all (nothing a session holds, such as a step-up,
// applies). So a script's requests don't each leave a session behind.
//
// A session whose user differs from the one the header names ends, and
// the header's user is signed in instead. A session from another method
// whose user the header doesn't name (an unlinked username) is left alone,
// so its user can link the username from their settings. When the header
// names the session's own user, the groups apply on every request, however
// the session began, so a role changed at the proxy applies at once and
// losing every mapped group ends the user's sessions.
//
// Banned addresses (bans.go) get no new session and no new account here;
// sessions they already hold keep working, as with passwords.
//
// Step-up never comes from a header: a proxy can't prove someone
// authenticated recently.

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"dash.cha.sh/internal/httpx"
)

// untrustedEvery: one untrusted-proxy, duplicate-header or refusal event
// per key this often.
const untrustedEvery = time.Hour

// maxGroupsSeen bounds the groups header value kept for Settings → Sign-in.
const maxGroupsSeen = 1024

type forwardState struct {
	// Each kind of record has its own throttle, so one can't crowd out
	// another.
	untrusted, duplicate, refused noteThrottle

	mu     sync.Mutex
	groups *ForwardGroupsSeen
}

// ForwardGroupsSeen is the groups header a trusted proxy last sent, since
// the hub started.
type ForwardGroupsSeen struct {
	Header string    `json:"header"`
	Value  string    `json:"value"`
	At     time.Time `json:"at"`
}

func (f *forwardState) sawGroups(header, value string, now time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.groups = &ForwardGroupsSeen{Header: header, Value: clip(value, maxGroupsSeen), At: now.UTC()}
}

// LastForwardGroups is the groups header a trusted proxy last sent with
// forward-auth on, or nil when none has since the hub started.
func (s *Service) LastForwardGroups() *ForwardGroupsSeen {
	s.forward.mu.Lock()
	defer s.forward.mu.Unlock()
	if s.forward.groups == nil {
		return nil
	}
	g := *s.forward.groups
	return &g
}

// forwardHeaders are the names stripped from untrusted requests: the
// configured ones and the defaults.
func forwardHeaders(fa ForwardAuthSettings) []string {
	names := []string{DefaultUserHeader, DefaultGroupsHeader, DefaultEmailHeader}
	for _, h := range []string{fa.UserHeader, fa.GroupsHeader, fa.EmailHeader} {
		if h = http.CanonicalHeaderKey(h); h != "" && !slices.Contains(names, h) {
			names = append(names, h)
		}
	}
	return names
}

func withoutHeaders(r *http.Request, names []string) *http.Request {
	r = r.Clone(r.Context())
	for _, n := range names {
		r.Header.Del(n)
	}
	return r
}

// repeatedHeader is the first configured header r carries more than once,
// or "".
func repeatedHeader(r *http.Request, fa ForwardAuthSettings) string {
	for _, h := range []string{fa.UserHeader, fa.GroupsHeader, fa.EmailHeader} {
		if h != "" && len(r.Header.Values(h)) > 1 {
			return http.CanonicalHeaderKey(h)
		}
	}
	return ""
}

// ForwardAuth is the middleware described at the top of this file. It
// sits in front of authz.Guard, so it runs before every handler.
func (s *Service) ForwardAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		set := s.SignIn()
		fa := set.ForwardAuth
		names := forwardHeaders(fa)
		if !slices.ContainsFunc(names, func(n string) bool { return len(r.Header.Values(n)) > 0 }) {
			next.ServeHTTP(w, r)
			return
		}
		username := strings.TrimSpace(r.Header.Get(fa.UserHeader))
		groupValues := r.Header.Values(fa.GroupsHeader)
		groups := splitGroups(r.Header.Get(fa.GroupsHeader))
		email := strings.TrimSpace(r.Header.Get(fa.EmailHeader))
		repeated := repeatedHeader(r, fa)
		trusted := s.proxies.ViaTrusted(r)
		r = withoutHeaders(r, names)
		_, bearer := BearerToken(r)
		switch {
		case !fa.Enabled:
		case !trusted:
			s.noteUntrusted(r)
		case repeated != "":
			s.noteRepeated(r, repeated)
		default:
			if len(groupValues) > 0 {
				s.forward.sawGroups(http.CanonicalHeaderKey(fa.GroupsHeader), groupValues[0], s.now())
			}
			if !bearer && username != "" {
				// An API token is the credential, whoever the proxy says it is.
				r = r.WithContext(context.WithValue(r.Context(), forwardNameKey{}, username))
				r = s.forwardSignIn(w, r, set, username, groups, email)
			}
		}
		next.ServeHTTP(w, r)
	})
}

func splitGroups(h string) []string {
	var out []string
	for g := range strings.SplitSeq(h, ",") {
		if g = strings.TrimSpace(g); g != "" && len(out) < 256 {
			out = append(out, g)
		}
	}
	return out
}

func (s *Service) noteUntrusted(r *http.Request) {
	peer := httpx.PeerAddr(r)
	ok, n := s.forward.untrusted.allow(httpx.ClientKey(peer), s.now(), untrustedEvery)
	if !ok {
		return
	}
	s.log.Warn("forward-auth headers from a peer that isn't a trusted proxy were ignored; list your proxy in CHADASH_TRUSTED_PROXIES", "peer", peer.String())
	s.emit(Event{Kind: EventForwardAuthUntrusted, IP: peer.String(), Reason: "peer not in CHADASH_TRUSTED_PROXIES", Request: r, Count: n})
}

// noteRepeated records a trusted request that carried a forward-auth header
// more than once: a proxy that appends rather than sets it, or a client's
// copy that got through. Neither is safe to pick from.
func (s *Service) noteRepeated(r *http.Request, header string) {
	ip := s.proxies.ClientIP(r)
	ok, n := s.forward.duplicate.allow(httpx.ClientKey(ip), s.now(), untrustedEvery)
	if !ok {
		return
	}
	s.log.Warn("forward-auth: a header came more than once, so the forward-auth headers were ignored; the proxy must set it, not add to it", "header", header, "client", ip.String())
	s.emit(Event{Kind: EventForwardAuthDuplicate, IP: ip.String(), Reason: header, Request: r, Count: n})
}

// forwardUser is the user username is linked to, if any.
func (s *Service) forwardUser(ctx context.Context, username string) (User, bool, bool, error) {
	var u User
	var disabled sql.NullInt64
	err := s.st.Reader().QueryRowContext(ctx, `SELECT u.id, u.username, u.role, u.disabled_at FROM forward_auth_links f JOIN users u ON u.id = f.user_id
		WHERE f.username = ?`, username).Scan(&u.ID, &u.Username, &u.Role, &disabled)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, false, false, nil
	}
	if err != nil {
		return User{}, false, false, err
	}
	return u, true, disabled.Valid, nil
}

// isNavigation: r is a top-level navigation, the only kind of request that
// opens a forward-auth session. Sec-Fetch-Mode says so; without it, an
// Accept that asks for HTML.
func isNavigation(r *http.Request) bool {
	if m := r.Header.Get("Sec-Fetch-Mode"); m != "" {
		return m == "navigate"
	}
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

// liveForwardSession is userID's newest live forward_auth session from ip
// with userAgent, if any.
func (s *Service) liveForwardSession(ctx context.Context, userID, ip, userAgent string) (Session, bool, error) {
	if len(userAgent) > 256 {
		userAgent = userAgent[:256] // as createSession stores it
	}
	sess := Session{UserID: userID, Method: MethodForwardAuth}
	var created, expires, seen int64
	var stepUp sql.NullInt64
	err := s.st.Reader().QueryRowContext(ctx, `SELECT id, created_at, expires_at, last_seen_at, step_up_until FROM sessions
		WHERE user_id = ? AND method = ? AND ip = ? AND user_agent = ? AND expires_at > ?
		ORDER BY last_seen_at DESC LIMIT 1`, userID, MethodForwardAuth, ip, userAgent, s.now().UnixMilli()).
		Scan(&sess.ID, &created, &expires, &seen, &stepUp)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, false, nil
	}
	if err != nil {
		return Session{}, false, err
	}
	sess.CreatedAt, sess.ExpiresAt, sess.lastSeen = time.UnixMilli(created), time.UnixMilli(expires), time.UnixMilli(seen)
	if stepUp.Valid {
		sess.StepUpUntil = time.UnixMilli(stepUp.Int64)
	}
	return sess, true, nil
}

// forwardSignIn applies a trusted request's headers and returns the
// request to serve: with the header's user on its context when it signed
// them in.
func (s *Service) forwardSignIn(w http.ResponseWriter, r *http.Request, set SignInSettings, username string, groups []string, email string) *http.Request {
	ctx := r.Context()
	if len(username) > 255 {
		return r
	}
	linked, isLinked, disabled, err := s.forwardUser(ctx, username)
	if err != nil {
		s.log.Error("forward-auth: could not look up the username", "err", err)
		return r
	}
	if c, err := r.Cookie(CookieName); err == nil && c.Value != "" {
		sess, u, err := s.lookupSession(ctx, c.Value)
		switch {
		case errors.Is(err, ErrNoSession):
			// Fall through to signing in.
		case err != nil:
			s.log.Error("forward-auth: session lookup failed", "err", err)
			return r
		case isLinked && linked.ID == u.ID:
			// The proxy is the source of truth for this user's groups,
			// whichever way this session began: a password or passkey
			// session doesn't keep a role the groups took away.
			if _, ok, err := s.applyGroups(r, u, set, groups, MethodForwardAuth); err != nil {
				s.log.Error("forward-auth: could not apply the group mapping", "err", err)
			} else if !ok {
				s.refuseForward(r, u.ID, username, OIDCErrNoGroup)
			}
			return r
		case !isLinked && sess.Method != MethodForwardAuth:
			return r // someone signed in another way; they may link this username
		default:
			// The header names someone else: this session ends.
			if id, user, err := s.deleteSession(ctx, c.Value); err != nil {
				s.log.Error("forward-auth: could not end the session", "err", err)
				return r
			} else if id != "" {
				s.log.Info("forward-auth: the proxy names another user; the session ended", "session", id)
				s.emit(Event{Kind: EventSessionEnded, SessionID: id, UserID: user, Request: r})
			}
		}
	}
	ip := s.proxies.ClientIP(r)
	switch ban, err := s.Banned(ctx, ip); {
	case err != nil:
		s.log.Error("forward-auth: could not check the ban list", "err", err)
		return r
	case ban:
		s.refuseForward(r, linked.ID, username, OIDCErrBanned)
		return r
	}
	nav := isNavigation(r)
	var u User
	provisioned := false
	switch {
	case isLinked && disabled:
		s.refuseForward(r, linked.ID, username, OIDCErrDisabled)
		return r
	case isLinked:
		role, ok, err := s.applyGroups(r, linked, set, groups, MethodForwardAuth)
		if err != nil {
			s.log.Error("forward-auth: could not apply the group mapping", "err", err)
			return r
		}
		if !ok {
			s.refuseForward(r, linked.ID, username, OIDCErrNoGroup)
			return r
		}
		u = linked
		u.Role = role
	default:
		role := set.MappedRole(groups)
		if !set.Provision || role == "" || s.SetupRequired() {
			s.refuseForward(r, "", username, OIDCErrUnlinked)
			return r
		}
		if !nav {
			return r // an account is made by a page load, not a script's request
		}
		nu, err := s.provision(ctx, []string{username, emailLocal(email)}, role, func(tx *sql.Tx, userID string, now int64) error {
			_, err := tx.ExecContext(ctx, `INSERT INTO forward_auth_links (username, user_id, created_at) VALUES (?, ?, ?)`, username, userID, now)
			return err
		})
		if err != nil {
			s.log.Error("forward-auth: could not create an account", "err", err)
			return r
		}
		s.log.Info("account created on first forward-auth sign-in", "user", nu.Username, "role", nu.Role)
		u, provisioned = nu, true
	}
	var sess Session
	if nav {
		if _, sess, err = s.startSession(w, r, u, MethodForwardAuth, provisioned); err != nil {
			s.log.Error("forward-auth: could not start a session", "err", err)
			return r
		}
	} else {
		live, ok, err := s.liveForwardSession(ctx, u.ID, ip.String(), r.UserAgent())
		if err != nil {
			s.log.Error("forward-auth: could not look for a session", "err", err)
			return r
		}
		if !ok {
			live = Session{UserID: u.ID, Method: MethodForwardAuth} // no session: the user, for this request only
		}
		sess = live
	}
	id := Identity{User: u, Session: sess, IP: ip.String()}
	return r.WithContext(context.WithValue(ctx, ctxKey{}, id))
}

// refuseForward logs and emits a refused forward-auth sign-in, once per
// username and reason an hour: the proxy sends the header on every request.
func (s *Service) refuseForward(r *http.Request, userID, username, code string) {
	ok, n := s.forward.refused.allow(code+"|"+username, s.now(), untrustedEvery)
	if !ok {
		return
	}
	ip := s.proxies.ClientIP(r).String()
	s.log.Info("forward-auth sign-in refused", "reason", refusalReason(code), "client", ip)
	s.emit(Event{Kind: EventSignInRefused, UserID: userID, Method: MethodForwardAuth, Reason: refusalReason(code), Subject: username, Request: r, IP: ip, Count: n})
}

// ForwardAuthUsername is the username a trusted proxy names on r, when
// forward-auth is on; "" otherwise. ForwardAuth has already removed the
// headers by the time a handler runs, so it keeps the name on the context.
func ForwardAuthUsername(ctx context.Context) string {
	u, _ := ctx.Value(forwardNameKey{}).(string)
	return u
}

type forwardNameKey struct{}
