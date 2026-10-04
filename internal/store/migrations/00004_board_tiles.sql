-- Tile configuration and discovery decisions, per board. Same conventions as
-- schema v0.
--
-- Each layout revision carries its board's tile configuration next to the
-- layout document: what the document's items can't hold (name, icon, link,
-- description, chromeless, the instance, check or resource a tile shows, its
-- options and where it came from), keyed by layout item id. The hub owns its
-- shape (internal/boards/tiles.go). Saving the two together keeps them on one
-- revision, so optimistic concurrency and undo cover both.
--
-- Until now the Homepage importer kept tile metadata in its import record
-- (settings 'import/homepage', field "tiles"). That moves into every revision
-- of the imported board here, in the shape the importer now writes, and
-- leaves the record. Links whose scheme isn't http(s) are dropped, as the hub
-- refuses them.

-- +goose Up
ALTER TABLE layouts ADD COLUMN tiles TEXT NOT NULL DEFAULT '{}'; -- JSON object: layout item id → tile config

UPDATE layouts SET tiles = COALESCE((
    SELECT json_group_object(t.item_id, json(t.tile)) FROM (
        SELECT
            json_extract(e.value, '$.item_id') AS item_id,
            CASE WHEN e.value -> '$.options' IS NULL THEN base ELSE json_set(base, '$.options', json(e.value -> '$.options')) END AS tile
        FROM (
            SELECT e.value, json_patch('{}', json_object(
                'name',        NULLIF(json_extract(e.value, '$.name'), ''),
                'icon',        NULLIF(json_extract(e.value, '$.icon'), ''),
                'href',        CASE WHEN lower(substr(json_extract(e.value, '$.href'), 1, 7)) = 'http://'
                                     OR lower(substr(json_extract(e.value, '$.href'), 1, 8)) = 'https://'
                                    THEN json_extract(e.value, '$.href') END,
                'description', NULLIF(json_extract(e.value, '$.description'), ''),
                'chromeless',  CASE WHEN json_extract(e.value, '$.chromeless') = 1 THEN json('true') END,
                'bind',        CASE WHEN NULLIF(json_extract(e.value, '$.instance'), '') IS NOT NULL
                                     OR NULLIF(json_extract(e.value, '$.check'), '') IS NOT NULL
                                     OR json_array_length(e.value, '$.sources') > 0
                                    THEN json_object(
                                        'instance', NULLIF(json_extract(e.value, '$.instance'), ''),
                                        'check',    NULLIF(json_extract(e.value, '$.check'), ''),
                                        'sources',  CASE WHEN json_array_length(e.value, '$.sources') > 0 THEN json(e.value -> '$.sources') END) END,
                'origin',      json_object(
                                    'source', json_extract(s.value, '$.source'),
                                    'file',   NULLIF(json_extract(e.value, '$.origin.file'), ''),
                                    'group',  NULLIF(json_extract(e.value, '$.origin.group'), ''),
                                    'entry',  NULLIF(json_extract(e.value, '$.origin.entry'), ''))
            )) AS base
            FROM settings s, json_each(s.value, '$.tiles') e
            WHERE s.key = 'import/homepage' AND json_extract(e.value, '$.item_id') IS NOT NULL
        ) e
    ) t
), '{}')
WHERE board_id = (SELECT json_extract(value, '$.board_id') FROM settings WHERE key = 'import/homepage');

UPDATE settings SET value = json_remove(value, '$.tiles') WHERE key = 'import/homepage';

-- Discovery suggestions the owner dismissed on a board (label discovery's
-- "Add them?"), by the resource each was about, so they aren't offered again.
CREATE TABLE board_dismissals (
    board_id     TEXT NOT NULL REFERENCES boards (id) ON DELETE CASCADE,
    ref          TEXT NOT NULL,            -- the resource id, e.g. docker:i_…/container/immich_server
    dismissed_at INTEGER NOT NULL,
    PRIMARY KEY (board_id, ref)
) STRICT;

-- +goose Down
-- Tile configuration isn't copied back into the import record: re-importing
-- puts it there again.
DROP TABLE board_dismissals;
ALTER TABLE layouts DROP COLUMN tiles;
