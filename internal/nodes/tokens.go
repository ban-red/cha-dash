package nodes

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"dash.cha.sh/internal/store"
)

// Join tokens (v0.3): how a node on another host joins. An admin makes one
// in Settings → Nodes; the node presents its secret once, over a channel
// where it already checked the hub's key, and is enrolled as pending until
// an admin approves it. A token is single-use and short-lived: it's spent at
// enrollment, whatever the approval says. Only the secret's SHA-256 is
// kept, and secrets are looked up by it, so there's nothing to compare in
// constant time.

// Token lifetimes.
const (
	DefaultTokenTTL = 15 * time.Minute
	MaxTokenTTL     = 24 * time.Hour
	// tokenKeep is how long a spent, expired or revoked token stays listed.
	tokenKeep = 7 * 24 * time.Hour
)

// Token states, as the list shows them.
const (
	TokenActive  = "active"
	TokenUsed    = "used"
	TokenExpired = "expired"
	TokenRevoked = "revoked"
)

// JoinToken is a join token as the API lists it: never its secret.
type JoinToken struct {
	ID        string `json:"id"`
	Name      string `json:"name"` // "": the node picks its name
	AddDocker bool   `json:"add_docker"`
	// CreatedBy is the user's ID, CreatedByName their username (empty when
	// the account is gone).
	CreatedBy     string     `json:"created_by,omitempty"`
	CreatedByName string     `json:"created_by_name,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	ExpiresAt     time.Time  `json:"expires_at"`
	UsedAt        *time.Time `json:"used_at,omitempty"`
	RevokedAt     *time.Time `json:"revoked_at,omitempty"`
	NodeID        string     `json:"node_id,omitempty"`
	NodeName      string     `json:"node_name,omitempty"`
	State         string     `json:"state"`
}

// Join token errors.
var (
	ErrTokenNotFound = errors.New("nodes: no such join token")
	// ErrTokenSpent is revoking a token that's used, expired or revoked.
	ErrTokenSpent = errors.New("nodes: the join token is already used, expired or revoked")
	// errBadToken is a presented secret that isn't a usable token.
	errBadToken = errors.New("nodes: the join token isn't valid")
	// errTokenName is a node whose name isn't the one its token names.
	errTokenName = errors.New("nodes: the join token is for another name")
)

// JoinTokens is the node_join_tokens table.
type JoinTokens struct {
	st  *store.Store
	now func() time.Time
}

func hashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// TTL turns minutes asked for into a token's lifetime: the default for 0,
// at most MaxTokenTTL.
func TTL(minutes int) (time.Duration, error) {
	switch {
	case minutes == 0:
		return DefaultTokenTTL, nil
	case minutes < 0 || time.Duration(minutes)*time.Minute > MaxTokenTTL:
		return 0, fmt.Errorf("a join token lives 1 minute to %d hours", int(MaxTokenTTL.Hours()))
	}
	return time.Duration(minutes) * time.Minute, nil
}

// validTokenName checks a token's name: empty, or a name a node may have.
func validTokenName(name string) error {
	switch {
	case name == "":
		return nil
	case !nameRE.MatchString(name):
		return errors.New("a node name is 1–63 letters, digits, dots, dashes or underscores, starting with a letter or digit")
	case reservedName(name):
		return fmt.Errorf("a node can't be called %q: the hub uses that name", name)
	}
	return nil
}

// Create makes a token for a node called name ("" lets the node pick),
// made by the user with ID by. It returns the token and its secret, which
// is never stored and never shown again.
func (j *JoinTokens) Create(ctx context.Context, by, name string, ttl time.Duration, addDocker bool) (JoinToken, string, error) {
	if err := validTokenName(name); err != nil {
		return JoinToken{}, "", err
	}
	if ttl <= 0 || ttl > MaxTokenTTL {
		return JoinToken{}, "", fmt.Errorf("a join token lives at most %d hours", int(MaxTokenTTL.Hours()))
	}
	secret, err := newSecret()
	if err != nil {
		return JoinToken{}, "", err
	}
	now := j.now()
	id := "njt_" + strings.ToLower(rand.Text())
	var creator any
	if by != "" {
		creator = by
	}
	_, err = j.st.Writer().ExecContext(ctx, `INSERT INTO node_join_tokens (id, hash, name, add_docker, created_by, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, id, hashSecret(secret), name, boolInt(addDocker), creator, now.UnixMilli(), now.Add(ttl).UnixMilli())
	if err != nil {
		return JoinToken{}, "", fmt.Errorf("nodes: saving the join token: %w", err)
	}
	t, err := j.get(ctx, id)
	return t, secret, err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

const tokenCols = `t.id, t.name, t.add_docker, t.created_by, u.username, t.created_at, t.expires_at, t.used_at, t.revoked_at, t.node_id, n.name
	FROM node_join_tokens t LEFT JOIN users u ON u.id = t.created_by LEFT JOIN nodes n ON n.id = t.node_id`

func (j *JoinTokens) scan(s scanner) (JoinToken, error) {
	var t JoinToken
	var addDocker int
	var by, byName, nodeID, nodeName sql.NullString
	var created, expires, used, revoked int64
	if err := s.Scan(&t.ID, &t.Name, &addDocker, &by, &byName, &created, &expires, &used, &revoked, &nodeID, &nodeName); err != nil {
		return t, err
	}
	t.AddDocker = addDocker == 1
	t.CreatedBy, t.CreatedByName, t.NodeID, t.NodeName = by.String, byName.String, nodeID.String, nodeName.String
	t.CreatedAt, t.ExpiresAt = time.UnixMilli(created).UTC(), time.UnixMilli(expires).UTC()
	if used > 0 {
		u := time.UnixMilli(used).UTC()
		t.UsedAt = &u
	}
	if revoked > 0 {
		r := time.UnixMilli(revoked).UTC()
		t.RevokedAt = &r
	}
	switch {
	case t.UsedAt != nil:
		t.State = TokenUsed
	case t.RevokedAt != nil:
		t.State = TokenRevoked
	case !t.ExpiresAt.After(j.now()):
		t.State = TokenExpired
	default:
		t.State = TokenActive
	}
	return t, nil
}

func (j *JoinTokens) get(ctx context.Context, id string) (JoinToken, error) {
	t, err := j.scan(j.st.Reader().QueryRowContext(ctx, `SELECT `+tokenCols+` WHERE t.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return JoinToken{}, ErrTokenNotFound
	}
	return t, err
}

// forNode is the token a node enrolled with, if it did.
func (j *JoinTokens) forNode(ctx context.Context, nodeID string) (JoinToken, bool, error) {
	t, err := j.scan(j.st.Reader().QueryRowContext(ctx, `SELECT `+tokenCols+` WHERE t.node_id = ?`, nodeID))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return JoinToken{}, false, nil
	case err != nil:
		return JoinToken{}, false, err
	}
	return t, true, nil
}

// List returns the unspent tokens and those spent, expired or revoked in
// the last 7 days, newest first. Older ones are deleted first.
func (j *JoinTokens) List(ctx context.Context) ([]JoinToken, error) {
	if err := j.prune(ctx); err != nil {
		return nil, err
	}
	rows, err := j.st.Reader().QueryContext(ctx, `SELECT `+tokenCols+` ORDER BY t.created_at DESC, t.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []JoinToken{}
	for rows.Next() {
		t, err := j.scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// prune deletes tokens spent, revoked or expired more than 7 days ago.
func (j *JoinTokens) prune(ctx context.Context) error {
	cut := j.now().Add(-tokenKeep).UnixMilli()
	_, err := j.st.Writer().ExecContext(ctx, `DELETE FROM node_join_tokens
		WHERE (used_at > 0 AND used_at < ?) OR (revoked_at > 0 AND revoked_at < ?) OR (used_at = 0 AND expires_at < ?)`, cut, cut, cut)
	return err
}

// Revoke stops an unspent token working. A token that's used, expired or
// revoked already is ErrTokenSpent.
func (j *JoinTokens) Revoke(ctx context.Context, id string) (JoinToken, error) {
	now := j.now().UnixMilli()
	res, err := j.st.Writer().ExecContext(ctx, `UPDATE node_join_tokens SET revoked_at = ?
		WHERE id = ? AND used_at = 0 AND revoked_at = 0 AND expires_at > ?`, now, id, now)
	if err != nil {
		return JoinToken{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, err := j.get(ctx, id); err != nil {
			return JoinToken{}, err
		}
		return JoinToken{}, ErrTokenSpent
	}
	return j.get(ctx, id)
}

// check finds the usable token a secret belongs to, and checks the node's
// name against it, without spending it: the handshake's answer to a bad
// token, before it touches the registry. redeem decides for real.
func (j *JoinTokens) check(ctx context.Context, secret, name string) (JoinToken, error) {
	t, err := j.scan(j.st.Reader().QueryRowContext(ctx, `SELECT `+tokenCols+` WHERE t.hash = ?`, hashSecret(secret)))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return JoinToken{}, errBadToken
	case err != nil:
		return JoinToken{}, err
	case t.State != TokenActive:
		return JoinToken{}, errBadToken
	case t.Name != "" && t.Name != name:
		return t, errTokenName
	}
	return t, nil
}

// redeem spends the token a secret belongs to on the node nodeID, inside
// the transaction that inserts that node: no usable row, no enrollment.
// Two nodes racing with one token can't both get in; the guarded UPDATE
// lets one through.
func (j *JoinTokens) redeem(ctx context.Context, tx *sql.Tx, secret, nodeID, name string) error {
	now := j.now().UnixMilli()
	res, err := tx.ExecContext(ctx, `UPDATE node_join_tokens SET used_at = ?, node_id = ?
		WHERE hash = ? AND used_at = 0 AND revoked_at = 0 AND expires_at > ? AND (name = '' OR name = ?)`,
		now, nodeID, hashSecret(secret), now, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errBadToken
	}
	return nil
}
