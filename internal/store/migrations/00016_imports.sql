-- Import history (internal/importer): one row per applied import that
-- changed something, from any source. It keeps what was created, by ID, so
-- History can open it and Revert can remove exactly that. File contents are
-- never stored, only the files' names. Same conventions as schema v0.

-- +goose Up
CREATE TABLE imports (
    id          TEXT PRIMARY KEY,         -- imp_…
    source      TEXT NOT NULL,            -- homepage, homarr, yaml
    actor       TEXT,                     -- the user who applied it; NULL from the CLI
    created_at  INTEGER NOT NULL,         -- Unix milliseconds
    files       TEXT NOT NULL,            -- JSON array of file names
    target      TEXT NOT NULL,            -- new, none, or board:<id>
    board_id    TEXT,                     -- the board the tiles went to
    created     TEXT NOT NULL,            -- JSON: {board?, instances, secrets, items}
    counts      TEXT NOT NULL,            -- JSON: {created, updated, tiles, instances, secrets}
    reverted_at INTEGER,                  -- set when Revert ran
    reverted    TEXT                      -- JSON: what Revert removed and kept
) STRICT;
CREATE INDEX imports_created ON imports (created_at DESC);

-- Discovery leads the owner said no to, hub-wide (internal/onboarding).
-- A lead's key is its integration and normalized URL. Label discoveries
-- (tiles for one board) keep their per-board dismissals in board_dismissals.
CREATE TABLE discovery_dismissals (
    key          TEXT PRIMARY KEY,
    dismissed_at INTEGER NOT NULL,        -- Unix milliseconds
    dismissed_by TEXT
) STRICT;

-- +goose Down
DROP TABLE discovery_dismissals;
DROP TABLE imports;
