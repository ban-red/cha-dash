# qBittorrent (`qbittorrent`)

Reads qBittorrent through its WebUI API v2 (qBittorrent 4.1 and later, 5.x included): download and upload speeds, and how many torrents are seeding, leeching and queued.

**Tier:** silver. **Capability:** `downloads.queue`. **Secret:** `password`.

## Config

```json
{"url": "https://torrents.lab.example", "tls": {"mode": "verify"}, "username": "admin", "password": "${secret:instance/<id>/password}"}
```

| Key | Meaning |
|---|---|
| `url` | The WebUI's address |
| `tls` | The usual `{"mode", "pin"}` |
| `username` | The WebUI username: an identifier, so plain config |
| `torrent_limit` | How many torrents the tile's Torrents view lists, 0–1000 (default 200; 0 lists none). Each is sent to everyone who can see the tile |
| `password` | The WebUI password: write-only, kept in the vault |

Both or neither: leave both out when the WebUI lets the hub in without signing in ("Bypass authentication for clients in whitelisted IP subnets"). This is the shape the Homepage importer writes.

## Signing in

A cookie session through the runtime's `Session` hook:
- **Login:** `POST /api/v2/auth/login` with the form `username`, `password`. qBittorrent answers `Ok.` and sets the session cookie; every cookie it sets is kept, so both `SID` (4.x) and `QBT_SID_<port>` (5.x) work. `Fails.` (or a 401) is a wrong username or password; a 403 means qBittorrent has banned the hub's address after failed sign-ins.
- **Requests** carry the cookie. qBittorrent answers 403 to an expired session, so the framework logs in again and retries once.
- **Logout:** `POST /api/v2/auth/logout` when the instance stops.
- **No `Referer` or `Origin`.** qBittorrent's CSRF check passes requests that carry neither, while a `Referer` naming the public address fails behind a proxy that rewrites `Host`.

## What it reads

| Endpoint | When | For |
|---|---|---|
| `GET /api/v2/app/version` | the first sync, then hourly while watched | the version (`v5.1.2` → `5.1.2`) |
| `GET /api/v2/app/webapiVersion` | with it | must be 2.x |
| `GET /api/v2/sync/maindata?rid=<n>` | every sync | everything else |

`sync/maindata` is incremental: the first answer (`rid=0`) is a full update, and later ones carry only what changed since the `rid` the hub sends back, so a sync with nothing new is a few bytes however many torrents there are. The integration keeps each torrent's state by hash and the merged `server_state`, starts over on `full_update` (qBittorrent sends one after a new login), and drops `torrents_removed`. After a failed request it asks for a full update again.

**Poll plan:** every 30 s while someone watches (the Demo Home's sample rate for speeds), every 5 minutes while nobody does; one request per sync either way. 30 s per sync.

## What it publishes

- **`qbittorrent:<instance id>/app`**, kind `app`, named after the instance, state `running`, with `attrs.type = "qbittorrent"`, `attrs.url`, `attrs.version`, `attrs.connection_status` (`connected`, `firewalled`, `disconnected`), `attrs.torrents` (how many), `attrs.errored`, `attrs.alt_speed` (alternative speed limits on), `attrs.ratio` (global share ratio), `attrs.free_gib`, `attrs.peers`, `attrs.active`, a link and `caps: ["downloads.queue"]`. `integration:<instance id> monitors` it.
- **`attrs.active`** is what's moving, for the tile: up to 8 torrents, downloads first (fastest first, queued ones after), then the ones uploading. Each has `name`, `state` (downloading, stalled, fetching metadata, queued, seeding), `progress` (percent), `dl_mbps`, `ul_mbps`, `size_gb` and `eta_s` (only while downloading). The names are your own torrents, so they're as visible as the tile is.
- **`attrs.list`** is a compact table of up to `torrent_limit` torrents (default 200, 0 to list none) for the tile's Torrents view, worst first (errors, downloads, the queue, uploads, the rest): `name`, `state` (adds `error` and `stopped`), `progress`, `dl_mbps`, `ul_mbps`, `ratio` and `added` (Unix seconds).
- **Health:** `attention` with "qBittorrent has no network connection" when `connection_status` is `disconnected` (a VPN container that lost its tunnel, typically); else `attention` with "N torrents have an error" when any torrent is in `error` or `missingFiles`; else `ok`. `firewalled` (no incoming connections) stays `ok`.

| Metric | Unit | Scale | Band | Value |
|---|---|---|---|---|
| `dl.mbps` | Mb/s | 0–1000 | 0–60 | `server_state.dl_info_speed` (bytes/s) × 8 / 10⁶, to 0.1 |
| `ul.mbps` | Mb/s | 0–1000 | 0–20 | `server_state.up_info_speed`, likewise |
| `seeding` | count | 0–10000 | 50–100 | torrents `uploading`, `stalledUP` or `forcedUP` |
| `leeching` | count | 0–1000 | 0–30 | torrents `downloading`, `stalledDL`, `forcedDL`, `metaDL` or `forcedMetaDL` |
| `queued` | count | 0–1000 | 0–10 | torrents `queuedDL` or `queuedUP` |

Stopped (paused), checking and moving torrents count in none of them. An idle client is 0 Mb/s and 0 torrents, never unknown.

## Grants

qBittorrent has one WebUI account and no read-only mode, so Test reports one grant, `qBittorrent WebUI`, access `admin` ("can add, remove and change torrents and settings"; with no credentials, "lets this address in without signing in, with full control"), and the hub warns about it.

## Repairs and hints

| What happened | Error | Field |
|---|---|---|
| Wrong username or password (`Fails.`, 401) | `auth` Repair | `password` |
| Banned after failed sign-ins (403 on login) | `auth` Repair, saying to fix the password and wait for the ban to lift | `password` |
| 403 with no credentials configured | `auth` Repair: "enter the WebUI username and password" | `username` |
| 404 (not the WebUI, or older than 4.1) | `api_changed` Repair | `url` |
| An answer that isn't JSON | `api_changed` Repair | `url` |
| Web API 1.x | `unsupported_version` Repair | |

## Actions

| Action | Tier | Undo | What it does | Verify |
|---|---|---|---|---|
| `qbittorrent.app.pause` | T1 | `qbittorrent.app.resume` | `POST /api/v2/torrents/stop` (`pause` before Web API 2.11, qBittorrent 4) with `hashes=all` | no torrent is left running (`torrents/info?filter=running`, or `resumed`) |
| `qbittorrent.app.resume` | T1 | `qbittorrent.app.pause` | `POST /api/v2/torrents/start` (`resume` before 2.11) with `hashes=all` | no torrent is left stopped (`filter=stopped`, or `paused`) |

- **Preconditions** (`409 precondition`): nothing to pause (no torrent running) or to resume (none stopped).
- **All means all:** resuming starts every stopped torrent, including ones that were stopped before the pause.
- **Credential:** the WebUI account. qBittorrent has one account with full control and no scopes, so there's no separate control credential to add.
- **Audit:** the target and empty params; the password never appears in a plan, task log or audit record.

## Limits

- No per-torrent resources or names: the counts are what the Downloads tile shows, and torrent names can be personal.
- Categories, tags, trackers and the free-space figure in `server_state` are read but not published.
- The WebUI's "Host header validation" must accept the host in `url`, as it must for a browser.

## Fixtures

Synthetic, in [`testdata/integrations/qbittorrent/`](../../testdata/integrations/qbittorrent/README.md).
