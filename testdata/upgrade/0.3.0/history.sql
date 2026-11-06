PRAGMA foreign_keys=OFF;
BEGIN TRANSACTION;
CREATE TABLE goose_db_version (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		version_id INTEGER NOT NULL,
		is_applied INTEGER NOT NULL,
		tstamp TIMESTAMP DEFAULT (datetime('now'))
	);
INSERT INTO goose_db_version VALUES(1,0,1,'2026-11-06 03:01:09');
INSERT INTO goose_db_version VALUES(2,1,1,'2026-11-06 03:01:09');
INSERT INTO goose_db_version VALUES(3,2,1,'2026-11-06 03:01:09');
CREATE TABLE series (
    id       INTEGER PRIMARY KEY,
    key      TEXT NOT NULL UNIQUE,     -- "<resource id>#<metric key>"
    instance TEXT NOT NULL DEFAULT '',
    unit     TEXT NOT NULL DEFAULT '',
    first_at INTEGER NOT NULL,
    last_at  INTEGER NOT NULL
) STRICT;
CREATE TABLE chunks (
    id     INTEGER PRIMARY KEY,
    series INTEGER NOT NULL REFERENCES series (id) ON DELETE CASCADE,
    tier   INTEGER NOT NULL CHECK (tier BETWEEN 1 AND 4), -- 1m, 15m, 1h, 1d
    start  INTEGER NOT NULL,
    data   BLOB NOT NULL
) STRICT;
CREATE TABLE states (
    resource TEXT NOT NULL,
    at       INTEGER NOT NULL,
    status   TEXT NOT NULL,
    PRIMARY KEY (resource, at)
) STRICT, WITHOUT ROWID;
PRAGMA writable_schema=ON;
CREATE TABLE IF NOT EXISTS sqlite_sequence(name,seq);
DELETE FROM sqlite_sequence;
INSERT INTO sqlite_sequence VALUES('goose_db_version',3);
CREATE UNIQUE INDEX chunks_key ON chunks (series, tier, start);
CREATE INDEX chunks_expiry ON chunks (tier, start);
CREATE INDEX states_at ON states (at);
PRAGMA writable_schema=OFF;
COMMIT;
