-- Displays (docs/plan/15-v0.5.md, "Wave 33: how displays work"). Same
-- conventions as schema v0.
--
-- A display is a wall tablet or a TV showing one board, with nobody at it.
-- It isn't an API token: a token acts as its user with that user's role,
-- and a display has neither. Its secret, cdd_<id>_<secret>, reads one
-- board's incidents and backups and what household sees of the rest
-- (internal/server/displays.go); only its SHA-256 is stored. Revoked displays
-- stay, so audit records resolve.

-- +goose Up
CREATE TABLE displays (
    id           TEXT PRIMARY KEY,         -- dsp_…, the public handle the API and audit use
    name         TEXT NOT NULL,            -- what the admin called it, e.g. "Hall tablet"
    board_id     TEXT NOT NULL REFERENCES boards (id) ON DELETE CASCADE,
    token_hash   BLOB NOT NULL UNIQUE,     -- SHA-256 of the secret; shown once, never stored
    quiet_start  TEXT,                     -- HH:MM, hub local time; NULL: no quiet hours
    quiet_end    TEXT,
    created_by   TEXT REFERENCES users (id) ON DELETE SET NULL,
    created_at   INTEGER NOT NULL,
    last_seen_at INTEGER,                  -- written at most once a minute
    last_seen_ip TEXT,
    revoked_at   INTEGER,
    revoked_by   TEXT REFERENCES users (id) ON DELETE SET NULL,
    CHECK ((quiet_start IS NULL) = (quiet_end IS NULL))
) STRICT;
CREATE INDEX displays_board ON displays (board_id);

-- +goose Down
DROP TABLE displays;
