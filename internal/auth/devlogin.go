//go:build devlogin

package auth

// Dev login gets a developer onto the front page in one click, with no setup
// token and no password. It exists only in binaries built with -tags devlogin,
// which `bun run dev` does. Release builds and images don't contain this file,
// and CI checks the image's build tags and that the route answers 404.
//
//	POST /api/auth/dev-login → 200 {"user"} + session cookie
//
// With no owner yet, it creates one named "dev" with no password (so the
// account can't sign in any other way) and spends the setup token. Otherwise
// it signs in as the existing owner. Only loopback clients are served.

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strings"

	"dash.cha.sh/internal/httpx"
)

const devLoginBuilt = true

const (
	// MethodDev marks sessions started by dev login.
	MethodDev   = "dev"
	devUsername = "dev"
)

var errOwnerDisabled = errors.New("auth: the owner account is disabled")

func (s *Service) registerDevLogin(mux *http.ServeMux) {
	s.log.Warn("dev login is built in: anyone on this machine can sign in as the owner; never run this binary for real")
	mux.HandleFunc("POST /api/auth/dev-login", s.handleDevLogin)
}

func (s *Service) handleDevLogin(w http.ResponseWriter, r *http.Request) {
	// RemoteAddr, not the forwarded client IP: a trusted proxy could vouch for anyone.
	if !fromLoopback(r) {
		httpx.Error(w, http.StatusForbidden, "forbidden", "dev login only answers on this machine")
		return
	}
	u, err := s.devOwner(r.Context())
	if errors.Is(err, errOwnerDisabled) {
		httpx.Error(w, http.StatusForbidden, "forbidden", "the owner account is disabled")
		return
	}
	if err != nil {
		s.log.Error("dev login failed", "err", err)
		httpx.Error(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	if _, err := s.StartSession(w, r, u, MethodDev); err != nil {
		s.log.Error("could not start session after dev login", "err", err)
		httpx.Error(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]User{"user": u})
}

func fromLoopback(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.Unmap().IsLoopback()
}

// devOwner returns the owner, creating a passwordless "dev" owner if there is none.
func (s *Service) devOwner(ctx context.Context) (User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var u User
	created := false
	err := s.st.WriteTx(ctx, func(tx *sql.Tx) error {
		var disabled sql.NullInt64
		err := tx.QueryRowContext(ctx, `SELECT id, username, role, disabled_at FROM users WHERE role = ?`, RoleOwner).
			Scan(&u.ID, &u.Username, &u.Role, &disabled)
		if err == nil && disabled.Valid {
			return errOwnerDisabled
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		u = User{ID: "u_" + strings.ToLower(rand.Text()), Username: devUsername, Role: RoleOwner}
		now := s.now().UnixMilli()
		_, err = tx.ExecContext(ctx,
			`INSERT INTO users (id, username, password_hash, role, created_at, updated_at) VALUES (?, ?, NULL, ?, ?, ?)`,
			u.ID, u.Username, u.Role, now, now)
		created = err == nil
		return err
	})
	if err != nil {
		return User{}, err
	}
	if created {
		s.setupToken = ""
		s.log.Info("dev login: created a passwordless owner; setup token spent", "user", u.Username)
	}
	return u, nil
}
