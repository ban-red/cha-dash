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
| `internal/boards` | Boards and their layouts: optimistic revisions, history, the boards API |
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

Dependencies point down: `cmd → server → {auth, boards, demo, realtime} → {topics, graph, livedoc, store} → …`.
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
| `--demo` | `CHADASH_DEMO` | off; `1`/`true` runs the Demo Home simulator |
| `--demo-at` | `CHADASH_DEMO_AT` | `14:05`; a time of day (`HH:MM[:SS]`, UTC) on the demo's day, or `now` for the real clock |
| `--demo-seed` | `CHADASH_DEMO_SEED` | `1` |
| `healthcheck --timeout` | — | `3s` |

The demo settings are validated even when demo mode is off, so a typo shows
up before anyone turns it on.

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
| `GET /api/auth/session` | session | `{"user","session"}` |
| `GET /api/version` | session | `{"version","commit"}` |
| `GET /api/boards` | owner | `{"boards":[{"id","name"}]}`. The first call creates a `Home` board |
| `GET /api/boards/{id}/layout` | owner | `{"revision","format_version","layout"}`. A board never saved gives `{"revision":0,"format_version":0,"layout":null}`; the SPA then creates the first layout. `404` unknown board |
| `PUT /api/boards/{id}/layout` | owner | `{"revision","format_version","layout"}`, where `revision` is the one the edit started from → `200 {"revision": n+1}`. Stale → `409 {"error":{"code":"conflict",…},"revision": current}`. `422` if `revision` < 0, `format_version` < 1 or `layout` isn't an object. Up to 1 MiB |
| `GET /api/demo` | owner | Demo mode only: `{"enabled":true,"now":RFC 3339,"seed":n}`. `now` is the simulated time |
| `POST /api/demo/incidents/{id}/fix` | owner | Demo mode only: presses the incident's suggested action → `202 {"id"}`. `404` unknown incident, `409` not open yet, already fixing or resolved. Progress arrives on the `inbox` topic |
| `GET /ws` | session | WebSocket; see [`realtime/protocol.go`](realtime/protocol.go) and the topics below |
| `/api/*` (other) | — | JSON `404`; so are the demo routes outside demo mode |
| `GET /…` | none | The web app. Unknown paths get `index.html` (history-mode routing) |

Errors are `{"error":{"code","message"}}`. `/setup` and `/auth/login` share a
per-IP budget: 10 attempts, then one every 6 s, answered with `429` and
`Retry-After`. "Owner" routes need a session whose user has the owner role;
today every account is the owner, and later roles get `403`.

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
Outside demo mode `metrics` is empty and `inbox` has no incidents, and the inbox
clock follows the wall clock, ticking every 30 s.

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
- **Sessions**: a 256-bit token in the cookie; only its SHA-256 is stored, next to a public `s_…` ID for listing and audit. **30-day idle expiry that renews while in use:** at most once a day, a request slides `expires_at` forward and reissues the cookie, so a session in regular use never lapses and an unused one expires after 30 days. `last_seen_at` is written at most every 5 minutes. A `method` column (`password` today) is there for step-up checks later.
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
- **No `ReadTimeout`/`WriteTimeout`** on the server, because they would also cut WebSockets. There's a 10 s `ReadHeaderTimeout`, bodies are capped at 64 KiB (1 MiB for layout saves), and the idle timeout is 2 minutes.
- **Security headers** on every response: CSP `default-src 'self'; base-uri 'none'; object-src 'none'; frame-ancestors 'none'; form-action 'self'`, plus `nosniff`, `X-Frame-Options: DENY`, `Referrer-Policy: no-referrer`, COOP and CORP `same-origin`, and a restrictive `Permissions-Policy`. API responses also get `no-store`. There's no HSTS, because the hub doesn't terminate TLS itself.
- **`healthcheck`** dials loopback when the listen host is a wildcard, and ignores `HTTP(S)_PROXY`. It exits 1 on any failure, including bad flags, because Docker reserves exit code 2.

## Stubbed or deferred

- Real metrics and incidents: outside demo mode, `metrics` and `inbox` are empty until integrations and the incident engine fill them.
- The metrics history axis (start, step) isn't on the wire; series carry only their 48 points, 30 minutes apart, newest last.
- Demo actions beyond each incident's suggested fix, and a reset button.
- Passkeys, TOTP, OIDC, roles beyond owner, API and kiosk tokens, step-up.
- Revoking live WebSockets when a session is logged out or expires. A socket stays open until it reconnects.
- Writing to `audit`; nothing records auth events yet.
- Vault key rotation and a keyed-hash fingerprint for spotting reused secrets.
- Graph persistence (the last-known cache) and identity resolution (the correlator).
- Replaying diffs on resume; the inline snapshot embedded in `index.html`.
- An SSE fallback for realtime.
