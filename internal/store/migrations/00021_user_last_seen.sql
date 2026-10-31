-- When each account was last seen. Same conventions as schema v0.
--
-- The Users page read this from the account's live sessions, so signing
-- out turned a regular user into "never signed in". The users row keeps it
-- now, written beside the session's own last_seen_at.

-- +goose Up
ALTER TABLE users ADD COLUMN last_seen_at INTEGER;
UPDATE users SET last_seen_at = (SELECT max(s.last_seen_at) FROM sessions s WHERE s.user_id = users.id);

-- +goose Down
ALTER TABLE users DROP COLUMN last_seen_at;
