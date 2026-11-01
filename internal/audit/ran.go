package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"
)

// Ran lists the actions that ran over [since, before): events that belong
// to a task, on target (every target when ""), oldest first. At most limit
// (MaxLimit when 0 or more), the newest kept. The timeline reads it.
func (l *Log) Ran(ctx context.Context, target string, since, before time.Time, limit int) ([]Event, error) {
	if limit <= 0 || limit > MaxLimit {
		limit = MaxLimit
	}
	q := `SELECT event, hash FROM audit_events WHERE at >= ? AND at < ? AND coalesce(json_extract(event, '$.task'), '') <> ''`
	args := []any{since.UnixMilli(), before.UnixMilli()}
	if target != "" {
		q += ` AND target = ?`
		args = append(args, target)
	}
	q += ` ORDER BY seq DESC LIMIT ?`
	args = append(args, limit)
	rows, err := l.st.Reader().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("audit: query: %w", err)
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var body, hash string
		if err := rows.Scan(&body, &hash); err != nil {
			return nil, err
		}
		var ev Event
		if err := json.Unmarshal([]byte(body), &ev); err != nil {
			return nil, fmt.Errorf("audit: stored event: %w", err)
		}
		ev.Hash = hash
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	slices.Reverse(out)
	return out, nil
}
