# Lidarr (`lidarr`)

Reads Lidarr 1 and later through its API v1: the download queue, what's wanted, Lidarr's own health checks, and the albums releasing this week for the media calendar.

**Tier:** silver. **Capability:** `media.calendar`. **Secret:** `api_key`.

It is the same instance as [Sonarr](../sonarr/README.md) and [Radarr](../radarr/README.md), from [`integrations/internal/arr`](../internal/arr); only the API path, the calendar and the metric names differ. Read Sonarr's README for the shared parts; this page lists the differences.

## Config

```json
{"url": "https://lidarr.home.arpa/lidarr", "tls": {"mode": "verify"}, "api_key": "${secret:instance/<id>/api_key}"}
```

`url` includes Lidarr's URL base, if it has one. `api_key` is Settings → General → Security → API Key, sent as `X-Api-Key`. This is the shape the Homepage importer writes from a `lidarr` widget.

## What it reads

Under `<url>/api/v1`, all `GET`: `/system/status`, `/wanted/missing` and `/wanted/cutoff` (one-record pages, `monitored=true`), and `/calendar?start=…&end=…&includeArtist=true` on the slow cadence (the first sync, then at most every 10 minutes while watched); `/health` and `/queue/status` on every sync. **Poll plan:** every 60 s while watched, every 5 minutes otherwise, 30 s per sync.

## What it publishes

- **`lidarr:<instance id>/app`**, kind `app`, `attrs.type = "lidarr"`, `attrs.url`, `attrs.version`, a link, `caps: ["media.calendar"]`, monitored by `integration:<instance id>`. Health as Sonarr's: `attention` on a health check error, else on a download error in the queue.
- **`attrs.calendar`:** `[{"at", "title", "detail"}]`, soonest first, at most 30, from 12 hours ago to 7 days ahead. The artist is the title and the album the detail, with its type when it isn't an album (`Night Ferries · EP`, `… · single`). Lidarr gives release dates at midnight UTC, so `at` is midnight. Albums without a release date or an artist are left out.

| Metric | Unit | Scale | Band | Value |
|---|---|---|---|---|
| `queue` | count | 0–200 | 0–5 | the queue's `totalCount` (an empty queue is 0, never unknown) |
| `wanted` | count | 0–5000 | 0–20 | missing + cutoff unmet albums, Lidarr's Wanted menu |
| `missing.albums` | count | 0–10000 | 0–20 | missing only. **New key**: not in the Demo Home |

## Grants, repairs, limits

As Sonarr's: one `admin` grant (`Lidarr API key`; there are no read-only keys); `auth` on `api_key` for a 401/403; `api_changed` on `url` for a 404 or an answer that isn't JSON (usually a missing URL base); `invalid_config` when the URL is another *arr ("that URL is Readarr, not Lidarr"); `unsupported_version` before Lidarr 1. No library counts (`/artist` returns the whole library), monitored albums only, no relations to download clients or indexers.

## Fixtures

Synthetic, in [`testdata/integrations/lidarr/`](../../testdata/integrations/lidarr/README.md).
