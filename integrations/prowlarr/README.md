# Prowlarr (`prowlarr`)

Reads Prowlarr through its API v1: which enabled indexers are failing, the grabs it has made, the last day's queries and failures, and Prowlarr's health checks.

**Tier:** silver. **Capability:** `downloads.indexers`. **Secret:** `api_key`.

The client, schema, status and health reads are shared with Sonarr and Radarr in [`integrations/internal/arr`](../internal/arr).

## Config

```json
{"url": "https://jupiter.lab.example/prowlarr", "tls": {"mode": "verify"}, "api_key": "${secret:instance/<id>/api_key}"}
```

`url` includes Prowlarr's URL base, if it has one. `api_key` is Settings → General → Security → API Key, sent as `X-Api-Key`, never in the URL.

## What it reads

All `GET`, under `<url>/api/v1`:

| Endpoint | When | For |
|---|---|---|
| `/system/status` | slow | version (`appName` must be Prowlarr) |
| `/indexerstats` | slow | all-time grabs, summed over indexers |
| `/indexerstats?startDate=<now − 24 h>&endDate=<now>` | slow | the last day's queries and failures |
| `/health` | every sync | Prowlarr's health checks |
| `/indexer` | every sync | the indexers and whether they're enabled |
| `/indexerstatus` | every sync | the indexers Prowlarr has seen failing, and until when it disabled them |

**Poll plan:** every 60 s while watched, every 5 minutes otherwise, 30 s per sync. The slow reads run on the first sync, then at most every 10 minutes while watched. A 404 from `/indexerstats` (an old version) leaves its metrics unknown.

## What it publishes

- **`prowlarr:<instance id>/app`**, kind `app`, `attrs.type = "prowlarr"`, `attrs.url`, `attrs.version`, `attrs.indexers` (how many are enabled), `attrs.indexers_failing` (their names, while any fail), a link, `caps: ["downloads.indexers"]`. `integration:<instance id> monitors` it.
- **Health:** `attention` when a health check fails with type `error`; else `attention` when an enabled indexer is failing, with the reason `Archive Tracker is failing (1 of 3 indexers)` (up to three names, then "and N more"); else `ok`. An indexer is failing when it has a status entry that's disabled until later or has a recent failure. Disabled indexers don't count.

| Metric | Unit | Scale | Band | Value |
|---|---|---|---|---|
| `grabs` | count | 0–10⁷ | 0–2000 | all-time grabs, a running total (as in the Demo Home) |
| `queries.24h` | count | 0–10⁵ | 20–80 | searches plus RSS queries in the last 24 h |
| `failures.24h` | count | 0–10⁴ | 0–2 | failed searches, RSS queries, grabs and logins in the last 24 h |
| `indexers` | count | 0–1000 | 0–1000 | enabled indexers (not in the Demo Home) |
| `indexers.failing` | count | 0–1000 | 0–0, warn 1 | enabled indexers failing (not in the Demo Home) |

## Grants

One grant, `Prowlarr API key`, access `admin`: Prowlarr keys can do everything, and there are no read-only ones. The hub warns about it on Test.

## Repairs and hints

As Sonarr's: `auth` on `api_key` for a 401 or 403; `api_changed` on `url` for a 404 or an answer that isn't JSON (check the URL base); `invalid_config` when the URL is another *arr; `unsupported_version` before Prowlarr 1.

## Limits

- No per-indexer resources: the indexers live in `attrs` and the metrics. Per-indexer health tiles would want `indexer` resources; the widget catalog decides (wave 6).
- No relations to the apps Prowlarr syncs (Sonarr, Radarr): the correlator joins those.
- `failures.24h` counts every kind of failure; Prowlarr's own UI splits them.

## Fixtures

Synthetic, in [`testdata/integrations/prowlarr/`](../../testdata/integrations/prowlarr/README.md).
