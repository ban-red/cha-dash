# Radarr API fixture (synthetic)

**Synthetic.** Nothing here was captured from a real Radarr. The responses were written by hand from Radarr's public API v3 (the OpenAPI document and the controllers in the [Radarr repository](https://github.com/Radarr/Radarr/tree/develop/src/Radarr.Api.V3)), in the shape Radarr 5 returns, trimmed to the fields the integration reads plus a few for realism. Movie titles and ids are made up.

The numbers match the Demo Home at 2026-10-03 14:05 UTC (an empty queue, wanted 10); the release dates sit around that moment, the `integrationtest` harness clock.

| File | Endpoint | Notes |
|---|---|---|
| `system_status.json` | `GET /api/v3/system/status` | Radarr 5.26, URL base `/radarr` |
| `health.json` | `GET /api/v3/health` | No problems |
| `queue_status.json` | `GET /api/v3/queue/status` | Empty |
| `queue_status_error.json` | `GET /api/v3/queue/status` | Two queued, one with an error |
| `wanted_missing.json` | `GET /api/v3/wanted/missing?page=1&pageSize=25&monitored=true` | `totalRecords` 7 (Radarr 5 and later) |
| `wanted_cutoff.json` | `GET /api/v3/wanted/cutoff?page=1&pageSize=1&monitored=true` | `totalRecords` 3 |
| `calendar.json` | `GET /api/v3/calendar?start=…&end=…` | 3 movies: a digital release inside the window (its cinema and physical dates outside it), a cinema release at the window's end, and a physical release the day before (outside it) |
