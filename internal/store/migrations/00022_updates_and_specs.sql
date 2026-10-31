-- Update holds and installed specs (docs/plan/09-v0.3.md, "The Update
-- Center and the spec format"). Same conventions as schema v0: STRICT
-- tables, Unix-millisecond timestamps, prefixed random IDs.
--
-- A hold keeps a container on its image until it's released; a snooze
-- hides an update for a while, or until a newer digest than the one it
-- was snoozed at appears. A hold is keyed by the engine and the container's
-- name, not its ID, because an update recreates the container and the ID
-- changes with it.
--
-- An installed spec is a Tier 1 spec the owner added in Settings, beside
-- the bundled ones. Its YAML is kept as given, so a backup or an export
-- carries it. Its actions stay off until the owner turns them on for it
-- (actions_on), after seeing each one.

-- +goose Up
CREATE TABLE update_holds (
    id         TEXT PRIMARY KEY,                 -- uph_…
    engine     TEXT NOT NULL,                    -- the engine resource the container runs on
    container  TEXT NOT NULL,                    -- the container's name
    kind       TEXT NOT NULL CHECK (kind IN ('hold', 'snooze')),
    digest     TEXT NOT NULL DEFAULT '',         -- snooze: the newest digest when snoozed
    until      INTEGER NOT NULL DEFAULT 0,       -- snooze: when it ends; hold: 0
    created_by TEXT REFERENCES users (id) ON DELETE SET NULL,
    created_at INTEGER NOT NULL,                 -- Unix milliseconds
    UNIQUE (engine, container)
) STRICT;

CREATE TABLE installed_specs (
    id           TEXT PRIMARY KEY,               -- isp_…
    spec_id      TEXT NOT NULL UNIQUE,           -- meta.id
    yaml         TEXT NOT NULL,
    sha256       TEXT NOT NULL,                  -- of yaml, hex
    source       TEXT NOT NULL DEFAULT '',       -- where the owner said it came from
    actions_on   INTEGER NOT NULL DEFAULT 0 CHECK (actions_on IN (0, 1)),
    installed_by TEXT REFERENCES users (id) ON DELETE SET NULL,
    installed_at INTEGER NOT NULL,               -- Unix milliseconds
    updated_at   INTEGER NOT NULL
) STRICT;

-- +goose Down
DROP TABLE installed_specs;
DROP TABLE update_holds;
