# Lidarr API fixture (synthetic)

**Synthetic.** Nothing here was captured from a real Lidarr. The responses were written by hand from Lidarr's public API v1 (the controllers and resources in the [Lidarr repository](https://github.com/Lidarr/Lidarr/tree/develop/src/Lidarr.Api.V1)), in the shape Lidarr 2 returns, trimmed to the fields the integration reads plus a few for realism. Artists, albums and ids are made up.

The release dates sit around 2026-10-03 14:05 UTC, the `integrationtest` harness clock.

| File | Endpoint | Notes |
|---|---|---|
| `system_status.json` | `GET /api/v1/system/status` | Lidarr 2.13, URL base `/lidarr` |
| `health.json` | `GET /api/v1/health` | An update warning only, which leaves health `ok` |
| `queue_status.json` | `GET /api/v1/queue/status` | One download, no errors |
| `wanted_missing.json` | `GET /api/v1/wanted/missing?page=1&pageSize=25&monitored=true` | `totalRecords` 14 |
| `wanted_cutoff.json` | `GET /api/v1/wanted/cutoff?page=1&pageSize=1&monitored=true` | `totalRecords` 2 |
| `calendar.json` | `GET /api/v1/calendar?start=…&end=…&includeArtist=true` | 3 albums: a single the day before the window (outside it), an EP and an album inside it |
