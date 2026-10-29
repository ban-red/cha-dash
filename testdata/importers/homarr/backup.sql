-- A synthetic Homarr 2 database: the tables the importer reads, with the
-- columns of Homarr's packages/db/schema/sqlite.ts, and a small board.
-- Every name, host and secret is made up. The test builds db.sqlite from
-- this file and zips it with a metadata.json, as Homarr's backup does.

CREATE TABLE board (id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, isPublic INTEGER NOT NULL DEFAULT 0, creatorId TEXT, pageTitle TEXT, metaTitle TEXT, logoImageUrl TEXT, faviconImageUrl TEXT, backgroundImageUrl TEXT, primaryColor TEXT NOT NULL DEFAULT '#fa5252', secondaryColor TEXT NOT NULL DEFAULT '#fd7e14', opacity INTEGER NOT NULL DEFAULT 100, customCss TEXT, iconColor TEXT, itemRadius TEXT NOT NULL DEFAULT 'lg', disableStatus INTEGER NOT NULL DEFAULT 0);
CREATE TABLE section (id TEXT PRIMARY KEY, boardId TEXT NOT NULL, kind TEXT NOT NULL, xOffset INTEGER, yOffset INTEGER, name TEXT, options TEXT DEFAULT '{"json":{}}');
CREATE TABLE item (id TEXT PRIMARY KEY, boardId TEXT NOT NULL, kind TEXT NOT NULL, options TEXT NOT NULL DEFAULT '{"json":{}}', advancedOptions TEXT NOT NULL DEFAULT '{"json":{}}');
CREATE TABLE app (id TEXT PRIMARY KEY, name TEXT NOT NULL, description TEXT, iconUrl TEXT NOT NULL, href TEXT, pingUrl TEXT);
CREATE TABLE integration (id TEXT PRIMARY KEY, name TEXT NOT NULL, url TEXT NOT NULL, kind TEXT NOT NULL, appId TEXT);
CREATE TABLE integration_item (itemId TEXT NOT NULL, integrationId TEXT NOT NULL, PRIMARY KEY (itemId, integrationId));
CREATE TABLE integrationSecret (kind TEXT NOT NULL, value TEXT NOT NULL, updatedAt INTEGER NOT NULL, integrationId TEXT NOT NULL, PRIMARY KEY (integrationId, kind));
CREATE TABLE layout (id TEXT PRIMARY KEY, name TEXT NOT NULL, boardId TEXT NOT NULL, columnCount INTEGER NOT NULL, leftGutterColumnCount INTEGER NOT NULL DEFAULT 0, rightGutterColumnCount INTEGER NOT NULL DEFAULT 0, breakpoint INTEGER NOT NULL DEFAULT 0, role TEXT NOT NULL DEFAULT 'custom', resizeSource TEXT);
CREATE TABLE item_layout (itemId TEXT NOT NULL, sectionId TEXT NOT NULL, layoutId TEXT NOT NULL, xOffset INTEGER NOT NULL, yOffset INTEGER NOT NULL, width INTEGER NOT NULL, height INTEGER NOT NULL, PRIMARY KEY (itemId, sectionId, layoutId));

INSERT INTO board (id, name) VALUES ('brd_home', 'home'), ('brd_spare', 'spare');
INSERT INTO layout (id, name, boardId, columnCount, breakpoint) VALUES ('lay_sm', 'Mobile', 'brd_home', 3, 0), ('lay_lg', 'Desktop', 'brd_home', 12, 1400);
INSERT INTO section (id, boardId, kind, xOffset, yOffset, name) VALUES
  ('sec_top', 'brd_home', 'empty', 0, 0, NULL),
  ('sec_media', 'brd_home', 'category', 0, 1, 'Media'),
  ('sec_net', 'brd_home', 'category', 0, 2, 'Network');
INSERT INTO app (id, name, description, iconUrl, href, pingUrl) VALUES
  ('app_sonarr', 'Sonarr', 'TV', 'https://cdn.example/sonarr.svg', 'https://sonarr.home.example', NULL),
  ('app_wiki', 'Wiki', NULL, 'https://cdn.example/wiki.svg', 'https://wiki.home.example', 'http://10.20.0.9:3000'),
  ('app_plex', 'Plex', NULL, 'https://cdn.example/plex.svg', 'https://plex.home.example', NULL);
INSERT INTO integration (id, name, url, kind, appId) VALUES
  ('int_sonarr', 'Sonarr', 'https://sonarr.home.example', 'sonarr', 'app_sonarr'),
  ('int_radarr', 'Radarr', 'https://radarr.home.example', 'radarr', NULL),
  ('int_qbit', 'qBittorrent', 'http://10.20.0.5:8080', 'qBittorrent', NULL),
  ('int_pihole', 'Pi-hole', 'http://pihole.lan', 'piHole', NULL),
  ('int_github', 'GitHub', 'https://api.github.example', 'github', NULL);
INSERT INTO integrationSecret (kind, value, updatedAt, integrationId) VALUES
  ('apiKey', 'aGVsbG8.ZmFrZS1lbmNyeXB0ZWQ', 0, 'int_sonarr'),
  ('password', 'aGVsbG8.ZmFrZS1lbmNyeXB0ZWQ', 0, 'int_qbit');
INSERT INTO item (id, boardId, kind, options) VALUES
  ('itm_clock', 'brd_home', 'clock', '{"json":{"is24HourFormat":true}}'),
  ('itm_sonarr', 'brd_home', 'app', '{"json":{"appId":"app_sonarr","openInNewTab":true}}'),
  ('itm_plex', 'brd_home', 'app', '{"json":{"appId":"app_plex"}}'),
  ('itm_cal', 'brd_home', 'calendar', '{"json":{}}'),
  ('itm_dl', 'brd_home', 'downloads', '{"json":{}}'),
  ('itm_wiki', 'brd_home', 'app', '{"json":{"appId":"app_wiki"}}'),
  ('itm_dns', 'brd_home', 'dnsHoleSummary', '{"json":{}}'),
  ('itm_note', 'brd_home', 'notebook', '{"json":{"content":"private notes"}}');
INSERT INTO integration_item (itemId, integrationId) VALUES
  ('itm_cal', 'int_sonarr'), ('itm_cal', 'int_radarr'), ('itm_dl', 'int_qbit'), ('itm_dns', 'int_pihole');
INSERT INTO item_layout (itemId, sectionId, layoutId, xOffset, yOffset, width, height) VALUES
  ('itm_clock', 'sec_top', 'lay_lg', 0, 0, 2, 1),
  ('itm_sonarr', 'sec_media', 'lay_lg', 0, 0, 1, 1),
  ('itm_plex', 'sec_media', 'lay_lg', 1, 0, 1, 1),
  ('itm_cal', 'sec_media', 'lay_lg', 0, 1, 4, 2),
  ('itm_dl', 'sec_media', 'lay_lg', 4, 1, 4, 2),
  ('itm_wiki', 'sec_net', 'lay_lg', 0, 0, 1, 1),
  ('itm_dns', 'sec_net', 'lay_lg', 1, 0, 2, 1),
  ('itm_note', 'sec_net', 'lay_lg', 3, 0, 2, 2),
  ('itm_clock', 'sec_top', 'lay_sm', 0, 0, 1, 1);
