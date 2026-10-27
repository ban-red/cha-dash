package nodes

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"dash.cha.sh/internal/nodeproto"
	"dash.cha.sh/internal/store"
)

// Node states.
const (
	StatePending  = "pending"
	StateApproved = "approved"
	StateRevoked  = "revoked"
)

// How a node joined.
const (
	ViaLocal = "local" // the compose network's pairing secret
	ViaToken = "token" // a join token and Inbox approval (v0.3)
)

// ApprovedByPairing is approved_by for a node the pairing secret approved.
const ApprovedByPairing = "local-pairing"

// Record is one row of the registry.
type Record struct {
	ID          string               `json:"id"`
	Name        string               `json:"name"`
	PublicKey   string               `json:"public_key"`
	Fingerprint string               `json:"fingerprint"`
	NextKey     string               `json:"next_key,omitempty"`
	State       string               `json:"state"`
	EnrolledVia string               `json:"enrolled_via"`
	ApprovedBy  string               `json:"approved_by,omitempty"`
	ApprovedAt  *time.Time           `json:"approved_at,omitempty"`
	RevokedBy   string               `json:"revoked_by,omitempty"`
	RevokedAt   *time.Time           `json:"revoked_at,omitempty"`
	LastSeenAt  *time.Time           `json:"last_seen_at,omitempty"`
	Address     string               `json:"address,omitempty"`
	Version     string               `json:"version,omitempty"`
	HubPins     []string             `json:"hub_pins"`
	Verbs       []nodeproto.VerbInfo `json:"verbs"`
	CreatedAt   time.Time            `json:"created_at"`
}

// ErrNotFound is an unknown node.
var ErrNotFound = errors.New("nodes: no such node")

// registry is the nodes table.
type registry struct {
	st  *store.Store
	now func() time.Time
}

const recordCols = `id, name, public_key, next_key, state, enrolled_via, approved_by, approved_at, revoked_by, revoked_at,
	last_seen_at, address, version, hub_pins, verbs, created_at`

type scanner interface{ Scan(dest ...any) error }

func scanRecord(s scanner) (Record, error) {
	var r Record
	var next, approvedBy, revokedBy, address, version sql.NullString
	var approvedAt, revokedAt, lastSeen sql.NullInt64
	var pins, verbs string
	var created int64
	err := s.Scan(&r.ID, &r.Name, &r.PublicKey, &next, &r.State, &r.EnrolledVia, &approvedBy, &approvedAt, &revokedBy, &revokedAt,
		&lastSeen, &address, &version, &pins, &verbs, &created)
	if err != nil {
		return r, err
	}
	r.NextKey, r.ApprovedBy, r.RevokedBy, r.Address, r.Version = next.String, approvedBy.String, revokedBy.String, address.String, version.String
	r.ApprovedAt, r.RevokedAt, r.LastSeenAt = msPtr(approvedAt), msPtr(revokedAt), msPtr(lastSeen)
	r.CreatedAt = time.UnixMilli(created).UTC()
	r.Fingerprint = nodeproto.Fingerprint(r.PublicKey)
	_ = json.Unmarshal([]byte(pins), &r.HubPins)
	_ = json.Unmarshal([]byte(verbs), &r.Verbs)
	if r.HubPins == nil {
		r.HubPins = []string{}
	}
	if r.Verbs == nil {
		r.Verbs = []nodeproto.VerbInfo{}
	}
	return r, nil
}

func msPtr(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := time.UnixMilli(v.Int64).UTC()
	return &t
}

// lookup finds a node by its current key, else by its announced next key.
// The current key wins: setNext refuses a next key that's anyone's key, but
// rows written before that rule (or a race) must never let an announced
// key shadow the node that holds it.
func (g *registry) lookup(ctx context.Context, pin string) (Record, bool, error) {
	for _, col := range []string{"public_key", "next_key"} {
		rows, err := g.st.Reader().QueryContext(ctx, `SELECT `+recordCols+` FROM nodes WHERE `+col+` = ? LIMIT 2`, pin)
		if err != nil {
			return Record{}, false, err
		}
		var found []Record
		for rows.Next() {
			r, err := scanRecord(rows)
			if err != nil {
				rows.Close()
				return Record{}, false, err
			}
			found = append(found, r)
		}
		err = rows.Err()
		rows.Close()
		switch {
		case err != nil:
			return Record{}, false, err
		case len(found) > 1:
			return Record{}, false, fmt.Errorf("nodes: %d nodes announce the same next key", len(found))
		case len(found) == 1:
			return found[0], true, nil
		}
	}
	return Record{}, false, nil
}

func (g *registry) get(ctx context.Context, id string) (Record, error) {
	r, err := scanRecord(g.st.Reader().QueryRowContext(ctx, `SELECT `+recordCols+` FROM nodes WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, ErrNotFound
	}
	return r, err
}

// byName finds the live (not revoked) node with a name.
func (g *registry) byName(ctx context.Context, name string) (Record, error) {
	r, err := scanRecord(g.st.Reader().QueryRowContext(ctx,
		`SELECT `+recordCols+` FROM nodes WHERE name = ? AND state <> 'revoked'`, name))
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, ErrNotFound
	}
	return r, err
}

func (g *registry) list(ctx context.Context) ([]Record, error) {
	rows, err := g.st.Reader().QueryContext(ctx, `SELECT `+recordCols+` FROM nodes ORDER BY state = 'revoked', name, created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Record{}
	for rows.Next() {
		r, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func newNodeID() string { return "n_" + strings.ToLower(rand.Text()) }

// errNameTaken is an enrollment under the name of a live node.
var errNameTaken = errors.New("nodes: a live node has that name")

// enrollApproved adds a node that the pairing secret approved. A live (not
// revoked) node with the same name refuses it with errNameTaken: pairing
// never replaces a node, an admin revokes the old one first.
func (g *registry) enrollApproved(ctx context.Context, name, pin, via, by string) (Record, error) {
	now := g.now().UnixMilli()
	id := newNodeID()
	err := g.st.WriteTx(ctx, func(tx *sql.Tx) error {
		var live string
		err := tx.QueryRowContext(ctx, `SELECT id FROM nodes WHERE name = ? AND state <> 'revoked'`, name).Scan(&live)
		switch {
		case err == nil:
			return errNameTaken
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO nodes (id, name, public_key, state, enrolled_via, approved_by, approved_at, created_at, updated_at)
			VALUES (?, ?, ?, 'approved', ?, ?, ?, ?, ?)`, id, name, pin, via, by, now, now, now)
		return err
	})
	if errors.Is(err, errNameTaken) {
		return Record{}, err
	}
	if err != nil {
		return Record{}, fmt.Errorf("nodes: enrolling: %w", err)
	}
	return g.get(ctx, id)
}

// seen records a connection: when, from where, which build, which hub keys
// it trusts and which verbs it serves. A node that connected with its next
// key has finished rotating: the next key becomes its key.
func (g *registry) seen(ctx context.Context, id, pin, address, version string, hubPins []string, verbs []nodeproto.VerbInfo) (rotated bool, err error) {
	now := g.now().UnixMilli()
	p, _ := json.Marshal(nonNil(hubPins))
	v, _ := json.Marshal(verbs)
	if verbs == nil {
		v = []byte("[]")
	}
	err = g.st.WriteTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE nodes SET public_key = next_key, next_key = NULL, updated_at = ? WHERE id = ? AND next_key = ?`, now, id, pin)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		rotated = n == 1
		_, err = tx.ExecContext(ctx, `UPDATE nodes SET last_seen_at = ?, address = ?, version = ?, hub_pins = ?, verbs = ?, updated_at = ? WHERE id = ?`,
			now, address, version, string(p), string(v), now, id)
		return err
	})
	return rotated, err
}

// errKeyInUse is a next key that's already some node's key or next key.
var errKeyInUse = errors.New("nodes: that key belongs to a node already")

// setNext records a node's next key. It must be a key no node holds or has
// announced, its own included: otherwise a node could park another's key
// as its next one and be found by it.
func (g *registry) setNext(ctx context.Context, id, pin string) error {
	now := g.now().UnixMilli()
	return g.st.WriteTx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM nodes WHERE public_key = ? OR next_key = ?)
			+ (SELECT count(*) FROM node_hub_keys WHERE pin = ?)`, pin, pin, pin).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return errKeyInUse
		}
		res, err := tx.ExecContext(ctx, `UPDATE nodes SET next_key = ?, updated_at = ? WHERE id = ? AND state = 'approved'`, pin, now, id)
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE") {
				return errKeyInUse
			}
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// addHubPin records that a node took a hub key.
func (g *registry) addHubPin(ctx context.Context, id, pin string) error {
	r, err := g.get(ctx, id)
	if err != nil {
		return err
	}
	if slices.Contains(r.HubPins, pin) {
		return nil
	}
	b, _ := json.Marshal(append(r.HubPins, pin))
	_, err = g.st.Writer().ExecContext(ctx, `UPDATE nodes SET hub_pins = ?, updated_at = ? WHERE id = ?`, string(b), g.now().UnixMilli(), id)
	return err
}

func (g *registry) revoke(ctx context.Context, id, by string) (Record, error) {
	now := g.now().UnixMilli()
	res, err := g.st.Writer().ExecContext(ctx, `UPDATE nodes SET state = 'revoked', revoked_by = ?, revoked_at = ?, next_key = NULL, updated_at = ?
		WHERE id = ? AND state <> 'revoked'`, by, now, now, id)
	if err != nil {
		return Record{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, err := g.get(ctx, id); err != nil {
			return Record{}, err
		}
		return Record{}, errAlreadyRevoked
	}
	return g.get(ctx, id)
}

var errAlreadyRevoked = errors.New("nodes: already revoked")

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
