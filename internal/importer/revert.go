package importer

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"dash.cha.sh/internal/boards"
	"dash.cha.sh/internal/store"
)

// ErrReverted is Revert on an import that was reverted already.
var ErrReverted = errors.New("importer: this import was reverted already")

// RevertItem is one thing Revert removes or keeps.
type RevertItem struct {
	Kind string `json:"kind"` // board, instance, tile
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	// Why says, for kept things, why they stay.
	Why string `json:"why,omitempty"`
}

// RevertPlan is what Revert does, or did.
type RevertPlan struct {
	Remove []RevertItem `json:"remove"`
	Keep   []RevertItem `json:"keep"`
	// Connections reports whether it removes integration instances: the
	// HTTP handler asks for a step-up then.
	Connections bool `json:"connections"`
}

// Revert removes what an import created and nobody has changed since: the
// instances (with their vault secrets), the board it made, or the tiles it
// added to a board. Everything else stays, and the plan says why. dryRun
// only plans. It never touches what the import updated rather than created.
func (s *Service) Revert(ctx context.Context, id string, dryRun bool) (RevertPlan, error) {
	var rp RevertPlan
	run := func(q querier, tx *sql.Tx) error {
		e, err := s.entry(ctx, q, id)
		if err != nil {
			return err
		}
		if e.RevertedAt != 0 {
			return ErrReverted
		}
		rp = RevertPlan{Remove: []RevertItem{}, Keep: []RevertItem{}}

		var gone []string
		for _, inst := range e.Created.Instances {
			var name string
			var updated int64
			err := q.QueryRowContext(ctx, `SELECT name, updated_at FROM integration_instances WHERE id = ?`, inst).Scan(&name, &updated)
			switch {
			case errors.Is(err, sql.ErrNoRows):
				continue // removed already
			case err != nil:
				return err
			case updated > e.CreatedAt:
				rp.Keep = append(rp.Keep, RevertItem{Kind: "instance", ID: inst, Name: name, Why: "changed since the import"})
			default:
				rp.Remove = append(rp.Remove, RevertItem{Kind: "instance", ID: inst, Name: name})
				gone = append(gone, inst)
			}
		}
		rp.Connections = len(gone) > 0

		board, items, err := s.revertBoard(ctx, q, e, &rp)
		if err != nil || tx == nil {
			return err
		}

		for _, inst := range gone {
			if _, err := tx.ExecContext(ctx, `DELETE FROM integration_instances WHERE id = ?`, inst); err != nil {
				return err
			}
		}
		for _, name := range e.Created.Secrets {
			for _, inst := range gone {
				if strings.HasPrefix(name, "instance/"+inst+"/") {
					if _, err := tx.ExecContext(ctx, `DELETE FROM vault_secrets WHERE name = ?`, name); err != nil {
						return err
					}
				}
			}
		}
		now := store.NowMillis()
		switch {
		case board:
			if _, err := tx.ExecContext(ctx, `DELETE FROM boards WHERE id = ?`, e.BoardID); err != nil {
				return err
			}
		case items != nil:
			if err := removeItems(ctx, tx, e.BoardID, items, now); err != nil {
				return err
			}
		}
		done, _ := json.Marshal(rp)
		_, err = tx.ExecContext(ctx, `UPDATE imports SET reverted_at = ?, reverted = ? WHERE id = ?`, now, string(done), id)
		return err
	}
	if dryRun {
		return rp, run(s.st.Reader(), nil)
	}
	return rp, s.st.WriteTx(ctx, func(tx *sql.Tx) error { return run(tx, tx) })
}

// revertBoard plans the board's part: the whole board when the import made
// it and nobody saved a layout since, else the items it added that are
// still there.
func (s *Service) revertBoard(ctx context.Context, q querier, e HistoryEntry, rp *RevertPlan) (bool, []string, error) {
	if e.BoardID == "" {
		return false, nil, nil
	}
	var title string
	err := q.QueryRowContext(ctx, `SELECT title FROM boards WHERE id = ?`, e.BoardID).Scan(&title)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil, nil
	}
	if err != nil {
		return false, nil, err
	}
	var latest int64
	var doc string
	err = q.QueryRowContext(ctx, `SELECT created_at, document FROM layouts WHERE board_id = ? ORDER BY revision DESC LIMIT 1`, e.BoardID).Scan(&latest, &doc)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, nil, err
	}
	if e.Created.Board != "" {
		if latest <= e.CreatedAt {
			rp.Remove = append(rp.Remove, RevertItem{Kind: "board", ID: e.BoardID, Name: title})
			return true, nil, nil
		}
		rp.Keep = append(rp.Keep, RevertItem{Kind: "board", ID: e.BoardID, Name: title, Why: "its layout was edited since the import"})
	}
	var d LayoutDocument
	if doc == "" || json.Unmarshal([]byte(doc), &d) != nil {
		return false, nil, nil
	}
	items := []string{}
	for _, sec := range d.Sections {
		for _, it := range sec.Items {
			if slices.Contains(e.Created.Items, it.ID) {
				items = append(items, it.ID)
				rp.Remove = append(rp.Remove, RevertItem{Kind: "tile", ID: it.ID})
			}
		}
	}
	if len(items) == 0 {
		return false, nil, nil
	}
	return false, items, nil
}

// removeItems writes a new layout revision without items.
func removeItems(ctx context.Context, tx *sql.Tx, boardID string, items []string, now int64) error {
	var rev int64
	var fv int
	var doc, tiles string
	if err := tx.QueryRowContext(ctx, `SELECT revision, format_version, document, tiles FROM layouts WHERE board_id = ? ORDER BY revision DESC LIMIT 1`, boardID).Scan(&rev, &fv, &doc, &tiles); err != nil {
		return err
	}
	var d LayoutDocument
	if err := json.Unmarshal([]byte(doc), &d); err != nil {
		return fmt.Errorf("importer: board %s: layout: %w", boardID, err)
	}
	for i := range d.Sections {
		d.Sections[i].Items = slices.DeleteFunc(d.Sections[i].Items, func(it LayoutItem) bool { return slices.Contains(items, it.ID) })
	}
	if err := d.Pack(); err != nil {
		return err
	}
	var t boards.Tiles
	if tiles != "" {
		if err := json.Unmarshal([]byte(tiles), &t); err != nil {
			return err
		}
	}
	for _, id := range items {
		delete(t, id)
	}
	nd, _ := json.Marshal(d)
	nt, _ := json.Marshal(t)
	if _, err := tx.ExecContext(ctx, `INSERT INTO layouts (board_id, revision, format_version, document, tiles, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		boardID, rev+1, fv, string(nd), string(nt), now); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE boards SET updated_at = ? WHERE id = ?`, now, boardID)
	return err
}
