# Audiobookshelf (`audiobookshelf`)

Reads Audiobookshelf 2.x through its API: how many books and podcasts its libraries hold and how many hours they run, and how many listening sessions are open.

**Tier:** silver. **Capabilities:** `media.library`, `media.sessions`. **Secret:** `api_key`.

**Why Go, not a spec:** the counts come from one request per library (`/api/libraries/<id>/stats`), with the IDs read from the server first. The spec engine has fixed paths only.

## Config

```json
{"url": "https://audiobooks.home.arpa", "tls": {"mode": "verify"}, "api_key": "${secret:instance/<id>/api_key}"}
```

`url` includes Audiobookshelf's path, if it has one. `api_key` is an API key (Settings → API Keys, Audiobookshelf 2.26 and later) or, on older releases, a user's API token; either is sent as `Authorization: Bearer`. This is the shape the Homepage importer writes from an `audiobookshelf` widget.

## What it reads

| Endpoint | When | For |
|---|---|---|
| `GET /status` (no key) | with the libraries | the version, and that it's Audiobookshelf |
| `GET /api/libraries` | the first sync, then every 10 minutes while watched | each library's ID and media type |
| `GET /api/libraries/<id>/stats` | with it, for each library (at most 20) | `totalItems`, `totalDuration` (seconds) |
| `GET /api/users/online` | every sync | `openSessions` (admin only) |
| `GET /api/me` | Test only | the user's type and permissions, for the grant |

Library IDs must look like IDs (letters, digits, `-` and `_`) before they go into a path. **Poll plan:** every 60 s while watched, every 5 minutes otherwise. 30 s per sync.

## What it publishes

- **`audiobookshelf:<instance id>/app`**, kind `app`, named after the instance, state `running`, health `ok`, with `attrs.type = "audiobookshelf"`, `attrs.url`, `attrs.version`, `attrs.libraries` (how many), a link and `caps: ["media.library", "media.sessions"]`. `integration:<instance id> monitors` it.

| Metric | Unit | Scale | Band | Value | |
|---|---|---|---|---|---|
| `streams` | count | 0–10 | 0–3 | open listening sessions; unknown without an admin's key | Plex's key and scale |
| `books` | count | 0–100000 | | items in the book libraries | new key |
| `books.hours` | h | 0–10⁶ | | their total duration in hours, to 0.1 | new key |
| `podcasts` | count | 0–10000 | | podcasts in the podcast libraries | new key |
| `podcasts.hours` | h | 0–10⁶ | | their episodes' total duration in hours, to 0.1 | new key |

A server with no podcast library has 0 podcasts, not unknown.

## Grants

Test reads `/api/me` and reports one grant, `Audiobookshelf user`: `read` for a user without upload, change or delete permissions; `rw` naming what it may do otherwise; `admin` for an admin or the root user. A key without admin rights gets an `info` note that open sessions stay unknown (a limit of a well-scoped key, so not a sync warning).

## Repairs

| What happened | Error | Field |
|---|---|---|
| 401 | `auth` Repair: "Audiobookshelf rejected the API key" | `api_key` |
| 403 on the libraries or `/api/me` | `auth` Repair | `api_key` |
| 403 on `/api/users/online` | none: `streams` is unknown | |
| 404, an answer that isn't JSON, or `/status` that isn't Audiobookshelf's | `api_changed` Repair: check the URL | `url` |
| A library ID that doesn't look like one | `api_changed` Repair | |

## Limits

- No per-item or per-user resources, and no titles: what's playing stays private.
- More than 20 libraries: the first 20 are counted (`attrs.libraries` says how many there are).
- Counts are of the libraries the key's user can see.

## Fixtures

Synthetic, in [`testdata/integrations/audiobookshelf/`](../../testdata/integrations/audiobookshelf/README.md).
