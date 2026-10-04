package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"dash.cha.sh/internal/store"
)

// row is one integration_instances row. Config is the stored JSON: the
// non-secret config plus a ${secret:<vault name>} reference in each secret
// field that's set.
type row struct {
	ID          string
	Integration string
	Name        string
	Config      map[string]any
	Enabled     bool
	CreatedAt   int64 // Unix ms
	UpdatedAt   int64
}

const rowColumns = `id, integration, name, config, enabled, created_at, updated_at`

func scanRow(sc interface{ Scan(...any) error }) (row, error) {
	var r row
	var cfg string
	var enabled int64
	if err := sc.Scan(&r.ID, &r.Integration, &r.Name, &cfg, &enabled, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return row{}, err
	}
	r.Enabled = enabled != 0
	if err := json.Unmarshal([]byte(cfg), &r.Config); err != nil {
		return row{}, fmt.Errorf("instance %s: config isn't JSON: %w", r.ID, err)
	}
	if r.Config == nil {
		r.Config = map[string]any{}
	}
	return r, nil
}

func loadRows(ctx context.Context, db *sql.DB) ([]row, error) {
	rows, err := db.QueryContext(ctx, `SELECT `+rowColumns+` FROM integration_instances ORDER BY integration, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []row
	for rows.Next() {
		r, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// errNameTaken means another instance of the integration has the name.
var errNameTaken = errors.New("runtime: name taken")

// errNotFound means the instance doesn't exist.
var errNotFound = errors.New("runtime: no such instance")

// saveRow inserts or updates r. The name check and the write share one
// transaction on the single writer, so two requests can't both take a name.
func saveRow(ctx context.Context, st *store.Store, r row, insert bool) error {
	cfg, err := json.Marshal(r.Config)
	if err != nil {
		return err
	}
	return st.WriteTx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM integration_instances WHERE integration = ? AND name = ? AND id <> ?`,
			r.Integration, r.Name, r.ID).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return errNameTaken
		}
		if insert {
			_, err := tx.ExecContext(ctx, `INSERT INTO integration_instances (`+rowColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?)`,
				r.ID, r.Integration, r.Name, string(cfg), boolInt(r.Enabled), r.CreatedAt, r.UpdatedAt)
			return err
		}
		res, err := tx.ExecContext(ctx, `UPDATE integration_instances SET name = ?, config = ?, enabled = ?, updated_at = ? WHERE id = ?`,
			r.Name, string(cfg), boolInt(r.Enabled), r.UpdatedAt, r.ID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return errNotFound
		}
		return nil
	})
}

func deleteRow(ctx context.Context, st *store.Store, id string) error {
	res, err := st.Writer().ExecContext(ctx, `DELETE FROM integration_instances WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errNotFound
	}
	return nil
}

func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
