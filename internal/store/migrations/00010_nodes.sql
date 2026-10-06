-- The node registry (internal/nodes, ADR-0014). Same conventions as schema
-- v0: STRICT tables, Unix-millisecond timestamps, prefixed random IDs.
--
-- A node is known by its Ed25519 public key, pinned as "ed25519:…". The
-- registry is the authority: every handshake looks the key up, so revoking
-- a node here cuts it off (there's no CRL). next_key holds a node's next
-- key during a rotation, until the hub first sees the node use it.
--
-- The hub's own keys live in the vault (node/hub-key/<id>); this table
-- lists them, newest last, with retired_at set once a rollover ends.
--
-- Join tokens for remote enrollment (v0.3) will get their own table.

-- +goose Up
CREATE TABLE nodes (
    id           TEXT PRIMARY KEY,               -- n_…
    name         TEXT NOT NULL,                  -- what the node calls itself (CHADASH_NODE_NAME)
    public_key   TEXT NOT NULL UNIQUE,           -- ed25519:…
    next_key     TEXT UNIQUE,                    -- ed25519:…, during a rotation
    state        TEXT NOT NULL CHECK (state IN ('pending', 'approved', 'revoked')),
    enrolled_via TEXT NOT NULL CHECK (enrolled_via IN ('local', 'token')),
    approved_by  TEXT,                           -- a user ID, or 'local-pairing'
    approved_at  INTEGER,
    revoked_by   TEXT,
    revoked_at   INTEGER,
    last_seen_at INTEGER,
    address      TEXT,                           -- the client IP it last connected from
    version      TEXT,                           -- the node binary's version
    hub_pins     TEXT NOT NULL DEFAULT '[]',     -- JSON: the hub keys the node said it trusts
    verbs        TEXT NOT NULL DEFAULT '[]',     -- JSON: the verbs it serves and whether its policy allows each
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL
) STRICT;
-- One live node per name: re-pairing under the same name revokes the old key.
CREATE UNIQUE INDEX nodes_live_name ON nodes (name) WHERE state <> 'revoked';

CREATE TABLE node_hub_keys (
    id         TEXT PRIMARY KEY,                 -- hk_…; the vault holds the private key as node/hub-key/<id>
    pin        TEXT NOT NULL UNIQUE,             -- ed25519:…
    created_at INTEGER NOT NULL,
    retired_at INTEGER
) STRICT;

-- +goose Down
DROP TABLE node_hub_keys;
DROP INDEX nodes_live_name;
DROP TABLE nodes;
