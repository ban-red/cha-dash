-- How a session's step-up grant was made (ADR-0017: the approval's audit
-- record names the approver's method). Same conventions as schema v0.
--
-- 'passkey', 'password' or 'oidc' (auth.StepUpMethod*), set with
-- step_up_until and cleared with it. NULL with no grant, and for grants
-- made before this migration.

-- +goose Up
ALTER TABLE sessions ADD COLUMN step_up_method TEXT CHECK (step_up_method IN ('passkey', 'password', 'oidc'));

-- +goose Down
ALTER TABLE sessions DROP COLUMN step_up_method;
