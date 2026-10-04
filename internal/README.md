# Hub internals

The hub (`cmd/chadash`) is a single Go binary. This page maps its packages and
records the decisions that aren't obvious from the code. The architecture is
in [docs/plan/03-architecture.md](../docs/plan/03-architecture.md).

## Package map

| Package | Job |
|---|---|
| `cmd/chadash` | Entry point: `serve`, `healthcheck`, `version`. Wires everything together in `newApp` |
| `internal/server` | HTTP routes, the middleware chain, graceful `Serve` |
| `internal/auth` | First-run setup token, argon2id passwords, passkeys (WebAuthn), cookie sessions, attempt limiting |
| `internal/boards` | Boards and their layouts: optimistic revisions, history, the boards API |
| `internal/importer` | The shared importer pipeline: the plan shape, layout documents, and a transactional, idempotent apply |
| `internal/importer/homepage` | The Homepage importer: parses `services.yaml`, `docker.yaml` and `settings.yaml` into a plan; the import API. Mapping table in its [README](importer/homepage/README.md) |
| `internal/integration` | The contract every integration implements: `Integration`, `Instance`, `Manifest` (config JSON Schema), `TestResult`, the facts and inbox shapes, error classes, the registry |
| `internal/integration/integrationtest` | A harness for contract tests: an integration on the runtime's real HTTP clients and emitter, publishing into a private graph |
| `internal/runtime` | Runs instances: a coordinator each, the per-instance HTTP client (TLS policy, session cache), secrets from the vault, the fact emitter, the integrations API |
| `internal/spec` | The Tier 1 spec engine: YAML specs with gjson extraction and expr-lang expressions |
| `integrations/<id>` | Tier 0 integrations, one folder each; `checks` is the first. Linked in by `cmd/chadash/integrations.go` |
| `specs` | The bundled Tier 1 specs (`<id>.yaml`), embedded and registered at init |
| `internal/demo` | Demo mode: runs the Demo Home simulator (`sim/`) and maps it onto the topics |
| `internal/topics` | The topic set (`graph`, `metrics`, `inbox`), their document shapes and routing |
| `internal/realtime` | WebSocket gateway, protocol v1 (`hello` / `sub` / `snap` / `diff` / `ping`); `GraphSource`, `DocSource`, `Mux` |
| `internal/graph` | Home Graph v0: resources, relations, version, watchers |
| `internal/livedoc` | Live JSON documents for topics that aren't the graph: values, keyed collections, watchers |
| `internal/jsonpatch` | The RFC 6902 subset used on the wire, plus `Compact` for coalescing |
| `internal/webui` | Serves the SPA, embedded with `-tags webui`; a placeholder page otherwise |
| `internal/vault` | Envelope-encrypted secrets |
| `internal/store` | SQLite (ncruces) connections, pragmas, embedded goose migrations |
| `internal/httpx` | Client IP and scheme behind trusted proxies, JSON I/O, shared middleware |
| `internal/config` | `CHADASH_*` env and flags |
| `internal/buildinfo` | Version and commit stamped with `-ldflags` |

Dependencies point down: `cmd → server → {auth, boards, demo, realtime, importer/homepage, runtime} → {importer, integration, topics, graph, livedoc, vault, store} → …`.
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
| `--demo-at` | `CHADASH_DEMO_AT` | `14:05`; a time of day (`HH:MM[:SS]`, UTC) on the demo's day, or `now` for the real clock |
| `--demo-seed` | `CHADASH_DEMO_SEED` | `1` |
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
| `GET /api/setup` | none | `{"required": bool}` |
| `POST /api/setup` | setup token | `{"token","username","password"}` → `201 {"user"}` + session cookie. `403` wrong token, `422` invalid input (token stays valid), `409` already done |
| `POST /api/auth/login` | none | `{"username","password"}` → `200 {"user"}` + cookie, `401` otherwise |
| `POST /api/auth/logout` | cookie (optional) | `204`; deletes the session server-side and clears the cookie |
| `GET /api/auth/session` | session | `{"user","session"}`. `session.method` is `password` or `passkey` |
| `POST /api/auth/passkeys/register/begin` | owner | No body → `200 {"publicKey": creation options}`, plus the ceremony cookie |
| `POST /api/auth/passkeys/register/finish` | owner | The new credential's `toJSON()` → `201 {"id","name"}`. `400 ceremony_expired`, `400 passkey_rejected`, `409 passkey_exists` |
| `POST /api/auth/passkeys/login/begin` | none | No body → `200 {"publicKey": request options}`, plus the ceremony cookie |
| `POST /api/auth/passkeys/login/finish` | none | The assertion's `toJSON()` → `200 {"user"}` + session cookie. `400 ceremony_expired`; `401 passkey_rejected`; `401 unknown_passkey` with `"signal":{"rpId","credentialId"}` |
| `GET /api/auth/passkeys` | owner | `[{"id","name","created_at","last_used_at","rp_id","backed_up","clone_warning"}]`, oldest first. `last_used_at` is `null` until first use |
| `PATCH /api/auth/passkeys/{id}` | owner | `{"name"}` → `200` the passkey. `422` empty, over 64 characters or control characters; `404` |
| `DELETE /api/auth/passkeys/{id}` | owner | `204`. `404`; `409 last_sign_in_method` if it's the only way left to sign in |
| `GET /api/version` | session | `{"version","commit"}` |
| `GET /api/boards` | owner | `{"boards":[{"id","name"}]}`. The first call creates a `Home` board |
| `GET /api/boards/{id}/layout` | owner | `{"revision","format_version","layout"}`. A board never saved gives `{"revision":0,"format_version":0,"layout":null}`; the SPA then creates the first layout. `404` unknown board |
| `PUT /api/boards/{id}/layout` | owner | `{"revision","format_version","layout"}`, where `revision` is the one the edit started from → `200 {"revision": n+1}`. Stale → `409 {"error":{"code":"conflict",…},"revision": current}`. `422` if `revision` < 0, `format_version` < 1 or `layout` isn't an object. Up to 1 MiB |
| `POST /api/import/homepage?dry_run=1` | owner | `{"services_yaml","docker_yaml","settings_yaml"}` (only `services_yaml` is required) → `200 {"plan","hints","unmapped"}`; the plan's `changes` say what applying would create, update or leave alone. Writes nothing. `422` for invalid YAML (the message names the file and line, never the text). Up to 1 MiB |
| `POST /api/import/homepage` | owner | The same body → `200 {"board_id","instance_ids","changes"}`, applied in one transaction. Re-importing the same files changes nothing |
| `GET /api/import/homepage` | owner | `{"record"}`: the last import's tiles, links, discovery entries and instance ids. `404` before the first import |
| `GET /api/integrations/catalog` | owner | `[{"id","name","icon","version","tier","capabilities","config_schema"}]`, by id. `config_schema` is JSON Schema; secret fields are top-level strings with `"writeOnly": true` |
| `GET /api/integrations/instances` | owner | `[instance]`, by integration and name. An instance is `{"id","integration","name","enabled","status","version","last_ok","last_error","last_error_code","last_error_at","warnings","config","secrets","created_at","updated_at"}`: `status` is `ok`, `degraded`, `repair`, `error` or `disabled`; `last_error` is the last failure's message (or `null`), with its stable code and time beside it; `warnings` are `[{"code","severity","message"}]`; `config` never holds a secret or a `${secret:…}` reference; `secrets` is `{"<field>":{"set":bool}}` for every write-only field |
| `POST /api/integrations/instances` | owner | `{"integration","name","config","secrets","enabled"?}` → `201 {"id"}`. `422 invalid` with `"fields":[{"path","message"}]` (JSON Pointers into the config), `422 unknown_integration`, `409 conflict` for a name the integration already has |
| `GET /api/integrations/instances/{id}` | owner | One instance. `404` |
| `PUT /api/integrations/instances/{id}` | owner | `{"name"?,"config"?,"secrets"?,"enabled"?}` → `200` the instance. `config` replaces the whole non-secret config (send `tls.pin` back to keep a TOFU pin); in `secrets` a string sets a field, `null` or `""` clears it, and fields left out keep their value. Restarts the instance |
| `DELETE /api/integrations/instances/{id}` | owner | `204`. Removes the row, its facts, and its vault entries no other instance references |
| `POST /api/integrations/test` | owner | `{"integration","config","secrets"}`, or unsaved edits of a saved instance: `{"instance_id","config"?,"secrets"?}` (secrets not sent come from the vault) → `200 {"ok","version","latency_ms","grants","warnings","error","certificate"?}`. Saves nothing. `grants` are `[{"name","access":"read\|write\|rw\|admin","write","detail"?}]`; `error` is `{"code","message"}` or `null`; `certificate` is `{"sha256"}` of the leaf the server presented |
| `POST /api/integrations/instances/{id}/test` | owner | The same result for the saved config. In `tofu` mode with no pin yet, the certificate seen is pinned |
| `POST /api/integrations/authorize/{integration}` | owner | Start a sign-in flow for an integration with an `Authorizer` (Plex's PIN) → `200 {"id","code","url","expires_at","interval_s"}`; 422 `no_sign_in` without one. Flows live in memory (at most 32, 30 minutes each) and always verify TLS |
| `GET /api/integrations/authorize/{integration}/{id}` | owner | `{"status":"pending"\|"expired"\|"failed"\|"done"}`; `done` carries `secrets` (write-only fields only) exactly once, then the flow is forgotten |
| `GET /api/integrations/instances/{id}/diagnostics` | owner | A bundle for bug reports, as an attachment: hub and manifest versions, the instance (secrets as `{"set"}`), schedule, TLS mode and pin, fact counts and the last 20 attempts. No secret values |
| `GET /api/demo` | owner | Demo mode only: `{"enabled":true,"now":RFC 3339,"seed":n}`. `now` is the simulated time |
| `POST /api/demo/incidents/{id}/fix` | owner | Demo mode only: presses the incident's suggested action → `202 {"id"}`. `404` unknown incident, `409` not open yet, already fixing or resolved. Progress arrives on the `inbox` topic |
| `GET /ws` | session | WebSocket; see [`realtime/protocol.go`](realtime/protocol.go) and the topics below |
| `/api/*` (other) | — | JSON `404`; so are the demo routes outside demo mode |
| `GET /…` | none | The web app. Unknown paths get `index.html` (history-mode routing) |

Errors are `{"error":{"code","message"}}`. Attempts are limited per client IP:
10, then one every 6 s, answered with `429` and `Retry-After`. Password
sign-in and passkey `login/finish` draw on one budget; `/setup` and passkey
`login/begin` each have their own. Every passkey `begin` and `finish` answers
`409 passkeys_unavailable`, with a message saying what to change, when
passkeys can't work at the address in use. "Owner" routes need a session whose
user has the owner role; today every account is the owner, and later roles get
`403`.

Passkey options and credentials are the WebAuthn JSON forms, with binary fields
as unpadded base64url. The SPA hands `publicKey` to
`PublicKeyCredential.parseCreationOptionsFromJSON` (or
`parseRequestOptionsFromJSON`), calls `navigator.credentials.create`/`get`, and
posts the credential's `toJSON()` to `finish` as is. `@simplewebauthn/browser`
produces the same shapes. Autofill (`mediation: "conditional"`) needs nothing
from the hub: it uses the same `login/begin` options.

## Realtime topics

Each topic's document is a member of the subscription document, so its diff
paths start with `/<topic>/`.

| Topic | Document | Changes |
|---|---|---|
| `graph` | `{"resources":{id: resource},"relations":{key: relation}}` | Whole resources and relations; see `internal/graph` |
| `graph:kind/<kind>[,<kind>…]` | The same, filtered to those kinds | As above |
| `metrics` | `{"series":{"<resource id>#<metric key>": series}}` | Each sample tick, the fields of each series that changed: `/series/<key>/value`, `/as_of`, and `/stale` when it flips. Every 30 minutes, when the history gains a point, `/history` whole (or the whole series, when that's smaller). Whole series when one appears or disappears |
| `inbox` | `{"now": RFC 3339, "incidents":{id: incident}}` | The fields of an incident that changed, or the whole incident when it appears or disappears; `/now` each tick |

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
- **Return errors from `Sync`** and let the runtime classify them: `CheckResponse` for HTTP status, `&integration.AuthError{Field: …}` for an auth failure reported inside a 200 (say which secret when you know), `&integration.RepairError{Code: integration.CodeAPIChanged, …}` for a vanished endpoint or `CodeUnsupportedVersion`, `&integration.ConfigError{…}` for a config that can't work. Don't retry or sleep: the coordinator backs off.
- **Publish complete scopes** with `d.Emit.Replace(scope, facts)`: resources (namespaced, stable IDs), relations (`d.Emit.Self()` `monitors` what you read), metrics (keys like the simulator's, unknown as `integration.Unknown()`, never 0). Use one scope, or one per refresh rate.
- **Respect the interest.** `want.Watched` false means nobody's looking: report health, skip the expensive parts. Set `Manifest.Poll` (interval while watched, idle interval, timeout) to the spike's polling plan.
- **`Test`** reports the upstream version and, where the source can tell, the grants it read back, `Write: true` (or `Access`) on any that can change things; the runtime warns about write grants on read-only connections. Use `integration.Failed(err)` on failure.
- **Push** is optional: implement `Subscribe(ctx, emit)`; it runs only while someone watches.
- **`Do` is a stub.** Actions arrive in v0.2.
- **Fixture tests** against `testdata/integrations/<id>/` with `integrationtest`: `Validate` the config shapes the importer writes, `Connect`, `Test`, `Sync`, then read back `Resource`, `Series` and `Incident`.

**Tier 1 specs** (internal and unstable until v1.0). `specs/<id>.yaml`,
embedded and registered by the `specs` package; `specs/seerr.yaml` is the
sample, with a fixture test.

```yaml
apiVersion: chadash/v1alpha1
kind: Integration
meta: { id: seerr, name: Seerr, icon: seerr, version: 0.1.0, tier: bronze, capabilities: [media.requests] }
config:                           # url and tls are always added; types: string, url, secret, integer, number, boolean
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
metrics:
  - { resource: app, key: requests.pending, value: counts.pending, unit: count, min: 0, max: 100, band: [0, 5], warn: 0, crit: 0 }
repairs:
  - { when: 'status.version startsWith "0."', code: unsupported_version, message: '"too old"' }
```

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
- **`audit`** is a stub table with an `UPDATE` trigger that aborts. Retention may delete rows; nothing writes to it yet.

**First run and sessions**
- **Setup token**: `cds_` + 128 random bits. It lives in memory only and is logged once at `warn`, so it survives a quiet log level. A restart before setup logs a fresh one. A wrong token or invalid input leaves it valid; success spends it.
- **Exactly one owner**, enforced by a partial unique index. That also makes concurrent setup requests race-free at the database level.
- **argon2id at OWASP's baseline** (19 MiB, t=2, p=1), with at most two hashes running at once. That keeps a login inside the RSS budget on small ARM boards. Parameters live in each PHC string, so they can be raised later. Unknown usernames are checked against a dummy hash, so timing doesn't reveal which usernames exist.
- **Passwords**: at least 12 characters, at most 512 bytes. **Usernames**: 1–64 of `A–Z a–z 0–9 . _ @ -`, case-insensitive.
- **Sessions**: a 256-bit token in the cookie; only its SHA-256 is stored, next to a public `s_…` ID for listing and audit. **30-day idle expiry that renews while in use:** at most once a day, a request slides `expires_at` forward and reissues the cookie, so a session in regular use never lapses and an unused one expires after 30 days. `last_seen_at` is written at most every 5 minutes. A `method` column (`password` or `passkey`) is there for step-up checks later.
- **Cookie** `chadash_session`: `HttpOnly`, `SameSite=Lax`, `Path=/`. `Secure` when the request came over TLS, or from a trusted proxy that sent `X-Forwarded-Proto: https`. There's no `__Host-` prefix, because plain-HTTP LAN installs are common and that prefix requires `Secure`.
- **CSRF**: `SameSite=Lax`, plus net/http's `CrossOriginProtection` on every unsafe method (it uses `Sec-Fetch-Site`, falling back to Origin vs Host), plus JSON-only request bodies, which a cross-site form can't send. There are no per-request CSRF tokens. The architecture doc lists them; these three layers replace them on purpose, and tokens can still be added if a form-based flow ever needs them.
- **One seam**: `auth.Service.StartSession(w, r, user, method)` is where every sign-in method ends: password, passkey, and later OIDC.
- **Rate limiting** is per client IP and in memory. Behind a reverse proxy, set `CHADASH_TRUSTED_PROXIES`; otherwise every client shares the proxy's bucket.
- **Forwarded headers** are honoured only from trusted proxies. The client IP is the rightmost `X-Forwarded-For` hop that isn't a trusted proxy.

**Passkeys**
- **`github.com/go-webauthn/webauthn` v0.18.2** does the ceremonies. It adds about 2 MB to the stripped binary: it links every attestation format (TPM, Android, SafetyNet) even though we ask for none.
- **Discoverable credentials, usernameless sign-in.** Registration asks for a resident key (`residentKey: required`), prefers user verification, and wants no attestation: a homelab has no use for authenticator certificates, and asking for them makes browsers prompt. Sign-in sends no `allowCredentials`; the authenticator returns the user handle and the hub finds the passkey by its credential ID.
- **Relying party.** With `CHADASH_PUBLIC_URL` set, the RP ID is its host and the origin is the URL. Without it, the hub derives both from the request's `Host`, but only for `localhost` and `*.localhost`. Browsers allow WebAuthn only over HTTPS or on localhost and never on an IP address (the library refuses IP RP IDs too), so `127.0.0.1` gets "open `http://localhost:<port>` instead", and any other address gets "set `CHADASH_PUBLIC_URL` to the `https://` address you open Cha Dash at". A request whose `Origin` doesn't match the relying party is refused up front with both addresses in the message, instead of failing later in the browser.
- **Local development.** The root `bun run dev` serves the SPA at `http://localhost:5173` and proxies `/api` to the hub without rewriting `Host` (`changeOrigin: false`), so passkeys work there with no configuration: RP ID `localhost`, origin `http://localhost:5173`. Open `localhost`, not `127.0.0.1`. Passkeys made there belong to RP ID `localhost` and never sign in anywhere else.
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
- **Two halves.** A source package (`importer/homepage`) parses and maps, purely: no database, no network. `importer.Service` compares the result with the hub (`Diff`, the dry run) and applies it (`Apply`). Homarr and plain YAML (v0.2) reuse the second half.
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
- **"Watched" is hub-wide and coarse.** `runtime.Watch` wraps the realtime `Source` and counts open feeds: any `graph` or `metrics` subscription makes every instance watched, and when every graph subscription is a `graph:kind/…` filter, `Interest.Kinds` lists those kinds. The inbox alone doesn't count. Opening a tab makes a stale instance sync at once. Per-widget interest needs lens topics.
- **Status** is derived: `disabled`; `repair` after a repair-class error; `error` after 3 transient failures in a row, a config error, a missing secret or an unknown integration; `degraded` while starting, after 1–2 failures, or with sync warnings (`Emit.SetWarnings`); else `ok`. `last_ok` and `last_error` are in memory and start empty after a restart.
- **The instance is a resource**, `integration:<instance id>` (kind `integration`, source `hub`), with `attrs.type`, `tier` (`go`/`spec`), `quality`, `status` and `upstream_version`, health `ok`, `attention`, `repair` or `unknown` while connecting. What it publishes has `source` = that ID. A failed sync marks every series of the instance stale (last-known value kept) and leaves resources as they were: a broken integration isn't an outage.
- **Repairs** are inbox incidents `<instance id>.repair`, class `repair`, target the instance resource, scenario `auth-rejected`, `tls-pin-mismatch`, `tls-untrusted`, `api-changed` or `needs-repair`, with `facts.instance`, `facts.code`, `facts.status` (the HTTP status, when there is one) and `facts.field` (the integration's, `tls` for certificates, else the first secret for a rejected credential) and the instance's resources (up to 20) as the blast radius. **Phases are a lifecycle**: `open`, `fixing` once the user changes the instance, `resolved`. Resolved incidents stay 24 h.
- **The HTTP client** per instance: no proxy, 5 s dial, 10 s TLS handshake, 15 s per request, a 16 MiB response cap, `User-Agent: chadash/<version> (+https://dash.cha.sh)`, redirects followed only within the origin (5 at most), and an optional `Session` hook (log in once, re-login and retry once on 401/403, log out on stop).
- **TLS policy, per instance.** `verify` is crypto/tls's own check with the URL host. `pin` accepts the pinned leaf, or a pinned CA that's in the presented chain *and* that the leaf verifies against, without checking the host (Proxmox is reached by aliases its certificate doesn't name). `tofu` with no pin accepts the first certificate and records its SHA-256 in `config.tls.pin` (a test of an unsaved config only reports it); after that it's `pin`, and a mismatch is a Repair. `insecure` checks nothing and the test warns. Every refusal reports the presented fingerprint, so the UI can offer to trust it.
- **Tests** connect a fresh instance with a discarding emitter, under 30 s, and add the framework's warnings: write grants on a read-only connection (all of v0.1's are), insecure TLS on https, credentials over plain http.
- **Demo mode** runs real instances too, but against a private topic set nobody subscribes to: CRUD, tests, status and diagnostics work (the web app's integration screens are built against `bun run dev`, which runs the demo), and nothing reaches the Demo Home. Restart without `--demo` and the same instances feed the real graph.
- **The config schema** is a JSON Schema subset (`integration.Schema`): types, properties in order, required, `additionalProperties: false`, items, enum, default, `uri`/`hostname` formats, pattern, length and range bounds, `writeOnly`. `uri` refuses credentials in the URL. Anything else is the integration's `ValidateConfig`. Empty strings and nulls in a submitted config mean "not set".

**Home Graph**
- **Batches.** `Update(func(tx))` applies a batch atomically under the write lock and bumps the version once. A batch with no effective change doesn't bump it. Re-asserting identical facts is free.
- **Immutable values.** `Upsert` deep-copies JSON-like `Attrs`, and empty maps normalise to nil. Snapshots and patches share these values, so they can be serialised without locks.
- **Relations need existing endpoints**, and removing a resource removes its relations. A filtered view shows a relation only when both endpoints are visible, so views never dangle.
- **Diffs replace whole resources**, not fields. This is simple, and a resource is small.
- **Watchers are bounded** (4096 pending operations by default). On overflow they drop the queue and return `ErrLagged` until the consumer takes a fresh `Snapshot`.
- **The version starts at 0 in every process.** The `epoch` in the realtime `hello` tells clients the counter restarted.
- **Not modelled yet**: `SeenAt`/freshness (it would churn diffs; it needs its own channel), `Desired`, `Links`, `Caps`, per-source provenance, persistence of the graph cache. Demo mode carries desired state, links and caps in attrs until they become fields.
- At boot the hub inserts itself as `chadash:hub` (kind `app`), except in demo mode.

**Realtime**
- **A subscription's state is one JSON document** with a member per topic. `snap` carries it whole; `diff` is an RFC 6902 patch against it, with paths like `/graph/resources/<id>`. Clients can use any JSON Patch library. Ops address object members and never index into an array (arrays are replaced whole). That's what keeps `jsonpatch.Compact` sound now that ops nest (`/metrics/series/<key>` and `/metrics/series/<key>/value`): an op on a member makes earlier ops inside it moot, and later ones inside it stay after it.
- **`diff` frames carry the subscription `id`.** The architecture doc's example omits it, but a multiplexed socket needs it.
- **Coalescing**: at most one `diff` per subscription per 250 ms. The first change after a quiet period goes out immediately.
- **Backpressure**: each feed is bounded, and the connection has a 64-frame send queue. If a feed overflows, or a `diff` doesn't fit in the queue, the pending diffs are dropped and the subscription gets `snap` with `resync: true`. Memory stays bounded however slow the client is. A frame that can't be written within 10 s drops the connection.
- **`resume` is accepted and ignored** in v0. Every `sub` gets an inline snapshot, so reconnecting is always correct, though not minimal.
- **Additions** to the doc's op list: `unsub`, `error` (with codes), and a client `ping` answered with `pong`.
- **Handshake**: `hello` must arrive within 10 s and must be `v: 1`. Otherwise the client gets close code 1008, after an `unsupported_version` error frame when the version is wrong. Every 30 s the server sends `{"op":"ping"}` for the browser's watchdog, plus a protocol ping that drops peers which have silently vanished.
- **Origin**: same-origin is always allowed; `CHADASH_ALLOWED_ORIGINS` adds more. Auth is the session cookie, checked before the upgrade.
- **Topics**: see [Realtime topics](#realtime-topics). `realtime.Mux` routes a topic by the name before its first `:`; lens topics plug in as more `Source`s.
- **Live documents** (`internal/livedoc`) back the topics that aren't the graph. Values are stored encoded, so they're immutable and compare exactly; `reflect.DeepEqual` would call every unknown (NaN) metric a change. Like the graph, a batch bumps the version once and a no-op batch doesn't bump it.
- **Live-document diffs carry changed fields.** A member that appears or disappears goes out whole. One that changes, if it's an object before and after, gets an `add`/`replace`/`remove` per top-level field that changed, unless replacing it whole is no bigger. This is shallow and general (the inbox gets it too), not a metrics special case. Field values go out whole, so a history that gains a point is one `replace` of the array, never `add …/history/-` plus `remove …/history/0`: array ops can't be compacted safely. The diff splits the stored encodings without copying, and only runs when someone is watching.
- **What that saves.** In demo mode a tab subscribed to every topic gets ~47 kB per simulated minute, down from ~128 kB with whole series (13:00–20:30, `TestDiffBytesPerMinute`). About 70% of what's left is each series' `as_of`, which every tick changes; moving it out of the series (one tick time per document) would cut the rest to ~15 kB, at the cost of a shape change.
- **More ops per tick.** A metrics tick is now ~300 ops (two per series) instead of 152, so a feed that isn't drained lags after ~13 ticks instead of ~27 (the bound is 4096 pending ops). A subscription drains on every notification, so in practice only a client blocked on a resync snapshot gets near it.
- **`seq` is per topic and informational.** A subscription's `seq` is the highest of its topics' versions, which are separate counters.
- **Topics publish separately.** One demo tick updates the graph, metrics and inbox in turn, so a subscription may see it as two diffs a moment apart. Each diff is correct on its own.
- **Shutdown**: hijacked connections aren't covered by `http.Server.Shutdown`, so `Serve` calls `Hub.Close` afterwards. That sends close code 1001 (going away), and new upgrades then get 1013 (try again later).

**Demo mode**
- **`--demo` runs the Demo Home simulator inside the hub.** It reads nothing real. First-run setup and sign-in work exactly as in a real install; there's no bypass.
- **The simulator owns the graph.** Its snapshot replaces the graph's contents, and the hub doesn't insert `chadash:hub`: the Demo Home's `app:chadash` (`self: true`) stands for it.
- **Mapping.** Simulator resources keep every field. Those `graph.Resource` lacks go into attrs: `desired`, `health.reason`, `health.since` (RFC 3339), `links` (`[{"title","url"}]`) and `caps`. No simulator attr uses those keys. Attr values are normalised to plain JSON, so times become RFC 3339 strings. Health may be `repair`, the simulator's sixth status for an integration Cha Dash can't read.
- **Events map one to one**: `resource.upsert`/`remove` and `relation.upsert`/`remove` become one graph batch per step; `incident.*` replace the incident in `inbox`. If a batch ever fails to apply, the driver reloads every topic from a fresh snapshot.
- **Metrics history scrolls every 30 minutes.** Each metrics event updates `value`, `as_of` and `stale` (a missing sample keeps the last-known value, marked stale). The 48-point history stays on the grid of the snapshot it was loaded from and gains a point when a sample lands on it. Sparklines don't shift under the reader every 30 s, and whenever the grid lines up with the clock the series equal a fresh simulator snapshot (a test checks this).
- **Clock.** By default the simulated day is the simulator's own, 2026-10-03, starting at 14:05 with all six incidents open, and runs in real time on the monotonic clock. `CHADASH_DEMO_AT=now` uses the real date and time instead.
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
- **`healthcheck`** dials loopback when the listen host is a wildcard, and ignores `HTTP(S)_PROXY`. It exits 1 on any failure, including bad flags, because Docker reserves exit code 2.

## Stubbed or deferred

- The incident engine (threshold detection on metrics); today only integrations raise incidents.
- Integrations: actions (`Do`), per-widget interest (lenses), persisting `last_ok`/`last_error`, rate budgets, version adapters as a framework feature, grants read-back for specs, re-pinning from the Repair, a hub-wide diagnostics bundle.
- The metrics history axis (start, step) isn't on the wire; series carry only their 48 points, 30 minutes apart, newest last.
- Demo actions beyond each incident's suggested fix, and a reset button.
- TOTP, OIDC, roles beyond owner, API and kiosk tokens, step-up (re-verify with a passkey for T3+).
- Passkeys: deleting one doesn't end the sessions it started (sessions don't record which credential began them, and there's no "sign out everywhere" yet). No attestation checks or FIDO metadata. The Signal API's `signalAllAcceptedCredentials` and `signalCurrentUserDetails`. Passkey-only accounts.
- Revoking live WebSockets when a session is logged out or expires. A socket stays open until it reconnects.
- Writing to `audit`; nothing records auth events yet.
- Vault key rotation and a keyed-hash fingerprint for spotting reused secrets. The importer finds reuse within one import with its own per-import HMAC key; reuse against secrets already in the vault needs the vault's fingerprint.
- Importer follow-ups: a `service` widget in the web catalog for the generic tiles, a tile-config home for the record's tile metadata, turning `runs_on` links and discovery entries into graph relations and Docker label discovery (wave 5), and the Inbox items for hints.
- Graph persistence (the last-known cache) and identity resolution (the correlator).
- Replaying diffs on resume; the inline snapshot embedded in `index.html`.
- An SSE fallback for realtime.
