-- Stack drafts (internal/stacks): compose files saved in the editor but not
-- deployed yet. Same conventions as schema v0.
--
-- Drafts live in the hub, not on the node: the stacks root only ever
-- changes by a deploy, adopt or rollback, each a commit, so a file on disk
-- that differs from its last commit always means an edit made outside Cha
-- Dash. A draft also survives the node being offline, and is kept when a
-- deploy fails and rolls back, so the work isn't lost.
--
-- One draft per stack and node, with a revision for optimistic saves (two
-- tabs can't both win). Drafts hold ${secret:…} references, never secret
-- values; those live in the vault as stacks/<name>.
--
-- MERGE (track 9B): 00009 was the next free number when this was written;
-- the plan reserves it for 00009_nodes (track 9A). Renumber at the merge.

-- +goose Up
CREATE TABLE stack_drafts (
    node       TEXT NOT NULL,              -- the node's ID
    stack      TEXT NOT NULL,              -- the compose project name
    content    TEXT NOT NULL,
    revision   INTEGER NOT NULL CHECK (revision > 0),
    updated_at INTEGER NOT NULL,
    updated_by TEXT NOT NULL DEFAULT '',   -- the username, for the editor's "saved by"
    PRIMARY KEY (node, stack)
) STRICT;

-- +goose Down
DROP TABLE stack_drafts;
