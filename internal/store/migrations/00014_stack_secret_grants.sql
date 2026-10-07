-- Stack secret grants (internal/stacks): which ${secret:…} names the hub
-- has sent to each stack on each node. Same conventions as schema v0.
--
-- A deploy sends the secrets its draft references, and the draft is the
-- hub's own content, so the hub decides. A rollback is different: the
-- version to restore lives on the node, and so does the list of names it
-- references, so a node could ask for secrets that were never its stack's.
-- A rollback therefore sends only names already granted to that stack on
-- that node by an earlier deploy; anything else fails, naming the secret.
-- Names only, never values (those live in the vault as stacks/<name>).

-- +goose Up
CREATE TABLE stack_secret_grants (
    node       TEXT NOT NULL,              -- as in stack_drafts: the target's node
    stack      TEXT NOT NULL,              -- the compose project name
    name       TEXT NOT NULL,              -- the secret's name (vault stacks/<name>)
    granted_at INTEGER NOT NULL,           -- the first deploy that sent it
    PRIMARY KEY (node, stack, name)
) STRICT;

-- +goose Down
DROP TABLE stack_secret_grants;
