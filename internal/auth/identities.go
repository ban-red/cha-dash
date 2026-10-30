package auth

// Linked identities: OIDC (issuer, subject) pairs and forward-auth
// usernames, each linked to one Cha Dash user. Users are matched by these
// only, never by email. Who may list or unlink whose is internal/authz's
// call; this file keeps the invariant that unlinking never leaves an
// account without a way to sign in.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ForwardAuthIssuer stands in for the issuer of forward-auth links in the
// identities API: the subject is the username the proxy sends.
const ForwardAuthIssuer = "forward-auth"

var (
	ErrIdentityNotFound = errors.New("auth: no such linked identity")
	// ErrIdentityTaken: the identity is linked to another account.
	ErrIdentityTaken = errors.New("auth: identity linked to another account")
)

// LinkedIdentity is one way into an account through someone else.
type LinkedIdentity struct {
	// Kind is oidc or forward_auth.
	Kind       string     `json:"kind"`
	Issuer     string     `json:"issuer"`
	Subject    string     `json:"subject"`
	Email      string     `json:"email,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
}

// ListIdentities returns userID's linked identities, oldest first. Never nil.
func (s *Service) ListIdentities(ctx context.Context, userID string) ([]LinkedIdentity, error) {
	rows, err := s.st.Reader().QueryContext(ctx, `
		SELECT 'oidc', issuer, subject, email, created_at, last_used_at FROM oidc_identities WHERE user_id = ?
		UNION ALL
		SELECT 'forward_auth', '`+ForwardAuthIssuer+`', username, '', created_at, NULL FROM forward_auth_links WHERE user_id = ?
		ORDER BY 5, 3`, userID, userID)
	if err != nil {
		return nil, fmt.Errorf("auth: %w", err)
	}
	defer rows.Close()
	out := []LinkedIdentity{}
	for rows.Next() {
		var li LinkedIdentity
		var created int64
		var used sql.NullInt64
		if err := rows.Scan(&li.Kind, &li.Issuer, &li.Subject, &li.Email, &created, &used); err != nil {
			return nil, fmt.Errorf("auth: %w", err)
		}
		li.Kind = map[string]string{"oidc": MethodOIDC, "forward_auth": MethodForwardAuth}[li.Kind]
		li.CreatedAt = time.UnixMilli(created).UTC()
		if used.Valid {
			t := time.UnixMilli(used.Int64).UTC()
			li.LastUsedAt = &t
		}
		out = append(out, li)
	}
	return out, rows.Err()
}

// UnlinkIdentity removes one of userID's identities, unless it's the last
// way the account can sign in: no password, no passkey, nothing else linked.
func (s *Service) UnlinkIdentity(ctx context.Context, userID, issuer, subject string) error {
	return s.st.WriteTx(ctx, func(tx *sql.Tx) error {
		var mine, others int
		var hasPassword, hasPasskey bool
		fwd := issuer == ForwardAuthIssuer
		if err := tx.QueryRowContext(ctx, `SELECT
			(SELECT count(*) FROM oidc_identities WHERE user_id = ? AND issuer = ? AND subject = ?) +
				(SELECT count(*) FROM forward_auth_links WHERE user_id = ? AND username = ? AND ?),
			(SELECT count(*) FROM oidc_identities WHERE user_id = ?) + (SELECT count(*) FROM forward_auth_links WHERE user_id = ?),
			coalesce((SELECT password_hash IS NOT NULL FROM users WHERE id = ?), 0),
			EXISTS (SELECT 1 FROM passkeys WHERE user_id = ?)`,
			userID, issuer, subject, userID, subject, fwd, userID, userID, userID, userID).Scan(&mine, &others, &hasPassword, &hasPasskey); err != nil {
			return err
		}
		if mine == 0 {
			return ErrIdentityNotFound
		}
		if !hasPassword && !hasPasskey && others <= 1 {
			return errLastSignInMethod
		}
		var err error
		if fwd {
			_, err = tx.ExecContext(ctx, `DELETE FROM forward_auth_links WHERE user_id = ? AND username = ?`, userID, subject)
		} else {
			_, err = tx.ExecContext(ctx, `DELETE FROM oidc_identities WHERE user_id = ? AND issuer = ? AND subject = ?`, userID, issuer, subject)
		}
		return err
	})
}

// IsLastSignInMethod reports whether err refused removing the account's
// last way in.
func IsLastSignInMethod(err error) bool { return errors.Is(err, errLastSignInMethod) }

// LinkForwardAuth links a forward-auth username to userID. A username
// linked to someone else is ErrIdentityTaken; to userID already, nothing.
func (s *Service) LinkForwardAuth(ctx context.Context, userID, username string) (bool, error) {
	username = strings.TrimSpace(username)
	if username == "" || len(username) > 255 {
		return false, &ValidationError{"the proxy sent no usable username"}
	}
	created := false
	err := s.st.WriteTx(ctx, func(tx *sql.Tx) error {
		created = false
		var owner string
		err := tx.QueryRowContext(ctx, `SELECT user_id FROM forward_auth_links WHERE username = ?`, username).Scan(&owner)
		switch {
		case err == nil && owner == userID:
			return nil
		case err == nil:
			return ErrIdentityTaken
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO forward_auth_links (username, user_id, created_at) VALUES (?, ?, ?)`, username, userID, s.now().UnixMilli())
		created = err == nil
		return err
	})
	return created, err
}

// ForwardAuthOwner returns the ID of the user username is linked to, or "".
func (s *Service) ForwardAuthOwner(ctx context.Context, username string) (string, error) {
	var id string
	err := s.st.Reader().QueryRowContext(ctx, `SELECT user_id FROM forward_auth_links WHERE username = ?`, username).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// roleFromGroups reports whether u's role comes from groups under set: a
// group mapping exists, the account isn't the owner's, and it has an
// identity on a method that's on.
func roleFromGroups(set SignInSettings, role string, oidc, forward bool) bool {
	if len(set.Mappings) == 0 || role == RoleOwner {
		return false
	}
	return (oidc && set.OIDC.Enabled) || (forward && set.ForwardAuth.Enabled)
}
