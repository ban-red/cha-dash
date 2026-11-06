# Backrest fixtures

**Synthetic.** Responses for `specs/backrest.yaml`, written by hand from Backrest v1.14.1's source ([garethgeorge/backrest](https://github.com/garethgeorge/backrest)): the Connect service in `proto/v1/service.proto`, the config and operation messages in `proto/v1/config.proto` and `proto/v1/operations.proto` (protojson: camelCase names, int64 as strings, zero values left out), the summary built in `internal/api/summarydashboard.go`, and basic auth in `internal/auth/middleware.go`. No real instance was contacted; names, paths and sizes are made up. Times are for 2026-11-06 06:00 UTC, the clock the fixture test and `fixtures.yaml` (`now`) set.

| File | Request | Notes |
|---|---|---|
| `config.json` | `POST /v1.Backrest/GetConfig` | Four plans: a nightly cron, a weekly one, an hourly one and one with its schedule off; the config as Backrest sends it, sanitized |
| `summary.json` | `POST /v1.Backrest/GetSummaryDashboard` | Per plan: recent backups newest first (one failed, one running), the next run, the size, and the day buckets with Backrest's overdue flag (the hourly plan is overdue) |
| `operations.json` | `POST /v1.Backrest/GetOperations` (`lastN` 100) | The failed backup's message, a forget that failed after it, and the running backup |

Not checked against a live Backrest: the exact JSON of an empty schedule (`{}` or left out) and of a running backup in the summary's chart.
