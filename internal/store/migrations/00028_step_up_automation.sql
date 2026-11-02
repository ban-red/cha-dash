-- Test automation (auth/automation.go) steps up with its key, and the grant
-- says so: step_up_method also takes 'automation'. SQLite can't change a
-- CHECK in place, so the column is dropped and added again; a grant in
-- force loses its method, which only the next approval's record would have
-- named, and grants last five minutes.

-- +goose Up
ALTER TABLE sessions DROP COLUMN step_up_method;
ALTER TABLE sessions ADD COLUMN step_up_method TEXT CHECK (step_up_method IN ('passkey', 'password', 'oidc', 'automation'));

-- +goose Down
ALTER TABLE sessions DROP COLUMN step_up_method;
ALTER TABLE sessions ADD COLUMN step_up_method TEXT CHECK (step_up_method IN ('passkey', 'password', 'oidc'));
