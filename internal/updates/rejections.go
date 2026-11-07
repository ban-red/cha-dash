package updates

import (
	"context"
	crand "crypto/rand"
	"errors"
	"fmt"
	"strings"
	"time"

	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/integration"
)

// Rejected digests (update_rejections, migration 00026): a container
// update that failed its health gate rolled back on its own, but the
// registry still offers the same image, so the next Update all would try
// it again. The Update Center remembers the digest it rolled back from,
// per engine and container name like a hold. While the newest digest is
// still that one, the row reads rolled back, and no rollout takes it (it's
// listed as skipped, with the reason). A newer digest ends it, and so does
// Try again (DELETE /api/updates/{resource}/rejected).

// StateRolledBack is a row whose update rolled back from the digest the
// registry still offers: Row.Rejected says which and why.
const StateRolledBack = "rolled_back"

// Rejection is the digest a container's update rolled back from.
type Rejection struct {
	Digest    string    `json:"digest"`
	Reason    string    `json:"reason,omitempty"`
	Rollout   string    `json:"rollout,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// active says whether a rejection applies to a row whose newest digest is
// latest: only while the registry offers the same image.
func (r Rejection) active(latest string) bool {
	return latest != "" && latest == r.Digest
}

func (s *Service) rejections(ctx context.Context) (map[string]Rejection, error) {
	rows, err := s.st.Reader().QueryContext(ctx, `SELECT engine, container, digest, reason, rollout, created_at FROM update_rejections`)
	if err != nil {
		return nil, fmt.Errorf("updates: read rejections: %w", err)
	}
	defer rows.Close()
	out := map[string]Rejection{}
	for rows.Next() {
		var r Rejection
		var engine, name string
		var at int64
		if err := rows.Scan(&engine, &name, &r.Digest, &r.Reason, &r.Rollout, &at); err != nil {
			return nil, fmt.Errorf("updates: read rejections: %w", err)
		}
		r.CreatedAt = time.UnixMilli(at).UTC()
		out[holdKey(engine, name)] = r
	}
	return out, rows.Err()
}

// Reject remembers that resource's update rolled back from digest (empty:
// the row's newest), replacing what it remembered before.
func (s *Service) Reject(ctx context.Context, resource, digest, reason, rollout string) error {
	r, err := s.row(ctx, resource)
	if err != nil {
		return err
	}
	if digest == "" {
		digest = r.Latest
	}
	if r.Engine == "" || digest == "" {
		return nil // nothing to key it by, or no digest to refuse
	}
	if len(reason) > 1000 {
		reason = reason[:1000]
	}
	_, err = s.st.Writer().ExecContext(ctx, `INSERT INTO update_rejections (id, engine, container, digest, reason, rollout, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (engine, container) DO UPDATE SET digest = excluded.digest, reason = excluded.reason, rollout = excluded.rollout,
			created_at = excluded.created_at`,
		"upr_"+strings.ToLower(crand.Text()), r.Engine, r.Name, digest, reason, rollout, s.now().UnixMilli())
	if err != nil {
		return fmt.Errorf("updates: reject: %w", err)
	}
	s.changed()
	return nil
}

// TryAgain forgets the digest a container's update rolled back from, so
// it's offered again. It answers false when there was none.
func (s *Service) TryAgain(ctx context.Context, resource string) (Row, bool, error) {
	r, err := s.row(ctx, resource)
	if err != nil {
		return Row{}, false, err
	}
	res, err := s.st.Writer().ExecContext(ctx, `DELETE FROM update_rejections WHERE engine = ? AND container = ?`, r.Engine, r.Name)
	if err != nil {
		return Row{}, false, fmt.Errorf("updates: try again: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return r, false, nil
	}
	s.changed()
	r, err = s.row(ctx, resource)
	return r, true, err
}

// pruneRejections forgets the rejections that no longer apply: the
// registry has a newer digest, or the container is current.
func (s *Service) pruneRejections(ctx context.Context, rows []Row) {
	for _, r := range rows {
		if r.Rejected == nil || r.Kind != KindImage {
			continue
		}
		stale := r.State == integration.UpdateCurrent || (r.Latest != "" && r.Latest != r.Rejected.Digest)
		if !stale {
			continue
		}
		if _, err := s.st.Writer().ExecContext(ctx, `DELETE FROM update_rejections WHERE engine = ? AND container = ? AND digest = ?`,
			r.Engine, r.Name, r.Rejected.Digest); err != nil {
			s.log.Warn("updates: forget a rejection", "err", err)
		}
	}
}

// rollbackActions are the updates that roll back on their own when the
// new image fails its health gate (Fail code "rolled_back"): a container's,
// and an owned stack's, which covers each of its containers.
var rollbackActions = map[string]bool{"docker.container.update": true, StackUpdate: true}

// TaskDone is told about every task that ends (action.Options.OnTaskDone):
// an update that rolled back on its own remembers the digest it rolled
// back from, for a stack each of its containers' that had an update. A
// rollout's steps record their own too; recording twice keeps one row. It
// returns at once.
func (s *Service) TaskDone(d action.TaskDone) {
	t := d.Task
	if !rollbackActions[t.Action] || t.State != action.StateFailed || t.Error == nil || t.Error.Code != "rolled_back" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		targets := []string{t.Target}
		if t.Action == StackUpdate {
			targets = s.stackMembers(ctx, t.Target)
		}
		for _, target := range targets {
			if err := s.Reject(ctx, target, "", t.Error.Message, ""); err != nil && !errors.Is(err, ErrNotFound) {
				s.log.Warn("updates: remember a rolled-back digest", "target", target, "err", err)
			}
		}
	}()
}

// stackMembers are the containers whose update is stack's stack.update and
// that had one waiting: what that update tried to move.
func (s *Service) stackMembers(ctx context.Context, stack string) []string {
	rows, err := s.List(ctx)
	if err != nil {
		s.log.Warn("updates: list a stack's containers", "stack", stack, "err", err)
		return nil
	}
	var out []string
	for _, r := range rows {
		if r.Update != nil && r.Update.ID == StackUpdate && r.Update.Target == stack && r.State == integration.UpdateAvailable {
			out = append(out, r.Resource)
		}
	}
	return out
}
