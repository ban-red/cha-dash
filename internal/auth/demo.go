package auth

// The public demo's sign-in (CHADASH_DEMO_PUBLIC; docs/plan/14-v0.4.md,
// "Reach"): every visitor is signed in as one fixed operator account with no
// password, so it can't sign in any other way. What that operator may do
// there is internal/authz's business: Guard refuses everything but the Demo
// Home's actions (authz.DemoRefuses).
//
//	POST /api/auth/demo → 200 {"user"} + session cookie
//
// A visitor who already has a demo session gets it back. New sessions are
// limited per client address (httpx.ClientKey: an IPv6 /64 counts once), so
// a script can't fill the sessions table. First-run setup stays closed.

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"dash.cha.sh/internal/httpx"
	"dash.cha.sh/internal/msg"
)

const (
	// MethodDemo marks sessions the public demo started.
	MethodDemo = "demo"
	// DemoUsername is the fixed account every visitor of the public demo is.
	DemoUsername = "visitor"
)

// Demo sign-ins per client address: a burst of demoBurst, then one per
// demoEvery.
const (
	demoBurst = 5
	demoEvery = time.Minute
)

// EnablePublicDemo turns the hub into the public demo: it finds or makes
// the passwordless operator DemoUsername, closes first-run setup and mounts
// POST /api/auth/demo at Register. Call it before Register. An existing
// account of that name with another role, or disabled, is an error: the
// data directory isn't a demo's.
func (s *Service) EnablePublicDemo(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var u User
	err := s.st.WriteTx(ctx, func(tx *sql.Tx) error {
		var disabled sql.NullInt64
		err := tx.QueryRowContext(ctx, `SELECT id, username, role, disabled_at FROM users WHERE username = ?`, DemoUsername).
			Scan(&u.ID, &u.Username, &u.Role, &disabled)
		switch {
		case err == nil && (u.Role != RoleOperator || disabled.Valid):
			return fmt.Errorf("auth: the account %q exists as a %s (disabled: %t), not the public demo's operator; start the demo with an empty data directory", DemoUsername, u.Role, disabled.Valid)
		case err == nil:
			return nil
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		u = User{ID: "u_" + strings.ToLower(rand.Text()), Username: DemoUsername, Role: RoleOperator}
		now := s.now().UnixMilli()
		_, err = tx.ExecContext(ctx,
			`INSERT INTO users (id, username, password_hash, role, created_at, updated_at) VALUES (?, ?, NULL, ?, ?, ?)`,
			u.ID, u.Username, u.Role, now, now)
		return err
	})
	if err != nil {
		return err
	}
	s.demoUser = u
	s.demoLimit = httpx.NewLimiter(demoBurst, demoEvery)
	s.setupOpen = false
	s.log.Warn("public demo: visitors are signed in as an operator; only the Demo Home's actions run", "user", u.Username)
	return nil
}

// PublicDemo reports whether EnablePublicDemo ran.
func (s *Service) PublicDemo() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.demoUser.ID != ""
}

func (s *Service) registerDemo(mux *http.ServeMux) {
	if s.PublicDemo() {
		mux.HandleFunc("POST /api/auth/demo", s.handleDemo)
	}
}

func (s *Service) handleDemo(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	u := s.demoUser
	s.mu.Unlock()
	if id, ok := s.Identify(r); ok && id.User.ID == u.ID {
		httpx.WriteJSON(w, http.StatusOK, map[string]User{"user": id.User})
		return
	}
	if ok, wait := s.demoLimit.Allow(httpx.ClientKey(s.proxies.ClientIP(r))); !ok {
		w.Header().Set("Retry-After", fmt.Sprint(int(wait.Seconds())+1))
		httpx.Say(w, http.StatusTooManyRequests, "rate_limited", msg.New("demo.signin_limit"))
		return
	}
	if _, err := s.StartSession(w, r, u, MethodDemo); err != nil {
		s.internalError(w, "could not start a demo session", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]User{"user": u})
}
