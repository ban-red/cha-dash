-- OIDC sign-in and forward-auth (internal/auth, ADR-0022). Same
-- conventions as schema v0: STRICT tables, Unix-millisecond timestamps.
--
-- An identity is the provider's (issuer, subject) pair linked to one Cha
-- Dash user. Users are matched by that pair only, never by email; email is
-- kept to show in settings.
--
-- A login row holds one sign-in or link in flight: state, nonce, the PKCE
-- verifier, the same-origin path to return to, and the user being linked,
-- if any. It expires after 10 minutes.
--
-- A forward-auth link maps the username a trusted proxy sends to a user.
-- The OIDC client secret lives in the vault as auth/oidc/client-secret.

-- +goose Up
CREATE TABLE oidc_identities (
    issuer       TEXT NOT NULL,
    subject      TEXT NOT NULL,
    user_id      TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    email        TEXT NOT NULL,                  -- shown in settings only, never used to match
    created_at   INTEGER NOT NULL,               -- Unix milliseconds
    last_used_at INTEGER NOT NULL,
    PRIMARY KEY (issuer, subject)
) STRICT;

CREATE TABLE oidc_logins (
    state      TEXT PRIMARY KEY,
    nonce      TEXT NOT NULL,
    verifier   TEXT NOT NULL,
    return_to  TEXT NOT NULL,
    link_user  TEXT REFERENCES users (id) ON DELETE CASCADE,  -- set when linking
    expires_at INTEGER NOT NULL                  -- Unix milliseconds
) STRICT;

CREATE TABLE forward_auth_links (
    username   TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    created_at INTEGER NOT NULL                  -- Unix milliseconds
) STRICT;

-- +goose Down
DROP TABLE forward_auth_links;
DROP TABLE oidc_logins;
DROP TABLE oidc_identities;
