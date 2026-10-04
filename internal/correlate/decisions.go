package correlate

import (
	"context"
	"crypto/rand"
	"strings"
	"time"

	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/store"
)

// decisions persist in the correlation_decisions table (migration 00005).
// Without a store they live in memory, for tests.
type decisions struct{ st *store.Store }

func newDecisionID() string { return "cd_" + strings.ToLower(rand.Text()) }

func (s decisions) load(ctx context.Context) ([]Decision, error) {
	if s.st == nil {
		return nil, nil
	}
	rows, err := s.st.Reader().QueryContext(ctx, `SELECT id, from_id, rel, to_id, verdict, rule, created_at FROM correlation_decisions ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Decision
	for rows.Next() {
		var d Decision
		var from, rel, to, verdict string
		var at int64
		if err := rows.Scan(&d.ID, &from, &rel, &to, &verdict, &d.Rule, &at); err != nil {
			return nil, err
		}
		d.From, d.Type, d.To, d.Verdict = graph.RID(from), graph.RelType(rel), graph.RID(to), Verdict(verdict)
		d.CreatedAt = time.UnixMilli(at).UTC()
		out = append(out, d)
	}
	return out, rows.Err()
}

// save stores d, replacing any decision on the same pair, and returns it
// with the ID it's stored under. fromInst and toInst are the instances
// publishing the two resources ("" when they aren't instances); userID is
// who decided.
func (s decisions) save(ctx context.Context, d Decision, fromInst, toInst, userID string) (Decision, error) {
	if s.st == nil {
		return d, nil
	}
	err := s.st.Writer().QueryRowContext(ctx, `INSERT INTO correlation_decisions
			(id, from_id, rel, to_id, verdict, rule, from_instance, to_instance, created_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?,
			(SELECT id FROM integration_instances WHERE id = ?), (SELECT id FROM integration_instances WHERE id = ?),
			(SELECT id FROM users WHERE id = ?), ?)
		ON CONFLICT (from_id, rel, to_id) DO UPDATE SET
			verdict = excluded.verdict, rule = excluded.rule, from_instance = excluded.from_instance,
			to_instance = excluded.to_instance, created_by = excluded.created_by, created_at = excluded.created_at
		RETURNING id`,
		d.ID, string(d.From), string(d.Type), string(d.To), string(d.Verdict), d.Rule, fromInst, toInst, userID, d.CreatedAt.UnixMilli(),
	).Scan(&d.ID)
	return d, err
}

// remove deletes a decision; false when there was none.
func (s decisions) remove(ctx context.Context, id string) (bool, error) {
	if s.st == nil {
		return true, nil
	}
	res, err := s.st.Writer().ExecContext(ctx, `DELETE FROM correlation_decisions WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}
