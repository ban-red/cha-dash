# Sonarr (`sonarr`)

Reads Sonarr v3 and v4 through its API v3: the download queue, what's wanted, Sonarr's own health checks, and the episodes airing this week for the media calendar.

**Tier:** silver (version detection, repairs that name the field, fixture tests). **Capability:** `media.calendar`. **Secret:** `api_key`.

The *arr plumbing (client, schema, status, health, paging, calendar shape, the app resource) is shared with Radarr and Prowlarr in [`integrations/internal/arr`](../internal/arr).

## Config

```json
{"url": "https://jupiter.lab.example/sonarr", "tls": {"mode": "verify"}, "api_key": "${secret:instance/<id>/api_key}"}
```

| Key | Meaning |
|---|---|
| `url` | Where Sonarr is, **with its URL base** if it has one (`/sonarr` behind a path-prefix proxy) |
| `tls` | The usual `{"mode", "pin"}` |
| `api_key` | Settings → General → Security → API Key. Write-only, kept in the vault, sent as `X-Api-Key` (never in the URL) |

This is the shape the Homepage importer writes.

## What it reads

All `GET`, under `<url>/api/v3`:

| Endpoint | When | For |
|---|---|---|
| `/system/status` | slow | version (`appName` must be Sonarr) |
| `/wanted/missing?page=1&pageSize=1&monitored=true` | slow | `totalRecords`: monitored episodes that aired without a file |
| `/wanted/cutoff?page=1&pageSize=1&monitored=true` | slow | `totalRecords`: episodes below their quality cutoff |
| `/calendar?start=…&end=…&includeSeries=true` | slow | the next 7 days (and the last 12 hours), monitored episodes only |
| `/health` | every sync | Sonarr's health checks |
| `/queue/status` | every sync | `totalCount`, and whether any download has an error |

**Poll plan:** every 60 s while someone watches, every 5 minutes while nobody does. The slow reads run on the first sync, then at most every 10 minutes and only while watched; in between their last values are published again. Idle syncs read only `/health` and `/queue/status`. Each sync is bounded at 30 s.

## What it publishes

- **`sonarr:<instance id>/app`**, kind `app`, named after the instance, state `running`, with `attrs.type = "sonarr"`, `attrs.url`, `attrs.version`, a link to the URL and `caps: ["media.calendar"]`. `integration:<instance id> monitors` it.
- **Health:** `ok`, or `attention` when a health check fails with type `error` (the reason is `Sonarr: <first message> (and N more)`), or else when a download in the queue has an error. Health warnings (an update is available, say) leave it `ok`.
- **`attrs.calendar`:** the Demo Home's shape, a list of `{"at": RFC 3339, "title", "detail"}`, soonest first, at most 30, inside the window from 12 hours ago to 7 days ahead (re-filtered on every sync, so aired episodes drop off between reads). `title` is the series, `detail` the episode: `S02E05 · <episode title>`, plus `season premiere`, `series premiere`, `season finale`, `midseason finale` or `series finale` where Sonarr says so. Episodes without an air time (TBA) are left out. The web's "Up next" tile reads it as is, and the importer binds calendar tiles to it through `media.calendar`.
- **`attrs.queue_items`** (only while the queue isn't empty) and **`attrs.wanted_items`**, for the tile's Queue and Wanted views. A queue item is `{title, detail?, status, progress, left_s?, client?, protocol?, error?}`: the series as the title, the episode (`S02E01 · Title`) as the detail, `status` as the app says it (`downloading`, `queued`, `paused`, `warning`, `error`…), and the reason in `error` when an import is blocked or a download failed. Up to 25 come from `GET /queue?includeSeries…`. A wanted item is `{title, detail?, at?}` (`at` in Unix seconds, left out when unknown), newest first by air date, from the same `GET /wanted/missing` request that counts them (25 per read, every 10 minutes). The titles are your library's, so they're as visible as the tile is.

| Metric | Unit | Scale | Band | Value |
|---|---|---|---|---|
| `queue` | count | 0–200 | 0–15 | the queue's `totalCount` (an empty queue is 0, never unknown) |
| `wanted` | count | 0–5000 | 0–40 | missing + cutoff unmet, Sonarr's Wanted menu |
| `missing.episodes` | count | 0–100000 | 0–40 | missing only (not in the Demo Home, where the key is Bazarr's) |

## Grants

Sonarr API keys have full access and there are no read-only keys, so Test reports one grant, `Sonarr API key`, access `admin`, and the hub warns that the credential can change things. Nothing here writes.

## Repairs and hints

| What happened | Error | Field |
|---|---|---|
| 401 or 403 | `auth` Repair: "Sonarr rejected the API key" | `api_key` |
| 404 on an endpoint | `api_changed` Repair: check the URL (with its URL base) and that it's Sonarr 3 or later | `url` |
| An answer that isn't JSON (a login page, the wrong path) | `api_changed` Repair: check the URL and its URL base | `url` |
| `/system/status` says it's another app | `invalid_config`: "that URL is Radarr, not Sonarr" | `url` |
| Version 2 or older | `unsupported_version` Repair | |

TLS refusals, timeouts and refused connections are the framework's.

## Actions

| Action | Tier | Undo | What it does | Verify |
|---|---|---|---|---|
| `sonarr.app.search-missing` | T1 | — (a search only looks) | `POST /command` with `{"name": "MissingEpisodeSearch"}`: Sonarr searches for every monitored episode that's missing | `GET /command/{id}` reads the command back: queued, started or completed passes; failed, aborted or cancelled fails the task. The search itself can run for long and isn't waited for |

- **Preconditions** (`409 precondition`): nothing monitored is missing (`/wanted/missing` totals 0; skipped on versions without it), or the same search is already queued or running.
- **Fewer than 10 at once.** With 10 or more monitored episodes missing, the search is `409 unavailable` with `"instead"`: a link to the app's own Missing page (`<url>/wanted/missing`), where you pick what to search for. A search for everything missing can hammer indexers. The listing keeps the action with that sentence, and the sheet, the palette and the error toast offer the link.
- **Credential:** the instance's API key. Sonarr has one key with full access and no scopes, so there's no separate control credential to add.
- **Audit:** the target and empty params; the key never appears in a plan, task log or audit record.

## Limits

- No library counts (series, episodes on disk): `/series` returns every series whole, too big to poll. So no `media.library` yet.
- The calendar shows monitored episodes only; the importer's `unmonitored` option on calendar tiles isn't honoured until tiles get their own config (wave 6).
- Dependencies on download clients and indexers (`depends_on` qBittorrent, Prowlarr) aren't published: Sonarr names them by host, and joining them is the correlator's job.
- Health check problems don't open inbox incidents; they show on the app's health.

## Fixtures

Synthetic, in [`testdata/integrations/sonarr/`](../../testdata/integrations/sonarr/README.md).
