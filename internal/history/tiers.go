package history

import (
	"fmt"
	"time"
)

// Level is how much history the hub keeps (ADR-0031).
type Level string

const (
	// Off keeps nothing on disk.
	Off Level = "off"
	// Standard keeps 1-minute points for 48 hours and 15-minute points for 14 days.
	Standard Level = "standard"
	// LongTerm also keeps 15-minute points for 30 days, 1-hour points for a
	// year and 1-day points forever.
	LongTerm Level = "long-term"
)

// ParseLevel reads CHADASH_HISTORY.
func ParseLevel(s string) (Level, error) {
	switch l := Level(s); l {
	case Off, Standard, LongTerm:
		return l, nil
	}
	return "", fmt.Errorf("history: level %q: want off, standard or long-term", s)
}

// Tier is a rollup grid. Its number is the chunks table's tier column.
type Tier uint8

const (
	Minute  Tier = 1
	Quarter Tier = 2
	Hour    Tier = 3
	Day     Tier = 4
)

// tierSpec is a tier's grid. A tier's chunk is a whole number of the next
// tier's steps, so the slot a higher point folds is always inside one chunk
// of the tier below.
type tierSpec struct {
	step  time.Duration
	chunk time.Duration
	// keep is how long chunks stay at Standard and at LongTerm; 0 is forever.
	keep, keepLong time.Duration
}

const day = 24 * time.Hour

var tiers = [...]tierSpec{
	Minute:  {step: time.Minute, chunk: time.Hour, keep: 48 * time.Hour, keepLong: 48 * time.Hour},
	Quarter: {step: 15 * time.Minute, chunk: 6 * time.Hour, keep: 14 * day, keepLong: 30 * day},
	Hour:    {step: time.Hour, chunk: day, keep: 365 * day, keepLong: 365 * day},
	Day:     {step: day, chunk: 30 * day},
}

// top is the highest tier a level writes.
func (l Level) top() Tier {
	switch l {
	case Standard:
		return Quarter
	case LongTerm:
		return Day
	}
	return 0
}

// keep is how long a tier's chunks stay. A tier the level doesn't write
// keeps its long-term retention, so turning long-term off and on again loses
// nothing that is still in date.
func (l Level) keep(t Tier) time.Duration {
	if l == Standard && t <= Standard.top() {
		return tiers[t].keep
	}
	return tiers[t].keepLong
}

func (t Tier) step() int64  { return tiers[t].step.Milliseconds() }
func (t Tier) chunk() int64 { return tiers[t].chunk.Milliseconds() }

// size is the number of points in one of the tier's chunks.
func (t Tier) size() int { return int(tiers[t].chunk / tiers[t].step) }

// floor aligns a Unix-ms time down to a multiple of d (d > 0).
func floor(ms, d int64) int64 {
	r := ms % d
	if r < 0 {
		r += d
	}
	return ms - r
}
