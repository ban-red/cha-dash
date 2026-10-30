package history

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"dash.cha.sh/internal/store"
)

// LevelKey is the hub database's settings row for the level an admin chose
// in Settings → History, a JSON string. Without it, CHADASH_HISTORY decides.
// LimitKey holds the size limit, a JSON number of bytes (0: none); without
// it, DefaultMaxBytes.
const (
	LevelKey = "history.level"
	LimitKey = "history.max_bytes"
)

// LoadLimit reads the size limit an admin set; ok is false when nobody did.
func LoadLimit(ctx context.Context, st *store.Store) (limit int64, ok bool, err error) {
	var raw string
	err = st.Reader().QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, LimitKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("history: %w", err)
	}
	if json.Unmarshal([]byte(raw), &limit) != nil || limit < 0 {
		return 0, false, nil
	}
	return limit, true, nil
}

// SaveLimit stores the size limit in the hub's database.
func SaveLimit(ctx context.Context, st *store.Store, limit int64, now time.Time) error {
	b, _ := json.Marshal(limit)
	_, err := st.Writer().ExecContext(ctx, `INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, LimitKey, string(b), now.UnixMilli())
	return err
}

// LoadLevel reads the chosen level from the hub's database (not history.db,
// so deleting that file keeps the choice). ok is false when nobody chose.
func LoadLevel(ctx context.Context, st *store.Store) (l Level, ok bool, err error) {
	var raw string
	err = st.Reader().QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, LevelKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("history: %w", err)
	}
	var s string
	if json.Unmarshal([]byte(raw), &s) != nil {
		return "", false, nil
	}
	if l, err = ParseLevel(s); err != nil {
		return "", false, nil // a level from a newer hub: CHADASH_HISTORY decides
	}
	return l, true, nil
}

// SaveLevel stores the chosen level in the hub's database.
func SaveLevel(ctx context.Context, st *store.Store, l Level, now time.Time) error {
	b, _ := json.Marshal(string(l))
	_, err := st.Writer().ExecContext(ctx, `INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, LevelKey, string(b), now.UnixMilli())
	return err
}

// TierStats is what one tier holds on disk.
type TierStats struct {
	Tier   string // "1m", "15m", "1h", "1d"
	Step   time.Duration
	Keep   time.Duration // 0: forever
	Chunks int
	Bytes  int64     // chunk data, before SQLite's overhead
	Oldest time.Time // zero when empty
}

// Stats is what history.db holds.
type Stats struct {
	Level     Level
	MaxBytes  int64     // the size limit; 0: none
	TrimmedAt time.Time // when points were last dropped to stay under it; zero: never
	Series    int       // series with a row
	Resources int       // resources with health rows
	FileBytes int64     // the database file, WAL not included
	Tiers     []TierStats
}

// TierNames are the tiers as Settings → History names them.
var TierNames = [...]string{Minute: "1m", Quarter: "15m", Hour: "1h", Day: "1d"}

// Stats reads what history.db holds.
func (r *Recorder) Stats(ctx context.Context) (Stats, error) {
	r.mu.Lock()
	level := r.level
	out := Stats{Level: level, MaxBytes: r.maxBytes, TrimmedAt: r.trimmedAt}
	r.mu.Unlock()
	q := r.st.Reader()
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM series`).Scan(&out.Series); err != nil {
		return out, fmt.Errorf("history: %w", err)
	}
	if err := q.QueryRowContext(ctx, `SELECT count(DISTINCT resource) FROM states`).Scan(&out.Resources); err != nil {
		return out, fmt.Errorf("history: %w", err)
	}
	var pages, size int64
	_ = q.QueryRowContext(ctx, `PRAGMA page_count`).Scan(&pages)
	_ = q.QueryRowContext(ctx, `PRAGMA page_size`).Scan(&size)
	out.FileBytes = pages * size
	for t := Minute; t <= Day; t++ {
		ts := TierStats{Tier: TierNames[t], Step: tiers[t].step, Keep: level.keep(t)}
		var oldest sql.NullInt64
		if err := q.QueryRowContext(ctx, `SELECT count(*), coalesce(sum(length(data)), 0), min(start) FROM chunks WHERE tier = ?`, int64(t)).
			Scan(&ts.Chunks, &ts.Bytes, &oldest); err != nil {
			return out, fmt.Errorf("history: %w", err)
		}
		if oldest.Valid {
			ts.Oldest = time.UnixMilli(oldest.Int64).UTC()
		}
		out.Tiers = append(out.Tiers, ts)
	}
	return out, nil
}

// Bytes a point, as measured (13 · History): the 1-minute tier on the Demo
// Home, and about the 15-minute tier's for the coarser ones. SQLite adds
// about a third.
const (
	minuteBytes  = 4.5
	coarserBytes = 7.5
	sqliteFactor = 1.35
	foreverYears = 2 // what "forever" is estimated as
)

// Estimate is about how big history.db grows at level with series series
// once every tier is full (the 1-day tier after two years).
func Estimate(level Level, series int) int64 {
	var perSeries float64
	for t := Minute; t <= level.top(); t++ {
		keep := level.keep(t)
		if keep == 0 {
			keep = foreverYears * 365 * day
		}
		points := float64(keep / tiers[t].step)
		b := coarserBytes
		if t == Minute {
			b = minuteBytes
		}
		perSeries += points * b
	}
	return int64(perSeries * sqliteFactor * float64(series))
}

// Clear deletes every point and health row. What each resource's health is
// now is written again at once, so uptime starts over rather than going
// blank until the next change.
func (r *Recorder) Clear(ctx context.Context) error {
	r.flushMu.Lock()
	defer r.flushMu.Unlock()
	r.mu.Lock()
	r.series, r.byID = map[string]*series{}, map[int64]*series{}
	r.sealed, r.fresh, r.pendingStates = nil, nil, nil
	now := r.now().UnixMilli()
	for id, t := range r.states {
		status := t.last.status
		r.states[id] = &stateTrack{}
		if r.top > 0 && status != StatusStopped && status != StatusGone {
			r.state(id, status, now)
		}
	}
	r.mu.Unlock()
	err := r.st.WriteTx(ctx, func(tx *sql.Tx) error {
		for _, q := range []string{`DELETE FROM chunks`, `DELETE FROM series`, `DELETE FROM states`} {
			if _, err := tx.ExecContext(ctx, q); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("history: %w", err)
	}
	_, err = r.st.Writer().ExecContext(ctx, `PRAGMA incremental_vacuum`)
	return err
}
