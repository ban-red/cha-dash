-- An admin's role choice over the group mappings (ADR-0022). Same
-- conventions as schema v0.
--
-- A user whose role comes from groups (a mapping is set and the account has
-- a linked identity on a method that's on) can still have their role set
-- by an admin, say to demote someone the provider hasn't caught up with.
-- role_override = 1 records that: the role no longer counts as from groups
-- until the user's next sign-in through the provider or the proxy, which
-- applies the mapping again and clears it.

-- +goose Up
ALTER TABLE users ADD COLUMN role_override INTEGER NOT NULL DEFAULT 0 CHECK (role_override IN (0, 1));

-- +goose Down
ALTER TABLE users DROP COLUMN role_override;
