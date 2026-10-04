package boards

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"dash.cha.sh/internal/store"
)

// Detail is a board with everything a full export carries: its slug, its
// position and its latest layout with its tiles. A board that was never
// saved has revision 0, a nil document and empty tiles.
type Detail struct {
	ID       string
	Slug     string
	Name     string
	Position int64
	Layout   Layout
}

// Querier reads boards: the read pool, the writer, or a write transaction.
type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

const detailQuery = `SELECT b.id, b.slug, b.title, b.position, l.revision, l.format_version, l.document, l.tiles
	FROM boards b LEFT JOIN layouts l ON l.board_id = b.id
		AND l.revision = (SELECT MAX(revision) FROM layouts WHERE board_id = b.id)`

func scanDetail(sc interface{ Scan(...any) error }) (Detail, error) {
	var (
		d          Detail
		rev, fv    sql.NullInt64
		doc, tiles sql.NullString
	)
	if err := sc.Scan(&d.ID, &d.Slug, &d.Name, &d.Position, &rev, &fv, &doc, &tiles); err != nil {
		return Detail{}, err
	}
	d.Layout = Layout{Revision: rev.Int64, FormatVersion: fv.Int64, Tiles: json.RawMessage(EmptyTiles)}
	if doc.Valid {
		d.Layout.Document = json.RawMessage(doc.String)
	}
	if tiles.Valid {
		d.Layout.Tiles = json.RawMessage(tiles.String)
	}
	return d, nil
}

// ReadAll returns every board in display order, each with its latest layout
// and tiles.
func ReadAll(ctx context.Context, q Querier) ([]Detail, error) {
	rows, err := q.QueryContext(ctx, detailQuery+` ORDER BY b.position, b.created_at, b.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Detail{}
	for rows.Next() {
		d, err := scanDetail(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// CreateTx inserts a board row (no layout) in the caller's write transaction.
func CreateTx(ctx context.Context, tx *sql.Tx, d Detail) error {
	now := store.NowMillis()
	_, err := tx.ExecContext(ctx, `INSERT INTO boards (id, slug, title, position, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		d.ID, d.Slug, d.Name, d.Position, now, now)
	if err != nil {
		return fmt.Errorf("boards: create %s: %w", d.ID, err)
	}
	return nil
}

// UpdateTx sets a board's name, slug and position in the caller's write
// transaction. Two boards trading slugs need a placeholder slug first
// (UNIQUE (slug) holds at every statement).
func UpdateTx(ctx context.Context, tx *sql.Tx, d Detail) error {
	res, err := tx.ExecContext(ctx, `UPDATE boards SET title = ?, slug = ?, position = ?, updated_at = ? WHERE id = ?`,
		d.Name, d.Slug, d.Position, store.NowMillis(), d.ID)
	if err != nil {
		return fmt.Errorf("boards: update %s: %w", d.ID, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// AppendLayoutTx stores doc and tiles as the board's next revision in the
// caller's write transaction, without the optimistic check SaveLayout makes
// (the caller compared inside the same transaction), and prunes history to
// KeepRevisions. tiles must pass ParseTiles (the error wraps
// ErrInvalidTiles). It returns the new revision. userID may be empty.
func AppendLayoutTx(ctx context.Context, tx *sql.Tx, boardID string, formatVersion int64, doc, tiles json.RawMessage, userID string) (int64, error) {
	var compact, compactTiles bytes.Buffer
	if err := json.Compact(&compact, doc); err != nil {
		return 0, fmt.Errorf("boards: layout is not JSON: %w", err)
	}
	if _, err := ParseTiles(tiles); err != nil {
		return 0, fmt.Errorf("boards: %w", err)
	}
	if err := json.Compact(&compactTiles, tiles); err != nil {
		return 0, fmt.Errorf("boards: %w: %v", ErrInvalidTiles, err)
	}
	var cur sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT MAX(revision) FROM layouts WHERE board_id = ?`, boardID).Scan(&cur); err != nil {
		return 0, err
	}
	next := cur.Int64 + 1
	now := store.NowMillis()
	createdBy := sql.NullString{String: userID, Valid: userID != ""}
	if _, err := tx.ExecContext(ctx, `INSERT INTO layouts (board_id, revision, format_version, document, tiles, created_at, created_by)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, boardID, next, formatVersion, compact.String(), compactTiles.String(), now, createdBy); err != nil {
		return 0, fmt.Errorf("boards: layout of %s: %w", boardID, err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE boards SET updated_at = ? WHERE id = ?`, now, boardID); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM layouts WHERE board_id = ? AND revision <= ?`, boardID, next-KeepRevisions); err != nil {
		return 0, err
	}
	return next, nil
}
