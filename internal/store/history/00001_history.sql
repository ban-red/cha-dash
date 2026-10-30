-- history.db's schema (internal/history, ADR-0031). Same conventions as the
-- hub's database: STRICT tables, Unix milliseconds.
--
-- A series is one metric of one resource, keyed as on the metrics topic. Its
-- integer ID keeps the chunks' key small, and it stays after the resource
-- leaves the graph, until retention has taken its chunks.
--
-- A chunk is a run of one tier's points for one series: start is Unix ms,
-- aligned to the tier's chunk length, and point i is at start + i × step.
-- data is the encoding in internal/history/codec.go.

-- +goose Up
CREATE TABLE series (
    id       INTEGER PRIMARY KEY,
    key      TEXT NOT NULL UNIQUE,     -- "<resource id>#<metric key>"
    instance TEXT NOT NULL DEFAULT '',
    unit     TEXT NOT NULL DEFAULT '',
    first_at INTEGER NOT NULL,
    last_at  INTEGER NOT NULL
) STRICT;

--
-- chunks is a rowid table, not WITHOUT ROWID on its key: new chunks append
-- to the end of the table, and only the small key index takes inserts all
-- over. Keyed on (series, tier, start), the blobs themselves split pages,
-- and recording cost twice the time (internal/history's budget test).
CREATE TABLE chunks (
    id     INTEGER PRIMARY KEY,
    series INTEGER NOT NULL REFERENCES series (id) ON DELETE CASCADE,
    tier   INTEGER NOT NULL CHECK (tier BETWEEN 1 AND 4), -- 1m, 15m, 1h, 1d
    start  INTEGER NOT NULL,
    data   BLOB NOT NULL
) STRICT;
CREATE UNIQUE INDEX chunks_key ON chunks (series, tier, start);

-- Retention deletes by tier and age.
CREATE INDEX chunks_expiry ON chunks (tier, start);

-- +goose Down
DROP INDEX chunks_expiry;
DROP INDEX chunks_key;
DROP TABLE chunks;
DROP TABLE series;
