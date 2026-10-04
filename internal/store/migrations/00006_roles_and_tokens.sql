-- Roles, step-up and API tokens (v0.2). Same conventions as schema v0.
--
-- Roles need no change here: users.role has carried the five roles in its
-- CHECK constraint since 00001, and the users_single_owner index keeps it to
-- one owner. What each role may do is internal/authz's table, not SQL.

-- +goose Up

-- A step-up grant (passkey or password re-verification) lasts five minutes
-- on the session that earned it. NULL: none. Stored with the session, so it
-- ends when the session does and never travels in a cookie.
ALTER TABLE sessions ADD COLUMN step_up_until INTEGER;

-- Scoped, expiring API tokens. The table arrives now so the schema settles
-- with roles; the token API that fills it is v0.2's wave 10. A token acts as
-- its user, narrowed to its scopes; authz never lets it do more than the
-- user's role allows, and a token can't step up, so T3+ stays out of reach.
CREATE TABLE api_tokens (
    id           TEXT PRIMARY KEY,         -- tok_…, the public handle the API and audit use
    user_id      TEXT NOT NULL REFERENCES users (id), -- users are disabled, never deleted
    name         TEXT NOT NULL,            -- what the owner called it, e.g. "backup script"
    token_hash   BLOB NOT NULL UNIQUE,     -- SHA-256 of the secret; the secret is shown once and never stored
    scopes       TEXT NOT NULL DEFAULT '[]', -- JSON array of authz scopes, e.g. ["docker.container.restart:host=pve-a"]
    created_at   INTEGER NOT NULL,
    expires_at   INTEGER,                  -- NULL: never; the API will default to an expiry
    last_used_at INTEGER,                  -- written at most every few minutes, like sessions
    revoked_at   INTEGER                   -- revoked tokens stay, so audit records resolve
) STRICT;
CREATE INDEX api_tokens_user ON api_tokens (user_id);

-- +goose Down
DROP TABLE api_tokens;
ALTER TABLE sessions DROP COLUMN step_up_until;
