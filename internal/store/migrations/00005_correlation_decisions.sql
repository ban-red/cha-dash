-- Correlation decisions: the owner's answer to "these look like the same
-- thing" (internal/correlate). One row per pair and relation type; same_as
-- pairs are stored with the smaller resource ID first. A decision outranks
-- every matching rule: confirm links the pair, reject keeps it apart and
-- stops the suggestion coming back. Same conventions as schema v0.
--
-- from_instance and to_instance are the integration instances that publish
-- the two resources, when they're instances: a decision goes when either
-- instance goes, like the instance's facts.

-- +goose Up
CREATE TABLE correlation_decisions (
    id            TEXT PRIMARY KEY,           -- cd_…
    from_id       TEXT NOT NULL,              -- resource IDs, e.g. docker:i_…/container/sonarr
    rel           TEXT NOT NULL,              -- same_as, runs_on, routes_to, stores_on, monitors, depends_on
    to_id         TEXT NOT NULL,
    verdict       TEXT NOT NULL CHECK (verdict IN ('confirm', 'reject')),
    rule          TEXT NOT NULL DEFAULT '',   -- the rule that suggested it, if a suggestion did
    from_instance TEXT REFERENCES integration_instances (id) ON DELETE CASCADE,
    to_instance   TEXT REFERENCES integration_instances (id) ON DELETE CASCADE,
    created_by    TEXT REFERENCES users (id) ON DELETE SET NULL,
    created_at    INTEGER NOT NULL,
    UNIQUE (from_id, rel, to_id)
) STRICT;

-- +goose Down
DROP TABLE correlation_decisions;
