-- The incident timeline (docs/plan/14-v0.4.md, "The incident timeline").
-- Same conventions as schema v0: STRICT tables, Unix-millisecond
-- timestamps, prefixed random IDs.
--
-- Today an incident lives in the inbox topic and a resolved one is kept
-- for 24 hours. This table keeps its lifecycle as rows, one per thing that
-- happened to it, so the timeline can show last week's incident beside the
-- action that fixed it. incident_id is the incident's ID as the inbox keys
-- it, not a foreign key: the incident itself isn't a row. data holds what
-- the kind needs (the new severity, the cause, who acknowledged it, the
-- window it was expected in) as a JSON object. Rows are deleted by age
-- (90 days by default, Settings → History), never changed.
--
-- What writes them comes in wave 28 (internal/incidents).

-- +goose Up
CREATE TABLE incident_events (
    id          TEXT PRIMARY KEY,                -- iev_…
    incident_id TEXT NOT NULL,
    at          INTEGER NOT NULL,                -- Unix milliseconds
    kind        TEXT NOT NULL CHECK (kind IN ('opened', 'changed', 'caused', 'acknowledged', 'expected', 'resolved')),
    data        TEXT NOT NULL DEFAULT '{}'       -- JSON object
) STRICT;
CREATE INDEX incident_events_incident ON incident_events (incident_id, at);
CREATE INDEX incident_events_at ON incident_events (at);

-- +goose Down
DROP TABLE incident_events;
