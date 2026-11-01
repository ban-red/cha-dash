-- The agent token kind (docs/plan/14-v0.4.md, "Agents"; ADR-0017). Same
-- conventions as schema v0.
--
-- A token's kind is fixed when it's minted: 'api' for scripts and
-- automations, as before, or 'agent' for an AI agent, which may only read
-- and propose. Every token made before this migration is an API token.
-- Minting an agent token needs Agents turned on (the 'agents' settings row,
-- off by default).

-- +goose Up
ALTER TABLE api_tokens ADD COLUMN kind TEXT NOT NULL DEFAULT 'api' CHECK (kind IN ('api', 'agent'));

-- +goose Down
ALTER TABLE api_tokens DROP COLUMN kind;
