# Homarr backup fixture

`backup.sql` is a synthetic Homarr 2 database: the tables the importer reads, with the columns from Homarr's `packages/db/schema/sqlite.ts`, and one small board. The test builds `db.sqlite` from it and zips it with a `metadata.json`, the way Management → Tools → Backup does. Names, hosts and the encrypted secret values are made up; nothing came from a real Homarr.
