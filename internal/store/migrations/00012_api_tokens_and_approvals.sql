-- API tokens in use, and the approval queue for plans a person must approve
-- (v0.2, wave 10). Same conventions as schema v0.
--
-- api_tokens arrived in 00006. A token's secret is `cdt_<id>_<secret>`: the
-- table keeps only the SHA-256 of the whole string (token_hash), looked up
-- by the id half and compared in constant time. internal/auth/tokens.go says
-- why a fast hash is right for a 256-bit random secret.

-- +goose Up

-- Who revoked a token (a user ID): the owner of the token, or an admin.
ALTER TABLE api_tokens ADD COLUMN revoked_by TEXT;
-- The client address of the token's last use, written with last_used_at
-- (at most once a minute), so the token list can show where it was used.
ALTER TABLE api_tokens ADD COLUMN last_used_ip TEXT;

-- A plan that waits for a person: an API token's T2 plan (and, later, any
-- plan an agent proposes; ADR-0017). The plan row (00007) keeps what was
-- shown and its hash; its actor is the originator. An approval binds to
-- that hash: a person approves exactly that plan, in a session, or it never
-- runs. Rows are kept like their plans; the audit log is the record.
CREATE TABLE approvals (
    id            TEXT PRIMARY KEY,               -- apv_…
    plan_id       TEXT NOT NULL UNIQUE REFERENCES plans (id) ON DELETE CASCADE,
    originator_id TEXT NOT NULL,                  -- the actor that asked (tok_…); only it, and approvers, may read it
    originator    TEXT NOT NULL,                  -- JSON: action.Actor of the originator
    state         TEXT NOT NULL CHECK (state IN ('awaiting_approval', 'approved', 'denied', 'expired')),
    reason        TEXT NOT NULL DEFAULT '',       -- why it was denied or expired, a sentence
    created_at    INTEGER NOT NULL,
    expires_at    INTEGER NOT NULL,
    decided_at    INTEGER,
    decided_by    TEXT,                           -- JSON: action.Actor of the person who decided; NULL when it expired
    task_id       TEXT                            -- the task an approval started
) STRICT;
CREATE INDEX approvals_awaiting ON approvals (expires_at) WHERE state = 'awaiting_approval';
CREATE INDEX approvals_originator ON approvals (originator_id);

-- +goose Down
DROP TABLE approvals;
ALTER TABLE api_tokens DROP COLUMN last_used_ip;
ALTER TABLE api_tokens DROP COLUMN revoked_by;
