package auth

// Users. This file keeps the account invariants, whoever asks: exactly one
// owner, who can't be demoted or disabled except by handing ownership over;
// users are disabled, never deleted, so audit records keep pointing at
// someone; disabling ends every session at once. Who may ask for what is
// decided in internal/authz, which serves the users API.

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrUserNotFound = errors.New("auth: no such user")
	ErrUserExists   = errors.New("auth: username taken")
	// ErrOwnerProtected: the change would leave the hub without its owner.
	ErrOwnerProtected = errors.New("auth: the owner can only change by transfer")
	// ErrNotOwner: a transfer from someone who isn't the owner (any more).
	ErrNotOwner = errors.New("auth: not the owner")
)

// UserInfo is an account as the users API lists it.
type UserInfo struct {
	ID         string     `json:"id"`
	Username   string     `json:"username"`
	Role       string     `json:"role"`
	Disabled   bool       `json:"disabled"`
	HasPasskey bool       `json:"has_passkey"`
	LastSeenAt *time.Time `json:"last_seen_at"`
}

const userInfoQuery = `SELECT u.id, u.username, u.role, u.disabled_at IS NOT NULL,
	EXISTS (SELECT 1 FROM passkeys p WHERE p.user_id = u.id),
	(SELECT max(s.last_seen_at) FROM sessions s WHERE s.user_id = u.id)
	FROM users u`

func scanUserInfo(sc scanner) (UserInfo, error) {
	var u UserInfo
	var seen sql.NullInt64
	if err := sc.Scan(&u.ID, &u.Username, &u.Role, &u.Disabled, &u.HasPasskey, &seen); err != nil {
		return UserInfo{}, err
	}
	if seen.Valid {
		t := time.UnixMilli(seen.Int64).UTC()
		u.LastSeenAt = &t
	}
	return u, nil
}

// ListUsers returns every account, the owner first, then by username.
// last_seen_at is the latest of the user's live sessions. Never nil.
func (s *Service) ListUsers(ctx context.Context) ([]UserInfo, error) {
	rows, err := s.st.Reader().QueryContext(ctx, userInfoQuery+` ORDER BY u.role <> 'owner', u.username COLLATE NOCASE`)
	if err != nil {
		return nil, fmt.Errorf("auth: %w", err)
	}
	defer rows.Close()
	out := []UserInfo{}
	for rows.Next() {
		u, err := scanUserInfo(rows)
		if err != nil {
			return nil, fmt.Errorf("auth: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// GetUser returns one account.
func (s *Service) GetUser(ctx context.Context, id string) (UserInfo, error) {
	return s.getUser(ctx, s.st.Reader(), id)
}

type queryRower interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func (s *Service) getUser(ctx context.Context, q queryRower, id string) (UserInfo, error) {
	u, err := scanUserInfo(q.QueryRowContext(ctx, userInfoQuery+` WHERE u.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return UserInfo{}, ErrUserNotFound
	}
	if err != nil {
		return UserInfo{}, fmt.Errorf("auth: %w", err)
	}
	return u, nil
}

// errOwnerByTransfer is the sentence for asking for the owner role directly.
var errOwnerByTransfer = &ValidationError{"there is exactly one owner; transfer ownership to make someone else the owner"}

// CreateUser adds an account with a password. role is any role but owner.
func (s *Service) CreateUser(ctx context.Context, username, password, role string) (UserInfo, error) {
	if role == RoleOwner {
		return UserInfo{}, errOwnerByTransfer
	}
	if !ValidRole(role) {
		return UserInfo{}, &ValidationError{"role must be admin, operator, viewer or household"}
	}
	if err := validateCredentials(username, password); err != nil {
		return UserInfo{}, err
	}
	id := "u_" + strings.ToLower(rand.Text())
	hash := hashPassword(password, s.params)
	var out UserInfo
	err := s.st.WriteTx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE username = ?`, username).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return ErrUserExists
		}
		now := s.now().UnixMilli()
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO users (id, username, password_hash, role, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
			id, username, hash, role, now, now); err != nil {
			return err
		}
		var err error
		out, err = s.getUser(ctx, tx, id)
		return err
	})
	if err != nil {
		return UserInfo{}, err
	}
	s.log.Info("user created", "user", username, "role", role)
	return out, nil
}

// UpdateUser changes an account's role and whether it's disabled; nil leaves
// a field alone. The owner's role and status can't change here, and nobody
// becomes the owner here (TransferOwnership). Disabling ends the user's
// sessions in the same transaction.
func (s *Service) UpdateUser(ctx context.Context, id string, role *string, disabled *bool) (UserInfo, error) {
	if role != nil {
		if *role == RoleOwner {
			return UserInfo{}, errOwnerByTransfer
		}
		if !ValidRole(*role) {
			return UserInfo{}, &ValidationError{"role must be admin, operator, viewer or household"}
		}
	}
	var out UserInfo
	changed := false
	err := s.st.WriteTx(ctx, func(tx *sql.Tx) error {
		changed = false // a retried transaction starts over
		cur, err := s.getUser(ctx, tx, id)
		if err != nil {
			return err
		}
		if cur.Role == RoleOwner && ((role != nil && *role != RoleOwner) || (disabled != nil && *disabled)) {
			return ErrOwnerProtected
		}
		now := s.now().UnixMilli()
		if role != nil && *role != cur.Role {
			changed = true
			if _, err := tx.ExecContext(ctx, `UPDATE users SET role = ?, updated_at = ? WHERE id = ?`, *role, now, id); err != nil {
				return err
			}
		}
		if disabled != nil && *disabled != cur.Disabled {
			changed = changed || *disabled
			if *disabled {
				if _, err := tx.ExecContext(ctx, `UPDATE users SET disabled_at = ?, updated_at = ? WHERE id = ?`, now, now, id); err != nil {
					return err
				}
				if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, id); err != nil {
					return err
				}
			} else if _, err := tx.ExecContext(ctx, `UPDATE users SET disabled_at = NULL, updated_at = ? WHERE id = ?`, now, id); err != nil {
				return err
			}
		}
		out, err = s.getUser(ctx, tx, id)
		return err
	})
	if err != nil {
		return UserInfo{}, err
	}
	s.log.Info("user updated", "user", out.Username, "role", out.Role, "disabled", out.Disabled)
	if changed {
		// What the user's open sockets were allowed no longer holds.
		s.emit(Event{Kind: EventUserChanged, UserID: id})
	}
	return out, nil
}

// TransferOwnership makes toID the owner and fromID, the current owner, an
// admin, in one transaction. The target must be an enabled account.
func (s *Service) TransferOwnership(ctx context.Context, fromID, toID string) (UserInfo, error) {
	if fromID == toID {
		return UserInfo{}, &ValidationError{"you're already the owner"}
	}
	var out UserInfo
	err := s.st.WriteTx(ctx, func(tx *sql.Tx) error {
		from, err := s.getUser(ctx, tx, fromID)
		if errors.Is(err, ErrUserNotFound) || (err == nil && from.Role != RoleOwner) {
			return ErrNotOwner
		}
		if err != nil {
			return err
		}
		to, err := s.getUser(ctx, tx, toID)
		if err != nil {
			return err
		}
		if to.Disabled {
			return &ValidationError{to.Username + " is disabled; enable the account before handing it ownership"}
		}
		now := s.now().UnixMilli()
		// Demote first: the single-owner index never sees two owners.
		if _, err := tx.ExecContext(ctx, `UPDATE users SET role = ?, updated_at = ? WHERE id = ?`, RoleAdmin, now, fromID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE users SET role = ?, updated_at = ? WHERE id = ?`, RoleOwner, now, toID); err != nil {
			return err
		}
		out, err = s.getUser(ctx, tx, toID)
		return err
	})
	if err != nil {
		return UserInfo{}, err
	}
	s.log.Warn("ownership transferred", "from", fromID, "to", out.Username)
	s.emit(Event{Kind: EventUserChanged, UserID: fromID})
	s.emit(Event{Kind: EventUserChanged, UserID: toID})
	return out, nil
}
