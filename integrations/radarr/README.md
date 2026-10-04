# Radarr (`radarr`)

Reads Radarr v3 and later through its API v3: the download queue, what's wanted (from Radarr 5), Radarr's own health checks, and the release dates falling this week for the media calendar.

**Tier:** silver. **Capability:** `media.calendar`. **Secret:** `api_key`.

It is the same instance as [Sonarr](../sonarr/README.md), from [`integrations/internal/arr`](../internal/arr); only the calendar and the metric names differ. Read Sonarr's README for the shared parts; this page lists the differences.

## Config

```json
{"url": "https://jupiter.lab.example/radarr", "tls": {"mode": "verify"}, "api_key": "${secret:instance/<id>/api_key}"}
```

`url` includes Radarr's URL base, if it has one. `api_key` is Settings → General → Security → API Key, sent as `X-Api-Key`.

## What it reads

Under `<url>/api/v3`, all `GET`: `/system/status`, `/wanted/missing` and `/wanted/cutoff` (one-record pages, `monitored=true`), and `/calendar?start=…&end=…` on the slow cadence (the first sync, then at most every 10 minutes while watched); `/health` and `/queue/status` on every sync. **Poll plan:** every 60 s while watched, every 5 minutes otherwise, 30 s per sync.

**Radarr 3 and 4** have no `/wanted` endpoints. A 404 there leaves `wanted` and `missing.movies` unknown (shown as `—`) instead of failing the sync. Everything else works.

## What it publishes

- **`radarr:<instance id>/app`**, kind `app`, `attrs.type = "radarr"`, `attrs.url`, `attrs.version`, a link, `caps: ["media.calendar"]`, monitored by `integration:<instance id>`. Health as Sonarr's: `attention` on a health check error, else on a download error in the queue.
- **`attrs.calendar`:** `[{"at", "title", "detail"}]`, soonest first, at most 30, from 12 hours ago to 7 days ahead. Radarr's calendar returns movies with any release date in the window; each date inside it becomes an entry, with the movie as the title and `in cinemas`, `digital release` or `physical release` as the detail. Radarr gives these as dates at midnight UTC, so `at` is midnight.

| Metric | Unit | Scale | Band | Value |
|---|---|---|---|---|
| `queue` | count | 0–200 | 0–5 | the queue's `totalCount` |
| `wanted` | count | 0–5000 | 0–20 | missing + cutoff unmet (unknown before Radarr 5) |
| `missing.movies` | count | 0–10000 | 0–20 | missing only (not in the Demo Home, where the key is Bazarr's) |

**The queue is `queue`**, as for Sonarr. The Demo Home calls Radarr's `queued`; the queue tile reads either, and one key for both *arrs is simpler for everything else.

## Grants, repairs, limits

As Sonarr's: one `admin` grant (`Radarr API key`; there are no read-only keys); `auth` on `api_key` for a 401/403; `api_changed` on `url` for a 404 or an answer that isn't JSON; `invalid_config` when the URL is another *arr; `unsupported_version` before Radarr 3. No library counts (`/movie` returns the whole library), monitored movies only, no relations to download clients or indexers.

## Fixtures

Synthetic, in [`testdata/integrations/radarr/`](../../testdata/integrations/radarr/README.md).
