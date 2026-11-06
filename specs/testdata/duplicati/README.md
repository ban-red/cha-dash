# Duplicati fixtures

**Synthetic.** Responses for `specs/duplicati.yaml`, written by hand from Duplicati v2.4.1.0's source ([duplicati/duplicati](https://github.com/duplicati/duplicati)): the routes in `Duplicati/WebserverCore/Endpoints/V1` (`Backups.cs`, `SystemInfo.cs`, `Auth.cs` for the forever token), the DTOs in `WebserverCore/Dto` (JSON with C# names: `PropertyNamingPolicy = null`), the list in `WebserverCore/Services/BackupListService.cs`, and the metadata keys and their `yyyyMMddTHHmmssK` dates in `Library/RestAPI/Runner.cs`. No real instance was contacted; names, buckets and sizes are made up, and passwords are masked as Duplicati masks them. Times are for 2026-11-06 06:00 UTC; the fixture test moves them to now.

| File | Request | Notes |
|---|---|---|
| `systeminfo.json` | `GET /api/v1/systeminfo` | Version 2.4.1.0 |
| `backups.json` | `GET /api/v1/backups` | Four jobs: a good daily one, a weekly one whose last error is newer than its last backup, a daily one with an old error and nothing new for days, and one with no schedule that never ran |

Not checked against a live Duplicati: whether a schedule's `Time` comes with a zone (the spec reads it as UTC without one) and that it holds the next run once a run finished.
