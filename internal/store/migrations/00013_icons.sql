-- The hub's icon cache (internal/icons). Tiles can't load images from other
-- origins under the CSP, so the hub fetches each icon itself, checks it, and
-- serves it from GET /api/icons/{id}. Same conventions as schema v0: STRICT
-- tables, Unix-millisecond timestamps, prefixed random IDs.
--
-- A row is one source the owner configured: an icon URL on a tile (kind
-- 'url'), or the origin of a tile's link or an instance's URL, whose favicon
-- is wanted (kind 'origin'). Nothing else is ever fetched. The body is the
-- validated image (PNG, SVG, ICO or WebP, at most 256 KiB); content_type is
-- '' until a fetch succeeds. Rows nothing refers to any more are deleted
-- after 30 days.

-- +goose Up
CREATE TABLE icons (
    id           TEXT PRIMARY KEY,               -- ic_…
    source       TEXT NOT NULL UNIQUE,           -- the icon URL, or the origin (scheme://host[:port])
    kind         TEXT NOT NULL CHECK (kind IN ('url', 'origin')),
    content_type TEXT NOT NULL DEFAULT '',       -- image/png, image/svg+xml, image/x-icon or image/webp
    body         BLOB,                           -- the image as served
    etag         TEXT NOT NULL DEFAULT '',       -- a hash of body
    error        TEXT NOT NULL DEFAULT '',       -- why the last fetch failed, '' after a success
    fetched_at   INTEGER,                        -- the last successful fetch
    checked_at   INTEGER NOT NULL DEFAULT 0,     -- the last attempt
    seen_at      INTEGER NOT NULL,               -- the last time a tile or an instance referred to it
    created_at   INTEGER NOT NULL
) STRICT;

-- +goose Down
DROP TABLE icons;
