-- Banned client addresses (internal/auth). Five wrong passwords in a row
-- from one address put it here, and it stays until an admin clears it
-- (Settings → Users, or `chadash bans clear`). A banned address can't sign
-- in by any method. Same conventions as schema v0.

-- +goose Up
CREATE TABLE ip_bans (
    ip        TEXT PRIMARY KEY,           -- the client address, as netip.Addr.String() prints it
    banned_at INTEGER NOT NULL,           -- Unix milliseconds
    failures  INTEGER NOT NULL            -- wrong passwords that led to the ban
) STRICT;

-- +goose Down
DROP TABLE ip_bans;
