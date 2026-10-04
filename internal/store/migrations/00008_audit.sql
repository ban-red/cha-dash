-- The audit log (internal/audit): every action attempt, including refusals
-- and failures, plus settings and user changes. Same conventions as schema
-- v0.
--
-- Append-only and hash-chained. Each row keeps the event's canonical JSON
-- exactly as it was hashed (event), and hash is the SHA-256 of those bytes.
-- The event includes prev_hash, the previous row's hash, so changing,
-- removing or reordering any row breaks every hash after it. The first
-- event's prev_hash is sha256: followed by 64 zeros. The canonical form is
-- documented in internal/audit.
--
-- The other columns repeat fields of the event for filtering; verification
-- checks that they agree with it. Nothing in the hub updates or deletes a
-- row (a test greps the code), and the triggers refuse it anyway.
--
-- The v0 stub table "audit" stays as it is; nothing writes to it.

-- +goose Up
CREATE TABLE audit_events (
    seq       INTEGER PRIMARY KEY,          -- 1, 2, 3, … with no gaps
    id        TEXT NOT NULL UNIQUE,         -- a_…
    at        INTEGER NOT NULL,
    actor_id  TEXT NOT NULL DEFAULT '',
    actor     TEXT NOT NULL DEFAULT '',     -- the actor's name
    action    TEXT NOT NULL,
    target    TEXT NOT NULL DEFAULT '',
    result    TEXT NOT NULL CHECK (result IN ('ok', 'failed', 'refused')),
    event     TEXT NOT NULL,                -- canonical JSON, as hashed
    prev_hash TEXT NOT NULL,
    hash      TEXT NOT NULL
) STRICT;
CREATE INDEX audit_events_at ON audit_events (at);
CREATE INDEX audit_events_action ON audit_events (action, seq);
CREATE INDEX audit_events_target ON audit_events (target, seq);
CREATE INDEX audit_events_actor ON audit_events (actor_id, seq);

-- +goose StatementBegin
CREATE TRIGGER audit_events_no_update BEFORE UPDATE ON audit_events
BEGIN
    SELECT RAISE(ABORT, 'audit_events is append-only');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER audit_events_no_delete BEFORE DELETE ON audit_events
BEGIN
    SELECT RAISE(ABORT, 'audit_events is append-only');
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER audit_events_no_delete;
DROP TRIGGER audit_events_no_update;
DROP TABLE audit_events;
