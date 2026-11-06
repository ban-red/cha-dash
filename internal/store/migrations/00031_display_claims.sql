-- Displays, from the v0.5 security review (docs/plan/15-v0.5.md, wave 38).
-- Same conventions as schema v0.
--
-- A display's link is a one-time claim code now, not its credential: the
-- claim swaps it for a device secret only that device's cookie holds.
-- claim_hash is the code's SHA-256, cleared once claimed; a code unclaimed
-- by claim_expires_at no longer works. Settings → Displays can make a new
-- link for a display, which signs out the device that had it.
--
-- And a display outlives its board as a revoked row, so audit records
-- still resolve: board_id is set to NULL with the board, and the trigger
-- revokes the board's displays first. SQLite can't change a foreign key in
-- place, so the table is rebuilt with its rows.

-- +goose Up
CREATE TABLE displays_next (
    id               TEXT PRIMARY KEY,
    name             TEXT NOT NULL,
    board_id         TEXT REFERENCES boards (id) ON DELETE SET NULL,
    token_hash       BLOB NOT NULL UNIQUE,
    claim_hash       BLOB UNIQUE,
    claim_expires_at INTEGER,
    quiet_start      TEXT,
    quiet_end        TEXT,
    style            TEXT NOT NULL DEFAULT 'wall' CHECK (style IN ('wall', 'tv', 'ink')),
    created_by       TEXT REFERENCES users (id) ON DELETE SET NULL,
    created_at       INTEGER NOT NULL,
    last_seen_at     INTEGER,
    last_seen_ip     TEXT,
    revoked_at       INTEGER,
    revoked_by       TEXT REFERENCES users (id) ON DELETE SET NULL,
    CHECK ((quiet_start IS NULL) = (quiet_end IS NULL)),
    CHECK ((claim_hash IS NULL) = (claim_expires_at IS NULL)),
    CHECK (board_id IS NOT NULL OR revoked_at IS NOT NULL)
) STRICT;
INSERT INTO displays_next (id, name, board_id, token_hash, quiet_start, quiet_end, style, created_by, created_at, last_seen_at, last_seen_ip, revoked_at, revoked_by)
    SELECT id, name, board_id, token_hash, quiet_start, quiet_end, style, created_by, created_at, last_seen_at, last_seen_ip, revoked_at, revoked_by FROM displays;
DROP TABLE displays;
ALTER TABLE displays_next RENAME TO displays;
CREATE INDEX displays_board ON displays (board_id);

-- +goose StatementBegin
CREATE TRIGGER displays_revoke_with_board BEFORE DELETE ON boards
BEGIN
    UPDATE displays SET revoked_at = CAST(unixepoch('subsec') * 1000 AS INTEGER), claim_hash = NULL, claim_expires_at = NULL
        WHERE board_id = OLD.id AND revoked_at IS NULL;
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER displays_revoke_with_board;
CREATE TABLE displays_prev (
    id           TEXT PRIMARY KEY,
    name         TEXT NOT NULL,
    board_id     TEXT NOT NULL REFERENCES boards (id) ON DELETE CASCADE,
    token_hash   BLOB NOT NULL UNIQUE,
    quiet_start  TEXT,
    quiet_end    TEXT,
    created_by   TEXT REFERENCES users (id) ON DELETE SET NULL,
    created_at   INTEGER NOT NULL,
    last_seen_at INTEGER,
    last_seen_ip TEXT,
    revoked_at   INTEGER,
    revoked_by   TEXT REFERENCES users (id) ON DELETE SET NULL,
    style        TEXT NOT NULL DEFAULT 'wall' CHECK (style IN ('wall', 'tv', 'ink')),
    CHECK ((quiet_start IS NULL) = (quiet_end IS NULL))
) STRICT;
INSERT INTO displays_prev (id, name, board_id, token_hash, quiet_start, quiet_end, created_by, created_at, last_seen_at, last_seen_ip, revoked_at, revoked_by, style)
    SELECT id, name, board_id, token_hash, quiet_start, quiet_end, created_by, created_at, last_seen_at, last_seen_ip, revoked_at, revoked_by, style FROM displays WHERE board_id IS NOT NULL;
DROP TABLE displays;
ALTER TABLE displays_prev RENAME TO displays;
CREATE INDEX displays_board ON displays (board_id);
