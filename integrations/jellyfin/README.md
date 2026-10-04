# Jellyfin (`jellyfin`)

Reads a Jellyfin server: how many streams are playing (and how many of them transcode), and how big the libraries are. It feeds the Now playing tile the way Plex does.

**Tier:** silver, Tier 0 (Go). **Capabilities:** `media.sessions`, `media.library`. **Secret:** `api_key`.

**Why Tier 0 and not a spec:** the stream count is the sessions that are *playing something* (a session can be open with nothing playing), and the transcode count filters those again; the spec engine extracts values with gjson paths and doesn't count filtered arrays. Go also gets the Emby check and the version gate.

## Config

```json
{"url": "https://jellyfin.lab.example", "tls": {"mode": "verify"}, "api_key": "${secret:instance/<id>/api_key}"}
```

| Key | Meaning |
|---|---|
| `url` | The server, with its base URL if it has one. The web app's address (`…/web/`) works too: a trailing `/web` is dropped |
| `tls` | The usual `{"mode", "pin"}` |
| `api_key` | Dashboard → API Keys → New API key. Write-only, kept in the vault |

The key goes in `Authorization: MediaBrowser Token="<key>"` on every request, never in the URL. `X-Emby-Token` and the `api_key` query parameter are Jellyfin's legacy forms, which newer servers turn off (`EnableLegacyAuthorization`), so they aren't sent.

## What it reads

All `GET`, with `Accept: application/json`:

| Endpoint | When | For |
|---|---|---|
| `/System/Info` | slow | version, server id and name, product name (must be Jellyfin), pending restart |
| `/Items/Counts` | slow | library counts |
| `/Sessions?activeWithinSeconds=960` | every sync | what's playing (the window Jellyfin's dashboard uses) |

**Poll plan:** every 30 s while watched, every 5 minutes otherwise, 30 s per sync. The slow reads run on the first sync, then every 30 minutes while watched.

## What it publishes

- **`jellyfin:<instance id>/app`**, kind `app`, named after the instance, state `running`, health `ok`, with:
  - `attrs.type = "jellyfin"`, `attrs.url` (the server root), `attrs.version`;
  - `attrs.server_id` (Jellyfin's server id, the natural key for the correlator) and `attrs.server_name`;
  - `attrs.pending_restart` (an update or plugin is waiting for a restart);
  - library counts as attrs, from `/Items/Counts`: `movies`, `shows` (series), `episodes`, `albums`, `songs`, `books`. `movies` and `shows` are the keys Plex and the Demo Home use;
  - a link to the configured URL, and `caps: ["media.sessions", "media.library"]`.
- `integration:<instance id> monitors` it.

| Metric | Unit | Scale | Band | Value |
|---|---|---|---|---|
| `streams` | count | 0–10 | 0–3 | sessions with a `NowPlayingItem`, playing or paused (Plex's key and scale) |
| `streams.transcode` | count | 0–10 | 0–2 | of those, `PlayState.PlayMethod == "Transcode"` (Tautulli's key) |

Nothing playing is 0, never unknown.

## Grants

Jellyfin's API keys act as an administrator and there are no read-only ones, so Test reports one grant, `Jellyfin API key`, access `admin`, and the hub warns about it.

## Repairs

| What happened | Error | Field |
|---|---|---|
| 401 or 403 | `auth` Repair: "Create one under Dashboard → API Keys" | `api_key` |
| 404, or an answer that isn't JSON | `api_changed` Repair: check that the URL is a Jellyfin server | `url` |
| `ProductName` isn't Jellyfin (Emby answers the same paths) | `api_changed` Repair: "the URL answers as Emby Server, not Jellyfin" | `url` |
| `/System/Info` without an id or version | `api_changed` Repair | `url` |
| Older than 10.8 | `unsupported_version` Repair | |

## Limits

- No per-stream detail (who, what, which device): the tile shows counts, and viewers' names are personal. The sessions answer has them; they aren't published.
- Health is `ok` whenever the server answers. An unreachable server is the framework's (series go stale, the app keeps its last state).
- Recently added items, scheduled tasks and library scans aren't read.

## Fixtures

Synthetic, in [`testdata/integrations/jellyfin/`](../../testdata/integrations/jellyfin/README.md).
