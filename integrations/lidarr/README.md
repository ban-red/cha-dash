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
- **`attrs.queue_items`** (only while the queue isn't empty) and **`attrs.wanted_items`**, for the tile's Queue and Wanted views. A queue item is `{title, detail?, status, progress, left_s?, client?, protocol?, error?}`: the artist as the title, the album as the detail, `status` as the app says it (`downloading`, `queued`, `paused`, `warning`, `error`…), and the reason in `error` when an import is blocked or a download failed. Up to 25 come from `GET /queue?includeSeries…`. A wanted item is `{title, detail?, at?}` (`at` in Unix seconds, left out when unknown), newest first by release date, from the same `GET /wanted/missing` request that counts them (25 per read, every 10 minutes). The titles are your library's, so they're as visible as the tile is.

| Metric | Unit | Scale | Band | Value |
|---|---|---|---|---|
| `queue` | count | 0–200 | 0–5 | the queue's `totalCount` (an empty queue is 0, never unknown) |
| `wanted` | count | 0–5000 | 0–20 | missing + cutoff unmet albums, Lidarr's Wanted menu |
| `missing.albums` | count | 0–10000 | 0–20 | missing only. **New key**: not in the Demo Home |

## Grants, repairs, limits

As Sonarr's: one `admin` grant (`Lidarr API key`; there are no read-only keys); `auth` on `api_key` for a 401/403; `api_changed` on `url` for a 404 or an answer that isn't JSON (usually a missing URL base); `invalid_config` when the URL is another *arr ("that URL is Readarr, not Lidarr"); `unsupported_version` before Lidarr 1. No library counts (`/artist` returns the whole library), monitored albums only, no relations to download clients or indexers.

## Actions

| Action | Tier | Undo | What it does | Verify |
|---|---|---|---|---|
| `lidarr.app.search-missing` | T1 | — (a search only looks) | `POST /command` with `{"name": "MissingAlbumSearch"}`: Lidarr searches for every monitored album that's missing | `GET /command/{id}` reads the command back: queued, started or completed passes; failed, aborted or cancelled fails the task. The search itself can run for long and isn't waited for |

- **Preconditions** (`409 precondition`): nothing monitored is missing (`/wanted/missing` totals 0; skipped on versions without it), or the same search is already queued or running.
- **Credential:** the instance's API key. Lidarr has one key with full access and no scopes, so there's no separate control credential to add.
- **Audit:** the target and empty params; the key never appears in a plan, task log or audit record.

## Fixtures

Synthetic, in [`testdata/integrations/lidarr/`](../../testdata/integrations/lidarr/README.md).
