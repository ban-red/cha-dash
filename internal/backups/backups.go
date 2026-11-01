// Package backups will answer one question about the home: what isn't
// backed up, and what's stale (docs/plan/14-v0.4.md, "Backups").
//
// Wave 25 fixes the shape of a freshness objective (backup_objectives,
// migration 00024) and the defaults. Coverage (the correlator's
// backed_up_by relations), the objectives' routes and the backups topic
// (topics.Backups) come in wave 27.
package backups

import "time"

// The default objectives when a resource has no row: a day plus slack for
// a daily job, a week plus a day for a weekly one.
const (
	DefaultDaily  = 26 * time.Hour
	DefaultWeekly = 8 * 24 * time.Hour
)

// Objective is one row of backup_objectives: how old a resource's newest
// good backup may be before coverage calls it stale.
type Objective struct {
	Resource string    `json:"resource"`
	MaxAgeMS int64     `json:"max_age_ms"` // more than 0
	SetBy    string    `json:"set_by,omitempty"`
	SetAt    time.Time `json:"set_at"`
}
