// Package boards stores boards, their layouts and their tile configuration.
//
// A layout is the @chadash/layout document (packages/layout), opaque to the
// hub: it's stored as JSON next to the document format version the SPA
// declares. Each revision also carries the board's tiles (tiles.go): the
// per-item configuration the document can't hold, in a shape the hub owns and
// validates. Each save appends a revision, and a save must name the revision
// it was based on, so two tabs editing the same board can't overwrite each
// other silently: the second gets a conflict and the current revision.
//
// The package also remembers which discovery suggestions were dismissed on a
// board (dismissals.go).
package boards

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"dash.cha.sh/internal/authz"
	"dash.cha.sh/internal/store"
)

// KeepRevisions is how many saved revisions of each board's layout are kept.
// Older ones are pruned on save, so autosave can't grow the database
// without bound; the newest are enough for history and undo across restarts.
const KeepRevisions = 100

// Default board, created the first time boards are listed.
const (
	defaultSlug = "home"
	defaultName = "Home"
)

var (
	// ErrNotFound means the board doesn't exist.
	ErrNotFound = errors.New("boards: no such board")
)

// ConflictError is returned by SaveLayout when the base revision is stale.
type ConflictError struct{ Current int64 }

func (e *ConflictError) Error() string {
	return fmt.Sprintf("boards: layout changed since it was loaded (now at revision %d)", e.Current)
}

// Board is a board as the API lists it.
type Board struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Layout is a board's latest saved layout and tiles. A board that was never
// saved has revision 0, format version 0, a nil document and empty tiles.
type Layout struct {
	Revision      int64
	FormatVersion int64
	Document      json.RawMessage
	// Tiles is the revision's tiles document, never nil ({} when empty).
	Tiles json.RawMessage
}

// EmptyTiles is the tiles document of a board with no tile configuration.
const EmptyTiles = "{}"

// Service reads and writes boards.
type Service struct {
	st  *store.Store
	now func() int64 // Unix milliseconds
	// Audit records layout saves and discovery dismissals made through the
	// API. Nil records nothing.
	Audit *authz.Recorder
}

// New returns a Service over st.
func New(st *store.Store) *Service { return &Service{st: st, now: store.NowMillis} }

// List returns every board in display order. With none yet, it creates the
// default "Home" board first.
func (s *Service) List(ctx context.Context) ([]Board, error) {
	boards, err := list(ctx, s.st.Reader())
	if err != nil || len(boards) > 0 {
		return boards, err
	}
	// The write transaction serialises concurrent first calls, and the
	// NOT EXISTS makes the second one a no-op.
	err = s.st.WriteTx(ctx, func(tx *sql.Tx) error {
		now := s.now()
		if _, err := tx.ExecContext(ctx, `INSERT INTO boards (id, slug, title, position, created_at, updated_at)
			SELECT ?, ?, ?, 0, ?, ? WHERE NOT EXISTS (SELECT 1 FROM boards)`,
			newID(), defaultSlug, defaultName, now, now); err != nil {
			return err
		}
		boards, err = list(ctx, tx)
		return err
	})
	return boards, err
}

type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func list(ctx context.Context, q querier) ([]Board, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, title FROM boards ORDER BY position, created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	boards := []Board{}
	for rows.Next() {
		var b Board
		if err := rows.Scan(&b.ID, &b.Name); err != nil {
			return nil, err
		}
		boards = append(boards, b)
	}
	return boards, rows.Err()
}

// Layout returns a board's latest layout and tiles.
func (s *Service) Layout(ctx context.Context, boardID string) (Layout, error) {
	var (
		rev, fv    sql.NullInt64
		doc, tiles sql.NullString
	)
	err := s.st.Reader().QueryRowContext(ctx, `SELECT l.revision, l.format_version, l.document, l.tiles
		FROM boards b LEFT JOIN layouts l ON l.board_id = b.id
			AND l.revision = (SELECT MAX(revision) FROM layouts WHERE board_id = b.id)
		WHERE b.id = ?`, boardID).Scan(&rev, &fv, &doc, &tiles)
	if errors.Is(err, sql.ErrNoRows) {
		return Layout{}, ErrNotFound
	}
	if err != nil {
		return Layout{}, err
	}
	l := Layout{Revision: rev.Int64, FormatVersion: fv.Int64, Tiles: json.RawMessage(EmptyTiles)}
	if doc.Valid {
		l.Document = json.RawMessage(doc.String)
	}
	if tiles.Valid {
		l.Tiles = json.RawMessage(tiles.String)
	}
	return l, nil
}

// SaveLayout stores doc and tiles as the board's next revision if base is
// still the latest one, and returns the new revision. With nil tiles the
// latest revision's tiles carry over; otherwise they must pass ParseTiles
// (the error wraps ErrInvalidTiles if they don't). A stale base gets a
// *ConflictError carrying the current revision. userID may be empty.
func (s *Service) SaveLayout(ctx context.Context, boardID string, base, formatVersion int64, doc, tiles json.RawMessage, userID string) (int64, error) {
	var compact, compactTiles bytes.Buffer
	if err := json.Compact(&compact, doc); err != nil {
		return 0, fmt.Errorf("boards: layout is not JSON: %w", err)
	}
	if tiles != nil {
		if _, err := ParseTiles(tiles); err != nil {
			return 0, fmt.Errorf("boards: %w", err)
		}
		if err := json.Compact(&compactTiles, tiles); err != nil {
			return 0, fmt.Errorf("boards: %w: %v", ErrInvalidTiles, err)
		}
	}
	var next int64
	err := s.st.WriteTx(ctx, func(tx *sql.Tx) error {
		var cur sql.NullInt64
		var curTiles sql.NullString
		err := tx.QueryRowContext(ctx, `SELECT l.revision, l.tiles
			FROM boards b LEFT JOIN layouts l ON l.board_id = b.id
				AND l.revision = (SELECT MAX(revision) FROM layouts WHERE board_id = b.id)
			WHERE b.id = ?`, boardID).Scan(&cur, &curTiles)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if cur.Int64 != base {
			return &ConflictError{Current: cur.Int64}
		}
		saved := compactTiles.String()
		if tiles == nil {
			saved = EmptyTiles
			if curTiles.Valid {
				saved = curTiles.String
			}
		}
		next = base + 1
		now := s.now()
		createdBy := sql.NullString{String: userID, Valid: userID != ""}
		if _, err := tx.ExecContext(ctx, `INSERT INTO layouts (board_id, revision, format_version, document, tiles, created_at, created_by)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, boardID, next, formatVersion, compact.String(), saved, now, createdBy); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE boards SET updated_at = ? WHERE id = ?`, now, boardID); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM layouts WHERE board_id = ? AND revision <= ?`, boardID, next-KeepRevisions)
		return err
	})
	if err != nil {
		return 0, err
	}
	return next, nil
}

func newID() string { return "b_" + strings.ToLower(rand.Text()) }
