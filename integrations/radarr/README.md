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
- **`attrs.queue_items`** (only while the queue isn't empty) and **`attrs.wanted_items`**, for the tile's Queue and Wanted views. A queue item is `{title, detail?, status, progress, left_s?, client?, protocol?, error?}`: the movie as the title, its year as the detail, `status` as the app says it (`downloading`, `queued`, `paused`, `warning`, `error`…), and the reason in `error` when an import is blocked or a download failed. Up to 25 come from `GET /queue?includeSeries…`. A wanted item is `{title, detail?, at?}` (`at` in Unix seconds, left out when unknown), alphabetical, each with the latest release that has happened, from the same `GET /wanted/missing` request that counts them (25 per read, every 10 minutes). The titles are your library's, so they're as visible as the tile is.

| Metric | Unit | Scale | Band | Value |
|---|---|---|---|---|
| `queue` | count | 0–200 | 0–5 | the queue's `totalCount` |
| `wanted` | count | 0–5000 | 0–20 | missing + cutoff unmet (unknown before Radarr 5) |
| `missing.movies` | count | 0–10000 | 0–20 | missing only (not in the Demo Home, where the key is Bazarr's) |

**The queue is `queue`**, as for Sonarr and in the Demo Home. One key for both *arrs is simpler for everything else; the queue tile also reads the older `queued`.

## Grants, repairs, limits

As Sonarr's: one `admin` grant (`Radarr API key`; there are no read-only keys); `auth` on `api_key` for a 401/403; `api_changed` on `url` for a 404 or an answer that isn't JSON; `invalid_config` when the URL is another *arr; `unsupported_version` before Radarr 3. No library counts (`/movie` returns the whole library), monitored movies only, no relations to download clients or indexers.

## Actions

| Action | Tier | Undo | What it does | Verify |
|---|---|---|---|---|
| `radarr.app.search-missing` | T1 | — (a search only looks) | `POST /command` with `{"name": "MissingMoviesSearch"}`: Radarr searches for every monitored movie that's missing | `GET /command/{id}` reads the command back: queued, started or completed passes; failed, aborted or cancelled fails the task. The search itself can run for long and isn't waited for |

- **Preconditions** (`409 precondition`): nothing monitored is missing (`/wanted/missing` totals 0; skipped on versions without it), or the same search is already queued or running.
- **Fewer than 10 at once.** With 10 or more monitored movies missing, the search is `409 unavailable` with `"instead"`: a link to the app's own Missing page (`<url>/wanted/missing`), where you pick what to search for. A search for everything missing can hammer indexers. The listing keeps the action with that sentence, and the sheet, the palette and the error toast offer the link.
- **Credential:** the instance's API key. Radarr has one key with full access and no scopes, so there's no separate control credential to add.
- **Audit:** the target and empty params; the key never appears in a plan, task log or audit record.

## Fixtures

Synthetic, in [`testdata/integrations/radarr/`](../../testdata/integrations/radarr/README.md).
