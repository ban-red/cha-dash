# Sonarr API fixture (synthetic)

**Synthetic.** Nothing here was captured from a real Sonarr. The responses were written by hand from Sonarr's public API v3 (the OpenAPI document and the controllers in the [Sonarr repository](https://github.com/Sonarr/Sonarr/tree/develop/src/Sonarr.Api.V3)), in the shape Sonarr 4.0 returns, trimmed to the fields the integration reads plus a few for realism. Series and episode titles, ids and paths are made up; hosts are `*.lab.example`.

The numbers match the Demo Home at 2026-10-03 14:05 UTC (queue 10, wanted 25), and the calendar dates sit around that moment, which is the `integrationtest` harness clock.

| File | Endpoint | Notes |
|---|---|---|
| `system_status.json` | `GET /api/v3/system/status` | Sonarr 4.0.15, URL base `/sonarr` |
| `health.json` | `GET /api/v3/health` | One warning (an update is available): the app stays `ok` |
| `health_error.json` | `GET /api/v3/health` | Two errors and a warning: the app is `attention` |
| `queue_status.json` | `GET /api/v3/queue/status` | 10 queued, warnings, no errors |
| `wanted_missing.json` | `GET /api/v3/wanted/missing?page=1&pageSize=1&monitored=true` | `totalRecords` 21 |
| `wanted_cutoff.json` | `GET /api/v3/wanted/cutoff?page=1&pageSize=1&monitored=true` | `totalRecords` 4 |
| `calendar.json` | `GET /api/v3/calendar?start=…&end=…&includeSeries=true` | 7 episodes: one aired before the window, one after it, one with no air time (TBA), a season premiere, a season finale, a series premiere, and an episode whose title is "TBA" (that one is unmonitored, so the calendar marks it). A real Sonarr returns only the requested window; the extras test the integration's own filtering |
