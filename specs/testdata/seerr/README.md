# Seerr fixtures

Synthetic responses for `specs/seerr.yaml`, written by hand in the shape of Seerr's API, which Overseerr and Jellyseerr share. No real instance was contacted; the numbers are made up.

| File | Endpoint |
|---|---|
| `status.json` | `GET /api/v1/status` (a Jellyseerr 2.x-style version) |
| `status_overseerr.json` | `GET /api/v1/status` as Overseerr 1.33 answers it, with an update available |
| `request_count.json` | `GET /api/v1/request/count` (needs `X-Api-Key`) |
| `request_pending.json` | `GET /api/v1/request?take=20&skip=0&filter=pending&sort=added`: two pending requests (a movie, a TV season) by made-up users |
| `request_42.json`, `request_43.json` | `GET /api/v1/request/{id}`: those two requests; the contract test sets `status` as approve and decline change it |
| `settings_sonarr.json`, `settings_radarr.json` | `GET /api/v1/settings/sonarr`, `…/radarr`: one Sonarr, a Radarr and a 4K Radarr (https, URL base). Fake API keys, never read (`uses` carries addresses only) |
| `settings_plex.json` | `GET /api/v1/settings/plex`: the media server. Jellyfin's settings aren't in the fixtures, so they answer 404, as when Plex is the media server |

Approve and decline are `POST /api/v1/request/{id}/approve` and `…/decline`; the fake answers them and changes the request's status (2 approved, 3 declined).
