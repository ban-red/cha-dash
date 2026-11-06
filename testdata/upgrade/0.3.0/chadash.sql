PRAGMA foreign_keys=OFF;
BEGIN TRANSACTION;
CREATE TABLE goose_db_version (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		version_id INTEGER NOT NULL,
		is_applied INTEGER NOT NULL,
		tstamp TIMESTAMP DEFAULT (datetime('now'))
	);
INSERT INTO goose_db_version VALUES(1,0,1,'2026-11-06 03:01:09');
INSERT INTO goose_db_version VALUES(2,1,1,'2026-11-06 03:01:09');
INSERT INTO goose_db_version VALUES(3,2,1,'2026-11-06 03:01:09');
INSERT INTO goose_db_version VALUES(4,3,1,'2026-11-06 03:01:09');
INSERT INTO goose_db_version VALUES(5,4,1,'2026-11-06 03:01:09');
INSERT INTO goose_db_version VALUES(6,5,1,'2026-11-06 03:01:09');
INSERT INTO goose_db_version VALUES(7,6,1,'2026-11-06 03:01:09');
INSERT INTO goose_db_version VALUES(8,7,1,'2026-11-06 03:01:09');
INSERT INTO goose_db_version VALUES(9,8,1,'2026-11-06 03:01:09');
INSERT INTO goose_db_version VALUES(10,9,1,'2026-11-06 03:01:09');
INSERT INTO goose_db_version VALUES(11,10,1,'2026-11-06 03:01:09');
INSERT INTO goose_db_version VALUES(12,11,1,'2026-11-06 03:01:09');
INSERT INTO goose_db_version VALUES(13,12,1,'2026-11-06 03:01:09');
INSERT INTO goose_db_version VALUES(14,13,1,'2026-11-06 03:01:09');
INSERT INTO goose_db_version VALUES(15,14,1,'2026-11-06 03:01:09');
INSERT INTO goose_db_version VALUES(16,15,1,'2026-11-06 03:01:09');
INSERT INTO goose_db_version VALUES(17,16,1,'2026-11-06 03:01:09');
INSERT INTO goose_db_version VALUES(18,17,1,'2026-11-06 03:01:09');
INSERT INTO goose_db_version VALUES(19,18,1,'2026-11-06 03:01:09');
INSERT INTO goose_db_version VALUES(20,19,1,'2026-11-06 03:01:09');
INSERT INTO goose_db_version VALUES(21,20,1,'2026-11-06 03:01:09');
INSERT INTO goose_db_version VALUES(22,21,1,'2026-11-06 03:01:09');
INSERT INTO goose_db_version VALUES(23,22,1,'2026-11-06 03:01:09');
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
INSERT INTO users VALUES('u_adnrcsxtkx4pn3o5lqbmgskp4s','owner','$argon2id$v=19$m=19456,t=2,p=1$ccRPzNyygif6AIumUqhW0A$Z4y8erWftoSfRLH2/DQkvHputFhlLf2FW4CiYGkM1+0','owner',1793934069890,1793934069890,NULL,0,1793934069890);
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
, step_up_until INTEGER) STRICT;
INSERT INTO sessions VALUES('s_t4ci5od723kpsnj6awhqzrkgdv',x'f1d4ab50e980e34c93bda54d98e756b2c450e92563704826114795e8065c0507','u_adnrcsxtkx4pn3o5lqbmgskp4s','password',1793934069890,1796526069890,1793934069890,'127.0.0.1','Go-http-client/1.1',NULL);
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
INSERT INTO integration_instances VALUES('i_egmtatlfh5fkkmlhxxugvo4omk','sonarr','Sonarr','{"api_key":"${secret:instance/i_egmtatlfh5fkkmlhxxugvo4omk/api_key}","url":"http://127.0.0.1:9/"}',0,1793934069891,1793934069891);
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
INSERT INTO vault_secrets VALUES('sec_2g2y35pg26gfoeta5dglph2dnp','node/hub-key/hk_jj27nbxu6mal3pogzcsb6qrfkl','xchacha20poly1305/v1','a071cd8d39a5d87b',x'5643837c833dce232f84280acc7569f6bc582c485aa5496112be986a7dac7d7ba49b9df89a0117ab2225cd3051a9bebbe3e193b321c2a8dea22933ee3a110b60c7592bdb72242941',x'c4b107c943d277dccccf20facf8aec0b9e857e6d1edb1d3777e8f30e25cf9c3f913a3a9b2def20869321278d95270d4121fd3f190a4725b9131dd93ba0079ed5dc8ab22835e21e1fca38d59d604dd3bfacc12acd4db414bde45b027e129952eacdc7206ebb8bcf6abb6c1a5593945a48f64ffc99dfbf9e68bf667a8939380f6c6840a13cc7ac496dcd44c2abd3df21c852c44108f43007b926a9b1459620bb',1793934069867,1793934069867);
INSERT INTO vault_secrets VALUES('sec_mhx6ml2cyc233ackkbckz7oxjg','instance/i_egmtatlfh5fkkmlhxxugvo4omk/api_key','xchacha20poly1305/v1','a071cd8d39a5d87b',x'9a5ad256d351133272fec1068988f561c70670d137d8d0e14658e8df9275be3c3ab6c186eb2251723045051f0aaefd8aa1783f8e5a2f8a6f6dcc611df6c69081d41bc267341366ad',x'b5432a283c17048d6bf8055d0b15957316c87fa728081867293cf5e1c9425d682b4b67c8c508879ada9dd03ee4f437c55457475e775bf6c19f494a1818289da212cf15659267fcad',1793934069891,1793934069891);
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
, revoked_by TEXT, last_used_ip TEXT) STRICT;
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
INSERT INTO audit_events VALUES(1,'a_rgjh33s4l23sqfnrin33vkkyuy',1793934069891,'u_adnrcsxtkx4pn3o5lqbmgskp4s','owner','settings.integrations.instance.create','integration:i_egmtatlfh5fkkmlhxxugvo4omk','ok','{"id":"a_rgjh33s4l23sqfnrin33vkkyuy","seq":1,"at":"2026-11-06T03:01:09.891Z","actor":{"kind":"user","id":"u_adnrcsxtkx4pn3o5lqbmgskp4s","name":"owner","via":"web","session":"s_t4ci5od723kpsnj6awhqzrkgdv","ip":"127.0.0.1"},"action":"settings.integrations.instance.create","target":"integration:i_egmtatlfh5fkkmlhxxugvo4omk","params":{"config_fields":["url"],"enabled":false,"integration":"sonarr","name":"Sonarr","secret_fields":["api_key"],"url_host":"127.0.0.1"},"tier":"T1","result":"ok","error":null,"duration_ms":0,"task":"","prev_hash":"sha256:0000000000000000000000000000000000000000000000000000000000000000"}','sha256:0000000000000000000000000000000000000000000000000000000000000000','sha256:35c1ed08704c3079e102af1d85c1c5e87f1eea0acde908333cf05255901e3298');
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
INSERT INTO node_hub_keys VALUES('hk_jj27nbxu6mal3pogzcsb6qrfkl','ed25519:WRR1X06ZZ6fPgQCrfdJELA42qW2Asn95Ji26D9rEOaQ',1793934069867,NULL);
CREATE TABLE notify_channels (
    id         TEXT PRIMARY KEY,                -- nc_…
    kind       TEXT NOT NULL CHECK (kind IN ('webpush', 'ntfy', 'webhook')),
    name       TEXT NOT NULL UNIQUE,
    enabled    INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    config     TEXT NOT NULL DEFAULT '{}',      -- JSON, never a secret
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
) STRICT;
INSERT INTO notify_channels VALUES('nc_webpush','webpush','Web Push',1,'{}',1793934069868,1793934069868);
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
PRAGMA writable_schema=ON;
CREATE TABLE IF NOT EXISTS sqlite_sequence(name,seq);
DELETE FROM sqlite_sequence;
INSERT INTO sqlite_sequence VALUES('goose_db_version',23);
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
PRAGMA writable_schema=OFF;
COMMIT;
