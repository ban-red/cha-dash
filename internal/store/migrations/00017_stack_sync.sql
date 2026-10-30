-- Git sync and deploy webhooks (internal/stacks, ADR-0021). Same
-- conventions as schema v0: STRICT tables, Unix-millisecond timestamps,
-- prefixed random IDs.
--
-- A sync makes one stack on one node follow a branch and path on a git
-- remote. It belongs to the admin who set it up and runs with their
-- current role; when that user is deleted owner_id goes NULL and the sync
-- is paused. The remote credential lives in the vault as
-- stacksync/<id>/credential, never here, and the URL never holds userinfo.
--
-- A hook belongs to a remote URL and branch, not to a stack: one webhook
-- triggers every active sync on that remote and branch. Its secret lives
-- in the vault as stackhook/<id>. sender picks the one header it checks.
--
-- A draft's source says where its content came from: 'web' for the
-- editor, 'git:<sha>' for a sync in draft-and-ask mode. Existing drafts
-- were all written by the editor.

-- +goose Up
CREATE TABLE stack_syncs (
    id                TEXT PRIMARY KEY,          -- syn_…
    node              TEXT NOT NULL,
    stack             TEXT NOT NULL,
    url               TEXT NOT NULL,             -- no userinfo
    branch            TEXT NOT NULL,
    path              TEXT NOT NULL,             -- default '<stack>/compose.yaml'
    auth_kind         TEXT NOT NULL CHECK (auth_kind IN ('none', 'https', 'ssh')),
    host_key          TEXT,                      -- pinned SSH host key, when ssh
    mode              TEXT NOT NULL CHECK (mode IN ('deploy', 'ask')),
    poll_seconds      INTEGER NOT NULL,
    secrets_allow     TEXT NOT NULL,             -- JSON array of stack secret names
    owner_id          TEXT REFERENCES users (id) ON DELETE SET NULL,  -- NULL: owner deleted, sync paused
    state             TEXT NOT NULL CHECK (state IN ('active', 'held', 'paused')),
    state_reason      TEXT NOT NULL,
    last_seen_sha     TEXT NOT NULL,
    last_deployed_sha TEXT NOT NULL,
    rejected_sha      TEXT NOT NULL,
    last_error        TEXT NOT NULL,
    created_at        INTEGER NOT NULL,          -- Unix milliseconds
    updated_at        INTEGER NOT NULL,
    UNIQUE (node, stack)
) STRICT;

CREATE INDEX stack_syncs_remote ON stack_syncs (url, branch);

CREATE TABLE stack_hooks (
    id           TEXT PRIMARY KEY,               -- whk_…
    url          TEXT NOT NULL,                  -- normalized, no userinfo
    branch       TEXT NOT NULL,
    sender       TEXT NOT NULL CHECK (sender IN ('github', 'gitea', 'forgejo', 'gitlab', 'chadash')),
    created_by   TEXT REFERENCES users (id) ON DELETE SET NULL,
    created_at   INTEGER NOT NULL,               -- Unix milliseconds
    last_used_at INTEGER NOT NULL,               -- 0 until the first valid request
    UNIQUE (url, branch)
) STRICT;

ALTER TABLE stack_drafts ADD COLUMN source TEXT NOT NULL DEFAULT 'web';  -- 'web' | 'git:<sha>'

-- +goose Down
ALTER TABLE stack_drafts DROP COLUMN source;
DROP TABLE stack_hooks;
DROP INDEX stack_syncs_remote;
DROP TABLE stack_syncs;
