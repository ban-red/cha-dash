-- Rollouts, maintenance windows and backup objectives (docs/plan/14-v0.4.md,
-- "Rollouts", "Maintenance windows" and "The coverage map"). Same
-- conventions as schema v0: STRICT tables, Unix-millisecond timestamps,
-- prefixed random IDs.
--
-- A maintenance window is a time range for a set of hosts (resource IDs),
-- once or weekly. A weekly window repeats starts_at..ends_at every 7 days,
-- so it can't be longer than a week. During one, incidents on its hosts
-- open as expected and don't notify.
--
-- A rollout is "Update all", or a selection, as one plan: its steps (JSON,
-- in the order they run) and the hash of the plan a person approved. One
-- scheduled into a window, or for a time, runs as created_by with the
-- step-up taken when it was scheduled, and only while the plan's hash
-- still matches; a changed plan waits for a person ('waiting'). It stops at
-- the first step that fails ('failed'): that step rolls back and the rest
-- don't start. 0 in started_at or finished_at: not yet.
--
-- A backup objective is how old a resource's newest good backup may be
-- before coverage calls it stale. Without a row the default applies (26
-- hours for a daily job, 8 days for a weekly one).
--
-- What reads and writes these comes in waves 26 (rollouts, windows) and 27
-- (objectives).

-- +goose Up
CREATE TABLE maintenance_windows (
    id         TEXT PRIMARY KEY,                 -- mw_…
    name       TEXT NOT NULL,
    hosts      TEXT NOT NULL DEFAULT '[]',       -- JSON array of resource IDs
    starts_at  INTEGER NOT NULL,                 -- Unix milliseconds
    ends_at    INTEGER NOT NULL,
    weekly     INTEGER NOT NULL DEFAULT 0 CHECK (weekly IN (0, 1)),
    created_by TEXT REFERENCES users (id) ON DELETE SET NULL,
    created_at INTEGER NOT NULL,
    CHECK (ends_at > starts_at),
    CHECK (weekly = 0 OR ends_at - starts_at <= 604800000)
) STRICT;

CREATE TABLE rollouts (
    id            TEXT PRIMARY KEY,              -- rol_…
    created_by    TEXT REFERENCES users (id) ON DELETE SET NULL,
    plan_hash     TEXT NOT NULL,
    state         TEXT NOT NULL CHECK (state IN ('scheduled', 'waiting', 'running', 'succeeded', 'failed', 'cancelled')),
    window_id     TEXT REFERENCES maintenance_windows (id) ON DELETE SET NULL,
    scheduled_for INTEGER NOT NULL DEFAULT 0,    -- Unix milliseconds; 0: at once, or when its window opens
    steps         TEXT NOT NULL DEFAULT '[]',    -- JSON array, in run order
    created_at    INTEGER NOT NULL,
    started_at    INTEGER NOT NULL DEFAULT 0,
    finished_at   INTEGER NOT NULL DEFAULT 0
) STRICT;
CREATE INDEX rollouts_scheduled ON rollouts (scheduled_for) WHERE state = 'scheduled';

CREATE TABLE backup_objectives (
    resource   TEXT PRIMARY KEY,                 -- the protected resource's ID
    max_age_ms INTEGER NOT NULL CHECK (max_age_ms > 0),
    set_by     TEXT REFERENCES users (id) ON DELETE SET NULL,
    set_at     INTEGER NOT NULL                  -- Unix milliseconds
) STRICT;

-- +goose Down
DROP TABLE backup_objectives;
DROP TABLE rollouts;
DROP TABLE maintenance_windows;
