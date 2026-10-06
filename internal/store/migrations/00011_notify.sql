-- Notifications (internal/notify). Same conventions as schema v0: STRICT
-- tables, Unix-millisecond timestamps, prefixed random IDs.
--
-- Channels are where messages go: ntfy, a webhook, or the built-in Web Push
-- channel (one row, nc_webpush, created by the service). config holds what
-- isn't secret; the ntfy token, the webhook's signing secret and the VAPID
-- private key live in the vault (notify/channel/<id>/<field>,
-- notify/vapid-key), never here.
--
-- Rules are a fixed set the code defines (symptom-first defaults); a row
-- only records what an admin changed. Events are what the hub decided to
-- tell someone, one per incident and transition, which is also how a
-- restart doesn't repeat itself. Deliveries are each attempt to reach one
-- channel (or one device, for Web Push), with retries, and are the
-- delivery log.

-- +goose Up
CREATE TABLE notify_channels (
    id         TEXT PRIMARY KEY,                -- nc_…
    kind       TEXT NOT NULL CHECK (kind IN ('webpush', 'ntfy', 'webhook')),
    name       TEXT NOT NULL UNIQUE,
    enabled    INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    config     TEXT NOT NULL DEFAULT '{}',      -- JSON, never a secret
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
) STRICT;

CREATE TABLE notify_rules (
    id         TEXT PRIMARY KEY,                -- the rule's name: incident.down, …
    settings   TEXT NOT NULL DEFAULT '{}',      -- JSON: enabled, resolved, grace_minutes, channels
    updated_at INTEGER NOT NULL,
    updated_by TEXT NOT NULL DEFAULT ''
) STRICT;

-- A browser's push subscription, per user and device. The endpoint is a
-- capability URL at the browser vendor's push service; p256dh and auth are
-- the keys payloads are encrypted to (RFC 8291). None of it is ever sent
-- back whole: the API shows the endpoint's host.
CREATE TABLE notify_push_subscriptions (
    id          TEXT PRIMARY KEY,               -- ps_…
    user_id     TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    endpoint    TEXT NOT NULL UNIQUE,
    p256dh      BLOB NOT NULL,
    auth        BLOB NOT NULL,
    label       TEXT NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL,
    last_ok_at  INTEGER,
    last_error  TEXT
) STRICT;
CREATE INDEX notify_push_subscriptions_user ON notify_push_subscriptions (user_id);

CREATE TABLE notify_events (
    id         TEXT PRIMARY KEY,                -- ne_…
    rule       TEXT NOT NULL,
    incident   TEXT NOT NULL DEFAULT '',        -- the incident, or '' (actions, tests)
    transition TEXT NOT NULL,                   -- down, attention, repair, stuck, resolved, action
    title      TEXT NOT NULL,
    notified   INTEGER NOT NULL CHECK (notified IN (0, 1)), -- 0: recorded only (a resolution the rule keeps quiet)
    created_at INTEGER NOT NULL
) STRICT;
CREATE INDEX notify_events_incident ON notify_events (incident, created_at);
CREATE INDEX notify_events_created ON notify_events (created_at);

CREATE TABLE notify_deliveries (
    id              TEXT PRIMARY KEY,           -- nd_…
    channel_id      TEXT NOT NULL,              -- no foreign key: the log outlives a deleted channel
    channel_name    TEXT NOT NULL,
    channel_kind    TEXT NOT NULL,
    subscription_id TEXT,                       -- Web Push: the device
    events          TEXT NOT NULL DEFAULT '[]', -- JSON: the event IDs this message carries
    kind            TEXT NOT NULL,              -- the message's kind (incident.down, group, test, …)
    title           TEXT NOT NULL,
    payload         TEXT NOT NULL,              -- JSON: the message, never a secret
    state           TEXT NOT NULL CHECK (state IN ('pending', 'sent', 'failed', 'dropped')),
    attempts        INTEGER NOT NULL DEFAULT 0,
    next_at         INTEGER,                    -- when a pending delivery is tried next
    status_code     INTEGER,
    last_error      TEXT,
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL
) STRICT;
CREATE INDEX notify_deliveries_due ON notify_deliveries (state, next_at);
CREATE INDEX notify_deliveries_created ON notify_deliveries (created_at);

-- +goose Down
DROP INDEX notify_deliveries_created;
DROP INDEX notify_deliveries_due;
DROP TABLE notify_deliveries;
DROP INDEX notify_events_created;
DROP INDEX notify_events_incident;
DROP TABLE notify_events;
DROP INDEX notify_push_subscriptions_user;
DROP TABLE notify_push_subscriptions;
DROP TABLE notify_rules;
DROP TABLE notify_channels;
