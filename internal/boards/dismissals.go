package boards

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"unicode/utf8"
)

// Discovery suggestions the owner dismissed on a board (the web app's label
// discovery: "3 services found by their labels. Add them?"), by the resource
// each was about. The hub doesn't make suggestions; it remembers the answer
// so a suggestion isn't offered again, on any device.

// Limits on dismissals.
const (
	// MaxDismissed is how many dismissals a board keeps; the oldest go first.
	MaxDismissed = 1000
	// MaxDismissBatch bounds one request.
	MaxDismissBatch = 200
	// MaxRefLen bounds a resource id.
	MaxRefLen = 512
)

// ErrInvalidRef means a dismissal names no resource, or too long a one.
var ErrInvalidRef = errors.New("invalid resource id")

// Dismissed returns the resource ids dismissed on a board, oldest first.
func (s *Service) Dismissed(ctx context.Context, boardID string) ([]string, error) {
	return dismissed(ctx, s.st.Reader(), boardID)
}

type rowQuerier interface {
	querier
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func dismissed(ctx context.Context, q rowQuerier, boardID string) ([]string, error) {
	var one int
	err := q.QueryRowContext(ctx, `SELECT 1 FROM boards WHERE id = ?`, boardID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, `SELECT ref FROM board_dismissals WHERE board_id = ? ORDER BY dismissed_at, ref`, boardID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	refs := []string{}
	for rows.Next() {
		var ref string
		if err := rows.Scan(&ref); err != nil {
			return nil, err
		}
		refs = append(refs, ref)
	}
	return refs, rows.Err()
}

// Dismiss remembers refs as dismissed on a board and returns every
// dismissal it now has. Dismissing one twice is a no-op; past MaxDismissed
// the oldest are forgotten.
func (s *Service) Dismiss(ctx context.Context, boardID string, refs []string) ([]string, error) {
	if len(refs) == 0 || len(refs) > MaxDismissBatch {
		return nil, fmt.Errorf("%w: dismiss 1–%d resources at a time", ErrInvalidRef, MaxDismissBatch)
	}
	for _, r := range refs {
		if r == "" || len(r) > MaxRefLen || !utf8.ValidString(r) {
			return nil, fmt.Errorf("%w: resource ids are 1–%d bytes of UTF-8", ErrInvalidRef, MaxRefLen)
		}
	}
	var out []string
	err := s.st.WriteTx(ctx, func(tx *sql.Tx) error {
		var one int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM boards WHERE id = ?`, boardID).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		now := s.now()
		for _, r := range refs {
			if _, err := tx.ExecContext(ctx, `INSERT INTO board_dismissals (board_id, ref, dismissed_at) VALUES (?, ?, ?)
				ON CONFLICT (board_id, ref) DO NOTHING`, boardID, r, now); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM board_dismissals WHERE board_id = ? AND ref NOT IN (
				SELECT ref FROM board_dismissals WHERE board_id = ? ORDER BY dismissed_at DESC, ref DESC LIMIT ?)`,
			boardID, boardID, MaxDismissed); err != nil {
			return err
		}
		out, err = dismissed(ctx, tx, boardID)
		return err
	})
	return out, err
}
