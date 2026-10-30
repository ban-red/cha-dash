package auth

// Forward-auth (ADR-0022): a reverse proxy that has already signed someone
// in (Authelia, authentik, oauth2-proxy) passes who it is in headers, by
// default Remote-User, Remote-Groups and Remote-Email.
//
// Trust. The headers count only when forward-auth is on and the TCP peer is
// in CHADASH_TRUSTED_PROXIES. Otherwise ForwardAuth strips them before any
// handler runs, and when forward-auth is on, the first such request from a
// peer in an hour is an event (audited as auth.forward.untrusted). Headers
// that count are consumed here too: no handler ever reads them.
//
// Sessions. A trusted request naming a username linked to a user
// (forward_auth_links), with no session, opens a session with method
// forward_auth, mapped through the same groups as OIDC; with provisioning
// on, an unknown username whose groups map to a role gets an account. A
// session whose user differs from the one the header names ends, and the
// header's user is signed in instead. A session from another method whose
// user the header doesn't name (an unlinked username) is left alone, so its
// user can link the username from their settings. A forward_auth session
// re-reads the groups on every request, so a role changed at the proxy
// applies at once and losing every mapped group ends it.
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

const (
	// untrustedEvery: one untrusted-proxy event per peer this often.
	untrustedEvery = time.Hour
	// maxForwardNotes bounds the throttle's memory.
	maxForwardNotes = 4096
)

type forwardState struct {
	mu   sync.Mutex
	last map[string]time.Time
}

// once reports whether key hasn't fired within every, and marks it.
func (f *forwardState) once(key string, now time.Time, every time.Duration) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.last == nil {
		f.last = map[string]time.Time{}
	}
	if t, ok := f.last[key]; ok && now.Sub(t) < every {
		return false
	}
	if len(f.last) >= maxForwardNotes {
		for k, t := range f.last {
			if now.Sub(t) >= every {
				delete(f.last, k)
			}
		}
		if len(f.last) >= maxForwardNotes {
			return false
		}
	}
	f.last[key] = now
	return true
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
		groups := splitGroups(r.Header.Get(fa.GroupsHeader))
		email := strings.TrimSpace(r.Header.Get(fa.EmailHeader))
		trusted := s.proxies.ViaTrusted(r)
		r = withoutHeaders(r, names)
		_, bearer := BearerToken(r)
		switch {
		case !fa.Enabled:
		case !trusted:
			s.noteUntrusted(r)
		case bearer:
			// An API token is the credential, whoever the proxy says it is.
		case username != "":
			r = r.WithContext(context.WithValue(r.Context(), forwardNameKey{}, username))
			r = s.forwardSignIn(w, r, set, username, groups, email)
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
	peer := httpx.PeerAddr(r).String()
	if !s.forward.once("peer|"+peer, s.now(), untrustedEvery) {
		return
	}
	s.log.Warn("forward-auth headers from a peer that isn't a trusted proxy were ignored; list your proxy in CHADASH_TRUSTED_PROXIES", "peer", peer)
	s.emit(Event{Kind: EventForwardAuthUntrusted, IP: peer, Reason: "peer not in CHADASH_TRUSTED_PROXIES", Request: r})
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

// forwardSignIn applies a trusted request's headers and returns the
// request to serve: with the new identity on its context when it opened a
// session.
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
			if sess.Method == MethodForwardAuth {
				// The proxy is the source of truth for this session's groups.
				if _, ok, err := s.applyGroups(ctx, u, set, groups); err != nil {
					s.log.Error("forward-auth: could not apply the group mapping", "err", err)
				} else if !ok {
					s.refuseForward(r, u.ID, username, OIDCErrNoGroup)
				}
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
	var u User
	switch {
	case isLinked && disabled:
		s.refuseForward(r, linked.ID, username, OIDCErrDisabled)
		return r
	case isLinked:
		role, ok, err := s.applyGroups(ctx, linked, set, groups)
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
		nu, err := s.provision(ctx, []string{username, emailLocal(email)}, role, func(tx *sql.Tx, userID string, now int64) error {
			_, err := tx.ExecContext(ctx, `INSERT INTO forward_auth_links (username, user_id, created_at) VALUES (?, ?, ?)`, username, userID, now)
			return err
		})
		if err != nil {
			s.log.Error("forward-auth: could not create an account", "err", err)
			return r
		}
		s.log.Info("account created on first forward-auth sign-in", "user", nu.Username, "role", nu.Role)
		u = nu
	}
	_, sess, err := s.startSession(w, r, u, MethodForwardAuth)
	if err != nil {
		s.log.Error("forward-auth: could not start a session", "err", err)
		return r
	}
	id := Identity{User: u, Session: sess, IP: s.proxies.ClientIP(r).String()}
	return r.WithContext(context.WithValue(ctx, ctxKey{}, id))
}

// refuseForward logs and emits a refused forward-auth sign-in, once per
// username and reason an hour: the proxy sends the header on every request.
func (s *Service) refuseForward(r *http.Request, userID, username, code string) {
	if !s.forward.once("refuse|"+code+"|"+username, s.now(), untrustedEvery) {
		return
	}
	ip := s.proxies.ClientIP(r).String()
	s.log.Info("forward-auth sign-in refused", "reason", refusalReason(code), "client", ip)
	s.emit(Event{Kind: EventSignInRefused, UserID: userID, Method: MethodForwardAuth, Reason: refusalReason(code), Subject: username, Request: r, IP: ip})
}

// ForwardAuthUsername is the username a trusted proxy names on r, when
// forward-auth is on; "" otherwise. ForwardAuth has already removed the
// headers by the time a handler runs, so it keeps the name on the context.
func ForwardAuthUsername(ctx context.Context) string {
	u, _ := ctx.Value(forwardNameKey{}).(string)
	return u
}

type forwardNameKey struct{}
