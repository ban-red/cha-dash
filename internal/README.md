# Hub internals

The hub (`cmd/chadash`) is a single Go binary. This page maps its packages and
records the decisions that aren't obvious from the code. The architecture is
in [docs/plan/03-architecture.md](../docs/plan/03-architecture.md).

## Package map

| Package | Job |
|---|---|
| `cmd/chadash` | Entry point: `serve`, `healthcheck`, `version`. Wires everything together in `newApp` |
| `internal/server` | HTTP routes, the middleware chain, graceful `Serve` |
| `internal/auth` | First-run setup token, argon2id passwords, cookie sessions, attempt limiting |
| `internal/realtime` | WebSocket gateway, protocol v1 (`hello` / `sub` / `snap` / `diff` / `ping`) |
| `internal/graph` | Home Graph v0: resources, relations, version, watchers |
| `internal/jsonpatch` | The RFC 6902 subset used on the wire, plus `Compact` for coalescing |
| `internal/vault` | Envelope-encrypted secrets |
| `internal/store` | SQLite connections, pragmas, embedded goose migrations |
| `internal/httpx` | Client IP and scheme behind trusted proxies, JSON I/O, shared middleware |
| `internal/config` | `CHADASH_*` env and flags |
| `internal/buildinfo` | Version and commit stamped with `-ldflags` |

Dependencies point down: `cmd → server → {auth, realtime} → {graph, store} → …`.
`realtime` sees topics only through its `Source` interface; `graph` doesn't
know about WebSockets. Domain packages (`auth`, `vault`) write their own SQL
against tables that `store` migrates; there is no repository layer yet.

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
| `healthcheck --timeout` | — | `3s` |

The data directory holds `chadash.db` (plus `-wal`/`-shm`) and `secret.key`.
It's created `0700`; the database and key files are `0600`.

## HTTP surface

| Route | Auth | Notes |
|---|---|---|
| `GET /healthz` | none | `200 ok` if both database pools answer within 2 s, else `503`. Reveals nothing else |
| `GET /api/setup` | none | `{"required": bool}` |
| `POST /api/setup` | setup token | `{"token","username","password"}` → `201 {"user"}` + session cookie. `403` wrong token, `422` invalid input (token stays valid), `409` already done |
| `POST /api/auth/login` | none | `{"username","password"}` → `200 {"user"}` + cookie, `401` otherwise |
| `POST /api/auth/logout` | cookie (optional) | `204`; deletes the session server-side and clears the cookie |
| `GET /api/auth/session` | session | `{"user","session"}` |
| `GET /api/version` | session | `{"version","commit"}` |
| `GET /ws` | session | WebSocket; see [`realtime/protocol.go`](realtime/protocol.go) |
| `/api/*` (other) | — | JSON `404` |
| `GET /` | none | Placeholder page. **Seam for 3.8:** pass the embedded SPA as `server.Deps.Web` |

Errors are `{"error":{"code","message"}}`. `/setup` and `/auth/login` share a
per-IP budget: 10 attempts, then one every 6 s, answered with `429` and
`Retry-After`.

## Decisions

**Storage**
- **One writer connection, four query-only readers.** SQLite has one writer at a time; a one-connection pool makes writes queue in Go instead of failing with `SQLITE_BUSY`. Readers carry `query_only(1)`, so a stray write fails loudly. Never query the writer pool while holding one of its transactions: that deadlocks.
- **Pragmas** on every connection: WAL, `busy_timeout(5000)`, `foreign_keys(1)`, `synchronous(NORMAL)` (safe with WAL; at worst it loses the last commits on power loss). The writer uses `_txlock=immediate`.
- **The driver is named in one file**, `store/driver_modernc.go`. Decision 15 (modernc vs ncruces) is still open; both accept the same `_pragma`/`_txlock` DSN parameters.
- **Driver-neutral columns.** Timestamps are Unix milliseconds (`INTEGER`), not `time.Time`, because the two drivers scan times differently. Tables are `STRICT`. IDs are prefixed random strings (`u_`, `s_`, `sec_`) from `crypto/rand.Text`.
- **goose through its `Provider` API**, not the global one. Don't call `Provider.Close`: it closes the database it was handed.
- **Layouts** are opaque JSON with a `format_version` (the layout package's document format) and a per-board `revision`. Each save appends a row, so history and undo survive restarts.
- **`audit`** is a stub table with an `UPDATE` trigger that aborts. Retention may delete rows; nothing writes to it yet.

**First run and sessions**
- **Setup token**: `cds_` + 128 random bits. It lives in memory only and is logged once at `warn`, so it survives a quiet log level. A restart before setup logs a fresh one. A wrong token or invalid input leaves it valid; success spends it.
- **Exactly one owner**, enforced by a partial unique index. That also makes concurrent setup requests race-free at the database level.
- **argon2id at OWASP's baseline** (19 MiB, t=2, p=1), with at most two hashes running at once. That keeps a login inside the RSS budget on small ARM boards. Parameters live in each PHC string, so they can be raised later. Unknown usernames are checked against a dummy hash, so timing doesn't reveal which usernames exist.
- **Passwords**: at least 12 characters, at most 512 bytes. **Usernames**: 1–64 of `A–Z a–z 0–9 . _ @ -`, case-insensitive.
- **Sessions**: a 256-bit token in the cookie; only its SHA-256 is stored, next to a public `s_…` ID for listing and audit. **Absolute 30-day expiry, no sliding.** `last_seen_at` is written at most every 5 minutes. A `method` column (`password` today) is there for step-up checks later.
- **Cookie** `chadash_session`: `HttpOnly`, `SameSite=Lax`, `Path=/`. `Secure` when the request came over TLS, or from a trusted proxy that sent `X-Forwarded-Proto: https`. There's no `__Host-` prefix, because plain-HTTP LAN installs are common and that prefix requires `Secure`.
- **CSRF**: `SameSite=Lax`, plus net/http's `CrossOriginProtection` on every unsafe method (it uses `Sec-Fetch-Site`, falling back to Origin vs Host), plus JSON-only request bodies, which a cross-site form can't send. There are no per-request CSRF tokens. The architecture doc lists them; these three layers replace them on purpose, and tokens can still be added if a form-based flow ever needs them.
- **Passkey seam**: `auth.Service.StartSession(w, r, user, method)` is where every sign-in method ends. Passkeys add a credential table and verification handlers, then call it.
- **Rate limiting** is per client IP and in memory. Behind a reverse proxy, set `CHADASH_TRUSTED_PROXIES`; otherwise every client shares the proxy's bucket.
- **Forwarded headers** are honoured only from trusted proxies. The client IP is the rightmost `X-Forwarded-For` hop that isn't a trusted proxy.

**Vault**
- **Envelope encryption.** A fresh data key per secret seals the value with XChaCha20-Poly1305, and the KEK seals the data key. Both seals use AAD `alg ‖ id ‖ name`, so swapping or renaming rows fails authentication.
- **The KEK file** may hold 32 raw bytes, base64 or hex. A 32-byte file that is all printable text is rejected as a passphrase, not taken as a raw key. Generated keys are base64 with mode `0600` and are created with `O_EXCL`.
- **A key is generated only at the default path.** A configured `CHADASH_SECRET_KEY_FILE` that doesn't exist stops the hub; that usually means an unmounted secret. The vault also refuses to generate a key when secrets already exist.
- **Boot fails if any stored secret was sealed under a different key.** Each row carries a KEK fingerprint (a truncated HMAC). Key rotation, later, will relax this to "every key ID is known".
- A key file readable by group or others is a **warning, not an error**: Docker secrets mount `0444`.
- Names follow `[A-Za-z0-9][A-Za-z0-9._/-]{0,127}`, the form `${secret:name}` references use. Values are capped at 64 KiB. Values never appear in logs or errors.

**Home Graph**
- **Batches.** `Update(func(tx))` applies a batch atomically under the write lock and bumps the version once. A batch with no effective change doesn't bump it. Re-asserting identical facts is free.
- **Immutable values.** `Upsert` deep-copies JSON-like `Attrs`, and empty maps normalise to nil. Snapshots and patches share these values, so they can be serialised without locks.
- **Relations need existing endpoints**, and removing a resource removes its relations. A filtered view shows a relation only when both endpoints are visible, so views never dangle.
- **Diffs replace whole resources**, not fields. This is simple, and a resource is small.
- **Watchers are bounded** (4096 pending operations by default). On overflow they drop the queue and return `ErrLagged` until the consumer takes a fresh `Snapshot`.
- **The version starts at 0 in every process.** The `epoch` in the realtime `hello` tells clients the counter restarted.
- **Not modelled yet**: `SeenAt`/freshness (it would churn diffs; it needs its own channel), `Desired`, `Links`, `Caps`, per-source provenance, persistence of the graph cache.
- At boot the hub inserts itself as `chadash:hub` (kind `app`).

**Realtime**
- **A subscription's state is one JSON document** with a member per topic. `snap` carries it whole; `diff` is an RFC 6902 patch against it, with paths like `/graph/resources/<id>`. Clients can use any JSON Patch library. Ops only ever touch whole members, which is what makes `jsonpatch.Compact` sound.
- **`diff` frames carry the subscription `id`.** The architecture doc's example omits it, but a multiplexed socket needs it.
- **Coalescing**: at most one `diff` per subscription per 250 ms. The first change after a quiet period goes out immediately.
- **Backpressure**: each feed is bounded, and the connection has a 64-frame send queue. If a feed overflows, or a `diff` doesn't fit in the queue, the pending diffs are dropped and the subscription gets `snap` with `resync: true`. Memory stays bounded however slow the client is. A frame that can't be written within 10 s drops the connection.
- **`resume` is accepted and ignored** in v0. Every `sub` gets an inline snapshot, so reconnecting is always correct, though not minimal.
- **Additions** to the doc's op list: `unsub`, `error` (with codes), and a client `ping` answered with `pong`.
- **Handshake**: `hello` must arrive within 10 s and must be `v: 1`. Otherwise the client gets close code 1008, after an `unsupported_version` error frame when the version is wrong. Every 30 s the server sends `{"op":"ping"}` for the browser's watchdog, plus a protocol ping that drops peers which have silently vanished.
- **Origin**: same-origin is always allowed; `CHADASH_ALLOWED_ORIGINS` adds more. Auth is the session cookie, checked before the upgrade.
- **Topics**: `graph` and `graph:kind/<kind>[,<kind>…]`. Lens and metric topics plug in as more `Source`s.
- **Shutdown**: hijacked connections aren't covered by `http.Server.Shutdown`, so `Serve` calls `Hub.Close` afterwards. That sends close code 1001 (going away), and new upgrades then get 1013 (try again later).

**HTTP**
- **No `ReadTimeout`/`WriteTimeout`** on the server, because they would also cut WebSockets. There's a 10 s `ReadHeaderTimeout`, bodies are capped at 64 KiB, and the idle timeout is 2 minutes.
- **Security headers** on every response: CSP `default-src 'self'; base-uri 'none'; object-src 'none'; frame-ancestors 'none'; form-action 'self'`, plus `nosniff`, `X-Frame-Options: DENY`, `Referrer-Policy: no-referrer`, COOP and CORP `same-origin`, and a restrictive `Permissions-Policy`. API responses also get `no-store`. There's no HSTS, because the hub doesn't terminate TLS itself.
- **`healthcheck`** dials loopback when the listen host is a wildcard, and ignores `HTTP(S)_PROXY`. It exits 1 on any failure, including bad flags, because Docker reserves exit code 2.

## Stubbed or deferred

- The SPA (3.8): `server.Placeholder` stands in for it.
- Passkeys, TOTP, OIDC, roles beyond owner, API and kiosk tokens, step-up.
- Revoking live WebSockets when a session is logged out or expires. A socket stays open until it reconnects.
- Writing to `audit`; nothing records auth events yet.
- Vault key rotation and a keyed-hash fingerprint for spotting reused secrets.
- Graph persistence (the last-known cache) and identity resolution (the correlator).
- Replaying diffs on resume; the inline snapshot embedded in `index.html`.
- An SSE fallback for realtime.
