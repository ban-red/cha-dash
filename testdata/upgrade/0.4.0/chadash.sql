PRAGMA foreign_keys=OFF;
BEGIN TRANSACTION;
CREATE TABLE goose_db_version (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		version_id INTEGER NOT NULL,
		is_applied INTEGER NOT NULL,
		tstamp TIMESTAMP DEFAULT (datetime('now'))
	);
INSERT INTO goose_db_version VALUES(1,0,1,'2026-11-06 03:01:12');
INSERT INTO goose_db_version VALUES(2,1,1,'2026-11-06 03:01:12');
INSERT INTO goose_db_version VALUES(3,2,1,'2026-11-06 03:01:12');
INSERT INTO goose_db_version VALUES(4,3,1,'2026-11-06 03:01:12');
INSERT INTO goose_db_version VALUES(5,4,1,'2026-11-06 03:01:12');
INSERT INTO goose_db_version VALUES(6,5,1,'2026-11-06 03:01:12');
INSERT INTO goose_db_version VALUES(7,6,1,'2026-11-06 03:01:12');
INSERT INTO goose_db_version VALUES(8,7,1,'2026-11-06 03:01:12');
INSERT INTO goose_db_version VALUES(9,8,1,'2026-11-06 03:01:12');
INSERT INTO goose_db_version VALUES(10,9,1,'2026-11-06 03:01:12');
INSERT INTO goose_db_version VALUES(11,10,1,'2026-11-06 03:01:12');
INSERT INTO goose_db_version VALUES(12,11,1,'2026-11-06 03:01:12');
INSERT INTO goose_db_version VALUES(13,12,1,'2026-11-06 03:01:12');
INSERT INTO goose_db_version VALUES(14,13,1,'2026-11-06 03:01:12');
INSERT INTO goose_db_version VALUES(15,14,1,'2026-11-06 03:01:12');
INSERT INTO goose_db_version VALUES(16,15,1,'2026-11-06 03:01:12');
INSERT INTO goose_db_version VALUES(17,16,1,'2026-11-06 03:01:12');
INSERT INTO goose_db_version VALUES(18,17,1,'2026-11-06 03:01:12');
INSERT INTO goose_db_version VALUES(19,18,1,'2026-11-06 03:01:12');
INSERT INTO goose_db_version VALUES(20,19,1,'2026-11-06 03:01:12');
INSERT INTO goose_db_version VALUES(21,20,1,'2026-11-06 03:01:12');
INSERT INTO goose_db_version VALUES(22,21,1,'2026-11-06 03:01:12');
INSERT INTO goose_db_version VALUES(23,22,1,'2026-11-06 03:01:12');
INSERT INTO goose_db_version VALUES(24,23,1,'2026-11-06 03:01:12');
INSERT INTO goose_db_version VALUES(25,24,1,'2026-11-06 03:01:12');
INSERT INTO goose_db_version VALUES(26,25,1,'2026-11-06 03:01:12');
INSERT INTO goose_db_version VALUES(27,26,1,'2026-11-06 03:01:12');
INSERT INTO goose_db_version VALUES(28,27,1,'2026-11-06 03:01:12');
INSERT INTO goose_db_version VALUES(29,28,1,'2026-11-06 03:01:12');
CREATE TABLE settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,              -- JSON
    updated_at INTEGER NOT NULL
) STRICT;
CREATE TABLE users (
    id            TEXT PRIMARY KEY,
    username      TEXT NOT NULL UNIQUE COLLATE NOCASE,
    password_hash TEXT,                    -- argon2id PHC string; NULL for passkey- or OIDC-only users
    role          TEXT NOT NULL CHECK (role IN ('owner', 'admin', 'operator', 'viewer', 'household')),
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL,
    disabled_at   INTEGER
, role_override INTEGER NOT NULL DEFAULT 0 CHECK (role_override IN (0, 1)), last_seen_at INTEGER) STRICT;
INSERT INTO users VALUES('u_24lldksrl4dvqjfovmqc35uoak','owner','$argon2id$v=19$m=19456,t=2,p=1$JW0q7lcfddr1A+Tr6rTmYg$AErbe5bR2n/42gSMGv3Pmvl8jQzSv+gc6MLSmY3lXi0','owner',1793934072946,1793934072946,NULL,0,1793934072947);
CREATE TABLE sessions (
    id           TEXT PRIMARY KEY,         -- public handle, safe to list and to reference in audit
    token_hash   BLOB NOT NULL UNIQUE,     -- SHA-256 of the cookie token; the token is never stored
    user_id      TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    method       TEXT NOT NULL,            -- how it was established: password (passkey, oidc later)
    created_at   INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL,
    last_seen_at INTEGER NOT NULL,
    ip           TEXT,
    user_agent   TEXT
, step_up_until INTEGER, step_up_method TEXT CHECK (step_up_method IN ('passkey', 'password', 'oidc', 'automation'))) STRICT;
INSERT INTO sessions VALUES('s_ee7hegy2etqcso5udjibqyojbu',x'76f18ba306db903040ecaa8f4967455046ebca33aec131dfb25022f31f8b09cd','u_24lldksrl4dvqjfovmqc35uoak','password',1793934072947,1796526072947,1793934072947,'127.0.0.1','Go-http-client/1.1',NULL,NULL);
CREATE TABLE integration_instances (
    id          TEXT PRIMARY KEY,
    integration TEXT NOT NULL,             -- e.g. proxmox, sonarr
    name        TEXT NOT NULL,
    config      TEXT NOT NULL DEFAULT '{}', -- JSON; credentials appear only as ${secret:…} references
    enabled     INTEGER NOT NULL DEFAULT 1,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL,
    UNIQUE (integration, name)
) STRICT;
INSERT INTO integration_instances VALUES('i_s5qw64ecijrfi2duazjblufxsn','sonarr','Sonarr','{"api_key":"${secret:instance/i_s5qw64ecijrfi2duazjblufxsn/api_key}","url":"http://127.0.0.1:9/"}',0,1793934072947,1793934072947);
CREATE TABLE vault_secrets (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE,
    alg        TEXT NOT NULL,              -- envelope format version
    kek_id     TEXT NOT NULL,              -- fingerprint of the key-encryption key that wrapped data_key
    data_key   BLOB NOT NULL,              -- nonce || sealed per-secret data key
    ciphertext BLOB NOT NULL,              -- nonce || sealed value
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
) STRICT;
INSERT INTO vault_secrets VALUES('sec_hvnjb2b7ke73swm3alejguqzfm','node/hub-key/hk_4zb4vvettiayp3djxtxi2bjais','xchacha20poly1305/v1','86b8182cfa9d4af2',x'299c6cdc8bd00a9423f7b4ee5364c3d42e76f2de33d4b8d7e4e6ccccf7558e3cfaea4563c9e00eec0452293f37cf5b995b4aa8ef1f4945943e2625925767afc9de9166a1f6ed6c1e',x'5b50f4a724e5ef2be729db61f70c9c117887352993e1ca480e3f8343bfbc153023d94cad3961bfbe4b254ca20afbaa0b769cfbf82d8e0f256b9bf7d7bf327fe70e1a838b8a2ca3a36e5563c50e26d59e4a5920ef722455b6385afdfbc8a14d430134901ef092c3dd4fb24e574c2a3d06b0d1d5300d85ab4f933d27797c02398abe05c3ff0b3e9f33d52970bca946ac60960a561e775d40a33b5b558e245fa7',1793934072924,1793934072924);
INSERT INTO vault_secrets VALUES('sec_k5lgo3q2lfpm4ivppwrsjbmop7','instance/i_s5qw64ecijrfi2duazjblufxsn/api_key','xchacha20poly1305/v1','86b8182cfa9d4af2',x'758bffaa151c17e20d5d836319f83e3ab602f1a6dbc8b8f71315a8bbe0002b4e8cda42631d880a1a58bde434ecd24612bbc01cd3e6b8c64f4245a583e28ac461d747ba3b7ab8258d',x'e31e502e535904923802308eb089c934a6d6571b7e112f73be312fc5793aaceeae0a093c0449a5376d6d4bc82a1458465630ef5f823e99e70b2ce7ed39e90b307b4f8935fee19bda',1793934072947,1793934072947);
CREATE TABLE boards (
    id         TEXT PRIMARY KEY,
    slug       TEXT NOT NULL UNIQUE,
    title      TEXT NOT NULL,
    position   INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
) STRICT;
CREATE TABLE layouts (
    board_id       TEXT NOT NULL REFERENCES boards (id) ON DELETE CASCADE,
    revision       INTEGER NOT NULL,
    format_version INTEGER NOT NULL,       -- the layout package's document format version
    document       TEXT NOT NULL,          -- JSON
    created_at     INTEGER NOT NULL,
    created_by     TEXT REFERENCES users (id) ON DELETE SET NULL, tiles TEXT NOT NULL DEFAULT '{}',
    PRIMARY KEY (board_id, revision)
) STRICT;
CREATE TABLE audit (
    id     INTEGER PRIMARY KEY AUTOINCREMENT,
    at     INTEGER NOT NULL,
    actor  TEXT NOT NULL,                  -- JSON: user, via, session, ip
    action TEXT NOT NULL,
    target TEXT,
    params TEXT,                           -- JSON
    tier   TEXT,
    result TEXT NOT NULL,
    detail TEXT                            -- JSON: plan, duration, task
) STRICT;
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
CREATE TABLE integration_state (
    instance_id TEXT NOT NULL REFERENCES integration_instances (id) ON DELETE CASCADE,
    key         TEXT NOT NULL,
    value       TEXT NOT NULL,             -- JSON; the integration owns its shape
    updated_at  INTEGER NOT NULL,
    PRIMARY KEY (instance_id, key)
) STRICT;
CREATE TABLE board_dismissals (
    board_id     TEXT NOT NULL REFERENCES boards (id) ON DELETE CASCADE,
    ref          TEXT NOT NULL,            -- the resource id, e.g. docker:i_…/container/immich_server
    dismissed_at INTEGER NOT NULL,
    PRIMARY KEY (board_id, ref)
) STRICT;
CREATE TABLE correlation_decisions (
    id            TEXT PRIMARY KEY,           -- cd_…
    from_id       TEXT NOT NULL,              -- resource IDs, e.g. docker:i_…/container/sonarr
    rel           TEXT NOT NULL,              -- same_as, runs_on, routes_to, stores_on, monitors, depends_on
    to_id         TEXT NOT NULL,
    verdict       TEXT NOT NULL CHECK (verdict IN ('confirm', 'reject')),
    rule          TEXT NOT NULL DEFAULT '',   -- the rule that suggested it, if a suggestion did
    from_instance TEXT REFERENCES integration_instances (id) ON DELETE CASCADE,
    to_instance   TEXT REFERENCES integration_instances (id) ON DELETE CASCADE,
    created_by    TEXT REFERENCES users (id) ON DELETE SET NULL,
    created_at    INTEGER NOT NULL,
    UNIQUE (from_id, rel, to_id)
) STRICT;
CREATE TABLE api_tokens (
    id           TEXT PRIMARY KEY,         -- tok_…, the public handle the API and audit use
    user_id      TEXT NOT NULL REFERENCES users (id), -- users are disabled, never deleted
    name         TEXT NOT NULL,            -- what the owner called it, e.g. "backup script"
    token_hash   BLOB NOT NULL UNIQUE,     -- SHA-256 of the secret; the secret is shown once and never stored
    scopes       TEXT NOT NULL DEFAULT '[]', -- JSON array of authz scopes, e.g. ["docker.container.restart:host=pve-a"]
    created_at   INTEGER NOT NULL,
    expires_at   INTEGER,                  -- NULL: never; the API will default to an expiry
    last_used_at INTEGER,                  -- written at most every few minutes, like sessions
    revoked_at   INTEGER                   -- revoked tokens stay, so audit records resolve
, revoked_by TEXT, last_used_ip TEXT, kind TEXT NOT NULL DEFAULT 'api' CHECK (kind IN ('api', 'agent'))) STRICT;
CREATE TABLE plans (
    id          TEXT PRIMARY KEY,           -- p_…
    hash        TEXT NOT NULL,              -- sha256:…, over action, target, params, tier, impact and the target's state
    session_id  TEXT NOT NULL,              -- only this session may approve it
    actor_id    TEXT NOT NULL,
    actor       TEXT NOT NULL,              -- JSON: action.Actor, without step-up
    action      TEXT NOT NULL,
    target      TEXT NOT NULL,
    params      TEXT NOT NULL DEFAULT '{}', -- JSON
    tier        TEXT NOT NULL CHECK (tier IN ('T0', 'T1', 'T2', 'T3', 'T4')),
    state       TEXT NOT NULL,              -- the target's state when planned; approve re-checks it
    document    TEXT NOT NULL,              -- JSON: the plan as the API returned it
    created_at  INTEGER NOT NULL,
    expires_at  INTEGER NOT NULL,
    approved_at INTEGER
) STRICT;
CREATE TABLE tasks (
    id          TEXT PRIMARY KEY,           -- t_…
    plan_id     TEXT NOT NULL REFERENCES plans (id) ON DELETE CASCADE,
    actor_id    TEXT NOT NULL,
    state       TEXT NOT NULL CHECK (state IN ('queued', 'running', 'verifying', 'succeeded', 'failed', 'cancelled')),
    document    TEXT NOT NULL,              -- JSON: the task as the API returns it
    created_at  INTEGER NOT NULL,
    finished_at INTEGER
) STRICT;
CREATE TABLE idempotency_keys (
    actor_id   TEXT NOT NULL,
    key        TEXT NOT NULL,               -- the client's Idempotency-Key header
    plan_id    TEXT NOT NULL,
    task_id    TEXT NOT NULL REFERENCES tasks (id) ON DELETE CASCADE,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (actor_id, key)
) STRICT;
CREATE TABLE audit_events (
    seq       INTEGER PRIMARY KEY,          -- 1, 2, 3, … with no gaps
    id        TEXT NOT NULL UNIQUE,         -- a_…
    at        INTEGER NOT NULL,
    actor_id  TEXT NOT NULL DEFAULT '',
    actor     TEXT NOT NULL DEFAULT '',     -- the actor's name
    action    TEXT NOT NULL,
    target    TEXT NOT NULL DEFAULT '',
    result    TEXT NOT NULL CHECK (result IN ('ok', 'failed', 'refused')),
    event     TEXT NOT NULL,                -- canonical JSON, as hashed
    prev_hash TEXT NOT NULL,
    hash      TEXT NOT NULL
) STRICT;
INSERT INTO audit_events VALUES(1,'a_5qrt5ay7fx32zlzgxmf2ikhj27',1793934072948,'u_24lldksrl4dvqjfovmqc35uoak','owner','settings.integrations.instance.create','integration:i_s5qw64ecijrfi2duazjblufxsn','ok','{"id":"a_5qrt5ay7fx32zlzgxmf2ikhj27","seq":1,"at":"2026-11-06T03:01:12.948Z","actor":{"kind":"user","id":"u_24lldksrl4dvqjfovmqc35uoak","name":"owner","via":"web","session":"s_ee7hegy2etqcso5udjibqyojbu","ip":"127.0.0.1"},"action":"settings.integrations.instance.create","target":"integration:i_s5qw64ecijrfi2duazjblufxsn","params":{"config_fields":["url"],"enabled":false,"integration":"sonarr","name":"Sonarr","secret_fields":["api_key"],"url_host":"127.0.0.1"},"tier":"T1","result":"ok","error":null,"duration_ms":0,"task":"","prev_hash":"sha256:0000000000000000000000000000000000000000000000000000000000000000"}','sha256:0000000000000000000000000000000000000000000000000000000000000000','sha256:3f290e75c1f9fcf46506a96e8951858ad7a1c88808a0767a00b2cd045d3ac4fc');
CREATE TABLE stack_drafts (
    node       TEXT NOT NULL,              -- the node's ID
    stack      TEXT NOT NULL,              -- the compose project name
    content    TEXT NOT NULL,
    revision   INTEGER NOT NULL CHECK (revision > 0),
    updated_at INTEGER NOT NULL,
    updated_by TEXT NOT NULL DEFAULT '', source TEXT NOT NULL DEFAULT 'web',   -- the username, for the editor's "saved by"
    PRIMARY KEY (node, stack)
) STRICT;
CREATE TABLE nodes (
    id           TEXT PRIMARY KEY,               -- n_…
    name         TEXT NOT NULL,                  -- what the node calls itself (CHADASH_NODE_NAME)
    public_key   TEXT NOT NULL UNIQUE,           -- ed25519:…
    next_key     TEXT UNIQUE,                    -- ed25519:…, during a rotation
    state        TEXT NOT NULL CHECK (state IN ('pending', 'approved', 'revoked')),
    enrolled_via TEXT NOT NULL CHECK (enrolled_via IN ('local', 'token')),
    approved_by  TEXT,                           -- a user ID, or 'local-pairing'
    approved_at  INTEGER,
    revoked_by   TEXT,
    revoked_at   INTEGER,
    last_seen_at INTEGER,
    address      TEXT,                           -- the client IP it last connected from
    version      TEXT,                           -- the node binary's version
    hub_pins     TEXT NOT NULL DEFAULT '[]',     -- JSON: the hub keys the node said it trusts
    verbs        TEXT NOT NULL DEFAULT '[]',     -- JSON: the verbs it serves and whether its policy allows each
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL
) STRICT;
CREATE TABLE node_hub_keys (
    id         TEXT PRIMARY KEY,                 -- hk_…; the vault holds the private key as node/hub-key/<id>
    pin        TEXT NOT NULL UNIQUE,             -- ed25519:…
    created_at INTEGER NOT NULL,
    retired_at INTEGER
) STRICT;
INSERT INTO node_hub_keys VALUES('hk_4zb4vvettiayp3djxtxi2bjais','ed25519:0dVEM_fgnd6iATNzK28XFrELebT88MQLtgEzZvEPCgI',1793934072924,NULL);
CREATE TABLE notify_channels (
    id         TEXT PRIMARY KEY,                -- nc_…
    kind       TEXT NOT NULL CHECK (kind IN ('webpush', 'ntfy', 'webhook')),
    name       TEXT NOT NULL UNIQUE,
    enabled    INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    config     TEXT NOT NULL DEFAULT '{}',      -- JSON, never a secret
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
) STRICT;
INSERT INTO notify_channels VALUES('nc_webpush','webpush','Web Push',1,'{}',1793934072925,1793934072925);
CREATE TABLE notify_rules (
    id         TEXT PRIMARY KEY,                -- the rule's name: incident.down, …
    settings   TEXT NOT NULL DEFAULT '{}',      -- JSON: enabled, resolved, grace_minutes, channels
    updated_at INTEGER NOT NULL,
    updated_by TEXT NOT NULL DEFAULT ''
) STRICT;
CREATE TABLE notify_push_subscriptions (
    id          TEXT PRIMARY KEY,               -- ps_…
    user_id     TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    endpoint    TEXT NOT NULL UNIQUE,
    p256dh      BLOB NOT NULL,
    auth        BLOB NOT NULL,
    label       TEXT NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL,
    last_ok_at  INTEGER,
    last_error  TEXT
) STRICT;
CREATE TABLE notify_events (
    id         TEXT PRIMARY KEY,                -- ne_…
    rule       TEXT NOT NULL,
    incident   TEXT NOT NULL DEFAULT '',        -- the incident, or '' (actions, tests)
    transition TEXT NOT NULL,                   -- down, attention, repair, stuck, resolved, action
    title      TEXT NOT NULL,
    notified   INTEGER NOT NULL CHECK (notified IN (0, 1)), -- 0: recorded only (a resolution the rule keeps quiet)
    created_at INTEGER NOT NULL
) STRICT;
CREATE TABLE notify_deliveries (
    id              TEXT PRIMARY KEY,           -- nd_…
    channel_id      TEXT NOT NULL,              -- no foreign key: the log outlives a deleted channel
    channel_name    TEXT NOT NULL,
    channel_kind    TEXT NOT NULL,
    subscription_id TEXT,                       -- Web Push: the device
    events          TEXT NOT NULL DEFAULT '[]', -- JSON: the event IDs this message carries
    kind            TEXT NOT NULL,              -- the message's kind (incident.down, group, test, …)
    title           TEXT NOT NULL,
    payload         TEXT NOT NULL,              -- JSON: the message, never a secret
    state           TEXT NOT NULL CHECK (state IN ('pending', 'sent', 'failed', 'dropped')),
    attempts        INTEGER NOT NULL DEFAULT 0,
    next_at         INTEGER,                    -- when a pending delivery is tried next
    status_code     INTEGER,
    last_error      TEXT,
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL
) STRICT;
CREATE TABLE approvals (
    id            TEXT PRIMARY KEY,               -- apv_…
    plan_id       TEXT NOT NULL UNIQUE REFERENCES plans (id) ON DELETE CASCADE,
    originator_id TEXT NOT NULL,                  -- the actor that asked (tok_…); only it, and approvers, may read it
    originator    TEXT NOT NULL,                  -- JSON: action.Actor of the originator
    state         TEXT NOT NULL CHECK (state IN ('awaiting_approval', 'approved', 'denied', 'expired')),
    reason        TEXT NOT NULL DEFAULT '',       -- why it was denied or expired, a sentence
    created_at    INTEGER NOT NULL,
    expires_at    INTEGER NOT NULL,
    decided_at    INTEGER,
    decided_by    TEXT,                           -- JSON: action.Actor of the person who decided; NULL when it expired
    task_id       TEXT                            -- the task an approval started
) STRICT;
CREATE TABLE icons (
    id           TEXT PRIMARY KEY,               -- ic_…
    source       TEXT NOT NULL UNIQUE,           -- the icon URL, or the origin (scheme://host[:port])
    kind         TEXT NOT NULL CHECK (kind IN ('url', 'origin')),
    content_type TEXT NOT NULL DEFAULT '',       -- image/png, image/svg+xml, image/x-icon or image/webp
    body         BLOB,                           -- the image as served
    etag         TEXT NOT NULL DEFAULT '',       -- a hash of body
    error        TEXT NOT NULL DEFAULT '',       -- why the last fetch failed, '' after a success
    fetched_at   INTEGER,                        -- the last successful fetch
    checked_at   INTEGER NOT NULL DEFAULT 0,     -- the last attempt
    seen_at      INTEGER NOT NULL,               -- the last time a tile or an instance referred to it
    created_at   INTEGER NOT NULL
) STRICT;
CREATE TABLE stack_secret_grants (
    node       TEXT NOT NULL,              -- as in stack_drafts: the target's node
    stack      TEXT NOT NULL,              -- the compose project name
    name       TEXT NOT NULL,              -- the secret's name (vault stacks/<name>)
    granted_at INTEGER NOT NULL,           -- the first deploy that sent it
    PRIMARY KEY (node, stack, name)
) STRICT;
CREATE TABLE ip_bans (
    ip        TEXT PRIMARY KEY,           -- the client address, as netip.Addr.String() prints it
    banned_at INTEGER NOT NULL,           -- Unix milliseconds
    failures  INTEGER NOT NULL            -- wrong passwords that led to the ban
) STRICT;
CREATE TABLE imports (
    id          TEXT PRIMARY KEY,         -- imp_…
    source      TEXT NOT NULL,            -- homepage, homarr, yaml
    actor       TEXT,                     -- the user who applied it; NULL from the CLI
    created_at  INTEGER NOT NULL,         -- Unix milliseconds
    files       TEXT NOT NULL,            -- JSON array of file names
    target      TEXT NOT NULL,            -- new, none, or board:<id>
    board_id    TEXT,                     -- the board the tiles went to
    created     TEXT NOT NULL,            -- JSON: {board?, instances, secrets, items}
    counts      TEXT NOT NULL,            -- JSON: {created, updated, tiles, instances, secrets}
    reverted_at INTEGER,                  -- set when Revert ran
    reverted    TEXT                      -- JSON: what Revert removed and kept
) STRICT;
CREATE TABLE discovery_dismissals (
    key          TEXT PRIMARY KEY,
    dismissed_at INTEGER NOT NULL,        -- Unix milliseconds
    dismissed_by TEXT
) STRICT;
CREATE TABLE stack_syncs (
    id                TEXT PRIMARY KEY,          -- syn_…
    node              TEXT NOT NULL,
    stack             TEXT NOT NULL,
    url               TEXT NOT NULL,             -- no userinfo
    branch            TEXT NOT NULL,
    path              TEXT NOT NULL,             -- default '<stack>/compose.yaml'
    auth_kind         TEXT NOT NULL CHECK (auth_kind IN ('none', 'https', 'ssh')),
    host_key          TEXT,                      -- pinned SSH host key, when ssh
    mode              TEXT NOT NULL CHECK (mode IN ('deploy', 'ask')),
    poll_seconds      INTEGER NOT NULL,
    secrets_allow     TEXT NOT NULL,             -- JSON array of stack secret names
    owner_id          TEXT REFERENCES users (id) ON DELETE SET NULL,  -- NULL: owner deleted, sync paused
    state             TEXT NOT NULL CHECK (state IN ('active', 'held', 'paused')),
    state_reason      TEXT NOT NULL,
    last_seen_sha     TEXT NOT NULL,
    last_deployed_sha TEXT NOT NULL,
    rejected_sha      TEXT NOT NULL,
    last_error        TEXT NOT NULL,
    created_at        INTEGER NOT NULL,          -- Unix milliseconds
    updated_at        INTEGER NOT NULL,
    UNIQUE (node, stack)
) STRICT;
CREATE TABLE stack_hooks (
    id           TEXT PRIMARY KEY,               -- whk_…
    url          TEXT NOT NULL,                  -- normalized, no userinfo
    branch       TEXT NOT NULL,
    sender       TEXT NOT NULL CHECK (sender IN ('github', 'gitea', 'forgejo', 'gitlab', 'chadash')),
    created_by   TEXT REFERENCES users (id) ON DELETE SET NULL,
    created_at   INTEGER NOT NULL,               -- Unix milliseconds
    last_used_at INTEGER NOT NULL,               -- 0 until the first valid request
    UNIQUE (url, branch)
) STRICT;
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
CREATE TABLE node_join_tokens (
    id         TEXT PRIMARY KEY,                 -- njt_…
    hash       TEXT NOT NULL UNIQUE,             -- SHA-256 of the secret, hex
    name       TEXT NOT NULL,                    -- '': the node picks its name
    add_docker INTEGER NOT NULL CHECK (add_docker IN (0, 1)),
    created_by TEXT REFERENCES users (id) ON DELETE SET NULL,
    created_at INTEGER NOT NULL,                 -- Unix milliseconds
    expires_at INTEGER NOT NULL,
    used_at    INTEGER NOT NULL DEFAULT 0,       -- 0 until a node spends it
    revoked_at INTEGER NOT NULL DEFAULT 0,
    node_id    TEXT REFERENCES nodes (id) ON DELETE SET NULL
) STRICT;
CREATE TABLE update_holds (
    id         TEXT PRIMARY KEY,                 -- uph_…
    engine     TEXT NOT NULL,                    -- the engine resource the container runs on
    container  TEXT NOT NULL,                    -- the container's name
    kind       TEXT NOT NULL CHECK (kind IN ('hold', 'snooze')),
    digest     TEXT NOT NULL DEFAULT '',         -- snooze: the newest digest when snoozed
    until      INTEGER NOT NULL DEFAULT 0,       -- snooze: when it ends; hold: 0
    created_by TEXT REFERENCES users (id) ON DELETE SET NULL,
    created_at INTEGER NOT NULL,                 -- Unix milliseconds
    UNIQUE (engine, container)
) STRICT;
CREATE TABLE installed_specs (
    id           TEXT PRIMARY KEY,               -- isp_…
    spec_id      TEXT NOT NULL UNIQUE,           -- meta.id
    yaml         TEXT NOT NULL,
    sha256       TEXT NOT NULL,                  -- of yaml, hex
    source       TEXT NOT NULL DEFAULT '',       -- where the owner said it came from
    actions_on   INTEGER NOT NULL DEFAULT 0 CHECK (actions_on IN (0, 1)),
    installed_by TEXT REFERENCES users (id) ON DELETE SET NULL,
    installed_at INTEGER NOT NULL,               -- Unix milliseconds
    updated_at   INTEGER NOT NULL
) STRICT;
CREATE TABLE incident_events (
    id          TEXT PRIMARY KEY,                -- iev_…
    incident_id TEXT NOT NULL,
    at          INTEGER NOT NULL,                -- Unix milliseconds
    kind        TEXT NOT NULL CHECK (kind IN ('opened', 'changed', 'caused', 'acknowledged', 'expected', 'resolved')),
    data        TEXT NOT NULL DEFAULT '{}'       -- JSON object
) STRICT;
CREATE TABLE maintenance_windows (
    id         TEXT PRIMARY KEY,                 -- mw_…
    name       TEXT NOT NULL,
    hosts      TEXT NOT NULL DEFAULT '[]',       -- JSON array of resource IDs
    starts_at  INTEGER NOT NULL,                 -- Unix milliseconds
    ends_at    INTEGER NOT NULL,
    weekly     INTEGER NOT NULL DEFAULT 0 CHECK (weekly IN (0, 1)),
    created_by TEXT REFERENCES users (id) ON DELETE SET NULL,
    created_at INTEGER NOT NULL,
    CHECK (ends_at > starts_at),
    CHECK (weekly = 0 OR ends_at - starts_at <= 604800000)
) STRICT;
CREATE TABLE rollouts (
    id            TEXT PRIMARY KEY,              -- rol_…
    created_by    TEXT REFERENCES users (id) ON DELETE SET NULL,
    plan_hash     TEXT NOT NULL,
    state         TEXT NOT NULL CHECK (state IN ('scheduled', 'waiting', 'running', 'succeeded', 'failed', 'cancelled')),
    window_id     TEXT REFERENCES maintenance_windows (id) ON DELETE SET NULL,
    scheduled_for INTEGER NOT NULL DEFAULT 0,    -- Unix milliseconds; 0: at once, or when its window opens
    steps         TEXT NOT NULL DEFAULT '[]',    -- JSON array, in run order
    created_at    INTEGER NOT NULL,
    started_at    INTEGER NOT NULL DEFAULT 0,
    finished_at   INTEGER NOT NULL DEFAULT 0
) STRICT;
CREATE TABLE backup_objectives (
    resource   TEXT PRIMARY KEY,                 -- the protected resource's ID
    max_age_ms INTEGER NOT NULL CHECK (max_age_ms > 0),
    set_by     TEXT REFERENCES users (id) ON DELETE SET NULL,
    set_at     INTEGER NOT NULL                  -- Unix milliseconds
) STRICT;
CREATE TABLE update_rejections (
    id         TEXT PRIMARY KEY,                 -- upr_…
    engine     TEXT NOT NULL,                    -- the engine resource the container runs on
    container  TEXT NOT NULL,                    -- the container's name
    digest     TEXT NOT NULL,                    -- the digest it rolled back from
    reason     TEXT NOT NULL DEFAULT '',         -- why, a sentence from the update
    rollout    TEXT NOT NULL DEFAULT '',         -- the rollout it happened in, if any (rol_…)
    created_at INTEGER NOT NULL,                 -- Unix milliseconds
    UNIQUE (engine, container)
) STRICT;
PRAGMA writable_schema=ON;
CREATE TABLE IF NOT EXISTS sqlite_sequence(name,seq);
DELETE FROM sqlite_sequence;
INSERT INTO sqlite_sequence VALUES('goose_db_version',29);
CREATE TRIGGER audit_append_only BEFORE UPDATE ON audit
BEGIN
    SELECT RAISE(ABORT, 'audit is append-only');
END;
CREATE TRIGGER audit_events_no_update BEFORE UPDATE ON audit_events
BEGIN
    SELECT RAISE(ABORT, 'audit_events is append-only');
END;
CREATE TRIGGER audit_events_no_delete BEFORE DELETE ON audit_events
BEGIN
    SELECT RAISE(ABORT, 'audit_events is append-only');
END;
CREATE UNIQUE INDEX users_single_owner ON users (role) WHERE role = 'owner';
CREATE INDEX sessions_user ON sessions (user_id);
CREATE INDEX sessions_expiry ON sessions (expires_at);
CREATE INDEX audit_at ON audit (at);
CREATE INDEX passkeys_user ON passkeys (user_id);
CREATE INDEX api_tokens_user ON api_tokens (user_id);
CREATE INDEX plans_expiry ON plans (expires_at);
CREATE INDEX tasks_created ON tasks (created_at);
CREATE INDEX tasks_unfinished ON tasks (state) WHERE finished_at IS NULL;
CREATE INDEX idempotency_keys_created ON idempotency_keys (created_at);
CREATE INDEX audit_events_at ON audit_events (at);
CREATE INDEX audit_events_action ON audit_events (action, seq);
CREATE INDEX audit_events_target ON audit_events (target, seq);
CREATE INDEX audit_events_actor ON audit_events (actor_id, seq);
CREATE UNIQUE INDEX nodes_live_name ON nodes (name) WHERE state <> 'revoked';
CREATE INDEX notify_push_subscriptions_user ON notify_push_subscriptions (user_id);
CREATE INDEX notify_events_incident ON notify_events (incident, created_at);
CREATE INDEX notify_events_created ON notify_events (created_at);
CREATE INDEX notify_deliveries_due ON notify_deliveries (state, next_at);
CREATE INDEX notify_deliveries_created ON notify_deliveries (created_at);
CREATE INDEX approvals_awaiting ON approvals (expires_at) WHERE state = 'awaiting_approval';
CREATE INDEX approvals_originator ON approvals (originator_id);
CREATE INDEX imports_created ON imports (created_at DESC);
CREATE INDEX stack_syncs_remote ON stack_syncs (url, branch);
CREATE INDEX incident_events_incident ON incident_events (incident_id, at);
CREATE INDEX incident_events_at ON incident_events (at);
CREATE INDEX rollouts_scheduled ON rollouts (scheduled_for) WHERE state = 'scheduled';
PRAGMA writable_schema=OFF;
COMMIT;
