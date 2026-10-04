# NZBGet (`nzbget`)

Reads NZBGet 16 and later (the nzbgetcom releases, 21 to 25, included) through its JSON-RPC API: the download speed, how many jobs are in the queue and how much is left, and whether downloading is paused.

**Tier:** silver. **Capability:** `downloads.queue`. **Secret:** `password`.

**Why Go, not a spec:** NZBGet authenticates with HTTP basic auth (or credentials in the URL path, which the spec engine rightly refuses), and every call is a JSON-RPC `POST`. The spec engine can't build a basic-auth header.

## Config

```json
{"url": "http://nas.home.arpa:6789", "tls": {"mode": "verify"}, "username": "chadash", "password": "${secret:instance/<id>/password}"}
```

| Key | Meaning |
|---|---|
| `url` | NZBGet's web interface, with its path if it's behind a reverse proxy |
| `tls` | The usual `{"mode", "pin"}` |
| `username` | Settings → Security → RestrictedUsername (or ControlUsername): an identifier, so plain config |
| `password` | That user's password: write-only, kept in the vault |

Both or neither: leave both out when NZBGet has no password. This is the shape the Homepage importer writes from an `nzbget` widget.

## What it reads

`POST <url>/jsonrpc` with `{"version": "1.1", "id": 1, "method", "params"}` and basic auth:

| Method | When | For |
|---|---|---|
| `version` | the first sync, then hourly while watched | the version (must be 16 or later) |
| `status` | every sync | `DownloadRateLo`/`Hi` (NZBGet 24+; else `DownloadRate`), `RemainingSizeMB`, `DownloadPaused`, `QuotaReached`, `FreeDiskSpaceMB`, `PostJobCount` |
| `listgroups` with `[0]` | every sync | how many jobs are queued; only each job's `Status` is decoded, never its name |

**Poll plan:** every 30 s while someone watches, every 5 minutes while nobody does, like SABnzbd. 30 s per sync.

## What it publishes

- **`nzbget:<instance id>/app`**, kind `app`, named after the instance, with `attrs.type = "nzbget"`, `attrs.url`, `attrs.version`, `attrs.paused`, `attrs.quota_reached`, `attrs.free_disk_gib` (free space on the destination folder, to 0.1), `attrs.post_jobs` (jobs post-processing), a link and `caps: ["downloads.queue"]`. `integration:<instance id> monitors` it.
- **State:** `paused` while downloading is paused (else `running`); health stays `ok`, because a pause is usually the owner's, as with SABnzbd.
- **Health:** `attention` with "NZBGet reached its download quota and stopped downloading" when `QuotaReached`; else `ok`.

| Metric | Unit | Scale | Band | Value |
|---|---|---|---|---|
| `dl.mbps` | Mb/s | 0–1000 | 0–60 | the download rate (bytes/s) × 8 / 10⁶, to 0.1 |
| `queue` | count | 0–1000 | 0–15 | jobs in the queue (`listgroups`), post-processing ones included |
| `left.gib` | GiB | 0–10000 | 0–50 | `RemainingSizeMB` in GiB, to 0.1 |

The keys and scales are SABnzbd's, so the Usenet side of the download tiles reads both the same way. A paused or empty client is 0, never unknown.

## Grants

NZBGet has three users and no read-only one. The restricted user can pause, resume and delete downloads but can't see or change settings; the control user can do everything; the add user can only add downloads. The API can't say which one signed in without reading the settings (which hold the other users' passwords), so Test reports one grant, `NZBGet user`, access `rw`, with that explanation, and the hub warns about it. With no password the grant is `admin`.

## Repairs

| What happened | Error | Field |
|---|---|---|
| 401 or 403 with credentials | `auth` Repair: "NZBGet rejected the username or password" | `password` |
| 401 or 403 without credentials | `auth` Repair: "enter its username and password" | `username` |
| "Access denied" inside a 200 (the add user) | `auth` Repair: use the restricted or control user | `username` |
| 404, or an answer that isn't JSON-RPC | `api_changed` Repair: check the URL | `url` |
| Another JSON-RPC error | `api_changed` Repair with NZBGet's message | |
| Version 15 or older | `unsupported_version` Repair | |

## Limits

- No per-job resources or names: job names can be personal, and the tiles show totals.
- History, news servers and the per-category speeds aren't published.

## Fixtures

Synthetic, in [`testdata/integrations/nzbget/`](../../testdata/integrations/nzbget/README.md).
