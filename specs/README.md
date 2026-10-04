# Bundled Tier 1 specs

Each `<id>.yaml` here is a Tier 1 integration: a YAML spec the engine in [`internal/spec`](../internal/spec) runs, embedded and registered at init by this package. The format (`chadash/v1alpha1`) is internal and unstable until v1.0; it's documented in [`internal/README.md`](../internal/README.md#integrations) ("Tier 1 specs"), and [`seerr.yaml`](seerr.yaml) is the annotated sample.

Every spec publishes one resource, `<id>:<instance id>/app`, kind `app`, named after the instance, state `running`, with `attrs.type` = the spec's ID, a link to the configured URL, and `integration:<instance id> monitors` it. Its metrics are series on that resource. All fixtures under `testdata/<id>/` are **synthetic**, written from the upstream's API docs or source; each folder's README says which.

The engine adds the shared behaviour: a 401 or 403 is an auth Repair on the secret, a 404 or 410 is an `api_changed` Repair naming the detected version, unless one of the spec's repairs already holds on what was read before the 404 (that's how an old release without an endpoint gets `unsupported_version`). Secrets in query strings (Tautulli) are allowed, and never appear in errors or logs.

| Spec | Config (secret) | Auth | Requests | Version | Metrics | Attrs | Repairs |
|---|---|---|---|---|---|---|---|
| [`seerr`](seerr.yaml) | `url` (`api_key`) | `X-Api-Key` | `/api/v1/status`, `/api/v1/request/count` | `status.version` | `requests.pending`, `requests.processing`, `requests.available` | `version`, `update_available` | 0.x: unsupported |
| [`tautulli`](tautulli.yaml) | `url` (`api_key`) | `apikey` query parameter | `/api/v2?cmd=get_tautulli_info`, `get_activity`, `server_status` (optional) | `tautulli_version`, without the `v` | `streams`, `streams.transcode`, `bandwidth.mbps` | `version`, `branch`, `install_type`, `plex_connected` | an error inside a 200 (`auth` when it's the key, else `api_changed`); 1.x: unsupported |
| [`bazarr`](bazarr.yaml) | `url` (`api_key`) | `X-API-KEY` | `/api/system/status`, `/api/badges` | `bazarr_version`, without a `v` | `wanted`, `missing.episodes`, `missing.movies` | `version`, `sonarr_version`, `radarr_version`, `sonarr_signalr`, `radarr_signalr`, `health_issues`, `throttled_providers` | 0.x: unsupported |
| [`mealie`](mealie.yaml) | `url` (`token`) | `Authorization: Bearer` | `/api/app/about` (public), `/api/households/statistics` | `about.version`, without a `v` | `recipes` | `version`, `recipes`, `categories`, `tags`, `tools` | 1.x (and 0.x): unsupported |
| [`linkding`](linkding.yaml) | `url` (`token`) | `Authorization: Token` | `/health` (public), `/api/bookmarks/`, `/api/bookmarks/archived/`, `/api/tags/` (each `limit=1`) | `/health`'s `version` | `bookmarks` | `version`, `archived`, `tags` | none of its own |
| [`karakeep`](karakeep.yaml) | `url` (`api_key`) | `Authorization: Bearer` | `/api/version` (public, optional), `/api/v1/users/me/stats` | `/api/version`, unless `unknown` | `bookmarks` | `version`, `favorites`, `archived`, `tags`, `lists`, `highlights` | before 0.23: unsupported |

The config shapes are the Homepage importer's ([07 · v0.1](../docs/plan/07-v0.1.md#instance-config-keys)): `url`, `tls` and one secret.

## Notes per spec

**Tautulli.** Polls every 30 s: it's the "what's playing" tile. `streams` is `stream_count` (a string in Tautulli's answer), `streams.transcode` is `stream_count_transcode`, and `bandwidth.mbps` is `total_bandwidth` (kbps) in Mb/s. `streams` uses Plex's scale from the Demo Home (0–10, band 0–3). Health is `attention` when `server_status` says Tautulli has lost Plex; that request is optional, so a Tautulli without it still reads. Capability `media.sessions`, like the Demo Home's Tautulli.

**Bazarr.** `missing.episodes` and `missing.movies` are the Demo Home's keys: Bazarr's sidebar badges, the subtitles still wanted, summed over episodes and movies. `wanted` is their sum, the key the *arr tile reads. Health is `attention` with health issues (System → Status) or throttled providers, the reason naming which. Capability `media.library`, like the Demo Home's Bazarr.

**Mealie.** Reads the token's household (Mealie 2.0 and later count per household). `recipes` is a series; `recipes`, `categories` and `tags` are also attrs, as in the Demo Home. Polls every 5 minutes. On a 1.x server `/api/households/statistics` is a 404, and the repair turns it into "Mealie v1.12.0 is older than this spec supports".

**Linkding.** Counts the token's user's bookmarks: `bookmarks` is unarchived plus archived (Karakeep's count includes archived ones too), with `archived` and `tags` as attrs. Lists are paginated, so each request asks for one result and reads `count`. The Demo Home has no Linkding series (it's only checked there); `bookmarks` is Karakeep's key. Polls every 5 minutes.

**Karakeep.** `bookmarks` is the key's user's `numBookmarks` (the Demo Home's key and scale). `/api/version` is optional because older releases don't have it; without it the version is unknown and the unsupported-version repair can't fire, so a pre-0.23 server shows `api_changed` instead. Polls every 5 minutes.

## Limits

- Grants aren't read back for specs (deferred; see `internal/README.md`). None of these upstreams offers a read-only key, so every credential here can change things even though Cha Dash only reads.
- Counts are per user or household where the upstream scopes them so (Mealie, Linkding, Karakeep): an instance shows what its credential's owner sees.
