-- Integration state: a small key-value store per instance (integration.State)
-- for what an integration accumulates across restarts, such as OPNsense's
-- 30-day WAN totals. Same conventions as schema v0. Values are JSON, at most
-- 64 KiB each (checked by the hub), and go when the instance goes.

-- +goose Up
CREATE TABLE integration_state (
    instance_id TEXT NOT NULL REFERENCES integration_instances (id) ON DELETE CASCADE,
    key         TEXT NOT NULL,
    value       TEXT NOT NULL,             -- JSON; the integration owns its shape
    updated_at  INTEGER NOT NULL,
    PRIMARY KEY (instance_id, key)
) STRICT;

-- +goose Down
DROP TABLE integration_state;
