package history

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"dash.cha.sh/internal/store"
)

// KeepKey is the hub database's settings row for per-tier retention an
// admin set, a JSON object of tier name ("1m") to milliseconds (0: forever).
// A tier without an entry keeps what its level says (tiers.go).
// ExcludeKeysKey holds the metric patterns left out of history, a JSON
// array.
const (
	KeepKey        = "history.keep"
	ExcludeKeysKey = "history.exclude_keys"
)

// KeepChoices are the retentions Settings → History offers a tier, shortest
// first; 0 is forever. Any value between the first and the last is allowed.
var KeepChoices = [...][]time.Duration{
	Minute:  {6 * time.Hour, 12 * time.Hour, day, 2 * day, 7 * day, 14 * day},
	Quarter: {2 * day, 7 * day, 14 * day, 30 * day, 90 * day, 180 * day, 365 * day},
	Hour:    {30 * day, 90 * day, 180 * day, 365 * day, 730 * day, 1825 * day},
	Day:     {365 * day, 730 * day, 1825 * day, 3650 * day, 0},
}

// ParseTier reads a tier's name ("1m", "15m", "1h", "1d").
func ParseTier(name string) (Tier, bool) {
	for t := Minute; t <= Day; t++ {
		if TierNames[t] == name {
			return t, true
		}
	}
	return 0, false
}

// keepFor is how long tier t's chunks stay at level, with over's entries
// in place of the level's.
func keepFor(level Level, over map[Tier]time.Duration, t Tier) time.Duration {
	if k, ok := over[t]; ok {
		return k
	}
	return level.keep(t)
}

// CheckKeep says whether per-tier retention is allowed: each value within
// its tier's choices, and no tier keeping less than the finer one below it
// at either level, since queries over a long range read the coarser tier.
func CheckKeep(over map[Tier]time.Duration) error {
	for t, k := range over {
		if t < Minute || t > Day {
			return fmt.Errorf("history: no tier %d", t)
		}
		cs := KeepChoices[t]
		lo, hi := cs[0], cs[len(cs)-1]
		if k == 0 && hi != 0 || k != 0 && (k < lo || hi != 0 && k > hi) {
			return fmt.Errorf("history: the %s tier keeps %s to %s", TierNames[t], words(lo), words(hi))
		}
	}
	inf := func(d time.Duration) time.Duration {
		if d == 0 {
			return 1<<63 - 1
		}
		return d
	}
	for _, l := range []Level{Standard, LongTerm} {
		for t := Quarter; t <= Day; t++ {
			if inf(keepFor(l, over, t)) < inf(keepFor(l, over, t-1)) {
				return fmt.Errorf("history: the %s tier can't keep less than the %s tier", TierNames[t], TierNames[t-1])
			}
		}
	}
	return nil
}

// words is a retention as Settings says it: "6 hours", "14 days", "forever".
func words(d time.Duration) string {
	switch {
	case d == 0:
		return "forever"
	case d%(365*day) == 0:
		return plural(int(d/(365*day)), "year")
	case d%day == 0:
		return plural(int(d/day), "day")
	}
	return plural(int(d/time.Hour), "hour")
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

// SetKeep replaces per-tier retention (CheckKeep it first). Shorter
// retention applies at the next tick rather than within the hour.
func (r *Recorder) SetKeep(over map[Tier]time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.keepOver = maps.Clone(over)
	r.retainSoon = true
}

// Keep is the per-tier retention SetKeep set, by tier.
func (r *Recorder) Keep() map[Tier]time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := maps.Clone(r.keepOver)
	if out == nil {
		out = map[Tier]time.Duration{}
	}
	return out
}

// LoadKeep reads per-tier retention from the hub's database. An entry that
// isn't allowed (a newer hub's, say) is dropped with the rest, so the
// level's retention applies.
func LoadKeep(ctx context.Context, st *store.Store) (map[Tier]time.Duration, error) {
	var raw string
	err := st.Reader().QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, KeepKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("history: %w", err)
	}
	var byName map[string]int64
	if json.Unmarshal([]byte(raw), &byName) != nil {
		return nil, nil
	}
	over := map[Tier]time.Duration{}
	for name, ms := range byName {
		t, ok := ParseTier(name)
		if !ok {
			return nil, nil
		}
		over[t] = time.Duration(ms) * time.Millisecond
	}
	if CheckKeep(over) != nil {
		return nil, nil
	}
	return over, nil
}

// SaveKeep stores per-tier retention in the hub's database.
func SaveKeep(ctx context.Context, st *store.Store, over map[Tier]time.Duration, now time.Time) error {
	byName := map[string]int64{}
	for t, k := range over {
		byName[TierNames[t]] = k.Milliseconds()
	}
	return saveSetting(ctx, st, KeepKey, byName, now)
}

func saveSetting(ctx context.Context, st *store.Store, key string, v any, now time.Time) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = st.Writer().ExecContext(ctx, `INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, key, string(b), now.UnixMilli())
	return err
}

// Metric patterns. A pattern matches a series key ("docker:abc#net_rx")
// whole, and * stands for any run of characters: "docker:*#net_rx" is
// every container's receive rate, "*#temp" every temperature.
const (
	MaxKeyPatterns   = 200
	maxKeyPatternLen = 200
)

// CheckKeyPattern says whether p is a pattern SetExcludedKeys takes.
func CheckKeyPattern(p string) error {
	if p == "" || len(p) > maxKeyPatternLen {
		return fmt.Errorf("history: a pattern is 1 to %d characters", maxKeyPatternLen)
	}
	if strings.Trim(p, "*") == "" {
		return errors.New("history: a pattern of only * would leave out every metric; turn history off instead")
	}
	for _, c := range p {
		if c < ' ' || c == 0x7f {
			return errors.New("history: a pattern has no control characters")
		}
	}
	return nil
}

// matchKey says whether key matches pattern p, * matching any run.
func matchKey(p, key string) bool {
	// The usual greedy match with one backtrack point: the last *.
	pi, ki, star, mark := 0, 0, -1, 0
	for ki < len(key) {
		switch {
		case pi < len(p) && p[pi] == '*':
			star, mark = pi, ki
			pi++
		case pi < len(p) && p[pi] == key[ki]:
			pi++
			ki++
		case star >= 0:
			pi, mark = star+1, mark+1
			ki = mark
		default:
			return false
		}
	}
	for pi < len(p) && p[pi] == '*' {
		pi++
	}
	return pi == len(p)
}

// SetExcludedKeys replaces the metric patterns whose samples aren't
// recorded. What matching series recorded before stays until it ages out.
func (r *Recorder) SetExcludedKeys(patterns []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.keyPatterns = slices.Clone(patterns)
	r.keySkip = map[string]bool{}
}

// ExcludedKeys lists the patterns SetExcludedKeys set, as given.
func (r *Recorder) ExcludedKeys() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string{}, r.keyPatterns...)
}

// skipKey says whether key matches an excluded pattern, remembering the
// answer so a sample costs a map lookup. Under mu.
func (r *Recorder) skipKey(key string) bool {
	if len(r.keyPatterns) == 0 {
		return false
	}
	skip, ok := r.keySkip[key]
	if !ok {
		skip = slices.ContainsFunc(r.keyPatterns, func(p string) bool { return matchKey(p, key) })
		r.keySkip[key] = skip
	}
	return skip
}

// Matching counts the series keys the recorder has seen that p matches,
// recorded or left out, so Settings can say what a pattern covers.
func (r *Recorder) Matching(p string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for key := range r.series {
		if matchKey(p, key) {
			n++
		}
	}
	for key, skipped := range r.keySkip {
		if _, known := r.series[key]; skipped && !known && matchKey(p, key) {
			n++
		}
	}
	return n
}

// LoadExcludedKeys reads the metric patterns an admin left out.
func LoadExcludedKeys(ctx context.Context, st *store.Store) ([]string, error) {
	var raw string
	err := st.Reader().QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, ExcludeKeysKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("history: %w", err)
	}
	var ps []string
	if json.Unmarshal([]byte(raw), &ps) != nil {
		return nil, nil
	}
	return slices.DeleteFunc(ps, func(p string) bool { return CheckKeyPattern(p) != nil }), nil
}

// SaveExcludedKeys stores the metric patterns left out of history.
func SaveExcludedKeys(ctx context.Context, st *store.Store, patterns []string, now time.Time) error {
	if patterns == nil {
		patterns = []string{}
	}
	return saveSetting(ctx, st, ExcludeKeysKey, patterns, now)
}
