# Transmission (`transmission`)

Reads Transmission through its RPC (Transmission 2.80 and later, RPC version 15+, 4.x included): download and upload speeds, and how many torrents are seeding, downloading and queued.

**Tier:** silver. **Capability:** `downloads.queue`. **Secret:** `password`.

**Why Go, not a spec:** every RPC call is a `POST` that first has to win the session-id handshake (a 409 hands out `X-Transmission-Session-Id`, and a restart hands out a new one), and authentication is HTTP basic auth. The spec engine does neither.

## Config

```json
{"url": "https://torrents.home.arpa", "tls": {"mode": "verify"}, "rpc_path": "/transmission/", "username": "chadash", "password": "${secret:instance/<id>/password}"}
```

| Key | Meaning |
|---|---|
| `url` | Transmission's address, without the RPC path (`http://nas.home.arpa:9091`, or the reverse proxy's) |
| `tls` | The usual `{"mode", "pin"}` |
| `rpc_path` | Optional. Transmission's `rpc-url`, `/transmission/` by default; the hub posts to `rpc` under it. `/`-separated path segments only. This is Homepage's `rpcUrl`, so the importer copies it as is |
| `username` | `rpc-username`: an identifier, so plain config |
| `password` | `rpc-password`: write-only, kept in the vault |

Both or neither: leave both out when Transmission doesn't ask for one (`rpc-authentication-required: false`). This is the shape the Homepage importer writes.

## The handshake

Every call is `POST <url><rpc_path>rpc` with `{"method", "arguments"}`, basic auth when a username is set, and the last session id in `X-Transmission-Session-Id`. Transmission answers 409 with a new id to a request without the current one (the first call, and the first after Transmission restarts); the call is sent once more with it. A second 409 in a row is an `api_changed` Repair, because something in front of Transmission is eating the header.

## What it reads

| Method | Arguments | For |
|---|---|---|
| `session-get` | `fields: [version, rpc-version, rpc-version-minimum, alt-speed-enabled]` | the version, the RPC version check, alternative speeds |
| `session-stats` | none | `downloadSpeed`, `uploadSpeed` (bytes/s) |
| `torrent-get` | `fields: [status, error]` | each torrent's state, never its name, files or trackers |

All three on every sync. **Poll plan:** every 30 s while someone watches (the Demo Home's sample rate for speeds), every 5 minutes while nobody does. 30 s per sync.

## What it publishes

- **`transmission:<instance id>/app`**, kind `app`, named after the instance, state `running`, with `attrs.type = "transmission"`, `attrs.url`, `attrs.version` (`4.0.6 (38c164933e)` → `4.0.6`), `attrs.rpc_version`, `attrs.torrents` (how many), `attrs.stopped`, `attrs.errored`, `attrs.alt_speed` (alternative speed limits on), a link to the web interface (`<url><rpc_path>web/`) and `caps: ["downloads.queue"]`. `integration:<instance id> monitors` it.
- **Health:** `attention` with "N torrents have an error" when any torrent has a local error (`error` 3: a missing file, a full disk); else `ok`. Tracker warnings and errors (`error` 1 and 2) are common and pass on their own, so they don't count.

| Metric | Unit | Scale | Band | Value |
|---|---|---|---|---|
| `dl.mbps` | Mb/s | 0–1000 | 0–60 | `downloadSpeed` (bytes/s) × 8 / 10⁶, to 0.1 |
| `ul.mbps` | Mb/s | 0–1000 | 0–20 | `uploadSpeed`, likewise |
| `seeding` | count | 0–10000 | 50–100 | torrents with status 6 (seeding) |
| `leeching` | count | 0–1000 | 0–30 | status 4 (downloading, stalled included) |
| `queued` | count | 0–1000 | 0–10 | status 3 or 5 (queued to download or to seed) |

The keys and scales are qBittorrent's, so the Downloads tile and the service tile read both the same way. Stopped and checking torrents count in none of them. An idle client is 0 Mb/s and 0 torrents, never unknown.

## Grants

Transmission has one RPC account and no read-only mode, so Test reports one grant, `Transmission RPC`, access `admin`, and the hub warns about it. Without credentials the detail says that anything reaching Transmission has full control.

## Repairs

| What happened | Error | Field |
|---|---|---|
| 401 with credentials | `auth` Repair: "Transmission rejected the username or password" | `password` |
| 401 without credentials | `auth` Repair: "enter its RPC username and password" | `username` |
| 403 (`rpc-whitelist` refuses the hub's address) | `auth` Repair naming `rpc-whitelist` | `username` |
| 404, an answer that isn't the RPC, or 409 twice | `api_changed` Repair: check the URL and the RPC path | `rpc_path` |
| RPC version below 15 | `unsupported_version` Repair | |
| A `result` other than `success` | an upstream error, retried | |

## Limits

- No per-torrent resources or names: the counts are what the Downloads tile shows, and torrent names can be personal.
- Free space, ratios and the cumulative statistics aren't published.
- Transmission 4.1's JSON-RPC 2.0 framing isn't used: the legacy `{"method", "arguments"}` framing is what every supported release answers. That 4.1 still does is from its documentation, not a live run.

## Fixtures

Synthetic, in [`testdata/integrations/transmission/`](../../testdata/integrations/transmission/README.md).
