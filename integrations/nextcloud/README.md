# Nextcloud (`nextcloud`)

Reads Nextcloud through the Monitoring app's API (serverinfo, bundled and on by default): how many users there are and how many were active, files, shares and the free space on the data folder.

**Tier:** silver. **Capability:** `files.storage` (new). **Secrets:** `token`, or `password`.

**Why Go, not a spec:** the endpoint takes either a serverinfo token header or HTTP basic auth with an app password, and Homepage users mostly have the second. The spec engine can't build a basic-auth header.

## Config

```json
{"url": "https://nextcloud.home.arpa", "tls": {"mode": "verify"}, "token": "${secret:instance/<id>/token}"}
```

| Key | Meaning |
|---|---|
| `url` | Nextcloud's address, with its path if it has one |
| `tls` | The usual `{"mode", "pin"}` |
| `token` | The serverinfo token (`occ config:app:set serverinfo token --value …`), sent as `NC-Token`. Write-only, kept in the vault. **The least-privilege option**: it reads this endpoint and nothing else |
| `username` | Or an administrator's username: an identifier, so plain config |
| `password` | With an app password for that user, sent as basic auth. Write-only |

A token, or a username with an app password; with both, the token is used. The Homepage importer writes `key` as `token` and `username`/`password` as they are.

## What it reads

`GET <url>/ocs/v2.php/apps/serverinfo/api/v1/info?format=json&skipApps=true&skipUpdate=true` with `OCS-APIRequest: true`, on every sync. `skipApps` and `skipUpdate` leave out the app-store and update checks, which are slow and reach out to the internet; releases without the parameters ignore them. **Poll plan:** every 2 minutes while someone watches (serverinfo counts files in the database), every 10 minutes while nobody does. 30 s per sync.

## What it publishes

- **`nextcloud:<instance id>/app`**, kind `app`, named after the instance, state `running`, with `attrs.type = "nextcloud"`, `attrs.url`, `attrs.version` (`30.0.6.2`), `attrs.php_version`, `attrs.database` (`pgsql`, `mysql`, `sqlite3`), `attrs.users`, a link and `caps: ["files.storage"]`. `integration:<instance id> monitors` it.
- **Health:** `attention` with "Nextcloud has 3.0 GiB free" when the data folder has less than 5 GiB free (uploads start failing); else `ok`.

| Metric | Unit | Scale | Band | Value | |
|---|---|---|---|---|---|
| `users` | count | 0–10000 | 0–50 | `storage.num_users` | new key |
| `users.active` | count | 0–10000 | 0–10 | `activeUsers.last5minutes` | new key |
| `users.24h` | count | 0–10000 | 0–20 | `activeUsers.last24hours` | new key |
| `files` | count | 0–10⁹ | 0–10⁷ | `storage.num_files` | new key |
| `shares` | count | 0–10⁶ | 0–1000 | `shares.num_shares` | new key |
| `free.gib` | GiB | 0–10⁵ | 50–10⁵ | `system.freespace` (bytes) in GiB, to 0.1 | new key |

None of these is in the Demo Home. A count serverinfo sends as a string is read as a number; a value it can't give (`freespace` is `false` when PHP's `disk_free_space` is disabled) is unknown, never 0.

## Grants

| Credential | Grant | Access |
|---|---|---|
| Token | `Nextcloud serverinfo token`: reads the monitoring endpoint and nothing else | `read` |
| Username and app password | `Nextcloud admin account`: an app password has all its account's rights, and serverinfo needs an administrator | `admin`, so the hub warns |

## Repairs

| What happened | Error | Field |
|---|---|---|
| 401, or OCS status 401/997 inside a 200 | `auth` Repair: "rejected the serverinfo token" or "…the username or app password" | `token` or `password` |
| 403 with a username (not an administrator) | `auth` Repair: use an admin's app password, or a token | `username` |
| 403 with a token | `auth` Repair | `token` |
| 404 (the Monitoring app is disabled, or the URL is wrong) | `api_changed` Repair saying both | `url` |
| An answer that isn't OCS JSON (a login page, a proxy) | `api_changed` Repair | `url` |
| Another OCS status | `api_changed` Repair with Nextcloud's message | |

## Limits

- CPU load and memory are in the answer but not published: they're the host's, and the host's own source reads them better.
- No per-user resources, and nothing from the apps (Talk, Calendar).
- Delegated administrators (Nextcloud's "Administration privileges" for the Monitoring settings) may be allowed in by recent releases; that's from the source, not a live run, so the recipe still says "an administrator".

## Fixtures

Synthetic, in [`testdata/integrations/nextcloud/`](../../testdata/integrations/nextcloud/README.md).
