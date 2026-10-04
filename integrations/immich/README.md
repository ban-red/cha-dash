# Immich (`immich`)

Immich's version and its photo, video and storage statistics.

**Tier:** silver (version detection, repairs, grants read back, fixture tests). **Capability:** `photos.library`. **Secret:** `api_key`.

## Config

```json
{"url": "https://photos.lab.example", "tls": {"mode": "verify"}, "api_key": "${secret:instance/<id>/api_key}"}
```

| Key | Meaning |
|---|---|
| `url` | Immich's address, `http://` or `https://`. A URL ending in `/api` is taken as its parent, so the API's own address works too |
| `tls` | The usual `{"mode", "pin"}` |
| `api_key` | An API key (Account Settings → API Keys), sent as `x-api-key`. Write-only, kept in the vault |

This is the Homepage importer's shape (`key` → `api_key`).

**Which key.** Server-wide statistics need an **admin's** key with the `server.statistics` permission. A read-only key with `server.about`, `server.statistics`, `asset.statistics` and `user.read` is enough for everything this integration reads; a key with `all` works too, and Test warns that it can change things. Any other user's key works, with the fallback below.

## What it reads

| Request | Auth | When | For |
|---|---|---|---|
| `GET /api/server/version` | public | every sync | The version (`{major, minor, patch}`) |
| `GET /api/server-info/version` | public | only when the one above is a 404 | Recognising an Immich older than 1.107 |
| `GET /api/server/about` | `server.about` | every sync | `licensed`. It also catches a revoked key (401) on syncs nobody watches. A 403 is fine |
| `GET /api/server/statistics` | `server.statistics`, admin | watched syncs | `photos`, `videos`, `usage` (bytes), users |
| `GET /api/users/me` | `user.read` | watched syncs, after a 403 above | Whether the key's user is an admin; their `quotaUsageInBytes` |
| `GET /api/assets/statistics` | `asset.statistics` | watched syncs, after a 403 above | The key's own `images` and `videos` |
| `GET /api/api-keys/me` | any key | Test | The key's permissions, as grants |

**Poll plan:** every 2 minutes while someone is watching, every 10 minutes otherwise, each sync bounded by 30 s. Unwatched syncs read only the version and the about page (health and the key); statistics walk every asset, so they wait for a watcher, and the last ones stay published meanwhile.

## What it publishes

- **`immich:<instance id>/app`**, kind `app`, named after the instance, state `running`, health `ok`. Attrs: `type` (`immich`), `url` (as configured), `version` (`2.1.0`), `licensed`, `stats_scope` (`server` or `user`, see below) and, for server statistics, `users` (the Demo Home's attr). A link to the URL, `caps: ["photos.library"]`. `integration:<instance id> monitors` it.
- **Series** on that resource, the Demo Home's keys and scales:

  | Key | Unit | Scale | From |
  |---|---|---|---|
  | `photos` | count | 0–10 000 000, band 0–1 000 000 | `photos` (server) or `images` (own library) |
  | `videos` | count | 0–1 000 000, band 0–100 000 | `videos` |
  | `storage.gib` | GiB, one decimal | 0–1 000 000, band 0–1000 | `usage` (server) or `quotaUsageInBytes` (own library), in GiB (2³⁰ bytes) |

  A value the key can't read is unknown (`null`), never 0.

**Without an admin's key.** `GET /api/server/statistics` answers 403. The integration then counts the key's own library (`/api/assets/statistics` and the user's quota usage), sets `stats_scope: "user"`, leaves `users` out, and raises the warning `statistics_not_admin` (severity `info`), which says why: the user isn't an admin, or the admin's key lacks `server.statistics`. A warning makes the instance `degraded`; switching to an admin's key clears it on the next watched sync. If the key can't read its own library either, the series are unknown and the warning says so. Test reports the same warning.

## Grants

Test reads `GET /api/api-keys/me` and turns each permission into a grant. Actions that only read (`read`, `view`, `download`, `statistics`, `about`, `storage`, `apkLinks`, `versionCheck`, `search`, …) are `read`; every other action (`create`, `update`, `delete`, `upload`, `share`, …) is `write`, and `all` is `admin`. The runtime warns about any write grant. Immich releases without that route (a 404) report no grants.

## Repairs

| When | Code | Message |
|---|---|---|
| A 401 from any authenticated route | `auth` | The key was rejected; the Repair points at `api_key` |
| `/api/server/version` is a 404 and `/api/server-info/version` answers | `unsupported_version` | "Immich 1.105.1 is older than this integration supports (1.107 or later)." |
| `/api/server/version` and the old path are both 404s | `api_changed` | Not an Immich API this integration knows (or not Immich at that URL) |
| Any other route this integration reads is a 404 | `api_changed` | Names the route and the detected version |

## Limits

- The fixtures are synthetic ([`testdata/integrations/immich`](../../testdata/integrations/immich/README.md)), from Immich's server source; no live Immich was read.
- No per-user breakdown, jobs, or library scan state yet. `usageByUser` is only counted.
- Statistics are polled; nothing is pushed.
- A 403 from `/api/server/about` is treated as a missing permission, and a 403 from `/api/server/version` (public) as a rejected credential; a reverse proxy that answers 403 for its own reasons looks the same.
