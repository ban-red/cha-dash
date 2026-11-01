-- The image digests a container's update rolled back from (docs/plan/
-- 14-v0.4.md, wave 31's "Update selected with a broken image"). Same
-- conventions as schema v0.
--
-- When a container's update fails its health gate and rolls back, the
-- Update Center remembers the digest it rolled back from, keyed like a
-- hold by the engine and the container's name (an update recreates the
-- container, and its ID changes with it). While the registry's newest
-- digest is still that one, the row reads "rolled back" and no rollout
-- takes it. A newer digest, or the owner's Try again, ends it.

-- +goose Up
CREATE TABLE update_rejections (
    id         TEXT PRIMARY KEY,                 -- upr_…
    engine     TEXT NOT NULL,                    -- the engine resource the container runs on
    container  TEXT NOT NULL,                    -- the container's name
    digest     TEXT NOT NULL,                    -- the digest it rolled back from
    reason     TEXT NOT NULL DEFAULT '',         -- why, a sentence from the update
    rollout    TEXT NOT NULL DEFAULT '',         -- the rollout it happened in, if any (rol_…)
    created_at INTEGER NOT NULL,                 -- Unix milliseconds
    UNIQUE (engine, container)
) STRICT;

-- +goose Down
DROP TABLE update_rejections;
