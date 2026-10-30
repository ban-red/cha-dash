package history

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"dash.cha.sh/internal/store"
)

// LevelKey is the hub database's settings row for the level an admin chose
// in Settings → History, a JSON string. Without it, CHADASH_HISTORY decides.
// LimitKey holds the size limit, a JSON number of bytes (0: none); without
// it, DefaultMaxBytes.
// ExcludeKey holds the instances whose metrics aren't recorded, a JSON
// array of instance IDs.
const (
	LevelKey   = "history.level"
	LimitKey   = "history.max_bytes"
	ExcludeKey = "history.exclude"
)

// LoadExcluded reads the instances an admin left out of history.
func LoadExcluded(ctx context.Context, st *store.Store) ([]string, error) {
	var raw string
	err := st.Reader().QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, ExcludeKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("history: %w", err)
	}
	var ids []string
	if json.Unmarshal([]byte(raw), &ids) != nil {
		return nil, nil
	}
	return ids, nil
}

// SaveExcluded stores the instances left out of history.
func SaveExcluded(ctx context.Context, st *store.Store, ids []string, now time.Time) error {
	if ids == nil {
		ids = []string{}
	}
	b, _ := json.Marshal(ids)
	_, err := st.Writer().ExecContext(ctx, `INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, ExcludeKey, string(b), now.UnixMilli())
	return err
}

// SetExcluded replaces the instances whose samples aren't recorded. What
// they recorded before stays until it ages out or is cleared.
func (r *Recorder) SetExcluded(ids []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.excluded = make(map[string]bool, len(ids))
	for _, id := range ids {
		r.excluded[id] = true
	}
}

// Excluded lists the instances SetExcluded left out, sorted.
func (r *Recorder) Excluded() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string{}, slices.Sorted(maps.Keys(r.excluded))...)
}

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
	Tier string // "1m", "15m", "1h", "1d"
	Step time.Duration
	Keep time.Duration // 0: forever
	// Default is what the level keeps; Custom, whether Keep was set instead.
	Default time.Duration
	Custom  bool
	Chunks  int
	Bytes   int64     // chunk data, before SQLite's overhead
	Oldest  time.Time // zero when empty
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
	Instances []InstanceStats // by series, most first
}

// InstanceStats is what one instance's series hold.
type InstanceStats struct {
	ID     string
	Series int
	Bytes  int64 // chunk data
}

// TierNames are the tiers as Settings → History names them.
var TierNames = [...]string{Minute: "1m", Quarter: "15m", Hour: "1h", Day: "1d"}

// Stats reads what history.db holds.
func (r *Recorder) Stats(ctx context.Context) (Stats, error) {
	r.mu.Lock()
	level, over := r.level, r.keepOver
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
		_, custom := over[t]
		ts := TierStats{Tier: TierNames[t], Step: tiers[t].step, Keep: keepFor(level, over, t), Default: level.keep(t), Custom: custom}
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
	rows, err := q.QueryContext(ctx, `SELECT s.instance, count(DISTINCT s.id), coalesce(sum(length(c.data)), 0)
		FROM series s LEFT JOIN chunks c ON c.series = s.id GROUP BY s.instance ORDER BY count(DISTINCT s.id) DESC, s.instance`)
	if err != nil {
		return out, fmt.Errorf("history: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var in InstanceStats
		if err := rows.Scan(&in.ID, &in.Series, &in.Bytes); err != nil {
			return out, fmt.Errorf("history: %w", err)
		}
		out.Instances = append(out.Instances, in)
	}
	return out, rows.Err()
}

// ClearInstance deletes one instance's series and their points; health
// rows stay, since they belong to resources, not to an instance.
func (r *Recorder) ClearInstance(ctx context.Context, instance string) error {
	r.flushMu.Lock()
	defer r.flushMu.Unlock()
	r.mu.Lock()
	gone := map[int64]bool{}
	for key, s := range r.series {
		if s.instance == instance {
			gone[s.id] = true
			delete(r.series, key)
			delete(r.byID, s.id)
		}
	}
	r.sealed = slices.DeleteFunc(r.sealed, func(c chunkRow) bool { return gone[c.series] })
	r.fresh = slices.DeleteFunc(r.fresh, func(s *series) bool { return gone[s.id] })
	r.mu.Unlock()
	err := r.st.WriteTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM chunks WHERE series IN (SELECT id FROM series WHERE instance = ?)`, instance); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM series WHERE instance = ?`, instance)
		return err
	})
	if err != nil {
		return fmt.Errorf("history: %w", err)
	}
	_, err = r.st.Writer().ExecContext(ctx, `PRAGMA incremental_vacuum`)
	return err
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
func Estimate(level Level, series int) int64 { return EstimateKeep(level, nil, series) }

// EstimateKeep is Estimate with per-tier retention in place of the level's.
func EstimateKeep(level Level, over map[Tier]time.Duration, series int) int64 {
	var perSeries float64
	for t := Minute; t <= level.top(); t++ {
		keep := keepFor(level, over, t)
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

// SeriesCount is how many series the recorder knows.
func (r *Recorder) SeriesCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.series)
}

// FileBytes is the size of history.db's pages, without the WAL.
func (r *Recorder) FileBytes(ctx context.Context) (int64, error) {
	var pages, size int64
	if err := r.st.Reader().QueryRowContext(ctx, `SELECT (SELECT page_count FROM pragma_page_count), (SELECT page_size FROM pragma_page_size)`).Scan(&pages, &size); err != nil {
		return 0, fmt.Errorf("history: %w", err)
	}
	return pages * size, nil
}
