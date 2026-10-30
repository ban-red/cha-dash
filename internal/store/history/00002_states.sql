-- Health over time (internal/history/states.go): a row when a resource's
-- status changes, never more often. at is when the status began (Unix ms).
-- Besides the integration statuses there are two of the hub's own:
-- "stopped" (written at a clean shutdown for every resource; Cha Dash wasn't
-- watching) and "gone" (the resource left the graph).

-- +goose Up
CREATE TABLE states (
    resource TEXT NOT NULL,
    at       INTEGER NOT NULL,
    status   TEXT NOT NULL,
    PRIMARY KEY (resource, at)
) STRICT, WITHOUT ROWID;

-- Retention looks rows up by age.
CREATE INDEX states_at ON states (at);

-- +goose Down
DROP INDEX states_at;
DROP TABLE states;
