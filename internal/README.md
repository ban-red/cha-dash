# Hub internals

The hub (`cmd/chadash`) is a single Go binary. This page maps its packages and
records the decisions that aren't obvious from the code. The architecture is
in [docs/plan/03-architecture.md](../docs/plan/03-architecture.md).

## Package map

| Package | Job |
|---|---|
| `cmd/chadash` | Entry point: `serve`, `healthcheck`, `version`, `export`, `import`, `audit verify`. Wires everything together in `newApp` |
| `cmd/chadash-node` | The node binary: `serve`, `healthcheck`, `keygen`, `show-key`, `version`; settings from `CHADASH_NODE_*`. Links the Docker client, which the hub never does (`TestHubFootprint`) |
| `internal/nodeproto` | The hub↔node wire protocol, imported by both (ADR-0014): Ed25519 keys and pins, mutual TLS 1.3 inside the WebSocket with ALPN version negotiation, length-prefixed JSON frames with a size limit, and `Session` (RPC both ways, streamed items, cancellation, keepalive pings every 30 s) |
| `internal/node` | The node runtime: the local policy file and its permission keys, the `Module`/`Verb` seam modules implement, the `Dispatcher` that enforces the policy on every verb, the identity on disk, the dial loop with pairing and key rotation, the health file |
| `internal/node/docker` | The node's Docker module: capability verbs over the Docker Engine API (moby client), with `docker.protect` and `redact_env` |
| `internal/nodes` | The hub's side of the node: `/node`, the registry of node keys (`nodes`, `node_hub_keys`), the hub's own keys in the vault, local pairing (single-use secret, regenerated on use, revoke and request), revocation, status in the Home Graph (`node:<id>`), the nodes API, and `Caller` (`Hub.Call`) for running node verbs |
| `internal/server` | HTTP routes, the middleware chain, graceful `Serve`; `sockets.go` wires auth events to the realtime hub (`SocketRecheck`, closing a session's or a user's sockets) and to the audit log |
| `internal/auth` | First-run setup, argon2id passwords, passkeys (WebAuthn), cookie sessions, step-up grants, attempt limiting, address bans after wrong passwords (`bans.go`); the users store and its invariants (one owner, disabled never deleted); API tokens (`cdt_…`, stored as a SHA-256, `RequireToken`, `TokenAccess`). `Identify` reads a session without renewing it; `StillValid` rechecks one a long-lived connection read earlier. `OnEvent` tells listeners about sign-outs, account changes, step-ups and passkey changes (the server closes sockets and audits them; `auth` imports neither) |
| `internal/authz` | Every permission decision: the five roles, the route table and `Guard` that enforces it (which routes take an API token, with which scope: `TokenRoute`), action decisions by tier and scope (`Decide`, and `Policy` for the action engine, with tokens resolved through `WithTokens`), which plans wait for a person (`NeedsApproval`, `DecideApproval`), `ActorFrom` for a request, and the users and tokens APIs; `Recorder` (changes outside the engine into the audit log, refusals included) and `audited.go` (what every unsafe route audits, or why it doesn't); `SeesWholeGraph` |
| `internal/boards` | Boards, their layouts and tile config (one revision carries both): optimistic revisions, history, label-discovery dismissals, the boards API; `Scope`, what the boards show (for the household's inbox) |
| `internal/correlate` | Identity across sources: `same_as` links to a primary, cross-source relations, causes across instances, merge suggestions and decisions. Design in its [README](correlate/README.md) |
| `internal/export` | YAML export and import of a hub with a field-level diff (boards, instances, carried settings, the security settings and the correlation decisions); secrets as placeholders or a passphrase-sealed bundle. Format in its [README](export/README.md) |
| `internal/history` | Metrics history on disk ([ADR-0031](../docs/adr/0031-history-storage.md)): samples roll up in memory into 1-minute and 15-minute points (and with `long-term`, 1-hour and 1-day), each `avg`, `min`, `max` and a count, stored as compressed chunks in `history.db`; retention, checkpoints, `Query` and the sparklines' past after a restart. Also health over time (`states`): a row per status change, `stopped` at a clean shutdown, `Since` for "down since" across restarts, `Summarize` for uptime. `SetLevel` changes the level at runtime (dropped tiers write what they have first; added ones pick up their open chunks), `Stats`, `Estimate` and `Clear` serve Settings → History, and `LoadLevel`/`SaveLevel` keep the choice in the hub's `settings`. A size limit (`SetLimit`, `LoadLimit`/`SaveLimit`, 1 GB by default): hourly retention then drops the oldest tenth of the tier holding the most data until the pages in use fit, never health rows. `SetExcluded` leaves instances' samples out (never their health), `ClearInstance` deletes one instance's series |
| `internal/icons` | The icon cache for tiles: fetches the icon URLs and favicons the owner's tiles and instances name, under SSRF rules, checks them (PNG, ICO, WebP, safe SVG, 256 KiB), keeps them in `icons` and serves them same-origin (`GET /api/icons`, `/api/icons/{id}`) |
| `internal/onboarding` | The first-run wizard's state and discovery (the compose sidecar, hosts the owner names, and the URLs of leads the web found in the graph); hub-wide lead dismissals (`dismissals.go`) |
| `internal/importer` | The shared importer pipeline: the plan shape, layout documents, the Review sheet's `Choices` (skip, target board, use existing, typed keys) and the plan hash, a transactional, idempotent apply, History (the `imports` table) and Revert. `Kit` builds plans for the simpler sources |
| `internal/importer/importhttp` | The import API every source shares: detection, the dry run, apply with the reviewed plan's hash (`409 plan_changed`), History and Revert |
| `internal/importer/homepage` | The Homepage importer: parses `services.yaml`, `docker.yaml` and `settings.yaml` into a plan. Mapping table in its [README](importer/homepage/README.md) |
| `internal/importer/homarr` | The Homarr 2 importer: reads the backup zip's `db.sqlite` read-only and maps one board. Mapping in its [README](importer/homarr/README.md) |
| `internal/importer/yamlboard` | Plain YAML: a board written by hand (`kind: Board`). Format in its [README](importer/yamlboard/README.md) |
| `internal/importer/leads` | The Discover tab's leads as a plan, so they go through the Review sheet like any import |
| `internal/integration` | The contract every integration implements: `Integration`, `Instance`, `Manifest` (config JSON Schema), `TestResult`, the facts and inbox shapes, error classes, the registry, recipes, the optional `Authorizer` and `Actor` (actions; `Params` adapts a schema to an action's params), `Manifest.Control` (control credentials: `ReadConfig`, `ControlConfig`) and recipes' control variant |
| `internal/action` | The action engine: `Def`, the `Registry` every source adds to, and the lifecycle (resolve, authorize, plan, approve, execute, verify, audit) with per-resource locks, idempotency keys, tasks and cancelling them (`Def.Cancellable`). Defines `Actor`, `Policy` and `Tier`; the hub runs `internal/authz`'s policy through `authz.Policy.ForEngine` (`OwnerOnly` remains for tests). Plans that wait for a person (approvals: `Grant`, `Deny`, expiry, an Inbox item of class `approval`). Serves the action routes, what can be planned on a target (`ActionsFor`), and the `tasks` topic |
| `internal/stacks` | Owned stacks, the hub's side: the stacks API over the nodes (`nodes.Caller`), drafts (`stack_drafts`), stack secrets in the vault (`stacks/<name>`) with per-stack grants (`stack_secret_grants`), and the `stack.deploy`, `stack.rollback`, `stack.adopt` and `stack.unadopt` actions. Never links the Compose SDK. See its [README](stacks/README.md) |
| `internal/notify` | Notifications: Web Push (VAPID, RFC 8291 encryption, per-user devices), ntfy and signed webhooks; symptom-first rules over the `inbox` topic plus every T4 action (`action.Options.OnTaskDone`); grouping, dedupe, rate limits, retries with backoff and the delivery log. Channel secrets and the VAPID key in the vault. See its [README](notify/README.md) |
| `internal/stackproto` | The stacks verbs' wire contract, shared by the hub and the node: verb names, policy permissions, request, response and progress shapes, `Error` with codes that survive a text-only transport. Standard library only |
| `internal/node/stacks` | The node's stacks module (`chadash-node` only): the stacks root as a go-git repository, validation, the hash planner, and the seven-step deploy with automatic, digest-pinned rollback through the Compose SDK. `dockerapi` is its Docker seam and `stackstest` a fake daemon. See its [README](node/stacks/README.md) |
| `internal/audit` | The append-only, hash-chained audit log: `Append`, `Query`, NDJSON `Export` (with the head in `X-Chadash-Audit-Head`), `Verify` (with the head), its routes. The canonical form, and what the chain can't show, are in its package doc |
| `internal/integration/integrationtest` | A harness for contract tests: an integration on the runtime's real HTTP clients and emitter, publishing into a private graph; `Harness.Actions` runs its actions through a real engine (plan, approve, execute, verify, audit) and `Writes` records what a fake upstream was asked to change |
| `internal/security` | The security posture (`relaxed`, `standard`, `strict`; one settings row): the severity transform for `category: security` incidents and its API |
| `internal/runtime` | Runs instances: a coordinator each, the per-instance HTTP client (TLS policy, session cache), secrets from the vault, the fact emitter, the integrations API, and `Actions`, which registers running instances' actions with the engine (control credential, routing by target, the self-aware T4, incidents' availability) |
| `internal/spec` | The Tier 1 spec engine: YAML specs with gjson extraction and expr-lang expressions; fan-out resources (`each`); actions on param-addressed targets (`actions.go`, an `integration.Actor`) |
| `integrations/<id>` | Tier 0 integrations, one folder each, with a `recipe.md` for the least-privilege credential. Linked in by `cmd/chadash/integrations.go` |
| `specs` | The bundled Tier 1 specs (`<id>.yaml`), embedded and registered at init |
| `internal/demo` | Demo mode: runs the Demo Home simulator (`sim/`) and maps it onto the topics; its incident fixes are engine actions (`Driver.Actions`), the only way to run them |
| `internal/topics` | The topic set (`graph`, `metrics`, `inbox`), their document shapes and routing; the inbox per viewer (`InboxSource`, `InboxView`: approvals only for approvers) and the household's projection of an incident (`ProjectIncident`); the graph per viewer (`GraphSource`) and the household's view of it (`HouseholdGraph`) |
| `internal/realtime` | WebSocket gateway, protocol v1 (`hello` / `sub` / `snap` / `diff` / `ping`); `GraphSource`, `DocSource`, `Mux`; `ContextSource`, `ViewerDocSource` (items filtered per viewer), `ProjectedDocSource` (items projected per viewer) and `GraphSource.View` (the graph filtered and projected per viewer); `Hub.CloseWhere`, `Options.Recheck` and `MaxLifetime`, which keep open sockets in step with their session |
| `internal/graph` | Home Graph v0: resources, relations, version, watchers |
| `internal/livedoc` | Live JSON documents for topics that aren't the graph: values, keyed collections, watchers; `Tx.Item` reads an item inside an update, so read-modify-write is atomic |
| `internal/jsonpatch` | The RFC 6902 subset used on the wire, plus `Compact` for coalescing; array appends (`add …/-`) are kept in order and never folded |
| `internal/webui` | Serves the SPA, embedded with `-tags webui`; a placeholder page otherwise. `WithInline` attaches a per-request first-paint snapshot, embedded only in `index.html`. Compressible files of 1 KiB or more are gzipped once and kept (their own ETag, `Vary: Accept-Encoding`; ranges get identity bytes); the personalised page is gzipped per request |
| `internal/firstpaint` | Builds the snapshot inlined into the first paint, for any signed-in role: user, board (layout and tiles), graph, metrics for its resources, open incidents as the role's inbox topic shows them. HTML-escaped, capped at 512 KiB |
| `internal/vault` | Envelope-encrypted secrets |
| `internal/store` | SQLite (ncruces) connections, pragmas, embedded goose migrations: `00001` schema v0, `00002` passkeys, `00003` integration state, `00004` board tiles and dismissals, `00005` correlation decisions, `00006` step-up grants on sessions and the `api_tokens` table, `00007` plans, tasks and idempotency keys, `00008` the audit log, `00009` stack drafts, `00010` node identities and the hub's node keys, `00011` notifications (channels, rules, push subscriptions, events, deliveries), `00012` API tokens' revoker and last address, and the approvals queue, `00013` the icon cache, `00014` stack secret grants, `00015` banned client addresses, `00016` import History (`imports`) and hub-wide lead dismissals (`discovery_dismissals`). `OpenHistory` opens `history.db` with its own migrations under `history/`: `00001` series and chunks, `00002` health states |
| `internal/httpx` | Client IP and scheme behind trusted proxies, `EmbeddedIPv4` (the IPv4 address inside a NAT64 or 6to4 address) and `GuardDial` (a dialer that checks IP literals before any socket and resolved names on the address dialled), the outbound address checks `notify` and `icons` share, `ProxyCheck` (notices an untrusted reverse proxy), JSON I/O, shared middleware; `GzipJSON` compresses `/api` JSON of 1 KiB or more (never auth, setup or sign-in flows, upgrades or ranges; the BREACH reasoning is at the top of `gzip.go`) |
| `internal/config` | `CHADASH_*` env and flags |
| `internal/buildinfo` | Version and commit stamped with `-ldflags` |

Dependencies point down: `cmd → server → {auth, authz, boards, demo, realtime, importer/importhttp and its sources, runtime} → {importer, integration, security, topics, graph, livedoc, vault, store} → …`. `security → authz → auth`: decisions live in `authz`, which reads identities from `auth`; `auth` never imports either.
The node binary imports `node`, `node/docker` and `nodeproto`; the hub imports `nodes` and `nodeproto`, never `node`. Integrations reach nodes only through `integration.NodeCaller` in their Deps, which `cmd/chadash` fills with `nodes.Allow(hub, nodes.IntegrationVerbs...)`: the Docker verbs the integration uses, by exact name (no exec, no stacks).
`stacks → {action, authz, nodes, stackproto, vault, store, graph, livedoc}`; `notify → {action, authz, topics, livedoc, vault, store}` (the server mounts it; `cmd/chadash` hands the engine its `TaskDone`); only the node imports `node/stacks`, which links the Compose SDK and go-git, and the hub reaches it through `stackproto`.
`integration` imports `action` (an Actor's `Def`s) and `action` imports `audit`, `auth`, `graph`, `livedoc` and `realtime`, never `integration`; the runtime, which owns instances, sits between them (`runtime.Actions`).
Integrations import only `integration`; `runtime` imports them never (they
register themselves), and `cmd/chadash` links them in.
`realtime` sees topics only through its `Source` interface; `graph` and
`livedoc` don't know about WebSockets. `demo` is the only package that imports
`sim`. Domain packages (`auth`, `vault`, `boards`) write their own SQL against
tables that `store` migrates; there is no repository layer yet.

## Building

- **`go build ./cmd/chadash`** builds the hub with a placeholder page instead of the web app. `go test ./...` needs nothing else.
- **`bun run build`** at the repo root builds the SPA into `internal/webui/dist/` (gitignored). Then **`go build -tags webui ./cmd/chadash`** embeds it. With the tag and no `dist/`, the build fails on purpose.
- **Version**: `-ldflags "-X dash.cha.sh/internal/buildinfo.Version=… -X dash.cha.sh/internal/buildinfo.Commit=…"`.

## Configuration

Precedence is flag > env > default. Nothing is required.

| Flag | Env | Default |
|---|---|---|
| `--listen` | `CHADASH_LISTEN` | `:3696` |
| `--data-dir` | `CHADASH_DATA_DIR` | `/data` |
| `--secret-key-file` | `CHADASH_SECRET_KEY_FILE` | `<data-dir>/secret.key`, generated on first boot |
| `--log-level` | `CHADASH_LOG_LEVEL` | `info` (`debug`, `info`, `warn`, `error`) |
| `--log-format` | `CHADASH_LOG_FORMAT` | `text` (or `json`) |
| `--trusted-proxies` | `CHADASH_TRUSTED_PROXIES` | none; comma-separated IPs or CIDRs |
| `--allowed-origins` | `CHADASH_ALLOWED_ORIGINS` | none; extra `scheme://host[:port]` origins |
| `--public-url` | `CHADASH_PUBLIC_URL` | none; the `scheme://host[:port]` you open Cha Dash at, e.g. `https://dash.lab.example`. Passkeys need it, over HTTPS |
| `--demo` | `CHADASH_DEMO` | off; `1`/`true` runs the Demo Home simulator |
| `--demo-at` | `CHADASH_DEMO_AT` | `local`: start at 14:05, shown as the hub's local time; a time of day (`HH:MM[:SS]`, UTC) on the demo's day; or `now` for the real clock in UTC |
| `--demo-seed` | `CHADASH_DEMO_SEED` | `1` |
| `--node-pairing-file` | `CHADASH_NODE_PAIRING_FILE` | none; where the hub writes chadash-node's pairing token (an absolute path in a volume shared with the node). Empty: no node can pair |
| `--node-pairing-from` | `CHADASH_NODE_PAIRING_FROM` | private, loopback, link-local and CGNAT ranges; comma-separated CIDRs a node may pair from, or `any`. A known node connects from anywhere |
| `healthcheck --timeout` | — | `3s` |

The demo settings are validated even when demo mode is off, so a typo shows
up before anyone turns it on.

`CHADASH_PUBLIC_URL` is normalised like an allowed origin and is trusted as
one, so it keeps working behind a proxy that rewrites `Host`. Plain `http://`
is accepted, but passkeys then stay off (they need HTTPS, or `localhost`): the
hub warns at startup, and the passkey routes say why.

The data directory holds `chadash.db` (plus `-wal`/`-shm`) and `secret.key`.
It's created `0700`; the database, its `-wal` and `-shm`, and the key file are
`0600`.

## HTTP surface

| Route | Auth | Notes |
|---|---|---|
| `GET /healthz` | none | `200 ok` if both database pools answer within 2 s, else `503`. Reveals nothing else |
| `GET /metrics` | none, or a bearer token | Only on its own listener, off by default (`CHADASH_METRICS_LISTEN`; `CHADASH_METRICS_TOKEN` adds `Authorization: Bearer`). The Prometheus text format: `chadash_build_info{version}`, `chadash_series_value{resource,metric,unit,source}` for every series with a current value (unknown and stale left out), `chadash_resource_health{resource,kind,source,status}` 1 per resource, `chadash_history_series` and `chadash_history_file_bytes` with history on, and the Go runtime (`chadash_go_goroutines`, `_heap_bytes`, `_memory_bytes`, `_gc_cycles_total`). Sorted, so scrapes are stable. Nothing else is on that listener, never pprof (09 → E9) |
| `GET /api/setup` | none | `{"required": bool}` |
| `POST /api/setup` | none (first run only) | `{"username"?,"password"}` → `201 {"user"}` + session cookie. `username` defaults to `admin`. `422` invalid input (setup stays open), `409` already done |
| `POST /api/auth/login` | none | `{"username","password"}` → `200 {"user"}` + cookie, `401` otherwise |
| `POST /api/auth/logout` | cookie (optional) | `204`; deletes the session server-side and clears the cookie |
| `GET /api/auth/session` | session | `{"user","session","step_up_until","step_up_methods"}`. `session.method` is `password` or `passkey`; `step_up_until` is RFC 3339 while a step-up grant lasts, else `null`; `step_up_methods` is what this account can step up with here: `["passkey"]`, `["password"]` or `[]` |
| `POST /api/auth/step-up/begin` | session | No body → `200 {"method":"passkey","publicKey": request options}` (the account's passkeys, user verification required) plus the ceremony cookie, or `{"method":"password"}` under the fallback. `403 passkey_required` under "passkey only" without a passkey; `409 passkeys_unavailable` when the account's passkeys can't work at this address and the password can't stand in |
| `POST /api/auth/step-up` | session | The assertion's `toJSON()`, or `{"password"}` → `200 {"until"}`, five minutes on this session. `403 step_up_failed` (wrong password or passkey), `403 passkey_required` (a password when a passkey is due), `400 ceremony_expired`, `429` |
| `POST /api/auth/passkeys/register/begin` | session | No body → `200 {"publicKey": creation options}`, plus the ceremony cookie |
| `POST /api/auth/passkeys/register/finish` | session | The new credential's `toJSON()` → `201 {"id","name"}`. `400 ceremony_expired`, `400 passkey_rejected`, `409 passkey_exists` |
| `POST /api/auth/passkeys/login/begin` | none | No body → `200 {"publicKey": request options}`, plus the ceremony cookie |
| `POST /api/auth/passkeys/login/finish` | none | The assertion's `toJSON()` → `200 {"user"}` + session cookie. `400 ceremony_expired`; `401 passkey_rejected`; `401 unknown_passkey` with `"signal":{"rpId","credentialId"}` |
| `GET /api/auth/passkeys` | session | `[{"id","name","created_at","last_used_at","rp_id","backed_up","clone_warning"}]`, oldest first. `last_used_at` is `null` until first use |
| `PATCH /api/auth/passkeys/{id}` | session | `{"name"}` → `200` the passkey. `422` empty, over 64 characters or control characters; `404` |
| `DELETE /api/auth/passkeys/{id}` | session | `204`. `404`; `409 last_sign_in_method` if it's the only way left to sign in |
| `GET /api/version` | session | `{"version","commit"}` |
| `GET /api/boards` | session | `{"boards":[{"id","name"}]}`. The first call creates a `Home` board |
| `GET /api/boards/{id}/layout` | session | `{"revision","format_version","layout","tiles"}`. A board never saved gives `{"revision":0,"format_version":0,"layout":null,"tiles":{}}`; the SPA then creates the first layout. `tiles` is per layout item id: `name`, `icon`, `href` (http(s) only), `description`, `chromeless`, `bind {instance, check, resource, sources}`, `options`, `origin` (`internal/boards/tiles.go`). `404` unknown board |
| `PUT /api/boards/{id}/layout` | admin | `{"revision","format_version","layout","tiles"?}`, where `revision` is the one the edit started from → `200 {"revision": n+1}`. Missing or `null` `tiles` carry the latest over; `{}` clears them. Stale → `409 {"error":{"code":"conflict",…},"revision": current}`. `422` if `revision` < 0, `format_version` < 1, `layout` isn't an object, or a tile is invalid. Up to 1 MiB |
| `GET /api/boards/{id}/discovery` | viewer | `{"dismissed":[resource id]}`: label-discovered services the owner said no to |
| `POST /api/boards/{id}/discovery/dismiss` | admin | `{"resources":[…]}` (1–200) → `{"dismissed":[…]}`. Idempotent; each board keeps its newest 1000 |
| `POST /api/boards/{id}/discovery/restore` | admin | `{"resources":[…]}` (1–200) → `{"dismissed":[…]}`, what's still dismissed: Undo on a dismissal, and edit mode's "offer again". Restoring one that isn't dismissed changes nothing. `404` unknown board, `422` invalid |
| `POST /api/import/detect` | admin | `{"files":[{"name","head","base64"?}]}` (1–32 files, each head at most 64 KiB) → `{"source","name","files":{name: role},"unused","missing","roles","message"?}`. `source` is `homepage`, `homarr`, `yaml`, `chadash` (a Home file: Backup restores it), `homarr_legacy` (not importable; the message says how) or `""` |
| `POST /api/import/{homepage,homarr,yaml,discovery}?dry_run=1` | admin | `{"files":{role: text or base64},"names"?:{role: file name},"choices"?}` → `200 {"plan","hints","unmapped","plan_hash"}`. Homepage also takes its roles at the top level (`{"services_yaml",…}`). `choices` is `{"skip":["instance:<id>"\|"tile:<item id>"],"target":"new"\|"none"\|"board:<id>","use_existing":{planned id: instance id},"secrets":{planned id: {field: value}}}`. `changes` rows carry `existing` and `match` (`url`, `name`) when the hub has a lookalike. Writes nothing. `422 invalid_config` names the file and line; `422 invalid_choices`. Up to 1 MiB (Homarr: 32 MiB, the zip as base64; discovery: 256 KiB) |
| `POST /api/import/{homepage,homarr,yaml,discovery}` | admin | The same body plus `"plan_hash"` → `200 {"import_id","board_id","instance_ids","changes"}`, applied in one transaction. `409 plan_changed` with `"result"`, the fresh dry run, when the plan differs from the reviewed one. Re-importing the same files with the same choices changes nothing and adds no History row. Audited as `import.<source>` |
| `GET /api/import/{homepage,homarr,yaml,discovery}` | admin | `{"record"}`: that source's last import's links, discovery entries and instance ids (its tiles live in the board). `404` before the first import |
| `GET /api/imports`, `GET /api/imports/{id}` | admin | History, newest first (at most 200): `{"imports":[{"id","source","actor","created_at","files","target","board_id","created":{"board"?,"instances","secrets","items"},"counts","reverted_at"?,"reverted"?}]}`. File names only, never contents |
| `POST /api/imports/{id}/revert?dry_run=1` | admin | `{"remove":[{"kind","id","name"}],"keep":[{…,"why"}],"connections"}`: what Revert would remove (what the import created and nobody changed since) and keep |
| `POST /api/imports/{id}/revert` | admin, step-up when it removes connections | Runs it → the same shape. `403 step_up_required`; `409 reverted`; `404`. Audited as `import.revert` |
| `GET /api/export` | admin | The hub as YAML (`kind: Home`), as an attachment: boards with layouts and tiles, instances (secrets as `!secret` placeholders), `import/*` settings, the security settings and the correlation decisions. `409 secret_in_plain_text` if a stored secret appears in a plain value |
| `POST /api/export` | admin | `{"passphrase"}` (12+ characters) → the same with a sealed `secrets_bundle` (scrypt + XChaCha20-Poly1305). `422 weak_passphrase` |
| `POST /api/import/chadash?dry_run=1` | admin | `{"yaml","passphrase"?}` → `{"plan"}`: per object create/update/unchanged/stale with field-level changes, `secrets_needed`, the bundle's state. Never a secret value. `422 invalid_export`, `wrong_passphrase`, `bad_bundle`; `413` over 6 MiB |
| `POST /api/import/chadash` | admin | The same body → `{"plan","changed","board_ids","instance_ids"}`, in one transaction. Nothing is deleted; re-importing an export of the same hub changes nothing. The security settings import only for the owner (and the step-up rule only with a step-up grant), else they're `skipped` with a `security_skipped` warning; see [export](export/README.md) |
| `GET /api/integrations/catalog` | admin | `[{"id","name","icon","version","tier","category"?,"capabilities","config_schema","recipe"?}]`, by id. `config_schema` is JSON Schema; secret fields are top-level strings with `"writeOnly": true`, and `"x-chadash-authorize": true` marks one a sign-in flow can fill. Form-only hints: `"x-chadash-when": {"<sibling>": <value>}` shows a property only while a sibling holds that value (`Schema.ShowWhen`), and `"x-chadash-item-test": true` on an array of objects offers Test per item. `recipe` is the least-privilege credential: `{title, summary, access: read-only\|scoped\|full\|none, params, steps, notes, fills, grants}`; parameters have anchored patterns, so a generated command can't carry a shell metacharacter; a parameter bound to a form field (`from: config.<field>`) may have options, each picked by the field's value (`when`), as Unraid's mode picks its scope list |
| `GET /api/integrations/instances` | admin | `[instance]`, by integration and name. An instance is `{"id","integration","name","enabled","status","version","last_ok","last_error","last_error_code","last_error_at","warnings","config","secrets","created_at","updated_at"}`: `status` is `ok`, `degraded`, `repair`, `error` or `disabled` (an instance missing only a secret is `repair`, with a Repair naming the field); `last_error` is the last failure's message (or `null`), with its stable code and time beside it; `warnings` are `[{"code","severity","message"}]`; `config` never holds a secret or a `${secret:…}` reference; `secrets` is `{"<field>":{"set":bool}}` for every write-only field |
| `POST /api/integrations/instances` | admin | `{"integration","name","config","secrets","enabled"?}` → `201 {"id"}`. `422 invalid` with `"fields":[{"path","message"}]` (JSON Pointers into the config), `422 unknown_integration`, `409 conflict` for a name the integration already has |
| `GET /api/integrations/instances/{id}` | admin | One instance. `404` |
| `PUT /api/integrations/instances/{id}` | admin | `{"name"?,"config"?,"secrets"?,"enabled"?}` → `200` the instance. `config` replaces the whole non-secret config (send `tls.pin` back to keep a TOFU pin); in `secrets` a string sets a field, `null` or `""` clears it, and fields left out keep their value. Restarts the instance |
| `DELETE /api/integrations/instances/{id}` | admin | `204`. Removes the row, its facts, and its vault entries no other instance references |
| `POST /api/integrations/test` | admin | `{"integration","config","secrets"}`, or unsaved edits of a saved instance: `{"instance_id","config"?,"secrets"?}` (secrets not sent come from the vault) → `200 {"ok","version","latency_ms","grants","warnings","error","certificate"?}`. Saves nothing. `grants` are `[{"name","access":"read\|write\|rw\|admin","write","detail"?}]`; `error` is `{"code","message"}` or `null`; `certificate` is `{"sha256"}` of the leaf the server presented |
| `POST /api/integrations/instances/{id}/test` | admin | The same result for the saved config. In `tofu` mode with no pin yet, the certificate seen is pinned. A running instance then syncs at once, as Check now does, so a fixed connection clears its status and problems |
| `POST /api/integrations/instances/{id}/refresh` | viewer | `202`: the instance syncs now with `Interest.Refresh` set (Check now); the result arrives on the usual topics. `404`; `409 not_running` when it's disabled or can't start. Not audited: it changes nothing |
| `POST /api/integrations/authorize/{integration}` | admin | Start a sign-in flow for an integration with an `Authorizer` (Plex's PIN) → `200 {"id","code","url","expires_at","interval_s"}`; 422 `no_sign_in` without one. Flows live in memory (at most 32, 30 minutes each) and always verify TLS |
| `GET /api/integrations/authorize/{integration}/{id}` | admin | `{"status":"pending"\|"expired"\|"failed"\|"done"}`; `done` carries `secrets` (write-only fields only) exactly once, then the flow is forgotten |
| `GET /api/integrations/instances/{id}/diagnostics` | admin | A bundle for bug reports, as an attachment: hub and manifest versions, the instance (secrets as `{"set"}`), schedule, TLS mode and pin, fact counts and the last 20 attempts. No secret values |
| `GET /api/correlate/links` | viewer | Every published cross-source link with its rule, confidence (`exact`, `strong`, `confirmed`) and evidence |
| `GET /api/correlate/suggestions` | viewer | Weak matches awaiting the owner; they're also `class: "suggestion"` incidents on the `inbox` topic |
| `POST /api/correlate/suggestions/{id}/confirm`, `…/reject` | admin | Answer one; the decision persists and the incident resolves |
| `GET`, `POST /api/correlate/decisions`, `DELETE …/{id}` | viewer reads, admin changes | List, make (merge or split any pair) or undo decisions. They go when either instance is deleted |
| `GET`, `PUT /api/onboarding` | admin | The wizard's state; the hub computes `show` (a new hub with no instances, never in demo mode) |
| `POST /api/onboarding/discover`, `POST /api/discover` | admin | `{"sidecar"?,"docker"?,"hosts"?,"urls"?}`: probes the compose sidecar, Docker endpoints the owner ticked, named hosts (at most 8 each), and leads' URLs (`[{"integration","url"}]`, at most 16: one GET on each, plus the recipe's hint path). A 20 s budget, GET only, no ranges |
| `GET /api/discover/dismissed` | admin | `{"dismissed":[key]}`: leads someone said no to, hub-wide. A key is `<integration>\|<url or resource id>` |
| `POST /api/discover/dismiss`, `POST /api/discover/restore` | admin | `{"keys":[…]}` (1–200) → `{"dismissed":[…]}`. Idempotent; the newest 1000 are kept. Audited as `discover.dismiss` and `discover.restore` |
| `GET /api/settings/security` | viewer | `{"posture":"relaxed","levels":[{"id":"relaxed","label":"Relaxed · LAN-only home network","description":"…","default":true},{"id":"standard",…},{"id":"strict",…}]}`, least interrupting first. `posture` is `relaxed` until the owner changes it. Also `"step_up":"passkey_or_password"` (the default) or `"passkey_only"`, and `step_up_modes` described the same way. `notes`, when present: deployment problems the hub noticed since it started, `[{"code":"untrusted_proxy"\|"plain_sign_in","message"}]` |
| `GET /api/settings/history` | admin | `{"level","locked","demo","max_bytes","trimmed_at","series","resources","file_bytes","tiers":[{"tier":"1m"\|"15m"\|"1h"\|"1d","step_ms","keep_ms","chunks","bytes","oldest"}],"estimates":{"standard","long-term"}}`: the recording level and what `history.db` holds. `keep_ms` 0 is forever; `bytes` is chunk data before SQLite's overhead; `estimates` is how big the file grows at each level with the series so far (at least 300). `max_bytes` is the size limit (0: none; 1 GB until an admin sets one), `trimmed_at` when retention last dropped points to keep under it (Unix ms, or null). `instances` is `[{"id","series","bytes"}]` per instance with history, most series first; `excluded` the instances whose metrics aren't recorded. `locked` with `CHADASH_HISTORY=off` or in demo mode (`demo`), when there's no recorder |
| `PUT /api/settings/history` | admin | `{"level"?:"off"\|"standard"\|"long-term","max_bytes"?,"exclude"?:[instance IDs]}` (at least one; `exclude` replaces the list, at most 500, kept in `history.exclude`) → `200` the same shape. Both apply at once and are kept in `settings` (`history.level`, `history.max_bytes`); the level wins over `CHADASH_HISTORY` at the next start. `max_bytes` is 0 (no limit) or at least 64 MB. `422 nothing_to_change`, `bad_level`, `bad_limit`, `bad_exclude`; `409 history_locked`. Audited as `settings.history.update` with each value's before and after |
| `POST /api/settings/history/clear[?instance=<id>]` | admin | Deletes every point and health row → `200` the same shape. Each resource's current health is written again, so uptime starts over rather than going blank. With `instance`, only that instance's series and points; health stays. `409 history_locked`. Audited as `settings.history.clear` (target `settings:history`, or `integration:<id>`) |
| `PUT /api/settings/security` | owner | `{"posture"?,"step_up"?}` (at least one) → `200` the same shape. `422 invalid` for an unknown value; both are checked before either changes. Changing `step_up` is T3: `403 step_up_required` without a step-up grant. Every open security finding is re-published under a new level before it answers |
| `GET /api/users` | admin | `[{"id","username","role","disabled","has_passkey","last_seen_at"}]`, the owner first. `last_seen_at` is the latest of the user's live sessions, or `null` |
| `POST /api/users` | admin | `{"username","password","role"}` → `201` the user. `role` is `admin`, `operator`, `viewer` or `household`; `owner` is `422` (transfer instead). `409` for a taken username |
| `PUT /api/users/{id}` | admin | `{"role"?,"disabled"?}` → `200` the user. Disabling ends the user's sessions at once. Only the owner may touch the owner's account (`403`), and even they can't demote or disable it (`409 owner_protected`); `owner` as a role is `422` |
| `GET /api/bans` | admin | `[{"ip","banned_at","failures"}]`, newest first: addresses banned after five wrong passwords in a row |
| `DELETE /api/bans/{ip}` | admin | Lifts a ban → `204`. `404` when the address isn't banned, `422` when it isn't an address. Audited as `security.ban.clear` |
| `POST /api/users/{id}/transfer-ownership` | owner | No body → `200` the new owner; the old owner becomes an admin in the same transaction. T3: `403 step_up_required` without a step-up grant. `422` for a disabled target |
| `GET /api/demo` | session | Demo mode only: `{"enabled":true,"now":RFC 3339,"seed":n}`. `now` is the simulated time. The Demo Home's fixes are engine actions (below); there's no demo-only fix route |
| `POST /api/actions/plan` | session; the policy decides | `{"action","target","params"?}` → `200` a plan: `{"id","hash","action","label","target","params","tier","confirm","confirm_text"?,"step_up","steps","impact","undo","expires_at"}` (2 minutes; secret params as `[redacted]`). `403 forbidden`, `404 not_found` (action or target), `409 precondition`, `409 unavailable` with `"recipe"`, `422 invalid` with `"fields"`. Every refusal is audited |
| `POST /api/plans/{id}/approve` | session (or API token) that made the plan | `{"hash","typed"?}`, optional `Idempotency-Key` header (≤ 128 bytes; a retry gets the same task) → `202 {"task"}`, or `202 {"task": null, "approval"}` when the plan waits for a person (a token's T2; see [API](#api)). `403 step_up_required`, `403 forbidden` (another session), `410 plan_expired` (also when it already ran), `409 plan_changed` (hash, or the target's state), `422 confirm_mismatch`, `409 locked`, `409 precondition` |
| `GET /api/tasks/{id}` | session | The task: `{"id","plan","action","label","target","actor":{"kind","name"},"state","step"?,"progress"?,"log"?,"started_at","finished_at","error","undo","audit","cancellable"?,"reads_data"?}`. Your own, or anyone's with the `tasks.all` scope; otherwise `404` |
| `POST /api/tasks/{id}/cancel` | session; the policy decides | No body → `202 {"task"}`: its context is cancelled, and it ends `cancelled` on the `tasks` topic. Your own task, or anyone's with `tasks.all`; otherwise `404`. `409 not_cancellable` when its action doesn't allow it (`Def.Cancellable`, false by default), `409 finished` when it has ended. Every attempt is audited as `task.cancel` (T0, the task's target, `{"task"}` as params) |
| `GET /api/actions?target=` | session; the policy decides | `[{"id","label","tier","available","unavailable"?,"recipe"?,"household"?}]`, by label: what the caller could plan on that resource. Left out: actions that don't apply to it, that the policy refuses the caller, or whose precondition refuses ("already stopped"). An unavailable one (no control credential, or a state read that failed or took longer than 3 s) is listed with `available: false` and its sentence. Reads only; nothing is audited. `422 invalid` without a target |
| `GET /api/audit` | `audit.read` (admin and owner) | `?actor=&action=&target=&result=&since=&before=&limit=&cursor=` → `{"events","next"?}`, newest first. `action` ending in `.` or `*` is a prefix; `since`/`before` are RFC 3339; `limit` 1–500 (default 100); pass `next` back as `cursor`. `422 invalid` otherwise |
| `GET /api/audit/export` | `audit.read` | Every event as NDJSON, oldest first, as an attachment: each line the stored canonical JSON plus `hash`. The chain's head as of the export is in `X-Chadash-Audit-Head: seq=<n> hash=sha256:…` (none for an empty log) |
| `GET /api/audit/verify` | `audit.read` | `{"ok","checked","first_break"?:{"seq","id"?,"reason"},"head"?:{"seq","hash"}}`; `head` is the last event that checks out (the chain's head when `ok`), for the owner to record elsewhere |
| `GET /api/graph` | session, or a token with `graph.read` | `{"version","resources","relations"}`: the Home Graph as the caller's role sees it on the `graph` topic (household: `topics.HouseholdGraph`) |
| `GET /api/history?series=<key>&from=&to=&points=` | session, or a token with `graph.read` | `{"level","series":{"<resource>#<metric>":{"unit","start","step","avg","min","max"}}}`: up to 20 series from `internal/history`, in the finest tier that gives at most `points` (default 300, at most 2,000). Times are Unix ms; point i is at `start + i × step`, `null` a gap. `from` defaults to a day before `to`, `to` to now. Series nobody recorded are left out, and household gets only those of resources its graph shows. With history off, `level` is `off` and `series` is empty |
| `GET /api/history/states?resource=<id>&from=&to=` | session, or a token with `graph.read` | `{"level","states":[{"at","status"}],"totals":{"<status>":ms},"up"}`: a resource's health changes over the range (default the last week), the one in effect at `from` first, moved to `from`. `totals` runs up to now; `up` is the share of watched time it was ok or attention, `null` when none was. `stopped` is the hub not running, `gone` the resource out of the graph. Household gets nothing for resources its graph doesn't show |
| `GET /api/inbox` | session, or a token with `inbox.read` | `{"now","incidents"}`: the Inbox as the caller's role sees it on the `inbox` topic |
| `GET /api/tokens` | session (any role) | `{"tokens":[{"id","user_id","username","name","scopes","created_at","expires_at","last_used_at","last_used_ip"?,"revoked_at","revoked_by"?}]}`, newest first: your own; the owner, everyone's; admins, everyone's but the owner's. Never a secret |
| `POST /api/tokens` | session (any role) | `{"name","scopes","expires_in_days"?}` (1–365, default 90) → `201 {"token","secret"}`, the only time the secret is shown (never compressed). `422 invalid` for no scopes, more than 50, a malformed one, one your role can't use (`audit.read` below admin) or `approvals.decide`. Audited as `tokens.create`, refusals included |
| `DELETE /api/tokens/{id}` | session (any role) | → `200 {"token"}`, revoked at once; your own, any for the owner, any but the owner's for admins (`404` otherwise, audited as a refusal). `409 already_revoked`. Withdraws the plans it left waiting. Audited as `tokens.revoke` |
| `GET /api/tokens/scopes` | session (any role) | The scope picker for your role: `{"reads","families","actions","all","default_days","max_days","approval"}`; each offer has `available` and, when not, `unavailable` (a sentence) |
| `GET /api/approvals` | admin; the policy decides | `{"approvals":[Approval]}`: awaiting first, then the last 24 hours, at most 50 |
| `GET /api/approvals/{id}` | session, or the token that asked | One Approval (see [API](#api)). Anyone else: `404` |
| `POST /api/approvals/{id}/approve` | admin, session only | `{"hash","typed"?}` → `202 {"task"}`, the task running as the originator with `approved_by`. `403 step_up_required` without a step-up made after the request, `403 forbidden`, `409 plan_changed` (hash; or the target changed, which also voids the approval), `409 already_decided`, `410 approval_expired`, `409 locked`. Audited as `approval.approve` |
| `POST /api/approvals/{id}/deny` | admin, session only | `{"reason"?}` → `200 {"approval"}`. No step-up. `409 already_decided`, `410 approval_expired`. Audited as `approval.deny` |
| `GET /node` | node key | chadash-node's channel: a WebSocket carrying mutual TLS 1.3, where the node proves its pinned key; no session, and no person's role admits it (`authz.NodeKey`). The registry decides; local pairing enrolls the compose network's node with a single-use secret, from `CHADASH_NODE_PAIRING_FROM` only, and never under the name of a live node (`conflict`). The hello is read under a 64 KiB frame limit; four handshakes per address and 64 in all may be in progress. `429` after five failed handshakes (bad secrets, unknown or revoked keys, malformed hellos) from an address in a minute |
| `GET /api/nodes` | admin | `{"nodes":[{"id","name","public_key","fingerprint","state","enrolled_via","approved_by","approved_at","revoked_by","revoked_at","last_seen_at","address","version","hub_pins","verbs","created_at","connected","hub_key_current"}],"hub_pin","pairing"}`. Never a secret |
| `POST /api/nodes/{id}/revoke` | admin | → `200` the node, revoked; its live session ends at once and its key never works again. The pairing secret is regenerated too. `404`, `409 already_revoked`. Audited as `nodes.revoke` |
| `POST /api/nodes/pairing/regenerate` | admin | → `204`: a fresh pairing secret in the pairing file; the old one stops working. The secret never appears in the response. `409 pairing_off`. Audited as `nodes.pairing.regenerate` |
| `GET /api/stacks` | viewer | Owned stacks per node: `{"nodes":[{node,name,online,error,root,stacks,detected}]}`. Each stack has its status, location, drift, protected, draft and last commit; `detected` lists running projects that could be adopted in place. See [`stacks/README.md`](stacks/README.md) |
| `GET /api/stacks/{node}/{stack}` | admin | One stack: the committed file, `.env` key names (never values), `x-chadash`, services, history, draft, referenced secrets with `set`. `503 node_unavailable` when the node is offline |
| `PUT /api/stacks/{node}/{stack}` | admin | `{content, revision}` saves a draft (never deployed by saving) → `{draft}`. `409 conflict` with `revision` when stale; `409 protected` for Cha Dash's own stack |
| `DELETE /api/stacks/{node}/{stack}/draft` | admin | Discard the draft → `204` |
| `POST /api/stacks/{node}/{stack}/validate` | admin | `{content}` → `{ok, messages, secrets, plan, diff}`: the node's compose-go validation, the vault's secrets, the hash plan and a line diff. Saves nothing |
| `GET /api/stacks/secrets`, `PUT`/`DELETE /api/stacks/secrets/{name}` | admin | Stack secrets (`stacks/<name>` in the vault): names only out, `{value}` in; changes audited without the value |
| `GET`, `POST /api/notify/channels`, `PUT`/`DELETE …/{id}`, `POST …/{id}/test` | admin | Notification channels: Web Push (built in), ntfy, webhooks. Secrets as `{set}`; a test sends one now. See [`notify/README.md`](notify/README.md) |
| `GET /api/notify/rules`, `PUT …/{id}` | admin | The five symptom-first rules: on/off (critical actions always on), resolutions, the attention grace, channels |
| `GET /api/notify/deliveries?state=&limit=` | admin | The delivery log, newest first |
| `GET /api/notify/push/key`, `GET`/`POST /api/notify/push/subscriptions`, `DELETE …/{id}`, `POST /api/notify/push/unsubscribe` | session | The VAPID public key and your own devices' push subscriptions, whatever your role |
| `GET /api/icons` | session | `{"by_icon":{<tile icon URL>: path},"by_origin":{<origin>: path},"by_instance":{<instance id>: path}}`, where a path is `/api/icons/<id>?v=<etag>`: the icons the hub has fetched for what tiles and instances name. Also asks the fetcher for a pass. See `internal/icons` |
| `GET /api/icons/{id}` | session | The image, with its checked type, `ETag`, `Cache-Control: private` (a year with the current `?v=`, else a week), `Content-Security-Policy: default-src 'none'; style-src 'unsafe-inline'; sandbox`; `304` for a matching `If-None-Match`; `404` otherwise |
| `GET /ws` | session | WebSocket; see [`realtime/protocol.go`](realtime/protocol.go) and the topics below. Closed with `1008` when its session ends or changes (sign-out, disabled, a role change, ownership handed over or expired, rechecked every minute), and with `1000 reconnect` after 12 hours |
| `/api/*` (other) | — | JSON `404`; so are the demo routes outside demo mode |
| `GET /…` | none | The web app. Unknown paths get `index.html` (history-mode routing). For any signed-in session, `index.html` carries the first-paint snapshot as `<script type="application/json" id="chadash-snapshot">` (non-executable, so the CSP holds), served `no-store` with `Vary: Cookie`, its inbox as that role's inbox topic shows it; signed out, the generic shell (`no-cache`, ETag) |

Errors are `{"error":{"code","message"}}`. Attempts are limited per client IP:
10, then one every 6 s, answered with `429` and `Retry-After`. Password
sign-in and passkey `login/finish` draw on one budget; `/setup` and passkey
`login/begin` each have their own. Every passkey `begin` and `finish` answers
`409 passkeys_unavailable`, with a message saying what to change, when
passkeys can't work at the address in use. Step-up has its own budget too, so
a fumbled confirmation can't lock anyone out of signing in.

**Roles.** The Auth column is the least a route admits: `session` is any
signed-in role, household included; `viewer` is everything but household;
then `operator`, `admin`, `owner`. The table that decides is
[`authz/routes.go`](authz/routes.go): `authz.Guard` sits in front of the whole
mux (inside the cross-origin check), looks up the pattern each request
matched, and answers `401` without a session or `403 forbidden` with a
sentence for the wrong role. A route with no row is refused with `500
no_authz_rule` and an error in the log, and `authz`'s tests fail on it, so
**a new route needs a row before it answers anyone**. Handlers then only
attach the session (`auth.Require`, which passes straight through when Guard
already ran it).

**API tokens.** A request with `Authorization: Bearer cdt_…` is
authenticated by the token alone, only on `/api/`, and only on rows whose
`Token` column admits it: a read scope, or any token for the action routes.
Every other route is session only and answers a token `403 session_only`. The
Auth column above names the scope where a token can call a route. See
[API](#api).

Passkey options and credentials are the WebAuthn JSON forms, with binary fields
as unpadded base64url. The SPA hands `publicKey` to
`PublicKeyCredential.parseCreationOptionsFromJSON` (or
`parseRequestOptionsFromJSON`), calls `navigator.credentials.create`/`get`, and
posts the credential's `toJSON()` to `finish` as is. `@simplewebauthn/browser`
produces the same shapes. Autofill (`mediation: "conditional"`) needs nothing
from the hub: it uses the same `login/begin` options.

## API

Scripts and automations call the same `/api/` routes as the web app, with an
**API token** instead of a session. The public OpenAPI document trails in
v0.2.x; until then this section and the HTTP table above are the reference.

**Making one.** Settings → API tokens (or `POST /api/tokens` from a signed-in
session) with a name, the scopes and an expiry in days (1–365, 90 by
default). The answer carries the secret once:

```jsonc
// POST /api/tokens  {"name": "backup script", "scopes": ["graph.read", "docker.container.*"], "expires_in_days": 90}
{ "token": { "id": "tok_k3v…", "user_id": "u_…", "username": "sam", "name": "backup script",
             "scopes": ["graph.read", "docker.container.*"], "created_at": "…", "expires_at": "…",
             "last_used_at": null, "revoked_at": null },
  "secret": "cdt_k3v…_ABC…" }      // shown this once; the hub keeps its SHA-256
```

**Using it.** `Authorization: Bearer cdt_…` on `/api/` routes only. The token
alone counts: a cookie sent with it is ignored, and another bearer token (an
identity proxy's) or another scheme (a proxy's Basic auth) isn't ours, so it
isn't read at all. Bad tokens answer `401 invalid_token` (or, when the secret
matched, `token_expired` / `token_revoked`); ten in a row from one address,
then one every 6 seconds, then `429`.

```sh
TOKEN=cdt_aaaaaaaaaaaaaaaaaaaaaaaaaa_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA   # fake
HUB=https://dash.lab.example

curl -H "Authorization: Bearer $TOKEN" $HUB/api/graph          # graph.read
curl -H "Authorization: Bearer $TOKEN" $HUB/api/inbox          # inbox.read
curl -H "Authorization: Bearer $TOKEN" "$HUB/api/audit?limit=20"   # audit.read (admin or owner)
```

**Scopes.** A token acts as its user, never above the user's *current* role
(a demotion applies at once), narrowed to its scopes:

| Scope | What it opens |
|---|---|
| `graph.read` | `GET /api/graph`, `GET /api/history`, boards and their layouts, discovery dismissals, correlation links, suggestions and decisions, the stacks list, the demo clock |
| `inbox.read` | `GET /api/inbox`, as the user's role sees it (household: its projection) |
| `audit.read` | the audit log, its export and verification (admin and owner only) |
| `tasks.all` | other people's and tokens' tasks, not only the token's own (admin and owner only) |
| an action ID or pattern | planning that action: `docker.container.restart`, `proxmox.guest.*`, `stack.*`, optionally qualified (`proxmox.guest.start:target=proxmox:i_x/qemu/201`) |
| `*` | every read and every action the user's role allows |

Planning, approving its own plans, reading its own tasks and cancelling them,
listing a target's actions (`GET /api/actions`) and polling its approvals take
no read scope: the engine decides per action. **Session only**, whatever the
scopes: signing in, step-up, passkeys, `GET /api/auth/session`, users,
tokens, every setting, integrations, import and export, nodes, stack files,
drafts and secrets, and `/ws`; a token gets `403 session_only` (or `401` off
`/api/`). The route table's `Token` column decides
([`authz/routes.go`](authz/routes.go); `authz.TokenRoute`), and
`server.TestRouteMatrix` calls every row with tokens too.

**Actions.** A token plans like the web app does, and approves its own plan:

```sh
# plan
curl -s -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"action":"docker.container.start","target":"docker:i_x/c/sonarr"}' $HUB/api/actions/plan
# → {"id":"p_…","hash":"sha256:…","tier":"T1",…}
curl -s -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"hash":"sha256:…"}' $HUB/api/plans/p_…/approve
# T0, T1 → 202 {"task": {…}}; then GET /api/tasks/t_… until it has finished
```

- **T0 and T1** run when the token approves its plan. The plan is bound to the
  token that made it (`plans.session_id` is `token:<id>`), as a session's is to
  its session.
- **T2** waits for a person: approve answers `202 {"task": null, "approval":
  {"id": "apv_…", "state": "awaiting_approval", "plan": {…}, "originator":
  {"kind": "token", "id": "tok_…", "name": "backup script (sam)", …},
  "requested_at", "expires_at", "decided_at", "decided_by", "reason"?, "task"}}`.
  Asking again returns the same approval in its current state. An Inbox item of
  class `approval` (for admins and the owner only) shows who asked, the plan's
  steps and impact. A person approves it in a session (`POST
  /api/approvals/{id}/approve {"hash"}`) with a step-up made after the request,
  so the confirmation is for this plan; or denies it (`…/deny {"reason"?}`, no
  step-up). The task then runs as the token, with `approved_by` naming the
  person. The token polls `GET /api/approvals/{id}`, then `GET /api/tasks/{task}`.
  Unanswered, it expires after an hour; revoking the token withdraws it; a
  target that changed, or a token that lost the scope or its role, voids it at
  approval time (`expired`, with the reason).
- **T3 and T4** are refused at planning (`403 forbidden`): a token can't step up.

Every step is audited: `approval.request` by the token, `approval.approve`
and `approval.deny` by the person (refusals included), `approval.expire` by
the hub, and the action itself by the token, as `{"kind": "token", "id":
"tok_…", "name": "backup script (sam)", "via": "api"}`. Token creation and
revocation are `tokens.create` and `tokens.revoke`. Secrets never reach any of
them.

**For agents later** ([ADR-0017](../docs/adr/0017-ai-and-mcp.md)): the
approval queue is generic. Whether a plan waits is
`action.ApprovalPolicy.NeedsApproval(actor, tier)`, which `authz` answers
(`authz.NeedsApproval`: a token's plans from T2, every plan of an `agent`); the
plan's actor is its originator; deciding is the `approvals.decide` scope
(`authz.DecideApproval`), never a token's.

## Realtime topics

Each topic's document is a member of the subscription document, so its diff
paths start with `/<topic>/`.

| Topic | Document | Changes |
|---|---|---|
| `graph` | `{"resources":{id: resource},"relations":{key: relation}}` | Whole resources and relations; see `internal/graph` |
| `graph:kind/<kind>[,<kind>…]` | The same, filtered to those kinds | As above |
| `metrics` | `{"series":{"<resource id>#<metric key>": series}}` | Each sample tick, the fields of each series that changed: `/series/<key>/value`, `/as_of`, and `/stale` when it flips. Every 30 minutes, when the history gains a point, `/history` whole (or the whole series, when that's smaller). Whole series when one appears or disappears |
| `metrics:resource/<id>` | The same, with only that resource's series. A resource sheet subscribes to it; the runtime reads it as close interest (`Interest.Resources`) | As above |
| `tasks` | `{"tasks":{id: task}}`: running tasks and the last 24 hours, at most 50; Admin and Owner (`tasks.all`) see everyone's, others their own | The fields of a task that changed (state, step, progress), each new log line as `add /tasks/<id>/log/-`, whole tasks when they appear or go. The live log keeps at most 50 lines and drops back to the latest 25 when full (one `replace` of the log then) |
| `inbox` | `{"now": RFC 3339, "incidents":{id: incident}}`; an incident's `class` is `problem`, `repair`, `suggestion` (the correlator's "merge?") or `approval` (a plan an API token made that waits for a person, with the Approval as `approval`; admins and the owner only); `category: "security"` marks a security finding, whose `severity` the security posture decides. **Per viewer**: Viewer and up get it whole; Household gets the household projection (below) | The fields of an incident that changed, or the whole incident when it appears or disappears; `/now` each tick. Household gets whole incidents, and only when its projection changed |

**The household's inbox** (`topics.ProjectIncident`, decided by `authz.SeesInboxDetail`). The Roles table says "Boards only, no Inbox detail", so household sees only what a tile needs to say something is wrong:
- **Which incidents:** problems (not repairs, which are about an integration's credentials, nor the correlator's suggestions), never a security finding, and only when the incident's target or a resource in its blast radius is on a board (`boards.Scope`: a resource an item names (`<type>@<id>`) or a tile binds, or one of the kinds a target-less widget shows, such as hosts for the fleet card; a board never saved, the Incidents widget or an unknown widget type shows everything).
- **Which fields:** `id`, `scenario`, `class`, `severity`, `status`, `phase`, `title`, `target`, `opened_at`, `updated_at`, `resolved_at`, `resolved_by`. `summary` and `resolves_when` are `""`, `causes` and `blast_radius` empty lists; facts, the resolution and actions are left out, except actions marked `household: true` (without their detail or impact).
- **Kept current:** `realtime.ProjectedDocSource` re-projects an incident whenever it changes and sends it whole when the projection changed, so it can appear, change or go as boards and incidents move. What the boards show is read when the subscription opens, so a board edit applies on the next connection.
- **`metrics` stays open to household, and `graph` nearly so.** Boards render from them (every board is the whole home until lenses arrive). The graph household gets is `topics.HouseholdGraph` (`authz.SeesWholeGraph` decides): chadash-node's resources (kind `node`: key fingerprint, address, enrolment, allowed verbs) are left out with their relations, and address-like attributes (`ip`, `ips`, `address`, `mac`, `fingerprint`, `verbs` and the like) are stripped from the rest. The web never shows those on a board. It's the same on the topic (snapshots and diffs: `realtime.GraphSource.View`), `GET /api/graph` and the first paint. Scoping the graph to what the boards show (`boards.Scope`, like the inbox) was the alternative; it was left for lenses, because a tile's widget reads neighbours (`runs_on`, `part_of`, same-as links) that a board's item list doesn't name. `tasks` already shows household only its own tasks.

A series is exactly a `series` entry of the simulator's snapshots (`unit`,
`min`, `max`, `band`, `warn`, `crit`, `value`, `as_of`, `stale`, `history`), and
an incident is exactly an `incidents` entry; see [`sim/README.md`](../sim/README.md).
Optional fields come and go (`stale` is omitted when false), so a field op can
be an `add` or a `remove` as well as a `replace`.
Outside demo mode the integration instances fill all three, in the same
shapes, and the inbox clock follows the wall clock, ticking every 30 s.

## Integrations

An **integration** is one kind of source (Proxmox, Sonarr, the built-in
checks); an **instance** is one configured connection to it. The framework
owns the boilerplate, so an integration only connects, tests, and turns what it
reads into facts.

**Bundled:** 33 integrations plus weather. All read. From v0.2 some also act (see each README's Actions): Proxmox VE and Proxmox Backup Server once given a control token (guest power, snapshots and backups; pruning a datastore), Unraid in control mode, Docker through chadash-node, and qBittorrent, Sonarr, Radarr, Lidarr, Home Assistant (allow-listed scripts), Pi-hole and AdGuard Home with their one credential. Seerr, a spec, approves and declines requests (spec actions, below).
- **Tier 0** (`integrations/<id>`, 25 with `checks` and `weather`): adguard, audiobookshelf, checks, docker, frigate, homeassistant, immich, jellyfin, lidarr, nextcloud, nzbget, opnsense, pbs, pihole, plex, prowlarr, proxmox, qbittorrent, radarr, sonarr, traefik, transmission, unraid, uptimekuma, weather.
- **Tier 1 specs** (`specs/<id>.yaml`, 9): bazarr, karakeep, linkding, mealie, paperlessngx, sabnzbd, seerr (also Overseerr and Jellyseerr, as `aliases`), speedtesttracker, tautulli.
- v0.2's breadth set (wave 8) is lidarr, transmission, nzbget, nextcloud, audiobookshelf, paperlessngx and speedtesttracker, plus seerr covering Overseerr and Jellyseerr. `cmd/chadash`'s `TestIntegrationsLinkedIn` lists every ID; `TestEveryIntegrationHasARecipe` checks each has a recipe.

**Writing a Tier 0 integration.** A folder `integrations/<id>/` with a package
that registers itself, a line in `cmd/chadash/integrations.go`, fixture tests
and a README:

```go
func init() { integration.Register(sonarr{}) }

func (sonarr) Manifest() integration.Manifest {
	return integration.Manifest{ID: "sonarr", Name: "Sonarr", Icon: "sonarr", Version: "0.1.0", Tier: integration.Bronze,
		Capabilities: []integration.Capability{"media.calendar"},
		ConfigSchema: integration.Object(integration.URLProp(), integration.TLSProp(),
			integration.Prop("api_key", integration.Secret("API key"))).Require("url", "api_key")}
}

func (sonarr) Connect(_ context.Context, cfg integration.Config, d integration.Deps) (integration.Instance, error) {
	return &instance{url: cfg.String("url"), key: cfg.String("api_key"), d: d}, nil // no I/O here
}
```

Choose Go when the API needs more than fixed JSON `GET`s with a header or query key: basic auth (Nextcloud, NZBGet), a handshake (Transmission's session id), per-item fan-out (Audiobookshelf's libraries) or shared code (the *arrs). Otherwise write a spec.

The instance's `Test` returns version and grants; `Sync(ctx, want)` fetches
with `d.HTTP`, calls `integration.CheckResponse(resp)` (a 401 or 403 becomes a
Repair), and publishes with `d.Emit.Replace("", integration.Facts{…})`.
[`integrations/checks`](../integrations/checks/README.md) is the worked
example; `integrationtest.New(t)` runs one against `httptest` fixtures.

What wave 5 integrations must do:
- **IDs and config keys** from [07 · v0.1](../docs/plan/07-v0.1.md#instance-config-keys). Identifiers (`token_id`, `username`) are plain string properties; only passwords, keys and tokens are `integration.Secret` (write-only).
- **Connect without I/O**; read the config with `cfg.Decode` or `cfg.String`. Secrets arrive resolved.
- **Use `d.HTTP`** (or `d.NewHTTPClient`, e.g. with a `Session` for cookie logins) for every request, so the TLS policy, timeouts, the response cap and same-origin redirects apply. Put keys in headers, never in paths.
- **State across restarts** goes in `d.State` (`Load`/`Save` of small JSON values, at most 64 KiB each), backed by the `integration_state` table and deleted with the instance; an unsaved config under test gets an in-memory one. OPNsense keeps its 30-day WAN totals there. Never put secrets in it.
- **A Unix socket** (Docker's `unix://`) is `HTTPOptions.UnixSocket` on `d.NewHTTPClient`.
- **chadash-node** is `d.Nodes` (`integration.NodeCaller`, nil when the hub serves no nodes): `Call(ctx, node, verb, req, progress)` runs one of the node's Docker verbs; `integration.NodeErrorCode(err)` reads a refusal's code (`forbidden`, `not_found`, `conflict`, `unavailable`, `not_connected`). Only the Docker integration uses it, for its `node://` endpoint and its actions.
- **Return errors from `Sync`** and let the runtime classify them: `CheckResponse` for HTTP status, `&integration.AuthError{Field: …}` for an auth failure reported inside a 200 (say which secret when you know), `&integration.RepairError{Code: integration.CodeAPIChanged, …}` for a vanished endpoint or `CodeUnsupportedVersion`, `&integration.ConfigError{…}` for a config that can't work. Don't retry or sleep: the coordinator backs off.
- **Publish complete scopes** with `d.Emit.Replace(scope, facts)`: resources (namespaced, stable IDs), relations (`d.Emit.Self()` `monitors` what you read), metrics (keys like the simulator's, unknown as `integration.Unknown()`, never 0). Use one scope, or one per refresh rate.
- **Respect the interest.** `want.Watched` false means nobody's looking: report health, skip the expensive parts. While the hub keeps history, nobody watching is `Watched` with `Record` set, polled once a minute, so a board's reads become its history; keep sheet-only reads behind `WantsResource`. `want.WantsResource(id)` is true while a sheet is open on that resource: the place for series too costly to read for everything. Set `Manifest.Poll` (interval while watched, idle interval, timeout) to the spike's polling plan.
- **`Test`** reports the upstream version and, where the source can tell, the grants it read back, `Write: true` (or `Access`) on any that can change things; the runtime warns about write grants on read-only connections. Use `integration.Failed(err)` on failure.
- **Push** is optional: implement `Subscribe(ctx, emit)`; it runs only while someone watches.
- **Actions** are the optional `integration.Actor` (`Actions() []action.Def`, `Execute`) in `integrations/<id>/actions.go`; see "Actions" below. Conventions:
  - IDs are `<integration>.<kind>.<verb>` (`unraid.container.start`, `pihole.app.pause`). Every instance of an integration returns the same IDs; the runtime routes each call to the instance that owns the target, and a Def's own `Resolve` may still refuse targets that aren't its instance's.
  - Resolve with a fresh read when preconditions depend on state; refuse with `action.Precondition` (a sentence) or `action.Unavailable` (a missing scope, with the instance ID as `recipe`). Verify through the normal reads with `act.Until`, never the upstream's 200 alone.
  - Params come from `integration.Params` (secrets as write-only properties are redacted); allow-lists, like Home Assistant's scripts, are an enum in the params schema, so anything else is `422 invalid` before a request is made.
  - `integrations/internal/act` holds shared helpers. Contract tests run actions through `integrationtest` (`h.Actions`); the app integrations still use `act/acttest`, which works the same way, until they move over.
- **Fixture tests** against `testdata/integrations/<id>/` with `integrationtest`: `Validate` the config shapes the importer writes, `Connect`, `Test`, `Sync`, then read back `Resource`, `Series` and `Incident`.

**Actions** (v0.2). An instance that implements `integration.Actor` gets its actions registered with the engine by the runtime (`runtime.Actions`, wired when `runtime.Options.Actions` is the engine's registry; not in demo mode):
- **Two instances beside Sync's.** One connected with `Manifest.ReadConfig` (no control fields) runs the Def hooks that read: `Resolve`, `Preconditions`, `Verify`, `Steps`, `TierFor`. One connected with `Manifest.ControlConfig` runs `Execute` (through `integration.ActionDefs`, so a nil `Def.Execute` is the Actor's `Execute`) and `Permitted`, which the runtime checks after `Preconditions` at plan and approve: whether the credential that acts may do this to this target (Proxmox reads the control token's ACL on the guest). Neither publishes facts; after an action the instance's coordinator syncs at once. Hooks may run concurrently, never on Sync's instance. Return `action.Precondition("win-gaming is already stopped.")` from Preconditions and an `action.Fail(code, sentence)` from Execute and Verify.
- **Registration.** The engine's registry gives each action ID to one source, so each integration is one source, `integration/<id>`, whose Defs route every hook to the instance that owns the target: the instance whose emitter published the resource (its graph `Source`), else the `<integration>:<instance id>/` prefix of the ID. Starting, restarting or reconfiguring an instance re-registers the source; stopping, disabling or deleting it removes the instance's part, and the source goes with the last one. Static fields (tier, params, title) come from the first instance by ID.
- **Control credentials.** `Manifest.Control` declares where actions get their credential. `Fields`: a control group, top-level properties marked `.InGroup(integration.GroupControl)` (`"x-chadash-group": "control"` in the schema; secrets among them go to the vault like any other), each replacing a read field when the instance acts (Proxmox: `control_token_id` → `token_id`, `control_token_secret` → `token_secret`). The group is all or nothing (`CheckControl`, a `422` naming the missing field). `Mode`: one credential, with a property saying whether the instance may act: a boolean, or an enum whose value `ModeOn` lets it act (Unraid: `mode` = `control`, decision 6). No `Control`, or one with only `Grants`: the instance acts with its one credential. `Control.Grants` names the write grants (as Test reports them) the acting credential needs; when Test checks the credential that acts (a mode that's on, or no control fields), they don't raise the `write_grant` warning (`Manifest.ExpectedWrites`, `WriteGrantWarning(grants, expected...)`). Without the control credential every action answers `409 unavailable` with `Control.Missing` (`{instance}` is the instance's name) and `"recipe"` naming the instance; Test warns `control_available` (info) pointing at the recipe's control variant. Sync and Test connect with `ReadConfig`, so they never see the control credential.
- **Control recipes.** `integrations/<id>/recipe.control.md` (the recipe Markdown, access never `read-only`) registered with `integration.RegisterControlRecipe`; the catalog serves it as the recipe's `control`, and its `fills` name the control fields.
- **The self-aware T4.** A Def marked `Disrupts` (reboot, stop) is T4 when its target runs the hub or the router: the target, or anything that `runs_on` or is `part_of` it, transitively in the Home Graph, is `chadash:hub` (`integration.HubResource`), a resource with `attrs.self`, or a resource of kind `router`. An integration's own `TierFor` can raise it further (Proxmox's `hub_vmid` hint), and an integration that can tell where the hub runs relates `chadash:hub` `runs_on` it: Proxmox (the hint, or the hub's addresses), Docker (the container chadash-node names with `engine.self`, else the one whose ID is the hub's own, `integration.HubContainerID`), and Unraid (its container of that ID, best effort). A plan whose target the hub runs on lists `chadash:hub` in its impact (`action.HubResource`; the engine adds it even to a Def's own blast radius), which the web calls "Cha Dash itself".
- **Incidents' availability.** For each incident's `suggested` and `alternatives` action, the emitter fills `plan_ready`, `unavailable` and `recipe` from `Actions.Availability`: only for an action an attached instance offers (others, like `link.open`, carry none, and anything an integration set is cleared). `plan_ready` is true when the instance that owns the target is attached and offers the action, has the control credential, and (for a Def without its own `Resolve`) the target is in the graph with a kind the action applies to. Otherwise `unavailable` says why, with `recipe` set when a control credential is missing. Preconditions aren't run (they read the upstream); planning checks them. Changes reach open incidents at once (`Emitter.Reapply`).
- **Contract tests.** `h.Actions(integ, config, secrets)` attaches the instance as the hub does and starts a real engine over a temporary database on the harness's graph: `Plan`, `Approve`, `Wait`, `Run`, `Succeeded`, `Refused` (or `Refusal`), `Events`/`LastEvent` (the audit log). Wrap the fake upstream's handler with an `integrationtest.Writes` and `Expect` the writes: method, path, and form or JSON body. `integrations/proxmox/actions_test.go` is the worked example.

**Tier 1 specs** (internal and unstable until v1.0). `specs/<id>.yaml`,
embedded and registered by the `specs` package; `specs/seerr.yaml` is the
sample, with a fixture test.

```yaml
apiVersion: chadash/v1alpha1
kind: Integration
meta: { id: seerr, name: Seerr, aliases: [Overseerr, Jellyseerr], icon: seerr, version: 0.1.0, tier: bronze, category: media, capabilities: [media.requests] }   # aliases: optional, other names it goes by; category: where it sits on the Add list (media, automation, downloads, hosts, network, monitoring, home, files)
config:                           # url and tls are always added; types: string, url, secret, integer, number, boolean
                                  # (strings may have enum, an anchored pattern and max_length)
  api_key: { type: secret, title: API key, required: true }
source:
  headers: { X-Api-Key: "{{ secret.api_key }}" }   # templates: config.x, secret.x, instance.id, instance.name
  timeout: 10s                    # per request, at most 30s
every: 60s                        # at least 10s
requests:                         # run in order, GET (or POST with body)
  counts:
    path: /api/v1/request/count   # under the instance URL's path; no secrets here
    query: { take: "1" }          # optional; secrets allowed (redacted from errors)
    extract: { pending: pending } # name → gjson path; missing is null
    optional: false               # true: a failure leaves the values null instead of failing the sync
version: status.version           # expr-lang over {config, instance, <request>: {<name>: value}}
resources:
  - id: app                       # the resource is <spec id>:<instance id>/app
    kind: app
    name: instance.name           # expr; default the instance name
    state: '"running"'
    health: '(counts.pending ?? 0) > 20 ? "attention" : "ok"'   # ok, attention, down, unknown, maintenance
    reason: '…'
    attrs: { version: status.version }                          # null results are left out
    labels: { chadash.group: Media }                            # literal
    caps: [media.requests]
    links: [{ title: Seerr, url: config.url }]
  - id: request                   # fan-out: one resource per element of a list, at most 50
    each: pending.items           # expr → list; its expressions see the element as item
    key: string(item.id)          # expr → slug ([A-Za-z0-9._-], ≤ 64); the ID is <spec>:<instance>/request/<key>
    part_of: app                  # optional: an earlier resource without each
    kind: media_request
    name: '"Request #" + string(item.id)'
metrics:                          # only on resources without each
  - { resource: app, key: requests.pending, value: counts.pending, unit: count, min: 0, max: 100, band: [0, 5], warn: 0, crit: 0 }
repairs:
  - { when: 'status.version startsWith "0."', code: unsupported_version, message: '"too old"' }
targets:                          # actions' targets, addressed by a param: <spec>:<instance>/<target>/<param>
  request:
    kind: media_request
    pattern: '^[1-9][0-9]{0,9}$'  # anchored, and must refuse "", ".", ".." and "/" (checked at load)
    read:                         # resolves the target: 404/410 not_found, 401/403 unavailable
      path: /api/v1/request/{{ target.id }}   # {{ target.id }} only; no query, %, .. or secrets
      extract: { status: status }             # not id or name
    name: '"Request #" + target.id'           # expr over {config, instance, target: {id, <extracts>}}
    state: string(target.status)              # what an approval must find unchanged
actions:                          # up to 16; the instance then implements integration.Actor
  - id: seerr.request.approve     # <spec>.<target>.<verb>
    target: request
    title: Approve
    tier: T1                      # T0 … T4
    label: '"Approve " + target.name'         # optional; default title + the target's name
    params: { note: { type: string, max_length: 200 } }   # optional; never secret or url
    preconditions:                # the first that holds refuses with its message
      - { when: 'target.status != 1', message: 'target.name + " isn''t pending."' }
    request:
      method: POST                # POST, PUT or DELETE
      path: /api/v1/request/{{ target.id }}/approve   # also {{ params.x }}: required, and an integer, an enum or with a pattern
      body: { note: params.note } # optional JSON object of exprs over {config, instance, target, params}; not for DELETE
    verify: { until: 'target.status == 2', message: '"Not approved."', timeout: 30s }   # re-reads every 2 s; ≤ 1m
    undo: seerr.request.decline   # optional: another action on the same target, without params
    household: false
```

**Spec actions.** The target's ID carries its param (`seerr:<instance>/request/42`), so the runtime routes it by the ID's prefix and it resolves through the read whether or not the graph has it; a fan-out resource may publish the same IDs so ⌘K finds them. The param is checked against the pattern before anything is sent, and every path value is escaped as one segment and can't be `.` or `..`. The read's values (with `id` and `name`) ride in `Target.Ref`, never persisted or shown, so preconditions and labels don't read again. Headers are `source.headers`: the instance's one credential acts (no control credential). Response bodies never reach a log, an error or a task; errors are fixed sentences without the URL. Failures are `forbidden` (401/403), `not_found`, `upstream` (other statuses), `unreachable`, `verify_failed` with the spec's message, and `spec_failed` for an expression that can't evaluate.

A 401 or 403 is an auth Repair; a 404 or 410 is an `api_changed` Repair that
names the version the spec detected so far, unless one of the spec's repairs
already holds on what was read before it (an old release without the
endpoint is `unsupported_version`, not "the API changed"). Responses must be JSON and at most
1 MiB. Expressions are compiled at load, so a typo fails registration (and
`go test ./specs`), not a sync.

## Decisions

**Storage**
- **The driver is `github.com/ncruces/go-sqlite3`, pinned at v0.35.6** ([ADR-0015](../docs/adr/0015-sqlite-driver.md)). It's SQLite's Wasm build translated to pure Go, so the hub stays a static, cross-compiled binary. On the hub's own workload it matched or beat modernc (session lookups 18–37% faster, no `SQLITE_BUSY` under mixed load), links fewer modules, and offers an encrypting VFS. It's v0.x and young, hence the pin.
- **The driver is named in one file**, `store/driver_ncruces.go`, behind `openDB(path, readOnly)`. Going back to modernc is a swap of that file, as long as encryption stays off. Swap notes, all handled there:
  - ncruces reads `_pragma` and `_txlock` only from a `file:` URI, so the DSN is an escaped `file:` URI. Spaces are written `%20`, not `+`: SQLite reads `modeof` itself and doesn't decode `+`.
  - Pragmas run in the order given, so `busy_timeout` comes first.
  - The driver creates `-wal` and `-shm` `0666` minus the umask, where SQLite's own VFS copies the database's mode. `modeof=<db path>` makes `-wal` (which holds row data) `0600`. `-shm` (the WAL index) relies on the process umask: `chadash` sets `077` at startup on Unix, so every file it creates is private. Anything that must be group-readable later needs an explicit `chmod`.
  - SQLite is built with `DQS=0`: a double-quoted string literal is an error. Strings in SQL are single-quoted; a test pins this.
  - Don't import ncruces's deprecated `embed` package (it warns at init). Extensions such as FTS5 are opt-in `ext/` packages.
- **One writer connection, four query-only readers.** SQLite has one writer at a time; a one-connection pool makes writes queue in Go instead of failing with `SQLITE_BUSY`. Readers carry `query_only(1)`, so a stray write fails loudly. Never query the writer pool while holding one of its transactions: that deadlocks.
- **Idle readers close after a minute.** An ncruces connection keeps its memory high-water mark (~6 MiB after a 1 MiB layout) until it closes; reopening one costs ~0.3 ms. The writer stays open.
- **Pragmas** on every connection: `busy_timeout(5000)`, WAL, `foreign_keys(1)`, `synchronous(NORMAL)` (safe with WAL; at worst it loses the last commits on power loss). The writer uses `_txlock=immediate`.
- **Encryption at rest is opt-in and not implemented yet.** ADR-0015 picks ncruces's Adiantum VFS, off by default. The seam is `openDB`: it gains a key and opens the pool with `driver.Open` and a connection hook that runs `PRAGMA hexkey` before the other pragmas, keeping the key out of the DSN. `store.Open` passes the key through, and `cmd/chadash` loads the KEK (and derives the database key with HKDF) before opening the store, the reverse of today's order. Converting an existing file means `VACUUM INTO` and a rename at startup, and the `sqlite3` shell can't open an encrypted file, so `chadash db decrypt` comes with it.
- **Driver-neutral columns.** Timestamps are Unix milliseconds (`INTEGER`), not `time.Time`, because drivers scan times differently. Tables are `STRICT`. IDs are prefixed random strings (`u_`, `s_`, `sec_`) from `crypto/rand.Text`.
- **goose through its `Provider` API**, not the global one. Don't call `Provider.Close`: it closes the database it was handed.
- **Layouts** are opaque JSON with a `format_version` (the layout package's document format) and a per-board `revision`. Each save appends a row, so history and undo survive restarts. The hub stores the document as sent (compacted), key order included.
- **Optimistic saves.** A save names the revision it started from; the check and the insert share one write transaction, and the single writer connection serialises them, so two tabs can't both win. The loser gets `409` and the current revision.
- **Layout history is capped** at the newest 100 revisions per board, pruned on save, so autosave can't grow the database without bound.
- **The default board** (`Home`, slug `home`, ID `b_…`) is created by the first `GET /api/boards`, with `INSERT … WHERE NOT EXISTS` inside a write transaction so concurrent first calls make one board.
- **History has its own database**, `history.db` (`store.OpenHistory`, ADR-0031), so its writes never queue behind the hub's, and deleting it loses nothing else. New files get incremental auto-vacuum. `chunks` is a rowid table with a unique index on `(series, tier, start)`: keyed `WITHOUT ROWID` on that, the blobs split pages and recording cost twice the time.
- **`audit`** is v0's stub table, left as it was; nothing writes to it. The audit log is `audit_events` (see Actions and audit).

**First run and sessions**
- **First-run setup has no token.** While no user exists, `POST /api/setup` creates the owner, named `admin` unless the request names someone else, so whoever reaches a new hub first claims it. The hub logs a note at `warn` on each boot until then, so it survives a quiet log level. Invalid input leaves setup open; success closes it for good. Attempts are rate-limited per client IP like sign-in.
- **Exactly one owner**, enforced by a partial unique index. That also makes concurrent setup requests race-free at the database level.
- **argon2id at OWASP's baseline** (19 MiB, t=2, p=1), with at most two hashes running at once. That keeps a login inside the RSS budget on small ARM boards. Parameters live in each PHC string, so they can be raised later. Unknown usernames are checked against a dummy hash, so timing doesn't reveal which usernames exist.
- **Passwords**: at least 6 characters, at most 512 bytes. **Usernames**: 1–64 of `A–Z a–z 0–9 . _ @ -`, case-insensitive.
- **Sessions**: a 256-bit token in the cookie; only its SHA-256 is stored, next to a public `s_…` ID for listing and audit. **30-day idle expiry that renews while in use:** at most once a day, a request slides `expires_at` forward and reissues the cookie, so a session in regular use never lapses and an unused one expires after 30 days. `last_seen_at` is written at most every 5 minutes. A `method` column records how it was established (`password` or `passkey`). The role is read from the users row on every request, so a role change applies from the next request, and disabling a user deletes their sessions in the same transaction. A WebSocket reads its identity once, at the upgrade, so open sockets are closed when it stops standing: `auth.Service.OnEvent` tells the server about sign-outs (the session) and role changes, disables and ownership handovers (the user), and `server` closes those sockets with `realtime.Hub.CloseWhere` (1008); every socket also rechecks its session every minute and before each new subscription (`auth.Service.StillValid`: exists, unexpired, not disabled, same role) and closes after 12 hours.
- **Cookie** `chadash_session`: `HttpOnly`, `SameSite=Lax`, `Path=/`. `Secure` when the request came over TLS, or from a trusted proxy that sent `X-Forwarded-Proto: https`. There's no `__Host-` prefix, because plain-HTTP LAN installs are common and that prefix requires `Secure`.
- **CSRF**: `SameSite=Lax`, plus net/http's `CrossOriginProtection` on every unsafe method (it uses `Sec-Fetch-Site`, falling back to Origin vs Host), plus JSON-only request bodies, which a cross-site form can't send. There are no per-request CSRF tokens. The architecture doc lists them; these three layers replace them on purpose, and tokens can still be added if a form-based flow ever needs them.
- **One seam**: `auth.Service.StartSession(w, r, user, method)` is where every sign-in method ends: password, passkey, and later OIDC.
- **Address bans.** Five wrong passwords in a row from one client address (`auth.BanAfter`; unknown usernames count too) ban it: the fifth answers `403 ip_banned`, and so does every sign-in from it after that, password or passkey, until someone clears it (Settings → Users, `DELETE /api/bans/{ip}`, or `chadash bans clear <ip>` on the host, which works with the hub stopped). Bans live in `ip_bans` (`00015`) and are read on every sign-in, so the CLI applies at once; the strike count is in memory, so a restart forgets strikes but never bans. A sign-in that works clears the address's strikes. The key is the exact address (IPv4-mapped IPv6 counts as IPv4), so an IPv6 client can dodge a ban by changing address within its /64; the /64 rate limit below still applies. Sessions an address already holds keep working. Behind a reverse proxy without `CHADASH_TRUSTED_PROXIES`, every client is the proxy, so five wrong passwords from anyone ban everyone: set it, or clear the ban with the CLI.
- **Rate limiting** is in memory and per client: the IPv4 address, or the IPv6 /64, so one connection can't mint fresh budgets. When the 4096-bucket map is full of buckets still draining, new clients share one overflow bucket; nobody's budget is reset. Wrong passwords also draw on a **per-account** budget (20, then one a minute, keyed by user ID), shared by sign-in and the password step-up, so changing address doesn't buy more guesses. Unknown usernames have no account budget. Passkeys aren't limited per account. Behind a reverse proxy, set `CHADASH_TRUSTED_PROXIES`; otherwise every client shares the proxy's bucket. `httpx.ProxyCheck` notices that mistake (forwarded headers from an untrusted peer, or a sign-in over plain HTTP when `CHADASH_PUBLIC_URL` is https), logs it once at warn and lists it under `notes` in `GET /api/settings/security`.
- **Forwarded headers** are honoured only from trusted proxies. The client IP is the rightmost `X-Forwarded-For` hop that isn't a trusted proxy.

**Roles, authorization and step-up** (v0.2)
- **Five roles**, most powerful first: owner, admin, operator, viewer, household. `users.role` has carried them since `00001`. What each may do is decided in `internal/authz` and nowhere else: routes by the table in `authz/routes.go`, actions by `authz.Decide` (tier caps: owner T4 where enabled, admin T3, operator T2, viewer T0, household only actions marked `household: true`, up to T1, T0 included). A `Def` marked `ReadsData` (it reads data back, like `docker.container.logs`; container logs often hold secrets) needs Operator or above whatever its tier, unless it's also marked `Household`. Log lines a task reads stay in the live task and the `tasks` topic; the stored task document never has them.
- **One owner, never stranded.** The owner can't be demoted, disabled or deleted; ownership moves only by `transfer-ownership`, which demotes the old owner to admin first so the single-owner index never sees two. An admin manages every other account. Users are disabled, never deleted, so audit records keep pointing at someone.
- **Step-up** is a re-verification on the session: a passkey assertion with user verification required, or the password when the account has no passkey that works at this address and the rule allows it (`security.step_up`, `passkey_or_password` by default, or `passkey_only`). A grant is `sessions.step_up_until`, five minutes, server-side, gone with the session. T3 needs a grant; T4 needs one from the last minute and the engine spends it (`auth.Service.SpendStepUp`, the engine's `ConsumeStepUp`): in the transaction that marks the plan used and starts the task, and only if the session still holds that very grant (`UPDATE … WHERE id = ? AND step_up_until = ?`), so T4 always asks again and two approvals racing on one grant can't both run. Changing the rule is itself T3.
- **The engine's seam.** `authz.ActorFrom(r)` gives the actor behind a request, step-up included; `authz.Policy` implements the action engine's `Policy`. T4 is refused unless its scope is listed in the `actions.t4` setting (a JSON array of scopes, empty by default). Scopes can be qualified by resource (`proxmox.guest.reboot:host=pve-a`) and patterns (`proxmox.guest.*`) are understood; the UI doesn't offer them yet.
- **`api_tokens`** (`00006`, `00012`) holds the scoped, expiring tokens: the SHA-256 of each secret, a JSON scope list, `last_used_at` and its address (written at most once a minute), revocation and who did it. A token acts as its user, with the user's current role, narrowed to its scopes (`authz.WithTokens` over `auth.Service.TokenAccess`), and can't step up, so T3+ is out of its reach. Its T2 plans wait for a person (`approvals`, `00012`). See [API](#api).

**Passkeys**
- **`github.com/go-webauthn/webauthn` v0.18.2** does the ceremonies. It adds about 2 MB to the stripped binary: it links every attestation format (TPM, Android, SafetyNet) even though we ask for none.
- **Discoverable credentials, usernameless sign-in.** Registration asks for a resident key (`residentKey: required`), prefers user verification, and wants no attestation: a homelab has no use for authenticator certificates, and asking for them makes browsers prompt. Sign-in sends no `allowCredentials`; the authenticator returns the user handle and the hub finds the passkey by its credential ID.
- **Relying party.** With `CHADASH_PUBLIC_URL` set, the RP ID is its host and the origin is the URL. Without it, the hub derives both from the request's `Host`, but only for `localhost` and `*.localhost`. Browsers allow WebAuthn only over HTTPS or on localhost and never on an IP address (the library refuses IP RP IDs too), so `127.0.0.1` gets "open `http://localhost:<port>` instead", and any other address gets "set `CHADASH_PUBLIC_URL` to the `https://` address you open Cha Dash at". A request whose `Origin` doesn't match the relying party is refused up front with both addresses in the message, instead of failing later in the browser.
- **Local development.** The root `bun run dev` serves the SPA at `http://localhost:3697` and proxies `/api` to the hub without rewriting `Host` (`changeOrigin: false`), so passkeys work there with no configuration: RP ID `localhost`, origin `http://localhost:3697`. Open `localhost`, not `127.0.0.1`. Passkeys made there belong to RP ID `localhost` and never sign in anywhere else.
- **Ceremonies stay in memory**, for 5 minutes (also the `timeout` the browser gets), at most 1024 at once. When it's full, the one closest to expiring goes first. Each `begin` sets `chadash_passkey`, an `HttpOnly`, `SameSite=Strict` cookie holding a random ceremony ID, scoped to `/api/auth/passkeys/register` or `/login`. `finish` removes the ceremony before checking anything, so each challenge gets exactly one attempt: a replay, a second try or a late answer gets `400 ceremony_expired`. A restart cancels ceremonies in progress, and that's all it loses. A registration also has to finish under the same user that began it.
- **The user handle** is 32 random bytes per user (`passkey_users`), shared by all their passkeys and unrelated to the username or user ID.
- **Stored per passkey** (`passkeys`): the RP ID it was registered under, credential ID (unique), COSE public key, sign count, a sticky clone warning, transports (filtered to the six WebAuthn defines), AAGUID, attachment, the flags octet with BE and BS split out, attestation type and format, name, `created_at`, `last_used_at`. Sign-in matches only passkeys of the current RP ID; the list shows all of them, with `rp_id`.
- **Names.** A new passkey is named after its provider when the AAGUID is a well-known one (iCloud Keychain, Google Password Manager, Windows Hello, 1Password, Bitwarden and a few more), otherwise `Passkey N`, with a number added to avoid the user's existing names. Browsers often report a zero AAGUID, hence the fallback. Renaming trims the name and allows 1–64 characters, no control characters.
- **Sign counter.** Synced passkeys always report 0, and that's fine. A counter that doesn't increase (other than 0 to 0) means two copies of the key may be in use: the hub logs a `warn` naming the passkey, sets `clone_warning` (the list shows it, and it never clears), keeps the higher stored count, and lets the sign-in through, as most relying parties do. The owner can delete the passkey.
- **Backup flags.** BE (can sync) must match registration on every sign-in, or the sign-in fails. BE=0 with BS=1 fails in both ceremonies. BS (synced) is stored at each sign-in and shown as `backed_up`.
- **Exclusions.** Registration lists the user's passkeys for this RP ID in `excludeCredentials`, so an authenticator won't register twice. A credential ID that's registered already gets `409 passkey_exists` anyway.
- **The last way in stays.** Deleting a passkey is refused when the user has no password and it's their last passkey. The owner always has a password today, except the passwordless owner that dev login creates, so this is mostly for later passkey-only accounts. It counts passkeys under any RP ID.
- **Unknown passkeys.** When a sign-in presents a credential the hub doesn't have (deleted, or registered elsewhere), the `401` carries `"signal":{"rpId","credentialId"}` for `PublicKeyCredential.signalUnknownCredential`, so the password manager can stop offering it.
- **Setup is unchanged.** First run is still token, then owner, then password, and the owner leaves signed in. The SPA can offer passkey enrollment right after (`register/begin` with that session); it's optional.
- **Logs** record registrations, deletions and failed sign-ins (with the library's reason, never key material) and clone warnings. Successful passkey sign-ins aren't logged, like password ones.

**Vault**
- **Envelope encryption.** A fresh data key per secret seals the value with XChaCha20-Poly1305, and the KEK seals the data key. Both seals use AAD `alg ‖ id ‖ name`, so swapping or renaming rows fails authentication.
- **The KEK file** may hold 32 raw bytes, base64 or hex. A 32-byte file that is all printable text is rejected as a passphrase, not taken as a raw key. Generated keys are base64 with mode `0600` and are created with `O_EXCL`.
- **A key is generated only at the default path.** A configured `CHADASH_SECRET_KEY_FILE` that doesn't exist stops the hub; that usually means an unmounted secret. The vault also refuses to generate a key when secrets already exist.
- **Boot fails if any stored secret was sealed under a different key.** Each row carries a KEK fingerprint (a truncated HMAC). Key rotation, later, will relax this to "every key ID is known".
- A key file readable by group or others is a **warning, not an error**: Docker secrets mount `0444`.
- Names follow `[A-Za-z0-9][A-Za-z0-9._/-]{0,127}`, the form `${secret:name}` references use. Values are capped at 64 KiB. Values never appear in logs or errors.
- **`PutTx`** seals a secret inside the caller's write transaction, so it commits or rolls back with the rows that reference it. `Put` wraps it.

**Importers**
- **Two halves.** A source package (`importer/homepage`, `homarr`, `yamlboard`, `leads`) parses and maps, purely: no database, no network (Homarr reads its own backup's SQLite file from a temporary copy, read-only and immutable). `importer.Service` compares the result with the hub (`DiffWith`, the dry run) and applies it (`ApplyWith`). Each source is an `importhttp.Source`: its file roles, how to recognise its files, and its parser. Route patterns are literal per source so `authz`'s route test can read them.
- **The Review sheet's choices** apply to the plan before it's compared (`Choices`): leaving a connection out leaves out the tiles and links that need it; leaving a tile out drops its check target when no other tile watches it; `use_existing` binds tiles to an instance the hub has (same integration only); typed keys fill `missing_secrets` the way a parser fills secrets; `board:<id>` adds the plan's tiles to that board after its own items, in the section with the same ID or a new one, keeping the board's tiles and section overrides as they are; `none` drops the board.
- **The plan hash.** A dry run returns `plan_hash`, a SHA-256 over the result as JSON (changes included, never a secret value). Apply recomputes it inside its transaction and refuses with `ErrPlanChanged` if it differs, so what lands is what was reviewed; the handler answers `409` with the fresh dry run.
- **History and Revert.** An apply that changes something writes an `imports` row: who, when, the files' names, the target, and what it created by ID (board, instances, secrets, layout items). Revert removes only what the import created and nobody changed since (an instance's `updated_at`, the board's latest layout revision), keeps the rest with a reason, and never touches what the import updated. Removing connections needs a step-up (the T3 check). The `import/<source>` settings row stays as the source's last state, for stale reports.
- **Secrets never ride in the plan.** The parser returns values next to the plan as `importer.Secrets`, whose `Secret` type redacts itself under every `fmt` verb, `slog` and JSON. Only `Reveal` gets the value out, and only `Apply` calls it, to hand it to the vault. Tests scan every response, stored row and log line for the fixture's credential values.
- **Secret references.** Instance config holds `${secret:<vault name>}` where a credential goes; importers name secrets `instance/<instance id>/<field>`. Identifiers (a Proxmox token ID, a username) stay plain in config.
- **One transaction.** `Apply` writes the board, a layout revision, the `integration_instances` rows, the vault secrets (`vault.PutTx`) and the import record, or nothing.
- **Idempotent by construction.** IDs are derived from the source path (Homepage group and entry key), so a re-import finds the same rows. Each object is compared first, JSON as values and secrets by decrypting the stored one; anything equal is skipped, so re-importing writes no row and bumps no timestamp. Changed objects are updated (a changed layout appends a revision, so the user's own edits stay in history). Instances an earlier import made that the source no longer has are reported `stale`, never deleted.
- **Name clashes.** An instance the user created by hand keeps its name; the imported one becomes `Name (imported)`.
- **The import record** (`settings["import/<source>"]`) keeps what neither the layout document nor an instance row can hold: tile metadata (name, icon, href, description, options, bindings), explicit `runs_on` links, discovery entries, and the ids the import created. It's interim: tile config and graph relations take these over in wave 6.
- **YAML** is `go.yaml.in/yaml/v3`, the YAML organization's maintained continuation of `gopkg.in/yaml.v3` (archived in 2025), with no dependencies. The importer walks `yaml.Node`s: key order survives, and a scalar keeps its text, so a 32-digit API key with leading zeros isn't read as the number 1. Aliases resolve against a 200 000-node budget, so an alias bomb is refused rather than expanded.

**Integrations**
- **Secrets** live in the vault as `instance/<instance id>/<field>` (the importer's convention); the stored config holds `${secret:instance/<id>/<field>}` in their place. References resolve only in the manifest's write-only fields, so a non-secret field can never pull a secret out of the vault, and the API refuses `${secret:` in config. A new value overwrites the entry in place, and a failed save puts the old value back. References to other names (hand-written ones) still resolve; when no instance references a vault entry any more, it's deleted.
- **Secret values never leave.** Responses show `{"set": bool}`. Every error message, warning, test result and log line an integration produces passes through a scrubber that replaces each of the instance's secret values (and its query-escaped form) with `[redacted]`; `Deps.Log` is wrapped the same way. URL query strings are dropped from error messages.
- **Instance IDs** are `i_` plus base32, random (the importer's are deterministic). Rows written outside the API (the importer) are picked up by `Manager.Reload`: an applied import calls it, and the manager re-reads the table every minute as a safety net. They're validated when they start: a config that doesn't pass the schema runs nothing and shows `error` with the fields. An instance of an integration this build doesn't have is listed with status `error`.
- **One coordinator per instance.** The first sync comes within 2 s of start (jittered); then the manifest's interval while watched, the idle interval (default 5 min) while not, or the interval always for `Poll.Background` integrations (checks). Every wait is ±10 % jittered. Transient errors back off exponentially from the interval, capped at 10 min (or the idle interval). Repair-class errors (401/403, TLS refusals, `RepairError`) raise a Repair and retry every 5 minutes, so fixing the upstream clears it without a click; config errors retry at the same pace without a Repair. A panicking integration is an `internal` error, not a crashed hub.
- **"Watched" is hub-wide and coarse, except for sheets.** `runtime.Watch` wraps the realtime `Source` and counts open feeds: any `graph` or `metrics` subscription makes every instance watched, and when every graph subscription is a `graph:kind/…` filter, `Interest.Kinds` lists those kinds. A `metrics:resource/<id>` feed (a resource's sheet) puts that ID in `Interest.Resources`, so an integration can read costly series for just those (Docker's container stats). An instance that implements `integration.InterestIntervaler` picks its interval from the interest, and it's asked again whenever the interest changes. The inbox alone doesn't count. Opening a tab makes a stale instance sync at once. A refresh (`POST …/refresh`, Check now) syncs at once with `Interest.Refresh`, which tells an integration with its own schedule (Checks) to read everything. Per-widget interest needs lens topics. While history is kept (`Watch.SetRecord`, from `history.Recorder.OnLevel`), nobody watching counts as a whole board on screen: `Interest.Record`, polled at `RecordEvery` (1 minute) rather than the idle interval; Off goes back to idle at once.
- **Status** is derived: `disabled`; `repair` after a repair-class error; `error` after 3 transient failures in a row, a config error, a missing secret or an unknown integration; `degraded` while starting, after 1–2 failures, or with sync warnings (`Emit.SetWarnings`); else `ok`. `last_ok` and `last_error` are in memory and start empty after a restart.
- **The instance is a resource**, `integration:<instance id>` (kind `integration`, source `hub`), with `attrs.type`, `tier` (`go`/`spec`), `quality`, `status` and `upstream_version`, health `ok`, `attention`, `repair` or `unknown` while connecting. What it publishes has `source` = that ID. A failed sync marks every series of the instance stale (last-known value kept) and leaves resources as they were: a broken integration isn't an outage.
- **Repairs** are inbox incidents `<instance id>.repair`, class `repair`, target the instance resource, scenario `auth-rejected`, `tls-pin-mismatch`, `tls-untrusted`, `api-changed` or `needs-repair`, with `facts.instance`, `facts.code`, `facts.status` (the HTTP status, when there is one) and `facts.field` (the integration's, `tls` for certificates, else the first secret for a rejected credential) and the instance's resources (up to 20) as the blast radius. **Phases are a lifecycle**: `open`, `fixing` once the user changes the instance, `resolved`. Resolved incidents stay 24 h.
- **Security findings and the posture.** An integration marks a security finding `category: "security"` and declares its severity: `attention` for an exposure that hands out control or credentials (the raw Docker API or socket, secrets in container labels, Traefik's API open, credentials over plain http), `info` for advice (a socket proxy that's too broad, Traefik's dashboard without auth, Frigate's anonymous admin). The emitter is the one place integration incidents enter the inbox, and it publishes each at the severity the hub's security posture gives it (`internal/security`): `relaxed` (the default, a LAN-only home) every finding at `info`, so a hint only; `standard` the declared severity; `strict` every finding at `attention`. The emitter keeps the declared severity in its own state, never in the incident, and `Manager.SetPosture` re-publishes every instance's findings at once (open and resolved), changing only `severity` in place, so the correlator's causes stay and `updated_at` doesn't move. Re-raising an unchanged finding still publishes nothing. The demo mirror applies the same transform to the Demo Home's hints. The Homepage importer's credential hints (reused secrets, plain http) stay in the import summary; they never reach the inbox.
- **The HTTP client** per instance: no proxy, 5 s dial, 10 s TLS handshake, 15 s per request, a 16 MiB response cap, `User-Agent: chadash/<version> (+https://dash.cha.sh)`, redirects followed only within the origin (5 at most), and an optional `Session` hook (log in once, re-login and retry once on 401/403, log out on stop).
- **TLS policy, per instance.** `verify` is crypto/tls's own check with the URL host. `pin` accepts the pinned leaf, or a pinned CA that's in the presented chain *and* that the leaf verifies against, without checking the host (Proxmox is reached by aliases its certificate doesn't name). `tofu` with no pin accepts the first certificate and records its SHA-256 in `config.tls.pin` (a test of an unsaved config only reports it); after that it's `pin`, and a mismatch is a Repair. `insecure` checks nothing; the test warns, and so does the instance's view (`tls_insecure` in `warnings`) for as long as the saved config has it, without changing its status. A pinned CA vouches for every certificate it signs, for any host, so pin a leaf unless you control the CA. Every refusal reports the presented fingerprint, so the UI can offer to trust it.
- **Tests** connect a fresh instance with a discarding emitter, under 30 s, and add the framework's warnings: write grants on a read-only connection (all of v0.1's are), insecure TLS on https, credentials over plain http.
- **Nodes and stacks** (v0.2 review fixes)
- **The pairing secret is single-use.** The enrollment it admits spends it, and the hub writes a fresh one into the pairing file at once (`Pairing.Redeem`, under one lock, so two nodes racing with one secret can't both get in). The hub also writes a fresh one when a node is revoked, on `POST /api/nodes/pairing/regenerate`, and at start once any node has paired. A fresh install still pairs by itself: the hub writes the file on first start and the node's first connection spends it. The node re-reads the file whenever it pairs, so it picks up a regenerated secret without a restart.
- **Pairing never replaces a node.** A pairing under the name of a live node is refused with `conflict` and leaves the secret unspent. Replacing a node is an admin's revoke, then a pairing.
- **A next key must be new.** `node.rotate` refuses a key that's any node's key or announced next key, or a hub key; lookups find a key's holder before anyone who announced it.
- **Deploying a stack is root on the node's host, by design.** The node's guard (`internal/node/stacks/guard.go`) always refuses the Docker socket, `/`, the node's own directories and volumes and the pairing volume (and paths that contain them), `volumes_from` another container, env and label files outside the stack's directory, `include`, and `extends` from another file, checked before the loader reads anything and again on the file as it runs with real secret values. Privileged containers, host namespaces, devices, `cap_add` and host paths are allowed unless the node's `stacks.deny` lists them, and every plan lists them as root-equivalent; the deploy confirmation leads with that line. A deploy, plan or rollback that would recreate or remove a container `docker.protect` names is refused.
- **Rollbacks send only granted secrets.** The version to restore and the names it references come from the node, so a rollback sends only names an earlier deploy (whose draft is the hub's own) sent to that stack on that node (`stack_secret_grants`); validation and plans take names from the hub's own reading of the draft. The vault namespace stays `stacks/<name>`.

**Demo mode** runs real instances too, but against a private topic set nobody subscribes to: CRUD, tests, status and diagnostics work (the web app's integration screens are built against `bun run dev`, which runs the demo), and nothing reaches the Demo Home. Restart without `--demo` and the same instances feed the real graph.
- **The config schema** is a JSON Schema subset (`integration.Schema`): types, properties in order, required, `additionalProperties: false`, items, enum (with titles, sent as `oneOf: [{const, title}]` beside it, which forms show), default, `uri`/`hostname` formats, pattern, length and range bounds, `writeOnly`. `uri` refuses credentials in the URL. Anything else is the integration's `ValidateConfig`. Empty strings and nulls in a submitted config mean "not set".

**Home Graph**
- **Batches.** `Update(func(tx))` applies a batch atomically under the write lock and bumps the version once. A batch with no effective change doesn't bump it. Re-asserting identical facts is free.
- **Immutable values.** `Upsert` deep-copies JSON-like `Attrs`, and empty maps normalise to nil. Snapshots and patches share these values, so they can be serialised without locks.
- **Relations need existing endpoints**, and removing a resource removes its relations. A filtered view shows a relation only when both endpoints are visible, so views never dangle.
- **Diffs replace whole resources**, not fields. This is simple, and a resource is small.
- **Watchers are bounded** (4096 pending operations by default). On overflow they drop the queue and return `ErrLagged` until the consumer takes a fresh `Snapshot`.
- **The version starts at 0 in every process.** The `epoch` in the realtime `hello` tells clients the counter restarted.
- **Not modelled yet**: `SeenAt`/freshness (it would churn diffs; it needs its own channel), `Desired`, `Links`, `Caps`, per-source provenance, persistence of the graph cache. Demo mode carries desired state, links and caps in attrs until they become fields.
- At boot the hub inserts itself as `chadash:hub` (kind `app`, name "Cha Dash", `attrs.version` and `attrs.self`), except in demo mode. Integrations relate it to where it runs (Integrations → Actions, the self-aware T4); the web's Settings → Security shows the chain.

**Realtime**
- **A subscription's state is one JSON document** with a member per topic. `snap` carries it whole; `diff` is an RFC 6902 patch against it, with paths like `/graph/resources/<id>`. Clients can use any JSON Patch library. Ops address object members and never index into an array: arrays are replaced whole, or appended to with `add …/-`. That's what keeps `jsonpatch.Compact` sound now that ops nest (`/metrics/series/<key>` and `/metrics/series/<key>/value`): an op on a member makes earlier ops inside it moot, and later ones inside it stay after it.
- **`diff` frames carry the subscription `id`.** The architecture doc's example omits it, but a multiplexed socket needs it.
- **Coalescing**: at most one `diff` per subscription per 250 ms. The first change after a quiet period goes out immediately.
- **Backpressure**: each feed is bounded, and the connection has a 64-frame send queue. If a feed overflows, or a `diff` doesn't fit in the queue, the pending diffs are dropped and the subscription gets `snap` with `resync: true`. Memory stays bounded however slow the client is. A frame that can't be written within 10 s drops the connection.
- **`resume` is accepted and ignored** in v0. Every `sub` gets an inline snapshot, so reconnecting is always correct, though not minimal.
- **Additions** to the doc's op list: `unsub`, `error` (with codes), and a client `ping` answered with `pong`.
- **Handshake**: `hello` must arrive within 10 s and must be `v: 1`. Otherwise the client gets close code 1008, after an `unsupported_version` error frame when the version is wrong. Every 30 s the server sends `{"op":"ping"}` for the browser's watchdog, plus a protocol ping that drops peers which have silently vanished.
- **Origin**: same-origin is always allowed; `CHADASH_ALLOWED_ORIGINS` adds more. Auth is the session cookie, checked before the upgrade; the connection keeps that identity (on its context) until it closes, so see Sessions above for how it's closed when the identity stops standing.
- **Topics**: see [Realtime topics](#realtime-topics). `realtime.Mux` routes a topic by the name before its first `:`; lens topics plug in as more `Source`s.
- **Live documents** (`internal/livedoc`) back the topics that aren't the graph. Values are stored encoded, so they're immutable and compare exactly; `reflect.DeepEqual` would call every unknown (NaN) metric a change. Like the graph, a batch bumps the version once and a no-op batch doesn't bump it.
- **Live-document diffs carry changed fields.** A member that appears or disappears goes out whole. One that changes, if it's an object before and after, gets an `add`/`replace`/`remove` per top-level field that changed, unless replacing it whole is no bigger. This is shallow and general (the inbox gets it too), not a metrics special case. Field values go out whole, so a history that gains a point is one `replace` of the array, never `add …/history/-` plus `remove …/history/0`. The one exception: an array that only grew at its end (a task's log) gets an `add …/-` per new element when that's smaller. `Compact` never folds an append; it drops one only when a later op on the array or a parent makes it moot. A 200-line task log sends about 53 kB instead of about 790 kB (`TestBudgetTaskLog`). The diff splits the stored encodings without copying, and only runs when someone is watching.
- **What that saves.** In demo mode a tab subscribed to every topic gets ~42 kB per simulated minute, down from ~128 kB with whole series (13:00–20:30, `TestDiffBytesPerMinute`). About 70% of what's left is each series' `as_of`, which every tick changes; moving it out of the series (one tick time per document) would cut the rest to ~15 kB, at the cost of a shape change.
- **More ops per tick.** A metrics tick is now ~300 ops (two per series) instead of 152, so a feed that isn't drained lags after ~13 ticks instead of ~27 (the bound is 4096 pending ops). A subscription drains on every notification, so in practice only a client blocked on a resync snapshot gets near it.
- **`seq` is per topic and informational.** A subscription's `seq` is the highest of its topics' versions, which are separate counters.
- **Topics publish separately.** One demo tick updates the graph, metrics and inbox in turn, so a subscription may see it as two diffs a moment apart. Each diff is correct on its own.
- **Shutdown**: hijacked connections aren't covered by `http.Server.Shutdown`, so `Serve` calls `Hub.Close` afterwards. That sends close code 1001 (going away), and new upgrades then get 1013 (try again later).

**Actions and audit**
- **One engine, one decision point.** Every action goes through `action.Engine`; whether an actor may do it is only ever `Policy.Authorize(actor, scope, target, tier)`. The hub runs `internal/authz`'s policy (`ForEngine`); a T4 approval spends the session's step-up grant, atomically with starting its task. Changes outside the engine are audited through `authz.Recorder` (on a context that outlives a client hanging up): users and settings, integration instances (names, the fields sent and the URL's host, never a value), board layouts and dismissals, correlation decisions, the first-run wizard and its discovery, imports and exports (counts and IDs), stack drafts and secrets, notifications, tokens, and the auth events (passkeys added, renamed or removed; step-ups granted or failed). Refusals outside the engine are recorded with result `refused`: the owner's protected account, invalid users, a token scope the role can't hand out, revoking a token that isn't yours, a failed step-up. Guard's role refusals aren't: they change nothing, and a misbehaving client would flood the chain. `authz/audited.go` lists what every unsafe route audits or why it doesn't; `authz.TestEveryUnsafeRouteIsAudited` and `server.TestAuditedRoutesRecord` keep it true. A token's records carry `actor.on_behalf_of`, its user. Admins don't see or revoke the owner's tokens. The policy finds the `Def` (for `Household`) with `action.DefFromContext`. Reads that aren't actions are scopes too: `audit.read`, and `tasks.all` for seeing other people's tasks.
- **Plans are bound to what was shown.** The hash covers the action, target, params, tier, blast radius and the target's state (for a graph resource its kind, name and state, not health or attrs, which move with every sync). Approve recomputes the state, asks the policy again (now with the step-up grant), checks the typed name for T3 and T4, and marks the plan used in the same transaction that creates the task, so a plan runs at most once. T4 also wants a grant that began after the plan: grants last 5 minutes, so the engine takes `StepUpUntil − 5 min` as when it began.
- **Tasks** run in the background on the engine's context, with a timeout (2 minutes, 30 for `Async`, plus the `Verify` timeout), a per-target lock, and a recovered panic as an `internal` failure. State and step changes are written to `tasks`; progress and log lines only go to the topic. A task that was running when the hub stopped is marked `failed` (`interrupted`) and audited at the next start. Shutdown cancels running tasks and waits for them to record how they ended.
- **The tasks topic is filtered per viewer.** `realtime.ContextSource` lets a source see the subscribing connection's context (the session); `Mux` and the runtime's `Watch` pass it on. `ViewerDocSource` decides once per item, when it first sees it whole, and then passes field ops and removals only for items the viewer holds, so every op applies to the viewer's document. The inbox uses `ProjectedDocSource` instead, because what household sees of an incident can change over its life.
- **Cancelling.** A task can be stopped only when its `Def` says `Cancellable` (false by default: instant T0 and T1 actions don't need it, and an action whose upstream can't be stopped mustn't claim it). Cancel ends the task's context; the task records itself `cancelled`, with its own audit record, and the request is audited separately as `task.cancel`. The Demo Home's fixes aren't cancellable: once pressed, the simulator's fix can't be taken back.
- **Listing actions** (`ActionsFor`, `GET /api/actions?target=`) runs the first half of planning for every registered action (resolve, tier, policy, preconditions) without saving or auditing anything. It's for ⌘K and tiles; preconditions should stay cheap reads, since every listing runs them. The policy is asked first, with the Def's static tier, so an action the caller may not run is never resolved. Then at most 4 actions resolve at once (`listParallel`), all within 3 s (`listDeadline`); one whose read failed or didn't finish in time is listed unavailable with a sentence ("Couldn't read its current state in time.") instead of holding up the list. Defs that read the same thing share one read per listing through `action.Memo(ctx, key, fn)` (outside a listing it just calls `fn`); Proxmox's guest actions use it, so a guest lists in about 2 API calls, not 20.
- **The audit log is append-only and hash-chained.** Each row keeps its canonical JSON exactly as hashed, and the chain links each event to the previous event's hash (see `internal/audit`'s package doc for the form). Triggers refuse `UPDATE` and `DELETE`, and a test fails if any non-test Go or SQL file in the repository updates or deletes `audit_events`. Secret-typed params (write-only properties of an action's schema) are redacted before hashing. Redaction is top-level only, so the registry refuses a `Def` whose params schema has a write-only property anywhere deeper (nothing needs one). The chain has no external anchor: someone who can write the database file can cut or rewrite it and recompute the hashes, so `verify` reports the head (seq and hash) for the owner to record elsewhere (SECURITY.md); a keyed chain is future work. Refusals are recorded at plan and approve time; a task's outcome when it ends, with its duration.
- **Task completions have one listener seam**, `Options.OnTaskDone`: called on the task's goroutine after its audit record, with the task as finished, its tier and its actor. `internal/notify` uses it to tell the admins about every T4. It must return quickly.
- **The Demo Home's fixes are actions.** One `Def` per suggested action ID in the day's script (not `link.open`), with the simulator's ID and tier, so the web plans exactly what an incident suggests. The plan finds the open incident; `Execute` presses the fix; `Verify` follows the incident's phases as task steps until it resolves (45 minutes at most). This is the only way to press one: the old `POST /api/demo/incidents/{id}/fix` is gone. Alternatives other than the suggested action aren't simulated, so planning them answers `404 not_found`.

**Demo mode**
- **`--demo` runs the Demo Home simulator inside the hub.** It reads nothing real. First-run setup and sign-in work exactly as in a real install; there's no bypass.
- **The simulator owns the graph.** Its snapshot replaces the graph's contents, and the hub doesn't insert `chadash:hub`: the Demo Home's `app:chadash` (`self: true`) stands for it.
- **Mapping.** Simulator resources keep every field. Those `graph.Resource` lacks go into attrs: `desired`, `health.reason`, `health.since` (RFC 3339), `health.url` (where the upstream shows why: a tile's health note links there), `links` (`[{"title","url"}]`) and `caps`. No simulator attr uses those keys. Attr values are normalised to plain JSON, so times become RFC 3339 strings. Health may be `repair`, the simulator's sixth status for an integration Cha Dash can't read.
- **Events map one to one**: `resource.upsert`/`remove` and `relation.upsert`/`remove` become one graph batch per step; `incident.*` replace the incident in `inbox`, a security finding at the posture's severity (the simulator declares its hints `info`, so only `strict` changes them; `Driver.SetPosture` re-publishes them at once). If a batch ever fails to apply, the driver reloads every topic from a fresh snapshot.
- **A resource's "since" survives a restart.** When the emitter first sees a resource without a `Health.Since` of its own, it asks `History.Since`, which looks past the `stopped` row a clean shutdown wrote. Every status change goes to `History.State` (in ID order, so it's deterministic), and a resource leaving a scope, or `Clear`, to `History.Gone`.
- **Real instances' sparklines are averages, and survive a restart.** The emitter keeps the same 48-point grid, but the slot it scrolls past becomes the average of its samples (the newest point is still the first sample of its slot, so the history still changes once every 30 minutes on the wire). Every valid sample goes to `internal/history`, and a series that appears takes its 47 older points from the 15-minute tier. Samples go in under the emitter's lock; the recorder only folds them in memory, and writes from its own goroutine every minute.
- **Metrics history scrolls every 30 minutes.** Each metrics event updates `value`, `as_of` and `stale` (a missing sample keeps the last-known value, marked stale). The 48-point history stays on the grid of the snapshot it was loaded from and gains a point when a sample lands on it. Sparklines don't shift under the reader every 30 s, and whenever the grid lines up with the clock the series equal a fresh simulator snapshot (a test checks this).
- **Clock.** By default (`local`) the simulated day starts at 14:05, with today's problems, repairs and standing hints open, and runs in real time on the monotonic clock. The page shows it as the hub's local date and time: the driver picks the 14:05 within 12 hours of the local wall clock and a whole-minute offset to it, `GET /api/demo` and the first paint name that offset as the display zone (`time_zone: "-08:05"`, `zone_label: "MDT"`), and the simulator quotes times in its copy shifted the same way (`sim.WithShownOffset`). The data stays on the simulated clock. `CHADASH_DEMO_AT=HH:MM` starts on the simulator's own day, 2026-10-03, shown in UTC; `now` uses the real date and time in UTC.
- **Ticks.** The driver wakes at each sample tick (every 30 s, on the Unix-epoch grid) and advances to the current time. `Fix` first catches up, so the fix lands at the right moment.
- **A new day at midnight.** The simulator has no repeat mode, so at simulated midnight the driver starts a fresh one for the next day with the same seed: the script replays, and the inbox empties until the first incident opens.
- **Clock jumps restart the day.** A gap of more than an hour (a suspended host, a board without an RTC fixing its clock) restarts the simulator at the new time instead of replaying hours of events.

**Web app**
- **History-mode routing.** A `GET`/`HEAD` for a path that isn't a file gets `index.html`, except under `/api`, `/ws` and `/assets/`. A missing hashed asset is usually a stale tab asking for an old chunk; `index.html` would be refused as a script, so it gets a plain `404`. Directories are never listed.
- **Caching.** Files under `/assets/` (hashed by Vite) get `public, max-age=31536000, immutable`; everything else, `index.html` included, gets `no-cache` plus a content-hash `ETag`, so revalidation answers `304`. Embedded files have no modification time to offer instead.
- **MIME types are pinned** for the types the SPA uses (js, css, woff2, svg, webmanifest, …). System tables differ between hosts and are often missing in containers, and `nosniff` makes a wrong type fatal.
- **The placeholder** (no `webui` tag) is plain HTML with no inline script or style, so it passes the CSP too.

**HTTP**
- **No `ReadTimeout`/`WriteTimeout`** on the server, because they would also cut WebSockets. There's a 10 s `ReadHeaderTimeout`, bodies are capped at 64 KiB (1 MiB for layout saves and imports), and the idle timeout is 2 minutes.
- **Security headers** on every response: CSP `default-src 'self'; base-uri 'none'; object-src 'none'; frame-ancestors 'none'; form-action 'self'`, plus `nosniff`, `X-Frame-Options: DENY`, `Referrer-Policy: no-referrer`, COOP and CORP `same-origin`, and a restrictive `Permissions-Policy`. API responses also get `no-store`. There's no HSTS, because the hub doesn't terminate TLS itself.
- **Icons are the hub's to fetch.** The CSP keeps tiles from loading other origins' images, so `internal/icons` fetches them: only the URLs the owner's tiles and instances name (an icon URL, else a link's or instance URL's origin, tried at `/favicon.ico`, `/favicon.svg`, `/apple-touch-icon.png`, `/favicon.png`), never a URL from a request. Every dialled address is checked after DNS (no loopback, link-local, metadata, multicast or unspecified addresses; LAN ranges allowed), one same-host redirect, 5 s to connect and 10 s in all, TLS verified, no proxy. A body must be at most 256 KiB and sniff as PNG, ICO, WebP or SVG, with a declared type that agrees; an SVG is parsed with `encoding/xml` and checked against an element and attribute allowlist (`checkSVG`; no scripts, event handlers, `foreignObject`, links, animation, `feImage`, DOCTYPE or entities; every `href` and CSS `url()` a `#fragment`), refused, not cleaned. The NAT64 and 6to4 forms of a refused IPv4 address are refused too (`httpx.EmbeddedIPv4`). Rows are `icons` (`00013`): fetched when first seen (half a minute after start, or when the web asks for the map), refreshed weekly, retried six-hourly after a failure (a failed refresh keeps the old image), forgotten 30 days after nothing names them. Never in demo mode.
- **`healthcheck`** dials loopback when the listen host is a wildcard, and ignores `HTTP(S)_PROXY`. It exits 1 on any failure, including bad flags, because Docker reserves exit code 2.

## Stubbed or deferred

- The incident engine (threshold detection on metrics); today only integrations raise incidents.
- Integrations: per-widget interest (lenses), persisting `last_ok`/`last_error`, rate budgets, version adapters as a framework feature, grants read-back for specs, re-pinning from the Repair, a hub-wide diagnostics bundle.
- The metrics history axis (start, step) isn't on the wire; series carry only their 48 points, 30 minutes apart, newest last.
- Demo actions beyond each incident's suggested fix, and a reset button.
- TOTP, OIDC, kiosk tokens. API tokens: per-token rate limits beyond the bad-token budget, a token's own tasks on its user's `tasks` topic (they're on the token's, and on admins'), and Web Push for approvals (10A's notifications can route the `approval` Inbox class).
- Passkeys: deleting one doesn't end the sessions it started (sessions don't record which credential began them, and there's no "sign out everywhere" yet). No attestation checks or FIDO metadata. The Signal API's `signalAllAcceptedCredentials` and `signalCurrentUserDetails`. Passkey-only accounts.
- Sign-ins in the audit log (they're logged; the audit log has passkey changes, step-ups and every setting).
- An external anchor for the audit chain (a keyed, HMAC chain, or publishing the head): today the owner records the head `verify` reports.
- Node follow-ups: remote enrollment with join tokens and Inbox approval (v0.3; `Hello.Token` is refused today), a UI for nodes and for hub key rotation (`Hub.RotateKey`, `Stranded`, `RetireOldKeys` exist, no routes), a trigger for node key rotation (`Client.RotateKey` exists), direct mode (the hub dials the node), exec, relay, host metrics, registry credentials for pulls, measuring the node's idle RSS against a real daemon.
- Action follow-ups: locks beyond the target resource; agents as actors (ADR-0017: the approval queue is ready, `KindAgent` waits on every plan, but no agent token can be minted yet).
- Vault key rotation and a keyed-hash fingerprint for spotting reused secrets. The importer finds reuse within one import with its own per-import HMAC key; reuse against secrets already in the vault needs the vault's fingerprint.
- Importer follow-ups: turning the record's `runs_on` links into confirmed correlation decisions; mapping `homepage.widget.type` of a label-discovered container to a catalog widget.
- Graph persistence (the last-known cache). Correlator follow-ups are in its [README](correlate/README.md): DNS ↔ IP matching, the nginx-proxy hop, MACs, cross-source `backed_up_by`.
- Export follow-ups: scheduled exports.
- Replaying diffs on resume. Brotli (gzip only today: the personalised Demo Home page is 182 kB, 31 kB gzipped).
- An SSE fallback for realtime.
- Notification follow-ups: Apprise, email and Home Assistant channels (v0.2.x, Decision 5); per-user rule choices and quiet hours; a test for an unsaved channel; ntfy over basic auth.
