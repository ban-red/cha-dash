# Prowlarr API fixture (synthetic)

**Synthetic.** Nothing here was captured from a real Prowlarr. The responses were written by hand from Prowlarr's public API v1 (the resources and controllers in the [Prowlarr repository](https://github.com/Prowlarr/Prowlarr/tree/develop/src/Prowlarr.Api.V1), and `IndexerStatistics` in `NzbDrone.Core/IndexerStats`), trimmed to the fields the integration reads plus a few for realism. Indexer names and definitions are made up; the one address is the Demo Home's `10.0.11.120`.

The numbers match the Demo Home at 2026-10-03 14:05 UTC (grabs 1100, queries in the last day 54).

| File | Endpoint | Notes |
|---|---|---|
| `system_status.json` | `GET /api/v1/system/status` | Prowlarr 1.37, URL base `/prowlarr` |
| `health.json` | `GET /api/v1/health` | A warning naming the failing indexer |
| `indexer.json` | `GET /api/v1/indexer` | 4 indexers: 3 enabled (two torrent, one usenet), one switched off |
| `indexerstatus.json` | `GET /api/v1/indexerstatus` | Archive Tracker (enabled) and Retired Tracker (switched off) are backing off after failures |
| `indexerstats.json` | `GET /api/v1/indexerstats` | All time: 1100 grabs across the three |
| `indexerstats_24h.json` | `GET /api/v1/indexerstats?startDate=…&endDate=…` | The last day: 54 queries (searches and RSS), 5 failures, all but one at Archive Tracker |
