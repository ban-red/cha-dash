-- The action engine's working state (internal/action). Same conventions as
-- schema v0.
--
-- A plan is what POST /api/actions/plan showed: the action, target and
-- params, its tier, blast radius and steps, and a hash over them. It expires
-- 2 minutes after it was made and can be approved once, by the session that
-- made it. A task is one approved plan running: its state, current step,
-- progress and log tail. Idempotency keys let a client retry an approval
-- without running the plan twice; they're kept for a day.
--
-- The record of what happened is the audit log (00008), not these tables:
-- plans and tasks are pruned (plans a day after they expire unapproved,
-- tasks after 30 days), audit events never are.

-- +goose Up
CREATE TABLE plans (
    id          TEXT PRIMARY KEY,           -- p_…
    hash        TEXT NOT NULL,              -- sha256:…, over action, target, params, tier, impact and the target's state
    session_id  TEXT NOT NULL,              -- only this session may approve it
    actor_id    TEXT NOT NULL,
    actor       TEXT NOT NULL,              -- JSON: action.Actor, without step-up
    action      TEXT NOT NULL,
    target      TEXT NOT NULL,
    params      TEXT NOT NULL DEFAULT '{}', -- JSON
    tier        TEXT NOT NULL CHECK (tier IN ('T0', 'T1', 'T2', 'T3', 'T4')),
    state       TEXT NOT NULL,              -- the target's state when planned; approve re-checks it
    document    TEXT NOT NULL,              -- JSON: the plan as the API returned it
    created_at  INTEGER NOT NULL,
    expires_at  INTEGER NOT NULL,
    approved_at INTEGER
) STRICT;
CREATE INDEX plans_expiry ON plans (expires_at);

CREATE TABLE tasks (
    id          TEXT PRIMARY KEY,           -- t_…
    plan_id     TEXT NOT NULL REFERENCES plans (id) ON DELETE CASCADE,
    actor_id    TEXT NOT NULL,
    state       TEXT NOT NULL CHECK (state IN ('queued', 'running', 'verifying', 'succeeded', 'failed', 'cancelled')),
    document    TEXT NOT NULL,              -- JSON: the task as the API returns it
    created_at  INTEGER NOT NULL,
    finished_at INTEGER
) STRICT;
CREATE INDEX tasks_created ON tasks (created_at);
CREATE INDEX tasks_unfinished ON tasks (state) WHERE finished_at IS NULL;

CREATE TABLE idempotency_keys (
    actor_id   TEXT NOT NULL,
    key        TEXT NOT NULL,               -- the client's Idempotency-Key header
    plan_id    TEXT NOT NULL,
    task_id    TEXT NOT NULL REFERENCES tasks (id) ON DELETE CASCADE,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (actor_id, key)
) STRICT;
CREATE INDEX idempotency_keys_created ON idempotency_keys (created_at);

-- +goose Down
DROP TABLE idempotency_keys;
DROP TABLE tasks;
DROP TABLE plans;
