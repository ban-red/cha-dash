-- Passkeys (WebAuthn). Same conventions as schema v0: Unix-millisecond
-- timestamps, prefixed random IDs, STRICT tables. Booleans are 0 or 1.
-- Ceremony challenges are not stored: they live in the hub's memory for
-- five minutes, so a restart only cancels sign-ins in progress.

-- +goose Up

-- The WebAuthn user handle: 32 random bytes the authenticator stores with each
-- passkey and returns at usernameless sign-in. One per user, shared by all of
-- their passkeys, and unrelated to the username or user ID, so it reveals nothing.
CREATE TABLE passkey_users (
    user_id    TEXT PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    handle     BLOB NOT NULL UNIQUE,
    created_at INTEGER NOT NULL
) STRICT;

CREATE TABLE passkeys (
    id                 TEXT PRIMARY KEY,           -- pk_…, the handle the API uses
    user_id            TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    rp_id              TEXT NOT NULL,              -- relying party ID it was registered under, e.g. dash.example.com
    credential_id      BLOB NOT NULL UNIQUE,       -- chosen by the authenticator; the lookup key at sign-in
    public_key         BLOB NOT NULL,              -- COSE_Key
    sign_count         INTEGER NOT NULL DEFAULT 0, -- highest signature counter seen; synced passkeys stay at 0
    clone_warning      INTEGER NOT NULL DEFAULT 0 CHECK (clone_warning IN (0, 1)), -- the counter once went backwards
    transports         TEXT NOT NULL DEFAULT '[]', -- JSON array: usb, nfc, ble, smart-card, hybrid, internal
    aaguid             BLOB,                       -- authenticator model, 16 bytes; all zero when not disclosed
    attachment         TEXT NOT NULL DEFAULT '',   -- platform or cross-platform, as reported at registration
    flags              INTEGER NOT NULL,           -- authenticator data flags from the latest ceremony
    backup_eligible    INTEGER NOT NULL CHECK (backup_eligible IN (0, 1)), -- BE: can sync; must never change
    backup_state       INTEGER NOT NULL CHECK (backup_state IN (0, 1)),    -- BS: synced or backed up; may change
    attestation_type   TEXT NOT NULL DEFAULT '',
    attestation_format TEXT NOT NULL DEFAULT '',
    name               TEXT NOT NULL,
    created_at         INTEGER NOT NULL,
    last_used_at       INTEGER
) STRICT;
CREATE INDEX passkeys_user ON passkeys (user_id);

-- +goose Down
DROP TABLE passkeys;
DROP TABLE passkey_users;
