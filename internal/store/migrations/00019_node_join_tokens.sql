-- Join tokens for remote nodes (internal/nodes, ADR-0014). Same
-- conventions as schema v0: STRICT tables, Unix-millisecond timestamps,
-- prefixed random IDs.
--
-- An admin makes a token; a node presents it once and waits as a pending
-- node until an admin approves it. The token is cdj_<secret>.<hub pin>;
-- only the secret's SHA-256 is kept, and it's looked up by that hash, so
-- there's nothing to compare in constant time. A token is spent at
-- enrollment (used_at and node_id set in the transaction that inserts the
-- pending node), whatever the approval says. Spent, expired and revoked
-- tokens are listed for 7 days, then deleted.
--
-- nodes already allows state = 'pending' and enrolled_via = 'token'
-- (00010), so it doesn't change.

-- +goose Up
CREATE TABLE node_join_tokens (
    id         TEXT PRIMARY KEY,                 -- njt_…
    hash       TEXT NOT NULL UNIQUE,             -- SHA-256 of the secret, hex
    name       TEXT NOT NULL,                    -- '': the node picks its name
    add_docker INTEGER NOT NULL CHECK (add_docker IN (0, 1)),
    created_by TEXT REFERENCES users (id) ON DELETE SET NULL,
    created_at INTEGER NOT NULL,                 -- Unix milliseconds
    expires_at INTEGER NOT NULL,
    used_at    INTEGER NOT NULL DEFAULT 0,       -- 0 until a node spends it
    revoked_at INTEGER NOT NULL DEFAULT 0,
    node_id    TEXT REFERENCES nodes (id) ON DELETE SET NULL
) STRICT;

-- +goose Down
DROP TABLE node_join_tokens;
