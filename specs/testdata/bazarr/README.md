# Bazarr fixtures (synthetic)

Synthetic responses for `specs/bazarr.yaml`, written by hand in the shape of Bazarr's API as its source defines it (`bazarr/api/system/status.py`, `bazarr/api/badges/badges.py`, `bazarr/api/system/health.py`). No real instance was contacted; the counts are the Demo Home's (798 episodes and 6 movies missing subtitles), the rest is made up.

The key goes in the `X-API-KEY` header; a wrong one gets a bare 401.

| File | Endpoint | Notes |
|---|---|---|
| `system_status.json` | `GET /api/system/status` | Wrapped in `{"data": …}`: `bazarr_version`, the Sonarr and Radarr versions, platform |
| `system_health.json` | `GET /api/system/health` | Wrapped in `{"data": […]}`: one `{object, issue}` per health issue, as System → Status lists them; empty here (the test sets some) |
| `badges.json` | `GET /api/badges` | What the sidebar badges count: `episodes` and `movies` (missing subtitles, the "Wanted" pages), `providers` (throttled), `status` (health issues), and the Sonarr/Radarr SignalR state (`LIVE` or `DOWN`) |
